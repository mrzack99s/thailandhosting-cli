package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
	"github.com/mrzack99s/thailandhosting-cli/internal/config"
)

const configUsage = `Usage:
  th config set PROPERTY VALUE
  th config get PROPERTY
  th config unset PROPERTY
  th config list

Properties: project (ID or slug), region (ID), endpoint (Console URL).`

func configSet(c *cmd, args []string) error {
	fs := newFlagSet("config set", configUsage)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th config set PROPERTY VALUE")
	}
	key, val := fs.Args()[0], fs.Args()[1]
	if key == "endpoint" {
		val = strings.TrimRight(val, "/")
		if !strings.HasPrefix(val, "https://") && !strings.HasPrefix(val, "http://") {
			return errors.New("endpoint must start with https://")
		}
	}
	if err := c.cfg.Set(key, val); err != nil {
		return err
	}
	if err := c.saveConfig(); err != nil {
		return err
	}
	fmt.Fprintf(c.app.Stdout, "Updated property [%s].\n", key)
	return nil
}

func configUnset(c *cmd, args []string) error {
	fs := newFlagSet("config unset", configUsage)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th config unset PROPERTY")
	}
	if err := c.cfg.Set(fs.Args()[0], ""); err != nil {
		return err
	}
	if err := c.saveConfig(); err != nil {
		return err
	}
	fmt.Fprintf(c.app.Stdout, "Unset property [%s].\n", fs.Args()[0])
	return nil
}

func configGet(c *cmd, args []string) error {
	fs := newFlagSet("config get", configUsage)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th config get PROPERTY")
	}
	v, err := c.cfg.Get(fs.Args()[0])
	if err != nil {
		return err
	}
	if fs.Args()[0] == "endpoint" && v == "" {
		v = config.DefaultEndpoint
	}
	if v == "" {
		return &exitError{code: 1, msg: fmt.Sprintf("(unset) %s", fs.Args()[0])}
	}
	fmt.Fprintln(c.app.Stdout, v)
	return nil
}

func configList(c *cmd, args []string) error {
	fs := newFlagSet("config list", configUsage)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	vals := c.cfg.Values()
	signedIn := c.cfg.Email
	if c.json() {
		out := map[string]any{"config": vals, "path": c.path, "account": signedIn}
		return c.printJSON(out)
	}
	fmt.Fprintf(c.app.Stdout, "[core]  (%s)\n", c.path)
	fmt.Fprintf(c.app.Stdout, "account = %s\n", dash(signedIn))
	for _, k := range config.Keys {
		if v, ok := vals[k]; ok {
			fmt.Fprintf(c.app.Stdout, "%s = %s\n", k, v)
		}
	}
	for _, env := range []string{"TH_ENDPOINT", "TH_PROJECT", "TH_REGION", "TH_TOKEN", "TH_API_KEY"} {
		if c.app.Getenv(env) != "" {
			fmt.Fprintf(c.app.Stdout, "(%s is set and overrides the file)\n", env)
		}
	}
	return nil
}

func projectsList(c *cmd, args []string) error {
	fs := newFlagSet("projects list", "Usage: th projects list\n\nLists the Projects you own or were invited to.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	var projects []api.Project
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/projects", nil, &projects)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	active := c.projectSetting()
	rows := [][]string{}
	for _, p := range projects {
		mark := ""
		if active != "" && (active == p.ID || active == p.Slug) {
			mark = "*"
		}
		rows = append(rows, []string{mark, p.Slug, p.Name, p.ID, p.Role})
	}
	c.table([]string{"", "SLUG", "NAME", "ID", "ROLE"}, rows)
	return nil
}

func regionsList(c *cmd, args []string) error {
	fs := newFlagSet("regions list", "Usage: th regions list\n\nLists regions and their compute classes.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	cl, err := c.client(false)
	if err != nil {
		return err
	}
	var regions []api.Region
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/regions", nil, &regions)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, r := range regions {
		var classes []string
		for _, cc := range r.ComputeClasses {
			classes = append(classes, cc.ID)
		}
		def := ""
		if r.IsDefault {
			def = "yes"
		}
		rows = append(rows, []string{r.ID, r.Name, def, strings.Join(classes, ",")})
	}
	c.table([]string{"ID", "NAME", "DEFAULT", "COMPUTE CLASSES"}, rows)
	return nil
}

// resolveProject returns the Project ID and a display name (slug).
// An API key is bound to one Project, which whoami reports.
func (c *cmd) resolveProject(cl *api.Client) (string, string, error) {
	if _, isKey := c.token(); isKey {
		var me api.WhoAmI
		if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/cli/whoami", nil, &me); err != nil {
			return "", "", err
		}
		if me.ProjectID != "" {
			return me.ProjectID, me.ProjectID, nil
		}
	}
	want := c.projectSetting()
	var projects []api.Project
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/projects", nil, &projects); err != nil {
		return "", "", err
	}
	if want == "" {
		return "", "", fmt.Errorf("no project set. Run: th config set project <id-or-slug>, or pass --project\n%s", projectChoices(projects))
	}
	for _, p := range projects {
		if p.ID == want || p.Slug == want {
			return p.ID, dash(p.Slug), nil
		}
	}
	return "", "", fmt.Errorf("project %q not found\n%s", want, projectChoices(projects))
}

func projectChoices(projects []api.Project) string {
	if len(projects) == 0 {
		return "You have no Projects yet; create one in the Console."
	}
	var b strings.Builder
	b.WriteString("Your Projects:")
	for _, p := range projects {
		fmt.Fprintf(&b, "\n  %s  (%s, %s)", p.Slug, p.Name, p.ID)
	}
	return b.String()
}

// resolveRegion matches the configured region against GET /regions by ID
// or name. required=false returns "" when nothing is configured.
func (c *cmd) resolveRegion(cl *api.Client, required bool) (string, error) {
	want := c.regionSetting()
	if want == "" && !required {
		return "", nil
	}
	var regions []api.Region
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/regions", nil, &regions); err != nil {
		if want != "" {
			return want, nil // cannot check; let the server decide
		}
		return "", err
	}
	if want == "" {
		// Like gcloud's default region: the platform's default, said aloud.
		for _, r := range regions {
			if r.IsDefault {
				c.info("Using the default region [%s]. Set another with --region or: th config set region <id>", r.ID)
				return r.ID, nil
			}
		}
		return "", fmt.Errorf("a region is required for a new service. Pass --region or run: th config set region <id>\n%s", regionChoices(regions))
	}
	for _, r := range regions {
		if r.ID == want || strings.EqualFold(r.Name, want) {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("region %q not found\n%s", want, regionChoices(regions))
}

func regionChoices(regions []api.Region) string {
	var b strings.Builder
	b.WriteString("Available regions:")
	for _, r := range regions {
		fmt.Fprintf(&b, "\n  %s  (%s)", r.ID, r.Name)
	}
	return b.String()
}

// findService looks a Container up by slug or name inside the Project.
func (c *cmd) findService(cl *api.Client, projectID, name string) (*api.Service, error) {
	svcs, err := c.listContainers(cl, projectID)
	if err != nil {
		return nil, err
	}
	for i := range svcs {
		if svcs[i].Slug == name {
			return &svcs[i], nil
		}
	}
	for i := range svcs {
		if svcs[i].Name == name {
			return &svcs[i], nil
		}
	}
	return nil, nil
}

func (c *cmd) listContainers(cl *api.Client, projectID string) ([]api.Service, error) {
	var all []api.Service
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services?kind=container&projectId="+q(projectID), nil, &all); err != nil {
		return nil, err
	}
	// The list may span every Project the account can see.
	out := all[:0]
	for _, s := range all {
		if (s.ProjectID == "" || s.ProjectID == projectID) && (s.Kind == "" || s.Kind == "container") {
			out = append(out, s)
		}
	}
	return out, nil
}

// mustService resolves project + service or fails with a helpful message.
func (c *cmd) mustService(name string) (*api.Client, *api.Service, error) {
	cl, err := c.client(true)
	if err != nil {
		return nil, nil, err
	}
	pid, slug, err := c.resolveProject(cl)
	if err != nil {
		return nil, nil, err
	}
	svc, err := c.findService(cl, pid, name)
	if err != nil {
		return nil, nil, err
	}
	if svc == nil {
		return nil, nil, fmt.Errorf("service [%s] not found in project [%s]. List them with: th container services list", name, slug)
	}
	return cl, svc, nil
}
