package cli

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Uploads above objMultipartThreshold go in parts of objPartSize (more
// when the object would need over 10,000 parts), objConcurrency at a
// time. Variables so tests can lower them.
var (
	objMultipartThreshold int64 = 64 << 20
	objPartSize           int64 = 16 << 20
	objConcurrency              = 4
)

const objMaxParts = 10000

// objLoc is a th://BUCKET/KEY name or a local path.
type objLoc struct {
	remote bool
	bucket string
	key    string // may be "" or end in "/" (a folder)
	local  string
}

func (l objLoc) String() string {
	if l.remote {
		return "th://" + l.bucket + "/" + l.key
	}
	return l.local
}

func parseObjLoc(s string) (objLoc, error) {
	rest, ok := strings.CutPrefix(s, "th://")
	if !ok {
		if strings.Contains(s, "://") {
			return objLoc{}, fmt.Errorf("%q: objects are named th://BUCKET/KEY", s)
		}
		return objLoc{local: s}, nil
	}
	bucket, key, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return objLoc{}, fmt.Errorf("%q has no bucket: use th://BUCKET/KEY", s)
	}
	return objLoc{remote: true, bucket: bucket, key: key}, nil
}

func mustRemote(s, usage string) (objLoc, error) {
	l, err := parseObjLoc(s)
	if err != nil {
		return l, err
	}
	if !l.remote {
		return l, fmt.Errorf("%q is not an object: use th://BUCKET/KEY\n%s", s, usage)
	}
	return l, nil
}

// asFolder makes a non-empty prefix end in "/".
func asFolder(prefix string) string {
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix
}

type objEntry struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"lastModified"`
}

// list lists a bucket under prefix, every page; without recursive the
// sub-folders come back as folders.
func (s *objSession) list(bucket, prefix string, recursive bool) ([]objEntry, []string, error) {
	var objs []objEntry
	var folders []string
	cursor := ""
	for {
		qv := url.Values{"limit": {"1000"}}
		if prefix != "" {
			qv.Set("prefix", prefix)
		}
		if !recursive {
			qv.Set("delimiter", "/")
		}
		if cursor != "" {
			qv.Set("cursor", cursor)
		}
		var page struct {
			Objects    []objEntry `json:"objects"`
			Folders    []string   `json:"folders"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := s.doJSON(http.MethodGet, bucket, "/objects", qv, nil, &page); err != nil {
			return nil, nil, err
		}
		objs = append(objs, page.Objects...)
		folders = append(folders, page.Folders...)
		if page.NextCursor == "" {
			return objs, folders, nil
		}
		cursor = page.NextCursor
	}
}

// listUnder lists every object under a folder-like prefix (recursive);
// a prefix not ending in "/" matches that object and its folder.
func (s *objSession) listUnder(bucket, prefix string) ([]objEntry, error) {
	objs, _, err := s.list(bucket, prefix, true)
	if err != nil {
		return nil, err
	}
	if prefix == "" || strings.HasSuffix(prefix, "/") {
		return objs, nil
	}
	out := objs[:0]
	for _, o := range objs {
		if o.Key == prefix || strings.HasPrefix(o.Key, prefix+"/") {
			out = append(out, o)
		}
	}
	return out, nil
}

func objTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// ---- ls ----

func objLs(c *cmd, args []string) error {
	var recursive bool
	fs := newFlagSet("object-storage ls", `Usage: th object-storage ls [th://BUCKET[/PREFIX]] [--recursive]

Without an argument lists the Project's buckets. With th://BUCKET lists the
objects and folders at the top of the bucket, with th://BUCKET/PREFIX those
under PREFIX. --recursive lists every object below, without folders.`)
	fs.Bool(&recursive, "recursive", "List every object under the prefix")
	fs.add(&flagDef{name: "r", help: "Same as --recursive", kind: kindBool, b: &recursive})
	s, err := c.objSession(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return errors.New("usage: th object-storage ls [th://BUCKET[/PREFIX]]")
	}
	if len(fs.Args()) == 0 {
		return c.objPrintBuckets(s.ov)
	}
	loc, err := mustRemote(fs.Args()[0], "usage: th object-storage ls [th://BUCKET[/PREFIX]]")
	if err != nil {
		return err
	}
	prefix := loc.key
	objs, folders, err := s.list(loc.bucket, prefix, recursive)
	if err != nil {
		return err
	}
	// th://b/photos lists inside the folder photos/ (not just its name).
	if !recursive && prefix != "" && !strings.HasSuffix(prefix, "/") && len(objs) == 0 && len(folders) == 1 && folders[0] == prefix+"/" {
		prefix += "/"
		if objs, folders, err = s.list(loc.bucket, prefix, false); err != nil {
			return err
		}
	}
	if c.json() {
		if objs == nil {
			objs = []objEntry{}
		}
		if folders == nil {
			folders = []string{}
		}
		return c.printJSON(map[string]any{"bucket": loc.bucket, "prefix": prefix, "objects": objs, "folders": folders})
	}
	if len(objs) == 0 && len(folders) == 0 {
		c.info("Nothing under %s", loc)
		return nil
	}
	rows := [][]string{}
	for _, f := range folders {
		rows = append(rows, []string{"-", "-", "th://" + loc.bucket + "/" + f})
	}
	var total int64
	for _, o := range objs {
		total += o.Size
		rows = append(rows, []string{humanBytes(o.Size), objTime(o.LastModified), "th://" + loc.bucket + "/" + o.Key})
	}
	c.table([]string{"SIZE", "MODIFIED", "NAME"}, rows)
	if recursive {
		c.info("%d objects, %s", len(objs), humanBytes(total))
	}
	return nil
}

// ---- upload / download ----

type objInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"lastModified"`
	ContentType  string    `json:"contentType"`
}

func objGuessType(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	return "application/octet-stream"
}

// objCheckETag compares an MD5 ETag (not a multipart one) with what was sent.
func objCheckETag(etag string, sum []byte) error {
	etag = strings.Trim(etag, `"`)
	if len(etag) != 32 || strings.Contains(etag, "-") {
		return nil
	}
	if etag != hex.EncodeToString(sum) {
		return fmt.Errorf("the stored data does not match what was sent (ETag %s, MD5 %x); try again", etag, sum)
	}
	return nil
}

// upload sends a local file as bucket/key: in one request up to
// objMultipartThreshold, else in parts sent in parallel.
func (s *objSession) upload(bucket, key, file, contentType string) (objInfo, error) {
	f, err := os.Open(file)
	if err != nil {
		return objInfo{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return objInfo{}, err
	}
	if contentType == "" {
		contentType = objGuessType(file)
	}
	size := st.Size()
	if size > objMultipartThreshold {
		return s.uploadParts(bucket, key, f, size, contentType)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(f, data); err != nil {
		return objInfo{}, err
	}
	var info objInfo
	resp, err := s.do(objReq{method: http.MethodPut, bucket: bucket, path: objKeyPath(key), body: data, contentType: contentType})
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	if err := jsonDecode(resp.Body, &info); err != nil {
		return info, err
	}
	sum := md5.Sum(data)
	return info, objCheckETag(info.ETag, sum[:])
}

func jsonDecode(r io.Reader, out any) error {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected answer from Object Storage: %w", err)
	}
	return nil
}

type objPart struct {
	Number int    `json:"number"`
	ETag   string `json:"etag"`
}

func (s *objSession) uploadParts(bucket, key string, f *os.File, size int64, contentType string) (objInfo, error) {
	ps := objPartSize
	for (size+ps-1)/ps > objMaxParts {
		ps *= 2
	}
	count := int((size + ps - 1) / ps)
	var up struct {
		UploadID string `json:"uploadId"`
	}
	if err := s.doJSON(http.MethodPost, bucket, "/uploads", nil, map[string]string{"key": key, "contentType": contentType}, &up); err != nil {
		return objInfo{}, err
	}
	upPath := "/uploads/" + up.UploadID
	abort := func() { _ = s.doJSON(http.MethodDelete, bucket, upPath, nil, nil, nil) }
	name := "th://" + bucket + "/" + key
	s.c.info("Uploading %s in %d parts of %s", name, count, humanBytes(ps))
	parts := make([]objPart, count)
	var (
		mu       sync.Mutex
		firstErr error
		done     int
		sent     int64
		wg       sync.WaitGroup
		next     = make(chan int)
	)
	for w := 0; w < min(objConcurrency, count); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				off := int64(i) * ps
				n := min(ps, size-off)
				buf := make([]byte, n)
				var p objPart
				_, err := f.ReadAt(buf, off)
				if err == nil {
					var resp *http.Response
					resp, err = s.do(objReq{method: http.MethodPut, bucket: bucket, path: upPath + "/parts/" + strconv.Itoa(i+1), body: buf, contentType: "application/octet-stream"})
					if err == nil {
						err = jsonDecode(resp.Body, &p)
						resp.Body.Close()
					}
					if err == nil {
						sum := md5.Sum(buf)
						if err = objCheckETag(p.ETag, sum[:]); err != nil {
							err = fmt.Errorf("part %d: %w", i+1, err)
						}
					}
				}
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
				} else {
					parts[i] = objPart{Number: i + 1, ETag: p.ETag}
					done++
					sent += n
					s.c.info("  %s: %d/%d parts, %s of %s", name, done, count, humanBytes(sent), humanBytes(size))
				}
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < count; i++ {
		mu.Lock()
		failed := firstErr != nil
		mu.Unlock()
		if failed {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	if firstErr != nil {
		abort()
		return objInfo{}, firstErr
	}
	var info objInfo
	if err := s.doJSON(http.MethodPost, bucket, upPath+"/complete", nil, map[string]any{"parts": parts}, &info); err != nil {
		abort()
		return objInfo{}, err
	}
	return info, nil
}

// download writes bucket/key to file through a temporary file in the same
// folder, renamed into place once complete; "-" writes to stdout.
func (s *objSession) download(bucket, key, file string) (int64, error) {
	resp, err := s.do(objReq{method: http.MethodGet, bucket: bucket, path: objKeyPath(key)})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	h := sha256.New()
	if file == "-" {
		n, err := io.Copy(s.c.app.Stdout, io.TeeReader(resp.Body, h))
		if err == nil {
			err = objCheckDigest(resp.Header.Get("Repr-Digest"), h.Sum(nil))
		}
		return n, err
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, ".th-download-*")
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(tmp, io.TeeReader(resp.Body, h))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && resp.ContentLength >= 0 && n != resp.ContentLength {
		err = fmt.Errorf("download of th://%s/%s stopped after %d of %d bytes", bucket, key, n, resp.ContentLength)
	}
	if err == nil {
		err = objCheckDigest(resp.Header.Get("Repr-Digest"), h.Sum(nil))
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return n, err
	}
	return n, nil
}

// objCheckDigest compares a sha-256 Repr-Digest (RFC 9530) with what arrived.
func objCheckDigest(field string, sum []byte) error {
	for _, m := range strings.Split(field, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(m), "=")
		if !ok || k != "sha-256" {
			continue
		}
		want, err := base64.StdEncoding.DecodeString(strings.Trim(v, ":"))
		if err != nil {
			return nil
		}
		if hex.EncodeToString(want) != hex.EncodeToString(sum) {
			return errors.New("the downloaded data does not match the object's digest; try again")
		}
	}
	return nil
}

// ---- cp / mv ----

type objCopied struct {
	From string `json:"from"`
	To   string `json:"to"`
	Size int64  `json:"size"`
	// what to delete for mv
	srcLocal  string
	srcBucket string
	srcKey    string
}

type objCopyJob struct {
	src, dst    objLoc
	size        int64
	contentType string
}

func objCp(c *cmd, args []string) error { return objCopyCmd(c, args, false) }
func objMv(c *cmd, args []string) error { return objCopyCmd(c, args, true) }

func objCopyCmd(c *cmd, args []string, move bool) error {
	var recursive bool
	var contentType string
	name := "cp"
	what := "Copies"
	if move {
		name, what = "mv", "Moves"
	}
	fs := newFlagSet("object-storage "+name, fmt.Sprintf(`Usage: th object-storage %[1]s SRC DST [-r] [--content-type TYPE]

%[2]s files to, from or between buckets. Objects are th://BUCKET/KEY;
anything else is a local path ("-" as DST writes to stdout).
  th object-storage %[1]s photo.jpg th://media/photos/      -> photos/photo.jpg
  th object-storage %[1]s th://media/a.pdf ./                -> ./a.pdf
  th object-storage %[1]s th://media/a.pdf th://archive/a.pdf (copied inside Object Storage)
With -r, SRC is a folder (or a prefix) and its contents go under DST, like
aws s3 cp --recursive. Files over 64 MiB are uploaded in parallel parts.
The content type is guessed from the file extension unless given.`, name, what))
	fs.Bool(&recursive, "recursive", "Copy a folder or prefix and everything under it")
	fs.add(&flagDef{name: "r", help: "Same as --recursive", kind: kindBool, b: &recursive})
	fs.String(&contentType, "content-type", "Content type of uploaded objects (default: by extension)")
	s, err := c.objSession(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return fmt.Errorf("usage: th object-storage %s SRC DST [-r]", name)
	}
	src, err := parseObjLoc(fs.Args()[0])
	if err != nil {
		return err
	}
	dst, err := parseObjLoc(fs.Args()[1])
	if err != nil {
		return err
	}
	if !src.remote && !dst.remote {
		return errors.New("one of SRC and DST must be th://BUCKET/KEY (to copy local files use your shell)")
	}
	if move && !src.remote && src.local == "-" {
		return errors.New("cannot move from stdin")
	}
	jobs, err := s.copyJobs(src, dst, recursive, contentType)
	if err != nil {
		return err
	}
	var done []objCopied
	for _, j := range jobs {
		r, err := s.runCopy(j)
		if err != nil {
			return fmt.Errorf("%s -> %s: %w", j.src, j.dst, err)
		}
		done = append(done, r)
		if move {
			if r.srcLocal != "" {
				err = os.Remove(r.srcLocal)
			} else {
				err = s.doJSON(http.MethodDelete, r.srcBucket, objKeyPath(r.srcKey), nil, nil, nil)
			}
			if err != nil {
				return fmt.Errorf("copied %s but could not remove it: %w", r.From, err)
			}
		}
	}
	if c.json() {
		if done == nil {
			done = []objCopied{}
		}
		return c.printJSON(done)
	}
	verb := "Copied"
	if move {
		verb = "Moved"
	}
	if len(done) == 0 {
		c.info("Nothing to %s.", name)
	} else if dst.local != "-" {
		var total int64
		for _, d := range done {
			total += d.Size
		}
		c.info("%s %d file(s), %s.", verb, len(done), humanBytes(total))
	}
	return nil
}

// copyJobs expands SRC and DST into single-file copies.
func (s *objSession) copyJobs(src, dst objLoc, recursive bool, contentType string) ([]objCopyJob, error) {
	if !src.remote {
		st, err := os.Stat(src.local)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			key := dst.key
			if key == "" || strings.HasSuffix(key, "/") {
				key += filepath.Base(src.local)
			}
			return []objCopyJob{{src: src, dst: objLoc{remote: true, bucket: dst.bucket, key: key}, size: st.Size(), contentType: contentType}}, nil
		}
		if !recursive {
			return nil, fmt.Errorf("%s is a folder: pass -r to copy everything in it", src.local)
		}
		prefix := asFolder(dst.key)
		var jobs []objCopyJob
		err = filepath.WalkDir(src.local, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return err
			}
			rel, err := filepath.Rel(src.local, p)
			if err != nil {
				return err
			}
			jobs = append(jobs, objCopyJob{src: objLoc{local: p}, dst: objLoc{remote: true, bucket: dst.bucket, key: prefix + filepath.ToSlash(rel)}, contentType: contentType})
			return nil
		})
		return jobs, err
	}
	if _, err := s.ov.bucket(src.bucket); err != nil {
		return nil, err
	}
	if !recursive {
		if src.key == "" || strings.HasSuffix(src.key, "/") {
			return nil, fmt.Errorf("%s is a folder: pass -r to copy everything in it", src)
		}
		return []objCopyJob{{src: src, dst: objTarget(dst, path.Base(src.key), false)}}, nil
	}
	objs, err := s.listUnder(src.bucket, src.key)
	if err != nil {
		return nil, err
	}
	base := asFolder(src.key)
	var jobs []objCopyJob
	for _, o := range objs {
		if strings.HasSuffix(o.Key, "/") {
			continue // a folder marker
		}
		rel := strings.TrimPrefix(o.Key, base)
		if o.Key == src.key {
			rel = path.Base(o.Key)
		}
		if !dst.remote && !filepath.IsLocal(filepath.FromSlash(rel)) {
			return nil, fmt.Errorf("refusing to write %s outside %s", o.Key, dst.local)
		}
		jobs = append(jobs, objCopyJob{src: objLoc{remote: true, bucket: src.bucket, key: o.Key}, dst: objTarget(dst, rel, true), size: o.Size})
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no objects under %s", src)
	}
	return jobs, nil
}

// objTarget is where an object named rel (relative to the source) goes.
func objTarget(dst objLoc, rel string, recursive bool) objLoc {
	if dst.remote {
		key := dst.key
		if recursive {
			key = asFolder(key) + rel
		} else if key == "" || strings.HasSuffix(key, "/") {
			key += rel
		}
		return objLoc{remote: true, bucket: dst.bucket, key: key}
	}
	if dst.local == "-" {
		return dst
	}
	if recursive {
		return objLoc{local: filepath.Join(dst.local, filepath.FromSlash(rel))}
	}
	if st, err := os.Stat(dst.local); (err == nil && st.IsDir()) || strings.HasSuffix(dst.local, "/") || strings.HasSuffix(dst.local, string(filepath.Separator)) {
		return objLoc{local: filepath.Join(dst.local, rel)}
	}
	return dst
}

func (s *objSession) runCopy(j objCopyJob) (objCopied, error) {
	r := objCopied{From: j.src.String(), To: j.dst.String()}
	switch {
	case !j.src.remote:
		r.srcLocal = j.src.local
		s.c.info("Uploading %s to %s", j.src.local, j.dst)
		info, err := s.upload(j.dst.bucket, j.dst.key, j.src.local, j.contentType)
		r.Size = info.Size
		return r, err
	case !j.dst.remote:
		r.srcBucket, r.srcKey = j.src.bucket, j.src.key
		if j.dst.local != "-" {
			s.c.info("Downloading %s to %s", j.src, j.dst.local)
		}
		n, err := s.download(j.src.bucket, j.src.key, j.dst.local)
		r.Size = n
		return r, err
	default:
		r.srcBucket, r.srcKey = j.src.bucket, j.src.key
		s.c.info("Copying %s to %s", j.src, j.dst)
		var info objInfo
		err := s.doJSON(http.MethodPost, j.dst.bucket, "/copy", nil, map[string]any{
			"from": map[string]string{"bucket": j.src.bucket, "key": j.src.key}, "key": j.dst.key}, &info)
		r.Size = info.Size
		return r, err
	}
}

// ---- rm / cat / stat ----

func objRm(c *cmd, args []string) error {
	var recursive bool
	fs := newFlagSet("object-storage rm", `Usage: th object-storage rm th://BUCKET/KEY [-r] [--quiet]

Deletes an object. With -r deletes every object under the prefix (after a
prompt that --quiet skips).`)
	fs.Bool(&recursive, "recursive", "Delete everything under the prefix")
	fs.add(&flagDef{name: "r", help: "Same as --recursive", kind: kindBool, b: &recursive})
	s, err := c.objSession(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th object-storage rm th://BUCKET/KEY [-r]")
	}
	loc, err := mustRemote(fs.Args()[0], "usage: th object-storage rm th://BUCKET/KEY [-r]")
	if err != nil {
		return err
	}
	var keys []string
	if recursive {
		objs, err := s.listUnder(loc.bucket, loc.key)
		if err != nil {
			return err
		}
		for _, o := range objs {
			keys = append(keys, o.Key)
		}
		if len(keys) == 0 {
			return fmt.Errorf("no objects under %s", loc)
		}
		ok, err := c.confirmNo(fmt.Sprintf("Delete %d object(s) under %s?", len(keys), loc))
		if err != nil {
			return err
		}
		if !ok {
			return &exitError{code: 1, msg: "Aborted."}
		}
	} else {
		if loc.key == "" || strings.HasSuffix(loc.key, "/") {
			return fmt.Errorf("%s is a folder: pass -r to delete everything under it", loc)
		}
		if _, err := s.head(loc.bucket, loc.key); err != nil {
			return fmt.Errorf("%s: %w", loc, err)
		}
		keys = []string{loc.key}
	}
	for _, k := range keys {
		if err := s.doJSON(http.MethodDelete, loc.bucket, objKeyPath(k), nil, nil, nil); err != nil {
			return fmt.Errorf("th://%s/%s: %w", loc.bucket, k, err)
		}
		if !c.json() {
			c.info("Deleted th://%s/%s", loc.bucket, k)
		}
	}
	if c.json() {
		return c.printJSON(map[string]any{"bucket": loc.bucket, "deleted": keys})
	}
	return nil
}

func objCat(c *cmd, args []string) error {
	fs := newFlagSet("object-storage cat", `Usage: th object-storage cat th://BUCKET/KEY

Writes an object to standard output.`)
	s, err := c.objSession(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th object-storage cat th://BUCKET/KEY")
	}
	loc, err := mustRemote(fs.Args()[0], "usage: th object-storage cat th://BUCKET/KEY")
	if err != nil {
		return err
	}
	if _, err := s.download(loc.bucket, loc.key, "-"); err != nil {
		return fmt.Errorf("%s: %w", loc, err)
	}
	return nil
}

type objStatInfo struct {
	URL          string            `json:"url"`
	Size         int64             `json:"size"`
	ContentType  string            `json:"contentType"`
	ETag         string            `json:"etag"`
	LastModified string            `json:"lastModified"`
	Digest       string            `json:"digest,omitempty"`
	CacheControl string            `json:"cacheControl,omitempty"`
	Metadata     map[string]string `json:"metadata"`
}

func (s *objSession) head(bucket, key string) (objStatInfo, error) {
	resp, err := s.do(objReq{method: http.MethodHead, bucket: bucket, path: objKeyPath(key)})
	if err != nil {
		return objStatInfo{}, err
	}
	resp.Body.Close()
	h := resp.Header
	size, _ := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	info := objStatInfo{URL: "th://" + bucket + "/" + key, Size: size, ContentType: h.Get("Content-Type"), ETag: strings.Trim(h.Get("ETag"), `"`),
		Digest: h.Get("Repr-Digest"), CacheControl: h.Get("Cache-Control"), Metadata: map[string]string{}}
	if t, err := http.ParseTime(h.Get("Last-Modified")); err == nil {
		info.LastModified = t.UTC().Format(time.RFC3339)
	}
	for k, v := range h {
		if m, ok := strings.CutPrefix(strings.ToLower(k), "x-meta-"); ok && len(v) > 0 {
			info.Metadata[m] = v[0]
		}
	}
	return info, nil
}

func objStat(c *cmd, args []string) error {
	fs := newFlagSet("object-storage stat", `Usage: th object-storage stat th://BUCKET/KEY

Shows an object's size, type, ETag, digest and metadata.`)
	s, err := c.objSession(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th object-storage stat th://BUCKET/KEY")
	}
	loc, err := mustRemote(fs.Args()[0], "usage: th object-storage stat th://BUCKET/KEY")
	if err != nil {
		return err
	}
	info, err := s.head(loc.bucket, loc.key)
	if err != nil {
		return fmt.Errorf("%s: %w", loc, err)
	}
	if c.json() {
		return c.printJSON(info)
	}
	out := c.app.Stdout
	fmt.Fprintf(out, "%s\n", info.URL)
	fmt.Fprintf(out, "  Size:          %s (%d bytes)\n", humanBytes(info.Size), info.Size)
	fmt.Fprintf(out, "  Content-Type:  %s\n", dash(info.ContentType))
	fmt.Fprintf(out, "  Modified:      %s\n", shortTime(info.LastModified))
	fmt.Fprintf(out, "  ETag:          %s\n", dash(info.ETag))
	if info.Digest != "" {
		fmt.Fprintf(out, "  Digest:        %s\n", info.Digest)
	}
	if info.CacheControl != "" {
		fmt.Fprintf(out, "  Cache-Control: %s\n", info.CacheControl)
	}
	keys := make([]string, 0, len(info.Metadata))
	for k := range info.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(out, "  Meta %s: %s\n", k, info.Metadata[k])
	}
	return nil
}

// ---- sync ----

func objFileMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func objSync(c *cmd, args []string) error {
	var del, dryRun bool
	var contentType string
	flags := newFlagSet("object-storage sync", `Usage: th object-storage sync LOCAL_DIR th://BUCKET[/PREFIX] [--delete] [--dry-run]

Uploads the files of LOCAL_DIR that are new or changed under PREFIX/. A
file is unchanged when its size matches and its MD5 matches the object's
ETag (objects uploaded in parts have no MD5 ETag: then a local file newer
than the object counts as changed). --delete also deletes objects under
PREFIX/ that are no longer in LOCAL_DIR (after a prompt --quiet skips).`)
	flags.Bool(&del, "delete", "Delete objects that are not in LOCAL_DIR")
	flags.Bool(&dryRun, "dry-run", "Only print what would change")
	flags.String(&contentType, "content-type", "Content type of uploaded objects (default: by extension)")
	s, err := c.objSession(flags, args)
	if err != nil {
		return err
	}
	usage := "usage: th object-storage sync LOCAL_DIR th://BUCKET[/PREFIX] [--delete]"
	if len(flags.Args()) != 2 {
		return errors.New(usage)
	}
	dir := flags.Args()[0]
	if st, err := os.Stat(dir); err != nil {
		return err
	} else if !st.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	loc, err := mustRemote(flags.Args()[1], usage)
	if err != nil {
		return err
	}
	prefix := asFolder(loc.key)
	objs, _, err := s.list(loc.bucket, prefix, true)
	if err != nil {
		return err
	}
	remote := map[string]objEntry{}
	for _, o := range objs {
		remote[o.Key] = o
	}
	type change struct {
		file, key string
	}
	var uploads []change
	local := map[string]bool{}
	unchanged := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		key := prefix + filepath.ToSlash(rel)
		local[key] = true
		o, ok := remote[key]
		if ok {
			st, err := d.Info()
			if err != nil {
				return err
			}
			if st.Size() == o.Size {
				etag := strings.Trim(o.ETag, `"`)
				if len(etag) == 32 && !strings.Contains(etag, "-") {
					sum, err := objFileMD5(p)
					if err != nil {
						return err
					}
					if sum == etag {
						unchanged++
						return nil
					}
				} else if !st.ModTime().After(o.LastModified) {
					unchanged++
					return nil
				}
			}
		}
		uploads = append(uploads, change{p, key})
		return nil
	})
	if err != nil {
		return err
	}
	var deletes []string
	if del {
		for _, o := range objs {
			if !local[o.Key] {
				deletes = append(deletes, o.Key)
			}
		}
	}
	type result struct {
		Uploaded  []string `json:"uploaded"`
		Deleted   []string `json:"deleted"`
		Unchanged int      `json:"unchanged"`
		DryRun    bool     `json:"dryRun,omitempty"`
	}
	res := result{Uploaded: []string{}, Deleted: []string{}, Unchanged: unchanged, DryRun: dryRun}
	if dryRun {
		for _, u := range uploads {
			res.Uploaded = append(res.Uploaded, u.key)
			if !c.json() {
				fmt.Fprintf(c.app.Stdout, "upload  %s -> th://%s/%s\n", u.file, loc.bucket, u.key)
			}
		}
		for _, k := range deletes {
			res.Deleted = append(res.Deleted, k)
			if !c.json() {
				fmt.Fprintf(c.app.Stdout, "delete  th://%s/%s\n", loc.bucket, k)
			}
		}
		if c.json() {
			return c.printJSON(res)
		}
		c.info("Dry run: %d to upload, %d to delete, %d unchanged.", len(uploads), len(deletes), unchanged)
		return nil
	}
	if len(deletes) > 0 {
		ok, err := c.confirmNo(fmt.Sprintf("Delete %d object(s) under th://%s/%s that are not in %s?", len(deletes), loc.bucket, prefix, dir))
		if err != nil {
			return err
		}
		if !ok {
			return &exitError{code: 1, msg: "Aborted."}
		}
	}
	for _, u := range uploads {
		c.info("Uploading %s to th://%s/%s", u.file, loc.bucket, u.key)
		if _, err := s.upload(loc.bucket, u.key, u.file, contentType); err != nil {
			return fmt.Errorf("%s: %w", u.file, err)
		}
		res.Uploaded = append(res.Uploaded, u.key)
	}
	for _, k := range deletes {
		if err := s.doJSON(http.MethodDelete, loc.bucket, objKeyPath(k), nil, nil, nil); err != nil {
			return fmt.Errorf("th://%s/%s: %w", loc.bucket, k, err)
		}
		c.info("Deleted th://%s/%s", loc.bucket, k)
		res.Deleted = append(res.Deleted, k)
	}
	if c.json() {
		return c.printJSON(res)
	}
	c.info("Uploaded %d, deleted %d, unchanged %d.", len(res.Uploaded), len(res.Deleted), unchanged)
	return nil
}

// ---- sign ----

func objParseExpiry(v string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, fmt.Errorf("--expires: %q is not a duration (e.g. 15m, 1h, 7d)", v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("--expires: %q is not a duration (e.g. 15m, 1h, 7d)", v)
	}
	return d, nil
}

func objSign(c *cmd, args []string) error {
	expires := "1h"
	method := "GET"
	fs := newFlagSet("object-storage sign", `Usage: th object-storage sign th://BUCKET/KEY [--expires 1h] [--method GET|PUT]

Prints a link that downloads (GET) or uploads (PUT) the object without a
key until it expires (at most 7d).`)
	fs.String(&expires, "expires", "How long the link works: 15m, 1h, 7d (default 1h)")
	fs.String(&method, "method", "GET (download, default) or PUT (upload)")
	s, err := c.objSession(fs, args)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th object-storage sign th://BUCKET/KEY [--expires 1h] [--method GET|PUT]")
	}
	loc, err := mustRemote(fs.Args()[0], "usage: th object-storage sign th://BUCKET/KEY")
	if err != nil {
		return err
	}
	if loc.key == "" {
		return fmt.Errorf("%s names a bucket, not an object", loc)
	}
	d, err := objParseExpiry(expires)
	if err != nil {
		return err
	}
	if d < time.Second || d > 7*24*time.Hour {
		return errors.New("--expires must be between 1s and 7d")
	}
	method = strings.ToUpper(method)
	if method != http.MethodGet && method != http.MethodPut {
		return errors.New("--method must be GET or PUT")
	}
	var out struct {
		URL       string `json:"url"`
		Method    string `json:"method"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := s.doJSON(http.MethodPost, loc.bucket, "/links", nil, map[string]any{"key": loc.key, "expiresIn": int64(d / time.Second), "method": method}, &out); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(out)
	}
	fmt.Fprintln(c.app.Stdout, out.URL)
	c.info("%s link, valid until %s", out.Method, shortTime(out.ExpiresAt))
	return nil
}
