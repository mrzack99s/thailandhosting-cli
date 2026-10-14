package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

// th static-site: Static Web sites, deployed from a built folder (this CLI)
// or from a Git repository (the Console).

func init() {
	register(map[string]handler{
		"static-site list":           ssList,
		"static-site describe":       ssDescribe,
		"static-site deploy":         ssDeploy,
		"static-site releases":       ssReleases,
		"static-site rollback":       ssRollback,
		"static-site delete":         ssDelete,
		"static-site builds list":    ssBuildsList,
		"static-site builds trigger": ssBuildsTrigger,
		"static-site builds log":     ssBuildsLog,
		"static-site domains list":   ssDomainsList,
		"static-site domains add":    ssDomainsAdd,
		"static-site domains verify": ssDomainsVerify,
		"static-site domains remove": ssDomainsRemove,
	},
		"static-site list | describe NAME | deploy NAME [DIR] | delete NAME",
		"static-site releases NAME | rollback NAME RELEASE|previous",
		"static-site builds list NAME | trigger NAME | log NAME BUILD",
		"static-site domains list NAME | add NAME DOMAIN | verify NAME DOMAIN | remove NAME DOMAIN",
	)
}

type ssSite struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	RegionID      string `json:"regionId"`
	ProjectID     string `json:"projectId,omitempty"`
	ActiveRelease string `json:"activeRelease"`
	Source        string `json:"source"`
}

type ssSource struct {
	Provider   string `json:"provider"`
	RepoURL    string `json:"repoUrl"`
	Branch     string `json:"branch"`
	RootDir    string `json:"rootDir"`
	Framework  string `json:"framework"`
	OutputDir  string `json:"outputDir"`
	AutoDeploy bool   `json:"autoDeploy"`
}

type ssBuild struct {
	ID            string `json:"id"`
	Number        int    `json:"number"`
	Status        string `json:"status"`
	Trigger       string `json:"trigger"`
	Branch        string `json:"branch"`
	CommitSHA     string `json:"commitSha"`
	CommitMessage string `json:"commitMessage"`
	ReleaseID     string `json:"releaseId"`
	Log           string `json:"log"`
	LogExpired    bool   `json:"logExpired"`
	Error         string `json:"error"`
	CreatedAt     string `json:"createdAt"`
	FinishedAt    string `json:"finishedAt"`
}

type ssView struct {
	Site        ssSite    `json:"site"`
	Source      *ssSource `json:"source"`
	LatestBuild *ssBuild  `json:"latestBuild"`
	URL         string    `json:"url"`
	PathURL     string    `json:"pathUrl"`
}

type ssRelease struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
}

type ssDomain struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Token    string `json:"token"`
	Verified bool   `json:"verified"`
}

type ssDeployResult struct {
	Release struct {
		ID    string `json:"id"`
		Files int    `json:"files"`
		Bytes int64  `json:"bytes"`
	} `json:"release"`
	URL      string `json:"url"`
	HasIndex bool   `json:"hasIndex"`
}

var ssSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,40}$`)

func ssSourceLabel(v ssView) string {
	if v.Source == nil {
		return "folder (th CLI)"
	}
	s := "git " + v.Source.RepoURL
	if v.Source.Branch != "" {
		s += " (" + v.Source.Branch + ")"
	}
	return s
}

func (c *cmd) ssListSites(cl *api.Client, projectID string) ([]ssView, []byte, error) {
	var all []ssView
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/static-web?projectId="+q(projectID), nil, &all)
	if err != nil {
		return nil, nil, err
	}
	out := all[:0]
	for _, v := range all {
		if v.Site.ProjectID == "" || v.Site.ProjectID == projectID {
			out = append(out, v)
		}
	}
	return out, raw, nil
}

func ssFind(sites []ssView, name string) *ssView {
	for i := range sites {
		if sites[i].Site.Slug == name {
			return &sites[i]
		}
	}
	for i := range sites {
		if sites[i].Site.Name == name {
			return &sites[i]
		}
	}
	return nil
}

// ssMustSite resolves project + site or fails with a helpful message.
func (c *cmd) ssMustSite(name string) (*api.Client, *ssView, error) {
	cl, err := c.client(true)
	if err != nil {
		return nil, nil, err
	}
	pid, slug, err := c.resolveProject(cl)
	if err != nil {
		return nil, nil, err
	}
	sites, _, err := c.ssListSites(cl, pid)
	if err != nil {
		return nil, nil, err
	}
	v := ssFind(sites, name)
	if v == nil {
		return nil, nil, fmt.Errorf("static site [%s] not found in project [%s]. List them with: th static-site list", name, slug)
	}
	return cl, v, nil
}

func (c *cmd) ssArgs(fs *flagSet, args []string, n int, usage string) ([]string, error) {
	if err := c.parse(fs, args); err != nil {
		return nil, err
	}
	if len(fs.Args()) != n {
		return nil, errors.New("usage: " + usage)
	}
	return fs.Args(), nil
}

func ssList(c *cmd, args []string) error {
	fs := newFlagSet("static-site list", "Usage: th static-site list\n\nLists the Static Web sites of the Project.")
	if _, err := c.ssArgs(fs, args, 0, "th static-site list"); err != nil {
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
	sites, raw, err := c.ssListSites(cl, pid)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, v := range sites {
		rows = append(rows, []string{v.Site.Slug, ssSourceLabel(v), dash(v.Site.RegionID), dash(v.URL)})
	}
	c.table([]string{"NAME", "SOURCE", "REGION", "URL"}, rows)
	return nil
}

func ssDescribe(c *cmd, args []string) error {
	fs := newFlagSet("static-site describe", "Usage: th static-site describe NAME\n\nShows a Static Web site: URL, source, active release and latest build.")
	a, err := c.ssArgs(fs, args, 1, "th static-site describe NAME")
	if err != nil {
		return err
	}
	_, v, err := c.ssMustSite(a[0])
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(v)
	}
	w := c.app.Stdout
	fmt.Fprintf(w, "Name:            %s\n", v.Site.Slug)
	if v.Site.Name != "" && v.Site.Name != v.Site.Slug {
		fmt.Fprintf(w, "Display name:    %s\n", v.Site.Name)
	}
	fmt.Fprintf(w, "ID:              %s\n", v.Site.ID)
	fmt.Fprintf(w, "URL:             %s\n", dash(v.URL))
	fmt.Fprintf(w, "Region:          %s\n", dash(v.Site.RegionID))
	fmt.Fprintf(w, "Source:          %s\n", ssSourceLabel(*v))
	if v.Source != nil {
		if v.Source.Framework != "" {
			fmt.Fprintf(w, "Framework:       %s\n", v.Source.Framework)
		}
		if v.Source.OutputDir != "" {
			fmt.Fprintf(w, "Output dir:      %s\n", v.Source.OutputDir)
		}
		fmt.Fprintf(w, "Auto deploy:     %t\n", v.Source.AutoDeploy)
	}
	fmt.Fprintf(w, "Active release:  %s\n", dash(v.Site.ActiveRelease))
	if b := v.LatestBuild; b != nil {
		fmt.Fprintf(w, "Latest build:    #%d %s (%s) %s\n", b.Number, b.Status, dash(b.Trigger), shortTime(b.CreatedAt))
		if b.Error != "" {
			fmt.Fprintf(w, "Build error:     %s\n", b.Error)
		}
	}
	return nil
}

const ssDeployUsage = `Usage: th static-site deploy NAME [DIR]

Uploads a built folder (default ".") as the next release of a Static Web
site. The site is created first when NAME does not exist yet (NAME becomes
its URL name: 3–41 characters a-z, 0-9 and -), in --region or the default
region.

Every file in DIR is uploaded — .gitignore is not read, since build output
is often gitignored — except .git, .DS_Store and paths listed in a .thignore
file in DIR. Up to 20,000 files, 500 MB (100 MB compressed) per deploy.

Examples:
  npm run build && th static-site deploy my-site dist
  th static-site deploy docs ./public --region bkk`

func ssDeploy(c *cmd, args []string) error {
	fs := newFlagSet("static-site deploy", ssDeployUsage)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if n := len(fs.Args()); n < 1 || n > 2 {
		return errors.New("usage: th static-site deploy NAME [DIR]")
	}
	name, dir := fs.Args()[0], "."
	dirGiven := len(fs.Args()) == 2
	if dirGiven {
		dir = fs.Args()[1]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	if hasIndex, cands := ssIndexHint(abs); !hasIndex && len(cands) > 0 {
		hint := fmt.Sprintf("%s has no index.html, but %s does. Deploy the built folder instead, e.g.: th static-site deploy %s %s",
			dir, strings.Join(cands, "/, ")+"/", name, filepath.ToSlash(filepath.Join(dir, cands[0])))
		if !dirGiven {
			return errors.New(hint)
		}
		c.info("Hint: %s", hint)
	}

	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, projectName, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	sites, _, err := c.ssListSites(cl, pid)
	if err != nil {
		return err
	}
	existing := ssFind(sites, name)
	var regionID string
	if existing == nil {
		if !ssSlugRE.MatchString(name) {
			return fmt.Errorf("static site [%s] does not exist, and %q cannot be a new site's name: use 3–41 characters a-z, 0-9 and - (starting with a letter or digit)", name, name)
		}
		if regionID, err = c.resolveRegion(cl, true); err != nil {
			return err
		}
	}

	// Pack before creating anything, so a bad folder leaves no empty site.
	tmp, err := os.CreateTemp("", "th-static-*.tar.gz")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	c.info("Packing %s…", abs)
	res, err := ssPack(abs, tmp)
	if errors.Is(err, errSSTooLarge) {
		return fmt.Errorf("the packed folder is larger than %d MB compressed. Leave files out with a .thignore file (same syntax as .gitignore)", ssMaxCompressed>>20)
	}
	if err != nil {
		return err
	}
	if res.Files == 0 {
		return fmt.Errorf("%s has no files to upload", dir)
	}
	if res.Ignored {
		c.info("Skipping files listed in .thignore.")
	}

	created := false
	var site ssSite
	if existing != nil {
		site = existing.Site
		c.info("Deploying to static site [%s] in project [%s]", site.Slug, projectName)
	} else {
		c.info("Creating static site [%s] in project [%s] region [%s]…", name, projectName, regionID)
		var out struct {
			Site ssSite `json:"site"`
			URL  string `json:"url"`
		}
		if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/static-web/upload-site", map[string]string{"name": name, "slug": name, "projectId": pid, "regionId": regionID}, &out); err != nil {
			return err
		}
		site, created = out.Site, true
		if site.Slug == "" {
			site.Slug = name
		}
	}

	if _, err := tmp.Seek(0, 0); err != nil {
		return err
	}
	fmt.Fprintf(c.app.Stderr, "Uploading %d files (%s, %s compressed)…", res.Files, humanBytes(res.Bytes), humanBytes(res.Compressed))
	var dr ssDeployResult
	if _, err := cl.UploadStream(c.ctx, "/api/v1/sites/"+site.ID+"/deploy", "application/gzip", tmp, res.Compressed, &dr); err != nil {
		fmt.Fprintln(c.app.Stderr, " failed")
		if created {
			c.info("The site [%s] was created; run the same command again to retry the upload.", site.Slug)
		}
		return err
	}
	fmt.Fprintln(c.app.Stderr, " done")
	if !dr.HasIndex {
		c.info("Warning: the release has no index.html at its top level, so the site's home page shows \"not found\". Deploy the folder that holds index.html (often dist/, build/ or out/).")
	}
	if c.json() {
		return c.printJSON(map[string]any{"site": site, "created": created, "release": dr.Release, "url": dr.URL, "hasIndex": dr.HasIndex})
	}
	fmt.Fprintf(c.app.Stdout, "Static site [%s] release [%s] is live (%d files).\n", site.Slug, dr.Release.ID, dr.Release.Files)
	if dr.URL != "" {
		fmt.Fprintf(c.app.Stdout, "URL: %s\n", dr.URL)
	}
	return nil
}

func ssReleases(c *cmd, args []string) error {
	fs := newFlagSet("static-site releases", "Usage: th static-site releases NAME\n\nLists a Static Web site's releases, newest first. * marks the live one.")
	a, err := c.ssArgs(fs, args, 1, "th static-site releases NAME")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustSite(a[0])
	if err != nil {
		return err
	}
	var rels []ssRelease
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/sites/"+v.Site.ID+"/releases", nil, &rels)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, r := range rels {
		mark := ""
		if r.ID == v.Site.ActiveRelease {
			mark = "*"
		}
		rows = append(rows, []string{mark, r.ID, shortTime(r.CreatedAt)})
	}
	c.table([]string{"", "RELEASE", "CREATED"}, rows)
	return nil
}

func ssRollback(c *cmd, args []string) error {
	fs := newFlagSet("static-site rollback", "Usage: th static-site rollback NAME RELEASE|previous\n\nMakes an earlier release live again. \"previous\" is the release before the live one.")
	a, err := c.ssArgs(fs, args, 2, "th static-site rollback NAME RELEASE|previous")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustSite(a[0])
	if err != nil {
		return err
	}
	target := a[1]
	if target == "previous" {
		var rels []ssRelease
		if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/sites/"+v.Site.ID+"/releases", nil, &rels); err != nil {
			return err
		}
		target = ""
		for i, r := range rels {
			if r.ID == v.Site.ActiveRelease && i+1 < len(rels) {
				target = rels[i+1].ID
			}
		}
		if target == "" {
			return fmt.Errorf("static site [%s] has no release before the live one. See: th static-site releases %s", v.Site.Slug, v.Site.Slug)
		}
	}
	var res struct {
		ActiveRelease string `json:"activeRelease"`
		URL           string `json:"url"`
	}
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/sites/"+v.Site.ID+"/rollback/"+url.PathEscape(target), nil, &res)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	if res.ActiveRelease == "" {
		res.ActiveRelease = target
	}
	fmt.Fprintf(c.app.Stdout, "Static site [%s] now serves release [%s].\n", v.Site.Slug, res.ActiveRelease)
	if res.URL != "" {
		fmt.Fprintf(c.app.Stdout, "URL: %s\n", res.URL)
	}
	return nil
}

func ssDelete(c *cmd, args []string) error {
	fs := newFlagSet("static-site delete", "Usage: th static-site delete NAME [--quiet]\n\nDeletes a Static Web site with all its releases and domains. --quiet skips the prompt.")
	a, err := c.ssArgs(fs, args, 1, "th static-site delete NAME")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustSite(a[0])
	if err != nil {
		return err
	}
	ok, err := c.confirm(fmt.Sprintf("Static site [%s] and all its releases will be deleted.", v.Site.Slug))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	raw, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/sites/"+v.Site.ID, nil, nil)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Deleted static site [%s].\n", v.Site.Slug)
	return nil
}

// ssMustGitSite is ssMustSite for the builds commands.
func (c *cmd) ssMustGitSite(name string) (*api.Client, *ssView, error) {
	cl, v, err := c.ssMustSite(name)
	if err != nil {
		return nil, nil, err
	}
	if v.Source == nil {
		return nil, nil, fmt.Errorf("static site [%s] is deployed from a folder (th CLI), so it has no builds. Deploy a new version with: th static-site deploy %s DIR", v.Site.Slug, v.Site.Slug)
	}
	return cl, v, nil
}

func ssBuildsList(c *cmd, args []string) error {
	fs := newFlagSet("static-site builds list", "Usage: th static-site builds list NAME\n\nLists the Git builds of a Static Web site, newest first.")
	a, err := c.ssArgs(fs, args, 1, "th static-site builds list NAME")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustGitSite(a[0])
	if err != nil {
		return err
	}
	var builds []ssBuild
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/sites/"+v.Site.ID+"/builds", nil, &builds)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, b := range builds {
		commit := b.CommitSHA
		if len(commit) > 7 {
			commit = commit[:7]
		}
		msg := strings.SplitN(b.CommitMessage, "\n", 2)[0]
		if len(msg) > 50 {
			msg = msg[:47] + "..."
		}
		rows = append(rows, []string{"#" + strconv.Itoa(b.Number), b.ID, dash(b.Status), dash(b.Trigger), dash(commit), dash(msg), shortTime(b.CreatedAt)})
	}
	c.table([]string{"NUMBER", "ID", "STATUS", "TRIGGER", "COMMIT", "MESSAGE", "CREATED"}, rows)
	return nil
}

func ssBuildsTrigger(c *cmd, args []string) error {
	fs := newFlagSet("static-site builds trigger", "Usage: th static-site builds trigger NAME\n\nStarts a Git build of the site's branch.")
	a, err := c.ssArgs(fs, args, 1, "th static-site builds trigger NAME")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustGitSite(a[0])
	if err != nil {
		return err
	}
	var b ssBuild
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/sites/"+v.Site.ID+"/builds", nil, &b)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Build #%d [%s] is %s.\n", b.Number, b.ID, dash(b.Status))
	fmt.Fprintf(c.app.Stdout, "Follow it with: th static-site builds log %s %s --follow\n", v.Site.Slug, b.ID)
	return nil
}

func ssBuildDone(status string) bool { return status != "queued" && status != "running" }

func ssBuildsLog(c *cmd, args []string) error {
	var follow bool
	fs := newFlagSet("static-site builds log", "Usage: th static-site builds log NAME BUILD [--follow]\n\nPrints a Git build's log. BUILD is its ID, its number (#3 or 3) or \"latest\".")
	fs.Bool(&follow, "follow", "Keep printing until the build finishes")
	a, err := c.ssArgs(fs, args, 2, "th static-site builds log NAME BUILD")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustGitSite(a[0])
	if err != nil {
		return err
	}
	id := a[1]
	if num := strings.TrimPrefix(id, "#"); id == "latest" || ssIsDigits(num) {
		var builds []ssBuild
		if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/sites/"+v.Site.ID+"/builds", nil, &builds); err != nil {
			return err
		}
		id = ""
		for _, b := range builds {
			if a[1] == "latest" || strconv.Itoa(b.Number) == num {
				id = b.ID
				break
			}
		}
		if id == "" {
			return fmt.Errorf("build %s not found. List them with: th static-site builds list %s", a[1], v.Site.Slug)
		}
	}
	path := "/api/v1/sites/" + v.Site.ID + "/builds/" + url.PathEscape(id)
	sig, stop := c.interrupts()
	defer stop()
	printed := 0
	for {
		var b ssBuild
		raw, err := cl.Do(c.ctx, http.MethodGet, path, nil, &b)
		if err != nil {
			return err
		}
		if c.json() && !follow {
			return c.printJSON(raw)
		}
		if b.LogExpired && b.Log == "" {
			c.info("The log of this build is no longer kept.")
		}
		if len(b.Log) >= printed {
			fmt.Fprint(c.app.Stdout, b.Log[printed:])
			printed = len(b.Log)
		} else { // the log moved to storage and was rewritten
			fmt.Fprint(c.app.Stdout, b.Log)
			printed = len(b.Log)
		}
		if !follow || ssBuildDone(b.Status) {
			if printed > 0 && !strings.HasSuffix(b.Log, "\n") {
				fmt.Fprintln(c.app.Stdout)
			}
			c.info("Build #%d %s", b.Number, dash(b.Status))
			if b.Error != "" {
				c.info("Error: %s", b.Error)
			}
			if follow && b.Status != "success" {
				return &exitError{code: 1}
			}
			return nil
		}
		select {
		case <-sig:
			return &exitError{code: 130, msg: "\nBuild continues on the server."}
		case <-time.After(c.app.PollInterval):
		}
	}
}

func ssIsDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (c *cmd) ssDomains(cl *api.Client, siteID string) ([]ssDomain, []byte, error) {
	var ds []ssDomain
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/sites/"+siteID+"/domains", nil, &ds)
	return ds, raw, err
}

func ssFindDomain(ds []ssDomain, host string) *ssDomain {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for i := range ds {
		if ds[i].Host == host {
			return &ds[i]
		}
	}
	return nil
}

// ssCNAMETarget is the site's own hostname, when its URL has one a CNAME
// can point at (not a path-style development URL).
var ssHostRE = regexp.MustCompile(`^[a-z0-9.-]+\.[a-z]{2,}$`)

func ssCNAMETarget(siteURL string) string {
	u, err := url.Parse(siteURL)
	if err != nil || (u.Path != "" && u.Path != "/") || !ssHostRE.MatchString(u.Host) {
		return ""
	}
	return u.Host
}

func (c *cmd) ssPrintDNS(v *ssView, d ssDomain) {
	w := c.app.Stdout
	fmt.Fprintf(w, "DNS records for %s:\n", d.Host)
	rows := [][]string{}
	if t := ssCNAMETarget(v.URL); t != "" {
		rows = append(rows, []string{"CNAME", d.Host, t})
	}
	rows = append(rows, []string{"TXT", "_thailandhosting." + d.Host, d.Token})
	c.table([]string{"TYPE", "NAME", "VALUE"}, rows)
	if ssCNAMETarget(v.URL) != "" {
		fmt.Fprintln(w, "For an apex domain (example.com) use your DNS host's ALIAS/ANAME or CNAME flattening.")
	}
}

func ssDomainsList(c *cmd, args []string) error {
	fs := newFlagSet("static-site domains list", "Usage: th static-site domains list NAME\n\nLists a Static Web site's custom domains.")
	a, err := c.ssArgs(fs, args, 1, "th static-site domains list NAME")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustSite(a[0])
	if err != nil {
		return err
	}
	ds, raw, err := c.ssDomains(cl, v.Site.ID)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, d := range ds {
		status := "pending (add the DNS records, then: th static-site domains verify " + v.Site.Slug + " " + d.Host + ")"
		if d.Verified {
			status = "verified"
		}
		rows = append(rows, []string{d.Host, status})
	}
	c.table([]string{"DOMAIN", "STATUS"}, rows)
	return nil
}

func ssDomainsAdd(c *cmd, args []string) error {
	fs := newFlagSet("static-site domains add", "Usage: th static-site domains add NAME DOMAIN\n\nAdds a custom domain and prints the DNS records that prove you own it.")
	a, err := c.ssArgs(fs, args, 2, "th static-site domains add NAME DOMAIN")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustSite(a[0])
	if err != nil {
		return err
	}
	var d ssDomain
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/sites/"+v.Site.ID+"/domains", map[string]string{"Host": a[1]}, &d)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Added domain [%s] to static site [%s].\n\n", d.Host, v.Site.Slug)
	c.ssPrintDNS(v, d)
	fmt.Fprintf(c.app.Stdout, "\nOnce the records are in place, run: th static-site domains verify %s %s\n", v.Site.Slug, d.Host)
	return nil
}

func ssDomainsVerify(c *cmd, args []string) error {
	fs := newFlagSet("static-site domains verify", "Usage: th static-site domains verify NAME DOMAIN\n\nChecks the domain's TXT record and turns the domain on.")
	a, err := c.ssArgs(fs, args, 2, "th static-site domains verify NAME DOMAIN")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustSite(a[0])
	if err != nil {
		return err
	}
	ds, _, err := c.ssDomains(cl, v.Site.ID)
	if err != nil {
		return err
	}
	d := ssFindDomain(ds, a[1])
	if d == nil {
		return fmt.Errorf("domain [%s] is not on static site [%s]. Add it with: th static-site domains add %s %s", a[1], v.Site.Slug, v.Site.Slug, a[1])
	}
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/sites/"+v.Site.ID+"/domains/"+url.PathEscape(d.ID)+"/verify", nil, nil)
	if err != nil {
		if !c.json() {
			c.ssPrintDNS(v, *d)
		}
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Domain [%s] is verified; https://%s/ serves static site [%s].\n", d.Host, d.Host, v.Site.Slug)
	return nil
}

func ssDomainsRemove(c *cmd, args []string) error {
	fs := newFlagSet("static-site domains remove", "Usage: th static-site domains remove NAME DOMAIN [--quiet]\n\nRemoves a custom domain from the site.")
	a, err := c.ssArgs(fs, args, 2, "th static-site domains remove NAME DOMAIN")
	if err != nil {
		return err
	}
	cl, v, err := c.ssMustSite(a[0])
	if err != nil {
		return err
	}
	ds, _, err := c.ssDomains(cl, v.Site.ID)
	if err != nil {
		return err
	}
	d := ssFindDomain(ds, a[1])
	if d == nil {
		return fmt.Errorf("domain [%s] is not on static site [%s]", a[1], v.Site.Slug)
	}
	ok, err := c.confirm(fmt.Sprintf("Domain [%s] will be removed from static site [%s].", d.Host, v.Site.Slug))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	raw, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/sites/"+v.Site.ID+"/domains/"+url.PathEscape(d.ID), nil, nil)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Removed domain [%s].\n", d.Host)
	return nil
}
