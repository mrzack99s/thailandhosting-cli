package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

type fakeStatic struct {
	t        *testing.T
	mu       sync.Mutex
	sites    []map[string]any
	created  []map[string]any
	uploads  map[string][]string // site ID -> file names in the tar
	contents map[string]string
	hasIndex bool
	rolled   string
	deleted  []string
	domains  []map[string]any
	verified []string
}

func newFakeStatic(t *testing.T) *fakeStatic {
	return &fakeStatic{t: t, uploads: map[string][]string{}, contents: map[string]string{}, hasIndex: true}
}

func (f *fakeStatic) folderSite() map[string]any {
	return map[string]any{"site": map[string]any{"id": "site1", "name": "docs", "slug": "docs", "projectId": "p1", "regionId": "bkk", "activeRelease": "rel2", "source": "git"},
		"source": nil, "latestBuild": nil, "url": "https://docs.site.bkk.thailandhosting.com/"}
}

func (f *fakeStatic) gitSite() map[string]any {
	return map[string]any{"site": map[string]any{"id": "site2", "name": "blog", "slug": "blog", "projectId": "p1", "regionId": "bkk", "activeRelease": "relg", "source": "git"},
		"source":      map[string]any{"provider": "github", "repoUrl": "https://github.com/acme/blog", "branch": "main"},
		"latestBuild": map[string]any{"id": "b2", "number": 2, "status": "success"}, "url": "https://blog.site.bkk.thailandhosting.com/"}
}

func (f *fakeStatic) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.EscapedPath()
	switch {
	case p == "/api/v1/projects":
		writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme"}})
	case p == "/api/v1/cli/whoami":
		writeJSON(w, 200, map[string]string{"kind": "api_key", "projectId": "p1"})
	case p == "/api/v1/regions":
		writeJSON(w, 200, []map[string]any{{"id": "bkk", "name": "Bangkok", "isDefault": true}, {"id": "sgp", "name": "Singapore"}})
	case r.Method == "GET" && p == "/api/v1/static-web":
		if r.URL.Query().Get("projectId") != "p1" {
			f.t.Errorf("static-web query = %s", r.URL.RawQuery)
		}
		writeJSON(w, 200, f.sites)
	case r.Method == "POST" && p == "/api/v1/static-web/upload-site":
		var in map[string]any
		ssDecodeBody(r, &in)
		f.created = append(f.created, in)
		site := map[string]any{"id": "new1", "name": in["name"], "slug": in["slug"], "projectId": in["projectId"], "regionId": in["regionId"], "source": "git"}
		f.sites = append(f.sites, map[string]any{"site": site, "source": nil, "url": "https://x/"})
		writeJSON(w, 201, map[string]any{"site": site, "url": "https://" + in["slug"].(string) + ".site.bkk.thailandhosting.com/"})
	case r.Method == "POST" && strings.HasSuffix(p, "/deploy"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/sites/"), "/deploy")
		if ct := r.Header.Get("Content-Type"); ct != "application/gzip" {
			f.t.Errorf("content type %q", ct)
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			f.t.Fatalf("gzip: %v", err)
		}
		tr := tar.NewReader(gz)
		var names []string
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				f.t.Fatalf("tar: %v", err)
			}
			names = append(names, h.Name)
			b, _ := io.ReadAll(tr)
			f.contents[h.Name] = string(b)
		}
		sort.Strings(names)
		f.uploads[id] = names
		writeJSON(w, 201, map[string]any{"release": map[string]any{"id": "rel3", "files": len(names), "bytes": 10}, "url": "https://docs.site.bkk.thailandhosting.com/", "hasIndex": f.hasIndex})
	case r.Method == "GET" && p == "/api/v1/sites/site1/releases":
		writeJSON(w, 200, []map[string]string{{"id": "rel3", "createdAt": "2026-10-14T10:00:00Z"}, {"id": "rel2", "createdAt": "2026-10-13T10:00:00Z"}, {"id": "rel1", "createdAt": "2026-10-12T10:00:00Z"}})
	case r.Method == "POST" && strings.HasPrefix(p, "/api/v1/sites/site1/rollback/"):
		f.rolled = strings.TrimPrefix(p, "/api/v1/sites/site1/rollback/")
		writeJSON(w, 200, map[string]any{"ok": true, "activeRelease": f.rolled, "url": "https://docs.site.bkk.thailandhosting.com/"})
	case r.Method == "DELETE" && strings.HasPrefix(p, "/api/v1/sites/") && !strings.Contains(strings.TrimPrefix(p, "/api/v1/sites/"), "/"):
		f.deleted = append(f.deleted, strings.TrimPrefix(p, "/api/v1/sites/"))
		writeJSON(w, 200, map[string]bool{"ok": true})
	case r.Method == "GET" && p == "/api/v1/sites/site1/domains":
		writeJSON(w, 200, f.domains)
	case r.Method == "POST" && p == "/api/v1/sites/site1/domains":
		var in map[string]string
		ssDecodeBody(r, &in)
		d := map[string]any{"id": "d1", "host": in["Host"], "token": "tok123", "verified": false}
		f.domains = append(f.domains, d)
		writeJSON(w, 201, d)
	case r.Method == "POST" && strings.HasPrefix(p, "/api/v1/sites/site1/domains/"):
		f.verified = append(f.verified, p)
		writeJSON(w, 200, map[string]bool{"verified": true})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, p)
		http.NotFound(w, r)
	}
}

func ssDecodeBody(r *http.Request, v any) {
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, v)
}

func startStatic(t *testing.T) (*fakeStatic, *harness) {
	f := newFakeStatic(t)
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, newHarness(t, srv.URL)
}

func ssWriteTree(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestStaticDeployExistingSite(t *testing.T) {
	f, h := startStatic(t)
	f.sites = []map[string]any{f.folderSite()}
	dir := ssWriteTree(t, map[string]string{
		"index.html":         "<h1>hi</h1>",
		"assets/app.js":      "x",
		"assets/app.js.map":  "map",
		".gitignore":         "*\n", // not read for a build output
		".thignore":          "*.map\n",
		".git/HEAD":          "ref",
		".DS_Store":          "junk",
		"sub/.DS_Store":      "junk",
		"node_modules/a.js":  "kept: a built folder may hold it on purpose",
		"deep/nested/x.html": "x",
	})
	if code := h.run("static-site", "deploy", "docs", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	got := strings.Join(f.uploads["site1"], ",")
	want := ".gitignore,assets/app.js,deep/nested/x.html,index.html,node_modules/a.js"
	if got != want {
		t.Fatalf("uploaded %s\nwant     %s", got, want)
	}
	if f.contents["index.html"] != "<h1>hi</h1>" {
		t.Errorf("index.html = %q", f.contents["index.html"])
	}
	if len(f.created) != 0 {
		t.Errorf("created a site for an existing one: %v", f.created)
	}
	if !strings.Contains(h.errOut.String(), "Uploading 5 files") || !strings.Contains(h.errOut.String(), "Skipping files listed in .thignore") {
		t.Errorf("stderr: %s", h.errOut)
	}
	if !strings.Contains(h.out.String(), "URL: https://docs.site.bkk.thailandhosting.com/") {
		t.Errorf("stdout: %s", h.out)
	}
	if strings.Contains(h.errOut.String(), "Warning") {
		t.Errorf("unexpected warning: %s", h.errOut)
	}
}

func TestStaticDeployCreatesSiteWithAPIKey(t *testing.T) {
	f, h := startStatic(t)
	h.env["TH_API_KEY"] = "thk_ci"
	dir := ssWriteTree(t, map[string]string{"index.html": "x"})
	if code := h.run("static-site", "deploy", "my-site", dir, "--region", "sgp"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %v", f.created)
	}
	c := f.created[0]
	if c["slug"] != "my-site" || c["projectId"] != "p1" || c["regionId"] != "sgp" {
		t.Errorf("create body %v", c)
	}
	if strings.Join(f.uploads["new1"], ",") != "index.html" {
		t.Errorf("uploads %v", f.uploads)
	}
}

func TestStaticDeployNewSiteDefaultRegionAndBadName(t *testing.T) {
	f, h := startStatic(t)
	dir := ssWriteTree(t, map[string]string{"index.html": "x"})
	if code := h.run("static-site", "deploy", "Bad_Name", dir); code == 0 {
		t.Fatal("bad name accepted")
	}
	if len(f.created) != 0 {
		t.Fatal("created with a bad name")
	}
	if code := h.run("static-site", "deploy", "web", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.created[0]["regionId"] != "bkk" || !strings.Contains(h.errOut.String(), "default region [bkk]") {
		t.Errorf("region: %v / %s", f.created, h.errOut)
	}
}

func TestStaticDeployMissingIndex(t *testing.T) {
	f, h := startStatic(t)
	f.sites = []map[string]any{f.folderSite()}

	// A project root whose output is in dist/: stop when DIR was defaulted.
	root := ssWriteTree(t, map[string]string{"package.json": "{}", "dist/index.html": "x"})
	wd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	code := h.run("static-site", "deploy", "docs")
	os.Chdir(wd)
	if code == 0 || !strings.Contains(h.errOut.String(), "th static-site deploy docs dist") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(f.uploads) != 0 {
		t.Fatal("uploaded the project root")
	}

	// No index.html anywhere: upload, then warn from the server's answer.
	f.hasIndex = false
	dir := ssWriteTree(t, map[string]string{"about.html": "x"})
	if code := h.run("static-site", "deploy", "docs", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.errOut.String(), "Warning: the release has no index.html") {
		t.Errorf("stderr: %s", h.errOut)
	}
}

func TestStaticRollbackPrevious(t *testing.T) {
	f, h := startStatic(t)
	f.sites = []map[string]any{f.folderSite()} // live: rel2
	if code := h.run("static-site", "rollback", "docs", "previous"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.rolled != "rel1" {
		t.Errorf("rolled back to %q", f.rolled)
	}
	if !strings.Contains(h.out.String(), "now serves release [rel1]") {
		t.Errorf("stdout: %s", h.out)
	}
}

func TestStaticBuildsOnFolderSite(t *testing.T) {
	f, h := startStatic(t)
	f.sites = []map[string]any{f.folderSite(), f.gitSite()}
	if code := h.run("static-site", "builds", "list", "docs"); code == 0 {
		t.Fatal("builds on a folder site succeeded")
	}
	if !strings.Contains(h.errOut.String(), "deployed from a folder (th CLI)") {
		t.Errorf("stderr: %s", h.errOut)
	}
	if code := h.run("static-site", "describe", "blog"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "git https://github.com/acme/blog (main)") {
		t.Errorf("describe: %s", h.out)
	}
	if code := h.run("static-site", "describe", "docs"); code != 0 || !strings.Contains(h.out.String(), "folder (th CLI)") {
		t.Errorf("describe folder: %s", h.out)
	}
}

func TestStaticDomainsAddAndVerify(t *testing.T) {
	f, h := startStatic(t)
	f.sites = []map[string]any{f.folderSite()}
	if code := h.run("static-site", "domains", "add", "docs", "www.example.com"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"CNAME", "docs.site.bkk.thailandhosting.com", "TXT", "_thailandhosting.www.example.com", "tok123", "domains verify docs www.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code := h.run("static-site", "domains", "verify", "docs", "WWW.example.com."); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(f.verified) != 1 || f.verified[0] != "/api/v1/sites/site1/domains/d1/verify" {
		t.Errorf("verified %v", f.verified)
	}
}

func TestStaticDeleteConfirm(t *testing.T) {
	f, h := startStatic(t)
	f.sites = []map[string]any{f.folderSite()}
	h.app.Stdin = strings.NewReader("n\n")
	if code := h.run("static-site", "delete", "docs"); code == 0 {
		t.Fatal("declined delete succeeded")
	}
	if len(f.deleted) != 0 {
		t.Fatal("deleted after no")
	}
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run("static-site", "delete", "docs"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if code := h.run("static-site", "delete", "docs", "--quiet"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if strings.Join(f.deleted, ",") != "site1,site1" {
		t.Errorf("deleted %v", f.deleted)
	}
}
