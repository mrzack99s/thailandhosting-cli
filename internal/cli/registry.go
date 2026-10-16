package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

// th registry: Container Registry. A Project has a default registry, where
// builds land (<host>/<project>/<image>), and may create more
// (<host>/<project>/<registry>/<image>), each with its own access tokens,
// public pulls, immutable tags and cleanup policy. Everything goes through
// /api/v1/run/registries.

func init() {
	register(map[string]handler{
		"registry list":          registryList,
		"registry create":        registryCreate,
		"registry describe":      registryDescribe,
		"registry update":        registryUpdate,
		"registry delete":        registryDelete,
		"registry cleanup":       registryCleanup,
		"registry images list":   registryImagesList,
		"registry images delete": registryImagesDelete,
		"registry images tag":    registryImagesTag,
		"registry images copy":   registryImagesCopy,
		"registry tokens list":   registryTokensList,
		"registry tokens create": registryTokensCreate,
		"registry tokens revoke": registryTokensRevoke,
	},
		"registry list | create REGISTRY | describe REGISTRY | update REGISTRY | delete REGISTRY | cleanup REGISTRY",
		"registry images list REPOSITORY | delete IMAGE | tag IMAGE TAG | copy SRC DST",
		"registry tokens list REGISTRY | create REGISTRY --name NAME | revoke REGISTRY TOKEN",
	)
}

// rgRegistry is a registry as GET /api/v1/run/registries lists it.
type rgRegistry struct {
	ID                  string   `json:"id"`
	ProjectID           string   `json:"projectId"`
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	IsDefault           bool     `json:"isDefault"`
	Public              bool     `json:"public"`
	ImmutableTags       bool     `json:"immutableTags"`
	CleanupKeepLatest   int      `json:"cleanupKeepLatest"`
	CleanupUntaggedDays int      `json:"cleanupUntaggedDays"`
	CleanupUnpulledDays int      `json:"cleanupUnpulledDays"`
	CleanupKeepTags     []string `json:"cleanupKeepTags"`
	CreatedAt           string   `json:"createdAt"`
	UpdatedAt           string   `json:"updatedAt"`
	Path                string   `json:"path"`
	Repositories        int64    `json:"repositories"`
	Images              int64    `json:"images"`
	SizeBytes           int64    `json:"sizeBytes"`
	Pulls               int64    `json:"pulls"`
	Tokens              int64    `json:"tokens"`
	LastPushAt          string   `json:"lastPushAt,omitempty"`
}

type rgOverview struct {
	Host          string       `json:"host"`
	Namespace     string       `json:"namespace"`
	Registries    []rgRegistry `json:"registries"`
	UsedBytes     int64        `json:"usedBytes"`
	QuotaBytes    int64        `json:"quotaBytes"`
	MaxRegistries int          `json:"maxRegistries"`
}

// rgRepository is one repository of GET /api/v1/run/registries/{id}.
type rgRepository struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Image        string   `json:"image"`
	Tags         []string `json:"tags"`
	Images       int      `json:"images"`
	SizeBytes    int64    `json:"sizeBytes"`
	Pulls        int64    `json:"pulls"`
	LastPulledAt string   `json:"lastPulledAt,omitempty"`
	UpdatedAt    string   `json:"updatedAt"`
}

type rgDetail struct {
	Registry     rgRegistry     `json:"registry"`
	Repositories []rgRepository `json:"repositories"`
}

type rgToken struct {
	ID         string `json:"id"`
	RegistryID string `json:"registryId"`
	Name       string `json:"name"`
	Hint       string `json:"hint"`
	Permission string `json:"permission"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
	LastUsedIP string `json:"lastUsedIp"`
	CreatedBy  string `json:"createdBy"`
	CreatedAt  string `json:"createdAt"`
}

type rgCleanupItem struct {
	Repository   string   `json:"repository"`
	Digest       string   `json:"digest"`
	Tags         []string `json:"tags"`
	SizeBytes    int64    `json:"sizeBytes"`
	Reason       string   `json:"reason"`
	CreatedAt    string   `json:"createdAt"`
	LastPulledAt string   `json:"lastPulledAt,omitempty"`
}

type rgCleanup struct {
	Images    int             `json:"images"`
	SizeBytes int64           `json:"sizeBytes"`
	Items     []rgCleanupItem `json:"items"`
}

// rgSettings is what create, update and cleanup --preview send; nil fields
// are left out so the server keeps them.
type rgSettings struct {
	ProjectID           string    `json:"projectId,omitempty"`
	Name                string    `json:"name,omitempty"`
	Description         *string   `json:"description,omitempty"`
	Public              *bool     `json:"public,omitempty"`
	ImmutableTags       *bool     `json:"immutableTags,omitempty"`
	CleanupKeepLatest   *int      `json:"cleanupKeepLatest,omitempty"`
	CleanupUntaggedDays *int      `json:"cleanupUntaggedDays,omitempty"`
	CleanupUnpulledDays *int      `json:"cleanupUnpulledDays,omitempty"`
	CleanupKeepTags     *[]string `json:"cleanupKeepTags,omitempty"`
}

func (s rgSettings) policySet() bool {
	return s.CleanupKeepLatest != nil || s.CleanupUntaggedDays != nil || s.CleanupUnpulledDays != nil || s.CleanupKeepTags != nil
}

func rgPath(id string, parts ...string) string {
	p := "/api/v1/run/registries/" + url.PathEscape(id)
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p
}

// registryOverview loads the Project's registries.
func (c *cmd) registryOverview() (*api.Client, string, rgOverview, []byte, error) {
	var o rgOverview
	cl, err := c.client(true)
	if err != nil {
		return nil, "", o, nil, err
	}
	pid, pname, err := c.resolveProject(cl)
	if err != nil {
		return nil, "", o, nil, err
	}
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/run/registries?projectId="+q(pid), nil, &o)
	return cl, pname, o, raw, err
}

// find returns the registry named name (or with that ID).
func (o rgOverview) find(name, pname string) (rgRegistry, error) {
	for _, r := range o.Registries {
		if r.Name == name || r.ID == name {
			return r, nil
		}
	}
	return rgRegistry{}, fmt.Errorf("registry %q not found in project [%s]. Run: th registry list", name, pname)
}

// findRegistry resolves the Project and its registry named name.
func (c *cmd) findRegistry(name string) (*api.Client, rgOverview, rgRegistry, error) {
	cl, pname, o, _, err := c.registryOverview()
	if err != nil {
		return nil, o, rgRegistry{}, err
	}
	reg, err := o.find(name, pname)
	return cl, o, reg, err
}

// rgRef is an image of one of the Project's registries.
type rgRef struct {
	reg         rgRegistry
	image       string // its name inside the registry
	tag, digest string // digest may be a short hex prefix
}

// repository is the image's path inside the Project, as th takes it.
func (r rgRef) repository() string {
	if r.reg.IsDefault {
		return r.image
	}
	return r.reg.Name + "/" + r.image
}

func (r rgRef) String() string {
	s := r.repository()
	switch {
	case r.digest != "":
		s += "@" + r.digest
	case r.tag != "":
		s += ":" + r.tag
	}
	return s
}

// parseRef reads [REGISTRY/]IMAGE[:TAG|@DIGEST], or the full
// <host>/<project>/[REGISTRY/]IMAGE… form of this Project. Without a
// registry it is the default one's (as builds name images); "default/"
// names it too.
func (o rgOverview) parseRef(s, pname string) (rgRef, error) {
	var r rgRef
	orig := s
	if o.Host != "" && strings.HasPrefix(s, o.Host+"/") {
		s = strings.TrimPrefix(s, o.Host+"/")
		if !strings.HasPrefix(s, o.Namespace+"/") {
			return r, fmt.Errorf("%s is not in this Project's registries (%s/%s); pass --project for the Project that owns it", orig, o.Host, o.Namespace)
		}
		s = strings.TrimPrefix(s, o.Namespace+"/")
	}
	if p, d, ok := strings.Cut(s, "@"); ok {
		s = p
		d = strings.ToLower(d)
		if !fullDigestRE.MatchString(d) {
			hex := strings.TrimPrefix(d, "sha256:")
			if !shortDigestRE.MatchString(hex) {
				return r, fmt.Errorf("%q is not a digest: use sha256:<64 hex> or at least the first 7 hex characters", d)
			}
			d = hex
		}
		r.digest = d
	} else if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		s, r.tag = s[:i], s[i+1:]
		if r.tag == "" {
			return r, fmt.Errorf("%q has an empty tag", orig)
		}
	}
	parts := strings.Split(s, "/")
	regName := "default"
	switch len(parts) {
	case 1:
		r.image = parts[0]
	case 2:
		regName, r.image = parts[0], parts[1]
	default:
		return r, fmt.Errorf("%s is not in this Project's registries (%s/%s): give REPOSITORY or REGISTRY/REPOSITORY, or pass --project for the Project that owns it", orig, o.Host, o.Namespace)
	}
	if r.image == "" {
		return r, fmt.Errorf("%q has no repository name", orig)
	}
	for _, reg := range o.Registries {
		if (regName == "default" && reg.IsDefault) || (reg.Name == regName && !reg.IsDefault) {
			r.reg = reg
			return r, nil
		}
	}
	if regName == "default" {
		return r, errors.New("the Project has no default registry yet")
	}
	return r, fmt.Errorf("registry %q not found in project [%s]. Run: th registry list", regName, pname)
}

func (c *cmd) rgImages(cl *api.Client, r rgRef) ([]api.RegistryImage, []byte, error) {
	var imgs []api.RegistryImage
	raw, err := cl.Do(c.ctx, http.MethodGet, rgPath(r.reg.ID, "images")+"?repository="+q(r.image), nil, &imgs)
	return imgs, raw, err
}

// resolveDigest turns a short digest of r into the full one.
func (c *cmd) resolveDigest(cl *api.Client, r *rgRef) (*api.RegistryImage, error) {
	imgs, _, err := c.rgImages(cl, *r)
	if err != nil {
		return nil, err
	}
	img, err := matchDigest(imgs, r.repository(), r.digest, "th registry images list "+r.repository())
	if err != nil {
		return nil, err
	}
	r.digest = img.Digest
	return img, nil
}

// ---- settings flags ----

type rgSettingFlags struct {
	description                            string
	public, immutable                      bool
	keepLatest, untaggedDays, unpulledDays int
	keepTags                               []string
}

func (f *rgSettingFlags) add(fs *flagSet, settings bool) {
	if settings {
		fs.String(&f.description, "description", "What the registry is for (up to 200 characters)")
		fs.Bool(&f.public, "public", "Let anyone pull without signing in (--no-public: sign-in or a token)")
		fs.Bool(&f.immutable, "immutable-tags", "A pushed tag never moves to another image, except latest")
	}
	fs.Int(&f.keepLatest, "keep-latest", "Keep the newest N images of each repository (0 = no limit)")
	fs.Int(&f.untaggedDays, "untagged-days", "Remove untagged images after N days (1-365, default 7)")
	fs.Int(&f.unpulledDays, "unpulled-days", "Remove images nobody pulled for N days (7-730, 0 = off)")
	fs.List(&f.keepTags, "keep-tags", `Tag patterns always kept, comma-separated, e.g. v*,prod ("none" clears)`)
}

func (f *rgSettingFlags) fill(fs *flagSet, in *rgSettings) {
	if fs.Set("description") {
		in.Description = &f.description
	}
	if fs.Set("public") {
		in.Public = &f.public
	}
	if fs.Set("immutable-tags") {
		in.ImmutableTags = &f.immutable
	}
	if fs.Set("keep-latest") {
		in.CleanupKeepLatest = &f.keepLatest
	}
	if fs.Set("untagged-days") {
		in.CleanupUntaggedDays = &f.untaggedDays
	}
	if fs.Set("unpulled-days") {
		in.CleanupUnpulledDays = &f.unpulledDays
	}
	if fs.Set("keep-tags") {
		tags := []string{}
		for _, t := range splitList(f.keepTags) {
			if t = strings.TrimSpace(t); t != "" && !strings.EqualFold(t, "none") {
				tags = append(tags, t)
			}
		}
		in.CleanupKeepTags = &tags
	}
}

const rgSettingsHelp = `Images a Container runs, or keeps for rolling back, are never removed by
cleanup. Removing images frees storage once no other image uses their
layers.`

// ---- registries ----

func registryList(c *cmd, args []string) error {
	fs := newFlagSet("registry list", `Usage: th registry list

Lists the Project's registries and how much of the registry storage quota
(shared by all of them) they use. "default" is where builds land.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	_, pname, o, raw, err := c.registryOverview()
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	usage := "used " + humanBytes(o.UsedBytes)
	if o.QuotaBytes > 0 {
		pct := (o.UsedBytes*100 + o.QuotaBytes/2) / o.QuotaBytes
		usage += fmt.Sprintf(" of %s (%d%%)", quotaGB(o.QuotaBytes), pct)
	}
	header := fmt.Sprintf("Registries of project [%s] · %s", pname, usage)
	if o.MaxRegistries > 0 {
		header += fmt.Sprintf(" · %d of %d registries", len(o.Registries), o.MaxRegistries)
	}
	fmt.Fprintf(c.app.Stdout, "%s\n\n", header)
	rows := [][]string{}
	for _, r := range o.Registries {
		rows = append(rows, []string{r.Name, dash(r.Path), rgAccess(r), rgTagMode(r), strconv.FormatInt(r.Repositories, 10),
			strconv.FormatInt(r.Images, 10), humanBytes(r.SizeBytes), strconv.FormatInt(r.Pulls, 10), shortTime(r.LastPushAt)})
	}
	c.table([]string{"NAME", "PATH", "ACCESS", "TAGS", "REPOSITORIES", "IMAGES", "SIZE", "PULLS", "LAST PUSH"}, rows)
	return nil
}

func rgAccess(r rgRegistry) string {
	if r.Public {
		return "public"
	}
	return "private"
}

func rgTagMode(r rgRegistry) string {
	if r.ImmutableTags {
		return "immutable"
	}
	return "mutable"
}

// rgPolicy describes a cleanup policy in one line.
func rgPolicy(r rgRegistry) string {
	untagged := r.CleanupUntaggedDays
	if untagged <= 0 {
		untagged = 7
	}
	parts := []string{fmt.Sprintf("untagged images after %d days", untagged)}
	if r.CleanupKeepLatest > 0 {
		parts = append(parts, fmt.Sprintf("beyond the newest %d per repository", r.CleanupKeepLatest))
	}
	if r.CleanupUnpulledDays > 0 {
		parts = append(parts, fmt.Sprintf("not pulled for %d days", r.CleanupUnpulledDays))
	}
	s := "remove " + strings.Join(parts, "; ")
	if len(r.CleanupKeepTags) > 0 {
		s += "; always keep tags " + strings.Join(r.CleanupKeepTags, ", ")
	}
	return s
}

func (c *cmd) printRegistry(r rgRegistry) {
	w := c.app.Stdout
	fmt.Fprintf(w, "Registry %s (%s)\n\n", r.Name, dash(r.Path))
	fmt.Fprintf(w, "ID:            %s\n", r.ID)
	if r.Description != "" {
		fmt.Fprintf(w, "Description:   %s\n", r.Description)
	}
	if r.Public {
		fmt.Fprintf(w, "Access:        public: anyone can pull without signing in (the Project pays the transfer)\n")
	} else {
		fmt.Fprintf(w, "Access:        private: pull with th auth configure-docker or an access token\n")
	}
	if r.ImmutableTags {
		fmt.Fprintf(w, "Tags:          immutable: a pushed tag never moves to another image (latest excepted)\n")
	} else {
		fmt.Fprintf(w, "Tags:          mutable\n")
	}
	fmt.Fprintf(w, "Cleanup:       %s\n", rgPolicy(r))
	fmt.Fprintf(w, "Usage:         %d repositories, %d images, %s, %d pulls\n", r.Repositories, r.Images, humanBytes(r.SizeBytes), r.Pulls)
	fmt.Fprintf(w, "Access tokens: %d\n", r.Tokens)
	if r.LastPushAt != "" {
		fmt.Fprintf(w, "Last push:     %s\n", shortTime(r.LastPushAt))
	}
	if r.CreatedAt != "" {
		fmt.Fprintf(w, "Created:       %s\n", shortTime(r.CreatedAt))
	}
}

func registryCreate(c *cmd, args []string) error {
	fs := newFlagSet("registry create", `Usage: th registry create REGISTRY [--description TEXT] [--public]
       [--immutable-tags] [--keep-latest N] [--untagged-days N]
       [--unpulled-days N] [--keep-tags PATTERNS]

Creates a registry in the Project. Names are 1-40 lowercase letters,
digits and -, starting and ending with a letter or digit ("default" is
taken). Its images are <host>/<project>/REGISTRY/<image>: push with docker
after th auth configure-docker, or build into it with
th container deploy --repository REGISTRY/<image>.

`+rgSettingsHelp+`

Examples:
  th registry create prod --immutable-tags --keep-tags "v*"
  th registry create public-images --public --description "Images we ship"
  th registry create staging --keep-latest 10 --unpulled-days 30`)
	var f rgSettingFlags
	f.add(fs, true)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th registry create REGISTRY")
	}
	in := rgSettings{Name: fs.Args()[0]}
	f.fill(fs, &in)
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, pname, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	in.ProjectID = pid
	var created rgRegistry
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/run/registries", in, &created)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	c.info("Created registry [%s] in project [%s].", created.Name, pname)
	fmt.Fprintf(c.app.Stdout, "Push to: %s/<image>:<tag>\n", dash(created.Path))
	return nil
}

func registryDescribe(c *cmd, args []string) error {
	fs := newFlagSet("registry describe", `Usage: th registry describe REGISTRY

Shows a registry's settings and cleanup policy, and its repositories.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th registry describe REGISTRY")
	}
	cl, _, reg, err := c.findRegistry(fs.Args()[0])
	if err != nil {
		return err
	}
	var d rgDetail
	raw, err := cl.Do(c.ctx, http.MethodGet, rgPath(reg.ID), nil, &d)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	if d.Registry.ID == "" {
		d.Registry = reg
	}
	c.printRegistry(d.Registry)
	fmt.Fprintln(c.app.Stdout)
	if len(d.Repositories) == 0 {
		fmt.Fprintln(c.app.Stdout, "No repositories yet.")
		return nil
	}
	rows := [][]string{}
	for _, r := range d.Repositories {
		rows = append(rows, []string{r.Name, dash(strings.Join(r.Tags, ",")), strconv.Itoa(r.Images), humanBytes(r.SizeBytes),
			strconv.FormatInt(r.Pulls, 10), shortTime(r.UpdatedAt)})
	}
	c.table([]string{"REPOSITORY", "TAGS", "IMAGES", "SIZE", "PULLS", "UPDATED"}, rows)
	return nil
}

func registryUpdate(c *cmd, args []string) error {
	fs := newFlagSet("registry update", `Usage: th registry update REGISTRY [--description TEXT] [--public|--no-public]
       [--immutable-tags|--no-immutable-tags] [--keep-latest N]
       [--untagged-days N] [--unpulled-days N] [--keep-tags PATTERNS|none]

Changes a registry's settings; settings left out stay as they are. To see
what a new cleanup policy would remove before saving it:
th registry cleanup REGISTRY --preview with the same flags.

`+rgSettingsHelp+`

Examples:
  th registry update prod --immutable-tags
  th registry update default --keep-latest 20 --keep-tags "v*,prod"
  th registry update staging --no-public --keep-tags none`)
	var f rgSettingFlags
	f.add(fs, true)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th registry update REGISTRY")
	}
	var in rgSettings
	f.fill(fs, &in)
	if in.Description == nil && in.Public == nil && in.ImmutableTags == nil && !in.policySet() {
		return errors.New("nothing to change: pass --description, --public, --immutable-tags, --keep-latest, --untagged-days, --unpulled-days or --keep-tags")
	}
	cl, _, reg, err := c.findRegistry(fs.Args()[0])
	if err != nil {
		return err
	}
	var updated rgRegistry
	raw, err := cl.Do(c.ctx, http.MethodPatch, rgPath(reg.ID), in, &updated)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	c.info("Updated registry [%s].", reg.Name)
	c.printRegistry(updated)
	return nil
}

func registryDelete(c *cmd, args []string) error {
	fs := newFlagSet("registry delete", `Usage: th registry delete REGISTRY [--quiet]

Deletes a registry with all its repositories, images and access tokens.
The default registry cannot be deleted, nor a registry a Container runs
an image from. --quiet skips the prompt.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th registry delete REGISTRY")
	}
	cl, _, reg, err := c.findRegistry(fs.Args()[0])
	if err != nil {
		return err
	}
	if reg.IsDefault {
		return errors.New("the default registry cannot be deleted: every build stores its image there. Delete its images instead: th registry images delete IMAGE")
	}
	question := fmt.Sprintf("Delete registry %s with all its repositories, images and access tokens?", reg.Name)
	if reg.Images > 0 {
		question = fmt.Sprintf("Delete registry %s with its %d images (%s) and access tokens?", reg.Name, reg.Images, humanBytes(reg.SizeBytes))
	}
	ok, err := c.confirmNo(question)
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, rgPath(reg.ID), nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": reg.Name, "id": reg.ID, "deleted": true})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted registry [%s].\n", reg.Name)
	return nil
}

// ---- cleanup ----

func rgReason(reason string, r rgRegistry, pol rgSettings) string {
	pick := func(p *int, saved int) int {
		if p != nil {
			return *p
		}
		return saved
	}
	switch reason {
	case "replaced":
		return "replaced (no revision keeps it)"
	case "untagged":
		days := pick(pol.CleanupUntaggedDays, r.CleanupUntaggedDays)
		if days <= 0 {
			days = 7
		}
		return fmt.Sprintf("untagged > %d days", days)
	case "keepLatest":
		return fmt.Sprintf("older than the newest %d", pick(pol.CleanupKeepLatest, r.CleanupKeepLatest))
	case "unpulled":
		return fmt.Sprintf("not pulled for %d days", pick(pol.CleanupUnpulledDays, r.CleanupUnpulledDays))
	}
	return dash(reason)
}

func (c *cmd) cleanupTable(reg rgRegistry, pol rgSettings, res rgCleanup) {
	rows := [][]string{}
	for _, it := range res.Items {
		repo := it.Repository
		if !reg.IsDefault {
			repo = reg.Name + "/" + repo
		}
		rows = append(rows, []string{repo, shortDigest(it.Digest), dash(strings.Join(it.Tags, ",")), humanBytes(it.SizeBytes),
			shortTime(it.CreatedAt), rgReason(it.Reason, reg, pol)})
	}
	c.table([]string{"REPOSITORY", "DIGEST", "TAGS", "SIZE", "CREATED", "REASON"}, rows)
	if res.Images > len(res.Items) {
		fmt.Fprintf(c.app.Stdout, "… and %d more\n", res.Images-len(res.Items))
	}
}

func registryCleanup(c *cmd, args []string) error {
	var preview, run bool
	fs := newFlagSet("registry cleanup", `Usage: th registry cleanup REGISTRY --preview|--run [--keep-latest N]
       [--untagged-days N] [--unpulled-days N] [--keep-tags PATTERNS|none]

Every registry's cleanup policy runs by itself every 10 minutes.
--preview lists what the saved policy removes now; with policy flags it
lists what that policy would remove, without saving it (save it with
th registry update). --run applies the saved policy now and asks first
(--quiet skips the prompt).

`+rgSettingsHelp+`

Examples:
  th registry cleanup default --preview
  th registry cleanup default --preview --keep-latest 5 --unpulled-days 30
  th registry cleanup staging --run`)
	fs.Bool(&preview, "preview", "List what would be removed; nothing is removed")
	fs.Bool(&run, "run", "Apply the saved policy now")
	var f rgSettingFlags
	f.add(fs, false)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th registry cleanup REGISTRY --preview|--run")
	}
	if preview == run {
		return errors.New("pass either --preview or --run")
	}
	var pol rgSettings
	f.fill(fs, &pol)
	if run && pol.policySet() {
		return errors.New("--run applies the saved policy: save the new one first with th registry update, or look at it with --preview")
	}
	cl, _, reg, err := c.findRegistry(fs.Args()[0])
	if err != nil {
		return err
	}
	var plan rgCleanup
	raw, err := cl.Do(c.ctx, http.MethodPost, rgPath(reg.ID, "cleanup", "preview"), pol, &plan)
	if err != nil {
		return err
	}
	if preview {
		if c.json() {
			return c.printJSON(raw)
		}
		if plan.Images == 0 {
			fmt.Fprintf(c.app.Stdout, "Cleanup of registry [%s] would remove nothing now.\n", reg.Name)
			return nil
		}
		c.cleanupTable(reg, pol, plan)
		fmt.Fprintf(c.app.Stdout, "\nCleanup of registry [%s] would remove %d images (%s).\n", reg.Name, plan.Images, humanBytes(plan.SizeBytes))
		return nil
	}
	if plan.Images == 0 {
		if c.json() {
			return c.printJSON(rgCleanup{Items: []rgCleanupItem{}})
		}
		fmt.Fprintf(c.app.Stdout, "Nothing to remove from registry [%s].\n", reg.Name)
		return nil
	}
	if !c.json() {
		c.cleanupTable(reg, pol, plan)
		fmt.Fprintln(c.app.Stdout)
	}
	ok, err := c.confirmNo(fmt.Sprintf("Remove %d images (%s) from registry %s? This cannot be undone.", plan.Images, humanBytes(plan.SizeBytes), reg.Name))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	var done rgCleanup
	raw, err = cl.Do(c.ctx, http.MethodPost, rgPath(reg.ID, "cleanup", "run"), nil, &done)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Removed %d images (%s) from registry [%s].\n", done.Images, humanBytes(done.SizeBytes), reg.Name)
	return nil
}

// ---- images ----

const rgRepoHelp = `REPOSITORY is IMAGE for the default registry (where builds land) or
REGISTRY/IMAGE for another one, e.g. api or prod/api.`

func registryImagesList(c *cmd, args []string) error {
	fs := newFlagSet("registry images list", `Usage: th registry images list REPOSITORY

Lists a repository's images, newest first. `+rgRepoHelp+` USED BY
names the Containers running an image; KEPT=yes means a Container runs it
or a revision keeps it for rolling back, so cleanup leaves it.
To list a registry's repositories: th registry describe REGISTRY.

Examples:
  th registry images list api
  th registry images list prod/api`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th registry images list REPOSITORY")
	}
	cl, pname, o, _, err := c.registryOverview()
	if err != nil {
		return err
	}
	ref, err := o.parseRef(fs.Args()[0], pname)
	if err != nil {
		return err
	}
	if ref.tag != "" || ref.digest != "" {
		return fmt.Errorf("give the repository without a tag or digest: th registry images list %s", ref.repository())
	}
	imgs, raw, err := c.rgImages(cl, ref)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, im := range imgs {
		kept := "no"
		if im.Kept || len(im.UsedBy) > 0 {
			kept = "yes"
		}
		rows = append(rows, []string{shortDigest(im.Digest), dash(strings.Join(im.Tags, ",")), humanBytes(im.SizeBytes),
			strconv.FormatInt(im.Pulls, 10), shortTime(im.CreatedAt), dash(strings.Join(im.UsedBy, ",")), kept})
	}
	c.table([]string{"DIGEST", "TAGS", "SIZE", "PULLS", "CREATED", "USED BY", "KEPT"}, rows)
	return nil
}

func registryImagesDelete(c *cmd, args []string) error {
	fs := newFlagSet("registry images delete", `Usage: th registry images delete IMAGE [--quiet]

IMAGE is REPOSITORY:TAG (removes only the tag), REPOSITORY@sha256:DIGEST
or REPOSITORY@<first 7+ hex of the digest> (removes the image and all its
tags), or the full <host>/<project>/REPOSITORY[:TAG|@DIGEST].
`+rgRepoHelp+`
An image a Container runs cannot be deleted. --quiet skips the prompt.

Examples:
  th registry images delete prod/api:v1.3
  th registry images delete api@3f2a9c1`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th registry images delete IMAGE")
	}
	cl, pname, o, _, err := c.registryOverview()
	if err != nil {
		return err
	}
	ref, err := o.parseRef(fs.Args()[0], pname)
	if err != nil {
		return err
	}
	if ref.tag == "" && ref.digest == "" {
		r := ref.repository()
		return fmt.Errorf("give a tag or a digest: th registry images delete %s:TAG or %s@DIGEST (see: th registry images list %s)", r, r, r)
	}
	repoQ := "?repository=" + q(ref.image)
	if ref.tag != "" {
		ok, err := c.confirmNo(fmt.Sprintf("Delete image %s? Only the tag is removed; the image stays under its digest and other tags.", ref))
		if err != nil {
			return err
		}
		if !ok {
			return &exitError{code: 1, msg: "Aborted."}
		}
		if _, err := cl.Do(c.ctx, http.MethodDelete, rgPath(ref.reg.ID, "images", ref.tag)+repoQ, nil, nil); err != nil {
			return err
		}
		if c.json() {
			return c.printJSON(map[string]any{"registry": ref.reg.Name, "repository": ref.image, "tag": ref.tag, "deleted": true})
		}
		fmt.Fprintf(c.app.Stdout, "Deleted tag [%s].\n", ref)
		return nil
	}
	img, err := c.resolveDigest(cl, &ref)
	if err != nil {
		return err
	}
	question := fmt.Sprintf("Delete image %s?", ref)
	if len(img.Tags) > 0 {
		question += fmt.Sprintf(" Its tags go with it: %s.", strings.Join(img.Tags, ", "))
	}
	if len(img.UsedBy) == 0 && img.Kept {
		question += " A revision keeps it for rolling back; after this that revision cannot be served again."
	}
	ok, err := c.confirmNo(question)
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, rgPath(ref.reg.ID, "images", ref.digest)+repoQ, nil, nil); err != nil {
		return err
	}
	tags := img.Tags
	if tags == nil {
		tags = []string{}
	}
	if c.json() {
		return c.printJSON(map[string]any{"registry": ref.reg.Name, "repository": ref.image, "digest": ref.digest, "tags": tags, "deleted": true})
	}
	if len(tags) > 0 {
		fmt.Fprintf(c.app.Stdout, "Deleted image [%s] and its tags: %s.\n", ref, strings.Join(tags, ", "))
	} else {
		fmt.Fprintf(c.app.Stdout, "Deleted image [%s].\n", ref)
	}
	return nil
}

// sourceRef parses an image given by tag or digest and resolves a short
// digest.
func (c *cmd) sourceRef(cl *api.Client, o rgOverview, pname, s, usage string) (rgRef, error) {
	ref, err := o.parseRef(s, pname)
	if err != nil {
		return ref, err
	}
	if ref.tag == "" && ref.digest == "" {
		return ref, fmt.Errorf("give the image's tag or digest, e.g. %s:latest or %s@DIGEST (%s)", ref.repository(), ref.repository(), usage)
	}
	if ref.digest != "" && !fullDigestRE.MatchString(ref.digest) {
		if _, err := c.resolveDigest(cl, &ref); err != nil {
			return ref, err
		}
	}
	return ref, nil
}

// srcPathRef is the tag or digest the API takes for a source image.
func (r rgRef) srcPathRef() string {
	if r.digest != "" {
		return r.digest
	}
	return r.tag
}

func registryImagesTag(c *cmd, args []string) error {
	fs := newFlagSet("registry images tag", `Usage: th registry images tag IMAGE TAG

Points TAG at an image of the same repository, e.g. to promote a build to
prod or name it v1.4.0. IMAGE is REPOSITORY:TAG or REPOSITORY@DIGEST
(7+ hex characters are enough). `+rgRepoHelp+`
In a registry with immutable tags an existing tag cannot move (latest
excepted); a tag a Container runs by name cannot move either.

Examples:
  th registry images tag api:latest v1.4.0
  th registry images tag prod/api@3f2a9c1 stable`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th registry images tag IMAGE TAG")
	}
	tag := fs.Args()[1]
	if strings.ContainsAny(tag, ":@/") {
		return fmt.Errorf("TAG is only the tag, e.g. v1.4.0, not %q (to copy into another repository: th registry images copy SRC DST)", tag)
	}
	cl, pname, o, _, err := c.registryOverview()
	if err != nil {
		return err
	}
	ref, err := c.sourceRef(cl, o, pname, fs.Args()[0], "th registry images list "+fs.Args()[0])
	if err != nil {
		return err
	}
	var res struct {
		Image string `json:"image"`
	}
	raw, err := cl.Do(c.ctx, http.MethodPost, rgPath(ref.reg.ID, "images", ref.srcPathRef(), "tags")+"?repository="+q(ref.image), map[string]string{"tag": tag}, &res)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Tagged [%s] as [%s].\n", ref, tag)
	if res.Image != "" {
		fmt.Fprintf(c.app.Stdout, "Image: %s\n", res.Image)
	}
	return nil
}

func registryImagesCopy(c *cmd, args []string) error {
	fs := newFlagSet("registry images copy", `Usage: th registry images copy SRC DST

Copies an image into another repository or registry of the Project, e.g.
to promote it from staging to prod. SRC is REPOSITORY:TAG or
REPOSITORY@DIGEST; DST is REPOSITORY[:TAG]. `+rgRepoHelp+`
Without a tag in DST the copy takes SRC's tag, or only its digest when
SRC is given by digest. Layers are shared, not stored again, so a copy
adds nothing to the storage bill.

Examples:
  th registry images copy staging/api:v1.4.0 prod/api
  th registry images copy api@3f2a9c1 prod/api:stable`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th registry images copy SRC DST")
	}
	cl, pname, o, _, err := c.registryOverview()
	if err != nil {
		return err
	}
	src, err := c.sourceRef(cl, o, pname, fs.Args()[0], "th registry images copy SRC DST")
	if err != nil {
		return err
	}
	dst, err := o.parseRef(fs.Args()[1], pname)
	if err != nil {
		return err
	}
	if dst.digest != "" {
		return errors.New("DST takes a tag, not a digest: the copy keeps SRC's digest")
	}
	if dst.tag == "" {
		dst.tag = src.tag
	}
	body := map[string]string{"registryId": dst.reg.ID, "repository": dst.image, "tag": dst.tag}
	var res struct {
		Image string `json:"image"`
	}
	raw, err := cl.Do(c.ctx, http.MethodPost, rgPath(src.reg.ID, "images", src.srcPathRef(), "copy")+"?repository="+q(src.image), body, &res)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Copied [%s] to [%s].\n", src, dst)
	if res.Image != "" {
		fmt.Fprintf(c.app.Stdout, "Image: %s\n", res.Image)
	}
	return nil
}

// ---- access tokens ----

func (c *cmd) rgTokens(cl *api.Client, reg rgRegistry) ([]rgToken, []byte, error) {
	var tokens []rgToken
	raw, err := cl.Do(c.ctx, http.MethodGet, rgPath(reg.ID, "tokens"), nil, &tokens)
	return tokens, raw, err
}

func registryTokensList(c *cmd, args []string) error {
	fs := newFlagSet("registry tokens list", `Usage: th registry tokens list REGISTRY

Lists a registry's access tokens. A token is shown once, when it is
created; HINT is its last characters, to tell tokens apart.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th registry tokens list REGISTRY")
	}
	cl, _, reg, err := c.findRegistry(fs.Args()[0])
	if err != nil {
		return err
	}
	tokens, raw, err := c.rgTokens(cl, reg)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	if len(tokens) == 0 {
		c.info("Listed 0 items.")
		return nil
	}
	rows := [][]string{}
	for _, t := range tokens {
		expires := "never"
		if t.ExpiresAt != "" {
			expires = shortTime(t.ExpiresAt)
		}
		rows = append(rows, []string{t.ID, t.Name, t.Permission, "…" + t.Hint, expires, shortTime(t.LastUsedAt), shortTime(t.CreatedAt)})
	}
	c.table([]string{"ID", "NAME", "PERMISSION", "HINT", "EXPIRES", "LAST USED", "CREATED"}, rows)
	return nil
}

func registryTokensCreate(c *cmd, args []string) error {
	var name, permission string
	var days int
	fs := newFlagSet("registry tokens create", `Usage: th registry tokens create REGISTRY --name NAME [--permission read|write]
       [--expires-in-days N]

Creates an access token that signs docker, CI or a server elsewhere into
this registry alone: read pulls, write pushes and pulls. The token is
printed once on standard output; store it now. Sign in with it as user th:
  echo "$TOKEN" | docker login <host> -u th --password-stdin

Examples:
  th registry tokens create prod --name github-actions --permission write
  th registry tokens create prod --name edge-server --expires-in-days 90`)
	fs.String(&name, "name", "Token name, e.g. github-actions (1-60 characters)")
	fs.String(&permission, "permission", "Token permission: read pulls, write pushes and pulls (default read)")
	fs.Int(&days, "expires-in-days", "Days until the token stops working (1-730, 0 = never)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 || name == "" {
		return errors.New("usage: th registry tokens create REGISTRY --name NAME")
	}
	if permission == "" {
		permission = "read"
	}
	if permission != "read" && permission != "write" {
		return errors.New("--permission must be read or write")
	}
	cl, o, reg, err := c.findRegistry(fs.Args()[0])
	if err != nil {
		return err
	}
	var res struct {
		Token    rgToken `json:"token"`
		Secret   string  `json:"secret"`
		Username string  `json:"username"`
	}
	raw, err := cl.Do(c.ctx, http.MethodPost, rgPath(reg.ID, "tokens"), map[string]any{"name": name, "permission": permission, "expiresInDays": days}, &res)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	user := res.Username
	if user == "" {
		user = "th"
	}
	c.info("Created access token [%s] (%s) for registry [%s]. It is shown once; store it now.", name, permission, reg.Name)
	c.info("Sign in: echo \"$TOKEN\" | docker login %s -u %s --password-stdin", dash(o.Host), user)
	fmt.Fprintln(c.app.Stdout, res.Secret)
	return nil
}

func registryTokensRevoke(c *cmd, args []string) error {
	fs := newFlagSet("registry tokens revoke", `Usage: th registry tokens revoke REGISTRY TOKEN [--quiet]

Revokes an access token, by ID or name (see th registry tokens list); it
stops working at once. --quiet skips the prompt.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th registry tokens revoke REGISTRY TOKEN")
	}
	cl, _, reg, err := c.findRegistry(fs.Args()[0])
	if err != nil {
		return err
	}
	tokens, _, err := c.rgTokens(cl, reg)
	if err != nil {
		return err
	}
	want := fs.Args()[1]
	var found []rgToken
	for _, t := range tokens {
		if t.ID == want {
			found = []rgToken{t}
			break
		}
		if t.Name == want {
			found = append(found, t)
		}
	}
	switch len(found) {
	case 0:
		return fmt.Errorf("access token %q not found in registry [%s]. Run: th registry tokens list %s", want, reg.Name, reg.Name)
	case 1:
	default:
		return fmt.Errorf("%d access tokens of registry [%s] are named %s; give the ID (th registry tokens list %s)", len(found), reg.Name, want, reg.Name)
	}
	t := found[0]
	ok, err := c.confirmNo(fmt.Sprintf("Revoke access token %s (…%s) of registry %s? Anything using it can no longer sign in.", t.Name, t.Hint, reg.Name))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, rgPath(reg.ID, "tokens", t.ID), nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"registry": reg.Name, "id": t.ID, "name": t.Name, "revoked": true})
	}
	fmt.Fprintf(c.app.Stdout, "Revoked access token [%s].\n", t.Name)
	return nil
}
