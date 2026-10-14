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
	"time"
)

type fakeQueues struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	consoleCalls []string         // "METHOD path"
	lastBody     map[string]any   // last Console JSON body
	dataCalls    []string         // "METHOD path keyid"
	dataBodies   []map[string]any // data-plane bodies, in order
	keysIssued   int              // POST /api/v1/cli/data-key calls
	rejectKey    string           // keyid answered 401
	rejectAll    bool
	receives     [][]map[string]any                // answers for /receive, in order
	onReceive    func(n int, r *http.Request) bool // true: answered already
	acked        []string
}

func (f *fakeQueues) queue() map[string]any {
	return map[string]any{"id": "qu1", "projectId": "p1", "regionId": "th-bkk-1", "name": "jobs", "status": "active",
		"visibilityTimeout": 30, "retention": 345600, "delay": 0, "maxReceives": 5, "deadLetterQueueId": "qu2",
		"deadLetterQueue": "jobs-dlq", "endpoint": f.srv.URL + "/th-bkk-1", "createdAt": "2026-10-10T08:00:00Z",
		"messages": map[string]any{"visible": 3, "inFlight": 1, "delayed": 0, "bytes": 2048, "oldestAge": 90}}
}

func (f *fakeQueues) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	var body map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/th-bkk-1/v1/") {
		f.data(w, r, body)
		return
	}
	defer f.mu.Unlock()
	f.consoleCalls = append(f.consoleCalls, r.Method+" "+p)
	if body != nil {
		f.lastBody = body
	}
	switch {
	case p == "/api/v1/projects":
		writeJSON(w, 200, []map[string]string{{"id": "p1", "slug": "acme"}})
	case r.Method == "GET" && p == "/api/v1/queues":
		if r.URL.Query().Get("projectId") != "p1" {
			f.t.Errorf("queues query %s", r.URL.RawQuery)
		}
		writeJSON(w, 200, map[string]any{"queues": []map[string]any{f.queue()}, "keys": []any{},
			"regions": []map[string]string{{"id": "th-bkk-1", "name": "Bangkok", "endpoint": f.srv.URL + "/th-bkk-1"}}})
	case r.Method == "POST" && p == "/api/v1/queues":
		out := f.queue()
		out["name"] = body["name"]
		writeJSON(w, 201, out)
	case r.Method == "PUT" && p == "/api/v1/queues/qu1":
		writeJSON(w, 200, f.queue())
	case r.Method == "DELETE" && p == "/api/v1/queues/qu1", r.Method == "POST" && p == "/api/v1/queues/qu1/purge":
		w.WriteHeader(204)
	case r.Method == "GET" && p == "/api/v1/queues/qu1/messages":
		writeJSON(w, 200, map[string]any{"messages": []map[string]any{{"id": "m1", "body": "hello", "sent": "2026-10-10T08:00:00Z", "receives": 0}}})
	case r.Method == "POST" && p == "/api/v1/cli/data-key":
		f.keysIssued++
		if body["service"] != "queue" || body["projectId"] != "p1" {
			f.t.Errorf("data-key body %v", body)
		}
		writeJSON(w, 200, map[string]any{"keyId": "K" + string(rune('0'+f.keysIssued)), "secret": "s", "expiresAt": time.Now().Add(12 * time.Hour)})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		writeJSON(w, 404, map[string]string{"error": "ไม่พบ"})
	}
}

func (f *fakeQueues) data(w http.ResponseWriter, r *http.Request, body map[string]any) {
	sigIn := r.Header.Get("Signature-Input")
	keyID := ""
	if i := strings.Index(sigIn, `keyid="`); i >= 0 {
		keyID = strings.SplitN(sigIn[i+7:], `"`, 2)[0]
	}
	if !strings.HasPrefix(sigIn, "th=(") || !strings.HasPrefix(r.Header.Get("Signature"), "th=:") ||
		(r.ContentLength > 0 && !strings.HasPrefix(r.Header.Get("Content-Digest"), "sha-256=:")) {
		f.t.Errorf("unsigned request %s: %v", r.URL, r.Header)
	}
	sub := strings.TrimPrefix(r.URL.Path, "/th-bkk-1/v1/queues/jobs")
	f.dataCalls = append(f.dataCalls, r.Method+" "+sub+" "+keyID)
	f.dataBodies = append(f.dataBodies, body)
	if keyID == f.rejectKey || f.rejectAll {
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("X-Error-Code", "unauthorized")
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{"title": "Sign the request.", "code": "unauthorized", "status": 401})
		return
	}
	switch {
	case r.Method == "POST" && sub == "/messages":
		f.mu.Unlock()
		writeJSON(w, 201, map[string]string{"id": "m9"})
	case r.Method == "POST" && sub == "/receive":
		n := 0
		for _, c := range f.dataCalls {
			if strings.Contains(c, "/receive") {
				n++
			}
		}
		if f.onReceive != nil {
			cb := f.onReceive
			f.mu.Unlock()
			if cb(n, r) {
				return
			}
			f.mu.Lock()
		}
		var msgs []map[string]any
		if n-1 < len(f.receives) {
			msgs = f.receives[n-1]
		}
		f.mu.Unlock()
		if msgs == nil {
			msgs = []map[string]any{}
		}
		writeJSON(w, 200, map[string]any{"messages": msgs})
	case r.Method == "POST" && sub == "/delete":
		var res []map[string]any
		for _, rc := range body["receipts"].([]any) {
			s := rc.(string)
			f.acked = append(f.acked, s)
			if s == "bad" {
				res = append(res, map[string]any{"receipt": s, "deleted": false, "error": "invalid_receipt"})
			} else {
				res = append(res, map[string]any{"receipt": s, "deleted": true})
			}
		}
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"results": res})
	default:
		f.mu.Unlock()
		f.t.Errorf("unexpected data %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	}
}

func startQueues(t *testing.T) (*fakeQueues, *harness) {
	f := &fakeQueues{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(f.srv.Close)
	return f, newHarness(t, f.srv.URL)
}

func queueTestMsg(id, receipt, body string) map[string]any {
	return map[string]any{"id": id, "receipt": receipt, "body": body, "attributes": map[string]string{"kind": "order"},
		"sentAt": "2026-10-10T08:00:00Z", "receives": 2}
}

func TestParseQueueDuration(t *testing.T) {
	for in, want := range map[string]int64{"30s": 30, "5m": 300, "4d": 345600, "1d12h": 129600, "90": 90, "1h30m": 5400, "0s": 0} {
		if got, err := parseQueueDuration(in); err != nil || got != want {
			t.Errorf("%s = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "x", "1.5s", "-3s", "d"} {
		if _, err := parseQueueDuration(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
	if s := formatQueueSeconds(345600 + 5400); s != "4d1h30m" {
		t.Errorf("format = %s", s)
	}
}

func TestQueueCreateSendsSeconds(t *testing.T) {
	f, h := startQueues(t)
	if code := h.run("queue", "create", "orders", "--visibility-timeout", "5m", "--retention", "4d", "--delay", "10s",
		"--max-receives", "3", "--dead-letter", "jobs-dlq"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b := f.lastBody
	want := map[string]any{"projectId": "p1", "regionId": "th-bkk-1", "name": "orders", "visibilityTimeout": 300.0,
		"retention": 345600.0, "delay": 10.0, "maxReceives": 3.0, "deadLetterQueue": "jobs-dlq"}
	for k, v := range want {
		if b[k] != v {
			t.Errorf("%s = %v, want %v (body %v)", k, b[k], v, b)
		}
	}
	if !strings.Contains(h.errOut.String(), "Created queue [orders]") || !strings.Contains(h.out.String(), "/th-bkk-1") {
		t.Fatalf("out %s / %s", h.out, h.errOut)
	}
}

func TestQueueCreateUnknownRegion(t *testing.T) {
	_, h := startQueues(t)
	if code := h.run("queue", "create", "orders", "--region", "us-1"); code == 0 || !strings.Contains(h.errOut.String(), "th-bkk-1") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
}

func TestQueueUpdateOnlySetFields(t *testing.T) {
	f, h := startQueues(t)
	if code := h.run("queue", "update", "jobs", "--retention", "2d", "--dead-letter", "none"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b := f.lastBody
	if len(b) != 2 || b["retention"] != 172800.0 || b["deadLetterQueue"] != "" {
		t.Fatalf("body %v", b)
	}
	if code := h.run("queue", "update", "jobs"); code == 0 {
		t.Fatal("update with nothing to change succeeded")
	}
}

func TestQueueListAndDescribe(t *testing.T) {
	_, h := startQueues(t)
	if code := h.run("queue", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	lines := strings.Split(strings.TrimSpace(h.out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "IN FLIGHT") {
		t.Fatalf("list: %s", h.out)
	}
	if f := strings.Fields(lines[1]); strings.Join(f, " ") != "jobs th-bkk-1 3 1 0 1m30s 30s 4d jobs-dlq" {
		t.Fatalf("row: %q", lines[1])
	}
	if code := h.run("queue", "describe", "jobs"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, s := range []string{"Queue jobs in region th-bkk-1", "/th-bkk-1", "Retention:           4d", "Max receives:        5", "jobs-dlq", "3 visible, 1 in flight"} {
		if !strings.Contains(out, s) {
			t.Errorf("describe lacks %q:\n%s", s, out)
		}
	}
	if code := h.run("queue", "describe", "nope"); code == 0 || !strings.Contains(h.errOut.String(), `queue "nope" not found`) {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if code := h.run("queue", "list", "--format", "json"); code != 0 || !strings.HasPrefix(strings.TrimSpace(h.out.String()), "[") {
		t.Fatalf("json list: %s", h.out)
	}
}

func TestQueuePurgeAndDeleteConfirm(t *testing.T) {
	f, h := startQueues(t)
	h.app.Stdin = strings.NewReader("n\n")
	if code := h.run("queue", "purge", "jobs"); code != 1 || !strings.Contains(h.errOut.String(), "Aborted.") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, c := range f.consoleCalls {
		if strings.HasSuffix(c, "/purge") {
			t.Fatal("purged without a yes")
		}
	}
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run("queue", "purge", "jobs"); code != 0 || !strings.Contains(h.out.String(), "Purged queue [jobs]") {
		t.Fatalf("exit %d: %s %s", code, h.out, h.errOut)
	}
	if code := h.run("queue", "delete", "jobs", "--quiet"); code != 0 || !strings.Contains(h.out.String(), "Deleted queue [jobs]") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	last := f.consoleCalls[len(f.consoleCalls)-1]
	if last != "DELETE /api/v1/queues/qu1" {
		t.Fatalf("last call %s", last)
	}
}

func TestQueuePeek(t *testing.T) {
	_, h := startQueues(t)
	if code := h.run("queue", "peek", "jobs"); code != 0 || !strings.Contains(h.out.String(), "Message:   m1") || !strings.Contains(h.out.String(), "  hello") {
		t.Fatalf("exit %d: %s %s", code, h.out, h.errOut)
	}
}

func TestQueueSendBodies(t *testing.T) {
	f, h := startQueues(t)
	if code := h.run("queue", "send", "jobs", "hi there", "--attr", "kind=order", "--attr", "n=1", "--delay", "1m"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "Sent message [m9] to queue [jobs]") {
		t.Fatalf("out %s", h.out)
	}
	b := f.dataBodies[len(f.dataBodies)-1]
	attrs, _ := b["attributes"].(map[string]any)
	if b["body"] != "hi there" || b["delay"] != 60.0 || attrs["kind"] != "order" || attrs["n"] != "1" {
		t.Fatalf("body %v", b)
	}
	if f.dataCalls[0] != "POST /messages K1" {
		t.Fatalf("call %v", f.dataCalls)
	}

	file := filepath.Join(t.TempDir(), "m.json")
	os.WriteFile(file, []byte(`{"orderId":42}`), 0o600)
	if code := h.run("queue", "send", "jobs", "--file", file); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if b := f.dataBodies[len(f.dataBodies)-1]; b["body"] != `{"orderId":42}` || b["delay"] != nil {
		t.Fatalf("file body %v", b)
	}

	h.app.Stdin = strings.NewReader("from stdin\n")
	if code := h.run("queue", "send", "jobs", "-"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if b := f.dataBodies[len(f.dataBodies)-1]; b["body"] != "from stdin\n" {
		t.Fatalf("stdin body %v", b)
	}
	// The cached key is reused.
	if f.keysIssued != 1 {
		t.Fatalf("keys issued %d", f.keysIssued)
	}
	if code := h.run("queue", "send", "jobs", "--attr", "x"); code == 0 {
		t.Fatal("bad --attr accepted")
	}
}

func TestQueueEnvKey(t *testing.T) {
	f, h := startQueues(t)
	h.env["TH_QUEUE_KEY_ID"] = "THQKENV"
	h.env["TH_QUEUE_SECRET"] = "sec"
	if code := h.run("queue", "send", "jobs", "x"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.keysIssued != 0 || f.dataCalls[0] != "POST /messages THQKENV" {
		t.Fatalf("issued %d calls %v", f.keysIssued, f.dataCalls)
	}
	f.rejectKey = "THQKENV"
	if code := h.run("queue", "send", "jobs", "x"); code == 0 || !strings.Contains(h.errOut.String(), "TH_QUEUE_KEY_ID") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
}

func TestQueueRetryAfter401(t *testing.T) {
	f, h := startQueues(t)
	f.rejectKey = "K1"
	if code := h.run("queue", "send", "jobs", "x"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if f.keysIssued != 2 || len(f.dataCalls) != 2 || f.dataCalls[1] != "POST /messages K2" {
		t.Fatalf("issued %d calls %v", f.keysIssued, f.dataCalls)
	}
	// Every key refused: only one retry.
	f.rejectAll = true
	f.dataCalls = nil
	if code := h.run("queue", "send", "jobs", "x"); code == 0 || !strings.Contains(h.errOut.String(), "unauthorized") || len(f.dataCalls) != 2 {
		t.Fatalf("exit %d: %s %v", code, h.errOut, f.dataCalls)
	}
}

func TestQueueReceiveWithoutAck(t *testing.T) {
	f, h := startQueues(t)
	f.receives = [][]map[string]any{{queueTestMsg("m1", "r1", "hello\nworld")}}
	if code := h.run("queue", "receive", "jobs", "--max", "5", "--wait", "20s", "--visibility", "1m"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	b := f.dataBodies[0]
	if b["max"] != 5.0 || b["wait"] != 20.0 || b["visibilityTimeout"] != 60.0 {
		t.Fatalf("receive body %v", b)
	}
	out := h.out.String()
	for _, s := range []string{"Message:   m1", "Receipt:   r1", "Receives:  2", "kind=order", "  hello\n  world"} {
		if !strings.Contains(out, s) {
			t.Errorf("lacks %q:\n%s", s, out)
		}
	}
	if !strings.Contains(h.errOut.String(), "th queue ack jobs RECEIPT") || len(f.acked) != 0 {
		t.Fatalf("hint %s acked %v", h.errOut, f.acked)
	}
	if code := h.run("queue", "receive", "jobs", "--wait", "30s"); code == 0 {
		t.Fatal("--wait 30s accepted")
	}
}

func TestQueueReceiveWithAck(t *testing.T) {
	f, h := startQueues(t)
	f.receives = [][]map[string]any{{queueTestMsg("m1", "r1", "a"), queueTestMsg("m2", "r2", "b")}}
	if code := h.run("queue", "receive", "jobs", "--max", "2", "--ack", "--format", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var got []qMessage
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || len(got) != 2 || got[0].Acked == nil || !*got[0].Acked {
		t.Fatalf("json %s (%v)", h.out, err)
	}
	if strings.Join(f.acked, ",") != "r1,r2" {
		t.Fatalf("acked %v", f.acked)
	}
	if code := h.run("queue", "receive", "jobs"); code != 0 || !strings.Contains(h.errOut.String(), "No messages") {
		t.Fatalf("empty: %d %s", code, h.errOut)
	}
}

func TestQueueAck(t *testing.T) {
	f, h := startQueues(t)
	if code := h.run("queue", "ack", "jobs", "r1", "r2"); code != 0 || !strings.Contains(h.out.String(), "Acknowledged 2 message(s)") {
		t.Fatalf("exit %d: %s %s", code, h.out, h.errOut)
	}
	if code := h.run("queue", "ack", "jobs", "r3", "bad"); code != 1 || !strings.Contains(h.errOut.String(), "1 message(s) were not acknowledged") {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	// More than 10 receipts go in batches.
	f.dataCalls = nil
	var many []string
	for i := 0; i < 12; i++ {
		many = append(many, "x"+string(rune('a'+i)))
	}
	if code := h.run(append([]string{"queue", "ack", "jobs"}, many...)...); code != 0 || len(f.dataCalls) != 2 {
		t.Fatalf("exit %d calls %v", code, f.dataCalls)
	}
}

func TestQueueTailStopsOnInterrupt(t *testing.T) {
	f, h := startQueues(t)
	intr := make(chan os.Signal, 1)
	h.app.Interrupt = intr
	f.receives = [][]map[string]any{{queueTestMsg("m1", "r1", "first")}, {}}
	f.onReceive = func(n int, r *http.Request) bool {
		if n < 3 {
			return false
		}
		intr <- os.Interrupt
		<-r.Context().Done()
		return true
	}
	done := make(chan int)
	go func() { done <- h.run("queue", "tail", "jobs") }()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, h.errOut)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tail did not stop")
	}
	if !strings.Contains(h.out.String(), "  first") || !strings.Contains(h.errOut.String(), "Stopped after 1 message(s).") {
		t.Fatalf("out %s / %s", h.out, h.errOut)
	}
	if strings.Join(f.acked, ",") != "r1" {
		t.Fatalf("acked %v", f.acked)
	}
	if b := f.dataBodies[0]; b["wait"] != 20.0 || b["max"] != 10.0 {
		t.Fatalf("receive body %v", b)
	}
}

func TestQueueTailNoAck(t *testing.T) {
	f, h := startQueues(t)
	intr := make(chan os.Signal, 1)
	h.app.Interrupt = intr
	f.receives = [][]map[string]any{{queueTestMsg("m1", "r1", "first")}}
	f.onReceive = func(n int, r *http.Request) bool {
		if n < 2 {
			return false
		}
		intr <- os.Interrupt
		<-r.Context().Done()
		return true
	}
	if code := h.run("queue", "tail", "jobs", "--no-ack", "--format", "json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(f.acked) != 0 || !strings.Contains(h.out.String(), `"receipt":"r1"`) {
		t.Fatalf("acked %v out %s", f.acked, h.out)
	}
}
