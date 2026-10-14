package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

const loginUsage = `Usage: th auth login [--no-browser]

Signs in with your ThailandHosting account. The CLI shows a code and opens
the Console in your browser; approve the code there. The token is saved in
the th config file and lasts 90 days.`

func authLogin(c *cmd, args []string) error {
	var noBrowser bool
	fs := newFlagSet("auth login", loginUsage)
	fs.Bool(&noBrowser, "no-browser", "Do not try to open a browser; just print the link")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if c.app.Getenv("TH_API_KEY") != "" {
		c.info("Note: TH_API_KEY is set and takes precedence over the token saved by this login.")
	}
	cl := api.New(c.endpoint(), "", c.userAgent())
	tok, err := deviceLogin(c, cl, noBrowser)
	if err != nil {
		return err
	}
	c.cfg.Token, c.cfg.Email = tok.Token, tok.Email
	if c.g.endpoint != "" {
		c.cfg.Endpoint = strings.TrimRight(c.g.endpoint, "/")
	}
	if err := c.saveConfig(); err != nil {
		return fmt.Errorf("signed in, but saving %s failed: %w", c.path, err)
	}
	fmt.Fprintf(c.app.Stdout, "\nYou are now logged in as [%s].\n", tok.Email)

	if c.projectSetting() == "" {
		cl.Token = tok.Token
		var projects []api.Project
		if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/projects", nil, &projects); err == nil && len(projects) > 0 {
			c.cfg.Project = projects[0].Slug
			if c.cfg.Project == "" {
				c.cfg.Project = projects[0].ID
			}
			if err := c.saveConfig(); err == nil {
				fmt.Fprintf(c.app.Stdout, "Your active project is now [%s]. Change it with: th config set project <id-or-slug>\n", c.cfg.Project)
			}
		}
	}
	return nil
}

// deviceLogin runs the device-code flow and returns the issued token.
func deviceLogin(c *cmd, cl *api.Client, noBrowser bool) (*api.TokenResponse, error) {
	host, _ := os.Hostname()
	clientName := fmt.Sprintf("th %s", c.app.Version)
	if host != "" {
		clientName += " (" + host + ")"
	}
	var dc api.DeviceCode
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/cli/device", map[string]string{"clientName": clientName}, &dc); err != nil {
		return nil, err
	}
	link := dc.VerificationURLComplete
	if link == "" {
		link = dc.VerificationURL
	}
	w := c.app.Stderr
	fmt.Fprintf(w, "Go to the following link in your browser, and approve the code:\n\n    %s\n\nCode: %s\n\n", link, dc.UserCode)
	if !noBrowser && c.app.OpenBrowser != nil {
		if err := c.app.OpenBrowser(link); err == nil {
			fmt.Fprintln(w, "Your browser has been opened. Waiting for approval…")
		} else {
			fmt.Fprintln(w, "Waiting for approval…")
		}
	} else {
		fmt.Fprintln(w, "Waiting for approval…")
	}

	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expires := time.Duration(dc.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 10 * time.Minute
	}
	var waited time.Duration
	for {
		if waited >= expires {
			return nil, errors.New("the code expired before it was approved. Run: th auth login")
		}
		c.app.Sleep(interval)
		waited += interval
		var tok api.TokenResponse
		_, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/cli/token", map[string]string{"deviceCode": dc.DeviceCode}, &tok)
		if err == nil {
			if tok.Token == "" {
				return nil, errors.New("the server approved the login but sent no token")
			}
			return &tok, nil
		}
		var ae *api.Error
		if !errors.As(err, &ae) {
			return nil, err
		}
		switch ae.Code {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired_token":
			return nil, errors.New("the code expired before it was approved. Run: th auth login")
		case "access_denied":
			return nil, errors.New("the login was denied in the Console")
		}
		return nil, err
	}
}

func authLogout(c *cmd, args []string) error {
	fs := newFlagSet("auth logout", "Usage: th auth logout\n\nSigns this CLI token out on the server and removes it from the config file.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if c.cfg.Token == "" {
		fmt.Fprintln(c.app.Stdout, "Not signed in.")
		return nil
	}
	cl := api.New(c.endpoint(), c.cfg.Token, c.userAgent())
	_, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/cli/logout", nil, nil)
	var ae *api.Error
	if err != nil && !(errors.As(err, &ae) && ae.Status == http.StatusUnauthorized) {
		c.info("Warning: could not sign the token out on the server (%v); removing it locally.", err)
	}
	email := c.cfg.Email
	c.cfg.Token, c.cfg.Email = "", ""
	if err := c.saveConfig(); err != nil {
		return err
	}
	// The short-lived data-plane keys went with the account's sign-in.
	_ = os.Remove(c.dataKeyPath())
	fmt.Fprintf(c.app.Stdout, "Signed out [%s].\n", dash(email))
	return nil
}

func authStatus(c *cmd, args []string) error {
	fs := newFlagSet(c.cmdPath, "Usage: th auth status\n\nShows who the CLI acts as (also: th auth whoami, th whoami).")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	var me api.WhoAmI
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/cli/whoami", nil, &me)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	kind := "CLI token"
	if me.Kind == "api_key" {
		kind = "API key (TH_API_KEY)"
	}
	fmt.Fprintf(c.app.Stdout, "Account:  %s\nAuth:     %s\nEndpoint: %s\n", me.Email, kind, c.endpoint())
	if me.ProjectID != "" {
		fmt.Fprintf(c.app.Stdout, "Project:  %s (fixed by the API key)\n", me.ProjectID)
	} else if p := c.projectSetting(); p != "" {
		fmt.Fprintf(c.app.Stdout, "Project:  %s\n", p)
	}
	if r := c.regionSetting(); r != "" {
		fmt.Fprintf(c.app.Stdout, "Region:   %s\n", r)
	}
	return nil
}

func authPrintToken(c *cmd, args []string) error {
	fs := newFlagSet("auth print-access-token", "Usage: th auth print-access-token\n\nPrints the token the CLI sends (TH_API_KEY, TH_TOKEN or the saved login).")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	tok, _ := c.token()
	if tok == "" {
		return errors.New("Not signed in. Run: th auth login")
	}
	fmt.Fprintln(c.app.Stdout, tok)
	return nil
}

func authConfigureDocker(c *cmd, args []string) error {
	fs := newFlagSet("auth configure-docker", "Usage: th auth configure-docker\n\nLogs Docker in to the Project's container registry, so docker push\nand docker pull work with <host>/<project-slug>/<repository>.")
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
	var reg api.Registry
	if _, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/run/registry?projectId="+q(pid), nil, &reg); err != nil {
		return err
	}
	if reg.Host == "" {
		return errors.New("the server did not return a registry host")
	}
	tok, _ := c.token()
	if err := c.app.RunDockerLogin(reg.Host, tok); err != nil {
		return err
	}
	fmt.Fprintf(c.app.Stdout, "Docker is logged in to %s. Push images as %s/%s/<repository>:<tag>\n", reg.Host, reg.Host, dash(reg.Namespace))
	return nil
}

func dockerLogin(host, token string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker was not found in PATH")
	}
	cmd := exec.Command("docker", "login", host, "-u", "th", "--password-stdin")
	cmd.Stdin = strings.NewReader(token)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker login failed: %w", err)
	}
	return nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
