package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/config"
)

type harness struct {
	t      *testing.T
	app    *App
	out    *bytes.Buffer
	errOut *bytes.Buffer
	cfg    string
	env    map[string]string
	sleeps []time.Duration
}

func newHarness(t *testing.T, endpoint string) *harness {
	h := &harness{t: t, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, env: map[string]string{}}
	h.cfg = filepath.Join(t.TempDir(), "th", "config.json")
	h.app = &App{
		Version:        "test",
		Stdout:         h.out,
		Stderr:         h.errOut,
		Stdin:          strings.NewReader(""),
		Getenv:         func(k string) string { return h.env[k] },
		ConfigPath:     h.cfg,
		OpenBrowser:    func(string) error { return nil },
		Sleep:          func(d time.Duration) { h.sleeps = append(h.sleeps, d) },
		PollInterval:   time.Millisecond,
		RunDockerLogin: func(string, string) error { return nil },
	}
	if endpoint != "" {
		if err := config.Save(h.cfg, config.Config{Endpoint: endpoint, Token: "thc_test", Email: "a@b.c", Project: "acme"}); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.errOut.Reset()
	return h.app.Run(args)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func TestDeviceLoginPolling(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/cli/device":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if !strings.HasPrefix(in["clientName"], "th test") {
				t.Errorf("clientName = %q", in["clientName"])
			}
			writeJSON(w, 200, map[string]any{"deviceCode": "dc1", "userCode": "ABCD-EFGH", "verificationUrl": "https://c/cli/authorize",
				"verificationUrlComplete": "https://c/cli/authorize?code=ABCD-EFGH", "interval": 5, "expiresIn": 600})
		case "/api/v1/cli/token":
			polls++
			switch polls {
			case 1:
				writeJSON(w, 400, map[string]string{"error": "authorization_pending", "message": "waiting"})
			case 2:
				writeJSON(w, 400, map[string]string{"error": "slow_down", "message": "slow"})
			case 3:
				writeJSON(w, 400, map[string]string{"error": "authorization_pending"})
			default:
				writeJSON(w, 200, map[string]string{"token": "thc_new", "email": "me@example.com", "accountId": "acc1"})
			}
		case "/api/v1/projects":
			if r.Header.Get("Authorization") != "Bearer thc_new" {
				t.Errorf("projects auth = %q", r.Header.Get("Authorization"))
			}
			writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "first", "name": "First"}, {"id": "p2", "slug": "second"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	h := newHarness(t, "")
	h.env["TH_ENDPOINT"] = srv.URL
	var opened string
	h.app.OpenBrowser = func(u string) error { opened = u; return nil }
	if code := h.run("auth", "login"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if opened != "https://c/cli/authorize?code=ABCD-EFGH" {
		t.Fatalf("opened %q", opened)
	}
	if !strings.Contains(h.errOut.String(), "ABCD-EFGH") {
		t.Fatalf("user code not shown: %s", h.errOut)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second}
	if fmt.Sprint(h.sleeps) != fmt.Sprint(want) {
		t.Fatalf("sleeps = %v, want %v (slow_down adds 5s)", h.sleeps, want)
	}
	cfg, err := config.Load(h.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "thc_new" || cfg.Email != "me@example.com" || cfg.Project != "first" {
		t.Fatalf("config = %+v", cfg)
	}
	if !strings.Contains(h.out.String(), "[first]") {
		t.Fatalf("project pick not reported: %s", h.out)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(h.cfg); st.Mode().Perm() != 0o600 {
			t.Fatalf("config mode %o", st.Mode().Perm())
		}
	}
}

func TestDeviceLoginDeniedAndExpired(t *testing.T) {
	for code, wantMsg := range map[string]string{"access_denied": "denied", "expired_token": "expired"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/cli/device" {
				writeJSON(w, 200, map[string]any{"deviceCode": "d", "userCode": "U", "verificationUrlComplete": "https://x", "interval": 1, "expiresIn": 60})
				return
			}
			writeJSON(w, 400, map[string]string{"error": code})
		}))
		h := newHarness(t, "")
		h.env["TH_ENDPOINT"] = srv.URL
		if c := h.run("auth", "login", "--no-browser"); c != 1 || !strings.Contains(h.errOut.String(), wantMsg) {
			t.Errorf("%s: exit %d, %s", code, c, h.errOut)
		}
		srv.Close()
	}
}

// fakeAPI is an in-memory Console API for deploy tests.
type fakeAPI struct {
	t        *testing.T
	mu       sync.Mutex
	services []map[string]any
	calls    []string
	created  map[string]any
	revision map[string]any
	build    map[string]any
	upload   map[string]string // files in the uploaded tarball
	logPolls int
	access   map[string]any
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer thc_test" {
		writeJSON(w, 401, map[string]string{"error": "ต้องเข้าสู่ระบบ"})
		return
	}
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "th/test (") {
		f.t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
	}
	p := r.URL.Path
	switch {
	case r.Method == "GET" && p == "/api/v1/projects":
		writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme", "name": "Acme"}})
	case r.Method == "GET" && p == "/api/v1/regions":
		writeJSON(w, 200, []map[string]any{{"id": "bkk", "name": "Bangkok", "isDefault": true}})
	case r.Method == "GET" && p == "/api/v1/services":
		if r.URL.Query().Get("kind") != "container" || r.URL.Query().Get("projectId") != "p1" {
			f.t.Errorf("services query = %s", r.URL.RawQuery)
		}
		writeJSON(w, 200, f.services)
	case r.Method == "POST" && p == "/api/v1/services":
		json.NewDecoder(r.Body).Decode(&f.created)
		writeJSON(w, 201, map[string]any{"id": "s-new", "slug": f.created["slug"], "projectId": "p1", "kind": "container",
			"servingRevisionId": "r1", "url": "https://api.run.bkk.thailandhosting.com"})
	case r.Method == "GET" && p == "/api/v1/services/s-new/revisions":
		writeJSON(w, 200, []map[string]any{{"id": "r1", "name": "api-00001", "number": 1}})
	case r.Method == "GET" && p == "/api/v1/services/s1":
		writeJSON(w, 200, map[string]any{"service": f.services[0], "envs": []any{}})
	case r.Method == "POST" && p == "/api/v1/services/s1/revisions":
		json.NewDecoder(r.Body).Decode(&f.revision)
		svc := f.services[0]
		svc["url"] = "https://api.run.bkk.thailandhosting.com"
		writeJSON(w, 201, map[string]any{"service": svc, "revision": map[string]any{"id": "r7", "name": "api-00007", "number": 7}})
	case r.Method == "PUT" && p == "/api/v1/services/s1/access":
		json.NewDecoder(r.Body).Decode(&f.access)
		writeJSON(w, 200, map[string]any{"service": f.services[0]})
	case r.Method == "POST" && p == "/api/v1/run/sources":
		if r.URL.Query().Get("projectId") != "p1" || r.Header.Get("Content-Type") != "application/gzip" {
			f.t.Errorf("sources: %s %s", r.URL.RawQuery, r.Header.Get("Content-Type"))
		}
		f.upload = map[string]string{}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			f.t.Errorf("upload not gzip: %v", err)
			writeJSON(w, 400, map[string]string{"error": "bad"})
			return
		}
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(tr)
			f.upload[hdr.Name] = string(b)
		}
		writeJSON(w, 201, map[string]any{"sourceId": "src1", "sizeBytes": r.ContentLength})
	case r.Method == "POST" && p == "/api/v1/run/builds":
		json.NewDecoder(r.Body).Decode(&f.build)
		writeJSON(w, 201, map[string]any{"id": "b1", "status": "queued"})
	case r.Method == "GET" && p == "/api/v1/run/builds/b1/log":
		f.logPolls++
		off := r.URL.Query().Get("offset")
		switch off {
		case "0":
			writeJSON(w, 200, map[string]any{"text": "Step 1/2\n", "offset": 9, "done": false, "status": "running"})
		case "9":
			if f.logPolls < 3 {
				writeJSON(w, 200, map[string]any{"text": "", "offset": 9, "done": false, "status": "running"})
				return
			}
			writeJSON(w, 200, map[string]any{"text": "Step 2/2\n", "offset": 18, "done": true, "status": "succeeded"})
		default:
			f.t.Errorf("unexpected offset %s", off)
		}
	case r.Method == "GET" && p == "/api/v1/run/builds/b1":
		writeJSON(w, 200, map[string]any{"id": "b1", "status": "succeeded", "image": "registry.th/acme/api@sha256:abc"})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		writeJSON(w, 404, map[string]string{"error": "ไม่พบ"})
	}
}

func startFake(t *testing.T) (*fakeAPI, *harness) {
	f := &fakeAPI{t: t}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, newHarness(t, srv.URL)
}

func existingService() map[string]any {
	return map[string]any{"id": "s1", "projectId": "p1", "kind": "container", "name": "api", "slug": "api", "regionId": "bkk",
		"image": "registry.th/acme/api@sha256:old", "port": 3000, "cpuQuota": 50000, "memoryMB": 512, "computeClass": "general",
		"minReplicas": 0, "maxReplicas": 3, "containerConcurrency": 80, "requestTimeoutSeconds": 300, "healthCheckPath": "/hz",
		"command": []string{"node"}, "args": []string{"server.js"}, "ingress": "all", "allowUnauthenticated": true}
}

func TestDeployCreatesService(t *testing.T) {
	f, h := startFake(t)
	h.env["TH_REGION"] = "Bangkok"
	code := h.run("container", "deploy", "api", "--image", "ghcr.io/acme/api:1.4", "--port", "3000", "--cpu", "0.5", "--memory", "1Gi",
		"--env", "A=1", "--secret-env", "KEY=s", "--min-instances", "0", "--max-instances", "4", "--timeout", "60s", "--no-allow-unauthenticated")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	c := f.created
	if c["kind"] != "container" || c["engine"] != "image" || c["name"] != "api" || c["slug"] != "api" || c["projectId"] != "p1" ||
		c["regionId"] != "bkk" || c["image"] != "ghcr.io/acme/api:1.4" || c["port"] != 3000.0 || c["cpuQuota"] != 50000.0 ||
		c["memoryMB"] != 1024.0 || c["minReplicas"] != 0.0 || c["maxReplicas"] != 4.0 || c["requestTimeoutSeconds"] != 60.0 ||
		c["allowUnauthenticated"] != false {
		t.Fatalf("create body = %v", c)
	}
	if env := c["env"].(map[string]any); env["A"] != "1" || env["KEY"] != "s" {
		t.Fatalf("env = %v", c["env"])
	}
	want := "Service [api] revision [api-00001] has been deployed and is serving 100 percent of traffic.\nService URL: https://api.run.bkk.thailandhosting.com\n"
	if h.out.String() != want {
		t.Fatalf("stdout = %q", h.out)
	}
}

func TestDeployNewServiceUsesDefaultRegion(t *testing.T) {
	f, h := startFake(t)
	if code := h.run("container", "deploy", "api", "--image", "x/y:1"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.errOut.String(), "Using the default region [bkk]") || f.created["regionId"] != "bkk" {
		t.Fatalf("stderr = %s, created = %v", h.errOut, f.created)
	}
}

func TestDeployNewRevisionMergesSpec(t *testing.T) {
	f, h := startFake(t)
	f.services = []map[string]any{existingService()}
	code := h.run("container", "deploy", "api", "--image", "ghcr.io/acme/api:2", "--memory", "1Gi", "--message", "v2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	r := f.revision
	if r["image"] != "ghcr.io/acme/api:2" || r["memoryMB"] != 1024.0 || r["cpuQuota"] != 50000.0 || r["port"] != 3000.0 ||
		r["computeClass"] != "general" || r["maxReplicas"] != 3.0 || r["minReplicas"] != 0.0 || r["healthCheckPath"] != "/hz" ||
		r["requestTimeoutSeconds"] != 300.0 || r["message"] != "v2" {
		t.Fatalf("revision body = %v", r)
	}
	if _, ok := r["env"]; ok {
		t.Fatal("env must be left out when no env flags are given")
	}
	if !strings.Contains(h.out.String(), "Service [api] revision [api-00007] has been deployed") {
		t.Fatalf("stdout = %s", h.out)
	}
	if f.access != nil {
		t.Fatal("access must not change without the flag")
	}
}

func TestDeployNewRevisionWithEnvAndAccess(t *testing.T) {
	f, h := startFake(t)
	f.services = []map[string]any{existingService()}
	code := h.run("container", "deploy", "api", "--image", "x/y:1", "--set-env-vars", "A=1,B=2", "--no-allow-unauthenticated", "--quiet")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	env, _ := f.revision["env"].(map[string]any)
	if env["A"] != "1" || env["B"] != "2" {
		t.Fatalf("env = %v", f.revision["env"])
	}
	if !strings.Contains(h.errOut.String(), "replace the service's whole set") {
		t.Fatalf("missing env warning: %s", h.errOut)
	}
	if f.access["allowUnauthenticated"] != false || f.access["ingress"] != "all" {
		t.Fatalf("access = %v", f.access)
	}
}

func TestDeployFromSource(t *testing.T) {
	f, h := startFake(t)
	f.services = []map[string]any{existingService()}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "app.js"), []byte("ok"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.env"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.env\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.js"), []byte("x"), 0o644)

	code := h.run("container", "deploy", "api", "--source", dir, "--tag", "v3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.upload["src/app.js"] != "ok" || f.upload["Dockerfile"] == "" {
		t.Fatalf("upload = %v", f.upload)
	}
	if _, ok := f.upload["secret.env"]; ok {
		t.Fatal("ignored file uploaded")
	}
	if _, ok := f.upload["node_modules/x/i.js"]; ok {
		t.Fatal("node_modules uploaded")
	}
	b := f.build
	if b["projectId"] != "p1" || b["sourceId"] != "src1" || b["repository"] != "api" || b["tag"] != "v3" || b["regionId"] != "bkk" || b["computeClass"] != "general" {
		t.Fatalf("build body = %v", b)
	}
	if f.logPolls != 3 {
		t.Fatalf("log polls = %d", f.logPolls)
	}
	if !strings.Contains(h.errOut.String(), "Step 1/2\nStep 2/2\n") || !strings.Contains(h.errOut.String(), "Uploading sources") {
		t.Fatalf("stderr = %s", h.errOut)
	}
	if f.revision["image"] != "registry.th/acme/api@sha256:abc" {
		t.Fatalf("revision image = %v", f.revision["image"])
	}
}

func TestDeploySourceInterruptLeavesBuild(t *testing.T) {
	f, h := startFake(t)
	f.services = []map[string]any{existingService()}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)
	sig := make(chan os.Signal, 1)
	sig <- os.Interrupt
	h.app.Interrupt = sig
	if code := h.run("container", "deploy", "api", "--source", dir); code != 130 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.errOut.String(), "Build continues on the server: th builds log b1") {
		t.Fatalf("stderr = %s", h.errOut)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "cancel") || strings.Contains(c, "/revisions") {
			t.Fatalf("unexpected call %s", c)
		}
	}
}

func TestUnauthorizedMessage(t *testing.T) {
	_, h := startFake(t)
	h.env["TH_TOKEN"] = "thc_wrong"
	if code := h.run("projects", "list"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.errOut.String(), "Not signed in or token expired. Run: th auth login") {
		t.Fatalf("stderr = %s", h.errOut)
	}
}

func TestServerErrorPrintedAsIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 400, map[string]string{"error": "ไม่สามารถดึง Project ได้"})
	}))
	defer srv.Close()
	h := newHarness(t, srv.URL)
	h.run("projects", "list")
	if !strings.Contains(h.errOut.String(), "ไม่สามารถดึง Project ได้") {
		t.Fatalf("stderr = %s", h.errOut)
	}
}

func TestConfigSetGet(t *testing.T) {
	h := newHarness(t, "")
	if code := h.run("config", "set", "region", "bkk"); code != 0 {
		t.Fatal(h.errOut)
	}
	if code := h.run("config", "get", "region"); code != 0 || strings.TrimSpace(h.out.String()) != "bkk" {
		t.Fatalf("get: %d %q", code, h.out)
	}
	if code := h.run("config", "set", "colour", "x"); code != 1 {
		t.Fatal("unknown key accepted")
	}
}

func TestUpdateTrafficMatchesRevision(t *testing.T) {
	revs := []map[string]any{{"id": "r3", "name": "api-00003", "number": 3}, {"id": "r2", "name": "api-00002", "number": 2}}
	served := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/projects":
			writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme"}})
		case r.URL.Path == "/api/v1/services":
			writeJSON(w, 200, []map[string]any{existingService()})
		case r.URL.Path == "/api/v1/services/s1/revisions":
			writeJSON(w, 200, revs)
		case strings.HasSuffix(r.URL.Path, "/serve"):
			served = r.URL.Path
			writeJSON(w, 200, existingService())
		}
	}))
	defer srv.Close()
	h := newHarness(t, srv.URL)
	for want, rev := range map[string]string{"r2": "2", "r3": "api-00003"} {
		if code := h.run("container", "services", "update-traffic", "api", "--to-revision", rev); code != 0 {
			t.Fatal(h.errOut)
		}
		if served != "/api/v1/services/s1/revisions/"+want+"/serve" {
			t.Fatalf("served %s", served)
		}
	}
}
