// Package pack turns a source directory into the tar.gz that
// POST /api/v1/run/sources takes.
package pack

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mrzack99s/thailandhosting-cli/internal/ignore"
)

// MaxPackedBytes is the largest upload the server accepts.
const MaxPackedBytes = 100 << 20

// AlwaysSkipped are names never uploaded, at any depth.
var AlwaysSkipped = map[string]bool{
	".git":         true,
	"node_modules": true,
	".venv":        true,
	"__pycache__":  true,
	".DS_Store":    true,
}

// IgnoreFiles are tried in order; the first one present is used.
var IgnoreFiles = []string{".thignore", ".dockerignore", ".gitignore"}

// ErrTooLarge is returned when the archive grows past MaxPackedBytes.
var ErrTooLarge = errors.New("source archive is larger than 100 MiB")

// Options control Pack.
type Options struct {
	// Keep are relative paths that are always included even when an ignore
	// file matches them (the Dockerfile the build needs).
	Keep []string
	// Limit overrides MaxPackedBytes (tests).
	Limit int64
}

// Result describes a packed archive.
type Result struct {
	Files      int
	Bytes      int64 // compressed size
	IgnoreFile string
}

// LoadIgnore returns the matcher from the first ignore file found in dir.
func LoadIgnore(dir string) (*ignore.Matcher, string, error) {
	for _, name := range IgnoreFiles {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		m, err := ignore.ParseFile(p, ignore.Options{Anchored: name == ".dockerignore"})
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", name, err)
		}
		return m, name, nil
	}
	return nil, "", nil
}

// Pack writes dir as a gzip-compressed tar to w. Paths inside the archive
// are relative to dir and use forward slashes.
func Pack(dir string, w io.Writer, opt Options) (Result, error) {
	limit := opt.Limit
	if limit <= 0 {
		limit = MaxPackedBytes
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("%s is not a directory", dir)
	}
	m, ignoreFile, err := LoadIgnore(dir)
	if err != nil {
		return Result{}, err
	}
	keep := map[string]bool{}
	for _, k := range opt.Keep {
		keep[path.Clean(filepath.ToSlash(k))] = true
	}
	cw := &countingWriter{w: w, limit: limit}
	gz := gzip.NewWriter(cw)
	tw := tar.NewWriter(gz)
	res := Result{IgnoreFile: ignoreFile}

	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
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
		if AlwaysSkipped[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !keep[rel] && m.Ignored(rel, d.IsDir()) {
			if d.IsDir() && !keepsInside(keep, rel) {
				return filepath.SkipDir
			}
			return nil // walk on, but only kept files get in
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
			link = filepath.ToSlash(link)
		case fi.Mode().IsRegular(), fi.IsDir():
		default:
			return nil // sockets, devices, pipes
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return err
		}
		hdr.Name = rel
		if fi.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		hdr.Format = tar.FormatPAX
		hdr.Mode = int64(fileMode(fi))
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
			}
			res.Files++
		}
		return nil
	})
	if walkErr != nil {
		return res, walkErr
	}
	if err := tw.Close(); err != nil {
		return res, err
	}
	if err := gz.Close(); err != nil {
		return res, err
	}
	res.Bytes = cw.n
	return res, nil
}

func keepsInside(keep map[string]bool, dir string) bool {
	for k := range keep {
		if strings.HasPrefix(k, dir+"/") {
			return true
		}
	}
	return false
}

// fileMode keeps Unix permission bits. Windows has no execute bit, so files
// get 0644 (0755 when they look like scripts) and directories 0755.
func fileMode(fi fs.FileInfo) fs.FileMode {
	if fi.IsDir() {
		if runtime.GOOS == "windows" {
			return 0o755
		}
		return fi.Mode().Perm()
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return 0o777
	}
	if runtime.GOOS == "windows" {
		if strings.HasSuffix(fi.Name(), ".sh") {
			return 0o755
		}
		return 0o644
	}
	return fi.Mode().Perm()
}

type countingWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.n+int64(len(p)) > c.limit {
		return 0, ErrTooLarge
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
