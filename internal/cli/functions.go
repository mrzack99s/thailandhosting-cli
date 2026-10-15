package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
	"github.com/mrzack99s/thailandhosting-cli/internal/pack"
)

// th functions: Functions are Containers whose image the platform builds
// from a handful of source files (Python, Node.js/TypeScript or Go) and our
// own small runtime. The files travel as JSON (/api/v1/services/{id}/function),
// the same code the Console's editor shows, so a function can be edited in
// either place: th functions pull fetches what the Console saved.

func init() {
	register(map[string]handler{
		"functions list":     functionsList,
		"functions describe": functionsDescribe,
		"functions deploy":   functionsDeploy,
		"functions pull":     functionsPull,
		"functions call":     functionsCall,
		"functions logs":     functionsLogs,
		"functions delete":   functionsDelete,
		"functions runtimes": functionsRuntimes,
		"functions versions list":     functionsVersionsList,
		"functions versions activate": functionsVersionsActivate,
	},
		"functions list | describe NAME | deploy NAME [--source DIR] | pull NAME [--dir DIR]",
		"functions call NAME [--data BODY] | logs NAME | delete NAME | runtimes",
		"functions versions list NAME | activate NAME VERSION",
	)
}

// Limits the server enforces, checked here first for clearer errors.
const (
	fnMaxFiles      = 100
	fnMaxFileBytes  = 512 << 10
	fnMaxTotalBytes = 2 << 20
)

var fnPathRE = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

type fnRuntime struct {
	ID          string `json:"id"`
	Family      string `json:"family"`
	Version     string `json:"version"`
	Recommended bool   `json:"recommended"`
	Deprecated  bool   `json:"deprecated"`
	Name        string `json:"name"`
	Language   string `json:"language"`
	MainFile   string `json:"mainFile"`
	EntryPoint string `json:"entryPoint"`
	Hint       string `json:"hint"`
}

type fnBuild struct {
	ID              string  `json:"id"`
	Number          int     `json:"number"`
	Status          string  `json:"status"`
	Error           string  `json:"error"`
	CreatedAt       string  `json:"createdAt"`
	DurationSeconds float64 `json:"durationSeconds"`
}

type fnSource struct {
	ServiceID       string            `json:"serviceId"`
	Runtime         string            `json:"runtime"`
	EntryPoint      string            `json:"entryPoint"`
	Files           map[string]string `json:"files"`
	BuildID         string            `json:"buildId"`
	DeployedBuildID string            `json:"deployedBuildId"`
	DeployState     string            `json:"deployState"`
	DeployError     string            `json:"deployError"`
	DeployedAt      string            `json:"deployedAt"`
	UpdatedBy       string            `json:"updatedBy"`
	UpdatedAt       string            `json:"updatedAt"`
	Build           *fnBuild          `json:"build"`
}

func isFunction(s api.Service) bool { return s.Kind == "container" && s.Engine == "function" }

func (c *cmd) listFunctions(cl *api.Client, pid string) ([]api.Service, error) {
	all, err := c.listContainers(cl, pid)
	if err != nil {
		return nil, err
	}
	var out []api.Service
	for _, s := range all {
		if isFunction(s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// mustFunction resolves a function of the active Project by slug or name.
func (c *cmd) mustFunction(name string) (*api.Client, *api.Service, error) {
	cl, svc, err := c.mustService(name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, nil, fmt.Errorf("function [%s] not found. List them with: th functions list", name)
		}
		return nil, nil, err
	}
	if !isFunction(*svc) {
		return nil, nil, fmt.Errorf("[%s] is a Container, not a function. Use th container commands for it", name)
	}
	return cl, svc, nil
}

func (c *cmd) runtimes(cl *api.Client) ([]fnRuntime, error) {
	var rts []fnRuntime
	_, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/functions/runtimes", nil, &rts)
	return rts, err
}

func functionsRuntimes(c *cmd, args []string) error {
	fs := newFlagSet("functions runtimes", "Usage: th functions runtimes\n\nLists the runtimes a function can use and the handler each expects.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	rts, err := c.runtimes(cl)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(rts)
	}
	rows := [][]string{}
	hints := map[string]string{}
	var families []string
	for _, r := range rts {
		note := ""
		switch {
		case r.Recommended:
			note = "recommended"
		case r.Deprecated:
			note = "existing functions only"
		}
		rows = append(rows, []string{r.ID, r.Name, r.MainFile, r.EntryPoint, dash(note)})
		if _, seen := hints[r.Family]; !seen {
			families = append(families, r.Family)
			hints[r.Family] = r.Hint
		}
	}
	c.table([]string{"RUNTIME", "NAME", "MAIN FILE", "ENTRY POINT", "NOTE"}, rows)
	for _, f := range families {
		fmt.Fprintf(c.app.Stdout, "\n%s: %s", f, hints[f])
	}
	fmt.Fprintln(c.app.Stdout)
	return nil
}

func functionsList(c *cmd, args []string) error {
	fs := newFlagSet("functions list", "Usage: th functions list\n\nLists the functions of the active Project.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	fns, err := c.listFunctions(cl, pid)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(fns)
	}
	rows := [][]string{}
	for _, s := range fns {
		rows = append(rows, []string{s.Slug, s.RegionID, s.Status, dash(s.URL)})
	}
	c.table([]string{"FUNCTION", "REGION", "STATUS", "URL"}, rows)
	return nil
}

func (c *cmd) functionSource(cl *api.Client, id string) (fnSource, []byte, error) {
	var src fnSource
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+id+"/function", nil, &src)
	return src, raw, err
}

func functionsDescribe(c *cmd, args []string) error {
	fs := newFlagSet("functions describe", "Usage: th functions describe NAME\n\nShows a function's runtime, deploy state, settings and files.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th functions describe NAME")
	}
	cl, svc, err := c.mustFunction(fs.Args()[0])
	if err != nil {
		return err
	}
	src, _, err := c.functionSource(cl, svc.ID)
	if err != nil {
		return err
	}
	var d api.ServiceDetail
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID, nil, &d); err != nil {
		return err
	}
	if c.json() {
		files := make([]string, 0, len(src.Files))
		for f := range src.Files {
			files = append(files, f)
		}
		sort.Strings(files)
		return c.printJSON(map[string]any{"service": d.Service, "runtime": src.Runtime, "entryPoint": src.EntryPoint, "deployState": src.DeployState,
			"deployError": src.DeployError, "deployedAt": src.DeployedAt, "build": src.Build, "files": files, "env": d.Envs})
	}
	s := d.Service
	w := c.app.Stdout
	fmt.Fprintf(w, "Function %s in region %s\n\n", s.Slug, s.RegionID)
	fmt.Fprintf(w, "URL:          %s\n", dash(s.URL))
	fmt.Fprintf(w, "Runtime:      %s\n", src.Runtime)
	fmt.Fprintf(w, "Entry point:  %s\n", src.EntryPoint)
	fmt.Fprintf(w, "Deploy:       %s\n", deployStateText(src))
	if src.DeployedAt != "" {
		fmt.Fprintf(w, "Deployed at:  %s\n", shortTime(src.DeployedAt))
	}
	fmt.Fprintf(w, "Status:       %s\n", dash(s.Status))
	fmt.Fprintf(w, "CPU / memory: %s vCPU / %d MiB\n", vcpu(s.CPUQuota), s.MemoryMB)
	fmt.Fprintf(w, "Instances:    %d–%d\n", s.MinReplicas, s.MaxReplicas)
	fmt.Fprintf(w, "Timeout:      %ds\n", s.RequestTimeoutSeconds)
	names := make([]string, 0, len(src.Files))
	for f := range src.Files {
		names = append(names, f)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "Files:")
	for _, f := range names {
		fmt.Fprintf(w, "  %s (%s)\n", f, humanBytes(int64(len(src.Files[f]))))
	}
	if len(d.Envs) > 0 {
		fmt.Fprintln(w, "Env:")
		for _, e := range d.Envs {
			v := e.Value
			if e.IsSecret {
				v = "(secret)"
			}
			fmt.Fprintf(w, "  %s=%s\n", e.Key, v)
		}
	}
	return nil
}

func deployStateText(src fnSource) string {
	build := ""
	if src.Build != nil {
		build = fmt.Sprintf(" (build #%d)", src.Build.Number)
	}
	switch src.DeployState {
	case "deployed":
		return "deployed" + build
	case "failed":
		return "failed" + build + ": " + src.DeployError
	case "building", "deploying":
		return src.DeployState + build
	}
	return "not deployed yet"
}

// readFunctionDir reads the files of a function's source folder, leaving
// out what .thignore / .dockerignore / .gitignore and the usual tool
// folders exclude, and checks them against the server's limits.
func readFunctionDir(dir string) (map[string]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	m, _, err := pack.LoadIgnore(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	total := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if pack.AlwaysSkipped[d.Name()] || d.Name() == "dist" && d.IsDir() || m.Ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		// The platform writes its own; the build ignores the customer's.
		if rel == "Dockerfile" || strings.HasPrefix(rel, "Dockerfile.") || strings.HasPrefix(d.Name(), "_thfunction_runtime") || strings.HasPrefix(rel, "thfunctionmain/") {
			return nil
		}
		if !fnPathRE.MatchString(rel) || len(rel) > 200 {
			return fmt.Errorf("file name %q cannot be deployed: use letters, digits, . _ - and / (or list it in .thignore)", rel)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(b) > fnMaxFileBytes || bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
			return fmt.Errorf("%s must be a text file of at most 512 KiB (list it in .thignore to leave it out)", rel)
		}
		total += len(b)
		files[rel] = string(b)
		if len(files) > fnMaxFiles {
			return fmt.Errorf("a function has at most %d files; list the rest in .thignore", fnMaxFiles)
		}
		if total > fnMaxTotalBytes {
			return errors.New("a function's files add up to at most 2 MiB; list large files in .thignore")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files to deploy in %s", dir)
	}
	return files, nil
}

// detectFamily guesses the language of a new function from its files.
func detectFamily(files map[string]string) string {
	switch {
	case files["go.mod"] != "" || hasExt(files, ".go"):
		return "go"
	case files["package.json"] != "" || files["index.ts"] != "" || files["index.js"] != "" || files["index.mjs"] != "":
		return "nodejs"
	case files["main.py"] != "" || files["requirements.txt"] != "":
		return "python"
	}
	return ""
}

// pickRuntime turns --runtime (a runtime such as python313, or a language:
// python, nodejs, node, go) or the files' language into a runtime ID; a
// language means its recommended version.
func pickRuntime(rts []fnRuntime, flag string, files map[string]string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(flag))
	if want == "node" {
		want = "nodejs"
	}
	for _, r := range rts {
		if r.ID == want {
			if r.Deprecated {
				return "", fmt.Errorf("runtime %s is for existing functions only; see th functions runtimes", r.ID)
			}
			return r.ID, nil
		}
	}
	family := want
	if family == "" {
		family = detectFamily(files)
	}
	if family == "" {
		return "", errors.New("cannot tell the language from the files: pass --runtime (see th functions runtimes)")
	}
	for _, r := range rts {
		if r.Family == family && r.Recommended {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("unknown runtime %q; see th functions runtimes", flag)
}

func hasExt(files map[string]string, ext string) bool {
	for f := range files {
		if strings.HasSuffix(f, ext) && !strings.Contains(f, "/") {
			return true
		}
	}
	return false
}

const functionsDeployUsage = `Usage: th functions deploy NAME [--source DIR] [flags]

Uploads the files of DIR (default .) as the function's code, builds them
and serves the result. A new function is created first; its runtime comes
from --runtime or from the files (go.mod: Go, package.json / index.ts:
Node.js, main.py / requirements.txt: Python).

Files listed in .thignore (or .dockerignore / .gitignore), .git,
node_modules, .venv, __pycache__ and dist are left out. A Dockerfile is
ignored: functions always build with the platform's runtime.

--runtime takes a runtime (python313, nodejs24, go126) or a language
(python, nodejs, go: its recommended version). An existing function may
move to another version of its language.

Every deploy is kept as a version (th functions versions list); --message
describes it. --env, --set-env-vars and --secret-env replace every variable
of the function; without them the current variables stay. Settings given
for an existing function (--cpu, --memory, --min-instances, ...) are applied
before its code is built.

Examples:
  th functions deploy hello
  th functions deploy hello --runtime python314 --message "Python 3.14"
  th functions deploy orders --source ./orders --memory 512Mi --max-instances 10`

func functionsDeploy(c *cmd, args []string) error {
	var (
		source, runtimeID, entry, cpu, memory, computeClass, timeout, message string
		minInst, maxInst, concurrency                                         int
		envs, setEnv, secretEnv                                               []string
		allowUnauth, async, cpuAlways                                         bool
	)
	fs := newFlagSet("functions deploy", functionsDeployUsage)
	fs.String(&source, "source", "Directory with the function's files (default .)")
	fs.String(&runtimeID, "runtime", "Runtime (e.g. python313, nodejs24, go126) or language (python, nodejs, go)")
	fs.String(&entry, "entry-point", "Handler name (default: the runtime's, e.g. main, handler, Handle)")
	fs.String(&cpu, "cpu", "vCPU, e.g. 0.25, 0.5, 1 (a size of the Container menu)")
	fs.String(&memory, "memory", "Memory, e.g. 256Mi, 512Mi, 1Gi")
	fs.String(&computeClass, "compute-class", "Compute class of a new function")
	fs.List(&envs, "env", "Environment variable KEY=VALUE (repeatable)")
	fs.List(&setEnv, "set-env-vars", "Environment variables KEY=VALUE,KEY2=VALUE2")
	fs.List(&secretEnv, "secret-env", "Secret environment variable KEY=VALUE (repeatable)")
	fs.Int(&minInst, "min-instances", "Minimum instances (0 stops it when idle)")
	fs.Int(&maxInst, "max-instances", "Maximum instances")
	fs.String(&timeout, "timeout", "Request timeout, e.g. 60s or 5m")
	fs.Int(&concurrency, "concurrency", "Requests at once per instance (1-1000)")
	fs.Bool(&cpuAlways, "cpu-always-allocated", "Keep CPU between requests")
	fs.Bool(&allowUnauth, "allow-unauthenticated", "Allow public calls (default for a new function; --no-allow-unauthenticated needs an invoker token)")
	fs.String(&message, "message", "Describe this version")
	fs.Bool(&async, "async", "Return once the build is queued")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th functions deploy NAME [--source DIR]")
	}
	name := fs.Args()[0]
	if err := validateName(name); err != nil {
		return errors.New(strings.Replace(err.Error(), "service name", "function name", 1))
	}
	if source == "" {
		source = "."
	}
	files, err := readFunctionDir(source)
	if err != nil {
		return err
	}
	env, secretKeys, err := buildEnv(envs, setEnv, secretEnv)
	if err != nil {
		return err
	}
	settings := map[string]any{}
	if env != nil {
		settings["env"], settings["secretEnvKeys"] = env, secretKeys
	}
	if fs.Set("min-instances") {
		settings["minInstances"] = minInst
	}
	if fs.Set("max-instances") {
		settings["maxInstances"] = maxInst
	}
	var timeoutSeconds int
	if fs.Set("timeout") {
		if timeoutSeconds, err = parseTimeout(timeout); err != nil {
			return err
		}
		settings["requestTimeoutSeconds"] = timeoutSeconds
	}
	if fs.Set("concurrency") {
		settings["concurrency"] = concurrency
	}
	if fs.Set("cpu-always-allocated") {
		settings["cpuAlwaysAllocated"] = cpuAlways
	}
	var cpuQuota int64
	var memoryMB int
	if fs.Set("cpu") {
		if cpuQuota, err = parseCPU(cpu); err != nil {
			return err
		}
	}
	if fs.Set("memory") {
		if memoryMB, err = parseMemory(memory); err != nil {
			return err
		}
	}

	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, projectSlug, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	svc, err := c.findService(cl, pid, name)
	if err != nil {
		return err
	}
	if svc != nil && !isFunction(*svc) {
		return fmt.Errorf("[%s] is a Container, not a function. Pick another name", name)
	}
	rts, err := c.runtimes(cl)
	if err != nil {
		return err
	}
	if svc == nil {
		if runtimeID, err = pickRuntime(rts, runtimeID, files); err != nil {
			return err
		}
		region, err := c.resolveRegion(cl, false)
		if err != nil {
			return err
		}
		in := map[string]any{"kind": "container", "engine": "function", "functionRuntime": runtimeID, "name": name, "slug": name,
			"projectId": pid, "minReplicas": 0, "maxReplicas": 3, "allowUnauthenticated": !fs.Set("allow-unauthenticated") || allowUnauth}
		if region != "" {
			in["regionId"] = region
		}
		if computeClass != "" {
			in["computeClass"] = computeClass
		}
		if cpuQuota > 0 {
			in["cpuQuota"] = cpuQuota
		}
		if memoryMB > 0 {
			in["memoryMB"] = memoryMB
		}
		if fs.Set("concurrency") {
			in["containerConcurrency"] = concurrency
		}
		if fs.Set("cpu-always-allocated") {
			in["cpuAlwaysAllocated"] = cpuAlways
		}
		if fs.Set("min-instances") {
			in["minReplicas"] = minInst
		}
		if fs.Set("max-instances") {
			in["maxReplicas"] = maxInst
		}
		if timeoutSeconds > 0 {
			in["requestTimeoutSeconds"] = timeoutSeconds
		}
		if env != nil {
			in["env"], in["secretEnvKeys"] = env, secretKeys
		}
		c.info("Creating function [%s] (%s) in project [%s]…", name, runtimeID, projectSlug)
		var created api.Service
		if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services", in, &created); err != nil {
			return err
		}
		svc = &created
		settings = nil // given at creation
		if created.InvokerToken != "" {
			c.info("Invoker token (shown once): %s", created.InvokerToken)
		}
	} else {
		if fs.Set("runtime") {
			if runtimeID, err = pickRuntime(rts, runtimeID, nil); err != nil {
				return err
			}
		}
		if fs.Set("compute-class") {
			return errors.New("--compute-class applies to a new function only")
		}
		if cpuQuota > 0 || memoryMB > 0 {
			// The server takes a whole size: fill in what was not given.
			var d api.ServiceDetail
			if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID, nil, &d); err != nil {
				return err
			}
			if cpuQuota == 0 {
				cpuQuota = d.Service.CPUQuota
			}
			if memoryMB == 0 {
				memoryMB = d.Service.MemoryMB
			}
			settings["cpuQuota"], settings["memoryMB"], settings["cpuBurst"] = cpuQuota, memoryMB, 0
		}
		if fs.Set("allow-unauthenticated") && allowUnauth != svc.AllowUnauthenticated {
			if err := c.setAccess(cl, svc, allowUnauth); err != nil {
				return err
			}
		}
		if len(settings) > 0 {
			c.info("Updating settings of [%s]…", name)
			if _, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/services/"+svc.ID+"/function/settings", settings, nil); err != nil {
				return err
			}
		}
	}

	body := map[string]any{"files": files, "message": message}
	if entry != "" {
		body["entryPoint"] = entry
	}
	if fs.Set("runtime") {
		body["runtime"] = runtimeID
	}
	c.info("Uploading %d files of %s…", len(files), source)
	var src fnSource
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+svc.ID+"/function/deploy", body, &src); err != nil {
		return err
	}
	if src.Build == nil {
		return errors.New("the server did not start a build")
	}
	if async {
		if c.json() {
			return c.printJSON(map[string]any{"function": name, "build": src.Build, "url": svc.URL})
		}
		fmt.Fprintf(c.app.Stdout, "Build #%d queued for function [%s]. Follow it with: th builds log %s\n", src.Build.Number, name, src.Build.ID)
		return nil
	}
	c.info("Building (build #%d)…", src.Build.Number)
	status, err := c.followBuildLog(cl, src.Build.ID, true, c.app.Stderr)
	if err != nil {
		return err
	}
	if status != "succeeded" {
		return &exitError{code: 1, msg: fmt.Sprintf("ERROR: (th.functions.deploy) build #%d %s. See: th builds log %s", src.Build.Number, status, src.Build.ID)}
	}
	// The build's success is served right away; wait for the revision.
	deadline := time.Now().Add(3 * time.Minute)
	for {
		cur, _, err := c.functionSource(cl, svc.ID)
		if err != nil {
			return err
		}
		if cur.BuildID != src.Build.ID {
			return fmt.Errorf("a newer deploy of [%s] replaced this one", name)
		}
		if cur.DeployState == "deployed" {
			break
		}
		if cur.DeployState == "failed" {
			return &exitError{code: 1, msg: "ERROR: (th.functions.deploy) " + cur.DeployError}
		}
		if time.Now().After(deadline) {
			return errors.New("the build succeeded but the new revision is not serving yet; check: th functions describe " + name)
		}
		c.app.Sleep(c.app.PollInterval)
	}
	var d api.ServiceDetail
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID, nil, &d); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"function": name, "url": d.Service.URL, "build": src.Build})
	}
	fmt.Fprintf(c.app.Stdout, "Function [%s] is deployed.\nURL: %s\n", name, dash(d.Service.URL))
	return nil
}

func functionsPull(c *cmd, args []string) error {
	var dir, version string
	var force bool
	fs := newFlagSet("functions pull", "Usage: th functions pull NAME [--dir DIR] [--version N] [--force]\n\nWrites the function's saved code (what the Console editor shows), or the\ncode of one version, into DIR (default ./NAME). An existing file with other\ncontent is kept unless --force.")
	fs.String(&dir, "dir", "Directory to write into (default ./NAME)")
	fs.String(&version, "version", "A version from th functions versions list (default: the saved code)")
	fs.Bool(&force, "force", "Overwrite files that differ")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th functions pull NAME [--dir DIR]")
	}
	name := fs.Args()[0]
	cl, svc, err := c.mustFunction(name)
	if err != nil {
		return err
	}
	src, _, err := c.functionSource(cl, svc.ID)
	if err != nil {
		return err
	}
	if version != "" {
		n, err := versionNumber(version)
		if err != nil {
			return err
		}
		var v fnVersion
		if _, err := cl.Do(c.ctx, http.MethodGet, fmt.Sprintf("/api/v1/services/%s/function/versions/%d", svc.ID, n), nil, &v); err != nil {
			return err
		}
		src.Files, src.Runtime, src.EntryPoint = v.Files, v.Runtime, v.EntryPoint
	}
	if dir == "" {
		dir = name
	}
	names := make([]string, 0, len(src.Files))
	for f := range src.Files {
		names = append(names, f)
	}
	sort.Strings(names)
	var conflicts []string
	for _, f := range names {
		clean := path.Clean(f)
		if !fnPathRE.MatchString(f) || clean != f || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("the server sent an unsafe file name %q", f)
		}
		if old, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f))); err == nil && string(old) != src.Files[f] && !force {
			conflicts = append(conflicts, f)
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("these files in %s differ from the function's: %s (pass --force to overwrite)", dir, strings.Join(conflicts, ", "))
	}
	for _, f := range names {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(src.Files[f]), 0o644); err != nil {
			return err
		}
	}
	if c.json() {
		return c.printJSON(map[string]any{"function": name, "dir": dir, "files": names, "runtime": src.Runtime, "entryPoint": src.EntryPoint})
	}
	fmt.Fprintf(c.app.Stdout, "Wrote %d files of [%s] (%s, entry point %s) to %s\n", len(names), name, src.Runtime, src.EntryPoint, dir)
	return nil
}

func functionsCall(c *cmd, args []string) error {
	var data, method, urlPath, token string
	fs := newFlagSet("functions call", "Usage: th functions call NAME [--data BODY|@FILE] [--method POST] [--path /x] [--token TOKEN]\n\nSends one request to the function and prints the answer. With --data the\nmethod defaults to POST, otherwise GET. --token is the invoker token of a\nfunction that does not allow unauthenticated calls.")
	fs.String(&data, "data", "Request body, or @FILE to read it from a file")
	fs.String(&method, "method", "HTTP method")
	fs.String(&urlPath, "path", "Path to call (default /)")
	fs.String(&token, "token", "Invoker token")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th functions call NAME [--data BODY]")
	}
	cl, svc, err := c.mustFunction(fs.Args()[0])
	if err != nil {
		return err
	}
	if svc.URL == "" {
		var d api.ServiceDetail
		if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID, nil, &d); err != nil {
			return err
		}
		svc.URL = d.Service.URL
	}
	if svc.URL == "" {
		return errors.New("the function has no public URL")
	}
	var body io.Reader
	if strings.HasPrefix(data, "@") {
		b, err := os.ReadFile(data[1:])
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	} else if data != "" {
		body = strings.NewReader(data)
	}
	if method == "" {
		method = http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
	}
	if urlPath == "" {
		urlPath = "/"
	}
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	req, err := http.NewRequestWithContext(c.ctx, strings.ToUpper(method), strings.TrimRight(svc.URL, "/")+urlPath, body)
	if err != nil {
		return err
	}
	if body != nil {
		ct := "text/plain; charset=utf-8"
		if t := strings.TrimSpace(data); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") || strings.HasSuffix(data, ".json") {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("X-Serverless-Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", c.userAgent())
	client := &http.Client{Timeout: 10 * time.Minute}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, 10<<20))
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"status": res.StatusCode, "headers": res.Header, "body": string(out)})
	}
	c.info("HTTP %d", res.StatusCode)
	c.app.Stdout.Write(out)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		fmt.Fprintln(c.app.Stdout)
	}
	if res.StatusCode >= 400 {
		return &exitError{code: 1}
	}
	return nil
}

func functionsLogs(c *cmd, args []string) error {
	lines := 100
	fs := newFlagSet("functions logs", "Usage: th functions logs NAME [--lines N]\n\nPrints the latest log lines of a function (what the handler printed).")
	fs.Int(&lines, "lines", "Number of lines (default 100)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th functions logs NAME [--lines N]")
	}
	cl, svc, err := c.mustFunction(fs.Args()[0])
	if err != nil {
		return err
	}
	return c.printServiceLogs(cl, svc, lines)
}

func functionsDelete(c *cmd, args []string) error {
	fs := newFlagSet("functions delete", "Usage: th functions delete NAME [--quiet]\n\nDeletes a function with its code, triggers and revisions. --quiet skips the prompt.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th functions delete NAME")
	}
	name := fs.Args()[0]
	cl, svc, err := c.mustFunction(name)
	if err != nil {
		return err
	}
	ok, err := c.confirm(fmt.Sprintf("Function [%s] and its code will be deleted.", name))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/services/"+svc.ID, nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]string{"function": name, "status": "deleted"})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted function [%s].\n", name)
	return nil
}

// fnVersion is one deploy of a function (/function/versions).
type fnVersion struct {
	Number     int               `json:"number"`
	Runtime    string            `json:"runtime"`
	EntryPoint string            `json:"entryPoint"`
	FileCount  int               `json:"fileCount"`
	SizeBytes  int64             `json:"sizeBytes"`
	Message    string            `json:"message"`
	Status     string            `json:"status"`
	Error      string            `json:"error"`
	CreatedBy  string            `json:"createdBy"`
	CreatedAt  string            `json:"createdAt"`
	Active     bool              `json:"active"`
	Build      *fnBuild          `json:"build"`
	Files      map[string]string `json:"files"`
}

// versionNumber reads 12 or v12.
func versionNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "v"))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid version %q: use a number from th functions versions list, e.g. 12 or v12", s)
	}
	return n, nil
}

func functionsVersionsList(c *cmd, args []string) error {
	fs := newFlagSet("functions versions list", "Usage: th functions versions list NAME\n\nLists a function's versions, newest first. Every deploy is a version; the\nserving one is marked. The latest 30 are kept.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th functions versions list NAME")
	}
	cl, svc, err := c.mustFunction(fs.Args()[0])
	if err != nil {
		return err
	}
	var vs []fnVersion
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID+"/function/versions", nil, &vs)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, v := range vs {
		state := v.Status
		if v.Active {
			state = "serving"
		}
		msg := v.Message
		if v.Status == "failed" && v.Error != "" {
			msg = strings.TrimSpace(msg + " (" + v.Error + ")")
		}
		if len(msg) > 60 {
			msg = msg[:57] + "..."
		}
		rows = append(rows, []string{fmt.Sprintf("v%d", v.Number), state, v.Runtime, shortTime(v.CreatedAt), dash(v.CreatedBy), dash(msg)})
	}
	c.table([]string{"VERSION", "STATE", "RUNTIME", "CREATED", "BY", "MESSAGE"}, rows)
	return nil
}

func functionsVersionsActivate(c *cmd, args []string) error {
	fs := newFlagSet("functions versions activate", "Usage: th functions versions activate NAME VERSION\n\nServes an earlier version again; today's settings and variables stay. When\nthe registry no longer holds its image, its code is built again as a new\nversion and served once the build succeeds.\n\nExamples:\n  th functions versions activate hello 12\n  th functions versions activate hello v12")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th functions versions activate NAME VERSION")
	}
	name := fs.Args()[0]
	n, err := versionNumber(fs.Args()[1])
	if err != nil {
		return err
	}
	cl, svc, err := c.mustFunction(name)
	if err != nil {
		return err
	}
	var res struct {
		Activated  bool     `json:"activated"`
		Rebuilding bool     `json:"rebuilding"`
		Build      *fnBuild `json:"build"`
	}
	if _, err := cl.Do(c.ctx, http.MethodPost, fmt.Sprintf("/api/v1/services/%s/function/versions/%d/activate", svc.ID, n), map[string]any{}, &res); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"function": name, "version": n, "activated": res.Activated, "rebuilding": res.Rebuilding, "build": res.Build})
	}
	if res.Rebuilding && res.Build != nil {
		fmt.Fprintf(c.app.Stdout, "The image of v%d was cleaned up; building its code again (build #%d). Follow it with: th builds log %s\n", n, res.Build.Number, res.Build.ID)
		return nil
	}
	fmt.Fprintf(c.app.Stdout, "Function [%s] now serves v%d.\n", name, n)
	return nil
}
