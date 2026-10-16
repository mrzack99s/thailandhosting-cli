package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
	"github.com/mrzack99s/thailandhosting-cli/internal/pack"
)

const deployUsage = `Usage: th container deploy NAME [--source DIR | --image IMAGE] [flags]

Deploys a Container. With --source (the default, "."), the directory is
uploaded and built on ThailandHosting — no Docker needed locally — from
its Dockerfile, or a generated one for Node.js, Go or Python. Files listed
in .thignore (else .dockerignore, else .gitignore) are not uploaded.

A new service is created when NAME does not exist yet (it needs a region);
otherwise a new revision of it is deployed and receives all traffic.
Flags you leave out keep the service's current settings.

A source build stores its image in the Project's default registry, as
<host>/<project>/NAME; --repository REGISTRY/IMAGE stores it in another
registry of the Project instead (see th registry list).

Examples:
  th container deploy api --source . --region bkk
  th container deploy api --source . --repository prod/api --tag v1.4.0
  th container deploy api --image ghcr.io/acme/api:1.4 --port 3000
  th container deploy api --cpu 0.5 --memory 512Mi --env NODE_ENV=production`

func runDeploy(c *cmd, args []string) error {
	var (
		source, image, cpu, memory, computeClass, timeout, dockerfile, tag, message, healthPath, repository string
		port, minInst, maxInst, concurrency                                                                 int
		envs, setEnv, secretEnv, command, cmdArgs                                                           []string
		allowUnauth, cpuAlways, async, clearCommand, clearArgs                                              bool
	)
	fs := newFlagSet("container deploy", deployUsage)
	fs.String(&source, "source", "Directory to upload and build (default .)")
	fs.String(&image, "image", "Deploy this image instead of building")
	fs.Int(&port, "port", "Port the container listens on (default 8080)")
	fs.String(&cpu, "cpu", "vCPU, e.g. 0.25, 0.5, 1, 2")
	fs.String(&memory, "memory", "Memory, e.g. 256Mi, 512Mi, 1Gi")
	fs.String(&computeClass, "compute-class", "Compute class, e.g. general, economy, highcpu, highmem")
	fs.List(&envs, "env", "Environment variable KEY=VALUE (repeatable)")
	fs.List(&setEnv, "set-env-vars", "Environment variables KEY=VALUE,KEY2=VALUE2")
	fs.List(&secretEnv, "secret-env", "Secret environment variable KEY=VALUE (repeatable)")
	fs.Int(&minInst, "min-instances", "Minimum instances (0 scales to zero)")
	fs.Int(&maxInst, "max-instances", "Maximum instances")
	fs.Int(&concurrency, "concurrency", "Requests per instance at once")
	fs.String(&timeout, "timeout", "Request timeout, e.g. 300s or 5m")
	fs.Bool(&allowUnauth, "allow-unauthenticated", "Allow public calls (--no-allow-unauthenticated needs an invoker token)")
	fs.Bool(&cpuAlways, "cpu-always-allocated", "Keep CPU allocated between requests")
	fs.String(&healthPath, "health-check-path", "HTTP path for health checks")
	fs.List(&command, "command", "Entrypoint, comma-separated (repeatable)")
	fs.List(&cmdArgs, "args", "Arguments, comma-separated (repeatable)")
	fs.Bool(&clearCommand, "clear-command", "Use the image's own entrypoint")
	fs.Bool(&clearArgs, "clear-args", "Use the image's own arguments")
	fs.String(&dockerfile, "dockerfile", "Dockerfile path inside the source (default Dockerfile)")
	fs.String(&tag, "tag", "Image tag for a source build")
	fs.String(&repository, "repository", "Where a source build stores its image: IMAGE or REGISTRY/IMAGE (default NAME)")
	fs.String(&message, "message", "Note stored with the revision")
	fs.Bool(&async, "async", "Return once the deploy is accepted (only with --image)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th container deploy NAME [--source DIR | --image IMAGE]")
	}
	name := fs.Args()[0]
	if err := validateName(name); err != nil {
		return err
	}
	if source != "" && image != "" {
		return errors.New("use either --source or --image, not both")
	}
	if image == "" && source == "" {
		source = "."
	}
	if repository != "" && image != "" {
		return errors.New("--repository is for a source build; with --image the image is deployed as it is")
	}
	if repository == "" {
		repository = name
	} else if err := validateRepository(repository); err != nil {
		return err
	}
	if async && image == "" {
		return errors.New("--async needs --image: a source deploy waits for its build. (Ctrl+C during the build leaves it running on the server.)")
	}

	var o deployOpts
	o.Message = message
	if fs.Set("port") {
		o.Port = &port
	}
	if fs.Set("cpu") {
		v, err := parseCPU(cpu)
		if err != nil {
			return err
		}
		o.CPUQuota = &v
	}
	if fs.Set("memory") {
		v, err := parseMemory(memory)
		if err != nil {
			return err
		}
		o.MemoryMB = &v
	}
	if fs.Set("compute-class") {
		o.ComputeClass = &computeClass
	}
	if fs.Set("command") || clearCommand {
		v := splitList(command)
		if clearCommand {
			v = []string{}
		}
		o.Command = &v
	}
	if fs.Set("args") || clearArgs {
		v := splitList(cmdArgs)
		if clearArgs {
			v = []string{}
		}
		o.Args = &v
	}
	env, secretKeys, err := buildEnv(envs, setEnv, secretEnv)
	if err != nil {
		return err
	}
	o.Env, o.SecretKeys = env, secretKeys
	if fs.Set("min-instances") {
		o.MinReplicas = &minInst
	}
	if fs.Set("max-instances") {
		o.MaxReplicas = &maxInst
	}
	if fs.Set("concurrency") {
		o.Concurrency = &concurrency
	}
	if fs.Set("timeout") {
		v, err := parseTimeout(timeout)
		if err != nil {
			return err
		}
		o.TimeoutSeconds = &v
	}
	if fs.Set("cpu-always-allocated") {
		o.CPUAlways = &cpuAlways
	}
	if fs.Set("health-check-path") {
		o.HealthPath = &healthPath
	}
	if fs.Set("allow-unauthenticated") {
		o.AllowUnauth = &allowUnauth
	}

	cl, err := c.client(true)
	if err != nil {
		return err
	}
	projectID, projectName, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	existing, err := c.findService(cl, projectID, name)
	if err != nil {
		return err
	}
	if existing != nil && isFunction(*existing) {
		return fmt.Errorf("[%s] is a function; deploy its code with: th functions deploy %s", name, name)
	}
	var regionID string
	if existing != nil {
		regionID = existing.RegionID
	} else if regionID, err = c.resolveRegion(cl, true); err != nil {
		return err
	}
	c.info("Deploying container to ThailandHosting service [%s] in project [%s] region [%s]", name, projectName, regionID)

	var build *api.Build
	if image == "" {
		cls := ""
		if o.ComputeClass != nil {
			cls = *o.ComputeClass
		} else if existing != nil {
			cls = existing.ComputeClass
		}
		build, err = c.sourceBuild(cl, sourceBuildInput{dir: source, projectID: projectID, repository: repository, tag: tag, dockerfile: dockerfile, regionID: regionID, computeClass: cls})
		if err != nil {
			return err
		}
		image = build.Image
	}
	o.Image = image

	var svc api.Service
	var revName string
	if existing != nil {
		var detail api.ServiceDetail
		if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+existing.ID, nil, &detail); err != nil {
			return err
		}
		cur := detail.Service
		if cur.ID == "" {
			cur = *existing
		}
		spec := mergeSpec(cur, o)
		if o.Env != nil {
			c.info("Note: environment flags replace the service's whole set of variables; variables not given now are removed.")
		}
		c.info("Creating revision…")
		var res api.DeployResult
		if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+existing.ID+"/revisions", spec, &res); err != nil {
			return err
		}
		svc, revName = res.Service, res.Revision.Name
		if svc.ID == "" {
			svc = cur
		}
		if o.AllowUnauth != nil && *o.AllowUnauth != cur.AllowUnauthenticated {
			if err := c.setAccess(cl, &svc, *o.AllowUnauth); err != nil {
				c.info("Warning: the revision is deployed, but changing public access failed: %v", err)
			}
		}
	} else {
		c.info("Creating service…")
		in := createInput(name, projectID, regionID, o)
		if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services", in, &svc); err != nil {
			return err
		}
		revName = c.servingRevisionName(cl, svc)
	}
	if svc.Slug == "" {
		svc.Slug = name
	}

	if c.json() {
		out := map[string]any{"service": svc, "revision": revName}
		if build != nil {
			out["build"] = build
		}
		return c.printJSON(out)
	}
	if revName != "" {
		fmt.Fprintf(c.app.Stdout, "Service [%s] revision [%s] has been deployed and is serving 100 percent of traffic.\n", name, revName)
	} else {
		fmt.Fprintf(c.app.Stdout, "Service [%s] has been deployed and is serving 100 percent of traffic.\n", name)
	}
	if svc.URL != "" {
		fmt.Fprintf(c.app.Stdout, "Service URL: %s\n", svc.URL)
	}
	if svc.InvokerToken != "" {
		fmt.Fprintf(c.app.Stdout, "Invoker token (shown once; send it as Authorization: Bearer): %s\n", svc.InvokerToken)
	}
	return nil
}

func (c *cmd) servingRevisionName(cl *api.Client, svc api.Service) string {
	if svc.ID == "" {
		return ""
	}
	var revs []api.Revision
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID+"/revisions", nil, &revs); err != nil || len(revs) == 0 {
		return ""
	}
	for _, r := range revs {
		if r.ID == svc.ServingRevisionID {
			return r.Name
		}
	}
	return revs[0].Name
}

func (c *cmd) setAccess(cl *api.Client, svc *api.Service, allow bool) error {
	body := map[string]any{"ingress": svc.Ingress, "allowUnauthenticated": allow}
	var res struct {
		Service      api.Service `json:"service"`
		InvokerToken string      `json:"invokerToken"`
	}
	if _, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/services/"+svc.ID+"/access", body, &res); err != nil {
		return err
	}
	if res.InvokerToken != "" {
		svc.InvokerToken = res.InvokerToken
	}
	svc.AllowUnauthenticated = allow
	return nil
}

var (
	repoNameRE     = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	registryNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
)

// validateRepository checks --repository: IMAGE (the default registry) or
// REGISTRY/IMAGE.
func validateRepository(s string) error {
	reg, image, named := strings.Cut(s, "/")
	if !named {
		image = reg
	}
	if strings.Contains(image, "/") || !repoNameRE.MatchString(image) || len(image) > 63 ||
		(named && (!registryNameRE.MatchString(reg) || reg == "default")) {
		return fmt.Errorf("--repository is IMAGE or REGISTRY/IMAGE of lowercase letters, digits and . _ - (e.g. api or prod/api), not %q", s)
	}
	return nil
}

type sourceBuildInput struct {
	dir, projectID, repository, tag, dockerfile, regionID, computeClass string
}

// sourceBuild packs, uploads and builds the source, streaming the build log.
func (c *cmd) sourceBuild(cl *api.Client, in sourceBuildInput) (*api.Build, error) {
	abs, err := filepath.Abs(in.dir)
	if err != nil {
		return nil, err
	}
	dockerfile := in.dockerfile
	keep := []string{}
	if dockerfile != "" {
		keep = append(keep, filepath.ToSlash(dockerfile))
	} else {
		keep = append(keep, "Dockerfile")
	}
	tmp, err := os.CreateTemp("", "th-source-*.tar.gz")
	if err != nil {
		return nil, err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	c.info("Packing sources from %s…", abs)
	res, err := pack.Pack(abs, tmp, pack.Options{Keep: keep})
	if errors.Is(err, pack.ErrTooLarge) {
		return nil, fmt.Errorf("the packed source is larger than 100 MiB. Leave build outputs, datasets and media out with a .thignore file (same syntax as .gitignore), or build the image yourself and use --image")
	}
	if err != nil {
		return nil, fmt.Errorf("packing %s: %w", abs, err)
	}
	if res.IgnoreFile != "" {
		c.info("Skipping files listed in %s.", res.IgnoreFile)
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return nil, err
	}
	fmt.Fprintf(c.app.Stderr, "Uploading sources (%d files, %s)…", res.Files, humanBytes(res.Bytes))
	var src api.Source
	if _, err := cl.UploadStream(c.ctx, "/api/v1/run/sources?projectId="+q(in.projectID), "application/gzip", tmp, res.Bytes, &src); err != nil {
		fmt.Fprintln(c.app.Stderr, " failed")
		return nil, err
	}
	fmt.Fprintln(c.app.Stderr, " done")

	req := api.BuildRequest{ProjectID: in.projectID, SourceID: src.SourceID, Repository: in.repository, Tag: in.tag, Dockerfile: in.dockerfile, RegionID: in.regionID, ComputeClass: in.computeClass}
	var b api.Build
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/run/builds", req, &b); err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == http.StatusConflict && strings.Contains(strings.ToLower(err.Error()), "registry") {
			return nil, fmt.Errorf("%s\nFree space: th container images list", err)
		}
		return nil, err
	}
	c.info("Building… (logs below; build %s)", b.ID)
	status, err := c.followBuildLog(cl, b.ID, true, c.app.Stderr)
	if err != nil {
		return nil, err
	}
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/run/builds/"+b.ID, nil, &b); err != nil {
		return nil, err
	}
	if b.Status == "" {
		b.Status = status
	}
	if b.Status != "succeeded" {
		msg := fmt.Sprintf("build %s %s", b.ID, dash(b.Status))
		if b.Error != "" {
			msg += ": " + b.Error
		}
		return nil, errors.New(msg)
	}
	if b.Image == "" {
		return nil, fmt.Errorf("build %s succeeded but returned no image", b.ID)
	}
	c.info("Build succeeded: %s", b.Image)
	return &b, nil
}

// followBuildLog prints the build log from offset 0. With follow it polls
// until the build is done; Ctrl+C leaves the build running and exits 130.
// Without follow it prints what exists now and returns.
func (c *cmd) followBuildLog(cl *api.Client, id string, follow bool, w io.Writer) (string, error) {
	sig, stop := c.interrupts()
	defer stop()
	var offset int64
	for {
		var l api.BuildLog
		if _, err := cl.Do(c.ctx, http.MethodGet, fmt.Sprintf("/api/v1/run/builds/%s/log?offset=%d", id, offset), nil, &l); err != nil {
			return "", err
		}
		if l.Text != "" {
			fmt.Fprint(w, l.Text)
		}
		advanced := l.Offset > offset
		if l.Offset > 0 {
			offset = l.Offset
		}
		if l.Done {
			return l.Status, nil
		}
		if !follow {
			if !advanced {
				return l.Status, nil
			}
			continue
		}
		select {
		case <-sig:
			return "", &exitError{code: 130, msg: "\nBuild continues on the server: th builds log " + id}
		case <-time.After(c.app.PollInterval):
		}
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
