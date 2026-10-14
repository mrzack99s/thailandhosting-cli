package pack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func write(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

type entry struct {
	mode int64
	body string
	typ  byte
}

func read(t *testing.T, b []byte) map[string]entry {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]entry{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = entry{mode: h.Mode, body: string(body), typ: h.Typeflag}
	}
	return out
}

func TestPackHonoursIgnoreAndKeepsPaths(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Dockerfile", "FROM scratch\n", 0o644)
	write(t, root, "main.go", "package main\n", 0o644)
	write(t, root, "run.sh", "#!/bin/sh\n", 0o755)
	write(t, root, "src/lib/util.go", "package lib\n", 0o644)
	write(t, root, "debug.log", "x", 0o644)
	write(t, root, "src/trace.log", "x", 0o644)
	write(t, root, "build/out.bin", "x", 0o644)
	write(t, root, ".git/config", "x", 0o644)
	write(t, root, "node_modules/a/index.js", "x", 0o644)
	write(t, root, "web/node_modules/b.js", "x", 0o644)
	write(t, root, "py/__pycache__/m.pyc", "x", 0o644)
	write(t, root, ".venv/bin/python", "x", 0o644)
	write(t, root, ".DS_Store", "x", 0o644)
	write(t, root, ".gitignore", "*.log\nbuild/\n", 0o644)
	write(t, root, ".thignore", "*.log\nbuild/\nDockerfile\n", 0o644)

	var buf bytes.Buffer
	res, err := Pack(root, &buf, Options{Keep: []string{"Dockerfile"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IgnoreFile != ".thignore" {
		t.Fatalf("ignore file = %q, want .thignore (it wins over .gitignore)", res.IgnoreFile)
	}
	got := read(t, buf.Bytes())
	var files []string
	for name, e := range got {
		if e.typ == tar.TypeReg {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	want := []string{".gitignore", ".thignore", "Dockerfile", "main.go", "run.sh", "src/lib/util.go"}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("files = %v, want %v", files, want)
		}
	}
	if res.Files != len(want) {
		t.Fatalf("Files = %d", res.Files)
	}
	if _, ok := got["src/"]; !ok {
		t.Fatal("directory entries use forward slashes and a trailing /")
	}
	if got["src/lib/util.go"].body != "package lib\n" {
		t.Fatal("content")
	}
	if m := got["run.sh"].mode; m != 0o755 {
		t.Fatalf("run.sh mode = %o, want 755", m)
	}
	if m := got["main.go"].mode; m != 0o644 {
		t.Fatalf("main.go mode = %o, want 644", m)
	}
	if res.Bytes != int64(buf.Len()) {
		t.Fatalf("Bytes = %d, buffer = %d", res.Bytes, buf.Len())
	}
}

func TestPackUnixModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no execute bit on Windows")
	}
	root := t.TempDir()
	write(t, root, "bin/tool", "x", 0o750)
	var buf bytes.Buffer
	if _, err := Pack(root, &buf, Options{}); err != nil {
		t.Fatal(err)
	}
	if m := read(t, buf.Bytes())["bin/tool"].mode; m != 0o750 {
		t.Fatalf("mode = %o, want 750", m)
	}
}

func TestPackTooLarge(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, 256<<10)
	rand.Read(big) // incompressible
	write(t, root, "blob.bin", string(big), 0o644)
	_, err := Pack(root, io.Discard, Options{Limit: 64 << 10})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestPackDockerignoreIsAnchored(t *testing.T) {
	root := t.TempDir()
	write(t, root, "tmp/a", "x", 0o644)
	write(t, root, "src/tmp/b", "x", 0o644)
	write(t, root, ".dockerignore", "tmp\n", 0o644)
	var buf bytes.Buffer
	if _, err := Pack(root, &buf, Options{}); err != nil {
		t.Fatal(err)
	}
	got := read(t, buf.Bytes())
	if _, ok := got["tmp/a"]; ok {
		t.Fatal("tmp/a should be ignored")
	}
	if _, ok := got["src/tmp/b"]; !ok {
		t.Fatal("src/tmp/b should stay: .dockerignore patterns are anchored")
	}
}
