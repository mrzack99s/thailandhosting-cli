package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	digA = "sha256:aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888"
	digB = "sha256:aaaa1119bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888"
	digC = "sha256:cccc0000bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa7777bbbb8888"
)

type fakeRegistry struct {
	t       *testing.T
	mu      sync.Mutex
	deleted []string
	status  int // answer for DELETE (default 204)
	msg     string
}

func (f *fakeRegistry) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.EscapedPath()
	if strings.HasPrefix(p, "/api/v1/run/registry") && r.URL.Query().Get("projectId") != "p1" {
		f.t.Errorf("registry query = %s", r.URL.RawQuery)
	}
	switch {
	case p == "/api/v1/projects":
		writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme"}})
	case r.Method == "GET" && p == "/api/v1/run/registry":
		writeJSON(w, 200, map[string]any{"host": "registry.th", "url": "https://registry.th", "namespace": "acme",
			"usedBytes": 5 << 30, "quotaBytes": 20 << 30,
			"repositories": []map[string]any{{"name": "api", "image": "registry.th/acme/api", "tags": []string{"latest", "v2"},
				"sizeBytes": 84200000, "updatedAt": "2026-10-10T08:00:00Z"}}})
	case r.Method == "GET" && p == "/api/v1/run/registry/api/images":
		writeJSON(w, 200, []map[string]any{
			{"digest": digA, "tags": []string{"latest", "v2"}, "sizeBytes": 1288490188, "createdAt": "2026-10-10T08:00:00Z", "usedBy": []string{"api"}, "kept": true},
			{"digest": digB, "tags": []string{}, "sizeBytes": 2048, "createdAt": "2026-10-09T08:00:00Z", "usedBy": []string{}, "kept": true},
			{"digest": digC, "tags": []string{"old"}, "sizeBytes": 2048, "createdAt": "2026-10-01T08:00:00Z", "usedBy": []string{}, "kept": false},
		})
	case r.Method == "DELETE" && strings.HasPrefix(p, "/api/v1/run/registry/"):
		f.deleted = append(f.deleted, strings.TrimPrefix(p, "/api/v1/run/registry/"))
		if f.status != 0 {
			writeJSON(w, f.status, map[string]string{"error": f.msg})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case p == "/api/v1/services":
		writeJSON(w, 200, []map[string]any{existingService()})
	case p == "/api/v1/services/s1/revisions":
		writeJSON(w, 200, []map[string]any{
			{"id": "r2", "name": "api-00002", "number": 2, "image": "registry.th/acme/api@" + digA},
			{"id": "r1", "name": "api-00001", "number": 1, "image": "registry.th/acme/api@" + digC, "imageRemoved": true},
		})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		writeJSON(w, 404, map[string]string{"error": "ไม่พบ"})
	}
}

func startRegistry(t *testing.T) (*fakeRegistry, *harness) {
	f := &fakeRegistry{t: t}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, newHarness(t, srv.URL)
}

func TestImagesListRepositories(t *testing.T) {
	_, h := startRegistry(t)
	if code := h.run("container", "images", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	if !strings.HasPrefix(out, "Registry registry.th/acme · used 5.0 GiB of 20 GB (25%)\n") {
		t.Fatalf("header: %q", out)
	}
	if !strings.Contains(out, "REPOSITORY") || !strings.Contains(out, "latest,v2") || !strings.Contains(out, "80.3 MiB") {
		t.Fatalf("table: %s", out)
	}
}

func TestImagesListImages(t *testing.T) {
	_, h := startRegistry(t)
	if code := h.run("container", "images", "list", "api"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], "USED BY") || !strings.Contains(lines[0], "KEPT") {
		t.Fatalf("out: %s", h.out)
	}
	f1 := strings.Fields(lines[1])
	if f1[0] != "aaaa1111bbbb" || f1[1] != "latest,v2" || !strings.Contains(lines[1], "1.2 GiB") || f1[len(f1)-2] != "api" || f1[len(f1)-1] != "yes" {
		t.Fatalf("row 1: %q", lines[1])
	}
	if f := strings.Fields(lines[2]); f[1] != "-" || f[len(f)-1] != "yes" {
		t.Fatalf("row 2: %q", lines[2])
	}
	if f := strings.Fields(lines[3]); f[0] != "cccc0000bbbb" || f[len(f)-1] != "no" {
		t.Fatalf("row 3: %q", lines[3])
	}
}

func TestImagesDeleteByTag(t *testing.T) {
	f, h := startRegistry(t)
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run("container", "images", "delete", "api:v2"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.errOut.String(), "Delete image api:v2?") || !strings.Contains(h.errOut.String(), "(y/N)") {
		t.Fatalf("prompt: %s", h.errOut)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "api/images/v2" {
		t.Fatalf("deleted %v", f.deleted)
	}
}

func TestImagesDeleteDeclined(t *testing.T) {
	f, h := startRegistry(t)
	h.app.Stdin = strings.NewReader("\n")
	if code := h.run("container", "images", "delete", "api:v2"); code != 1 || len(f.deleted) != 0 {
		t.Fatalf("exit %d, deleted %v", code, f.deleted)
	}
}

func TestImagesDeleteFullPathQuiet(t *testing.T) {
	f, h := startRegistry(t)
	if code := h.run("container", "images", "delete", "registry.th/acme/api@"+digC, "--quiet"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if strings.Contains(h.errOut.String(), "(y/N)") {
		t.Fatalf("prompted with --quiet: %s", h.errOut)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "api/images/"+digC {
		t.Fatalf("deleted %v", f.deleted)
	}
	if !strings.Contains(h.out.String(), "and its tags: old") {
		t.Fatalf("out: %s", h.out)
	}
}

func TestImagesDeleteFullPathWrongNamespace(t *testing.T) {
	f, h := startRegistry(t)
	if code := h.run("container", "images", "delete", "registry.th/other/api:v2", "--quiet"); code != 1 || len(f.deleted) != 0 {
		t.Fatalf("exit %d, deleted %v", code, f.deleted)
	}
	if !strings.Contains(h.errOut.String(), "not in this Project's registry") {
		t.Fatalf("stderr: %s", h.errOut)
	}
}

func TestImagesDeleteShortDigest(t *testing.T) {
	f, h := startRegistry(t)
	h.app.Stdin = strings.NewReader("yes\n")
	if code := h.run("container", "images", "delete", "api@cccc000"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "api/images/"+digC {
		t.Fatalf("deleted %v", f.deleted)
	}
	if !strings.Contains(h.errOut.String(), "Its tags go with it: old") {
		t.Fatalf("prompt: %s", h.errOut)
	}
}

func TestImagesDeleteShortDigestAmbiguous(t *testing.T) {
	f, h := startRegistry(t)
	if code := h.run("container", "images", "delete", "api@aaaa111", "--quiet"); code != 1 || len(f.deleted) != 0 {
		t.Fatalf("exit %d, deleted %v", code, f.deleted)
	}
	if !strings.Contains(h.errOut.String(), "matches 2 images") {
		t.Fatalf("stderr: %s", h.errOut)
	}
}

func TestImagesDeleteBareRepository(t *testing.T) {
	f, h := startRegistry(t)
	if code := h.run("container", "images", "delete", "api", "--quiet"); code != 1 || len(f.deleted) != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.errOut.String(), "th container images delete-repository api") {
		t.Fatalf("stderr: %s", h.errOut)
	}
}

func TestImagesDeleteInUseShowsServerMessage(t *testing.T) {
	f, h := startRegistry(t)
	f.status, f.msg = 409, "Container api ยังใช้ image นี้อยู่"
	if code := h.run("container", "images", "delete", "api@"+digA, "--quiet"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.errOut.String(), "Container api ยังใช้ image นี้อยู่") {
		t.Fatalf("stderr: %s", h.errOut)
	}
}

func TestImagesDeleteRepository(t *testing.T) {
	f, h := startRegistry(t)
	if code := h.run("container", "images", "delete-repository", "api", "--quiet"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "api" {
		t.Fatalf("deleted %v", f.deleted)
	}
}

func TestRevisionsListMarksRemovedImage(t *testing.T) {
	_, h := startRegistry(t)
	if code := h.run("container", "revisions", "list", "--service", "api"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
	if len(lines) != 3 || strings.Contains(lines[1], "removed") || !strings.Contains(lines[2], "(removed — cannot roll back)") {
		t.Fatalf("out: %s", h.out)
	}
}

func TestDeployRegistryFullHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/projects":
			writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme"}})
		case "/api/v1/services":
			writeJSON(w, 200, []map[string]any{existingService()})
		case "/api/v1/run/sources":
			writeJSON(w, 201, map[string]any{"sourceId": "src1"})
		case "/api/v1/run/builds":
			writeJSON(w, 409, map[string]string{"error": "Registry ของ Project นี้จะเกิน 20 GB"})
		default:
			writeJSON(w, 404, map[string]string{"error": "x"})
		}
	}))
	defer srv.Close()
	h := newHarness(t, srv.URL)
	dir := t.TempDir()
	if code := h.run("container", "deploy", "api", "--source", dir); code != 1 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.errOut.String(), "Registry ของ Project นี้จะเกิน 20 GB\nFree space: th container images list") {
		t.Fatalf("stderr: %s", h.errOut)
	}
}
