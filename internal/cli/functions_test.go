package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeFunctions answers the Console API for one Container (api) and one
// function (hello) with a queue in the Project.
type fakeFunctions struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	calls    []string
	bodies   map[string]map[string]any
	created  bool
	files    map[string]string
	state    string
	logPolls int
	triggers []map[string]any
}

func newFakeFunctions(t *testing.T) *fakeFunctions {
	f := &fakeFunctions{t: t, bodies: map[string]map[string]any{}, files: map[string]string{"main.py": "def main(request):\n    return 'saved'\n"}, state: "deployed"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFunctions) services() []map[string]any {
	out := []map[string]any{{"id": "c1", "projectId": "p1", "name": "api", "slug": "api", "kind": "container", "engine": "image", "status": "active", "regionId": "th-bkk-1", "url": "https://api.example"}}
	if f.created {
		out = append(out, map[string]any{"id": "f1", "projectId": "p1", "name": "hello", "slug": "hello", "kind": "container", "engine": "function", "status": "active", "regionId": "th-bkk-1", "url": f.srv.URL + "/fn"})
	}
	return out
}

func (f *fakeFunctions) source() map[string]any {
	return map[string]any{"serviceId": "f1", "runtime": "python312", "entryPoint": "main", "files": f.files, "buildId": "b7", "deployState": f.state,
		"build": map[string]any{"id": "b7", "number": 7, "status": "succeeded"}}
}

func (f *fakeFunctions) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	key := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, key)
	if body != nil {
		f.bodies[key] = body
	}
	switch key {
	case "GET /api/v1/functions/runtimes":
		writeJSON(w, 200, []map[string]any{
			{"id": "python314", "family": "python", "version": "3.14", "name": "Python 3.14"},
			{"id": "python313", "family": "python", "version": "3.13", "name": "Python 3.13", "recommended": true},
			{"id": "nodejs24", "family": "nodejs", "version": "24", "name": "Node.js 24", "recommended": true},
			{"id": "go126", "family": "go", "version": "1.26", "name": "Go 1.26", "recommended": true},
			{"id": "go124", "family": "go", "version": "1.24", "name": "Go 1.24", "deprecated": true},
		})
	case "GET /api/v1/services/f1/function/versions":
		writeJSON(w, 200, []map[string]any{
			{"number": 2, "runtime": "python313", "status": "ready", "active": true, "message": "faster", "createdBy": "a@b.c", "createdAt": "2026-10-15T10:00:00Z"},
			{"number": 1, "runtime": "python313", "status": "failed", "error": "pip failed", "createdAt": "2026-10-15T09:00:00Z"},
		})
	case "GET /api/v1/services/f1/function/versions/1":
		writeJSON(w, 200, map[string]any{"number": 1, "runtime": "python313", "entryPoint": "main", "files": map[string]string{"main.py": "old"}})
	case "POST /api/v1/services/f1/function/versions/1/activate":
		writeJSON(w, 202, map[string]any{"rebuilding": true, "build": map[string]any{"id": "b9", "number": 9}})
	case "POST /api/v1/services/f1/function/versions/2/activate":
		writeJSON(w, 200, map[string]any{"activated": true})
	case "PUT /api/v1/services/f1/access":
		writeJSON(w, 200, map[string]any{"service": f.services()[1]})
	case "GET /api/v1/projects":
		writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme"}})
	case "GET /api/v1/regions":
		writeJSON(w, 200, []map[string]any{{"id": "th-bkk-1", "name": "Bangkok", "enabled": true}})
	case "GET /api/v1/services":
		writeJSON(w, 200, f.services())
	case "POST /api/v1/services":
		f.created = true
		writeJSON(w, 201, f.services()[1])
	case "GET /api/v1/services/f1":
		writeJSON(w, 200, map[string]any{"service": f.services()[1], "envs": []map[string]any{{"key": "MODE", "value": "prod"}}})
	case "GET /api/v1/services/f1/function":
		writeJSON(w, 200, f.source())
	case "POST /api/v1/services/f1/function/deploy":
		f.files = map[string]string{}
		for k, v := range body["files"].(map[string]any) {
			f.files[k] = v.(string)
		}
		f.state = "building"
		writeJSON(w, 202, f.source())
	case "PUT /api/v1/services/f1/function/settings":
		writeJSON(w, 200, f.services()[1])
	case "GET /api/v1/run/builds/b7/log":
		f.logPolls++
		f.state = "deployed"
		writeJSON(w, 200, map[string]any{"text": "✓ Build สำเร็จ\n", "offset": 20, "done": true, "status": "succeeded"})
	case "POST /fn/hook":
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"got":` + mustJSON(body) + `}`))
	case "DELETE /api/v1/services/f1":
		w.WriteHeader(204)
	case "GET /api/v1/services/c1/triggers":
		writeJSON(w, 200, map[string]any{"triggers": f.triggers, "queues": []map[string]any{{"id": "q1", "name": "jobs", "regionId": "th-bkk-1"}}})
	case "POST /api/v1/services/c1/triggers":
		t := map[string]any{"id": "t1", "enabled": true, "queueName": "jobs", "timeZone": "Asia/Bangkok", "path": "/"}
		for k, v := range body {
			t[k] = v
		}
		f.triggers = append(f.triggers, t)
		writeJSON(w, 201, t)
	case "PUT /api/v1/services/c1/triggers/t1":
		for k, v := range body {
			f.triggers[0][k] = v
		}
		writeJSON(w, 200, f.triggers[0])
	case "POST /api/v1/services/c1/triggers/t1/run":
		w.WriteHeader(202)
	case "DELETE /api/v1/services/c1/triggers/t1":
		f.triggers = nil
		w.WriteHeader(204)
	default:
		f.t.Errorf("unexpected %s", key)
		writeJSON(w, 404, map[string]string{"error": "not found"})
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFunctionsDeployCreatesAndWaits(t *testing.T) {
	f := newFakeFunctions(t)
	h := newHarness(t, f.srv.URL)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"main.py": "def main(request):\n    return 'hi'\n", "requirements.txt": "requests==2.32.3\n", "lib/util.py": "X = 1\n",
		"Dockerfile": "FROM x", ".thignore": "notes.txt\n", "notes.txt": "private", "__pycache__/x.pyc": "\x00",
	})
	if code := h.run("functions", "deploy", "hello", "--source", dir, "--env", "MODE=prod", "--max-instances", "5"); code != 0 {
		t.Fatalf("deploy: %d\n%s%s", code, h.out, h.errOut)
	}
	create := f.bodies["POST /api/v1/services"]
	if create["engine"] != "function" || create["functionRuntime"] != "python313" || create["slug"] != "hello" || create["maxReplicas"] != float64(5) {
		t.Fatalf("create body %v", create)
	}
	if env, _ := create["env"].(map[string]any); env["MODE"] != "prod" {
		t.Fatalf("create env %v", create["env"])
	}
	want := map[string]bool{"main.py": true, "requirements.txt": true, "lib/util.py": true, ".thignore": true}
	if len(f.files) != len(want) {
		t.Fatalf("uploaded %v", f.files)
	}
	for k := range f.files {
		if !want[k] {
			t.Fatalf("uploaded %s", k)
		}
	}
	if !strings.Contains(h.out.String(), "Function [hello] is deployed.") || !strings.Contains(h.errOut.String(), "Build สำเร็จ") {
		t.Fatalf("output:\n%s\n%s", h.out, h.errOut)
	}
	for _, c := range f.calls {
		if c == "PUT /api/v1/services/f1/function/settings" {
			t.Fatal("settings sent for a new function")
		}
	}

	// Redeploying an existing function sends settings first and refuses a new runtime.
	if code := h.run("functions", "deploy", "hello", "--source", dir, "--timeout", "30s"); code != 0 {
		t.Fatalf("redeploy: %d %s", code, h.errOut)
	}
	if s := f.bodies["PUT /api/v1/services/f1/function/settings"]; s["requestTimeoutSeconds"] != float64(30) {
		t.Fatalf("settings %v", s)
	}
	if code := h.run("functions", "deploy", "hello", "--source", dir, "--runtime", "go124"); code == 0 || !strings.Contains(h.errOut.String(), "existing functions only") {
		t.Fatalf("deprecated runtime: %d %s", code, h.errOut)
	}
	if code := h.run("functions", "deploy", "hello", "--source", dir, "--runtime", "python314", "--message", "py 3.14", "--memory", "1Gi"); code != 0 {
		t.Fatalf("runtime + size: %d %s", code, h.errOut)
	}
	if b := f.bodies["POST /api/v1/services/f1/function/deploy"]; b["runtime"] != "python314" || b["message"] != "py 3.14" {
		t.Fatalf("deploy body %v", b)
	}
	if s := f.bodies["PUT /api/v1/services/f1/function/settings"]; s["memoryMB"] != float64(1024) || s["cpuQuota"] == nil {
		t.Fatalf("size settings %v", s)
	}
	if code := h.run("functions", "deploy", "api", "--source", dir); code == 0 || !strings.Contains(h.errOut.String(), "is a Container") {
		t.Fatalf("deploy onto a container: %d %s", code, h.errOut)
	}
	if code := h.run("container", "deploy", "hello", "--image", "nginx"); code == 0 || !strings.Contains(h.errOut.String(), "th functions deploy hello") {
		t.Fatalf("container deploy onto a function: %d %s", code, h.errOut)
	}
}

func TestFunctionsListPullCallDelete(t *testing.T) {
	f := newFakeFunctions(t)
	f.created = true
	h := newHarness(t, f.srv.URL)
	if code := h.run("functions", "list"); code != 0 || !strings.Contains(h.out.String(), "hello") || strings.Contains(h.out.String(), "api ") {
		t.Fatalf("list: %d\n%s", code, h.out)
	}
	if code := h.run("container", "services", "list"); code != 0 || strings.Contains(h.out.String(), "hello") {
		t.Fatalf("container list shows the function:\n%s", h.out)
	}
	if code := h.run("functions", "describe", "hello"); code != 0 || !strings.Contains(h.out.String(), "Runtime:      python312") || !strings.Contains(h.out.String(), "main.py") {
		t.Fatalf("describe:\n%s%s", h.out, h.errOut)
	}

	dir := filepath.Join(t.TempDir(), "hello")
	if code := h.run("functions", "pull", "hello", "--dir", dir); code != 0 {
		t.Fatalf("pull: %s", h.errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "main.py")); !strings.Contains(string(b), "saved") {
		t.Fatalf("pulled %q", b)
	}
	os.WriteFile(filepath.Join(dir, "main.py"), []byte("local edit"), 0o644)
	if code := h.run("functions", "pull", "hello", "--dir", dir); code == 0 || !strings.Contains(h.errOut.String(), "--force") {
		t.Fatalf("pull over an edit: %d %s", code, h.errOut)
	}
	if code := h.run("functions", "pull", "hello", "--dir", dir, "--force"); code != 0 {
		t.Fatalf("pull --force: %s", h.errOut)
	}

	if code := h.run("functions", "call", "hello", "--path", "/hook", "--data", `{"a":1}`); code != 0 || !strings.Contains(h.out.String(), `"got":{"a":1}`) {
		t.Fatalf("call: %d\n%s%s", code, h.out, h.errOut)
	}
	if code := h.run("functions", "delete", "hello", "--quiet"); code != 0 || !strings.Contains(h.out.String(), "Deleted function [hello]") {
		t.Fatalf("delete: %s", h.errOut)
	}
	if code := h.run("functions", "describe", "api"); code == 0 || !strings.Contains(h.errOut.String(), "is a Container") {
		t.Fatalf("describe a container: %s", h.errOut)
	}
}

func TestFunctionVersions(t *testing.T) {
	f := newFakeFunctions(t)
	f.created = true
	h := newHarness(t, f.srv.URL)
	if code := h.run("functions", "versions", "list", "hello"); code != 0 || !strings.Contains(h.out.String(), "serving") || !strings.Contains(h.out.String(), "pip failed") {
		t.Fatalf("list:\n%s%s", h.out, h.errOut)
	}
	if code := h.run("functions", "versions", "activate", "hello", "v2"); code != 0 || !strings.Contains(h.out.String(), "now serves v2") {
		t.Fatalf("activate: %s %s", h.out, h.errOut)
	}
	if code := h.run("functions", "versions", "activate", "hello", "1"); code != 0 || !strings.Contains(h.out.String(), "building its code again") {
		t.Fatalf("rebuild: %s %s", h.out, h.errOut)
	}
	if code := h.run("functions", "versions", "activate", "hello", "latest"); code == 0 {
		t.Fatal("bad version accepted")
	}
	dir := t.TempDir()
	if code := h.run("functions", "pull", "hello", "--version", "v1", "--dir", dir); code != 0 {
		t.Fatalf("pull version: %s", h.errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "main.py")); string(b) != "old" {
		t.Fatalf("pulled %q", b)
	}
}

func TestReadFunctionDirLimits(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"main.py": "x", "bad name.py": "x"})
	if _, err := readFunctionDir(dir); err == nil || !strings.Contains(err.Error(), "bad name.py") {
		t.Fatalf("bad name: %v", err)
	}
	dir = t.TempDir()
	writeFiles(t, dir, map[string]string{"main.py": "x", "logo.png": "\x89PNG\x00"})
	if _, err := readFunctionDir(dir); err == nil || !strings.Contains(err.Error(), "logo.png") {
		t.Fatalf("binary: %v", err)
	}
	for files, want := range map[string]string{"go.mod": "go", "index.ts": "nodejs", "requirements.txt": "python"} {
		if got := detectFamily(map[string]string{files: "x"}); got != want {
			t.Errorf("%s: %s", files, got)
		}
	}
	rts := []fnRuntime{{ID: "python313", Family: "python", Recommended: true}, {ID: "python314", Family: "python"}, {ID: "nodejs24", Family: "nodejs", Recommended: true}}
	for flag, want := range map[string]string{"": "python313", "python": "python313", "python314": "python314", "node": "nodejs24"} {
		if got, err := pickRuntime(rts, flag, map[string]string{"main.py": "x"}); err != nil || got != want {
			t.Errorf("--runtime %q: %s %v", flag, got, err)
		}
	}
}

func TestTriggers(t *testing.T) {
	f := newFakeFunctions(t)
	h := newHarness(t, f.srv.URL)
	if code := h.run("triggers", "create", "worker", "--service", "api", "--queue", "nope"); code == 0 || !strings.Contains(h.errOut.String(), "jobs") {
		t.Fatalf("unknown queue: %s", h.errOut)
	}
	if code := h.run("triggers", "create", "worker", "--service", "api"); code == 0 || !strings.Contains(h.errOut.String(), "--schedule") {
		t.Fatalf("no kind: %s", h.errOut)
	}
	if code := h.run("triggers", "create", "worker", "--service", "api", "--queue", "jobs", "--concurrency", "4", "--path", "/jobs"); code != 0 {
		t.Fatalf("create: %s", h.errOut)
	}
	if b := f.bodies["POST /api/v1/services/c1/triggers"]; b["kind"] != "queue" || b["queueId"] != "q1" || b["concurrency"] != float64(4) || b["path"] != "/jobs" {
		t.Fatalf("create body %v", b)
	}
	if code := h.run("triggers", "list", "--service", "api"); code != 0 || !strings.Contains(h.out.String(), "worker") || !strings.Contains(h.out.String(), "queue jobs") {
		t.Fatalf("list:\n%s%s", h.out, h.errOut)
	}
	if code := h.run("triggers", "pause", "worker", "--service", "api"); code != 0 || f.bodies["PUT /api/v1/services/c1/triggers/t1"]["enabled"] != false {
		t.Fatalf("pause: %s %v", h.errOut, f.bodies["PUT /api/v1/services/c1/triggers/t1"])
	}
	if code := h.run("triggers", "update", "worker", "--service", "api", "--schedule", "* * * * *"); code == 0 {
		t.Fatal("schedule on a queue trigger accepted")
	}
	if code := h.run("triggers", "update", "worker", "--service", "api", "--concurrency", "8"); code != 0 || f.bodies["PUT /api/v1/services/c1/triggers/t1"]["concurrency"] != float64(8) {
		t.Fatalf("update: %s", h.errOut)
	}
	if code := h.run("triggers", "run", "worker", "--service", "api"); code != 0 {
		t.Fatalf("run: %s", h.errOut)
	}
	if code := h.run("triggers", "delete", "worker", "--service", "api", "--quiet"); code != 0 || f.triggers != nil {
		t.Fatalf("delete: %s", h.errOut)
	}
	if code := h.run("triggers", "delete", "worker", "--service", "api", "--quiet"); code == 0 || !strings.Contains(h.errOut.String(), "not found") {
		t.Fatalf("delete twice: %s", h.errOut)
	}
}
