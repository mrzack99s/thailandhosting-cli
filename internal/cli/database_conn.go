package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
)

// dbConnection is GET /services/{id}/connection.
type dbConnection struct {
	Engine       dbEngine `json:"engine"`
	StorageBytes int64    `json:"storageBytes"`
	DiskType     string   `json:"diskType"`
	Credentials  struct {
		User           string `json:"user"`
		Password       string `json:"password"`
		Database       string `json:"database"`
		PasswordHidden bool   `json:"passwordHidden"`
	} `json:"credentials"`
	Private struct {
		IP   string `json:"ip"`
		DNS  string `json:"dns"`
		Port int    `json:"port"`
		URL  string `json:"url"`
	} `json:"private"`
	Public struct {
		Enabled        bool     `json:"enabled"`
		Available      bool     `json:"available"`
		TLSCertificate string   `json:"tlsCertificate"`
		Hostname       string   `json:"hostname"`
		Port           int      `json:"port"`
		LoginUser      string   `json:"loginUser"`
		SNIRequired    bool     `json:"sniRequired"`
		URL            string   `json:"url"`
		AllowedCIDRs   []string `json:"allowedCidrs"`
		BandwidthMbps  int      `json:"bandwidthMbps"`
		SelfSignedTLS  bool     `json:"selfSignedTls"`
	} `json:"public"`
}

func (c *cmd) dbConnection(p *dbProduct, name string) (*dbService, *dbConnection, error) {
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return nil, nil, err
	}
	var conn dbConnection
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+s.ID+"/connection", nil, &conn); err != nil {
		return nil, nil, err
	}
	return s, &conn, nil
}

// dbRevealURL puts the real password where the Console masks it.
func dbRevealURL(raw, password string) string {
	if raw == "" || password == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), password)
	return u.String()
}

func dbEnableHint(p *dbProduct, slug string) string {
	return fmt.Sprintf("th %s public %s --enable --allow <your-ip>/32", p.group, slug)
}

func dbCredentialsCmd(c *cmd, p *dbProduct, args []string) error {
	usage := fmt.Sprintf("th %s credentials NAME [--format json]", p.group)
	fs := newFlagSet(p.group+" credentials", "Usage: "+usage+"\n\nPrints the user, password and connection URLs of a "+p.noun+".\nThe password is shown only to members who may operate it.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	name, err := dbOneArg(fs, usage)
	if err != nil {
		return err
	}
	s, conn, err := c.dbConnection(p, name)
	if err != nil {
		return err
	}
	cr := conn.Credentials
	pw := cr.Password
	if cr.PasswordHidden {
		pw = ""
	}
	out := struct {
		User           string `json:"user"`
		Password       string `json:"password,omitempty"`
		PasswordHidden bool   `json:"passwordHidden,omitempty"`
		Database       string `json:"database,omitempty"`
		PrivateHost    string `json:"privateHost"`
		PrivatePort    int    `json:"privatePort"`
		PrivateURL     string `json:"privateUrl"`
		PublicEnabled  bool   `json:"publicEnabled"`
		PublicHost     string `json:"publicHost,omitempty"`
		PublicPort     int    `json:"publicPort,omitempty"`
		PublicUser     string `json:"publicLoginUser,omitempty"`
		PublicURL      string `json:"publicUrl,omitempty"`
	}{
		User: cr.User, Password: pw, PasswordHidden: pw == "", Database: cr.Database,
		PrivateHost: conn.Private.DNS, PrivatePort: conn.Private.Port, PrivateURL: dbRevealURL(conn.Private.URL, pw),
		PublicEnabled: conn.Public.Enabled,
	}
	if out.PrivateHost == "" {
		out.PrivateHost = conn.Private.IP
	}
	if conn.Public.Enabled {
		out.PublicHost, out.PublicPort, out.PublicURL = conn.Public.Hostname, conn.Public.Port, dbRevealURL(conn.Public.URL, pw)
		if conn.Engine.Protocol != "redis" {
			out.PublicUser = conn.Public.LoginUser
		}
	}
	if c.json() {
		return c.printJSON(out)
	}
	w := c.app.Stdout
	fmt.Fprintf(w, "User:         %s\n", dash(out.User))
	if pw != "" {
		fmt.Fprintf(w, "Password:     %s\n", pw)
	} else {
		fmt.Fprintf(w, "Password:     (hidden — your role in this Project cannot see it)\n")
	}
	if out.Database != "" {
		fmt.Fprintf(w, "Database:     %s\n", out.Database)
	}
	fmt.Fprintf(w, "Private URL:  %s\n", dash(out.PrivateURL))
	fmt.Fprintf(w, "              (reachable only from inside the Project's VPC)\n")
	if conn.Public.Enabled {
		fmt.Fprintf(w, "Public URL:   %s\n", dash(out.PublicURL))
		if out.PublicUser != "" && out.PublicUser != out.User {
			fmt.Fprintf(w, "              (clients without TLS SNI log in as %s)\n", out.PublicUser)
		}
	} else {
		fmt.Fprintf(w, "Public URL:   off. Enable with: %s\n", dbEnableHint(p, s.Slug))
	}
	return nil
}

// ---- connect ----

// dbLookPath and dbExec are swapped by tests.
var dbLookPath = exec.LookPath

var dbExec = func(c *cmd, bin string, args, env []string) error {
	x := exec.CommandContext(c.ctx, bin, args...)
	x.Stdin, x.Stdout, x.Stderr = c.app.Stdin, c.app.Stdout, c.app.Stderr
	x.Env = append(os.Environ(), env...)
	// Ctrl+C belongs to the client (psql cancels the query); th waits.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	err := x.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &exitError{code: ee.ExitCode()}
	}
	return err
}

type dbClientCmd struct {
	Bin  string
	Args []string
}

// dbClientPlan is how to run the engine's own client against one endpoint.
type dbClientPlan struct {
	Candidates []dbClientCmd // tried in order; the first installed one runs
	Env        []string
	Install    string
	URL        string // with the password when known
}

func dbConninfo(k, v string) string {
	return k + "='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// dbConnectPlan builds the client command for the public (TLS) or private
// endpoint; extra arguments go to the client.
func dbConnectPlan(conn *dbConnection, public bool, extra []string) dbClientPlan {
	cr := conn.Credentials
	pw := cr.Password
	if cr.PasswordHidden {
		pw = ""
	}
	host, port, user, rawURL := conn.Private.DNS, conn.Private.Port, cr.User, conn.Private.URL
	if host == "" {
		host = conn.Private.IP
	}
	if public {
		host, port, rawURL = conn.Public.Hostname, conn.Public.Port, conn.Public.URL
		// The gateway also routes on user@server, so clients that send no
		// TLS SNI still reach the right database.
		if conn.Engine.Protocol != "redis" && conn.Public.LoginUser != "" {
			user = conn.Public.LoginUser
		}
	}
	plan := dbClientPlan{URL: dbRevealURL(rawURL, pw)}
	family := conn.Engine.Family
	switch conn.Engine.Protocol {
	case "postgres":
		mode := "disable"
		if public {
			mode = "require"
		}
		info := []string{dbConninfo("host", host), fmt.Sprintf("port=%d", port), dbConninfo("user", user)}
		if cr.Database != "" {
			info = append(info, dbConninfo("dbname", cr.Database))
		}
		info = append(info, "sslmode="+mode)
		plan.Candidates = []dbClientCmd{{Bin: "psql", Args: append([]string{strings.Join(info, " ")}, extra...)}}
		if pw != "" {
			plan.Env = append(plan.Env, "PGPASSWORD="+pw)
		}
		plan.Install = "Install the PostgreSQL client (psql): macOS: brew install libpq · Debian/Ubuntu: sudo apt install postgresql-client · Windows: winget install PostgreSQL.PostgreSQL"
	case "mysql":
		args := func(bin string) []string {
			a := []string{"-h", host, "-P", fmt.Sprint(port), "-u", user}
			if public {
				if bin == "mariadb" {
					a = append(a, "--ssl")
				} else {
					a = append(a, "--ssl-mode=REQUIRED")
				}
			}
			if cr.Database != "" {
				a = append(a, cr.Database)
			}
			return append(a, extra...)
		}
		bins := []string{"mysql", "mariadb"}
		plan.Install = "Install the MySQL client: macOS: brew install mysql-client · Debian/Ubuntu: sudo apt install mysql-client · Windows: winget install Oracle.MySQL"
		if family == "mariadb" {
			bins = []string{"mariadb", "mysql"}
			plan.Install = "Install the MariaDB client: macOS: brew install mariadb · Debian/Ubuntu: sudo apt install mariadb-client · Windows: winget install MariaDB.Server"
		}
		for _, b := range bins {
			plan.Candidates = append(plan.Candidates, dbClientCmd{Bin: b, Args: args(b)})
		}
		if pw != "" {
			plan.Env = append(plan.Env, "MYSQL_PWD="+pw)
		}
	default: // redis protocol
		a := []string{"-h", host, "-p", fmt.Sprint(port)}
		if user != "" {
			a = append(a, "--user", user)
		}
		if public {
			a = append(a, "--tls", "--sni", host)
			if conn.Public.SelfSignedTLS {
				a = append(a, "--insecure")
			}
		}
		a = append(a, extra...)
		bins := []string{"redis-cli", "valkey-cli"}
		plan.Install = "Install redis-cli 6 or newer (TLS support): macOS: brew install redis · Debian/Ubuntu: sudo apt install redis-tools · Windows: use WSL"
		if family == "valkey" {
			bins = []string{"valkey-cli", "redis-cli"}
			plan.Install = "Install valkey-cli (or redis-cli 6+): macOS: brew install valkey · Debian/Ubuntu: sudo apt install valkey-tools (or redis-tools) · Windows: use WSL"
		}
		for _, b := range bins {
			plan.Candidates = append(plan.Candidates, dbClientCmd{Bin: b, Args: a})
		}
		if pw != "" {
			plan.Env = append(plan.Env, "REDISCLI_AUTH="+pw)
		}
	}
	return plan
}

func dbConnect(c *cmd, p *dbProduct, args []string) error {
	var public, private bool
	usage := fmt.Sprintf("th %s connect NAME [--public | --private] [-- CLIENT-ARGS...]", p.group)
	fs := newFlagSet(p.group+" connect", "Usage: "+usage+`

Opens the engine's own client (psql, mysql/mariadb, redis-cli/valkey-cli)
with the credentials passed through the environment, never the command line.

By default it uses the public TLS endpoint, which must be enabled and allow
your address. --private uses the private address, which is reachable only
from inside the Project's VPC. Arguments after -- go to the client.`)
	fs.Bool(&public, "public", "Use the public TLS endpoint (default)")
	fs.Bool(&private, "private", "Use the private address (inside the VPC only)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) < 1 {
		return errors.New("usage: " + usage)
	}
	name, extra := fs.Args()[0], fs.Args()[1:]
	if public && private {
		return errors.New("use either --public or --private")
	}
	s, conn, err := c.dbConnection(p, name)
	if err != nil {
		return err
	}
	privAddr := fmt.Sprintf("%s:%d", dash(conn.Private.DNS), conn.Private.Port)
	if !private {
		if !conn.Public.Enabled {
			return fmt.Errorf("the public endpoint of %s [%s] is off, and its private address %s is reachable only from inside the Project's VPC.\nEnable public access for your address with:\n  %s\nor, from a machine inside the VPC, run: th %s connect %s --private",
				p.noun, s.Slug, privAddr, dbEnableHint(p, s.Slug), p.group, s.Slug)
		}
		if !conn.Public.Available {
			return fmt.Errorf("public endpoints are not available in this region yet; connect from inside the VPC with: th %s connect %s --private", p.group, s.Slug)
		}
		if len(conn.Public.AllowedCIDRs) == 0 {
			c.info("The public endpoint allows no addresses yet; add yours with: th %s public %s --allow <your-ip>/32", p.group, s.Slug)
		}
	} else {
		c.info("Connecting to the private address %s (reachable only from inside the Project's VPC).", privAddr)
	}
	if conn.Credentials.PasswordHidden || conn.Credentials.Password == "" {
		c.info("Your role in this Project cannot see the password; the client will ask for it.")
	}
	plan := dbConnectPlan(conn, !private, extra)
	for _, cand := range plan.Candidates {
		bin, err := dbLookPath(cand.Bin)
		if err != nil {
			continue
		}
		return dbExec(c, bin, cand.Args, plan.Env)
	}
	c.info("%s is not installed. %s", plan.Candidates[0].Bin, plan.Install)
	c.info("Connection URL (use it with any client or driver):")
	fmt.Fprintln(c.app.Stdout, plan.URL)
	return &exitError{code: 1}
}

// ---- public endpoint ----

func dbPublic(c *cmd, p *dbProduct, args []string) error {
	var enable, disable bool
	var allow []string
	var bandwidth int
	usage := fmt.Sprintf("th %s public NAME --enable|--disable [--allow CIDR,...] [--bandwidth MBPS]", p.group)
	fs := newFlagSet(p.group+" public", "Usage: "+usage+`

Turns the public TLS endpoint on or off. Only addresses in --allow may
connect (an empty list lets nobody in); --allow replaces the list. Without
--enable or --disable, only the list or bandwidth changes.`)
	fs.Bool(&enable, "enable", "Turn the public endpoint on")
	fs.Bool(&disable, "disable", "Turn the public endpoint off")
	fs.List(&allow, "allow", "Allowed addresses, e.g. 203.0.113.10/32 (repeatable, comma-separated; replaces the list)")
	fs.Int(&bandwidth, "bandwidth", "Bandwidth cap in Mbps (1–1000)")
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
	if !enable && !disable && !fs.Set("allow") && !fs.Set("bandwidth") {
		return errors.New("nothing to change: pass --enable, --disable, --allow or --bandwidth")
	}
	if fs.Set("bandwidth") && (bandwidth < 1 || bandwidth > 1000) {
		return errors.New("--bandwidth must be from 1 to 1000 Mbps")
	}
	cl, s, err := c.dbMust(p, name)
	if err != nil {
		return err
	}
	var cur dbConnection
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+s.ID+"/connection", nil, &cur); err != nil {
		return err
	}
	body := struct {
		PublicEnabled bool     `json:"publicEnabled"`
		AllowedCIDRs  []string `json:"allowedCidrs"`
		BandwidthMbps int      `json:"bandwidthMbps"`
	}{PublicEnabled: cur.Public.Enabled, AllowedCIDRs: cur.Public.AllowedCIDRs, BandwidthMbps: cur.Public.BandwidthMbps}
	if enable || disable {
		body.PublicEnabled = enable
	}
	if fs.Set("allow") {
		body.AllowedCIDRs = splitList(allow)
	}
	if body.AllowedCIDRs == nil {
		body.AllowedCIDRs = []string{}
	}
	if fs.Set("bandwidth") {
		body.BandwidthMbps = bandwidth
	}
	if body.BandwidthMbps == 0 {
		body.BandwidthMbps = 10
	}
	var out dbConnection
	raw, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/services/"+s.ID+"/connectivity", body, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	w := c.app.Stdout
	if !out.Public.Enabled {
		fmt.Fprintf(w, "Public endpoint of %s [%s] is off.\n", p.noun, s.Slug)
		return nil
	}
	fmt.Fprintf(w, "Public endpoint of %s [%s] is on: %s:%d (TLS)\n", p.noun, s.Slug, out.Public.Hostname, out.Public.Port)
	if len(out.Public.AllowedCIDRs) == 0 {
		fmt.Fprintf(w, "Allowed from: nobody yet. Add your address with: th %s public %s --allow <your-ip>/32\n", p.group, s.Slug)
	} else {
		fmt.Fprintf(w, "Allowed from: %s\n", strings.Join(out.Public.AllowedCIDRs, ", "))
	}
	fmt.Fprintf(w, "Bandwidth:    %d Mbps\n", out.Public.BandwidthMbps)
	fmt.Fprintf(w, "Connect with: th %s connect %s\n", p.group, s.Slug)
	return nil
}
