// Package ignore matches paths against .gitignore-style pattern files
// (.thignore, .dockerignore, .gitignore).
//
// Supported syntax: blank lines and # comments, globs (* ? [abc] [!a-z]),
// ** across directories (leading "**/", trailing "/**", inner "/**/"),
// a trailing / for directories only, a leading or inner / anchoring the
// pattern to the root, negation with !, and \ escapes. The last matching
// pattern wins, and a path inside an ignored directory stays ignored.
package ignore

import (
	"bufio"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// Matcher holds compiled patterns. The zero value ignores nothing.
type Matcher struct {
	rules []rule
}

// Options change how patterns are read.
type Options struct {
	// Anchored treats every pattern as relative to the root, as Docker does
	// for .dockerignore ("node_modules" only matches ./node_modules).
	Anchored bool
}

// New compiles the given pattern lines.
func New(lines []string, opt Options) *Matcher {
	m := &Matcher{}
	for _, line := range lines {
		if r, ok := compile(line, opt); ok {
			m.rules = append(m.rules, r)
		}
	}
	return m
}

// Parse reads pattern lines from r.
func Parse(r io.Reader, opt Options) (*Matcher, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return New(lines, opt), nil
}

// ParseFile reads a pattern file.
func ParseFile(name string, opt Options) (*Matcher, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f, opt)
}

// Len is the number of usable patterns.
func (m *Matcher) Len() int {
	if m == nil {
		return 0
	}
	return len(m.rules)
}

// Match reports whether rel (a slash-separated path relative to the root)
// is ignored by its own patterns alone, without looking at its parents.
func (m *Matcher) Match(rel string, isDir bool) bool {
	if m == nil {
		return false
	}
	rel = strings.Trim(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/")
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(rel) {
			ignored = !r.negate
		}
	}
	return ignored
}

// Ignored reports whether rel is ignored, either itself or because one of
// its parent directories is.
func (m *Matcher) Ignored(rel string, isDir bool) bool {
	if m == nil {
		return false
	}
	rel = strings.Trim(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/")
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if m.Match(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return m.Match(rel, isDir)
}

func compile(line string, opt Options) (rule, bool) {
	line = strings.TrimRight(line, "\r")
	// Trailing spaces are dropped unless escaped.
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, "\\ ") {
		line = line[:len(line)-1]
	}
	if line == "" || strings.HasPrefix(line, "#") {
		return rule{}, false
	}
	var r rule
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	} else if strings.HasPrefix(line, "\\!") || strings.HasPrefix(line, "\\#") {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") && !strings.HasSuffix(line, "\\/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	// Docker allows a leading "./".
	line = strings.TrimPrefix(line, "./")
	if line == "" || line == "/" {
		return rule{}, false
	}
	anchored := opt.Anchored || strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")
	if line == "" {
		return rule{}, false
	}
	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	if !writeGlob(&b, line) {
		return rule{}, false
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return rule{}, false
	}
	r.re = re
	return r, true
}

func writeGlob(b *strings.Builder, p string) bool {
	for i := 0; i < len(p); {
		rest := p[i:]
		switch {
		case i == 0 && strings.HasPrefix(rest, "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
			continue
		case rest == "**" && (i == 0 || p[i-1] == '/'):
			b.WriteString(".*")
			i += 2
			continue
		case rest == "/**":
			b.WriteString("/.*")
			i += 3
			continue
		case strings.HasPrefix(rest, "/**/"):
			b.WriteString("/(?:.*/)?")
			i += 4
			continue
		}
		c := p[i]
		switch c {
		case '*':
			b.WriteString("[^/]*")
			for i < len(p) && p[i] == '*' {
				i++
			}
			continue
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(p[i+1:], ']')
			if end < 0 {
				b.WriteString(regexp.QuoteMeta("["))
				break
			}
			class := p[i+1 : i+1+end]
			if end == 0 { // "[]...]" — a literal ] first
				end2 := strings.IndexByte(p[i+2:], ']')
				if end2 < 0 {
					return false
				}
				class = p[i+1 : i+2+end2]
				end = end2 + 1
			}
			b.WriteString("[")
			if strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^") {
				b.WriteString("^/")
				class = class[1:]
			}
			b.WriteString(strings.ReplaceAll(strings.ReplaceAll(class, "\\", "\\\\"), "[", "\\["))
			b.WriteString("]")
			i += end + 2
			continue
		case '\\':
			if i+1 < len(p) {
				i++
				b.WriteString(regexp.QuoteMeta(string(p[i])))
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
		i++
	}
	return true
}
