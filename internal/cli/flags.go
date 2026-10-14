package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// flagSet is a small parser that, unlike package flag, lets flags and
// positional arguments mix in any order (th container deploy api --port 3000 and
// th container deploy --port 3000 api both work), and knows --no-<bool>.
type flagSet struct {
	name  string
	usage string
	defs  map[string]*flagDef
	order []string
	args  []string
	seen  map[string]bool
}

type flagKind int

const (
	kindString flagKind = iota
	kindBool
	kindInt
	kindList
)

type flagDef struct {
	name   string
	help   string
	kind   flagKind
	str    *string
	b      *bool
	i      *int
	list   *[]string
	global bool
}

func newFlagSet(name, usage string) *flagSet {
	return &flagSet{name: name, usage: usage, defs: map[string]*flagDef{}, seen: map[string]bool{}}
}

func (fs *flagSet) add(d *flagDef) {
	fs.defs[d.name] = d
	fs.order = append(fs.order, d.name)
}

func (fs *flagSet) String(p *string, name, help string) {
	fs.add(&flagDef{name: name, help: help, kind: kindString, str: p})
}

func (fs *flagSet) Bool(p *bool, name, help string) {
	fs.add(&flagDef{name: name, help: help, kind: kindBool, b: p})
}

func (fs *flagSet) Int(p *int, name, help string) {
	fs.add(&flagDef{name: name, help: help, kind: kindInt, i: p})
}

func (fs *flagSet) List(p *[]string, name, help string) {
	fs.add(&flagDef{name: name, help: help, kind: kindList, list: p})
}

// Set reports whether a flag was given on the command line.
func (fs *flagSet) Set(name string) bool { return fs.seen[name] }

// Args are the positional arguments.
func (fs *flagSet) Args() []string { return fs.args }

var errHelp = fmt.Errorf("help requested")

func (fs *flagSet) Parse(args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			fs.args = append(fs.args, args[i+1:]...)
			return nil
		}
		if len(a) < 2 || a[0] != '-' {
			fs.args = append(fs.args, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		value, hasValue := "", false
		if k, v, ok := strings.Cut(name, "="); ok {
			name, value, hasValue = k, v, true
		}
		if name == "h" || name == "help" {
			return errHelp
		}
		d, ok := fs.defs[name]
		negated := false
		if !ok && strings.HasPrefix(name, "no-") {
			if nd, ok2 := fs.defs[strings.TrimPrefix(name, "no-")]; ok2 && nd.kind == kindBool {
				d, ok, negated = nd, true, true
			}
		}
		if !ok {
			return fmt.Errorf("unknown flag: --%s (see: th %s --help)", name, fs.name)
		}
		if d.kind == kindBool {
			v := !negated
			if hasValue {
				bv, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("--%s takes true or false, not %q", name, value)
				}
				v = bv != negated
			}
			*d.b = v
			fs.seen[d.name] = true
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return fmt.Errorf("--%s needs a value", name)
			}
			i++
			value = args[i]
		}
		switch d.kind {
		case kindString:
			*d.str = value
		case kindInt:
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("--%s takes a whole number, not %q", name, value)
			}
			*d.i = n
		case kindList:
			*d.list = append(*d.list, value)
		}
		fs.seen[d.name] = true
	}
	return nil
}

// Help renders the usage text plus the command's own flags.
func (fs *flagSet) Help() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(fs.usage))
	b.WriteString("\n")
	var own, global []*flagDef
	for _, n := range fs.order {
		d := fs.defs[n]
		if d.global {
			global = append(global, d)
		} else {
			own = append(own, d)
		}
	}
	writeFlags := func(title string, defs []*flagDef) {
		if len(defs) == 0 {
			return
		}
		b.WriteString("\n" + title + ":\n")
		for _, d := range defs {
			left := "--" + d.name
			switch d.kind {
			case kindString, kindList:
				left += " VALUE"
			case kindInt:
				left += " N"
			}
			fmt.Fprintf(&b, "  %-28s %s\n", left, d.help)
		}
	}
	writeFlags("Flags", own)
	writeFlags("Global flags", global)
	return b.String()
}
