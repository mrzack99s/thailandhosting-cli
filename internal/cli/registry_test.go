package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeRegistries is the /api/v1/run/registries API with three registries:
// default (r1), prod (r2) and staging (r3).
type fakeRegistries struct {
	t     *testing.T
	mu    sync.Mutex
	calls []string       // "METHOD path?query"
	body  map[string]any // last JSON body
}

func (f *fakeRegistries) registries() []map[string]any {
	return []map[string]any{
		{"id": "r1", "name": "default", "isDefault": true, "path": "registry.th/acme", "cleanupUntaggedDays": 7,
			"cleanupKeepTags": []string{}, "repositories": 2, "images": 5, "sizeBytes": 3 << 30, "pulls": 12, "lastPushAt": "2026-10-10T08:00:00Z"},
		{"id": "r2", "name": "prod", "path": "registry.th/acme/prod", "public": true, "immutableTags": true, "cleanupUntaggedDays": 7,
			"cleanupKeepLatest": 10, "cleanupKeepTags": []string{"v*"}, "repositories": 1, "images": 2, "sizeBytes": 1 << 20, "tokens": 1},
		{"id": "r3", "name": "staging", "path": "registry.th/acme/staging", "cleanupUntaggedDays": 7, "cleanupKeepTags": []string{}},
	}
}

func (f *fakeRegistries) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.EscapedPath()
	call := r.Method + " " + p
	if r.URL.RawQuery != "" {
		call += "?" + r.URL.RawQuery
	}
	f.calls = append(f.calls, call)
	f.body = nil
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&f.body)
	}
	images := []map[string]any{
		{"digest": digA, "tags": []string{"latest", "v1"}, "sizeBytes": 2048, "createdAt": "2026-10-10T08:00:00Z", "usedBy": []string{"api"}, "kept": true, "pulls": 7},
		{"digest": digC, "tags": []string{}, "sizeBytes": 1024, "createdAt": "2026-10-01T08:00:00Z", "usedBy": []string{}, "kept": false},
	}
	switch {
	case p == "/api/v1/projects":
		writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme", "name": "Acme"}})
	case r.Method == "GET" && p == "/api/v1/run/registries":
		if r.URL.Query().Get("projectId") != "p1" {
			f.t.Errorf("registries query %s", r.URL.RawQuery)
		}
		writeJSON(w, 200, map[string]any{"host": "registry.th", "namespace": "acme", "registries": f.registries(),
			"usedBytes": 5 << 30, "quotaBytes": 20 << 30, "maxRegistries": 10})
	case r.Method == "POST" && p == "/api/v1/run/registries":
		writeJSON(w, 201, map[string]any{"id": "r9", "name": f.body["name"], "path": "registry.th/acme/" + f.body["name"].(string)})
	case r.Method == "GET" && p == "/api/v1/run/registries/r2":
		writeJSON(w, 200, map[string]any{"registry": f.registries()[1], "repositories": []map[string]any{
			{"name": "api", "path": "prod/api", "tags": []string{"v1"}, "images": 2, "sizeBytes": 1 << 20, "pulls": 3, "updatedAt": "2026-10-10T08:00:00Z"}}})
	case r.Method == "PATCH" && p == "/api/v1/run/registries/r3":
		writeJSON(w, 200, f.registries()[2])
	case r.Method == "DELETE" && p == "/api/v1/run/registries/r3":
		w.WriteHeader(204)
	case r.Method == "GET" && strings.HasSuffix(p, "/images"):
		writeJSON(w, 200, images)
	case r.Method == "DELETE" && strings.Contains(p, "/images/"):
		w.WriteHeader(204)
	case r.Method == "POST" && strings.HasSuffix(p, "/tags") && strings.Contains(p, "/images/"):
		writeJSON(w, 200, map[string]string{"image": "registry.th/acme/api:" + f.body["tag"].(string)})
	case r.Method == "POST" && strings.HasSuffix(p, "/copy"):
		writeJSON(w, 200, map[string]string{"image": "registry.th/acme/prod/api:v1"})
	case r.Method == "POST" && strings.HasSuffix(p, "/cleanup/preview"):
		writeJSON(w, 200, map[string]any{"images": 1, "sizeBytes": 1024, "items": []map[string]any{
			{"repository": "api", "digest": digC, "tags": []string{}, "sizeBytes": 1024, "reason": "untagged", "createdAt": "2026-10-01T08:00:00Z"}}})
	case r.Method == "POST" && strings.HasSuffix(p, "/cleanup/run"):
		writeJSON(w, 200, map[string]any{"images": 1, "sizeBytes": 1024, "items": []any{}})
	case r.Method == "GET" && p == "/api/v1/run/registries/r2/tokens":
		writeJSON(w, 200, []map[string]any{{"id": "t1", "name": "ci", "hint": "a1b2", "permission": "write", "createdAt": "2026-10-10T08:00:00Z"}})
	case r.Method == "POST" && p == "/api/v1/run/registries/r2/tokens":
		writeJSON(w, 201, map[string]any{"token": map[string]any{"id": "t2", "name": f.body["name"]}, "secret": "thr_t2_secret", "username": "th"})
	case r.Method == "DELETE" && p == "/api/v1/run/registries/r2/tokens/t1":
		w.WriteHeader(204)
	case r.Method == "GET" && p == "/api/v1/run/registries/builds":
		writeJSON(w, 200, []map[string]any{
			{"id": "b2", "kind": "function", "repository": "fn-hello", "status": "succeeded", "createdAt": "2026-10-10T08:00:00Z", "durationSeconds": 30, "deployedTo": []string{"hello"}},
			{"id": "b1", "kind": "container", "repository": "prod/api", "tag": "v1", "status": "succeeded", "imageGone": true, "createdAt": "2026-10-09T08:00:00Z", "deployedTo": []string{}},
		})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		writeJSON(w, 404, map[string]string{"error": "ไม่พบ"})
	}
}

func (f *fakeRegistries) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func startRegistries(t *testing.T) (*fakeRegistries, *harness) {
	f := &fakeRegistries{t: t}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, newHarness(t, srv.URL)
}

func TestRegistryList(t *testing.T) {
	_, h := startRegistries(t)
	if code := h.run("registry", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
	if lines[0] != "Registries of project [acme] · used 5.0 GiB of 20 GB (25%) · 3 of 10 registries" {
		t.Fatalf("header %q", lines[0])
	}
	if f := strings.Fields(lines[3]); f[0] != "default" || f[2] != "private" || f[3] != "mutable" || f[6] != "3.0" {
		t.Fatalf("default row %q", lines[3])
	}
	if f := strings.Fields(lines[4]); f[0] != "prod" || f[2] != "public" || f[3] != "immutable" {
		t.Fatalf("prod row %q", lines[4])
	}
}

func TestRegistryCreateAndUpdate(t *testing.T) {
	f, h := startRegistries(t)
	if code := h.run("registry", "create", "edge", "--public", "--immutable-tags", "--keep-tags", "v*,prod", "--keep-latest", "5"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	want := map[string]any{"projectId": "p1", "name": "edge", "public": true, "immutableTags": true, "cleanupKeepLatest": float64(5),
		"cleanupKeepTags": []any{"v*", "prod"}}
	if !reflect.DeepEqual(f.body, want) {
		t.Fatalf("create body %v", f.body)
	}
	if !strings.Contains(h.out.String(), "Push to: registry.th/acme/edge/<image>:<tag>") {
		t.Fatalf("out %s", h.out)
	}

	if code := h.run("registry", "update", "staging", "--no-public", "--keep-tags", "none", "--unpulled-days", "30"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	want = map[string]any{"public": false, "cleanupKeepTags": []any{}, "cleanupUnpulledDays": float64(30)}
	if f.last() != "PATCH /api/v1/run/registries/r3" || !reflect.DeepEqual(f.body, want) {
		t.Fatalf("update %s %v", f.last(), f.body)
	}
	if code := h.run("registry", "update", "staging"); code != 1 || !strings.Contains(h.errOut.String(), "nothing to change") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
}

func TestRegistryDescribe(t *testing.T) {
	_, h := startRegistries(t)
	if code := h.run("registry", "describe", "prod"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, s := range []string{"Registry prod (registry.th/acme/prod)", "Access:        public", "Tags:          immutable",
		"remove untagged images after 7 days; beyond the newest 10 per repository; always keep tags v*", "REPOSITORY"} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing %q in\n%s", s, out)
		}
	}
	if code := h.run("registry", "describe", "nope"); code != 1 || !strings.Contains(h.errOut.String(), `registry "nope" not found in project [acme]`) {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
}

func TestRegistryDelete(t *testing.T) {
	f, h := startRegistries(t)
	if code := h.run("registry", "delete", "default", "--quiet"); code != 1 || !strings.Contains(h.errOut.String(), "default registry cannot be deleted") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run("registry", "delete", "staging"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "DELETE /api/v1/run/registries/r3" {
		t.Fatalf("last %s", f.last())
	}
}

func TestRegistryImages(t *testing.T) {
	f, h := startRegistries(t)
	if code := h.run("registry", "images", "list", "prod/api"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "GET /api/v1/run/registries/r2/images?repository=api" {
		t.Fatalf("last %s", f.last())
	}
	lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "PULLS") {
		t.Fatalf("out %s", h.out)
	}
	if fl := strings.Fields(lines[1]); fl[0] != "aaaa1111bbbb" || fl[1] != "latest,v1" || fl[4] != "7" || fl[len(fl)-1] != "yes" {
		t.Fatalf("row %q", lines[1])
	}
	// The full name of an image of the default registry.
	if code := h.run("registry", "images", "list", "registry.th/acme/api"); code != 0 || f.last() != "GET /api/v1/run/registries/r1/images?repository=api" {
		t.Fatalf("exit %d %s: %s", code, f.last(), h.errOut)
	}
	if code := h.run("registry", "images", "list", "other.io/acme/api"); code != 1 || !strings.Contains(h.errOut.String(), "is not in this Project's registries (registry.th/acme)") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
}

func TestRegistryImagesDelete(t *testing.T) {
	f, h := startRegistries(t)
	if code := h.run("registry", "images", "delete", "prod/api:v1", "--quiet"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "DELETE /api/v1/run/registries/r2/images/v1?repository=api" || !strings.Contains(h.out.String(), "Deleted tag [prod/api:v1].") {
		t.Fatalf("%s: %s", f.last(), h.out)
	}
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run("registry", "images", "delete", "api@cccc000"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "DELETE /api/v1/run/registries/r1/images/"+digC+"?repository=api" {
		t.Fatalf("last %s", f.last())
	}
	if code := h.run("registry", "images", "delete", "prod/api"); code != 1 || !strings.Contains(h.errOut.String(), "give a tag or a digest") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
}

func TestRegistryImagesTagAndCopy(t *testing.T) {
	f, h := startRegistries(t)
	if code := h.run("registry", "images", "tag", "api@aaaa1111", "stable"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "POST /api/v1/run/registries/r1/images/"+digA+"/tags?repository=api" || f.body["tag"] != "stable" {
		t.Fatalf("%s %v", f.last(), f.body)
	}
	if code := h.run("registry", "images", "copy", "staging/api:v1", "prod/api"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	want := map[string]any{"registryId": "r2", "repository": "api", "tag": "v1"}
	if f.last() != "POST /api/v1/run/registries/r3/images/v1/copy?repository=api" || !reflect.DeepEqual(f.body, want) {
		t.Fatalf("%s %v", f.last(), f.body)
	}
	if !strings.Contains(h.out.String(), "Copied [staging/api:v1] to [prod/api:v1].") {
		t.Fatalf("out %s", h.out)
	}
	if code := h.run("registry", "images", "copy", "staging/api", "prod/api"); code != 1 || !strings.Contains(h.errOut.String(), "tag or digest") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
}

func TestRegistryTokens(t *testing.T) {
	f, h := startRegistries(t)
	if code := h.run("registry", "tokens", "create", "prod", "--name", "gha", "--permission", "write", "--expires-in-days", "90"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if h.out.String() != "thr_t2_secret\n" {
		t.Fatalf("stdout %q", h.out)
	}
	if !strings.Contains(h.errOut.String(), "docker login registry.th -u th --password-stdin") {
		t.Fatalf("stderr %s", h.errOut)
	}
	want := map[string]any{"name": "gha", "permission": "write", "expiresInDays": float64(90)}
	if !reflect.DeepEqual(f.body, want) {
		t.Fatalf("body %v", f.body)
	}
	if code := h.run("registry", "tokens", "create", "prod"); code != 1 {
		t.Fatalf("no --name: exit %d", code)
	}
	if code := h.run("registry", "tokens", "list", "prod"); code != 0 || !strings.Contains(h.out.String(), "…a1b2") || !strings.Contains(h.out.String(), "never") {
		t.Fatalf("exit %d: %s %s", code, h.out, h.errOut)
	}
	if code := h.run("registry", "tokens", "revoke", "prod", "ci", "--quiet"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "DELETE /api/v1/run/registries/r2/tokens/t1" {
		t.Fatalf("last %s", f.last())
	}
}

func TestRegistryCleanup(t *testing.T) {
	f, h := startRegistries(t)
	if code := h.run("registry", "cleanup", "default", "--preview", "--keep-latest", "3"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "POST /api/v1/run/registries/r1/cleanup/preview" || !reflect.DeepEqual(f.body, map[string]any{"cleanupKeepLatest": float64(3)}) {
		t.Fatalf("%s %v", f.last(), f.body)
	}
	if !strings.Contains(h.out.String(), "untagged > 7 days") || !strings.Contains(h.out.String(), "would remove 1 images (1.0 KiB)") {
		t.Fatalf("out %s", h.out)
	}
	if code := h.run("registry", "cleanup", "default", "--run", "--keep-latest", "3"); code != 1 || !strings.Contains(h.errOut.String(), "saved policy") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if code := h.run("registry", "cleanup", "default"); code != 1 || !strings.Contains(h.errOut.String(), "--preview or --run") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	h.app.Stdin = strings.NewReader("n\n")
	if code := h.run("registry", "cleanup", "default", "--run"); code != 1 || f.last() != "POST /api/v1/run/registries/r1/cleanup/preview" {
		t.Fatalf("declined: exit %d, %s", code, f.last())
	}
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run("registry", "cleanup", "default", "--run"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "POST /api/v1/run/registries/r1/cleanup/run" || !strings.Contains(h.out.String(), "Removed 1 images (1.0 KiB) from registry [default].") {
		t.Fatalf("%s: %s", f.last(), h.out)
	}
}

func TestBuildsListAllKinds(t *testing.T) {
	f, h := startRegistries(t)
	if code := h.run("builds", "list", "--status", "succeeded"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.last() != "GET /api/v1/run/registries/builds?projectId=p1&status=succeeded" {
		t.Fatalf("last %s", f.last())
	}
	lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "DEPLOYED TO") {
		t.Fatalf("out %s", h.out)
	}
	if fl := strings.Fields(lines[1]); fl[1] != "function" || fl[len(fl)-1] != "hello" {
		t.Fatalf("row %q", lines[1])
	}
	if !strings.Contains(lines[2], "prod/api:v1") || !strings.Contains(lines[2], "succeeded (image removed)") {
		t.Fatalf("row %q", lines[2])
	}
	if code := h.run("builds", "list", "--kind", "container", "--format", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var got []map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || len(got) != 1 || got[0]["id"] != "b1" {
		t.Fatalf("json %s", h.out)
	}
	if code := h.run("builds", "list", "--status", "done"); code != 1 {
		t.Fatalf("bad status: exit %d", code)
	}
}

func TestDeployIntoAnotherRegistry(t *testing.T) {
	f, h := startFake(t)
	f.services = []map[string]any{existingService()}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	if code := h.run("container", "deploy", "api", "--source", dir, "--repository", "prod/api", "--tag", "v2"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.build["repository"] != "prod/api" || f.build["tag"] != "v2" {
		t.Fatalf("build body %v", f.build)
	}
	for _, bad := range []string{"Prod/api", "default/api", "a/b/c", "prod/"} {
		if code := h.run("container", "deploy", "api", "--source", dir, "--repository", bad); code != 1 || !strings.Contains(h.errOut.String(), "--repository is IMAGE or REGISTRY/IMAGE") {
			t.Fatalf("%s: exit %d: %s", bad, code, h.errOut)
		}
	}
	if code := h.run("container", "deploy", "api", "--image", "x/y:1", "--repository", "prod/api"); code != 1 || !strings.Contains(h.errOut.String(), "--repository is for a source build") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
}
