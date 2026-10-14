// Package api is a small client for the ThailandHosting Console API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls /api/v1 on Endpoint with a bearer token.
type Client struct {
	Endpoint  string
	Token     string
	UserAgent string
	// APIKey marks Token as a thk_ key (only changes the 401 message).
	APIKey bool
	// HTTP is used for ordinary calls (60 s timeout); Upload for streamed
	// bodies (no total timeout).
	HTTP   *http.Client
	Upload *http.Client
}

// New returns a client with the default timeouts.
func New(endpoint, token, userAgent string) *Client {
	return &Client{
		Endpoint:  strings.TrimRight(endpoint, "/"),
		Token:     token,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		Upload:    &http.Client{},
	}
}

// Error is a non-2xx answer.
type Error struct {
	Status int
	// Code is the "error" field. For most endpoints it is the
	// customer-facing message; the device-login endpoints use it for a code
	// such as authorization_pending and put the text in Message.
	Code    string
	Message string
	APIKey  bool
}

func (e *Error) Error() string {
	if e.Status == http.StatusUnauthorized {
		if e.APIKey {
			return "The API key in TH_API_KEY was rejected (revoked, expired or missing the run:* scope)."
		}
		return "Not signed in or token expired. Run: th auth login"
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("request failed: %d %s", e.Status, http.StatusText(e.Status))
}

// Do sends a JSON request (in may be nil) and decodes a JSON answer into
// out (may be nil). It returns the raw answer body.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) ([]byte, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.send(c.HTTP, req, out)
}

// UploadStream posts body (of the given length) with no total timeout.
func (c *Client) UploadStream(ctx context.Context, path, contentType string, body io.Reader, length int64, out any) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", contentType)
	return c.send(c.Upload, req, out)
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

func (c *Client) send(hc *http.Client, req *http.Request, out any) ([]byte, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.Endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := &Error{Status: resp.StatusCode, APIKey: c.APIKey}
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &body) == nil {
			e.Code, e.Message = body.Error, body.Message
		} else if s := strings.TrimSpace(string(raw)); s != "" && len(s) < 500 {
			e.Message = s
		}
		return raw, e
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, fmt.Errorf("unexpected answer from %s: %w", req.URL.Path, err)
		}
	}
	return raw, nil
}
