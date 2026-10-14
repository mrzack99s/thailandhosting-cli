package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
	"github.com/mrzack99s/thailandhosting-cli/internal/httpsig"
)

// th object-storage: buckets through the Console API, objects through the
// Object Storage data plane's own /v1 API (RFC 9421 signed with a data
// key, see datakey.go). Objects are named th://BUCKET/KEY.

func init() {
	register(map[string]handler{
		"object-storage buckets list":   objBucketsList,
		"object-storage buckets create": objBucketsCreate,
		"object-storage buckets delete": objBucketsDelete,
		"object-storage buckets public": objBucketsPublic,
		"object-storage ls":             objLs,
		"object-storage cp":             objCp,
		"object-storage mv":             objMv,
		"object-storage rm":             objRm,
		"object-storage cat":            objCat,
		"object-storage stat":           objStat,
		"object-storage sync":           objSync,
		"object-storage sign":           objSign,
	},
		"object-storage buckets list | create NAME | delete NAME | public NAME",
		"object-storage ls | cp SRC DST | mv SRC DST | rm | cat | stat | sync DIR th://B/P | sign   (objects are th://BUCKET/KEY)",
	)
}

// ---- Console API ----

type objPublicRule struct {
	Prefix string `json:"prefix"`
	List   bool   `json:"list"`
}

type objBucket struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	RegionID    string          `json:"regionId"`
	Status      string          `json:"status"`
	StoredBytes int64           `json:"storedBytes"`
	ObjectCount int64           `json:"objectCount"`
	PublicRules []objPublicRule `json:"publicRules"`
	Endpoint    string          `json:"endpoint"`
	CreatedAt   string          `json:"createdAt"`
}

type objRegion struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Endpoint   string `json:"endpoint"`
	S3Endpoint string `json:"s3Endpoint"`
}

type objOverview struct {
	Buckets []objBucket `json:"buckets"`
	Regions []objRegion `json:"regions"`
}

func (c *cmd) objOverview(cl *api.Client, pid string) (*objOverview, error) {
	var ov objOverview
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/object-storage?projectId="+q(pid), nil, &ov); err != nil {
		return nil, err
	}
	return &ov, nil
}

func (ov *objOverview) bucket(name string) (*objBucket, error) {
	for i := range ov.Buckets {
		if ov.Buckets[i].Name == name && ov.Buckets[i].Status != "deleting" {
			return &ov.Buckets[i], nil
		}
	}
	return nil, fmt.Errorf("bucket %q not found in this Project. List them with: th object-storage buckets list", name)
}

func objPublicSummary(rules []objPublicRule) string {
	if len(rules) == 0 {
		return "-"
	}
	var parts []string
	for _, r := range rules {
		p := r.Prefix
		if p == "" {
			p = "(all)"
		}
		if r.List {
			p += "+list"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ",")
}

// objConsole parses args and returns the client and Project ID.
func (c *cmd) objConsole(fs *flagSet, args []string) (*api.Client, string, error) {
	if err := c.parse(fs, args); err != nil {
		return nil, "", err
	}
	cl, err := c.client(true)
	if err != nil {
		return nil, "", err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return nil, "", err
	}
	return cl, pid, nil
}

func objBucketsList(c *cmd, args []string) error {
	fs := newFlagSet("object-storage buckets list", `Usage: th object-storage buckets list

Lists the Project's Object Storage buckets.`)
	cl, pid, err := c.objConsole(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("usage: th object-storage buckets list")
	}
	ov, err := c.objOverview(cl, pid)
	if err != nil {
		return err
	}
	return c.objPrintBuckets(ov)
}

func (c *cmd) objPrintBuckets(ov *objOverview) error {
	if c.json() {
		if ov.Buckets == nil {
			ov.Buckets = []objBucket{}
		}
		return c.printJSON(ov.Buckets)
	}
	if len(ov.Buckets) == 0 {
		c.info("No buckets yet. Create one with: th object-storage buckets create NAME")
		return nil
	}
	rows := [][]string{}
	for _, b := range ov.Buckets {
		name := b.Name
		if b.Status == "deleting" {
			name += " (deleting)"
		}
		rows = append(rows, []string{name, b.RegionID, fmt.Sprint(b.ObjectCount), humanBytes(b.StoredBytes), objPublicSummary(b.PublicRules), shortTime(b.CreatedAt)})
	}
	c.table([]string{"NAME", "REGION", "OBJECTS", "SIZE", "PUBLIC", "CREATED"}, rows)
	return nil
}

func objRegionChoices(regions []objRegion) string {
	if len(regions) == 0 {
		return "Object Storage is not offered in any region yet."
	}
	var b strings.Builder
	b.WriteString("Object Storage regions:")
	for _, r := range regions {
		fmt.Fprintf(&b, "\n  %s  (%s)", r.ID, r.Name)
	}
	return b.String()
}

func objBucketsCreate(c *cmd, args []string) error {
	fs := newFlagSet("object-storage buckets create", `Usage: th object-storage buckets create NAME [--region REGION]

Creates a bucket. NAME is 3-63 characters of a-z, 0-9, dots and hyphens.
The region defaults to the configured one (th config set region) when it
offers Object Storage, or to the only region that does.`)
	cl, pid, err := c.objConsole(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th object-storage buckets create NAME [--region REGION]")
	}
	name := strings.ToLower(fs.Args()[0])
	ov, err := c.objOverview(cl, pid)
	if err != nil {
		return err
	}
	region := c.regionSetting()
	found := false
	for _, r := range ov.Regions {
		if r.ID == region {
			found = true
		}
	}
	if !found {
		switch {
		case c.g.region != "":
			return fmt.Errorf("region %q does not offer Object Storage\n%s", region, objRegionChoices(ov.Regions))
		case len(ov.Regions) == 1:
			region = ov.Regions[0].ID
		default:
			return fmt.Errorf("pass --region\n%s", objRegionChoices(ov.Regions))
		}
	}
	var b objBucket
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/object-storage/buckets", map[string]string{"projectId": pid, "regionId": region, "name": name}, &b)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Created bucket [%s] in %s.\nEndpoint: %s\n", b.Name, b.RegionID, b.Endpoint)
	return nil
}

func objBucketsDelete(c *cmd, args []string) error {
	var force bool
	fs := newFlagSet("object-storage buckets delete", `Usage: th object-storage buckets delete NAME [--force] [--quiet]

Deletes an empty bucket. With --force a bucket with objects is deleted at
once and its objects are removed in the background. --quiet skips the
prompt.`)
	fs.Bool(&force, "force", "Delete the bucket with all its objects")
	cl, pid, err := c.objConsole(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th object-storage buckets delete NAME [--force]")
	}
	ov, err := c.objOverview(cl, pid)
	if err != nil {
		return err
	}
	b, err := ov.bucket(fs.Args()[0])
	if err != nil {
		return err
	}
	question := fmt.Sprintf("Delete bucket %s?", b.Name)
	if force && b.ObjectCount > 0 {
		question = fmt.Sprintf("Delete bucket %s with all its %d objects (%s)? This cannot be undone.", b.Name, b.ObjectCount, humanBytes(b.StoredBytes))
	}
	ok, err := c.confirmNo(question)
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	path := "/api/v1/object-storage/buckets/" + url.PathEscape(b.ID)
	if force {
		path += "?force=true"
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, path, nil, nil); err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			return fmt.Errorf("%s\nTo delete it with all its objects: th object-storage buckets delete %s --force", err, b.Name)
		}
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"bucket": b.Name, "deleted": true})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted bucket [%s].\n", b.Name)
	return nil
}

func objBucketsPublic(c *cmd, args []string) error {
	var prefix string
	var list, private bool
	fs := newFlagSet("object-storage buckets public", `Usage: th object-storage buckets public NAME [--prefix P [--list]] [--private]

Shows or changes what anyone may read from the bucket without a key.
  --prefix P          make the folder P (e.g. images/) public; --prefix ""
                      makes the whole bucket public
  --prefix P --list   also let anyone list that folder
  --private --prefix P  make that folder private again
  --private           make the whole bucket private (removes every rule)
Without flags it prints the current rules.`)
	fs.String(&prefix, "prefix", "Folder (key prefix) the change applies to")
	fs.Bool(&list, "list", "Also allow listing the public folder")
	fs.Bool(&private, "private", "Remove the rule for --prefix, or all rules")
	cl, pid, err := c.objConsole(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th object-storage buckets public NAME [--prefix P [--list]] [--private]")
	}
	ov, err := c.objOverview(cl, pid)
	if err != nil {
		return err
	}
	b, err := ov.bucket(fs.Args()[0])
	if err != nil {
		return err
	}
	rules := append([]objPublicRule{}, b.PublicRules...)
	hasPrefix := fs.Set("prefix")
	switch {
	case private && hasPrefix:
		kept := []objPublicRule{}
		for _, r := range rules {
			if r.Prefix != prefix {
				kept = append(kept, r)
			}
		}
		rules = kept
	case private:
		rules = []objPublicRule{}
	case hasPrefix:
		replaced := false
		for i := range rules {
			if rules[i].Prefix == prefix {
				rules[i].List, replaced = list, true
			}
		}
		if !replaced {
			rules = append(rules, objPublicRule{Prefix: prefix, List: list})
		}
	default:
		if list {
			return errors.New("--list needs --prefix")
		}
		return c.objPrintRules(b.Name, b.PublicRules)
	}
	var out struct {
		Rules []objPublicRule `json:"rules"`
	}
	if _, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/object-storage/buckets/"+url.PathEscape(b.ID)+"/public", map[string]any{"rules": rules}, &out); err != nil {
		return err
	}
	return c.objPrintRules(b.Name, out.Rules)
}

func (c *cmd) objPrintRules(bucket string, rules []objPublicRule) error {
	if rules == nil {
		rules = []objPublicRule{}
	}
	if c.json() {
		return c.printJSON(map[string]any{"bucket": bucket, "rules": rules})
	}
	if len(rules) == 0 {
		fmt.Fprintf(c.app.Stdout, "Bucket [%s] is private.\n", bucket)
		return nil
	}
	rows := [][]string{}
	for _, r := range rules {
		p := "th://" + bucket + "/" + r.Prefix
		list := "no"
		if r.List {
			list = "yes"
		}
		rows = append(rows, []string{p, list})
	}
	c.table([]string{"PUBLIC", "LIST"}, rows)
	return nil
}

// ---- data plane ----

// objSession talks to the data plane of a Project's buckets.
type objSession struct {
	c       *cmd
	cl      *api.Client
	pid     string
	ov      *objOverview
	key     DataKey
	haveKey bool
	retried bool
	hc      *http.Client
}

// objSession parses args, signs in and loads the Project's buckets.
func (c *cmd) objSession(fs *flagSet, args []string) (*objSession, error) {
	cl, pid, err := c.objConsole(fs, args)
	if err != nil {
		return nil, err
	}
	ov, err := c.objOverview(cl, pid)
	if err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 2 * time.Minute
	return &objSession{c: c, cl: cl, pid: pid, ov: ov, hc: &http.Client{Transport: t}}, nil
}

type objReq struct {
	method string
	bucket string
	// path follows /v1/buckets/{bucket}, already escaped ("/objects/a%20b").
	path        string
	query       url.Values
	body        []byte
	contentType string
	header      http.Header
}

// objProblem is an error answer of the data plane (RFC 9457).
type objProblem struct {
	status int
	code   string
	msg    string
}

func (e *objProblem) Error() string { return e.msg }

var objCodeText = map[string]string{
	"object_not_found":  "The object does not exist.",
	"bucket_not_found":  "The bucket does not exist.",
	"forbidden":         "This access key may not do that.",
	"project_suspended": "This bucket's Project is suspended. Top up its wallet to read and write again.",
}

func objReadProblem(resp *http.Response) *objProblem {
	defer resp.Body.Close()
	e := &objProblem{status: resp.StatusCode, code: resp.Header.Get("X-Error-Code")}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var doc struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Code   string `json:"code"`
	}
	if json.Unmarshal(data, &doc) == nil {
		if doc.Code != "" {
			e.code = doc.Code
		}
		e.msg = doc.Detail
		if e.msg == "" {
			e.msg = doc.Title
		}
	}
	if e.msg == "" {
		e.msg = objCodeText[e.code]
	}
	if e.msg == "" {
		e.msg = fmt.Sprintf("Object Storage answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return e
}

func objIsNotFound(err error) bool {
	var p *objProblem
	return errors.As(err, &p) && p.status == http.StatusNotFound
}

func objEscapeKey(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func objKeyPath(key string) string { return "/objects/" + objEscapeKey(key) }

// do sends r signed with the Project's data key and returns the response
// with an open body when it is a 2xx (the caller closes it). A key the data
// plane refuses is dropped from the cache and replaced once; a busy answer
// is retried a few times.
func (s *objSession) do(r objReq) (*http.Response, error) {
	b, err := s.ov.bucket(r.bucket)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(strings.TrimRight(b.Endpoint, "/"), "/v1")
	full := base + "/v1/buckets/" + url.PathEscape(b.Name) + r.path
	if len(r.query) > 0 {
		full += "?" + r.query.Encode()
	}
	for attempt := 0; ; attempt++ {
		if !s.haveKey {
			k, err := s.c.dataKey(s.cl, dataKeyObjects, s.pid)
			if err != nil {
				return nil, err
			}
			s.key, s.haveKey = k, true
		}
		var body io.Reader
		if r.body != nil {
			body = bytes.NewReader(r.body)
		}
		req, err := http.NewRequestWithContext(s.c.ctx, r.method, full, body)
		if err != nil {
			return nil, err
		}
		if r.body != nil {
			req.ContentLength = int64(len(r.body))
			if len(r.body) == 0 {
				req.Body = http.NoBody
			}
		}
		for k, v := range r.header {
			req.Header[k] = v
		}
		if r.contentType != "" {
			req.Header.Set("Content-Type", r.contentType)
		}
		req.Header.Set("User-Agent", s.c.userAgent())
		httpsig.Sign(req, s.key.KeyID, s.key.Secret, r.body, time.Now())
		resp, err := s.hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("cannot reach %s: %w", req.URL.Host, err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		perr := objReadProblem(resp)
		refused := perr.status == http.StatusUnauthorized || (perr.status == http.StatusForbidden && perr.code == "forbidden")
		if refused && !s.retried {
			s.retried = true
			s.c.forgetDataKey(dataKeyObjects, s.pid)
			s.haveKey = false
			continue
		}
		if perr.status == http.StatusUnauthorized {
			env := dataKeyEnv[dataKeyObjects]
			if s.c.app.Getenv(env[0]) != "" {
				perr.msg = fmt.Sprintf("Object Storage refused the access key in %s (deleted, or the secret in %s is wrong).", env[0], env[1])
			}
		}
		busy := perr.status == http.StatusServiceUnavailable || perr.status == http.StatusTooManyRequests
		if busy && attempt < 3 {
			s.c.app.Sleep(time.Second << attempt)
			continue
		}
		return nil, perr
	}
}

// doJSON sends in (if any) as JSON and decodes the answer into out.
func (s *objSession) doJSON(method, bucket, path string, query url.Values, in, out any) error {
	r := objReq{method: method, bucket: bucket, path: path, query: query}
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		r.body, r.contentType = data, "application/json"
	}
	resp, err := s.do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("unexpected answer from Object Storage: %w", err)
	}
	return nil
}
