// Package httpsig signs requests to the Object Storage and Queue Service
// data planes the way ThailandHosting's SDKs do: RFC 9421 HTTP Message
// Signatures with HMAC-SHA256, labelled "th", over the method, authority,
// path, query and (when there is a body) its Content-Digest.
package httpsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Sign signs req with an access key. body is the exact request body (nil
// for none); the request's URL must carry the host it is sent to.
func Sign(req *http.Request, keyID, secret string, body []byte, now time.Time) {
	components := `"@method" "@authority" "@path" "@query"`
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		req.Header.Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":")
		components += ` "content-digest"`
	}
	params := fmt.Sprintf(`(%s);created=%d;keyid=%s;alg="hmac-sha256"`, components, now.Unix(), sfString(keyID))
	host := strings.ToLower(req.URL.Host)
	host = strings.TrimSuffix(strings.TrimSuffix(host, ":443"), ":80")
	base := fmt.Sprintf("\"@method\": %s\n\"@authority\": %s\n\"@path\": %s\n\"@query\": ?%s\n",
		req.Method, host, req.URL.EscapedPath(), req.URL.RawQuery)
	if len(body) > 0 {
		base += "\"content-digest\": " + req.Header.Get("Content-Digest") + "\n"
	}
	base += `"@signature-params": ` + params
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))
	req.Header.Set("Signature-Input", "th="+params)
	req.Header.Set("Signature", "th=:"+base64.StdEncoding.EncodeToString(mac.Sum(nil))+":")
}

func sfString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
