package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

func servicesList(c *cmd, args []string) error {
	fs := newFlagSet("container services list", "Usage: th container services list\n\nLists the Containers of the active Project.")
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
	all, err := c.listContainers(cl, pid)
	if err != nil {
		return err
	}
	svcs := []api.Service{}
	for _, s := range all {
		if !isFunction(s) { // th functions list
			svcs = append(svcs, s)
		}
	}
	if c.json() {
		return c.printJSON(svcs)
	}
	rows := [][]string{}
	for _, s := range svcs {
		rows = append(rows, []string{s.Slug, s.RegionID, s.Status, dash(s.URL)})
	}
	c.table([]string{"SERVICE", "REGION", "STATUS", "URL"}, rows)
	return nil
}

func servicesDescribe(c *cmd, args []string) error {
	fs := newFlagSet("container services describe", "Usage: th container services describe NAME\n\nShows a Container's settings and serving revision.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th container services describe NAME")
	}
	cl, found, err := c.mustService(fs.Args()[0])
	if err != nil {
		return err
	}
	var d api.ServiceDetail
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+found.ID, nil, &d)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	s := d.Service
	w := c.app.Stdout
	fmt.Fprintf(w, "Service %s in region %s\n\n", s.Slug, s.RegionID)
	fmt.Fprintf(w, "URL:            %s\n", dash(s.URL))
	fmt.Fprintf(w, "Status:         %s\n", dash(s.Status))
	if d.ServingRevision != nil {
		fmt.Fprintf(w, "Revision:       %s (serving 100%%)\n", d.ServingRevision.Name)
	}
	fmt.Fprintf(w, "Image:          %s\n", dash(s.Image))
	fmt.Fprintf(w, "Port:           %d\n", s.Port)
	fmt.Fprintf(w, "CPU:            %s vCPU", vcpu(s.CPUQuota))
	if s.CPUBurst > 0 {
		fmt.Fprintf(w, " (bursts to %s)", vcpu(s.CPUBurst))
	}
	fmt.Fprintf(w, "\nMemory:         %d MiB\n", s.MemoryMB)
	fmt.Fprintf(w, "Compute class:  %s\n", dash(s.ComputeClass))
	fmt.Fprintf(w, "Instances:      %d–%d\n", s.MinReplicas, s.MaxReplicas)
	fmt.Fprintf(w, "Concurrency:    %d\n", s.ContainerConcurrency)
	fmt.Fprintf(w, "Timeout:        %ds\n", s.RequestTimeoutSeconds)
	public := "no (invoker token required)"
	if s.AllowUnauthenticated {
		public = "yes"
	}
	fmt.Fprintf(w, "Public:         %s\n", public)
	if len(s.Command) > 0 {
		fmt.Fprintf(w, "Command:        %s\n", strings.Join(s.Command, " "))
	}
	if len(s.Args) > 0 {
		fmt.Fprintf(w, "Args:           %s\n", strings.Join(s.Args, " "))
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

func servicesDelete(c *cmd, args []string) error {
	fs := newFlagSet("container services delete", "Usage: th container services delete NAME [--quiet]\n\nDeletes a Container and all its revisions. --quiet skips the prompt.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th container services delete NAME")
	}
	name := fs.Args()[0]
	cl, svc, err := c.mustService(name)
	if err != nil {
		return err
	}
	ok, err := c.confirm(fmt.Sprintf("Service [%s] will be deleted.", name))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	var res struct {
		Status string `json:"status"`
	}
	raw, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/services/"+svc.ID, nil, &res)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	if res.Status == "deleting" {
		fmt.Fprintf(c.app.Stdout, "Deleting service [%s]; it is removed in the background.\n", name)
	} else {
		fmt.Fprintf(c.app.Stdout, "Deleted service [%s].\n", name)
	}
	return nil
}

func servicesLogs(c *cmd, args []string) error {
	lines := 100
	fs := newFlagSet("container services logs", "Usage: th container services logs NAME [--lines N]\n\nPrints the latest log lines of a Container.")
	fs.Int(&lines, "lines", "Number of lines (default 100)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th container services logs NAME [--lines N]")
	}
	cl, svc, err := c.mustService(fs.Args()[0])
	if err != nil {
		return err
	}
	return c.printServiceLogs(cl, svc, lines)
}

// printServiceLogs prints the latest lines a Container or function wrote.
func (c *cmd) printServiceLogs(cl *api.Client, svc *api.Service, lines int) error {
	var l api.Logs
	raw, err := cl.Do(c.ctx, http.MethodGet, fmt.Sprintf("/api/v1/services/%s/logs?lines=%d", svc.ID, lines), nil, &l)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	if !l.Available {
		c.info("Logs are not available right now (the service may be scaled to zero or still starting).")
		return nil
	}
	for _, e := range l.Entries {
		msg := strings.TrimRight(e.Message, "\n")
		if e.Stream == "stderr" {
			fmt.Fprintf(c.app.Stdout, "%s  E %s\n", e.Timestamp, msg)
		} else {
			fmt.Fprintf(c.app.Stdout, "%s    %s\n", e.Timestamp, msg)
		}
	}
	return nil
}

func revisionsList(c *cmd, args []string) error {
	var service string
	fs := newFlagSet("container revisions list", "Usage: th container revisions list --service NAME\n\nLists a Container's revisions, newest first. * marks the serving one.")
	fs.String(&service, "service", "Service name")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if service == "" && len(fs.Args()) == 1 {
		service = fs.Args()[0]
	}
	if service == "" {
		return errors.New("usage: th container revisions list --service NAME")
	}
	cl, svc, err := c.mustService(service)
	if err != nil {
		return err
	}
	var revs []api.Revision
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID+"/revisions", nil, &revs)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, r := range revs {
		mark := ""
		if r.ID == svc.ServingRevisionID {
			mark = "*"
		}
		image := r.Image
		if r.ImageRemoved {
			image += " (removed — cannot roll back)"
		}
		rows = append(rows, []string{mark, r.Name, strconv.Itoa(r.Number), shortTime(r.CreatedAt), dash(r.CreatedBy), image})
	}
	c.table([]string{"", "REVISION", "NUMBER", "DEPLOYED", "BY", "IMAGE"}, rows)
	return nil
}

func servicesUpdateTraffic(c *cmd, args []string) error {
	var to string
	fs := newFlagSet("container services update-traffic", "Usage: th container services update-traffic NAME --to-revision REV\n\nSends all traffic to an earlier (or later) revision. REV is a\nrevision name, number, ID or LATEST.")
	fs.String(&to, "to-revision", "Revision name, number, ID or LATEST")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 || to == "" {
		return errors.New("usage: th container services update-traffic NAME --to-revision REV")
	}
	name := fs.Args()[0]
	cl, svc, err := c.mustService(name)
	if err != nil {
		return err
	}
	var revs []api.Revision
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID+"/revisions", nil, &revs); err != nil {
		return err
	}
	rev := matchRevision(revs, to)
	if rev == nil {
		return fmt.Errorf("revision %q not found. List them with: th container revisions list --service %s", to, name)
	}
	var out api.Service
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+svc.ID+"/revisions/"+rev.ID+"/serve", nil, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Service [%s] revision [%s] is serving 100 percent of traffic.\n", name, rev.Name)
	if out.URL != "" {
		fmt.Fprintf(c.app.Stdout, "Service URL: %s\n", out.URL)
	}
	return nil
}

func matchRevision(revs []api.Revision, want string) *api.Revision {
	if strings.EqualFold(want, "latest") && len(revs) > 0 {
		best := &revs[0]
		for i := range revs {
			if revs[i].Number > best.Number {
				best = &revs[i]
			}
		}
		return best
	}
	for i := range revs {
		if revs[i].Name == want || revs[i].ID == want {
			return &revs[i]
		}
	}
	if n, err := strconv.Atoi(want); err == nil {
		for i := range revs {
			if revs[i].Number == n {
				return &revs[i]
			}
		}
	}
	return nil
}

func buildsList(c *cmd, args []string) error {
	var repo, status, kind string
	fs := newFlagSet("builds list", `Usage: th builds list [--status STATUS] [--kind container|function] [--repository REPOSITORY]

Lists the Project's latest 100 builds, Containers and Functions alike,
newest first. REPOSITORY is where the image went (IMAGE in the default
registry, REGISTRY/IMAGE in another); DEPLOYED TO names the services whose
revisions use it. "image removed" means cleanup or a delete took the image
out of the registry.

Examples:
  th builds list --status active
  th builds list --kind function --status failed`)
	fs.String(&status, "status", "Only builds that are active (queued or running), queued, running, succeeded, failed or cancelled")
	fs.String(&kind, "kind", "Only container or function builds")
	fs.String(&repo, "repository", "Only builds into this repository")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	switch status {
	case "", "active", "queued", "running", "succeeded", "failed", "cancelled":
	default:
		return fmt.Errorf("--status must be active, queued, running, succeeded, failed or cancelled, not %q", status)
	}
	switch kind {
	case "", "container", "function":
	default:
		return fmt.Errorf("--kind must be container or function, not %q", kind)
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	path := "/api/v1/run/registries/builds?projectId=" + q(pid)
	if status != "" {
		path += "&status=" + q(status)
	}
	var all []api.ProjectBuild
	if _, err := cl.Do(c.ctx, http.MethodGet, path, nil, &all); err != nil {
		return err
	}
	builds := []api.ProjectBuild{}
	for _, b := range all {
		if (kind == "" || b.Kind == kind) && (repo == "" || b.Repository == repo) {
			builds = append(builds, b)
		}
	}
	if c.json() {
		return c.printJSON(builds)
	}
	if len(builds) == 0 {
		c.info("Listed 0 items.")
		return nil
	}
	rows := [][]string{}
	for _, b := range builds {
		dur := "-"
		if b.DurationSeconds > 0 {
			dur = fmt.Sprintf("%.0fs", b.DurationSeconds)
		}
		ref := b.Repository
		if b.Tag != "" {
			ref += ":" + b.Tag
		}
		st := b.Status
		if b.ImageGone {
			st += " (image removed)"
		}
		rows = append(rows, []string{b.ID, dash(b.Kind), ref, st, shortTime(b.CreatedAt), dur, dash(strings.Join(b.DeployedTo, ","))})
	}
	c.table([]string{"ID", "KIND", "REPOSITORY", "STATUS", "CREATED", "DURATION", "DEPLOYED TO"}, rows)
	return nil
}

func buildsLog(c *cmd, args []string) error {
	var follow bool
	fs := newFlagSet("builds log", "Usage: th builds log ID [--follow]\n\nPrints a build's log; --follow keeps printing until the build ends.")
	fs.Bool(&follow, "follow", "Keep printing until the build ends")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th builds log ID [--follow]")
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	id := fs.Args()[0]
	status, err := c.followBuildLog(cl, id, follow, c.app.Stdout)
	if err != nil {
		return err
	}
	if follow && status != "" && status != "succeeded" {
		return &exitError{code: 1, msg: fmt.Sprintf("Build %s %s.", id, status)}
	}
	return nil
}

func buildsCancel(c *cmd, args []string) error {
	fs := newFlagSet("builds cancel", "Usage: th builds cancel ID\n\nStops a queued or running build.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th builds cancel ID")
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	var b api.Build
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/run/builds/"+fs.Args()[0]+"/cancel", nil, &b)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Build [%s] is %s.\n", fs.Args()[0], dash(b.Status))
	return nil
}
