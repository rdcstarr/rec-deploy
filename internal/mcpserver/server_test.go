package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/store"
)

func TestHTTPHandlerRequiresBearerToken(t *testing.T) {
	ctx := context.Background()
	st, _, _ := testStore(ctx, t)
	token, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	handler := New(&config.Config{}, st).HTTPHandler(hash)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)

	for _, test := range []struct {
		name   string
		path   string
		token  string
		status int
	}{
		{name: "missing", path: "/mcp", status: http.StatusUnauthorized},
		{name: "wrong", path: "/mcp", token: "wrong", status: http.StatusUnauthorized},
		{name: "valid", path: "/mcp", token: token, status: http.StatusOK},
		{name: "wrong path", path: "/", token: token, status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != test.status {
				t.Errorf("status = %d, want %d; body=%s", res.Code, test.status, res.Body.String())
			}
		})
	}
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func TestHTTPHandlerCompletesMCPHandshake(t *testing.T) {
	ctx := context.Background()
	st, _, _ := testStore(ctx, t)
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(New(&config.Config{}, st).HTTPHandler(hash))
	defer httpServer.Close()
	transport := &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}}, DisableStandaloneSSE: true}
	client := mcp.NewClient(&mcp.Implementation{Name: "production-audit", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 6 {
		t.Fatalf("tools = %d, want 6", len(tools.Tools))
	}
}

func TestCloudflareHandlerAllowsOnlyPublicOrLoopbackHost(t *testing.T) {
	ctx := context.Background()
	st, _, _ := testStore(ctx, t)
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(&config.Config{MCP: config.MCPConfig{
		Mode: "cloudflare", PublicURL: "https://mcp.example.com/mcp",
	}}, st).HTTPHandler(hash)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	for _, test := range []struct {
		host string
		want int
	}{
		{host: "mcp.example.com", want: http.StatusOK},
		{host: "127.0.0.1:8765", want: http.StatusOK},
		{host: "attacker.example", want: http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", bytes.NewReader(body))
		req.Host = test.host
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != test.want {
			t.Errorf("Host %q: status = %d, want %d; body=%s", test.host, res.Code, test.want, res.Body.String())
		}
	}
}

func TestNewTokenIsUniqueAndVerifiable(t *testing.T) {
	token1, hash1, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	token2, hash2, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if token1 == token2 || hash1 == hash2 {
		t.Fatal("NewToken returned duplicate credentials")
	}
	if hash1 != TokenHash(token1) || sameTokenHash(TokenHash(token2), hash1) {
		t.Fatal("token digest verification is incorrect")
	}
}

func TestReadOnlyTools(t *testing.T) {
	ctx := context.Background()
	st, _, deployID := testStore(ctx, t)
	root := testInstallation(t)
	server := New(&config.Config{
		Listen:    "127.0.0.1:0",
		Discovery: config.DiscoveryConfig{Roots: []string{root}},
	}, st)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.mcp.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Errorf("tool %s is not annotated read-only and idempotent", tool.Name)
		}
	}
	sort.Strings(names)
	wantNames := []string{"get_deploy", "get_status", "list_deploys", "list_installations", "list_repositories", "validate_manifest"}
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}

	repos := callTool(ctx, t, clientSession, "list_repositories", map[string]any{})
	assertNoSecrets(t, repos)
	if !strings.Contains(string(repos), "owner/repo") {
		t.Errorf("repository output missing slug: %s", repos)
	}
	installations := callTool(ctx, t, clientSession, "list_installations", map[string]any{"repository": "owner/repo"})
	if !strings.Contains(string(installations), `"branch":"main"`) {
		t.Errorf("installation output missing branch: %s", installations)
	}
	deploys := callTool(ctx, t, clientSession, "list_deploys", map[string]any{"repository": "owner/repo", "limit": 10})
	assertNoSecrets(t, deploys)
	if !strings.Contains(string(deploys), `"sha":"abcdef123456"`) {
		t.Errorf("deploy list output missing SHA: %s", deploys)
	}

	deploy := callTool(ctx, t, clientSession, "get_deploy", map[string]any{"id": deployID})
	assertNoSecrets(t, deploy)
	if !strings.Contains(string(deploy), `"repository":"owner/repo"`) || !strings.Contains(string(deploy), `"path":"/srv/app"`) {
		t.Errorf("deploy output missing public fields: %s", deploy)
	}

	manifest := callTool(ctx, t, clientSession, "validate_manifest", map[string]any{"path": root})
	if !strings.Contains(string(manifest), `"repository":"owner/repo"`) {
		t.Errorf("manifest output missing repository: %s", manifest)
	}
	status := callTool(ctx, t, clientSession, "get_status", map[string]any{})
	if !strings.Contains(string(status), `"path":"/srv/app"`) {
		t.Errorf("status output missing path: %s", status)
	}
}

func TestValidateManifestRejectsArbitraryPath(t *testing.T) {
	ctx := context.Background()
	st, _, _ := testStore(ctx, t)
	server := New(&config.Config{Discovery: config.DiscoveryConfig{Roots: []string{t.TempDir()}}}, st)

	_, _, err := server.validateManifest(ctx, nil, manifestInput{Path: "/etc"})
	if err == nil || !strings.Contains(err.Error(), "not a checkout") {
		t.Fatalf("validateManifest error = %v, want checkout restriction", err)
	}
}

func testStore(ctx context.Context, t *testing.T) (*store.Store, int64, int64) {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	repoID, err := st.RepoInsert(ctx, store.Repo{
		Repository: "owner/repo", Token: "super-secret-token", Secret: "super-secret-hmac",
		GitHubKeyID: 12, GitHubHookID: 34,
	})
	if err != nil {
		t.Fatalf("RepoInsert: %v", err)
	}
	deployID, err := st.DeployStart(ctx, store.Deploy{
		RepoID: repoID, DeliveryID: "secret-delivery-id", Ref: "refs/heads/main",
		SHA: "abcdef123456", Message: "ship it", Author: "Ada", Status: store.StatusRunning,
	})
	if err != nil {
		t.Fatalf("DeployStart: %v", err)
	}
	if err := st.DeployPathInsert(ctx, store.DeployPath{
		DeployID: deployID, Path: "/srv/app", User: "deploy", PreviousSHA: "old",
		NewSHA: "abcdef123456", Status: store.StatusSuccess,
		Commands: `[{"command":"echo super-secret-output","output":"super-secret-output"}]`,
	}); err != nil {
		t.Fatalf("DeployPathInsert: %v", err)
	}
	if err := st.DeployFinish(ctx, deployID, store.StatusSuccess); err != nil {
		t.Fatalf("DeployFinish: %v", err)
	}

	return st, repoID, deployID
}

func testInstallation(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	files := map[string]string{
		filepath.Join(root, ".git", "HEAD"):    "ref: refs/heads/main\n",
		filepath.Join(root, ".git", "config"):  "[remote \"origin\"]\n\turl = git@github.com:owner/repo.git\n",
		filepath.Join(root, ".rec-deploy.yml"): "repository: owner/repo\npost_deploy:\n  - run: make deploy\n    timeout: 5m\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	return root
}

func callTool(ctx context.Context, t *testing.T, session *mcp.ClientSession, name string, args map[string]any) json.RawMessage {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned tool error: %v", name, result.Content)
	}
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal %s output: %v", name, err)
	}

	return b
}

func assertNoSecrets(t *testing.T, output []byte) {
	t.Helper()
	for _, secret := range []string{"super-secret-token", "super-secret-hmac", "secret-delivery-id", "super-secret-output", "commands"} {
		if strings.Contains(string(output), secret) {
			t.Errorf("MCP output exposes %q: %s", secret, output)
		}
	}
}
