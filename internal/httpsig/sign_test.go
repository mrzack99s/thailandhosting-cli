package httpsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The signature base must match the server's (internal/httpsig in the main
// repo) byte for byte; this rebuilds it independently.
func TestSign(t *testing.T) {
	req, _ := http.NewRequest("PUT", "https://objects.example.test/bkk/v1/buckets/b/objects/a%20b.txt?x=1", nil)
	body := []byte("hello")
	now := time.Unix(1700000000, 0)
	Sign(req, "THAKKEY", "secret", body, now)
	digest := req.Header.Get("Content-Digest")
	sum := sha256.Sum256(body)
	if digest != "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":" {
		t.Fatalf("digest %s", digest)
	}
	params := `("@method" "@authority" "@path" "@query" "content-digest");created=1700000000;keyid="THAKKEY";alg="hmac-sha256"`
	if req.Header.Get("Signature-Input") != "th="+params {
		t.Fatalf("input %s", req.Header.Get("Signature-Input"))
	}
	base := "\"@method\": PUT\n\"@authority\": objects.example.test\n\"@path\": /bkk/v1/buckets/b/objects/a%20b.txt\n\"@query\": ?x=1\n\"content-digest\": " + digest + "\n\"@signature-params\": " + params
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(base))
	if want := "th=:" + base64.StdEncoding.EncodeToString(mac.Sum(nil)) + ":"; req.Header.Get("Signature") != want {
		t.Fatal("signature mismatch")
	}
	// No body: no digest component.
	get, _ := http.NewRequest("GET", "https://queues.example.test:443/bkk/v1/queues", nil)
	Sign(get, "THQK", "s", nil, now)
	if strings.Contains(get.Header.Get("Signature-Input"), "content-digest") || get.Header.Get("Content-Digest") != "" {
		t.Fatal("digest on an empty body")
	}
}
