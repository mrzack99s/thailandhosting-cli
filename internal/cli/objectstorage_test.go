package cli

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/httpsig"
)

type fakeObject struct {
	data  []byte
	ctype string
	etag  string
	mod   time.Time
}

// fakeObjectStorage is the Console's object-storage API and one region's
// data plane (under /bkk) in one server.
type fakeObjectStorage struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	objects map[string]*fakeObject // "bucket/key"
	parts   map[int][]byte
	secrets map[string]string // key ID -> secret
	reject  map[string]bool   // key IDs answered 401
	issued  []string          // data keys made by the Console
	used    []string          // key IDs that signed data-plane requests
	links   []map[string]any
	rules   []objPublicRule
	deletes []string // Console bucket deletes (path + query)
	page    int      // listing page size (default 1000)
	pending *fakeObject
	badSig  int
	// allowBad lets a test send wrong signatures on purpose.
	allowBad bool
}

var sigKeyRE = regexp.MustCompile(`keyid="([^"]*)"`)
var sigCreatedRE = regexp.MustCompile(`created=(\d+)`)

func (f *fakeObjectStorage) put(key string, data []byte, ctype string) {
	sum := md5.Sum(data)
	f.objects[key] = &fakeObject{data: data, ctype: ctype, etag: hex.EncodeToString(sum[:]), mod: time.Now().UTC()}
}

func (f *fakeObjectStorage) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/api/v1/projects":
		writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme"}})
	case p == "/api/v1/cli/data-key" && r.Method == "POST":
		var in map[string]string
		json.Unmarshal(body, &in)
		if in["service"] != "object-storage" || in["projectId"] != "p1" {
			f.t.Errorf("data-key body %s", body)
		}
		id := fmt.Sprintf("k%d", len(f.issued)+1)
		f.issued = append(f.issued, id)
		f.secrets[id] = "secret-" + id
		writeJSON(w, 201, map[string]any{"keyId": id, "secret": "secret-" + id, "expiresAt": time.Now().Add(12 * time.Hour)})
	case p == "/api/v1/object-storage" && r.Method == "GET":
		if r.URL.Query().Get("projectId") != "p1" {
			f.t.Errorf("overview query %s", r.URL.RawQuery)
		}
		writeJSON(w, 200, map[string]any{
			"buckets": []map[string]any{{"id": "b1", "name": "media", "regionId": "bkk", "status": "active", "storedBytes": 2048,
				"objectCount": len(f.objects), "publicRules": f.rules, "endpoint": f.srv.URL + "/bkk", "createdAt": "2026-10-01T00:00:00Z"}},
			"keys":    []any{},
			"regions": []map[string]any{{"id": "bkk", "name": "Bangkok", "endpoint": f.srv.URL + "/bkk"}},
		})
	case p == "/api/v1/object-storage/buckets" && r.Method == "POST":
		var in map[string]string
		json.Unmarshal(body, &in)
		writeJSON(w, 201, map[string]any{"id": "b2", "name": in["name"], "regionId": in["regionId"], "endpoint": f.srv.URL + "/" + in["regionId"]})
	case strings.HasPrefix(p, "/api/v1/object-storage/buckets/b1") && r.Method == "DELETE":
		f.deletes = append(f.deletes, r.URL.RequestURI())
		if r.URL.Query().Get("force") != "true" {
			writeJSON(w, 409, map[string]string{"error": "Bucket นี้ยังมีไฟล์อยู่", "code": "bucket_not_empty"})
			return
		}
		w.WriteHeader(204)
	case p == "/api/v1/object-storage/buckets/b1/public" && r.Method == "PUT":
		var in struct {
			Rules []objPublicRule `json:"rules"`
		}
		json.Unmarshal(body, &in)
		f.rules = in.Rules
		writeJSON(w, 200, map[string]any{"rules": in.Rules})
	case strings.HasPrefix(p, "/bkk/v1/buckets/"):
		f.data(w, r, body)
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		writeJSON(w, 404, map[string]string{"error": "ไม่พบ"})
	}
}

func problem(w http.ResponseWriter, status int, code, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Error-Code", code)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"type": "tag:x:" + code, "title": title, "status": status, "code": code})
}

// verify checks the RFC 9421 signature the way the data plane does.
func (f *fakeObjectStorage) verify(r *http.Request, body []byte) (string, bool) {
	in := r.Header.Get("Signature-Input")
	m := sigKeyRE.FindStringSubmatch(in)
	cm := sigCreatedRE.FindStringSubmatch(in)
	if m == nil || cm == nil {
		f.t.Errorf("%s %s not signed: %q", r.Method, r.URL, in)
		return "", false
	}
	id := m[1]
	secret, ok := f.secrets[id]
	if !ok {
		return id, false
	}
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		if r.Header.Get("Content-Digest") != "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":" {
			f.t.Errorf("Content-Digest %q does not match the body", r.Header.Get("Content-Digest"))
		}
	}
	created, _ := strconv.ParseInt(cm[1], 10, 64)
	check, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	httpsig.Sign(check, id, secret, body, time.Unix(created, 0))
	if check.Header.Get("Signature") != r.Header.Get("Signature") {
		f.badSig++
		return id, false
	}
	return id, true
}

func (f *fakeObjectStorage) data(w http.ResponseWriter, r *http.Request, body []byte) {
	id, ok := f.verify(r, body)
	if !ok || f.reject[id] {
		problem(w, 401, "unauthorized", "Sign the request with your access key.")
		return
	}
	f.used = append(f.used, id)
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/bkk/v1/buckets/")
	parts := strings.SplitN(rest, "/", 3)
	if parts[0] != "media" {
		problem(w, 404, "bucket_not_found", "The bucket does not exist.")
		return
	}
	sub := ""
	if len(parts) > 2 {
		sub = parts[2]
	}
	switch parts[1] {
	case "objects":
		if sub == "" {
			f.list(w, r)
			return
		}
		key := r.URL.Path[len("/bkk/v1/buckets/media/objects/"):]
		f.object(w, r, key, body)
	case "copy":
		var in struct {
			From struct{ Bucket, Key string } `json:"from"`
			Key  string                       `json:"key"`
		}
		json.Unmarshal(body, &in)
		o, ok := f.objects[in.From.Bucket+"/"+in.From.Key]
		if !ok {
			problem(w, 404, "object_not_found", "The object does not exist.")
			return
		}
		f.put("media/"+in.Key, o.data, o.ctype)
		writeJSON(w, 200, map[string]any{"key": in.Key, "size": len(o.data), "etag": o.etag})
	case "links":
		var in map[string]any
		json.Unmarshal(body, &in)
		f.links = append(f.links, in)
		writeJSON(w, 200, map[string]any{"url": "https://objects.example/bkk/v1/buckets/media/objects/" + in["key"].(string) + "?signature=x", "method": in["method"], "expiresAt": "2026-10-14T10:00:00Z"})
	case "uploads":
		f.uploads(w, r, sub, body)
	default:
		problem(w, 404, "not_found", "No such API path.")
	}
}

func (f *fakeObjectStorage) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix, delim := q.Get("prefix"), q.Get("delimiter")
	var keys []string
	for k := range f.objects {
		if key := strings.TrimPrefix(k, "media/"); strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	objs := []map[string]any{}
	folders := []string{}
	seen := map[string]bool{}
	page := f.page
	if page == 0 {
		page = 1000
	}
	next := ""
	n := 0
	for _, k := range keys {
		if k <= q.Get("cursor") {
			continue
		}
		if n == page {
			next = k
			break
		}
		if delim != "" {
			if i := strings.Index(k[len(prefix):], delim); i >= 0 {
				fd := k[:len(prefix)+i+1]
				if !seen[fd] {
					seen[fd] = true
					folders = append(folders, fd)
					n++
				}
				continue
			}
		}
		o := f.objects["media/"+k]
		objs = append(objs, map[string]any{"key": k, "size": len(o.data), "etag": o.etag, "lastModified": o.mod})
		n++
	}
	out := map[string]any{"objects": objs, "folders": folders}
	if next != "" {
		// The cursor is the last key returned.
		last := ""
		if len(objs) > 0 {
			last = objs[len(objs)-1]["key"].(string)
		}
		out["nextCursor"] = last
	}
	writeJSON(w, 200, out)
}

func (f *fakeObjectStorage) object(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	full := "media/" + key
	switch r.Method {
	case "PUT":
		if r.ContentLength != int64(len(body)) {
			f.t.Errorf("Content-Length %d, body %d", r.ContentLength, len(body))
		}
		f.put(full, body, r.Header.Get("Content-Type"))
		o := f.objects[full]
		writeJSON(w, 200, map[string]any{"key": key, "size": len(body), "etag": o.etag, "contentType": o.ctype})
	case "GET", "HEAD":
		o, ok := f.objects[full]
		if !ok {
			if r.Method == "HEAD" {
				w.Header().Set("X-Error-Code", "object_not_found")
				w.WriteHeader(404)
				return
			}
			problem(w, 404, "object_not_found", "The object does not exist.")
			return
		}
		sum := sha256.Sum256(o.data)
		w.Header().Set("Content-Type", o.ctype)
		w.Header().Set("ETag", `"`+o.etag+`"`)
		w.Header().Set("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":")
		w.Header().Set("Last-Modified", o.mod.Format(http.TimeFormat))
		w.Header().Set("X-Meta-Owner", "ops")
		w.Header().Set("Content-Length", strconv.Itoa(len(o.data)))
		w.WriteHeader(200)
		if r.Method == "GET" {
			w.Write(o.data)
		}
	case "DELETE":
		delete(f.objects, full)
		w.WriteHeader(204)
	}
}

func (f *fakeObjectStorage) uploads(w http.ResponseWriter, r *http.Request, sub string, body []byte) {
	switch {
	case sub == "" && r.Method == "POST":
		var in map[string]string
		json.Unmarshal(body, &in)
		f.parts = map[int][]byte{}
		writeJSON(w, 201, map[string]string{"uploadId": "dXA.u1", "key": in["key"]})
		f.pending = &fakeObject{ctype: in["contentType"], etag: in["key"]}
	case strings.HasPrefix(sub, "dXA.u1/parts/") && r.Method == "PUT":
		n, _ := strconv.Atoi(strings.TrimPrefix(sub, "dXA.u1/parts/"))
		f.parts[n] = body
		sum := md5.Sum(body)
		writeJSON(w, 200, map[string]any{"number": n, "etag": hex.EncodeToString(sum[:]), "size": len(body)})
	case sub == "dXA.u1/complete" && r.Method == "POST":
		var in struct {
			Parts []objPart `json:"parts"`
		}
		json.Unmarshal(body, &in)
		var all []byte
		for i, p := range in.Parts {
			sum := md5.Sum(f.parts[p.Number])
			if p.Number != i+1 || p.ETag != hex.EncodeToString(sum[:]) {
				f.t.Errorf("complete part %d: %+v", i, p)
			}
			all = append(all, f.parts[p.Number]...)
		}
		pend := f.pending
		f.put("media/"+pend.etag, all, pend.ctype)
		f.objects["media/"+pend.etag].etag = fmt.Sprintf("abc-%d", len(in.Parts))
		writeJSON(w, 200, map[string]any{"key": pend.etag, "size": len(all), "etag": fmt.Sprintf("abc-%d", len(in.Parts))})
	default:
		f.t.Errorf("unexpected upload call %s %s", r.Method, sub)
		problem(w, 404, "not_found", "No such API path.")
	}
}

func startObjectStorage(t *testing.T) (*fakeObjectStorage, *harness) {
	f := &fakeObjectStorage{t: t, objects: map[string]*fakeObject{}, secrets: map[string]string{}, reject: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() {
		if f.badSig > 0 && !f.allowBad {
			t.Errorf("%d requests with a bad signature", f.badSig)
		}
	})
	f.put("media/a.txt", []byte("hello"), "text/plain")
	f.put("media/photos/x.jpg", []byte("xx"), "image/jpeg")
	f.put("media/photos/y.jpg", []byte("yyy"), "image/jpeg")
	return f, newHarness(t, f.srv.URL)
}

func (f *fakeObjectStorage) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects["media/"+key]
	return ok
}

func TestObjLsBuckets(t *testing.T) {
	_, h := startObjectStorage(t)
	if code := h.run("object-storage", "ls"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "media") || !strings.Contains(out, "bkk") || !strings.Contains(out, "2.0 KiB") {
		t.Fatalf("out: %s", out)
	}
	if code := h.run("object-storage", "buckets", "list", "--format", "json"); code != 0 || !strings.Contains(h.out.String(), `"endpoint"`) {
		t.Fatalf("json %d: %s %s", code, h.out, h.errOut)
	}
}

func TestObjLsPrefix(t *testing.T) {
	f, h := startObjectStorage(t)
	if code := h.run("object-storage", "ls", "th://media"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	if !strings.Contains(out, "th://media/photos/") || !strings.Contains(out, "th://media/a.txt") || !strings.Contains(out, "5 B") {
		t.Fatalf("out: %s", out)
	}
	// A folder name without its slash lists inside the folder.
	if code := h.run("object-storage", "ls", "th://media/photos"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out = h.out.String()
	if !strings.Contains(out, "th://media/photos/x.jpg") || !strings.Contains(out, "th://media/photos/y.jpg") || strings.Contains(out, "a.txt") {
		t.Fatalf("out: %s", out)
	}
	f.page = 1 // every page one object: follows the cursor
	if code := h.run("object-storage", "ls", "-r", "th://media", "--format", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var res struct {
		Objects []objEntry `json:"objects"`
	}
	json.Unmarshal(h.out.Bytes(), &res)
	if len(res.Objects) != 3 {
		t.Fatalf("recursive: %s", h.out)
	}
}

func TestObjUploadSmall(t *testing.T) {
	f, h := startObjectStorage(t)
	file := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(file, []byte("some notes"), 0o644)
	if code := h.run("object-storage", "cp", file, "th://media/docs/"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	o := f.objects["media/docs/notes.txt"]
	if o == nil || string(o.data) != "some notes" || !strings.HasPrefix(o.ctype, "text/plain") {
		t.Fatalf("object %+v; objects %v", o, f.objects)
	}
	if code := h.run("object-storage", "cp", file, "th://media/x.bin", "--content-type", "application/x-thing"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.objects["media/x.bin"].ctype != "application/x-thing" {
		t.Fatalf("content type %q", f.objects["media/x.bin"].ctype)
	}
	if len(f.issued) != 1 {
		t.Fatalf("data keys made %v (should be cached)", f.issued)
	}
}

func TestObjUploadMultipart(t *testing.T) {
	f, h := startObjectStorage(t)
	oldT, oldP := objMultipartThreshold, objPartSize
	objMultipartThreshold, objPartSize = 10, 8
	t.Cleanup(func() { objMultipartThreshold, objPartSize = oldT, oldP })
	data := []byte("0123456789abcdefghijklmnopqrstuvwxyz") // 36 bytes: 5 parts
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "big.bin"), data, 0o644)
	if code := h.run("object-storage", "cp", filepath.Join(dir, "big.bin"), "th://media/backups/big.bin"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	o := f.objects["media/backups/big.bin"]
	if o == nil || !bytes.Equal(o.data, data) || o.etag != "abc-5" {
		t.Fatalf("object %+v", o)
	}
	if !strings.Contains(h.errOut.String(), "5/5 parts") {
		t.Fatalf("progress: %s", h.errOut)
	}
}

func TestObjDownload(t *testing.T) {
	_, h := startObjectStorage(t)
	dir := t.TempDir()
	if code := h.run("object-storage", "cp", "th://media/a.txt", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "hello" {
		t.Fatalf("file %q", b)
	}
	if code := h.run("object-storage", "cp", "-r", "th://media/photos", filepath.Join(dir, "p")); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "p", "y.jpg")); string(b) != "yyy" {
		t.Fatalf("recursive file %q", b)
	}
	if code := h.run("object-storage", "cat", "th://media/a.txt"); code != 0 || h.out.String() != "hello" {
		t.Fatalf("cat %d: %q %s", code, h.out, h.errOut)
	}
	if code := h.run("object-storage", "stat", "th://media/a.txt"); code != 0 || !strings.Contains(h.out.String(), "5 bytes") || !strings.Contains(h.out.String(), "Meta owner: ops") {
		t.Fatalf("stat %d: %s %s", code, h.out, h.errOut)
	}
	if code := h.run("object-storage", "stat", "th://media/missing"); code != 1 || !strings.Contains(h.errOut.String(), "does not exist") {
		t.Fatalf("stat missing %d: %s", code, h.errOut)
	}
}

func TestObjRemoteCopyAndMove(t *testing.T) {
	f, h := startObjectStorage(t)
	if code := h.run("object-storage", "mv", "th://media/a.txt", "th://media/archive/"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !f.has("archive/a.txt") || f.has("a.txt") {
		t.Fatalf("objects %v", f.objects)
	}
}

func TestObjRmRecursive(t *testing.T) {
	f, h := startObjectStorage(t)
	h.app.Stdin = strings.NewReader("n\n")
	if code := h.run("object-storage", "rm", "-r", "th://media/photos"); code != 1 || !f.has("photos/x.jpg") {
		t.Fatalf("declined: exit %d", code)
	}
	if code := h.run("object-storage", "rm", "-r", "th://media/photos", "--quiet"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.has("photos/x.jpg") || f.has("photos/y.jpg") || !f.has("a.txt") {
		t.Fatalf("objects %v", f.objects)
	}
	if code := h.run("object-storage", "rm", "th://media/a.txt"); code != 0 || f.has("a.txt") {
		t.Fatalf("rm: %d %s", code, h.errOut)
	}
}

func TestObjSync(t *testing.T) {
	f, h := startObjectStorage(t)
	dir := t.TempDir()
	write := func(name, s string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	write("same.txt", "same")
	write("changed.txt", "new!") // same size as the remote copy, other content
	write("sub/new.txt", "brand new")
	write("big.bin", "multipart")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, "big.bin"), old, old)
	f.put("media/site/same.txt", []byte("same"), "text/plain")
	f.put("media/site/changed.txt", []byte("old!"), "text/plain")
	f.put("media/site/big.bin", []byte("multipart"), "application/octet-stream")
	f.objects["media/site/big.bin"].etag = "abc-2"
	f.put("media/site/gone.txt", []byte("bye"), "text/plain")
	f.put("media/other.txt", []byte("keep"), "text/plain")

	if code := h.run("object-storage", "sync", dir, "th://media/site", "--delete", "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !f.has("site/gone.txt") || f.has("site/sub/new.txt") {
		t.Fatalf("dry run changed something")
	}
	if code := h.run("object-storage", "sync", dir, "th://media/site", "--delete", "--quiet", "--format", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var res struct {
		Uploaded  []string `json:"uploaded"`
		Deleted   []string `json:"deleted"`
		Unchanged int      `json:"unchanged"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &res); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	sort.Strings(res.Uploaded)
	if strings.Join(res.Uploaded, ",") != "site/changed.txt,site/sub/new.txt" || strings.Join(res.Deleted, ",") != "site/gone.txt" || res.Unchanged != 2 {
		t.Fatalf("result %+v", res)
	}
	if string(f.objects["media/site/changed.txt"].data) != "new!" || !f.has("other.txt") || f.has("site/gone.txt") {
		t.Fatalf("objects after sync")
	}
	// Run again: nothing to do.
	if code := h.run("object-storage", "sync", dir, "th://media/site"); code != 0 || !strings.Contains(h.errOut.String(), "Uploaded 0, deleted 0, unchanged 4") {
		t.Fatalf("second sync %d: %s", code, h.errOut)
	}
}

func TestObjSign(t *testing.T) {
	f, h := startObjectStorage(t)
	if code := h.run("object-storage", "sign", "th://media/a.txt", "--expires", "2h", "--method", "put"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.HasPrefix(h.out.String(), "https://objects.example/") {
		t.Fatalf("out %q", h.out)
	}
	if len(f.links) != 1 || f.links[0]["expiresIn"] != float64(7200) || f.links[0]["method"] != "PUT" || f.links[0]["key"] != "a.txt" {
		t.Fatalf("links %v", f.links)
	}
	if code := h.run("object-storage", "sign", "th://media/a.txt", "--expires", "8d"); code != 1 {
		t.Fatalf("8d accepted")
	}
}

func TestObjKeyFromEnv(t *testing.T) {
	f, h := startObjectStorage(t)
	f.secrets["envkey"] = "envsecret"
	h.env["TH_OBJECT_STORAGE_KEY_ID"] = "envkey"
	h.env["TH_OBJECT_STORAGE_SECRET"] = "envsecret"
	if code := h.run("object-storage", "ls", "th://media"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(f.issued) != 0 || len(f.used) == 0 || f.used[0] != "envkey" {
		t.Fatalf("issued %v used %v", f.issued, f.used)
	}
	h.env["TH_OBJECT_STORAGE_SECRET"] = "wrong"
	f.allowBad = true
	if code := h.run("object-storage", "ls", "th://media"); code != 1 || !strings.Contains(h.errOut.String(), "TH_OBJECT_STORAGE_KEY_ID") {
		t.Fatalf("wrong secret %d: %s", code, h.errOut)
	}
}

func TestObjRetryAfter401DropsCachedKey(t *testing.T) {
	f, h := startObjectStorage(t)
	f.reject["k1"] = true
	if code := h.run("object-storage", "ls", "th://media"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if strings.Join(f.issued, ",") != "k1,k2" || f.used[len(f.used)-1] != "k2" {
		t.Fatalf("issued %v used %v", f.issued, f.used)
	}
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(h.cfg), "datakeys.json"))
	if strings.Contains(string(b), `"k1"`) || !strings.Contains(string(b), `"k2"`) {
		t.Fatalf("cache %s", b)
	}
}

func TestObjBucketsDeleteAndPublic(t *testing.T) {
	f, h := startObjectStorage(t)
	if code := h.run("object-storage", "buckets", "delete", "media", "--quiet"); code != 1 || !strings.Contains(h.errOut.String(), "--force") {
		t.Fatalf("not empty %d: %s", code, h.errOut)
	}
	if code := h.run("object-storage", "buckets", "delete", "media", "--force", "--quiet"); code != 0 {
		t.Fatalf("force %d: %s", code, h.errOut)
	}
	if strings.Join(f.deletes, " ") != "/api/v1/object-storage/buckets/b1 /api/v1/object-storage/buckets/b1?force=true" {
		t.Fatalf("deletes %v", f.deletes)
	}
	if code := h.run("object-storage", "buckets", "public", "media", "--prefix", "images/", "--list"); code != 0 {
		t.Fatalf("public %d: %s", code, h.errOut)
	}
	if len(f.rules) != 1 || f.rules[0] != (objPublicRule{Prefix: "images/", List: true}) || !strings.Contains(h.out.String(), "th://media/images/") {
		t.Fatalf("rules %v: %s", f.rules, h.out)
	}
	if code := h.run("object-storage", "buckets", "public", "media", "--prefix", "css/"); code != 0 || len(f.rules) != 2 {
		t.Fatalf("add %d: %v", code, f.rules)
	}
	if code := h.run("object-storage", "buckets", "public", "media", "--private", "--prefix", "images/"); code != 0 || len(f.rules) != 1 || f.rules[0].Prefix != "css/" {
		t.Fatalf("remove %d: %v", code, f.rules)
	}
	if code := h.run("object-storage", "buckets", "public", "media", "--private"); code != 0 || len(f.rules) != 0 || !strings.Contains(h.out.String(), "private") {
		t.Fatalf("private %d: %v %s", code, f.rules, h.out)
	}
	if code := h.run("object-storage", "buckets", "create", "logs"); code != 0 || !strings.Contains(h.out.String(), "Created bucket [logs] in bkk") {
		t.Fatalf("create %d: %s %s", code, h.out, h.errOut)
	}
}
