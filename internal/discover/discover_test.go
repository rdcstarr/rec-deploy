package discover

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// repoAt creates a git checkout at dir with the given origin URL, branch and
// manifest body. An empty manifest body writes no .rec-deploy.yml at all.
func repoAt(t *testing.T, dir, origin, branch, manifestBody string) {
	t.Helper()

	git := filepath.Join(dir, ".git")
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", git, err)
	}
	write(t, filepath.Join(git, "HEAD"), "ref: refs/heads/"+branch+"\n")
	write(t, filepath.Join(git, "config"), "[remote \"origin\"]\n\turl = "+origin+"\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")

	if manifestBody != "" {
		write(t, filepath.Join(dir, ".rec-deploy.yml"), manifestBody)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const temaManifest = "repository: rdcstarr/tema\npost_deploy:\n  - echo hi\n"

func TestScanFindsEveryCheckoutOfARepo(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "andrei", "public_html", "wp-content", "themes", "tema")
	b := filepath.Join(root, "maria", "public_html", "wp-content", "themes", "tema")
	repoAt(t, a, "git@github.com:rdcstarr/tema.git", "main", temaManifest)
	repoAt(t, b, "git@github.com:rdcstarr/tema.git", "develop", temaManifest)

	all, err := Scan(context.Background(), Options{Roots: []string{filepath.Join(root, "*", "public_html")}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	found := Filter(all, "rdcstarr/tema")
	if len(found) != 2 {
		t.Fatalf("found %d installations, want 2: %+v", len(found), found)
	}

	branches := map[string]bool{}
	for _, in := range found {
		branches[in.Branch] = true
		if in.Repository != "rdcstarr/tema" {
			t.Errorf("Repository = %q", in.Repository)
		}
	}
	// Each installation follows its own branch: staging on develop and
	// production on main coexist on one server with no configuration.
	if !branches["main"] || !branches["develop"] {
		t.Errorf("branches = %v, want main and develop", branches)
	}
}

func TestScanFindsLongYAMLManifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "site")
	repoAt(t, path, "git@github.com:rdcstarr/tema.git", "main", "")
	write(t, filepath.Join(path, ".rec-deploy.yaml"), temaManifest)

	all, err := Scan(context.Background(), Options{Roots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	found := Filter(all, "rdcstarr/tema")
	if len(found) != 1 || found[0].Err != nil {
		t.Fatalf("installations = %+v, want one valid long-extension manifest", found)
	}
}

// The git-root stop rule: a theme cloned inside a site repo is its own root and
// must not be reached by descending into the site repo.
func TestScanDoesNotDescendIntoAGitRepository(t *testing.T) {
	root := t.TempDir()
	site := filepath.Join(root, "site")
	repoAt(t, site, "git@github.com:rdcstarr/site.git", "main", "repository: rdcstarr/site\n")
	repoAt(t, filepath.Join(site, "wp-content", "themes", "tema"), "git@github.com:rdcstarr/tema.git", "main", temaManifest)

	all, err := Scan(context.Background(), Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(all) != 1 || all[0].Repository != "rdcstarr/site" {
		t.Fatalf("Scan = %+v, want only the outer site repo", all)
	}
}

func TestScanPrunesConfiguredDirectories(t *testing.T) {
	root := t.TempDir()
	repoAt(t, filepath.Join(root, "node_modules", "pkg"), "git@github.com:rdcstarr/tema.git", "main", temaManifest)

	all, err := Scan(context.Background(), Options{
		Roots: []string{root},
		Prune: []string{"node_modules"},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("Scan = %+v, want nothing: node_modules is pruned", all)
	}
}

func TestScanZeroInstallations(t *testing.T) {
	all, err := Scan(context.Background(), Options{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("Scan = %+v, want empty", all)
	}
	// Zero is not an error here — it is the deploy engine that turns "zero
	// installations for this repository" into a reported, notified failure.
}

func TestScanReportsAnInvalidManifest(t *testing.T) {
	root := t.TempDir()
	repoAt(t, filepath.Join(root, "broken"), "git@github.com:rdcstarr/tema.git", "main", "repository: [unterminated\n")

	all, err := Scan(context.Background(), Options{Roots: []string{root}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(all) != 1 || all[0].Err == nil {
		t.Fatalf("Scan = %+v, want one installation carrying a parse error", all)
	}
}

// Reporting the parse error is only half the job: the installation must also
// survive Filter, or the deploy engine never sees it. A broken checkout that
// Filter drops deploys nothing, reports nothing, and lets a healthy sibling
// report the whole push as a success — the "invalid manifest reports success"
// defect, re-entered through discovery instead of through the manifest.
func TestFilterKeepsACheckoutWhoseManifestIsBroken(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "andrei", "public_html", "tema")
	broken := filepath.Join(root, "maria", "public_html", "tema")
	repoAt(t, good, "git@github.com:rdcstarr/tema.git", "main", temaManifest)
	// `post_deploys` is not a field: strict decoding rejects the whole manifest.
	repoAt(t, broken, "git@github.com:rdcstarr/tema.git", "main", "repository: rdcstarr/tema\npost_deploys:\n  - echo hi\n")

	all, err := Scan(context.Background(), Options{Roots: []string{filepath.Join(root, "*", "public_html")}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	found := Filter(all, "rdcstarr/tema")
	if len(found) != 2 {
		t.Fatalf("Filter returned %d installations, want 2 — the broken checkout must reach the engine to be reported", len(found))
	}

	for _, in := range found {
		if in.Path == broken && in.Err == nil {
			t.Errorf("the broken checkout passed Filter with no Err — the engine would deploy it as healthy")
		}
	}
}

// Origin is not the only fallible source. A checkout whose origin cannot be read
// — a submodule or worktree, whose .git is a file, or a remote that was removed
// — must still be attributed from its manifest, or it is dropped by Filter and
// silently skipped exactly like the broken-manifest case above.
func TestFilterKeepsACheckoutWhoseOriginIsUnreadable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "andrei", "public_html", "tema")
	repoAt(t, dir, "git@github.com:rdcstarr/tema.git", "main", temaManifest)

	// What `git submodule add` and `git worktree add` leave behind: .git is a
	// file pointing elsewhere, so reading .git/config returns ENOTDIR.
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("rm .git: %v", err)
	}
	write(t, filepath.Join(dir, ".git"), "gitdir: /nonexistent/modules/tema\n")

	all, err := Scan(context.Background(), Options{Roots: []string{filepath.Join(root, "*", "public_html")}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("Scan found %d installations, want 1", len(all))
	}
	if all[0].Err == nil {
		t.Error("an unreadable origin was not reported")
	}

	if found := Filter(all, "rdcstarr/tema"); len(found) != 1 {
		t.Errorf("Filter dropped a checkout whose manifest names it — it would vanish from its own push")
	}
}

// The registered slug and the checkout's origin can differ in case: GitHub
// resolves slugs case-insensitively, so `repo add rdcstarr/tema` registers a
// repository whose canonical name — and origin URL — is RdcStarr/Tema. The
// engine filters installations by the registered slug, so an exact match here
// would drop a checkout that discovery itself considers healthy and tell the
// operator no installation exists for a repository sitting right there.
func TestFilterMatchesTheRegisteredSlugCaseInsensitively(t *testing.T) {
	root := t.TempDir()
	repoAt(t, filepath.Join(root, "andrei", "public_html", "tema"),
		"git@github.com:rdcstarr/tema.git", "main", temaManifest)

	all, err := Scan(context.Background(), Options{Roots: []string{filepath.Join(root, "*", "public_html")}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(all) != 1 || all[0].Err != nil {
		t.Fatalf("Scan = %+v, want one healthy installation", all)
	}

	// The query carries the case difference — that is where it comes from in
	// production, since Repository is now read from origin.
	if found := Filter(all, "RdcStarr/Tema"); len(found) != 1 {
		t.Errorf("Filter dropped a checkout whose registered slug differs from origin only in case")
	}
}

func TestOriginSlugParsesBothURLForms(t *testing.T) {
	cases := []struct {
		url   string
		slug  string
		https bool
	}{
		{"git@github.com:rdcstarr/tema.git", "rdcstarr/tema", false},
		{"ssh://git@github.com/rdcstarr/tema.git", "rdcstarr/tema", false},
		{"https://github.com/rdcstarr/tema.git", "rdcstarr/tema", true},
		{"https://github.com/rdcstarr/tema", "rdcstarr/tema", true},
	}

	for _, c := range cases {
		dir := t.TempDir()
		repoAt(t, dir, c.url, "main", temaManifest)

		slug, https, err := OriginSlug(dir)
		if err != nil {
			t.Fatalf("OriginSlug(%s): %v", c.url, err)
		}
		if slug != c.slug || https != c.https {
			t.Errorf("OriginSlug(%s) = %q, https=%v; want %q, %v", c.url, slug, https, c.slug, c.https)
		}
	}
}

func TestBranchReadsGitHEAD(t *testing.T) {
	dir := t.TempDir()
	repoAt(t, dir, "git@github.com:rdcstarr/tema.git", "feature/x", temaManifest)

	branch, err := Branch(dir)
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	if branch != "feature/x" {
		t.Errorf("Branch = %q, want feature/x", branch)
	}
}

// A detached HEAD has no branch: the push filter can never match it, so the
// deploy skips rather than guessing.
func TestBranchDetachedHEAD(t *testing.T) {
	dir := t.TempDir()
	repoAt(t, dir, "git@github.com:rdcstarr/tema.git", "main", temaManifest)
	write(t, filepath.Join(dir, ".git", "HEAD"), "9f1c2ab0000000000000000000000000000000ab\n")

	if branch, err := Branch(dir); err == nil {
		t.Errorf("Branch = %q, nil; want an error for a detached HEAD", branch)
	}
}
