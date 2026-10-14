package cli

// th database and th cache: Managed Database (PostgreSQL, MySQL, MariaDB)
// and Managed Cache (Redis, Valkey). Both are Console services of kind
// "database"; the engine decides the product, so one implementation serves
// both groups, parameterised by dbProduct.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

// dbProduct is one of the two command groups.
type dbProduct struct {
	group string // command group and product: "database" or "cache"
	title string // "Managed Database"
	noun  string // "database" or "cache", for messages
	// example engine for usage text
	example string
}

var (
	dbProductDatabase = &dbProduct{group: "database", title: "Managed Database", noun: "database", example: "postgres|mysql|mariadb"}
	dbProductCache    = &dbProduct{group: "cache", title: "Managed Cache", noun: "cache", example: "redis|valkey"}
)

func (p *dbProduct) isCache() bool { return p.group == "cache" }

// dbEngineProduct is the product of a services row's engine, as the Console
// decides it: Redis-protocol engines are caches.
func dbEngineProduct(engine string) string {
	e := strings.ToLower(engine)
	if strings.HasPrefix(e, "redis") || strings.HasPrefix(e, "valkey") {
		return "cache"
	}
	return "database"
}

func (p *dbProduct) owns(engine string) bool { return dbEngineProduct(engine) == p.group }

type dbHandler func(c *cmd, p *dbProduct, args []string) error

func (p *dbProduct) h(f dbHandler) handler {
	return func(c *cmd, args []string) error { return f(c, p, args) }
}

func init() {
	for _, p := range []*dbProduct{dbProductDatabase, dbProductCache} {
		g := p.group
		m := map[string]handler{
			g + " list":            p.h(dbListCmd),
			g + " describe":        p.h(dbDescribe),
			g + " create":          p.h(dbCreate),
			g + " delete":          p.h(dbDelete),
			g + " start":           p.h(dbStart),
			g + " stop":            p.h(dbStop),
			g + " resize":          p.h(dbResize),
			g + " engines":         p.h(dbEngines),
			g + " sizes":           p.h(dbSizes),
			g + " credentials":     p.h(dbCredentialsCmd),
			g + " connect":         p.h(dbConnect),
			g + " public":          p.h(dbPublic),
			g + " backups list":    p.h(dbBackupsList),
			g + " backups create":  p.h(dbBackupsCreate),
			g + " backups delete":  p.h(dbBackupsDelete),
			g + " backups restore": p.h(dbBackupsRestore),
			g + " backup-policy":   p.h(dbBackupPolicy),
		}
		usage := []string{
			g + " list | describe NAME | create NAME | delete NAME | start NAME | stop NAME | resize NAME",
			g + " credentials NAME | connect NAME | public NAME | engines | sizes",
			g + " backups list|create|delete|restore NAME [BACKUP] | backup-policy NAME",
		}
		if !p.isCache() {
			m[g+" replicas list"] = p.h(dbReplicasList)
			m[g+" replicas create"] = p.h(dbReplicasCreate)
			m[g+" replicas promote"] = p.h(dbReplicasPromote)
			usage = append(usage, g+" replicas list|create|promote NAME")
		}
		register(m, usage...)
	}
}

// ---- API shapes ----

type dbService struct {
	ID                         string   `json:"id"`
	ProjectID                  string   `json:"projectId"`
	Name                       string   `json:"name"`
	Slug                       string   `json:"slug"`
	Kind                       string   `json:"kind"`
	Engine                     string   `json:"engine"`
	Status                     string   `json:"status"`
	RegionID                   string   `json:"regionId"`
	ComputeClass               string   `json:"computeClass"`
	CPUQuota                   int64    `json:"cpuQuota"`
	CPUBurst                   int64    `json:"cpuBurst,omitempty"`
	MemoryMB                   int      `json:"memoryMB"`
	Port                       int      `json:"port"`
	InternalDNS                string   `json:"internalDns"`
	InternalIP                 string   `json:"internalIp"`
	DeletionProtection         bool     `json:"deletionProtection"`
	DatabaseProxyEnabled       bool     `json:"databaseProxyEnabled"`
	DatabaseProxyHostname      string   `json:"databaseProxyHostname,omitempty"`
	DatabaseProxyBandwidthMbps int      `json:"databaseProxyBandwidthMbps,omitempty"`
	DatabaseProxyAllowedCIDRs  []string `json:"databaseProxyAllowedCidrs"`
	ReplicaOfServiceID         string   `json:"replicaOfServiceId,omitempty"`
	ReplicationState           string   `json:"replicationState,omitempty"`
	CreatedAt                  string   `json:"createdAt,omitempty"`
}

type dbEngine struct {
	ID        string `json:"id"`
	Family    string `json:"family"`
	Protocol  string `json:"protocol"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Port      int    `json:"port"`
	Default   bool   `json:"default,omitempty"`
	Tagline   string `json:"tagline"`
	Product   string `json:"product"`
	Available bool   `json:"available"`
}

func (e dbEngine) product() string {
	if e.Product != "" {
		return e.Product
	}
	if e.Protocol == "redis" {
		return "cache"
	}
	return dbEngineProduct(e.ID)
}

type dbSize struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Hint          string   `json:"hint"`
	Series        string   `json:"series"`
	CPUQuota      int64    `json:"cpuQuota"`
	BurstCPUQuota int64    `json:"burstCpuQuota,omitempty"`
	MemoryMB      int      `json:"memoryMB"`
	MinStorageGB  int      `json:"minStorageGb,omitempty"`
	MaxStorageGB  int      `json:"maxStorageGb,omitempty"`
	Pools         []string `json:"pools"`
}

type dbBackupPolicyInput struct {
	Enabled        *bool `json:"enabled"`
	FrequencyHours int   `json:"frequencyHours,omitempty"`
	WindowHour     *int  `json:"windowHour,omitempty"`
	RetentionCount int   `json:"retentionCount,omitempty"`
}

// dbCreateInput is the POST /services body for a database or cache.
type dbCreateInput struct {
	Kind                       string               `json:"kind"`
	Engine                     string               `json:"engine"`
	Name                       string               `json:"name"`
	Slug                       string               `json:"slug"`
	ProjectID                  string               `json:"projectId,omitempty"`
	RegionID                   string               `json:"regionId,omitempty"`
	CPUQuota                   int64                `json:"cpuQuota,omitempty"`
	CPUBurst                   int64                `json:"cpuBurst,omitempty"`
	MemoryMB                   int                  `json:"memoryMB,omitempty"`
	DiskType                   string               `json:"diskType,omitempty"`
	StorageGB                  int                  `json:"storageGb,omitempty"`
	DatabaseProxyEnabled       bool                 `json:"databaseProxyEnabled,omitempty"`
	DatabaseProxyAllowedCIDRs  []string             `json:"databaseProxyAllowedCidrs,omitempty"`
	DatabaseProxyBandwidthMbps int                  `json:"databaseProxyBandwidthMbps,omitempty"`
	BackupPolicy               *dbBackupPolicyInput `json:"backupPolicy,omitempty"`
}

// ---- lookups ----

func (c *cmd) dbListServices(cl *api.Client, p *dbProduct, projectID string) ([]dbService, error) {
	var all []dbService
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services?kind=database&projectId="+q(projectID), nil, &all); err != nil {
		return nil, err
	}
	out := []dbService{}
	for _, s := range all {
		if (s.ProjectID == "" || s.ProjectID == projectID) && (s.Kind == "" || s.Kind == "database") && p.owns(s.Engine) {
			out = append(out, s)
		}
	}
	return out, nil
}

// dbMust resolves the Project and a database or cache by slug or name.
func (c *cmd) dbMust(p *dbProduct, name string) (*api.Client, *dbService, error) {
	cl, err := c.client(true)
	if err != nil {
		return nil, nil, err
	}
	pid, slug, err := c.resolveProject(cl)
	if err != nil {
		return nil, nil, err
	}
	svcs, err := c.dbListServices(cl, p, pid)
	if err != nil {
		return nil, nil, err
	}
	for _, match := range []func(dbService) bool{
		func(s dbService) bool { return s.Slug == name },
		func(s dbService) bool { return s.Name == name },
		func(s dbService) bool { return s.ID == name },
	} {
		for i := range svcs {
			if match(svcs[i]) {
				return cl, &svcs[i], nil
			}
		}
	}
	return nil, nil, fmt.Errorf("%s [%s] not found in project [%s]. List them with: th %s list", p.noun, name, slug, p.group)
}

func (c *cmd) dbFetchEngines(cl *api.Client) ([]dbEngine, error) {
	var engines []dbEngine
	_, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/databases/engines", nil, &engines)
	return engines, err
}

func (c *cmd) dbFetchSizes(cl *api.Client, p *dbProduct) ([]dbSize, error) {
	var cat struct {
		Sizes map[string][]dbSize `json:"sizes"`
	}
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/service-catalog", nil, &cat); err != nil {
		return nil, err
	}
	return cat.Sizes[p.group], nil
}

func dbFindSize(sizes []dbSize, p *dbProduct, want string) (dbSize, error) {
	for _, z := range sizes {
		if z.ID == want || strings.EqualFold(z.Name, want) {
			return z, nil
		}
	}
	var ids []string
	for _, z := range sizes {
		ids = append(ids, z.ID)
	}
	return dbSize{}, fmt.Errorf("size %q not found. Sizes: %s (see: th %s sizes)", want, strings.Join(ids, ", "), p.group)
}

// dbPickEngine maps --engine (a family, alias or engine ID) and --version
// to a catalog engine of this product.
func dbPickEngine(engines []dbEngine, p *dbProduct, want, version string) (dbEngine, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "postgresql" {
		want = "postgres"
	}
	version = strings.TrimSpace(version)
	var pick *dbEngine
	for i := range engines {
		if engines[i].ID == want && version == "" {
			pick = &engines[i]
			break
		}
	}
	if pick == nil {
		var family []*dbEngine
		for i := range engines {
			if engines[i].Family == want {
				family = append(family, &engines[i])
			}
		}
		if len(family) == 0 {
			return dbEngine{}, fmt.Errorf("unknown engine %q. %s", want, dbEngineChoices(engines, p))
		}
		for _, e := range family {
			if version == "" && e.Default {
				pick = e
				break
			}
			if version != "" && (e.Version == version || strings.HasPrefix(e.Version, version+" ") || e.ID == e.Family+"-"+version) {
				pick = e
				break
			}
		}
		if pick == nil && version == "" {
			pick = family[0]
		}
		if pick == nil {
			var vs []string
			for _, e := range family {
				vs = append(vs, e.Version)
			}
			return dbEngine{}, fmt.Errorf("%s has no version %q. Versions: %s", family[0].Name, version, strings.Join(vs, ", "))
		}
	}
	if pr := pick.product(); pr != p.group {
		return dbEngine{}, fmt.Errorf("%s is a %s engine; create it with: th %s create NAME --engine %s", pick.Name, pr, pr, pick.Family)
	}
	if !pick.Available {
		return dbEngine{}, fmt.Errorf("%s %s is not available right now. %s", pick.Name, pick.Version, dbEngineChoices(engines, p))
	}
	return *pick, nil
}

func dbEngineChoices(engines []dbEngine, p *dbProduct) string {
	var b strings.Builder
	b.WriteString("Engines:")
	for _, e := range engines {
		if e.product() != p.group {
			continue
		}
		def := ""
		if e.Default {
			def = " (default)"
		}
		fmt.Fprintf(&b, "\n  --engine %s --version %s%s", e.Family, strings.Fields(e.Version + " x")[0], def)
	}
	return b.String()
}

// ---- parsing helpers ----

// dbParseStorage turns "10GB", "10G", "10" (GB) or "1TB" into whole GB.
func dbParseStorage(s string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	mult := 1
	for _, suf := range []struct {
		s string
		m int
	}{{"tib", 1024}, {"tb", 1024}, {"ti", 1024}, {"t", 1024}, {"gib", 1}, {"gb", 1}, {"gi", 1}, {"g", 1}} {
		if strings.HasSuffix(t, suf.s) {
			t, mult = strings.TrimSpace(t[:len(t)-len(suf.s)]), suf.m
			break
		}
	}
	n, err := strconv.Atoi(t)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid --storage %q: use whole GB such as 10GB, 50GB or 100GB", s)
	}
	return n * mult, nil
}

// dbShape reads --size or --cpu/--memory; zero values mean "not given".
func (c *cmd) dbShape(cl *api.Client, p *dbProduct, size, cpu, memory string) (cpuQuota, burst int64, memMB int, z *dbSize, err error) {
	if size != "" && (cpu != "" || memory != "") {
		return 0, 0, 0, nil, errors.New("use either --size or --cpu/--memory, not both")
	}
	if size != "" {
		sizes, err := c.dbFetchSizes(cl, p)
		if err != nil {
			return 0, 0, 0, nil, err
		}
		found, err := dbFindSize(sizes, p, size)
		if err != nil {
			return 0, 0, 0, nil, err
		}
		return found.CPUQuota, found.BurstCPUQuota, found.MemoryMB, &found, nil
	}
	if cpu != "" {
		if cpuQuota, err = parseCPU(cpu); err != nil {
			return 0, 0, 0, nil, err
		}
	}
	if memory != "" {
		if memMB, err = parseMemory(memory); err != nil {
			return 0, 0, 0, nil, err
		}
	}
	return cpuQuota, 0, memMB, nil, nil
}

func dbValidatePolicy(hours, window, keep int, windowSet bool) error {
	if hours != 0 && hours != 6 && hours != 12 && hours != 24 && hours != 168 {
		return errors.New("backup frequency must be 6, 12, 24 or 168 hours")
	}
	if windowSet && (window < 0 || window > 23) {
		return errors.New("backup window must be an hour from 0 to 23 (Asia/Bangkok)")
	}
	if keep != 0 && (keep < 1 || keep > 90) {
		return errors.New("backups kept must be from 1 to 90")
	}
	return nil
}

func dbSizeText(cpuQuota, burst int64, memMB int) string {
	s := vcpu(cpuQuota) + " vCPU"
	if burst > 0 {
		s += " (bursts to " + vcpu(burst) + ")"
	}
	return fmt.Sprintf("%s, %s", s, dbMemText(memMB))
}

func dbMemText(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%d GiB", mb/1024)
	}
	if mb >= 1024 {
		return strconv.FormatFloat(float64(mb)/1024, 'f', 1, 64) + " GiB"
	}
	return fmt.Sprintf("%d MiB", mb)
}

func dbBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GiB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KiB"
	}
	return fmt.Sprintf("%d B", n)
}

func dbOneArg(fs *flagSet, usage string) (string, error) {
	if len(fs.Args()) != 1 {
		return "", errors.New("usage: " + usage)
	}
	return fs.Args()[0], nil
}

// ---- commands ----

func dbListCmd(c *cmd, p *dbProduct, args []string) error {
	fs := newFlagSet(p.group+" list", fmt.Sprintf("Usage: th %s list\n\nLists the %s instances of the active Project.", p.group, p.title))
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
	svcs, err := c.dbListServices(cl, p, pid)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(svcs)
	}
	rows := [][]string{}
	for _, s := range svcs {
		public := "-"
		if s.DatabaseProxyEnabled {
			public = "on"
		}
		role := ""
		if s.ReplicaOfServiceID != "" {
			role = " (replica)"
		}
		rows = append(rows, []string{s.Slug, s.Engine + role, s.RegionID, s.Status, vcpu(s.CPUQuota), dbMemText(s.MemoryMB), public})
	}
	c.table([]string{strings.ToUpper(p.noun), "ENGINE", "REGION", "STATUS", "VCPU", "MEMORY", "PUBLIC"}, rows)
	return nil
}

func dbDescribe(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s describe NAME", p.group)
	fs := newFlagSet(p.group+" describe", "Usage: "+usage+"\n\nShows the engine, size, status and addresses of a "+p.noun+".")
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
	var conn dbConnection
	connErr := error(nil)
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+s.ID+"/connection", nil, &conn); err != nil {
		connErr = err
	}
	if c.json() {
		out := map[string]any{"service": s}
		if connErr == nil {
			conn.Credentials.Password = "" // describe never prints secrets
			out["connection"] = conn
		}
		return c.printJSON(out)
	}
	w := c.app.Stdout
	engine := s.Engine
	if conn.Engine.Name != "" {
		engine = conn.Engine.Name + " " + conn.Engine.Version
	}
	fmt.Fprintf(w, "%s %s (%s) in region %s\n\n", strings.ToUpper(p.noun[:1])+p.noun[1:], s.Slug, engine, s.RegionID)
	fmt.Fprintf(w, "Status:         %s\n", dash(s.Status))
	fmt.Fprintf(w, "Size:           %s\n", dbSizeText(s.CPUQuota, s.CPUBurst, s.MemoryMB))
	if !p.isCache() && connErr == nil && conn.StorageBytes > 0 {
		disk := strings.ToUpper(conn.DiskType)
		fmt.Fprintf(w, "Storage:        %d GB %s\n", conn.StorageBytes>>30, disk)
	}
	priv := s.InternalDNS
	if priv == "" {
		priv = s.InternalIP
	}
	fmt.Fprintf(w, "Private:        %s:%d (inside the Project's VPC)\n", dash(priv), s.Port)
	switch {
	case connErr == nil && conn.Public.Enabled:
		fmt.Fprintf(w, "Public:         %s:%d (TLS)\n", conn.Public.Hostname, conn.Public.Port)
		allowed := "nobody yet — add with: th " + p.group + " public " + s.Slug + " --allow <your-ip>/32"
		if len(conn.Public.AllowedCIDRs) > 0 {
			allowed = strings.Join(conn.Public.AllowedCIDRs, ", ")
		}
		fmt.Fprintf(w, "Allowed from:   %s\n", allowed)
		fmt.Fprintf(w, "Bandwidth:      %d Mbps\n", conn.Public.BandwidthMbps)
	case s.DatabaseProxyEnabled:
		fmt.Fprintf(w, "Public:         %s (on)\n", dash(s.DatabaseProxyHostname))
	default:
		fmt.Fprintf(w, "Public:         off\n")
	}
	if s.ReplicaOfServiceID != "" {
		fmt.Fprintf(w, "Replica of:     %s (%s)\n", s.ReplicaOfServiceID, dash(s.ReplicationState))
	}
	if s.DeletionProtection {
		fmt.Fprintf(w, "Deletion protection: on\n")
	}
	if connErr == nil {
		fmt.Fprintf(w, "User:           %s\n", dash(conn.Credentials.User))
		if conn.Credentials.Database != "" {
			fmt.Fprintf(w, "Database:       %s\n", conn.Credentials.Database)
		}
	}
	fmt.Fprintf(w, "\nCredentials: th %s credentials %s\n", p.group, s.Slug)
	return nil
}

func dbCreate(c *cmd, p *dbProduct, args []string) error {
	var engine, version, size, cpu, memory, storage, disk string
	var public, backups bool
	var allow []string
	var bandwidth, backupHours, backupWindow, backupKeep int
	usage := fmt.Sprintf("th %s create NAME --engine %s [--version V] [--region R] [--size ID | --cpu N --memory M]", p.group, p.example)
	if !p.isCache() {
		usage += " [--storage 10GB] [--disk ssd|hdd]"
	}
	usage += " [--public --allow CIDR,...] [--backups --backup-hours N --backup-window H --backup-keep N]"
	fs := newFlagSet(p.group+" create", "Usage: "+usage+"\n\nCreates a "+p.title+" instance. It is billed by the hour from the\nProject's wallet; see sizes with: th "+p.group+" sizes")
	fs.String(&engine, "engine", "Engine: "+p.example+" (or an engine ID from th "+p.group+" engines)")
	fs.String(&version, "version", "Engine version (default: the recommended one)")
	fs.String(&size, "size", "Size ID from th "+p.group+" sizes")
	fs.String(&cpu, "cpu", "vCPU, e.g. 0.5, 1, 2")
	fs.String(&memory, "memory", "Memory, e.g. 512Mi, 1Gi, 4Gi")
	if !p.isCache() {
		fs.String(&storage, "storage", "Storage, e.g. 10GB (default 10GB; grows later, never shrinks)")
		fs.String(&disk, "disk", "Disk type: ssd or hdd")
	}
	fs.Bool(&public, "public", "Open a public TLS endpoint (only --allow addresses may connect)")
	fs.List(&allow, "allow", "Addresses allowed on the public endpoint, e.g. 203.0.113.10/32 (repeatable, comma-separated)")
	fs.Int(&bandwidth, "bandwidth", "Public endpoint bandwidth in Mbps (1–1000, default 10)")
	fs.Bool(&backups, "backups", "Turn on automated backups (billed by stored GB)")
	fs.Int(&backupHours, "backup-hours", "Backup every N hours: 6, 12, 24 (default) or 168")
	fs.Int(&backupWindow, "backup-window", "Hour backups start, 0–23 Asia/Bangkok (default 2)")
	fs.Int(&backupKeep, "backup-keep", "Automated backups kept, 1–90 (default 7)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return errors.New(strings.Replace(err.Error(), "service name", p.noun+" name", 1))
	}
	if engine == "" {
		return fmt.Errorf("--engine is required (%s)", p.example)
	}
	in := dbCreateInput{Kind: "database", Name: name, Slug: name}
	if storage != "" {
		if in.StorageGB, err = dbParseStorage(storage); err != nil {
			return err
		}
	}
	if disk != "" {
		disk = strings.ToLower(disk)
		if disk != "ssd" && disk != "hdd" {
			return errors.New("--disk must be ssd or hdd")
		}
		in.DiskType = disk
	}
	cidrs := splitList(allow)
	if fs.Set("bandwidth") && (bandwidth < 1 || bandwidth > 1000) {
		return errors.New("--bandwidth must be from 1 to 1000 Mbps")
	}
	if (len(cidrs) > 0 || fs.Set("bandwidth")) && !public {
		return errors.New("--allow and --bandwidth need --public")
	}
	in.DatabaseProxyEnabled, in.DatabaseProxyAllowedCIDRs, in.DatabaseProxyBandwidthMbps = public, cidrs, bandwidth
	policyFlags := fs.Set("backup-hours") || fs.Set("backup-window") || fs.Set("backup-keep")
	if fs.Set("backups") || policyFlags {
		if err := dbValidatePolicy(backupHours, backupWindow, backupKeep, fs.Set("backup-window")); err != nil {
			return err
		}
		on := backups || !fs.Set("backups")
		pol := &dbBackupPolicyInput{Enabled: &on, FrequencyHours: backupHours, RetentionCount: backupKeep}
		if fs.Set("backup-window") {
			w := backupWindow
			pol.WindowHour = &w
		}
		in.BackupPolicy = pol
	}

	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	engines, err := c.dbFetchEngines(cl)
	if err != nil {
		return err
	}
	eng, err := dbPickEngine(engines, p, engine, version)
	if err != nil {
		return err
	}
	in.Engine = eng.ID
	if in.CPUQuota, in.CPUBurst, in.MemoryMB, _, err = c.dbShape(cl, p, size, cpu, memory); err != nil {
		return err
	}
	region, err := c.resolveRegion(cl, true)
	if err != nil {
		return err
	}
	in.ProjectID, in.RegionID = pid, region

	var out dbService
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services", in, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Creating %s [%s] (%s %s) in region [%s]: %s.\n", p.noun, name, eng.Name, eng.Version, dash(out.RegionID), dash(out.Status))
	if out.CPUQuota > 0 {
		fmt.Fprintf(c.app.Stdout, "Size: %s\n", dbSizeText(out.CPUQuota, out.CPUBurst, out.MemoryMB))
	}
	fmt.Fprintf(c.app.Stdout, "Credentials: th %s credentials %s\n", p.group, name)
	if public && len(cidrs) == 0 {
		c.info("The public endpoint allows no addresses yet. Add yours with: th %s public %s --allow <your-ip>/32", p.group, name)
	}
	return nil
}

func dbDelete(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s delete NAME [--quiet]", p.group)
	fs := newFlagSet(p.group+" delete", "Usage: "+usage+"\n\nDeletes a "+p.noun+" and its data. --quiet skips the prompt.\nA "+p.noun+" with deletion protection on must have it turned off in the Console first.")
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
	ok, err := c.confirm(fmt.Sprintf("%s [%s] and all its data will be deleted.", strings.ToUpper(p.noun[:1])+p.noun[1:], s.Slug))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	var res struct {
		Status string `json:"status"`
	}
	raw, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/services/"+s.ID, nil, &res)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	if res.Status == "deleting" {
		fmt.Fprintf(c.app.Stdout, "Deleting %s [%s]; it is removed in the background.\n", p.noun, s.Slug)
	} else {
		fmt.Fprintf(c.app.Stdout, "Deleted %s [%s].\n", p.noun, s.Slug)
	}
	return nil
}

func dbStart(c *cmd, p *dbProduct, args []string) error { return dbSetStatus(c, p, args, "start") }
func dbStop(c *cmd, p *dbProduct, args []string) error  { return dbSetStatus(c, p, args, "stop") }

func dbSetStatus(c *cmd, p *dbProduct, args []string, action string) error {
	usage := fmt.Sprintf("th %s %s NAME", p.group, action)
	help := "Starts a stopped " + p.noun + "."
	if action == "stop" {
		help = "Stops a " + p.noun + ". Its storage is kept (and billed); start it again with th " + p.group + " start."
	}
	fs := newFlagSet(p.group+" "+action, "Usage: "+usage+"\n\n"+help)
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
	var out dbService
	raw, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/services/"+s.ID+"/status", map[string]string{"action": action}, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	verb := map[string]string{"start": "Starting", "stop": "Stopped"}[action]
	fmt.Fprintf(c.app.Stdout, "%s %s [%s] (status: %s).\n", verb, p.noun, s.Slug, dash(out.Status))
	return nil
}

func dbResize(c *cmd, p *dbProduct, args []string) error {
	var size, cpu, memory, storage string
	usage := fmt.Sprintf("th %s resize NAME [--size ID | --cpu N --memory M]", p.group)
	if !p.isCache() {
		usage += " [--storage GB]"
	}
	fs := newFlagSet(p.group+" resize", "Usage: "+usage+"\n\nChanges the size of a "+p.noun+". A new vCPU or memory restarts it, so\nconnections drop for a moment."+map[bool]string{true: "", false: " Storage only grows."}[p.isCache()])
	fs.String(&size, "size", "Size ID from th "+p.group+" sizes")
	fs.String(&cpu, "cpu", "vCPU, e.g. 0.5, 1, 2")
	fs.String(&memory, "memory", "Memory, e.g. 512Mi, 1Gi, 4Gi")
	if !p.isCache() {
		fs.String(&storage, "storage", "Storage, e.g. 50GB (grow only)")
	}
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	if size == "" && cpu == "" && memory == "" && storage == "" {
		return errors.New("nothing to change: pass --size, --cpu, --memory" + map[bool]string{true: "", false: " or --storage"}[p.isCache()])
	}
	var body struct {
		CPUQuota  int64  `json:"cpuQuota,omitempty"`
		CPUBurst  *int64 `json:"cpuBurst,omitempty"`
		MemoryMB  int    `json:"memoryMB,omitempty"`
		StorageGB int    `json:"storageGb,omitempty"`
	}
	if storage != "" {
		if body.StorageGB, err = dbParseStorage(storage); err != nil {
			return err
		}
	}
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return err
	}
	var burst int64
	var z *dbSize
	if body.CPUQuota, burst, body.MemoryMB, z, err = c.dbShape(cl, p, size, cpu, memory); err != nil {
		return err
	}
	if z != nil {
		body.CPUBurst = &burst
	}
	newCPU, newMem := s.CPUQuota, s.MemoryMB
	if body.CPUQuota > 0 {
		newCPU = body.CPUQuota
	}
	if body.MemoryMB > 0 {
		newMem = body.MemoryMB
	}
	if newCPU != s.CPUQuota || newMem != s.MemoryMB || (z != nil && burst != s.CPUBurst) {
		ok, err := c.confirm(fmt.Sprintf("%s [%s] will be resized from %s to %s and restarted.", strings.ToUpper(p.noun[:1])+p.noun[1:], s.Slug,
			dbSizeText(s.CPUQuota, s.CPUBurst, s.MemoryMB), dbSizeText(newCPU, burst, newMem)))
		if err != nil {
			return err
		}
		if !ok {
			return &exitError{code: 1, msg: "Aborted."}
		}
	}
	var out struct {
		Service   dbService `json:"service"`
		Restarted bool      `json:"restarted"`
	}
	raw, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/services/"+s.ID+"/resources", body, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Resized %s [%s]: %s.\n", p.noun, s.Slug, dbSizeText(out.Service.CPUQuota, out.Service.CPUBurst, out.Service.MemoryMB))
	if out.Restarted {
		fmt.Fprintln(c.app.Stdout, "It was restarted to apply the new size.")
	}
	return nil
}

func dbEngines(c *cmd, p *dbProduct, args []string) error {
	fs := newFlagSet(p.group+" engines", "Usage: th "+p.group+" engines\n\nLists the engines and versions a new "+p.noun+" can run.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	engines, err := c.dbFetchEngines(cl)
	if err != nil {
		return err
	}
	mine := []dbEngine{}
	for _, e := range engines {
		if e.product() == p.group {
			mine = append(mine, e)
		}
	}
	if c.json() {
		return c.printJSON(mine)
	}
	rows := [][]string{}
	for _, e := range mine {
		def, avail := "", "yes"
		if e.Default {
			def = "yes"
		}
		if !e.Available {
			avail = "no"
		}
		rows = append(rows, []string{e.Family, e.Version, e.ID, def, avail})
	}
	c.table([]string{"ENGINE", "VERSION", "ID", "DEFAULT", "AVAILABLE"}, rows)
	return nil
}

func dbSizes(c *cmd, p *dbProduct, args []string) error {
	fs := newFlagSet(p.group+" sizes", "Usage: th "+p.group+" sizes\n\nLists the sizes for --size.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	sizes, err := c.dbFetchSizes(cl, p)
	if err != nil {
		return err
	}
	if sizes == nil {
		sizes = []dbSize{}
	}
	if c.json() {
		return c.printJSON(sizes)
	}
	rows := [][]string{}
	for _, z := range sizes {
		cpu := vcpu(z.CPUQuota)
		if z.BurstCPUQuota > 0 {
			cpu += " (burst " + vcpu(z.BurstCPUQuota) + ")"
		}
		row := []string{z.ID, z.Name, dash(z.Series), cpu, dbMemText(z.MemoryMB)}
		if !p.isCache() {
			st := "-"
			if z.MaxStorageGB > 0 {
				st = fmt.Sprintf("%d–%d GB", max(z.MinStorageGB, 10), z.MaxStorageGB)
			}
			row = append(row, st)
		}
		rows = append(rows, row)
	}
	header := []string{"ID", "NAME", "SERIES", "VCPU", "MEMORY"}
	if !p.isCache() {
		header = append(header, "STORAGE")
	}
	c.table(header, rows)
	return nil
}
