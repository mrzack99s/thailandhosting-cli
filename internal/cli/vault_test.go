package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeVaults struct {
	t      *testing.T
	calls  []string
	bodies map[string]map[string]any
}

func (f *fakeVaults) handler(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	json.NewDecoder(r.Body).Decode(&body)
	key := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, key)
	f.bodies[key] = body
	enc := base64.StdEncoding.EncodeToString
	switch key {
	case "GET /api/v1/projects":
		writeJSON(w, 200, []map[string]any{{"id": "p1", "name": "Shop", "slug": "shop", "role": "owner"}})
	case "GET /api/v1/vaults":
		writeJSON(w, 200, []map[string]any{
			{"id": "v1", "name": "app", "softDeleteDays": 30, "secrets": 1, "keys": 2, "purgeProtection": true},
			{"id": "v2", "name": "old", "softDeleteDays": 30, "deletedAt": "2026-11-01T00:00:00Z", "purgeAt": "2026-12-01T00:00:00Z"},
		})
	case "POST /api/v1/vaults":
		writeJSON(w, 201, map[string]any{"id": "v3", "name": body["name"]})
	case "GET /api/v1/vaults/v1/secrets":
		writeJSON(w, 200, []map[string]any{
			{"id": "s1", "name": "db", "lastVersion": 3, "versions": []map[string]any{
				{"version": 3, "enabled": false},
				{"version": 2, "enabled": true},
				{"version": 1, "enabled": true, "destroyedAt": "2026-10-01T00:00:00Z"},
			}},
			{"id": "s2", "name": "gone", "deletedAt": "2026-11-01T00:00:00Z", "purgeAt": "2026-12-01T00:00:00Z"},
		})
	case "POST /api/v1/vaults/v1/secrets/s1/versions/2/reveal":
		writeJSON(w, 200, map[string]any{"version": 2, "value": "s3cret"})
	case "POST /api/v1/vaults/v1/secrets/s1/versions":
		writeJSON(w, 201, map[string]any{"version": 4, "enabled": true})
	case "POST /api/v1/vaults/v1/secrets":
		writeJSON(w, 201, map[string]any{"id": "s3", "name": body["name"]})
	case "GET /api/v1/vaults/v1/keys":
		writeJSON(w, 200, []map[string]any{
			{"id": "k1", "name": "data", "type": "aes256", "purpose": "encrypt", "enabled": true, "rotationDays": 90, "versions": []map[string]any{{"version": 1, "enabled": true}}},
			{"id": "k2", "name": "signer", "type": "ecp256", "purpose": "sign", "enabled": true, "versions": []map[string]any{{"version": 2, "enabled": true, "publicKey": "PEM\n"}, {"version": 1, "enabled": true, "publicKey": "OLD\n"}}},
		})
	case "POST /api/v1/vaults/v1/keys":
		writeJSON(w, 201, map[string]any{"id": "k3", "name": body["name"], "type": body["type"], "purpose": body["purpose"]})
	case "POST /api/v1/vaults/v1/keys/k1/encrypt":
		writeJSON(w, 200, map[string]any{"ciphertext": "thv1:1:AA", "version": 1})
	case "POST /api/v1/vaults/v1/keys/k1/decrypt":
		writeJSON(w, 200, map[string]any{"plaintext": enc([]byte("hello"))})
	case "POST /api/v1/vaults/v1/keys/k1/rotate":
		writeJSON(w, 201, map[string]any{"version": 2})
	case "POST /api/v1/vaults/v1/keys/k2/sign":
		writeJSON(w, 200, map[string]any{"signature": enc([]byte("sig")), "version": 2})
	case "POST /api/v1/vaults/v1/keys/k2/verify":
		writeJSON(w, 200, map[string]any{"valid": body["signature"] == enc([]byte("sig"))})
	case "PATCH /api/v1/vaults/v1/keys/k1", "PATCH /api/v1/vaults/v1/secrets/s1/versions/2", "DELETE /api/v1/vaults/v1/secrets/s1", "POST /api/v1/vaults/v2/recover":
		w.WriteHeader(204)
	default:
		f.t.Errorf("unexpected %s", key)
		w.WriteHeader(500)
	}
}

func startVaults(t *testing.T) (*fakeVaults, *harness) {
	f := &fakeVaults{t: t, bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	h := newHarness(t, srv.URL)
	h.env["TH_PROJECT"] = "p1"
	return f, h
}

// vaultRun runs a command that must succeed and answers what it printed.
func vaultRun(t *testing.T, h *harness, args ...string) string {
	t.Helper()
	if code := h.run(args...); code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, h.errOut)
	}
	return h.out.String()
}

func TestVaultListAndCreate(t *testing.T) {
	f, h := startVaults(t)
	out := vaultRun(t, h, "vault", "list")
	if !strings.Contains(out, "app") || !strings.Contains(out, "purged 2026-12-01") {
		t.Fatalf("list: %s", out)
	}
	vaultRun(t, h, "vault", "create", "new", "--soft-delete-days", "7", "--purge-protection")
	in := f.bodies["POST /api/v1/vaults"]
	if in["name"] != "new" || in["softDeleteDays"] != float64(7) || in["purgeProtection"] != true || in["projectId"] != "p1" {
		t.Fatalf("create: %v", in)
	}
	if code := h.run("vault", "secret", "list", "old"); code == 0 || !strings.Contains(h.errOut.String(), "th vault recover old") {
		t.Fatalf("deleted vault: %d %s", code, h.errOut)
	}
	vaultRun(t, h, "vault", "recover", "old")
}

func TestVaultSecretGetPicksNewestUsable(t *testing.T) {
	_, h := startVaults(t)
	// Version 3 is disabled and 1 destroyed, and the value has nothing added to it.
	if out := vaultRun(t, h, "vault", "secret", "get", "app", "db"); out != "s3cret" {
		t.Fatalf("get: %q", out)
	}
	if code := h.run("vault", "secret", "get", "app", "gone"); code == 0 || !strings.Contains(h.errOut.String(), "recover") {
		t.Fatalf("deleted secret: %d %s", code, h.errOut)
	}
}

func TestVaultSecretSet(t *testing.T) {
	f, h := startVaults(t)
	h.app.Stdin = strings.NewReader("from-stdin")
	out := vaultRun(t, h, "vault", "secret", "set", "app", "db", "--expires", "2027-01-31")
	in := f.bodies["POST /api/v1/vaults/v1/secrets/s1/versions"]
	if in["value"] != "from-stdin" || in["expiresAt"] != time.Date(2027, 1, 31, 23, 59, 59, 0, time.Local).Format(time.RFC3339) {
		t.Fatalf("set: %v", in)
	}
	if !strings.Contains(out, "version 4") || !strings.Contains(out, "vault://app/db") {
		t.Fatalf("set answer: %s", out)
	}
	vaultRun(t, h, "vault", "secret", "set", "app", "fresh", "value")
	if in = f.bodies["POST /api/v1/vaults/v1/secrets"]; in["name"] != "fresh" || in["value"] != "value" {
		t.Fatalf("new secret: %v", in)
	}
	if code := h.run("vault", "secret", "set", "app", "gone", "x"); code == 0 || !strings.Contains(h.errOut.String(), "recover") {
		t.Fatalf("deleted secret: %d %s", code, h.errOut)
	}
	if code := h.run("vault", "secret", "set", "app", "db", "x", "--expires", "soon"); code == 0 {
		t.Fatal("bad expiry accepted")
	}
}

func TestVaultSecretChanges(t *testing.T) {
	f, h := startVaults(t)
	vaultRun(t, h, "vault", "secret", "disable", "app", "db", "2")
	if in := f.bodies["PATCH /api/v1/vaults/v1/secrets/s1/versions/2"]; in["enabled"] != false {
		t.Fatalf("disable: %v", in)
	}
	h.app.Stdin = strings.NewReader("n\n")
	if code := h.run("vault", "secret", "delete", "app", "db"); code == 0 {
		t.Fatal("deleted after answering no")
	}
	h.app.Stdin = strings.NewReader("y\n")
	vaultRun(t, h, "vault", "secret", "delete", "app", "db")
}

func TestVaultKeys(t *testing.T) {
	f, h := startVaults(t)
	if out := vaultRun(t, h, "vault", "key", "list", "app"); !strings.Contains(out, "90d") || !strings.Contains(out, "signer") {
		t.Fatalf("list: %s", out)
	}
	vaultRun(t, h, "vault", "key", "create", "app", "new", "--type", "ecp256")
	if in := f.bodies["POST /api/v1/vaults/v1/keys"]; in["purpose"] != "sign" || in["type"] != "ecp256" {
		t.Fatalf("create: %v", in)
	}
	if code := h.run("vault", "key", "create", "app", "new", "--type", "rsa2048"); code == 0 || !strings.Contains(h.errOut.String(), "--purpose") {
		t.Fatalf("rsa without purpose: %d %s", code, h.errOut)
	}
	if out := vaultRun(t, h, "vault", "key", "rotate", "app", "data"); !strings.Contains(out, "version 2") {
		t.Fatalf("rotate: %s", out)
	}
	vaultRun(t, h, "vault", "key", "disable", "app", "data")
	if in := f.bodies["PATCH /api/v1/vaults/v1/keys/k1"]; in["enabled"] != false {
		t.Fatalf("disable: %v", in)
	}
	if out := vaultRun(t, h, "vault", "key", "public-key", "app", "signer"); out != "PEM\n" {
		t.Fatalf("public key: %q", out)
	}
	if out := vaultRun(t, h, "vault", "key", "public-key", "app", "signer", "--version", "1"); out != "OLD\n" {
		t.Fatalf("public key 1: %q", out)
	}
	if code := h.run("vault", "key", "public-key", "app", "data"); code == 0 {
		t.Fatal("public key of an AES key")
	}
}

func TestVaultKeyCrypto(t *testing.T) {
	f, h := startVaults(t)
	enc := base64.StdEncoding.EncodeToString
	if out := vaultRun(t, h, "vault", "key", "encrypt", "app", "data", "hello", "--aad", "ctx"); out != "thv1:1:AA\n" {
		t.Fatalf("encrypt: %q", out)
	}
	if in := f.bodies["POST /api/v1/vaults/v1/keys/k1/encrypt"]; in["plaintext"] != enc([]byte("hello")) || in["aad"] != enc([]byte("ctx")) {
		t.Fatalf("encrypt body: %v", in)
	}
	h.app.Stdin = strings.NewReader("thv1:1:AA\n")
	if out := vaultRun(t, h, "vault", "key", "decrypt", "app", "data"); out != "hello" {
		t.Fatalf("decrypt: %q", out)
	}
	if in := f.bodies["POST /api/v1/vaults/v1/keys/k1/decrypt"]; in["ciphertext"] != "thv1:1:AA" {
		t.Fatalf("decrypt body: %v", in)
	}
	if out := vaultRun(t, h, "vault", "key", "sign", "app", "signer", "doc"); !strings.Contains(out, enc([]byte("sig"))) || !strings.Contains(out, "2") {
		t.Fatalf("sign: %q", out)
	}
	vaultRun(t, h, "vault", "key", "verify", "app", "signer", "doc", "--signature", enc([]byte("sig")), "--version", "2")
	if code := h.run("vault", "key", "verify", "app", "signer", "doc", "--signature", enc([]byte("bad")), "--version", "2"); code != 1 {
		t.Fatalf("bad signature: exit %d", code)
	}
}
