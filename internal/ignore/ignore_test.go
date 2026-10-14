package ignore

import (
	"strings"
	"testing"
)

func TestMatcher(t *testing.T) {
	m, err := Parse(strings.NewReader(`
# comment
*.log
!keep.log
build/
/root-only.txt
docs/*.md
**/cache
logs/**
a/**/z
secret?.env
[Tt]emp*
\#hash
dist
`), Options{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path  string
		dir   bool
		want  bool
		label string
	}{
		{"app.log", false, true, "glob at root"},
		{"x/y/app.log", false, true, "glob at depth"},
		{"keep.log", false, false, "negation"},
		{"x/keep.log", false, false, "negation at depth"},
		{"build", true, true, "dir-only on dir"},
		{"build", false, false, "dir-only on file"},
		{"src/build", true, true, "dir-only at depth"},
		{"src/build/out.js", false, true, "inside ignored dir"},
		{"root-only.txt", false, true, "anchored"},
		{"sub/root-only.txt", false, false, "anchored not at depth"},
		{"docs/a.md", false, true, "inner slash anchors"},
		{"docs/x/a.md", false, false, "* stops at /"},
		{"x/docs/a.md", false, false, "inner slash anchored"},
		{"cache", true, true, "**/ at root"},
		{"p/q/cache", true, true, "**/ at depth"},
		{"logs/a/b.txt", false, true, "trailing /**"},
		{"logs", true, false, "trailing /** not the dir itself"},
		{"a/z", true, true, "/**/ zero dirs"},
		{"a/b/c/z", true, true, "/**/ many dirs"},
		{"secret1.env", false, true, "?"},
		{"secret12.env", false, false, "? is one char"},
		{"Temp1", false, true, "class"},
		{"temp", false, true, "class lower"},
		{"#hash", false, true, "escaped #"},
		{"dist", true, true, "plain name dir"},
		{"src/main.go", false, false, "unmatched"},
	}
	for _, c := range cases {
		if got := m.Ignored(c.path, c.dir); got != c.want {
			t.Errorf("%s: Ignored(%q, %v) = %v, want %v", c.label, c.path, c.dir, got, c.want)
		}
	}
}

func TestLastMatchWins(t *testing.T) {
	m := New([]string{"*.txt", "!important.txt", "important.txt"}, Options{})
	if !m.Ignored("important.txt", false) {
		t.Fatal("last pattern should win")
	}
}

func TestNegationCannotReincludeInsideIgnoredDir(t *testing.T) {
	m := New([]string{"vendor/", "!vendor/keep.go"}, Options{})
	if !m.Ignored("vendor/keep.go", false) {
		t.Fatal("a file inside an ignored directory stays ignored")
	}
}

func TestAnchoredDockerignore(t *testing.T) {
	m := New([]string{"node_modules", "*.md", "!README.md"}, Options{Anchored: true})
	if !m.Ignored("node_modules", true) || m.Ignored("web/node_modules", true) {
		t.Fatal("dockerignore patterns are relative to the root")
	}
	if !m.Ignored("CHANGES.md", false) || m.Ignored("README.md", false) || m.Ignored("docs/x.md", false) {
		t.Fatal("anchored glob / negation")
	}
}

func TestNilMatcher(t *testing.T) {
	var m *Matcher
	if m.Ignored("x", false) || m.Len() != 0 {
		t.Fatal("nil matcher ignores nothing")
	}
}
