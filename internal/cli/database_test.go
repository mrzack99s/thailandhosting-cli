package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeDB struct {
	t      *testing.T
	mu     sync.Mutex
	bodies map[string]map[string]any // "METHOD path" -> last JSON body
	calls  []string
	public bool
	hidden bool
}

func (f *fakeDB) body(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[key]
}

func (f *fakeDB) called(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == key {
			return true
		}
	}
	return false
}

func (f *fakeDB) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, key)
	if r.Body != nil && (r.Method == "POST" || r.Method == "PUT") {
		var m map[string]any
		if json.NewDecoder(r.Body).Decode(&m) == nil {
			f.bodies[key] = m
		}
	}
	switch key {
	case "GET /api/v1/projects":
		writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme"}})
	case "GET /api/v1/regions":
		writeJSON(w, 200, []map[string]any{{"id": "th-bkk", "name": "Bangkok", "isDefault": true}})
	case "GET /api/v1/services":
		if r.URL.Query().Get("kind") != "database" || r.URL.Query().Get("projectId") != "p1" {
			f.t.Errorf("services query = %s", r.URL.RawQuery)
		}
		writeJSON(w, 200, []map[string]any{
			{"id": "d1", "projectId": "p1", "slug": "maindb", "name": "maindb", "kind": "database", "engine": "postgres-17", "status": "active", "regionId": "th-bkk", "cpuQuota": 100000, "memoryMB": 2048, "port": 5432, "internalDns": "maindb.acme.internal"},
			{"id": "d2", "projectId": "p1", "slug": "sessions", "name": "sessions", "kind": "database", "engine": "valkey-9", "status": "active", "regionId": "th-bkk", "cpuQuota": 50000, "memoryMB": 512, "port": 6379, "internalDns": "sessions.acme.internal"},
			{"id": "d3", "projectId": "p1", "slug": "shop", "name": "shop", "kind": "database", "engine": "mysql-8.4", "status": "stopped", "regionId": "th-bkk", "cpuQuota": 50000, "memoryMB": 1024, "port": 3306},
			{"id": "d4", "projectId": "p1", "slug": "maindb-replica", "name": "r", "kind": "database", "engine": "postgres-17", "status": "active", "replicaOfServiceId": "d1"},
		})
	case "GET /api/v1/databases/engines":
		writeJSON(w, 200, []map[string]any{
			{"id": "postgres-18", "family": "postgres", "protocol": "postgres", "name": "PostgreSQL", "version": "18", "product": "database", "available": true},
			{"id": "postgres-17", "family": "postgres", "protocol": "postgres", "name": "PostgreSQL", "version": "17", "default": true, "product": "database", "available": true},
			{"id": "mysql-8.4", "family": "mysql", "protocol": "mysql", "name": "MySQL", "version": "8.4 LTS", "default": true, "product": "database", "available": true},
			{"id": "redis-7.2", "family": "redis", "protocol": "redis", "name": "Redis", "version": "7.2", "default": true, "product": "cache", "available": true},
			{"id": "valkey-9", "family": "valkey", "protocol": "redis", "name": "Valkey", "version": "9", "default": true, "product": "cache", "available": true},
			{"id": "valkey-8", "family": "valkey", "protocol": "redis", "name": "Valkey", "version": "8.1", "product": "cache", "available": true},
		})
	case "GET /api/v1/service-catalog":
		writeJSON(w, 200, map[string]any{"sizes": map[string]any{
			"database": []map[string]any{{"id": "db-s", "name": "Small", "series": "standard", "cpuQuota": 100000, "memoryMB": 2048, "minStorageGb": 10, "maxStorageGb": 500}},
			"cache":    []map[string]any{{"id": "cache-b1", "name": "Burst 1", "series": "burstable", "cpuQuota": 25000, "burstCpuQuota": 100000, "memoryMB": 1024}},
		}})
	case "POST /api/v1/services":
		writeJSON(w, 201, map[string]any{"id": "new", "slug": f.bodies[key]["slug"], "status": "deploying", "regionId": "th-bkk", "cpuQuota": 100000, "memoryMB": 2048})
	case "GET /api/v1/services/d1/connection", "PUT /api/v1/services/d1/connectivity":
		writeJSON(w, 200, f.pgConn(key))
	case "GET /api/v1/services/d2/connection":
		writeJSON(w, 200, map[string]any{
			"engine":      map[string]any{"id": "valkey-9", "family": "valkey", "protocol": "redis", "name": "Valkey", "version": "9"},
			"credentials": map[string]any{"user": "default", "password": "vk-secret"},
			"private":     map[string]any{"dns": "sessions.acme.internal", "port": 6379, "url": "redis://default:********@sessions.acme.internal:6379"},
			"public": map[string]any{"enabled": true, "available": true, "hostname": "sessions.databases.th-bkk.thailandhosting.com", "port": 6380,
				"loginUser": "default@sessions", "sniRequired": true, "url": "rediss://default:********@sessions.databases.th-bkk.thailandhosting.com:6380",
				"allowedCidrs": []string{"203.0.113.10/32"}, "bandwidthMbps": 10},
		})
	case "GET /api/v1/services/d1/backups":
		writeJSON(w, 200, map[string]any{
			"backups": []map[string]any{
				{"id": "b2", "name": "manual-2", "kind": "manual", "status": "completed", "sizeBytes": 5 << 20, "createdAt": "2026-10-12T02:00:00Z"},
				{"id": "b1", "name": "nightly-1", "kind": "automated", "status": "completed", "sizeBytes": 4 << 20, "createdAt": "2026-10-11T02:00:00Z"},
			},
			"policy":           map[string]any{"enabled": true, "frequencyHours": 24, "windowHour": 2, "retentionCount": 7},
			"restoreSupported": true,
		})
	case "POST /api/v1/services/d1/backups":
		writeJSON(w, 202, map[string]any{"id": "b3", "status": "in_progress"})
	case "POST /api/v1/services/d1/backups/b1/restore":
		writeJSON(w, 200, map[string]string{"status": "restored"})
	case "DELETE /api/v1/services/d3":
		writeJSON(w, 409, map[string]string{"error": "บริการนี้เปิดการป้องกันการลบอยู่ — ปิดการป้องกันในหน้าตั้งค่าก่อนลบ"})
	default:
		f.t.Errorf("unexpected %s", key)
		writeJSON(w, 404, map[string]string{"error": "ไม่พบ"})
	}
}

func (f *fakeDB) pgConn(key string) map[string]any {
	enabled := f.public
	cidrs := []any{}
	bw := 10
	if b, ok := f.bodies[key]; ok && strings.HasPrefix(key, "PUT") {
		enabled = b["publicEnabled"].(bool)
		cidrs = b["allowedCidrs"].([]any)
		bw = int(b["bandwidthMbps"].(float64))
	}
	pw := "p@ss/w0rd"
	if f.hidden {
		pw = ""
	}
	return map[string]any{
		"engine":       map[string]any{"id": "postgres-17", "family": "postgres", "protocol": "postgres", "name": "PostgreSQL", "version": "17"},
		"storageBytes": 10 << 30, "diskType": "ssd",
		"credentials": map[string]any{"user": "postgres", "password": pw, "database": "maindb", "passwordHidden": f.hidden},
		"private":     map[string]any{"dns": "maindb.acme.internal", "port": 5432, "url": "postgresql://postgres:********@maindb.acme.internal:5432/maindb?sslmode=disable"},
		"public": map[string]any{"enabled": enabled, "available": true, "hostname": "maindb.databases.th-bkk.thailandhosting.com", "port": 5433,
			"loginUser": "postgres@maindb", "url": "postgresql://postgres:********@maindb.databases.th-bkk.thailandhosting.com:5433/maindb?sslmode=require",
			"allowedCidrs": cidrs, "bandwidthMbps": bw},
	}
}

func startDB(t *testing.T) (*fakeDB, *harness) {
	f := &fakeDB{t: t, bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, newHarness(t, srv.URL)
}

func TestDatabaseListFiltersByProduct(t *testing.T) {
	_, h := startDB(t)
	if code := h.run("database", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"maindb", "shop", "maindb-replica", "(replica)"} {
		if !strings.Contains(out, want) {
			t.Errorf("database list misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sessions") {
		t.Errorf("database list shows a cache:\n%s", out)
	}
	if code := h.run("cache", "list", "--format", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var caches []dbService
	if err := json.Unmarshal(h.out.Bytes(), &caches); err != nil || len(caches) != 1 || caches[0].Slug != "sessions" {
		t.Fatalf("cache list = %v %s", err, h.out)
	}
}

func TestDatabaseCreatePostgresBody(t *testing.T) {
	f, h := startDB(t)
	code := h.run("database", "create", "orders", "--engine", "postgres", "--version", "18", "--region", "th-bkk",
		"--cpu", "1", "--memory", "2Gi", "--storage", "50GB", "--disk", "hdd",
		"--public", "--allow", "203.0.113.10/32,198.51.100.0/24", "--backups", "--backup-hours", "12", "--backup-window", "3", "--backup-keep", "14")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b := f.body("POST /api/v1/services")
	want := map[string]any{"kind": "database", "engine": "postgres-18", "name": "orders", "slug": "orders", "projectId": "p1", "regionId": "th-bkk",
		"cpuQuota": 100000.0, "memoryMB": 2048.0, "storageGb": 50.0, "diskType": "hdd", "databaseProxyEnabled": true}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("%s = %v, want %v", k, b[k], v)
		}
	}
	if got := b["databaseProxyAllowedCidrs"].([]any); len(got) != 2 || got[1] != "198.51.100.0/24" {
		t.Errorf("cidrs = %v", got)
	}
	pol := b["backupPolicy"].(map[string]any)
	if pol["enabled"] != true || pol["frequencyHours"] != 12.0 || pol["windowHour"] != 3.0 || pol["retentionCount"] != 14.0 {
		t.Errorf("backupPolicy = %v", pol)
	}
	if !strings.Contains(h.out.String(), "Creating database [orders] (PostgreSQL 18)") {
		t.Errorf("out: %s", h.out)
	}
}

func TestCacheCreateValkeyBody(t *testing.T) {
	f, h := startDB(t)
	if code := h.run("cache", "create", "jobs", "--engine", "valkey", "--size", "cache-b1"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b := f.body("POST /api/v1/services")
	if b["engine"] != "valkey-9" || b["kind"] != "database" || b["cpuQuota"] != 25000.0 || b["cpuBurst"] != 100000.0 || b["memoryMB"] != 1024.0 || b["regionId"] != "th-bkk" {
		t.Fatalf("body = %v", b)
	}
	for _, k := range []string{"storageGb", "diskType", "backupPolicy", "databaseProxyEnabled"} {
		if _, ok := b[k]; ok {
			t.Errorf("cache body has %s: %v", k, b)
		}
	}
	// A database engine is refused under th cache, and --storage is not a cache flag.
	if code := h.run("cache", "create", "x", "--engine", "postgres"); code == 0 || !strings.Contains(h.errOut.String(), "th database create") {
		t.Errorf("postgres as cache: %d %s", code, h.errOut)
	}
	if code := h.run("cache", "create", "x", "--engine", "redis", "--storage", "10GB"); code == 0 {
		t.Errorf("--storage accepted for cache")
	}
}

func TestDatabaseCredentialsRevealsPassword(t *testing.T) {
	f, h := startDB(t)
	f.public = true
	if code := h.run("database", "credentials", "maindb"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{
		"Password:     p@ss/w0rd",
		"postgresql://postgres:p%40ss%2Fw0rd@maindb.acme.internal:5432/maindb?sslmode=disable",
		"postgresql://postgres:p%40ss%2Fw0rd@maindb.databases.th-bkk.thailandhosting.com:5433/maindb?sslmode=require",
		"postgres@maindb",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "********") {
		t.Errorf("mask left in output:\n%s", out)
	}
	// Viewers: no password, URLs stay masked.
	f.hidden = true
	if code := h.run("database", "credentials", "maindb", "--format", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var j map[string]any
	json.Unmarshal(h.out.Bytes(), &j)
	if _, has := j["password"]; has || j["passwordHidden"] != true || !strings.Contains(j["publicUrl"].(string), "********") {
		t.Fatalf("hidden json = %v", j)
	}
}

type execCall struct {
	bin  string
	args []string
	env  []string
}

func fakeExec(t *testing.T, installed map[string]bool) *[]execCall {
	var calls []execCall
	oldLook, oldExec := dbLookPath, dbExec
	t.Cleanup(func() { dbLookPath, dbExec = oldLook, oldExec })
	dbLookPath = func(bin string) (string, error) {
		if installed[bin] {
			return "/usr/bin/" + bin, nil
		}
		return "", &dbNoBinErr{}
	}
	dbExec = func(c *cmd, bin string, args, env []string) error {
		calls = append(calls, execCall{bin, args, env})
		return nil
	}
	return &calls
}

type dbNoBinErr struct{}

func (*dbNoBinErr) Error() string { return "not found" }

func TestDatabaseConnectPsql(t *testing.T) {
	f, h := startDB(t)
	f.public = true
	calls := fakeExec(t, map[string]bool{"psql": true})
	if code := h.run("database", "connect", "maindb", "--", "-c", "select 1"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v", *calls)
	}
	c := (*calls)[0]
	if c.bin != "/usr/bin/psql" || len(c.args) != 3 || c.args[1] != "-c" || c.args[2] != "select 1" {
		t.Fatalf("args = %q", c.args)
	}
	for _, want := range []string{"host='maindb.databases.th-bkk.thailandhosting.com'", "port=5433", "user='postgres@maindb'", "dbname='maindb'", "sslmode=require"} {
		if !strings.Contains(c.args[0], want) {
			t.Errorf("conninfo %q misses %q", c.args[0], want)
		}
	}
	if strings.Contains(strings.Join(c.args, " "), "w0rd") || len(c.env) != 1 || c.env[0] != "PGPASSWORD=p@ss/w0rd" {
		t.Errorf("password handling: args %q env %q", c.args, c.env)
	}
}

func TestDatabaseConnectNeedsPublic(t *testing.T) {
	_, h := startDB(t)
	calls := fakeExec(t, map[string]bool{"psql": true})
	if code := h.run("database", "connect", "maindb"); code == 0 {
		t.Fatal("connected without a public endpoint")
	}
	if !strings.Contains(h.errOut.String(), "th database public maindb --enable --allow <your-ip>/32") || len(*calls) != 0 {
		t.Fatalf("err: %s", h.errOut)
	}
	// --private goes to the VPC address without TLS.
	if code := h.run("database", "connect", "maindb", "--private"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if a := (*calls)[0].args[0]; !strings.Contains(a, "host='maindb.acme.internal'") || !strings.Contains(a, "user='postgres'") || !strings.Contains(a, "sslmode=disable") {
		t.Fatalf("private conninfo %q", a)
	}
}

func TestCacheConnectValkeyAndMissingClient(t *testing.T) {
	_, h := startDB(t)
	calls := fakeExec(t, map[string]bool{"redis-cli": true})
	if code := h.run("cache", "connect", "sessions"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	c := (*calls)[0]
	want := "-h sessions.databases.th-bkk.thailandhosting.com -p 6380 --user default --tls --sni sessions.databases.th-bkk.thailandhosting.com"
	if c.bin != "/usr/bin/redis-cli" || strings.Join(c.args, " ") != want || len(c.env) != 1 || c.env[0] != "REDISCLI_AUTH=vk-secret" {
		t.Fatalf("call = %+v", c)
	}
	// No client: print the URL and how to install one.
	fakeExec(t, map[string]bool{})
	if code := h.run("cache", "connect", "sessions"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.out.String(), "rediss://default:vk-secret@sessions.databases.th-bkk.thailandhosting.com:6380") || !strings.Contains(h.errOut.String(), "valkey-cli is not installed") {
		t.Fatalf("out %s err %s", h.out, h.errOut)
	}
}

func TestDBConnectPlanMySQL(t *testing.T) {
	conn := &dbConnection{}
	conn.Engine = dbEngine{Family: "mariadb", Protocol: "mysql"}
	conn.Credentials.User, conn.Credentials.Password, conn.Credentials.Database = "app", "pw", "shop"
	conn.Public.Hostname, conn.Public.Port, conn.Public.LoginUser = "shop.db.example", 3307, "app@shop"
	plan := dbConnectPlan(conn, true, nil)
	if len(plan.Candidates) != 2 || plan.Candidates[0].Bin != "mariadb" {
		t.Fatalf("candidates = %+v", plan.Candidates)
	}
	if got := strings.Join(plan.Candidates[0].Args, " "); got != "-h shop.db.example -P 3307 -u app@shop --ssl shop" {
		t.Errorf("mariadb args %q", got)
	}
	if got := strings.Join(plan.Candidates[1].Args, " "); got != "-h shop.db.example -P 3307 -u app@shop --ssl-mode=REQUIRED shop" {
		t.Errorf("mysql args %q", got)
	}
	if len(plan.Env) != 1 || plan.Env[0] != "MYSQL_PWD=pw" {
		t.Errorf("env %q", plan.Env)
	}
}

func TestDatabasePublicToggle(t *testing.T) {
	f, h := startDB(t)
	if code := h.run("database", "public", "maindb", "--enable", "--allow", "203.0.113.10/32", "--bandwidth", "50"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b := f.body("PUT /api/v1/services/d1/connectivity")
	if b["publicEnabled"] != true || b["bandwidthMbps"] != 50.0 || len(b["allowedCidrs"].([]any)) != 1 {
		t.Fatalf("body = %v", b)
	}
	if !strings.Contains(h.out.String(), "is on: maindb.databases.th-bkk.thailandhosting.com:5433") || !strings.Contains(h.out.String(), "203.0.113.10/32") {
		t.Fatalf("out: %s", h.out)
	}
	// --disable keeps the list and bandwidth as they are.
	f.public = true
	if code := h.run("database", "public", "maindb", "--disable"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b = f.body("PUT /api/v1/services/d1/connectivity")
	if b["publicEnabled"] != false || b["bandwidthMbps"] != 10.0 || b["allowedCidrs"] == nil {
		t.Fatalf("disable body = %v", b)
	}
	if code := h.run("database", "public", "maindb"); code == 0 {
		t.Error("public with no flags succeeded")
	}
}

func TestDatabaseBackups(t *testing.T) {
	f, h := startDB(t)
	if code := h.run("database", "backups", "list", "maindb"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "b2") || !strings.Contains(h.out.String(), "5.0 MiB") || !strings.Contains(h.errOut.String(), "daily from 02:00") {
		t.Fatalf("out %s err %s", h.out, h.errOut)
	}
	if code := h.run("database", "backups", "create", "maindb"); code != 0 || !f.called("POST /api/v1/services/d1/backups") {
		t.Fatalf("create: %d %s", code, h.errOut)
	}
	// Restore asks first; "n" aborts without calling the API.
	h.app.Stdin = strings.NewReader("n\n")
	if code := h.run("database", "backups", "restore", "maindb", "nightly-1"); code != 1 || f.called("POST /api/v1/services/d1/backups/b1/restore") {
		t.Fatalf("restore without consent: %d %s", code, h.errOut)
	}
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run("database", "backups", "restore", "maindb", "nightly-1"); code != 0 || !f.called("POST /api/v1/services/d1/backups/b1/restore") {
		t.Fatalf("restore: %d %s", code, h.errOut)
	}
	if !strings.Contains(h.errOut.String(), "will be replaced with backup [b1]") || !strings.Contains(h.out.String(), "Restored database [maindb]") {
		t.Fatalf("out %s err %s", h.out, h.errOut)
	}
}

func TestDatabaseDeleteShowsServerError(t *testing.T) {
	_, h := startDB(t)
	if code := h.run("database", "delete", "shop", "--quiet"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.errOut.String(), "ป้องกันการลบ") {
		t.Fatalf("err: %s", h.errOut)
	}
}

func TestDBParseStorage(t *testing.T) {
	for in, want := range map[string]int{"10GB": 10, "10": 10, "50 gb": 50, "1TB": 1024, "20Gi": 20} {
		if got, err := dbParseStorage(in); err != nil || got != want {
			t.Errorf("%q = %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "1.5GB", "ten"} {
		if _, err := dbParseStorage(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
