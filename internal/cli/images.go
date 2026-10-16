package cli

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

var (
	fullDigestRE  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	shortDigestRE = regexp.MustCompile(`^[a-f0-9]{7,64}$`)
)

func registryPath(pid string, parts ...string) string {
	p := "/api/v1/run/registry"
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p + "?projectId=" + q(pid)
}

func imagesList(c *cmd, args []string) error {
	fs := newFlagSet("container images list", `Usage: th container images list [REPOSITORY]

Without REPOSITORY lists the Project's registry repositories and how much
of the registry quota they use. With REPOSITORY lists its images, newest
first. KEPT=yes means a Container runs the image or a revision keeps it
for rolling back.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return errors.New("usage: th container images list [REPOSITORY]")
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	if len(fs.Args()) == 1 {
		repo := fs.Args()[0]
		imgs, raw, err := c.registryImages(cl, pid, repo)
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
				shortTime(im.CreatedAt), dash(strings.Join(im.UsedBy, ",")), kept})
		}
		c.table([]string{"DIGEST", "TAGS", "SIZE", "CREATED", "USED BY", "KEPT"}, rows)
		return nil
	}
	var reg api.Registry
	raw, err := cl.Do(c.ctx, http.MethodGet, registryPath(pid), nil, &reg)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	usage := "used " + humanBytes(reg.UsedBytes)
	if reg.QuotaBytes > 0 {
		pct := (reg.UsedBytes*100 + reg.QuotaBytes/2) / reg.QuotaBytes
		usage += fmt.Sprintf(" of %s (%d%%)", quotaGB(reg.QuotaBytes), pct)
	}
	fmt.Fprintf(c.app.Stdout, "Registry %s/%s · %s\n\n", reg.Host, reg.Namespace, usage)
	rows := [][]string{}
	for _, r := range reg.Repositories {
		rows = append(rows, []string{r.Name, dash(strings.Join(r.Tags, ",")), humanBytes(r.SizeBytes), shortTime(r.UpdatedAt)})
	}
	c.table([]string{"REPOSITORY", "TAGS", "SIZE", "UPDATED"}, rows)
	return nil
}

func (c *cmd) registryImages(cl *api.Client, pid, repo string) ([]api.RegistryImage, []byte, error) {
	var imgs []api.RegistryImage
	raw, err := cl.Do(c.ctx, http.MethodGet, registryPath(pid, repo, "images"), nil, &imgs)
	return imgs, raw, err
}

func imagesDelete(c *cmd, args []string) error {
	fs := newFlagSet("container images delete", `Usage: th container images delete IMAGE [--quiet]

IMAGE is REPOSITORY:TAG (removes only the tag), REPOSITORY@sha256:DIGEST
or REPOSITORY@<first 7+ hex of the digest> (removes the image and all its
tags), or the full <host>/<namespace>/REPOSITORY[:TAG|@DIGEST].
An image a Container runs cannot be deleted. --quiet skips the prompt.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th container images delete IMAGE")
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	ref, err := c.parseImageRef(cl, pid, fs.Args()[0])
	if err != nil {
		return err
	}
	if ref.tag != "" {
		name := ref.repo + ":" + ref.tag
		ok, err := c.confirmNo(fmt.Sprintf("Delete image %s? Only the tag is removed; the image stays under its digest and other tags.", name))
		if err != nil {
			return err
		}
		if !ok {
			return &exitError{code: 1, msg: "Aborted."}
		}
		if _, err := cl.Do(c.ctx, http.MethodDelete, registryPath(pid, ref.repo, "images", ref.tag), nil, nil); err != nil {
			return err
		}
		if c.json() {
			return c.printJSON(map[string]any{"repository": ref.repo, "tag": ref.tag, "deleted": true})
		}
		fmt.Fprintf(c.app.Stdout, "Deleted tag [%s].\n", name)
		return nil
	}
	// By digest: find the image (also resolves a short digest).
	imgs, _, err := c.registryImages(cl, pid, ref.repo)
	if err != nil {
		return err
	}
	img, err := matchDigest(imgs, ref.repo, ref.digest, "th container images list "+ref.repo)
	if err != nil {
		return err
	}
	name := ref.repo + "@" + img.Digest
	question := fmt.Sprintf("Delete image %s?", name)
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
	if _, err := cl.Do(c.ctx, http.MethodDelete, registryPath(pid, ref.repo, "images", img.Digest), nil, nil); err != nil {
		return err
	}
	tags := img.Tags
	if tags == nil {
		tags = []string{}
	}
	if c.json() {
		return c.printJSON(map[string]any{"repository": ref.repo, "digest": img.Digest, "tags": tags, "deleted": true})
	}
	if len(tags) > 0 {
		fmt.Fprintf(c.app.Stdout, "Deleted image [%s] and its tags: %s.\n", name, strings.Join(tags, ", "))
	} else {
		fmt.Fprintf(c.app.Stdout, "Deleted image [%s].\n", name)
	}
	return nil
}

func imagesDeleteRepository(c *cmd, args []string) error {
	fs := newFlagSet("container images delete-repository", `Usage: th container images delete-repository REPOSITORY [--quiet]

Deletes a repository with all its images and tags. It cannot be deleted
while a Container runs one of its images. --quiet skips the prompt.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th container images delete-repository REPOSITORY")
	}
	repo := fs.Args()[0]
	if strings.ContainsAny(repo, ":@/") {
		return fmt.Errorf("give only the repository name, not %q (to delete one image: th container images delete IMAGE)", repo)
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	ok, err := c.confirmNo(fmt.Sprintf("Delete repository %s with all its images and tags?", repo))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, registryPath(pid, repo), nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"repository": repo, "deleted": true})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted repository [%s].\n", repo)
	return nil
}

type imageRef struct {
	repo, tag, digest string // digest may be a short hex prefix
}

// parseImageRef reads repo:tag, repo@digest or the full
// <host>/<namespace>/repo form (whose namespace must be this Project's).
func (c *cmd) parseImageRef(cl *api.Client, pid, s string) (imageRef, error) {
	var r imageRef
	orig := s
	if i := strings.LastIndex(s, "/"); i >= 0 {
		head := s[:i]
		s = s[i+1:]
		ns := head
		if j := strings.LastIndex(head, "/"); j >= 0 {
			ns = head[j+1:]
		}
		var reg api.Registry
		if _, err := cl.Do(c.ctx, http.MethodGet, registryPath(pid), nil, &reg); err != nil {
			return r, err
		}
		if ns != reg.Namespace {
			return r, fmt.Errorf("%s is not in this Project's registry (%s/%s); pass --project for the Project that owns it", orig, reg.Host, reg.Namespace)
		}
	}
	if repo, d, ok := strings.Cut(s, "@"); ok {
		r.repo = repo
		d = strings.ToLower(d)
		if !fullDigestRE.MatchString(d) {
			hex := strings.TrimPrefix(d, "sha256:")
			if !shortDigestRE.MatchString(hex) {
				return r, fmt.Errorf("%q is not a digest: use sha256:<64 hex> or at least the first 7 hex characters", d)
			}
			d = hex
		}
		r.digest = d
	} else if repo, tag, ok := strings.Cut(s, ":"); ok {
		r.repo, r.tag = repo, tag
		if tag == "" {
			return r, fmt.Errorf("%q has an empty tag", orig)
		}
	} else {
		return r, fmt.Errorf("give a tag or a digest: th container images delete %s:TAG or %s@DIGEST (see: th container images list %s).\nTo delete the whole repository: th container images delete-repository %s", s, s, s, s)
	}
	if r.repo == "" {
		return r, fmt.Errorf("%q has no repository name", orig)
	}
	return r, nil
}

// matchDigest finds a full digest or a unique hex prefix of one; listCmd
// is the command that lists repo's images, for the error messages.
func matchDigest(imgs []api.RegistryImage, repo, want, listCmd string) (*api.RegistryImage, error) {
	if fullDigestRE.MatchString(want) {
		for i := range imgs {
			if imgs[i].Digest == want {
				return &imgs[i], nil
			}
		}
		return nil, fmt.Errorf("image %s@%s not found. List them with: %s", repo, want, listCmd)
	}
	var found []*api.RegistryImage
	for i := range imgs {
		if strings.HasPrefix(strings.TrimPrefix(imgs[i].Digest, "sha256:"), want) {
			found = append(found, &imgs[i])
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no image of %s has a digest starting with %s. List them with: %s", repo, want, listCmd)
	case 1:
		return found[0], nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "digest %s matches %d images of %s; give more characters:", want, len(found), repo)
	for _, im := range found {
		fmt.Fprintf(&b, "\n  %s", im.Digest)
	}
	return nil, errors.New(b.String())
}

func shortDigest(d string) string {
	h := strings.TrimPrefix(d, "sha256:")
	if len(h) > 12 {
		h = h[:12]
	}
	return h
}

func quotaGB(n int64) string {
	if n%(1<<30) == 0 {
		return fmt.Sprintf("%d GB", n>>30)
	}
	return humanBytes(n)
}

// confirmNo asks "question (y/N)"; only y/yes continues. --quiet answers yes.
func (c *cmd) confirmNo(question string) (bool, error) {
	if c.g.quiet {
		return true, nil
	}
	fmt.Fprintf(c.app.Stderr, "%s (y/N) ", question)
	line, err := bufio.NewReader(c.app.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		fmt.Fprintln(c.app.Stderr)
		return false, errors.New("no answer on standard input; pass --quiet to skip the prompt")
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}
