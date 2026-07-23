// Package mcpserver exposes rec-deploy's local state through read-only MCP
// tools. It deliberately returns dedicated public views rather than store or
// config values, because those internal types contain credentials.
package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rdcstarr/rec-deploy/internal/buildinfo"
	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/discover"
	"github.com/rdcstarr/rec-deploy/internal/manifest"
	"github.com/rdcstarr/rec-deploy/internal/store"
	"github.com/rdcstarr/rec-deploy/internal/systemd"
	"github.com/rdcstarr/rec-deploy/internal/units"
)

const maxDeploys = 100

// NewToken creates a high-entropy bearer token and its one-way digest. The
// clear token is returned only so the caller can provision it once.
func NewToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate MCP token: %w", err)
	}
	token := "rdmcp_" + base64.RawURLEncoding.EncodeToString(raw)

	return token, TokenHash(token), nil
}

// TokenHash returns the digest persisted for a bearer token.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Server serves the read-only rec-deploy MCP API.
type Server struct {
	config *config.Config
	store  *store.Store
	mcp    *mcp.Server
}

// New creates a read-only MCP server backed by cfg and st.
func New(cfg *config.Config, st *store.Store) *Server {
	s := &Server{config: cfg, store: st}
	s.mcp = mcp.NewServer(&mcp.Implementation{
		Name:    "rec-deploy",
		Version: buildinfo.Resolved(),
	}, nil)
	s.addTools()

	return s
}

// RunStdio serves MCP over stdin and stdout until the client disconnects or ctx
// is cancelled. Callers must keep stdout free of all non-MCP output.
func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// HTTPHandler serves streamable HTTP MCP requests authenticated with the
// configured bearer token digest.
func (s *Server) HTTPHandler(tokenHash string) http.Handler {
	cloudflareMode := s.config.MCP.Mode == "cloudflare"
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.mcp
	}, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		JSONResponse:               true,
		DisableLocalhostProtection: cloudflareMode,
	})
	publicHost := ""
	if publicURL, err := url.Parse(s.config.MCP.PublicURL); err == nil {
		publicHost = publicURL.Hostname()
	}

	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if cloudflareMode && !allowedMCPHost(r.Host, publicHost) {
			http.Error(w, "forbidden: invalid MCP host", http.StatusForbidden)
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) || !sameTokenHash(TokenHash(strings.TrimPrefix(header, prefix)), tokenHash) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		transport.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	protection := http.NewCrossOriginProtection()
	mux.Handle("/mcp", protection.Handler(authenticated))
	return mux
}

func allowedMCPHost(hostport, publicHost string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, publicHost) && publicHost != "" {
		return true
	}
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}

func sameTokenHash(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) addTools() {
	falseValue := false
	annotations := &mcp.ToolAnnotations{
		DestructiveHint: &falseValue,
		IdempotentHint:  true,
		OpenWorldHint:   &falseValue,
		ReadOnlyHint:    true,
	}
	trueValue := true
	statusAnnotations := *annotations
	statusAnnotations.OpenWorldHint = &trueValue

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_repositories",
		Description: "List repositories registered on this rec-deploy server without exposing credentials.",
		Annotations: annotations,
	}, s.listRepositories)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_installations",
		Description: "List discovered deployment checkouts, optionally filtered by repository.",
		Annotations: annotations,
	}, s.listInstallations)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_status",
		Description: "Report daemon health, systemd unit state, auto-update state, and the last deploy per path.",
		Annotations: &statusAnnotations,
	}, s.getStatus)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_deploys",
		Description: "List recent deploys without delivery IDs or captured command output.",
		Annotations: annotations,
	}, s.listDeploys)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_deploy",
		Description: "Get one deploy and its per-path results without captured command output.",
		Annotations: annotations,
	}, s.getDeploy)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "validate_manifest",
		Description: "Validate the manifest of a checkout found in the configured discovery roots.",
		Annotations: annotations,
	}, s.validateManifest)
}

type emptyInput struct{}

type repositoryView struct {
	Repository       string    `json:"repository"`
	DeployKeySet     bool      `json:"deploy_key_set"`
	WebhookSet       bool      `json:"webhook_set"`
	WebhookTokenSet  bool      `json:"webhook_token_set"`
	WebhookSecretSet bool      `json:"webhook_secret_set"`
	CreatedAt        time.Time `json:"created_at"`
}

type repositoriesOutput struct {
	Repositories []repositoryView `json:"repositories"`
}

func (s *Server) listRepositories(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, repositoriesOutput, error) {
	repos, err := s.store.Repos(ctx)
	if err != nil {
		return nil, repositoriesOutput{}, err
	}

	out := make([]repositoryView, 0, len(repos))
	for _, repo := range repos {
		out = append(out, repositoryView{
			Repository: repo.Repository, DeployKeySet: repo.GitHubKeyID != 0,
			WebhookSet: repo.GitHubHookID != 0, WebhookTokenSet: repo.Token != "",
			WebhookSecretSet: repo.Secret != "", CreatedAt: repo.CreatedAt,
		})
	}

	return nil, repositoriesOutput{Repositories: out}, nil
}

type repositoryInput struct {
	Repository string `json:"repository,omitempty" jsonschema:"optional owner/repository filter"`
}

type installationView struct {
	Path         string `json:"path"`
	Repository   string `json:"repository,omitempty"`
	Branch       string `json:"branch,omitempty"`
	User         string `json:"user,omitempty"`
	RanAsRoot    bool   `json:"ran_as_root"`
	RemoteHTTPS  bool   `json:"remote_https"`
	Inconsistent string `json:"inconsistent,omitempty"`
	Error        string `json:"error,omitempty"`
}

type installationsOutput struct {
	Installations []installationView `json:"installations"`
}

func (s *Server) listInstallations(ctx context.Context, _ *mcp.CallToolRequest, input repositoryInput) (*mcp.CallToolResult, installationsOutput, error) {
	found, err := discover.Scan(ctx, discover.Options{Roots: s.config.Discovery.Roots, Prune: s.config.Discovery.Prune})
	if err != nil {
		return nil, installationsOutput{}, err
	}
	if input.Repository != "" {
		found = discover.Filter(found, input.Repository)
	}

	out := make([]installationView, 0, len(found))
	for _, in := range found {
		view := installationView{
			Path: in.Path, Repository: in.Repository, Branch: in.Branch, User: in.User,
			RanAsRoot: in.RanAsRoot, RemoteHTTPS: in.RemoteHTTPS, Inconsistent: in.Inconsistent,
		}
		if in.Err != nil {
			view.Error = in.Err.Error()
		}
		out = append(out, view)
	}

	return nil, installationsOutput{Installations: out}, nil
}

type deploysInput struct {
	Repository string `json:"repository,omitempty" jsonschema:"optional owner/repository filter"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum number of deploys to return, from 1 to 100"`
}

type deployView struct {
	ID         int64      `json:"id"`
	Repository string     `json:"repository"`
	Ref        string     `json:"ref,omitempty"`
	SHA        string     `json:"sha,omitempty"`
	Message    string     `json:"message,omitempty"`
	Author     string     `json:"author,omitempty"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type deploysOutput struct {
	Deploys []deployView `json:"deploys"`
}

func (s *Server) listDeploys(ctx context.Context, _ *mcp.CallToolRequest, input deploysInput) (*mcp.CallToolResult, deploysOutput, error) {
	if input.Limit < 0 || input.Limit > maxDeploys {
		return nil, deploysOutput{}, fmt.Errorf("limit must be between 1 and %d", maxDeploys)
	}
	if input.Repository != "" {
		if _, err := s.store.RepoByName(ctx, input.Repository); err != nil {
			return nil, deploysOutput{}, fmt.Errorf("repository %q: %w", input.Repository, err)
		}
	}

	deploys, err := s.store.Deploys(ctx, input.Repository, input.Limit)
	if err != nil {
		return nil, deploysOutput{}, err
	}
	names, err := s.repositoryNames(ctx)
	if err != nil {
		return nil, deploysOutput{}, err
	}

	out := make([]deployView, 0, len(deploys))
	for _, deploy := range deploys {
		out = append(out, newDeployView(deploy, names[deploy.RepoID]))
	}

	return nil, deploysOutput{Deploys: out}, nil
}

type deployInput struct {
	ID int64 `json:"id" jsonschema:"database ID of the deploy"`
}

type deployPathView struct {
	Path        string `json:"path"`
	User        string `json:"user,omitempty"`
	RanAsRoot   bool   `json:"ran_as_root"`
	PreviousSHA string `json:"previous_sha,omitempty"`
	NewSHA      string `json:"new_sha,omitempty"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
}

type deployOutput struct {
	Deploy deployView       `json:"deploy"`
	Paths  []deployPathView `json:"paths"`
}

func (s *Server) getDeploy(ctx context.Context, _ *mcp.CallToolRequest, input deployInput) (*mcp.CallToolResult, deployOutput, error) {
	deploy, err := s.store.DeployByID(ctx, input.ID)
	if err != nil {
		return nil, deployOutput{}, err
	}
	names, err := s.repositoryNames(ctx)
	if err != nil {
		return nil, deployOutput{}, err
	}
	paths, err := s.store.DeployPaths(ctx, deploy.ID)
	if err != nil {
		return nil, deployOutput{}, err
	}

	out := make([]deployPathView, 0, len(paths))
	for _, path := range paths {
		out = append(out, deployPathView{
			Path: path.Path, User: path.User, RanAsRoot: path.RanAsRoot,
			PreviousSHA: path.PreviousSHA, NewSHA: path.NewSHA, Status: path.Status, Reason: path.Reason,
		})
	}

	return nil, deployOutput{Deploy: newDeployView(deploy, names[deploy.RepoID]), Paths: out}, nil
}

func newDeployView(deploy store.Deploy, repository string) deployView {
	view := deployView{
		ID: deploy.ID, Repository: repository, Ref: deploy.Ref, SHA: deploy.SHA,
		Message: deploy.Message, Author: deploy.Author, Status: deploy.Status, StartedAt: deploy.StartedAt,
	}
	if !deploy.FinishedAt.IsZero() {
		finished := deploy.FinishedAt
		view.FinishedAt = &finished
	}

	return view
}

func (s *Server) repositoryNames(ctx context.Context) (map[int64]string, error) {
	repos, err := s.store.Repos(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(repos))
	for _, repo := range repos {
		names[repo.ID] = repo.Repository
	}

	return names, nil
}

type manifestInput struct {
	Path string `json:"path" jsonschema:"absolute path of a checkout returned by list_installations"`
}

type manifestStepView struct {
	Run               string `json:"run"`
	Timeout           string `json:"timeout"`
	ContinueOnFailure bool   `json:"continue_on_failure"`
}

type manifestOutput struct {
	Path              string             `json:"path"`
	Repository        string             `json:"repository"`
	RollbackOnFailure bool               `json:"rollback_on_failure"`
	PostDeploy        []manifestStepView `json:"post_deploy"`
}

func (s *Server) validateManifest(ctx context.Context, _ *mcp.CallToolRequest, input manifestInput) (*mcp.CallToolResult, manifestOutput, error) {
	wanted, err := filepath.Abs(input.Path)
	if err != nil {
		return nil, manifestOutput{}, err
	}
	wanted = filepath.Clean(wanted)

	found, err := discover.Scan(ctx, discover.Options{Roots: s.config.Discovery.Roots, Prune: s.config.Discovery.Prune})
	if err != nil {
		return nil, manifestOutput{}, err
	}
	allowed := false
	for _, installation := range found {
		if filepath.Clean(installation.Path) == wanted {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, manifestOutput{}, fmt.Errorf("path %q is not a checkout found in the configured discovery roots", wanted)
	}

	m, err := manifest.Load(wanted)
	if err != nil {
		return nil, manifestOutput{}, err
	}
	steps := make([]manifestStepView, 0, len(m.PostDeploy))
	for _, step := range m.PostDeploy {
		steps = append(steps, manifestStepView{
			Run: step.Run, Timeout: step.Timeout.String(), ContinueOnFailure: step.ContinueOnFailure,
		})
	}

	return nil, manifestOutput{
		Path: wanted, Repository: m.Repository, RollbackOnFailure: m.RollbackOnFailure, PostDeploy: steps,
	}, nil
}

type pathStatusView struct {
	Path      string `json:"path"`
	User      string `json:"user,omitempty"`
	RanAsRoot bool   `json:"ran_as_root"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	SHA       string `json:"sha,omitempty"`
}

type statusOutput struct {
	DaemonURL     string           `json:"daemon_url"`
	DaemonHealthy bool             `json:"daemon_healthy"`
	AutoUpdate    bool             `json:"auto_update"`
	Units         []units.Status   `json:"units"`
	Paths         []pathStatusView `json:"paths"`
}

func (s *Server) getStatus(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, statusOutput, error) {
	paths, err := s.store.LastDeployPerPath(ctx)
	if err != nil {
		return nil, statusOutput{}, err
	}
	pathViews := make([]pathStatusView, 0, len(paths))
	for _, path := range paths {
		pathViews = append(pathViews, pathStatusView{
			Path: path.Path, User: path.User, RanAsRoot: path.RanAsRoot,
			Status: path.Status, Reason: path.Reason, SHA: path.NewSHA,
		})
	}

	url := healthURL(s.config)
	return nil, statusOutput{
		DaemonURL: url, DaemonHealthy: daemonHealthy(ctx, url),
		AutoUpdate: systemd.IsEnabled(ctx, "rec-deploy-update.timer"),
		Units:      unitStates(ctx), Paths: pathViews,
	}, nil
}

func unitStates(ctx context.Context) []units.Status {
	if !systemd.Available() {
		return []units.Status{}
	}

	out := make([]units.Status, 0, len(units.Names))
	for _, name := range units.Names {
		status := units.Compare(name, systemd.FragmentPath(ctx, name))
		if systemd.LoadState(ctx, name) == systemd.LoadMasked {
			status.State = units.StateMasked
		}
		out = append(out, status)
	}

	return out
}

func healthURL(cfg *config.Config) string {
	if url := strings.TrimSpace(cfg.PublicURL); url != "" {
		return strings.TrimRight(url, "/") + "/health"
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return "http://" + cfg.Listen + "/health"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	return "http://" + net.JoinHostPort(host, port) + "/health"
}

func daemonHealthy(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}
