// Command release cross-compiles th and writes release archives:
//
//	go run ./scripts/release -version 0.1.0
//
// It writes dist/th_<version>_<os>_<arch>.tar.gz (.zip for Windows) and
// dist/SHA256SUMS.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var targets = []struct{ os, arch string }{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

func main() {
	version := flag.String("version", "", "version to stamp, e.g. 0.1.0")
	out := flag.String("out", "dist", "output directory")
	flag.Parse()
	if *version == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/release -version X.Y.Z")
		os.Exit(2)
	}
	v := strings.TrimPrefix(*version, "v")
	if err := os.RemoveAll(*out); err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	extra := []string{"README.md"}
	sums := map[string]string{}
	for _, t := range targets {
		bin := "th"
		if t.os == "windows" {
			bin = "th.exe"
		}
		work, err := os.MkdirTemp("", "th-release-*")
		if err != nil {
			fail(err)
		}
		binPath := filepath.Join(work, bin)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+v, "-o", binPath, "./cmd/th")
		cmd.Env = append(os.Environ(), "GOOS="+t.os, "GOARCH="+t.arch, "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		fmt.Printf("building %s/%s\n", t.os, t.arch)
		if err := cmd.Run(); err != nil {
			fail(err)
		}
		files := map[string]string{bin: binPath}
		for _, e := range extra {
			if _, err := os.Stat(e); err == nil {
				files[e] = e
			}
		}
		name := fmt.Sprintf("th_%s_%s_%s", v, t.os, t.arch)
		var archive string
		if t.os == "windows" {
			archive = filepath.Join(*out, name+".zip")
			err = writeZip(archive, files)
		} else {
			archive = filepath.Join(*out, name+".tar.gz")
			err = writeTarGz(archive, files)
		}
		os.RemoveAll(work)
		if err != nil {
			fail(err)
		}
		sum, err := sha256File(archive)
		if err != nil {
			fail(err)
		}
		sums[filepath.Base(archive)] = sum
	}
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	if err := os.WriteFile(filepath.Join(*out, "SHA256SUMS"), []byte(b.String()), 0o644); err != nil {
		fail(err)
	}
	fmt.Print(b.String())
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func writeTarGz(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, name := range sortedKeys(files) {
		src := files[name]
		st, err := os.Stat(src)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if name == "th" {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: st.Size(), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if err := copyFile(tw, src); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, name := range sortedKeys(files) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if err := copyFile(w, files[name]); err != nil {
			return err
		}
	}
	return zw.Close()
}

func copyFile(w io.Writer, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "release:", err)
	os.Exit(1)
}
