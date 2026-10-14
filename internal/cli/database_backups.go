package cli

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

type dbBackup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	SizeBytes   int64  `json:"sizeBytes"`
	Driver      string `json:"driver"`
	Engine      string `json:"engine"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"createdAt"`
	CompletedAt string `json:"completedAt,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

type dbBackupPolicyView struct {
	Enabled        bool   `json:"enabled"`
	FrequencyHours int    `json:"frequencyHours"`
	WindowHour     int    `json:"windowHour"`
	RetentionCount int    `json:"retentionCount"`
	NextRunAt      string `json:"nextRunAt,omitempty"`
	LastRunAt      string `json:"lastRunAt,omitempty"`
}

type dbBackupList struct {
	Backups          []dbBackup          `json:"backups"`
	Policy           *dbBackupPolicyView `json:"policy"`
	RestoreSupported bool                `json:"restoreSupported"`
}

func (c *cmd) dbBackups(cl *api.Client, s *dbService) (dbBackupList, []byte, error) {
	var l dbBackupList
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+s.ID+"/backups", nil, &l)
	return l, raw, err
}

// dbFindBackup matches a backup by ID or name.
func dbFindBackup(l dbBackupList, p *dbProduct, slug, want string) (*dbBackup, error) {
	for i := range l.Backups {
		if l.Backups[i].ID == want {
			return &l.Backups[i], nil
		}
	}
	for i := range l.Backups {
		if l.Backups[i].Name == want {
			return &l.Backups[i], nil
		}
	}
	return nil, fmt.Errorf("backup %q not found. List them with: th %s backups list %s", want, p.group, slug)
}

func dbPolicyText(pol *dbBackupPolicyView) string {
	if pol == nil || !pol.Enabled {
		return "Automated backups: off"
	}
	every := fmt.Sprintf("every %d hours", pol.FrequencyHours)
	if pol.FrequencyHours == 24 {
		every = "daily"
	} else if pol.FrequencyHours == 168 {
		every = "weekly"
	}
	s := fmt.Sprintf("Automated backups: %s from %02d:00 (Asia/Bangkok), keep %d", every, pol.WindowHour, pol.RetentionCount)
	if pol.NextRunAt != "" {
		s += ", next " + shortTime(pol.NextRunAt)
	}
	return s
}

func dbBackupsList(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s backups list NAME", p.group)
	fs := newFlagSet(p.group+" backups list", "Usage: "+usage+"\n\nLists a "+p.noun+"'s backups, newest first, and its backup policy.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return err
	}
	l, raw, err := c.dbBackups(cl, s)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, b := range l.Backups {
		size := "-"
		if b.SizeBytes > 0 {
			size = dbBytes(b.SizeBytes)
		}
		expires := "-"
		if b.ExpiresAt != "" {
			expires = shortTime(b.ExpiresAt)
		}
		rows = append(rows, []string{b.ID, dash(b.Name), b.Kind, b.Status, size, shortTime(b.CreatedAt), expires})
	}
	c.table([]string{"ID", "NAME", "KIND", "STATUS", "SIZE", "CREATED", "EXPIRES"}, rows)
	c.info("%s", dbPolicyText(l.Policy))
	if !l.RestoreSupported {
		c.info("Backups of this %s are restored by our staff; open a support ticket to restore one.", p.noun)
	}
	return nil
}

func dbBackupsCreate(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s backups create NAME", p.group)
	fs := newFlagSet(p.group+" backups create", "Usage: "+usage+"\n\nStarts a manual backup. Backups are billed by stored GB.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return err
	}
	var b dbBackup
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+s.ID+"/backups", nil, &b)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Backup [%s] of %s [%s] started (status: %s).\n", b.ID, p.noun, s.Slug, dash(b.Status))
	fmt.Fprintf(c.app.Stdout, "Follow it with: th %s backups list %s\n", p.group, s.Slug)
	return nil
}

func dbBackupsDelete(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s backups delete NAME BACKUP [--quiet]", p.group)
	fs := newFlagSet(p.group+" backups delete", "Usage: "+usage+"\n\nDeletes a backup (by ID or name). --quiet skips the prompt.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: " + usage)
	}
	cl, s, err := c.dbMust(p, fs.Args()[0])
	if err != nil {
		return err
	}
	l, _, err := c.dbBackups(cl, s)
	if err != nil {
		return err
	}
	b, err := dbFindBackup(l, p, s.Slug, fs.Args()[1])
	if err != nil {
		return err
	}
	ok, err := c.confirm(fmt.Sprintf("Backup [%s] of %s [%s] will be deleted.", b.ID, p.noun, s.Slug))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	raw, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/services/"+s.ID+"/backups/"+b.ID, nil, nil)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Deleted backup [%s].\n", b.ID)
	return nil
}

func dbBackupsRestore(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s backups restore NAME BACKUP [--quiet]", p.group)
	fs := newFlagSet(p.group+" backups restore", "Usage: "+usage+"\n\nReplaces the "+p.noun+"'s data with a backup (by ID or name).\n--quiet skips the prompt.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 2 {
		return errors.New("usage: " + usage)
	}
	cl, s, err := c.dbMust(p, fs.Args()[0])
	if err != nil {
		return err
	}
	l, _, err := c.dbBackups(cl, s)
	if err != nil {
		return err
	}
	b, err := dbFindBackup(l, p, s.Slug, fs.Args()[1])
	if err != nil {
		return err
	}
	when := shortTime(b.CreatedAt)
	ok, err := c.confirm(fmt.Sprintf("The data in %s [%s] will be replaced with backup [%s] from %s.\nChanges made since then are lost.", p.noun, s.Slug, b.ID, when))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	c.info("Restoring backup [%s] into %s [%s]; this can take a while...", b.ID, p.noun, s.Slug)
	// The restore answers when it is done; allow far longer than a normal call.
	cl.HTTP = &http.Client{Timeout: 2 * time.Hour}
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+s.ID+"/backups/"+b.ID+"/restore", nil, nil)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Restored %s [%s] from backup [%s].\n", p.noun, s.Slug, b.ID)
	return nil
}

func dbBackupPolicy(c *cmd, p *dbProduct, args []string) error {
	var enable, disable bool
	var hours, window, keep int
	usage := fmt.Sprintf("th %s backup-policy NAME [--enable|--disable] [--hours N] [--window H] [--keep N]", p.group)
	fs := newFlagSet(p.group+" backup-policy", "Usage: "+usage+"\n\nShows or changes automated backups. Without flags it prints the policy.\nBackups are billed by stored GB.")
	fs.Bool(&enable, "enable", "Turn automated backups on")
	fs.Bool(&disable, "disable", "Turn automated backups off")
	fs.Int(&hours, "hours", "Back up every N hours: 6, 12, 24 or 168")
	fs.Int(&window, "window", "Hour backups start, 0–23 (Asia/Bangkok)")
	fs.Int(&keep, "keep", "Automated backups kept, 1–90")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	if enable && disable {
		return errors.New("use either --enable or --disable")
	}
	if err := dbValidatePolicy(hours, window, keep, fs.Set("window")); err != nil {
		return err
	}
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return err
	}
	l, _, err := c.dbBackups(cl, s)
	if err != nil {
		return err
	}
	changing := enable || disable || fs.Set("hours") || fs.Set("window") || fs.Set("keep")
	if !changing {
		if c.json() {
			return c.printJSON(l.Policy)
		}
		fmt.Fprintln(c.app.Stdout, dbPolicyText(l.Policy))
		return nil
	}
	cur := dbBackupPolicyView{FrequencyHours: 24, WindowHour: 2, RetentionCount: 7}
	if l.Policy != nil {
		cur = *l.Policy
	}
	on := cur.Enabled
	switch {
	case enable:
		on = true
	case disable:
		on = false
	case l.Policy == nil:
		// Setting a schedule on a database that never had one turns it on.
		on = true
	}
	body := dbBackupPolicyInput{Enabled: &on, FrequencyHours: cur.FrequencyHours, RetentionCount: cur.RetentionCount}
	w := cur.WindowHour
	if fs.Set("hours") {
		body.FrequencyHours = hours
	}
	if fs.Set("window") {
		w = window
	}
	if fs.Set("keep") {
		body.RetentionCount = keep
	}
	body.WindowHour = &w
	var out dbBackupPolicyView
	raw, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/services/"+s.ID+"/backup-policy", body, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Updated %s [%s]. %s\n", p.noun, s.Slug, dbPolicyText(&out))
	return nil
}

// ---- read replicas (database only) ----

func dbReplicasList(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s replicas list NAME", p.group)
	fs := newFlagSet(p.group+" replicas list", "Usage: "+usage+"\n\nLists the read replicas of a database (or of a replica's primary).")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return err
	}
	var out struct {
		Primary     *dbService  `json:"primary"`
		Replicas    []dbService `json:"replicas"`
		MaxReplicas int         `json:"maxReplicas"`
	}
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+s.ID+"/replicas", nil, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	if out.Primary != nil {
		c.info("Primary: %s", out.Primary.Slug)
	}
	rows := [][]string{}
	for _, r := range out.Replicas {
		rows = append(rows, []string{r.Slug, r.Status, dash(r.ReplicationState), dbSizeText(r.CPUQuota, r.CPUBurst, r.MemoryMB)})
	}
	c.table([]string{"REPLICA", "STATUS", "REPLICATION", "SIZE"}, rows)
	if out.MaxReplicas > 0 {
		c.info("%d of %d replicas.", len(out.Replicas), out.MaxReplicas)
	}
	return nil
}

func dbReplicasCreate(c *cmd, p *dbProduct, args []string) error {
	var size, cpu, memory, display string
	usage := fmt.Sprintf("th %s replicas create NAME [--size ID | --cpu N --memory M] [--name DISPLAY]", p.group)
	fs := newFlagSet(p.group+" replicas create", "Usage: "+usage+"\n\nCreates a read replica of database NAME, by default the same size.\nIt is billed like a database of its size.")
	fs.String(&size, "size", "Size ID from th "+p.group+" sizes")
	fs.String(&cpu, "cpu", "vCPU, e.g. 0.5, 1, 2")
	fs.String(&memory, "memory", "Memory, e.g. 512Mi, 1Gi")
	fs.String(&display, "name", "Display name (default: \"<primary> · Replica N\")")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return err
	}
	var body struct {
		Name     string `json:"name,omitempty"`
		CPUQuota int64  `json:"cpuQuota,omitempty"`
		CPUBurst *int64 `json:"cpuBurst,omitempty"`
		MemoryMB int    `json:"memoryMB,omitempty"`
	}
	body.Name = display
	var burst int64
	var z *dbSize
	if body.CPUQuota, burst, body.MemoryMB, z, err = c.dbShape(cl, p, size, cpu, memory); err != nil {
		return err
	}
	if z != nil {
		body.CPUBurst = &burst
	}
	var out dbService
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+s.ID+"/replicas", body, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Creating read replica [%s] of [%s] (status: %s).\n", dash(out.Slug), s.Slug, dash(out.Status))
	return nil
}

func dbReplicasPromote(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s replicas promote REPLICA [--quiet]", p.group)
	fs := newFlagSet(p.group+" replicas promote", "Usage: "+usage+"\n\nStops replication and makes the replica a writable database of its own.\nThis cannot be undone. --quiet skips the prompt.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return err
	}
	if s.ReplicaOfServiceID == "" {
		return fmt.Errorf("database [%s] is not a read replica. List replicas with: th %s replicas list %s", s.Slug, p.group, s.Slug)
	}
	ok, err := c.confirm(fmt.Sprintf("Replica [%s] will stop replicating and become a writable database. This cannot be undone.", s.Slug))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	var out dbService
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+s.ID+"/promote", nil, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Promoted [%s]; it is now a writable database.\n", s.Slug)
	return nil
}
