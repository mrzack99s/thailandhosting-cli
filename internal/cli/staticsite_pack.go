package cli

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/ignore"
)

// Limits of POST /api/v1/sites/{id}/deploy.
const (
	ssMaxCompressed = 100 << 20
	ssMaxFiles      = 20000
	ssMaxBytes      = 500 << 20
)

// ssSkipped are never uploaded, at any depth. A built output folder is
// uploaded as it is: .gitignore is NOT read (dist/ is often gitignored),
// only .thignore.
var ssSkipped = map[string]bool{".git": true, ".DS_Store": true}

var errSSTooLarge = errors.New("the packed folder is larger than 100 MB compressed")

type ssPackResult struct {
	Files      int
	Bytes      int64 // uncompressed file bytes
	Compressed int64
	Ignored    bool // .thignore was applied
}

// ssPack writes dir's files as a tar.gz to w, paths relative to dir with
// forward slashes. Symlinked files are uploaded as their content; symlinked
// directories, devices and the like are skipped.
func ssPack(dir string, w io.Writer) (ssPackResult, error) {
	var res ssPackResult
	var m *ignore.Matcher
	if _, err := os.Stat(filepath.Join(dir, ".thignore")); err == nil {
		var perr error
		if m, perr = ignore.ParseFile(filepath.Join(dir, ".thignore"), ignore.Options{}); perr != nil {
			return res, fmt.Errorf("read .thignore: %w", perr)
		}
		res.Ignored = true
	}
	cw := &ssLimitWriter{w: w, limit: ssMaxCompressed}
	gz := gzip.NewWriter(cw)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ssSkipped[d.Name()] || rel == ".thignore" || (m != nil && m.Ignored(rel, d.IsDir())) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		fi, err := os.Stat(p) // follows a symlink
		if err != nil || !fi.Mode().IsRegular() {
			return nil
		}
		res.Files++
		res.Bytes += fi.Size()
		if res.Files > ssMaxFiles {
			return fmt.Errorf("the folder has more than %d files (the most one deploy takes); leave some out with a .thignore file", ssMaxFiles)
		}
		if res.Bytes > ssMaxBytes {
			return fmt.Errorf("the folder is larger than %d MB (the most one release holds); leave files out with a .thignore file", ssMaxBytes>>20)
		}
		hdr := &tar.Header{Name: rel, Mode: 0o644, Size: fi.Size(), ModTime: fi.ModTime().Truncate(time.Second), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		n, err := io.Copy(tw, f)
		f.Close()
		if err != nil {
			return err
		}
		if n != fi.Size() {
			return fmt.Errorf("%s changed while it was being packed", rel)
		}
		return nil
	})
	if err == nil {
		if err = tw.Close(); err == nil {
			err = gz.Close()
		}
	}
	res.Compressed = cw.n
	return res, err
}

type ssLimitWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

func (c *ssLimitWriter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.limit {
		return 0, errSSTooLarge
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// ssIndexHint finds the built output when dir itself has no index.html.
func ssIndexHint(dir string) (hasIndex bool, candidates []string) {
	if ssFileExists(filepath.Join(dir, "index.html")) {
		return true, nil
	}
	for _, sub := range []string{"dist", "build", "out", "public", "_site"} {
		if ssFileExists(filepath.Join(dir, sub, "index.html")) {
			candidates = append(candidates, sub)
		}
	}
	return false, candidates
}

func ssFileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
