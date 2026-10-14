package cli

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// th __docs prints every command, its usage text and its flags as JSON, so
// the reference on the ThailandHosting docs site is generated from the CLI
// itself and cannot drift from it. It is not listed in th help.

var errDocCaptured = errors.New("flags captured")

type docFlag struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // string | bool | int | list
	Default string `json:"default,omitempty"`
	Help    string `json:"help"`
	Global  bool   `json:"global,omitempty"`
}

type docCommand struct {
	Command string    `json:"command"`
	Usage   string    `json:"usage"`
	Flags   []docFlag `json:"flags"`
}

func init() {
	register(map[string]handler{"__docs": docsCmd})
}

func docsCmd(c *cmd, _ []string) error {
	names := make([]string, 0, len(commands))
	for k := range commands {
		if !strings.HasPrefix(k, "__") && k != "version" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	out := []docCommand{}
	for _, name := range names {
		sub := &cmd{app: c.app, ctx: c.ctx, cmdPath: name}
		var captured *flagSet
		sub.capture = func(fs *flagSet) { captured = fs }
		_ = commands[name](sub, nil)
		d := docCommand{Command: name, Flags: []docFlag{}}
		if captured != nil {
			d.Usage = captured.usage
			for _, fname := range captured.order {
				f := captured.defs[fname]
				df := docFlag{Name: f.name, Help: f.help, Global: f.global}
				switch f.kind {
				case kindString:
					df.Type, df.Default = "string", *f.str
				case kindBool:
					df.Type = "bool"
					if *f.b {
						df.Default = "true"
					}
				case kindInt:
					df.Type = "int"
					if *f.i != 0 {
						df.Default = itoa(*f.i)
					}
				case kindList:
					df.Type, df.Default = "list", strings.Join(*f.list, ",")
				}
				d.Flags = append(d.Flags, df)
			}
		}
		out = append(out, d)
	}
	out = append(out, docCommand{Command: "version", Usage: "Usage: th version\n\nPrints the version of th.", Flags: []docFlag{}})
	enc := json.NewEncoder(c.app.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
