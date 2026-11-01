package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

// th vault: secrets and encryption keys, through the Console API (/api/v1/vaults)
// as whoever signed in. API keys are refused there on purpose: a program uses a
// service account instead. Purging asks for the password again, so it stays in
// the Console.

func init() {
	register(map[string]handler{
		"vault list":     vaultList,
		"vault describe": vaultDescribe,
		"vault create":   vaultCreate,
		"vault delete":   vaultDelete,
		"vault recover":  vaultRecover,
		"vault activity": vaultActivity,

		"vault secret list":     vaultSecretList,
		"vault secret versions": vaultSecretVersions,
		"vault secret get":      vaultSecretGet,
		"vault secret set":      vaultSecretSet,
		"vault secret enable":   vaultSecretEnable,
		"vault secret disable":  vaultSecretDisable,
		"vault secret delete":   vaultSecretDelete,
		"vault secret recover":  vaultSecretRecover,

		"vault key list":       vaultKeyList,
		"vault key describe":   vaultKeyDescribe,
		"vault key create":     vaultKeyCreate,
		"vault key rotate":     vaultKeyRotate,
		"vault key enable":     vaultKeyEnable,
		"vault key disable":    vaultKeyDisable,
		"vault key delete":     vaultKeyDelete,
		"vault key recover":    vaultKeyRecover,
		"vault key public-key": vaultKeyPublic,
		"vault key encrypt":    vaultKeyEncrypt,
		"vault key decrypt":    vaultKeyDecrypt,
		"vault key sign":       vaultKeySign,
		"vault key verify":     vaultKeyVerify,
	},
		"vault list | describe NAME | create NAME | delete NAME | recover NAME | activity NAME",
		"vault secret list VAULT | versions VAULT NAME | get VAULT NAME | set VAULT NAME [VALUE|-] | enable|disable VAULT NAME VERSION | delete|recover VAULT NAME",
		"vault key list VAULT | describe|rotate|enable|disable|delete|recover|public-key VAULT NAME | create VAULT NAME --type TYPE",
		"vault key encrypt|decrypt|sign VAULT NAME [VALUE|-] | verify VAULT NAME [VALUE|-] --signature S --version N",
	)
}

type vVault struct {
	ID              string `json:"id"`
	ProjectID       string `json:"projectId"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	SoftDeleteDays  int    `json:"softDeleteDays"`
	PurgeProtection bool   `json:"purgeProtection"`
	CreatedAt       string `json:"createdAt"`
	DeletedAt       string `json:"deletedAt,omitempty"`
	PurgeAt         string `json:"purgeAt,omitempty"`
	Secrets         int    `json:"secrets"`
	Keys            int    `json:"keys"`
}

type vSecretVersion struct {
	Version     int    `json:"version"`
	Enabled     bool   `json:"enabled"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	DestroyedAt string `json:"destroyedAt,omitempty"`
	CreatedAt   string `json:"createdAt"`
}

type vSecret struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	LastVersion int              `json:"lastVersion"`
	UpdatedAt   string           `json:"updatedAt"`
	DeletedAt   string           `json:"deletedAt,omitempty"`
	PurgeAt     string           `json:"purgeAt,omitempty"`
	Versions    []vSecretVersion `json:"versions"`
}

type vKeyVersion struct {
	Version     int    `json:"version"`
	Enabled     bool   `json:"enabled"`
	PublicKey   string `json:"publicKey,omitempty"`
	DestroyAt   string `json:"destroyAt,omitempty"`
	DestroyedAt string `json:"destroyedAt,omitempty"`
	CreatedAt   string `json:"createdAt"`
}

type vKey struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	Type           string        `json:"type"`
	Purpose        string        `json:"purpose"`
	Enabled        bool          `json:"enabled"`
	RotationDays   int           `json:"rotationDays"`
	NextRotationAt string        `json:"nextRotationAt,omitempty"`
	DeletedAt      string        `json:"deletedAt,omitempty"`
	PurgeAt        string        `json:"purgeAt,omitempty"`
	Versions       []vKeyVersion `json:"versions"`
}

type vEvent struct {
	At         string `json:"at"`
	ActorKind  string `json:"actorKind"`
	ActorName  string `json:"actorName"`
	SourceIP   string `json:"sourceIp"`
	Action     string `json:"action"`
	ObjectName string `json:"objectName"`
	Version    int    `json:"version"`
	Allowed    bool   `json:"allowed"`
	Count      int64  `json:"count"`
}

// findVault resolves the Project and the vault named name, deleted or not.
func (c *cmd) findVault(name string) (*api.Client, vVault, error) {
	cl, err := c.client(true)
	if err != nil {
		return nil, vVault{}, err
	}
	pid, pname, err := c.resolveProject(cl)
	if err != nil {
		return nil, vVault{}, err
	}
	var rows []vVault
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/vaults?projectId="+q(pid), nil, &rows); err != nil {
		return nil, vVault{}, err
	}
	for _, v := range rows {
		if v.Name == name || v.ID == name {
			return cl, v, nil
		}
	}
	return nil, vVault{}, fmt.Errorf("vault %q not found in project [%s]. Run: th vault list", name, pname)
}

// liveVault is findVault for what a deleted vault cannot do.
func (c *cmd) liveVault(name string) (*api.Client, vVault, error) {
	cl, v, err := c.findVault(name)
	if err == nil && v.DeletedAt != "" {
		return nil, v, fmt.Errorf("vault %s was deleted and is purged on %s. Recover it first: th vault recover %s", v.Name, shortDate(v.PurgeAt), v.Name)
	}
	return cl, v, err
}

func shortDate(s string) string {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Local().Format("2006-01-02")
	}
	return dash(s)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func vaultList(c *cmd, args []string) error {
	fs := newFlagSet("vault list", "Usage: th vault list\n\nLists the Project's vaults, with deleted ones waiting to be purged.")
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
	rows := []vVault{}
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/vaults?projectId="+q(pid), nil, &rows); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(rows)
	}
	if len(rows) == 0 {
		c.info("Listed 0 items.")
		return nil
	}
	table := make([][]string, 0, len(rows))
	for _, v := range rows {
		state := "active"
		if v.DeletedAt != "" {
			state = "purged " + shortDate(v.PurgeAt)
		}
		table = append(table, []string{v.Name, strconv.Itoa(v.Secrets), strconv.Itoa(v.Keys), yesNo(v.PurgeProtection), state})
	}
	c.table([]string{"NAME", "SECRETS", "KEYS", "PURGE PROTECTION", "STATE"}, table)
	return nil
}

func vaultDescribe(c *cmd, args []string) error {
	fs := newFlagSet("vault describe", "Usage: th vault describe NAME\n\nShows a vault's settings.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th vault describe NAME")
	}
	_, v, err := c.findVault(fs.Args()[0])
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(v)
	}
	w := c.app.Stdout
	fmt.Fprintf(w, "Vault %s\n\n", v.Name)
	fmt.Fprintf(w, "ID:                  %s\n", v.ID)
	fmt.Fprintf(w, "Description:         %s\n", dash(v.Description))
	fmt.Fprintf(w, "Secrets:             %d\n", v.Secrets)
	fmt.Fprintf(w, "Keys:                %d\n", v.Keys)
	fmt.Fprintf(w, "Soft delete:         %d days\n", v.SoftDeleteDays)
	fmt.Fprintf(w, "Purge protection:    %s\n", yesNo(v.PurgeProtection))
	if v.DeletedAt != "" {
		fmt.Fprintf(w, "Purged on:           %s\n", shortDate(v.PurgeAt))
	}
	return nil
}

func vaultCreate(c *cmd, args []string) error {
	fs := newFlagSet("vault create", `Usage: th vault create NAME [flags]

Creates a vault. Make one per app or environment, since access is given vault by
vault. --purge-protection cannot be turned off afterwards.`)
	var description string
	var softDays int
	var protect bool
	fs.String(&description, "description", "What the vault is for")
	fs.Int(&softDays, "soft-delete-days", "Days deleted items can be recovered (7-90, default 30)")
	fs.Bool(&protect, "purge-protection", "Nobody can purge before the soft delete period ends")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th vault create NAME")
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	in := map[string]any{"projectId": pid, "name": fs.Args()[0], "description": description, "purgeProtection": protect}
	if fs.Set("soft-delete-days") {
		in["softDeleteDays"] = softDays
	}
	var v vVault
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults", in, &v); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(v)
	}
	fmt.Fprintf(c.app.Stdout, "Created vault [%s].\n", v.Name)
	return nil
}

func vaultDelete(c *cmd, args []string) error {
	fs := newFlagSet("vault delete", "Usage: th vault delete NAME [--quiet]\n\nDeletes a vault. Programs lose access at once. It can be recovered until its soft delete period ends, and is then purged.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th vault delete NAME")
	}
	cl, v, err := c.liveVault(fs.Args()[0])
	if err != nil {
		return err
	}
	ok, err := c.confirmNo(fmt.Sprintf("Delete vault %s? Programs lose access at once. You can recover it within %d days.", v.Name, v.SoftDeleteDays))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/vaults/"+v.ID, nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": v.Name, "id": v.ID, "deleted": true})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted vault [%s]. Recover it with: th vault recover %s\n", v.Name, v.Name)
	return nil
}

func vaultRecover(c *cmd, args []string) error {
	fs := newFlagSet("vault recover", "Usage: th vault recover NAME\n\nBrings back a deleted vault.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th vault recover NAME")
	}
	cl, v, err := c.findVault(fs.Args()[0])
	if err != nil {
		return err
	}
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/recover", map[string]any{}, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": v.Name, "id": v.ID, "recovered": true})
	}
	fmt.Fprintf(c.app.Stdout, "Recovered vault [%s].\n", v.Name)
	return nil
}

func vaultActivity(c *cmd, args []string) error {
	fs := newFlagSet("vault activity", "Usage: th vault activity NAME [--limit N]\n\nShows who did what in a vault, newest first. Uses of a key by a program are one line per hour.")
	limit := 50
	fs.Int(&limit, "limit", "Lines to show (at most 200)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th vault activity NAME")
	}
	cl, v, err := c.findVault(fs.Args()[0])
	if err != nil {
		return err
	}
	rows := []vEvent{}
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/vaults/"+v.ID+"/events?limit="+strconv.Itoa(limit), nil, &rows); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(rows)
	}
	if len(rows) == 0 {
		c.info("Listed 0 items.")
		return nil
	}
	table := make([][]string, 0, len(rows))
	for _, e := range rows {
		action := e.Action
		if !e.Allowed {
			action += " (refused)"
		}
		table = append(table, []string{e.At, dash(e.ActorName), action, dash(e.ObjectName), strconv.FormatInt(e.Count, 10), dash(e.SourceIP)})
	}
	c.table([]string{"TIME", "BY", "ACTION", "NAME", "COUNT", "IP"}, table)
	return nil
}

// ---- Secrets ----

func (c *cmd) vaultSecrets(cl *api.Client, v vVault) ([]vSecret, error) {
	rows := []vSecret{}
	_, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/vaults/"+v.ID+"/secrets", nil, &rows)
	return rows, err
}

// findVaultSecret resolves a vault and a secret in it, deleted or not.
func (c *cmd) findVaultSecret(vault, name string) (*api.Client, vVault, vSecret, error) {
	cl, v, err := c.liveVault(vault)
	if err != nil {
		return nil, v, vSecret{}, err
	}
	rows, err := c.vaultSecrets(cl, v)
	if err != nil {
		return nil, v, vSecret{}, err
	}
	for _, s := range rows {
		if s.Name == name {
			return cl, v, s, nil
		}
	}
	return nil, v, vSecret{}, fmt.Errorf("secret %q not found in vault %s. Run: th vault secret list %s", name, v.Name, v.Name)
}

func vaultSecretState(s vSecretVersion, now time.Time) string {
	switch {
	case s.DestroyedAt != "":
		return "destroyed"
	case s.ExpiresAt != "" && expired(s.ExpiresAt, now):
		return "expired"
	case !s.Enabled:
		return "disabled"
	}
	return "enabled"
}

func expired(at string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, at)
	return err == nil && !t.After(now)
}

func vaultSecretList(c *cmd, args []string) error {
	fs := newFlagSet("vault secret list", "Usage: th vault secret list VAULT\n\nLists a vault's secrets, without their values.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th vault secret list VAULT")
	}
	cl, v, err := c.liveVault(fs.Args()[0])
	if err != nil {
		return err
	}
	rows, err := c.vaultSecrets(cl, v)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(rows)
	}
	if len(rows) == 0 {
		c.info("Listed 0 items.")
		return nil
	}
	table := make([][]string, 0, len(rows))
	for _, s := range rows {
		state := "active"
		if s.DeletedAt != "" {
			state = "purged " + shortDate(s.PurgeAt)
		}
		table = append(table, []string{s.Name, strconv.Itoa(s.LastVersion), shortDate(s.UpdatedAt), state})
	}
	c.table([]string{"NAME", "LAST VERSION", "UPDATED", "STATE"}, table)
	return nil
}

func vaultSecretVersions(c *cmd, args []string) error {
	fs := newFlagSet("vault secret versions", "Usage: th vault secret versions VAULT NAME\n\nLists a secret's versions, without their values.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th vault secret versions VAULT NAME")
	}
	_, _, s, err := c.findVaultSecret(fs.Args()[0], fs.Args()[1])
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(s.Versions)
	}
	now := time.Now()
	table := make([][]string, 0, len(s.Versions))
	for _, ver := range s.Versions {
		table = append(table, []string{strconv.Itoa(ver.Version), vaultSecretState(ver, now), shortDate(ver.CreatedAt), shortDate(ver.ExpiresAt)})
	}
	c.table([]string{"VERSION", "STATE", "CREATED", "EXPIRES"}, table)
	return nil
}

func vaultSecretGet(c *cmd, args []string) error {
	fs := newFlagSet("vault secret get", `Usage: th vault secret get VAULT NAME [--version N]

Prints a secret's value exactly, with no newline added: the newest version that is
enabled and has not expired, or the one asked for. It needs Editor or higher, and
the read goes in the vault's activity.`)
	var version int
	fs.Int(&version, "version", "A version to read")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th vault secret get VAULT NAME [--version N]")
	}
	cl, v, s, err := c.findVaultSecret(fs.Args()[0], fs.Args()[1])
	if err != nil {
		return err
	}
	if s.DeletedAt != "" {
		return fmt.Errorf("secret %s was deleted. Recover it first: th vault secret recover %s %s", s.Name, v.Name, s.Name)
	}
	if !fs.Set("version") {
		now := time.Now()
		for _, ver := range s.Versions {
			if vaultSecretState(ver, now) == "enabled" && ver.Version > version {
				version = ver.Version
			}
		}
		if version == 0 {
			return fmt.Errorf("secret %s has no enabled version that has not expired", s.Name)
		}
	}
	var out struct {
		Version int    `json:"version"`
		Value   string `json:"value"`
	}
	if _, err := cl.Do(c.ctx, http.MethodPost, fmt.Sprintf("/api/v1/vaults/%s/secrets/%s/versions/%d/reveal", v.ID, s.ID, version), map[string]any{}, &out); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": s.Name, "version": out.Version, "value": out.Value})
	}
	_, err = io.WriteString(c.app.Stdout, out.Value)
	return err
}

// vaultInput reads a command's data: the argument, FILE, or standard input for "-".
func (c *cmd) vaultInput(arg *string, file string) ([]byte, error) {
	switch {
	case arg != nil && file != "":
		return nil, errors.New("give either a value or --file, not both")
	case file != "":
		return os.ReadFile(file)
	case arg == nil || *arg == "-":
		return io.ReadAll(c.app.Stdin)
	}
	return []byte(*arg), nil
}

// vaultExpiry reads YYYY-MM-DD (the end of that day here, as in the Console) or
// RFC 3339.
func vaultExpiry(s string) (string, error) {
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t.Add(24*time.Hour - time.Second).Format(time.RFC3339), nil
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return "", fmt.Errorf("--expires: use YYYY-MM-DD or RFC 3339, not %q", s)
	}
	return s, nil
}

func vaultSecretSet(c *cmd, args []string) error {
	fs := newFlagSet("vault secret set", `Usage: th vault secret set VAULT NAME [VALUE | - | --file FILE] [--expires DATE]

Adds a version with this value, making the secret when it is new. With no value it
reads standard input: prefer that or --file, since a value on the command line stays
in your shell's history.`)
	var file, expires string
	fs.String(&file, "file", "Read the value from FILE")
	fs.String(&expires, "expires", "When this version expires (YYYY-MM-DD or RFC 3339)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	a := fs.Args()
	if len(a) < 2 || len(a) > 3 {
		return errors.New("usage: th vault secret set VAULT NAME [VALUE | - | --file FILE]")
	}
	var arg *string
	if len(a) == 3 {
		arg = &a[2]
	}
	value, err := c.vaultInput(arg, file)
	if err != nil {
		return err
	}
	if len(value) == 0 {
		return errors.New("the value is empty")
	}
	in := map[string]any{"value": string(value)}
	if expires != "" {
		at, err := vaultExpiry(expires)
		if err != nil {
			return err
		}
		in["expiresAt"] = at
	}
	cl, v, err := c.liveVault(a[0])
	if err != nil {
		return err
	}
	rows, err := c.vaultSecrets(cl, v)
	if err != nil {
		return err
	}
	version := 1
	var existing *vSecret
	for i := range rows {
		if rows[i].Name == a[1] {
			existing = &rows[i]
		}
	}
	switch {
	case existing != nil && existing.DeletedAt != "":
		return fmt.Errorf("secret %s was deleted. Recover it first: th vault secret recover %s %s", existing.Name, v.Name, existing.Name)
	case existing != nil:
		var out vSecretVersion
		if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/secrets/"+existing.ID+"/versions", in, &out); err != nil {
			return err
		}
		version = out.Version
	default:
		in["name"] = a[1]
		if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/secrets", in, nil); err != nil {
			return err
		}
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": a[1], "version": version})
	}
	fmt.Fprintf(c.app.Stdout, "Set secret [%s] version %d. Use it in a variable as vault://%s/%s\n", a[1], version, v.Name, a[1])
	return nil
}

func vaultSecretSetEnabled(c *cmd, args []string, verb string, enabled bool) error {
	fs := newFlagSet("vault secret "+verb, "Usage: th vault secret "+verb+" VAULT NAME VERSION\n\nA disabled version cannot be read; programs get the newest one still enabled.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	a := fs.Args()
	if len(a) != 3 {
		return fmt.Errorf("usage: th vault secret %s VAULT NAME VERSION", verb)
	}
	version, err := strconv.Atoi(a[2])
	if err != nil || version < 1 {
		return fmt.Errorf("invalid version %q", a[2])
	}
	cl, v, s, err := c.findVaultSecret(a[0], a[1])
	if err != nil {
		return err
	}
	if _, err := cl.Do(c.ctx, http.MethodPatch, fmt.Sprintf("/api/v1/vaults/%s/secrets/%s/versions/%d", v.ID, s.ID, version), map[string]bool{"enabled": enabled}, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": s.Name, "version": version, "enabled": enabled})
	}
	fmt.Fprintf(c.app.Stdout, "Version %d of secret [%s] is now %sd.\n", version, s.Name, verb)
	return nil
}

func vaultSecretEnable(c *cmd, args []string) error {
	return vaultSecretSetEnabled(c, args, "enable", true)
}

func vaultSecretDisable(c *cmd, args []string) error {
	return vaultSecretSetEnabled(c, args, "disable", false)
}

func vaultSecretDelete(c *cmd, args []string) error {
	fs := newFlagSet("vault secret delete", "Usage: th vault secret delete VAULT NAME [--quiet]\n\nDeletes a secret. Programs lose it at once. It can be recovered until the vault's soft delete period ends, and is then purged.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th vault secret delete VAULT NAME")
	}
	cl, v, s, err := c.findVaultSecret(fs.Args()[0], fs.Args()[1])
	if err != nil {
		return err
	}
	ok, err := c.confirmNo(fmt.Sprintf("Delete secret %s? Programs lose it at once. You can recover it within %d days.", s.Name, v.SoftDeleteDays))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/vaults/"+v.ID+"/secrets/"+s.ID, nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": s.Name, "deleted": true})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted secret [%s].\n", s.Name)
	return nil
}

func vaultSecretRecover(c *cmd, args []string) error {
	fs := newFlagSet("vault secret recover", "Usage: th vault secret recover VAULT NAME\n\nBrings back a deleted secret.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th vault secret recover VAULT NAME")
	}
	cl, v, s, err := c.findVaultSecret(fs.Args()[0], fs.Args()[1])
	if err != nil {
		return err
	}
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/secrets/"+s.ID+"/recover", map[string]any{}, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": s.Name, "recovered": true})
	}
	fmt.Fprintf(c.app.Stdout, "Recovered secret [%s].\n", s.Name)
	return nil
}

// ---- Keys ----

func (c *cmd) findVaultKey(vault, name string) (*api.Client, vVault, vKey, error) {
	cl, v, err := c.liveVault(vault)
	if err != nil {
		return nil, v, vKey{}, err
	}
	rows := []vKey{}
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/vaults/"+v.ID+"/keys", nil, &rows); err != nil {
		return nil, v, vKey{}, err
	}
	for _, k := range rows {
		if k.Name == name {
			return cl, v, k, nil
		}
	}
	return nil, v, vKey{}, fmt.Errorf("key %q not found in vault %s. Run: th vault key list %s", name, v.Name, v.Name)
}

// vaultKeyArgs parses a command that takes only VAULT NAME.
func (c *cmd) vaultKeyArgs(verb, help string, args []string) (*api.Client, vVault, vKey, error) {
	fs := newFlagSet("vault key "+verb, "Usage: th vault key "+verb+" VAULT NAME\n\n"+help)
	if err := c.parse(fs, args); err != nil {
		return nil, vVault{}, vKey{}, err
	}
	if len(fs.Args()) != 2 {
		return nil, vVault{}, vKey{}, fmt.Errorf("usage: th vault key %s VAULT NAME", verb)
	}
	return c.findVaultKey(fs.Args()[0], fs.Args()[1])
}

func vaultKeyList(c *cmd, args []string) error {
	fs := newFlagSet("vault key list", "Usage: th vault key list VAULT\n\nLists a vault's keys.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th vault key list VAULT")
	}
	cl, v, err := c.liveVault(fs.Args()[0])
	if err != nil {
		return err
	}
	rows := []vKey{}
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/vaults/"+v.ID+"/keys", nil, &rows); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(rows)
	}
	if len(rows) == 0 {
		c.info("Listed 0 items.")
		return nil
	}
	table := make([][]string, 0, len(rows))
	for _, k := range rows {
		rotation, state := "off", "enabled"
		if k.RotationDays > 0 {
			rotation = strconv.Itoa(k.RotationDays) + "d"
		}
		if !k.Enabled {
			state = "disabled"
		}
		if k.DeletedAt != "" {
			state = "purged " + shortDate(k.PurgeAt)
		}
		table = append(table, []string{k.Name, k.Type, k.Purpose, rotation, state})
	}
	c.table([]string{"NAME", "TYPE", "PURPOSE", "ROTATION", "STATE"}, table)
	return nil
}

func vaultKeyDescribe(c *cmd, args []string) error {
	_, _, k, err := c.vaultKeyArgs("describe", "Shows a key and its versions.", args)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(k)
	}
	w := c.app.Stdout
	fmt.Fprintf(w, "Key %s\n\n", k.Name)
	fmt.Fprintf(w, "Type:           %s\n", k.Type)
	fmt.Fprintf(w, "Purpose:        %s\n", k.Purpose)
	fmt.Fprintf(w, "Enabled:        %s\n", yesNo(k.Enabled))
	if k.RotationDays > 0 {
		fmt.Fprintf(w, "Rotation:       every %d days, next %s\n", k.RotationDays, shortDate(k.NextRotationAt))
	} else {
		fmt.Fprintf(w, "Rotation:       off\n")
	}
	fmt.Fprintln(w)
	table := make([][]string, 0, len(k.Versions))
	for _, ver := range k.Versions {
		state := "enabled"
		switch {
		case ver.DestroyedAt != "":
			state = "destroyed"
		case ver.DestroyAt != "":
			state = "destroyed " + shortDate(ver.DestroyAt)
		case !ver.Enabled:
			state = "disabled"
		}
		table = append(table, []string{strconv.Itoa(ver.Version), state, shortDate(ver.CreatedAt)})
	}
	c.table([]string{"VERSION", "STATE", "CREATED"}, table)
	return nil
}

func vaultKeyCreate(c *cmd, args []string) error {
	fs := newFlagSet("vault key create", `Usage: th vault key create VAULT NAME --type TYPE [flags]

Creates a key. Types: aes256 (encrypt), rsa2048 and rsa4096 (encrypt or sign), ecp256
and ecp384 (sign). The key itself never leaves the system.`)
	var keyType, purpose, description string
	var rotation int
	fs.String(&keyType, "type", "aes256, rsa2048, rsa4096, ecp256 or ecp384")
	fs.String(&purpose, "purpose", "encrypt or sign (default: what the type does; rsa needs it)")
	fs.String(&description, "description", "What the key is for")
	fs.Int(&rotation, "rotation-days", "Rotate every N days (7-3650, default off)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th vault key create VAULT NAME --type TYPE")
	}
	if keyType == "" {
		return errors.New("--type is required: aes256, rsa2048, rsa4096, ecp256 or ecp384")
	}
	if purpose == "" {
		switch {
		case keyType == "aes256":
			purpose = "encrypt"
		case strings.HasPrefix(keyType, "ecp"):
			purpose = "sign"
		default:
			return errors.New("--purpose is required for an RSA key: encrypt or sign")
		}
	}
	cl, v, err := c.liveVault(fs.Args()[0])
	if err != nil {
		return err
	}
	var k vKey
	in := map[string]any{"name": fs.Args()[1], "type": keyType, "purpose": purpose, "description": description, "rotationDays": rotation}
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/keys", in, &k); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(k)
	}
	fmt.Fprintf(c.app.Stdout, "Created key [%s] (%s, %s).\n", k.Name, k.Type, k.Purpose)
	return nil
}

func vaultKeyRotate(c *cmd, args []string) error {
	cl, v, k, err := c.vaultKeyArgs("rotate", "Makes a key's next version. New data uses it; existing data still decrypts.", args)
	if err != nil {
		return err
	}
	var out vKeyVersion
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/keys/"+k.ID+"/rotate", map[string]any{}, &out); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": k.Name, "version": out.Version})
	}
	fmt.Fprintf(c.app.Stdout, "Key [%s] is now on version %d.\n", k.Name, out.Version)
	return nil
}

func (c *cmd) vaultKeySetEnabled(args []string, verb, help string, enabled bool) error {
	cl, v, k, err := c.vaultKeyArgs(verb, help, args)
	if err != nil {
		return err
	}
	if _, err := cl.Do(c.ctx, http.MethodPatch, "/api/v1/vaults/"+v.ID+"/keys/"+k.ID, map[string]bool{"enabled": enabled}, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": k.Name, "enabled": enabled})
	}
	fmt.Fprintf(c.app.Stdout, "Key [%s] is now %sd.\n", k.Name, verb)
	return nil
}

func vaultKeyEnable(c *cmd, args []string) error {
	return c.vaultKeySetEnabled(args, "enable", "Lets a disabled key be used again.", true)
}

func vaultKeyDisable(c *cmd, args []string) error {
	return c.vaultKeySetEnabled(args, "disable", "Stops all use of a key at once, without deleting it.", false)
}

func vaultKeyDelete(c *cmd, args []string) error {
	cl, v, k, err := c.vaultKeyArgs("delete", "Deletes a key. It can be recovered until the vault's soft delete period ends. After that, anything encrypted with it can never be opened again.", args)
	if err != nil {
		return err
	}
	ok, err := c.confirmNo(fmt.Sprintf("Delete key %s? You can recover it within %d days. After that, anything encrypted with it can never be opened again.", k.Name, v.SoftDeleteDays))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/vaults/"+v.ID+"/keys/"+k.ID, nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": k.Name, "deleted": true})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted key [%s].\n", k.Name)
	return nil
}

func vaultKeyRecover(c *cmd, args []string) error {
	cl, v, k, err := c.vaultKeyArgs("recover", "Brings back a deleted key.", args)
	if err != nil {
		return err
	}
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/keys/"+k.ID+"/recover", map[string]any{}, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": k.Name, "recovered": true})
	}
	fmt.Fprintf(c.app.Stdout, "Recovered key [%s].\n", k.Name)
	return nil
}

func vaultKeyPublic(c *cmd, args []string) error {
	fs := newFlagSet("vault key public-key", "Usage: th vault key public-key VAULT NAME [--version N]\n\nPrints a key's public key as PEM: the newest version's, or the one asked for.")
	var version int
	fs.Int(&version, "version", "A version")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: th vault key public-key VAULT NAME [--version N]")
	}
	_, _, k, err := c.findVaultKey(fs.Args()[0], fs.Args()[1])
	if err != nil {
		return err
	}
	for _, ver := range k.Versions { // newest first
		if ver.PublicKey != "" && ver.DestroyedAt == "" && (version == 0 || ver.Version == version) {
			if c.json() {
				return c.printJSON(map[string]any{"name": k.Name, "version": ver.Version, "publicKey": ver.PublicKey})
			}
			_, err := io.WriteString(c.app.Stdout, ver.PublicKey)
			return err
		}
	}
	return fmt.Errorf("key %s has no public key for that version (AES keys have none)", k.Name)
}

// vaultDataFlags is what the key operations take.
type vaultDataFlags struct{ file, aad string }

func (f *vaultDataFlags) add(fs *flagSet) {
	fs.String(&f.file, "file", "Read the data from FILE")
	fs.String(&f.aad, "aad", "Additional data the ciphertext is bound to; give the same to decrypt")
}

// aadField is the request field for --aad.
func (f *vaultDataFlags) aadField(in map[string]any) map[string]any {
	if f.aad != "" {
		in["aad"] = base64.StdEncoding.EncodeToString([]byte(f.aad))
	}
	return in
}

func vaultKeyEncrypt(c *cmd, args []string) error {
	fs := newFlagSet("vault key encrypt", `Usage: th vault key encrypt VAULT NAME [VALUE | - | --file FILE] [--aad TEXT]

Encrypts up to 64 KiB with a key's newest version and prints the ciphertext. With no
value it reads standard input.`)
	var f vaultDataFlags
	f.add(fs)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	a := fs.Args()
	if len(a) < 2 || len(a) > 3 {
		return errors.New("usage: th vault key encrypt VAULT NAME [VALUE | - | --file FILE]")
	}
	var arg *string
	if len(a) == 3 {
		arg = &a[2]
	}
	data, err := c.vaultInput(arg, f.file)
	if err != nil {
		return err
	}
	cl, v, k, err := c.findVaultKey(a[0], a[1])
	if err != nil {
		return err
	}
	var out struct {
		Ciphertext string `json:"ciphertext"`
		Version    int    `json:"version"`
	}
	in := f.aadField(map[string]any{"plaintext": base64.StdEncoding.EncodeToString(data)})
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/keys/"+k.ID+"/encrypt", in, &out); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(out)
	}
	fmt.Fprintln(c.app.Stdout, out.Ciphertext)
	return nil
}

func vaultKeyDecrypt(c *cmd, args []string) error {
	fs := newFlagSet("vault key decrypt", `Usage: th vault key decrypt VAULT NAME [CIPHERTEXT | - | --file FILE] [--aad TEXT]

Decrypts a ciphertext and prints the data exactly, with no newline added. With no
ciphertext it reads standard input.`)
	var f vaultDataFlags
	f.add(fs)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	a := fs.Args()
	if len(a) < 2 || len(a) > 3 {
		return errors.New("usage: th vault key decrypt VAULT NAME [CIPHERTEXT | - | --file FILE]")
	}
	var arg *string
	if len(a) == 3 {
		arg = &a[2]
	}
	data, err := c.vaultInput(arg, f.file)
	if err != nil {
		return err
	}
	cl, v, k, err := c.findVaultKey(a[0], a[1])
	if err != nil {
		return err
	}
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	in := f.aadField(map[string]any{"ciphertext": strings.TrimSpace(string(data))})
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/keys/"+k.ID+"/decrypt", in, &out); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(out)
	}
	plain, err := base64.StdEncoding.DecodeString(out.Plaintext)
	if err != nil {
		return err
	}
	_, err = c.app.Stdout.Write(plain)
	return err
}

func vaultKeySign(c *cmd, args []string) error {
	fs := newFlagSet("vault key sign", `Usage: th vault key sign VAULT NAME [VALUE | - | --file FILE]

Signs up to 64 KiB with a key's newest version, and prints the signature (base64) and
the version that made it, which verify needs. With no value it reads standard input.`)
	var file string
	fs.String(&file, "file", "Read the data from FILE")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	a := fs.Args()
	if len(a) < 2 || len(a) > 3 {
		return errors.New("usage: th vault key sign VAULT NAME [VALUE | - | --file FILE]")
	}
	var arg *string
	if len(a) == 3 {
		arg = &a[2]
	}
	data, err := c.vaultInput(arg, file)
	if err != nil {
		return err
	}
	cl, v, k, err := c.findVaultKey(a[0], a[1])
	if err != nil {
		return err
	}
	var out struct {
		Signature string `json:"signature"`
		Version   int    `json:"version"`
	}
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/keys/"+k.ID+"/sign", map[string]any{"message": base64.StdEncoding.EncodeToString(data)}, &out); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(out)
	}
	fmt.Fprintf(c.app.Stdout, "Signature: %s\nVersion:   %d\n", out.Signature, out.Version)
	return nil
}

func vaultKeyVerify(c *cmd, args []string) error {
	fs := newFlagSet("vault key verify", `Usage: th vault key verify VAULT NAME [VALUE | - | --file FILE] --signature S --version N

Checks a signature. It exits with status 1 when the signature is not valid.`)
	var file, signature string
	var version int
	fs.String(&file, "file", "Read the data from FILE")
	fs.String(&signature, "signature", "The signature (base64)")
	fs.Int(&version, "version", "The version sign printed")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	a := fs.Args()
	if len(a) < 2 || len(a) > 3 || signature == "" || version < 1 {
		return errors.New("usage: th vault key verify VAULT NAME [VALUE | - | --file FILE] --signature S --version N")
	}
	var arg *string
	if len(a) == 3 {
		arg = &a[2]
	}
	data, err := c.vaultInput(arg, file)
	if err != nil {
		return err
	}
	cl, v, k, err := c.findVaultKey(a[0], a[1])
	if err != nil {
		return err
	}
	var out struct {
		Valid bool `json:"valid"`
	}
	in := map[string]any{"message": base64.StdEncoding.EncodeToString(data), "signature": signature, "version": version}
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/vaults/"+v.ID+"/keys/"+k.ID+"/verify", in, &out); err != nil {
		return err
	}
	if c.json() {
		if err := c.printJSON(out); err != nil {
			return err
		}
	}
	if !out.Valid {
		return &exitError{code: 1, msg: "The signature is not valid."}
	}
	if !c.json() {
		fmt.Fprintln(c.app.Stdout, "The signature is valid.")
	}
	return nil
}
