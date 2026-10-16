// Package cli implements the th command line.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
	"github.com/mrzack99s/thailandhosting-cli/internal/config"
)

// App holds everything a command touches outside itself, so tests can
// swap it.
type App struct {
	Version     string
	Stdout      io.Writer
	Stderr      io.Writer
	Stdin       io.Reader
	Getenv      func(string) string
	ConfigPath  string
	OpenBrowser func(url string) error
	// Sleep waits between device-login polls.
	Sleep func(time.Duration)
	// PollInterval is the build-log poll period (default 1 s).
	PollInterval time.Duration
	// Interrupt delivers Ctrl+C; nil means the process's own signal.
	Interrupt <-chan os.Signal
	// RunDockerLogin runs `docker login host -u th --password-stdin`.
	RunDockerLogin func(host, token string) error
}

// NewApp returns an App wired to the real process.
func NewApp(version string) *App {
	return &App{
		Version:        version,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		Stdin:          os.Stdin,
		Getenv:         os.Getenv,
		OpenBrowser:    openBrowser,
		Sleep:          time.Sleep,
		PollInterval:   time.Second,
		RunDockerLogin: dockerLogin,
	}
}

// exitError ends the program with a specific code.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

type globals struct {
	project  string
	region   string
	endpoint string
	format   string
	quiet    bool
}

type cmd struct {
	app  *App
	g    globals
	cfg  config.Config
	path string // config file
	ctx  context.Context
	// cmdPath is e.g. "container deploy", for messages.
	cmdPath string
	// capture, when set (th __docs), receives the command's flags instead
	// of the command running.
	capture func(*flagSet)
}

// Run executes args (without the program name) and returns the exit code.
func (a *App) Run(args []string) int {
	if a.Getenv == nil {
		a.Getenv = os.Getenv
	}
	if a.Sleep == nil {
		a.Sleep = time.Sleep
	}
	if a.PollInterval == 0 {
		a.PollInterval = time.Second
	}
	if a.Stdin == nil {
		a.Stdin = strings.NewReader("")
	}
	c := &cmd{app: a, ctx: context.Background()}
	err := c.dispatch(args)
	if err == nil {
		return 0
	}
	if errors.Is(err, errHelp) {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(a.Stderr, ee.msg)
		}
		return ee.code
	}
	fmt.Fprintf(a.Stderr, "ERROR: (th%s) %s\n", prefixSpace(c.cmdPath), err)
	return 1
}

func prefixSpace(s string) string {
	if s == "" {
		return ""
	}
	return "." + strings.ReplaceAll(s, " ", ".")
}

const rootUsage = `th — the ThailandHosting command line

Usage: th <group> <command> [flags]

Groups and commands:
  auth login | logout | status | whoami | print-access-token | configure-docker
  config set | get | unset | list
  projects list
  regions list
  container deploy NAME
  container services list | describe NAME | delete NAME | logs NAME | update-traffic NAME
  container revisions list --service NAME
  container images list [REPOSITORY] | delete IMAGE | delete-repository REPOSITORY
  builds list | log ID | cancel ID
%s  version

Global flags: --project, --region, --endpoint, --format json|text, --quiet

Run "th <group> <command> --help" for details.`

// groupUsage are the usage lines other files add for their groups
// (register), shown in the root usage in the order registered.
var groupUsage []string

// register adds a group's commands and usage lines from its own file, so
// each service lives in one file: register(map[string]handler{...}, "...").
func register(cmds map[string]handler, usage ...string) {
	for k, h := range cmds {
		if _, dup := commands[k]; dup {
			panic("th: command registered twice: " + k)
		}
		commands[k] = h
	}
	groupUsage = append(groupUsage, usage...)
}

func usageText() string {
	var b strings.Builder
	for _, l := range groupUsage {
		b.WriteString("  " + l + "\n")
	}
	return fmt.Sprintf(rootUsage, b.String())
}

type handler func(c *cmd, args []string) error

var commands = map[string]handler{
	"auth login":                         authLogin,
	"auth logout":                        authLogout,
	"auth status":                        authStatus,
	"auth whoami":                        authStatus,
	"auth print-access-token":            authPrintToken,
	"auth configure-docker":              authConfigureDocker,
	"whoami":                             authStatus,
	"config set":                         configSet,
	"config get":                         configGet,
	"config unset":                       configUnset,
	"config list":                        configList,
	"projects list":                      projectsList,
	"regions list":                       regionsList,
	"container deploy":                   runDeploy,
	"container services list":            servicesList,
	"container services describe":        servicesDescribe,
	"container services delete":          servicesDelete,
	"container services logs":            servicesLogs,
	"container services update-traffic":  servicesUpdateTraffic,
	"container revisions list":           revisionsList,
	"container images list":              imagesList,
	"container images delete":            imagesDelete,
	"container images delete-repository": imagesDeleteRepository,
	"builds list":                        buildsList,
	"builds log":                         buildsLog,
	"builds cancel":                      buildsCancel,
	"version":                            versionCmd,
}

func (c *cmd) dispatch(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 && args[0] == "help" {
			return c.dispatch(append(args[1:], "--help"))
		}
		fmt.Fprintln(c.app.Stdout, usageText())
		return nil
	}
	if args[0] == "--version" {
		return versionCmd(c, nil)
	}
	// Longest command path made of leading words.
	for n := 3; n >= 1; n-- {
		if len(args) < n {
			continue
		}
		key := strings.Join(args[:n], " ")
		if h, ok := commands[key]; ok {
			c.cmdPath = key
			return h(c, args[n:])
		}
	}
	// A group (or part of one): list its commands.
	var words []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || len(words) == 3 {
			break
		}
		words = append(words, a)
	}
	for n := len(words); n >= 1; n-- {
		prefix := strings.Join(words[:n], " ") + " "
		var group []string
		for k := range commands {
			if strings.HasPrefix(k, prefix) {
				group = append(group, "th "+k)
			}
		}
		if len(group) == 0 {
			continue
		}
		sortStrings(group)
		msg := "Commands:\n  " + strings.Join(group, "\n  ")
		if n < len(words) {
			return fmt.Errorf("unknown command %q\n%s", strings.Join(words, " "), msg)
		}
		fmt.Fprintln(c.app.Stdout, msg)
		return nil
	}
	return fmt.Errorf("unknown command %q. Run: th help", args[0])
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// flags makes a flag set with the global flags and parses args into it.
// It returns errHelp (after printing help) for --help.
func (c *cmd) parse(fs *flagSet, args []string) error {
	for _, d := range []*flagDef{
		{name: "project", help: "Project ID or slug (default: config / TH_PROJECT)", kind: kindString, str: &c.g.project},
		{name: "region", help: "Region ID (default: config / TH_REGION)", kind: kindString, str: &c.g.region},
		{name: "endpoint", help: "API endpoint (default: config / TH_ENDPOINT, else https://cloud.thailandhosting.com)", kind: kindString, str: &c.g.endpoint},
		{name: "format", help: "Output format: text or json", kind: kindString, str: &c.g.format},
		{name: "quiet", help: "Do not prompt; assume yes", kind: kindBool, b: &c.g.quiet},
	} {
		d.global = true
		if _, exists := fs.defs[d.name]; !exists {
			fs.add(d)
		}
	}
	if c.capture != nil {
		c.capture(fs)
		return errDocCaptured
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, errHelp) {
			fmt.Fprintln(c.app.Stdout, fs.Help())
		}
		return err
	}
	switch c.g.format {
	case "", "text", "json":
	default:
		return fmt.Errorf("--format must be text or json")
	}
	return c.loadConfig()
}

func (c *cmd) loadConfig() error {
	p := c.app.ConfigPath
	if p == "" {
		var err error
		if p, err = config.DefaultPath(); err != nil {
			return fmt.Errorf("cannot find the config directory: %w", err)
		}
	}
	c.path = p
	cfg, err := config.Load(p)
	if err != nil {
		return err
	}
	c.cfg = cfg
	return nil
}

func (c *cmd) saveConfig() error { return config.Save(c.path, c.cfg) }

func (c *cmd) json() bool { return c.g.format == "json" }

func (c *cmd) endpoint() string {
	for _, v := range []string{c.g.endpoint, c.app.Getenv("TH_ENDPOINT"), c.cfg.Endpoint} {
		if v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return config.DefaultEndpoint
}

// token returns the bearer token and whether it is an API key.
func (c *cmd) token() (string, bool) {
	if k := c.app.Getenv("TH_API_KEY"); k != "" {
		return k, true
	}
	if t := c.app.Getenv("TH_TOKEN"); t != "" {
		return t, strings.HasPrefix(t, "thk_")
	}
	return c.cfg.Token, false
}

func (c *cmd) projectSetting() string {
	for _, v := range []string{c.g.project, c.app.Getenv("TH_PROJECT"), c.cfg.Project} {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c *cmd) regionSetting() string {
	for _, v := range []string{c.g.region, c.app.Getenv("TH_REGION"), c.cfg.Region} {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c *cmd) userAgent() string {
	return fmt.Sprintf("th/%s (%s/%s)", c.app.Version, runtime.GOOS, runtime.GOARCH)
}

// client returns an API client; signedIn requires a token.
func (c *cmd) client(signedIn bool) (*api.Client, error) {
	tok, isKey := c.token()
	if signedIn && tok == "" {
		return nil, errors.New("Not signed in. Run: th auth login (or set TH_API_KEY)")
	}
	cl := api.New(c.endpoint(), tok, c.userAgent())
	cl.APIKey = isKey
	return cl, nil
}

// out prints a progress/info line to stderr (stdout stays clean for
// --format json and scripts).
func (c *cmd) info(format string, a ...any) {
	fmt.Fprintf(c.app.Stderr, format+"\n", a...)
}

func (c *cmd) printJSON(v any) error {
	if raw, ok := v.([]byte); ok {
		var x any
		if err := json.Unmarshal(raw, &x); err == nil {
			v = x
		}
	}
	enc := json.NewEncoder(c.app.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (c *cmd) table(header []string, rows [][]string) {
	tw := tabwriter.NewWriter(c.app.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// confirm asks a yes/no question; --quiet answers yes.
func (c *cmd) confirm(question string) (bool, error) {
	if c.g.quiet {
		return true, nil
	}
	fmt.Fprintf(c.app.Stderr, "%s\n\nDo you want to continue (Y/n)?  ", question)
	line, err := bufio.NewReader(c.app.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintln(c.app.Stderr)
		return false, errors.New("no answer on standard input; pass --quiet to skip the prompt")
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "" || ans == "y" || ans == "yes", nil
}

// interrupts returns a channel for Ctrl+C and a stop function.
func (c *cmd) interrupts() (<-chan os.Signal, func()) {
	if c.app.Interrupt != nil {
		return c.app.Interrupt, func() {}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	return ch, func() { signal.Stop(ch) }
}

func q(v string) string { return url.QueryEscape(v) }

func versionCmd(c *cmd, args []string) error {
	fs := newFlagSet("version", "Usage: th version\n\nPrints the CLI version.")
	if args != nil {
		if err := c.parse(fs, args); err != nil {
			return err
		}
	}
	if c.json() {
		return c.printJSON(map[string]string{"version": c.app.Version, "os": runtime.GOOS, "arch": runtime.GOARCH})
	}
	fmt.Fprintf(c.app.Stdout, "th %s (%s/%s)\n", c.app.Version, runtime.GOOS, runtime.GOARCH)
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return dash(s)
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
