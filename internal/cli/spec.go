package cli

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

// cpuPeriod is one vCPU in cpuQuota units.
const cpuPeriod = 100000

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func validateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid service name %q: use lowercase letters, digits and -, starting with a letter (at most 63 characters)", name)
	}
	return nil
}

// parseCPU turns "0.5", "1", "2" or "500m" into a cpuQuota (vCPU × 100000).
func parseCPU(s string) (int64, error) {
	s = strings.TrimSpace(s)
	var v float64
	var err error
	if m, ok := strings.CutSuffix(s, "m"); ok {
		v, err = strconv.ParseFloat(m, 64)
		v /= 1000
	} else {
		v, err = strconv.ParseFloat(s, 64)
	}
	if err != nil || math.IsNaN(v) || v <= 0 || v > 64 {
		return 0, fmt.Errorf("invalid --cpu %q: use a vCPU count such as 0.25, 0.5, 1 or 2", s)
	}
	return int64(math.Round(v * cpuPeriod)), nil
}

// parseMemory turns "512Mi", "1Gi", "1.5G" or "512" (MiB) into MB.
func parseMemory(s string) (int, error) {
	t := strings.TrimSpace(s)
	mult := 1.0
	lower := strings.ToLower(t)
	for _, suf := range []struct {
		s string
		m float64
	}{{"gib", 1024}, {"gi", 1024}, {"gb", 1024}, {"g", 1024}, {"mib", 1}, {"mi", 1}, {"mb", 1}, {"m", 1}} {
		if strings.HasSuffix(lower, suf.s) {
			t, mult = t[:len(t)-len(suf.s)], suf.m
			break
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	mb := v * mult
	if err != nil || math.IsNaN(mb) || mb < 1 || mb > 1024*1024 || mb != math.Trunc(mb) {
		return 0, fmt.Errorf("invalid --memory %q: use a size such as 256Mi, 512Mi, 1Gi or 2Gi", s)
	}
	return int(mb), nil
}

// parseTimeout turns "300s", "5m" or "300" into seconds.
func parseTimeout(s string) (int, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Second || d%time.Second != 0 {
		return 0, fmt.Errorf("invalid --timeout %q: use seconds such as 300s or 5m", s)
	}
	return int(d / time.Second), nil
}

func parseKV(s, flag string) (string, string, error) {
	k, v, ok := strings.Cut(s, "=")
	k = strings.TrimSpace(k)
	if !ok || k == "" {
		return "", "", fmt.Errorf("%s takes KEY=VALUE, not %q", flag, s)
	}
	return k, v, nil
}

// splitList splits comma-separated values from repeatable flags.
func splitList(vals []string) []string {
	var out []string
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			if p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// deployOpts are the flag values of th container deploy; nil means "not given".
type deployOpts struct {
	Image          string
	Port           *int
	CPUQuota       *int64
	MemoryMB       *int
	ComputeClass   *string
	Command        *[]string
	Args           *[]string
	Env            map[string]string // nil: no env flags
	SecretKeys     []string
	MinReplicas    *int
	MaxReplicas    *int
	Concurrency    *int
	TimeoutSeconds *int
	CPUAlways      *bool
	HealthPath     *string
	AllowUnauth    *bool
	Message        string
}

// buildEnv merges --env, --set-env-vars and --secret-env. It returns nil
// when none was given.
func buildEnv(env, setEnv, secret []string) (map[string]string, []string, error) {
	if len(env) == 0 && len(setEnv) == 0 && len(secret) == 0 {
		return nil, nil, nil
	}
	out := map[string]string{}
	for _, s := range setEnv {
		for _, p := range strings.Split(s, ",") {
			if strings.TrimSpace(p) == "" {
				continue
			}
			k, v, err := parseKV(p, "--set-env-vars")
			if err != nil {
				return nil, nil, err
			}
			out[k] = v
		}
	}
	for _, s := range env {
		k, v, err := parseKV(s, "--env")
		if err != nil {
			return nil, nil, err
		}
		out[k] = v
	}
	var keys []string
	for _, s := range secret {
		k, v, err := parseKV(s, "--secret-env")
		if err != nil {
			return nil, nil, err
		}
		out[k] = v
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return out, keys, nil
}

// mergeSpec lays the given flags over the service's current spec. Env is
// sent only when flags gave some (the server then replaces the whole set);
// otherwise it is left out and the server keeps the current variables.
func mergeSpec(cur api.Service, o deployOpts) api.ContainerSpec {
	min := cur.MinReplicas
	spec := api.ContainerSpec{
		Image:                 cur.Image,
		Port:                  cur.Port,
		CPUQuota:              cur.CPUQuota,
		MemoryMB:              cur.MemoryMB,
		ComputeClass:          cur.ComputeClass,
		Command:               cur.Command,
		Args:                  cur.Args,
		MinReplicas:           &min,
		MaxReplicas:           cur.MaxReplicas,
		ContainerConcurrency:  cur.ContainerConcurrency,
		RequestTimeoutSeconds: cur.RequestTimeoutSeconds,
		CPUAlwaysAllocated:    cur.CPUAlwaysAllocated,
		HealthCheckPath:       cur.HealthCheckPath,
		Message:               o.Message,
	}
	if o.Image != "" {
		spec.Image = o.Image
	}
	if o.Port != nil {
		spec.Port = *o.Port
	}
	if o.CPUQuota != nil {
		spec.CPUQuota = *o.CPUQuota
	}
	if o.MemoryMB != nil {
		spec.MemoryMB = *o.MemoryMB
	}
	if o.ComputeClass != nil {
		spec.ComputeClass = *o.ComputeClass
	}
	if o.Command != nil {
		spec.Command = *o.Command
	}
	if o.Args != nil {
		spec.Args = *o.Args
	}
	if o.MinReplicas != nil {
		v := *o.MinReplicas
		spec.MinReplicas = &v
	}
	if o.MaxReplicas != nil {
		spec.MaxReplicas = *o.MaxReplicas
	}
	if o.Concurrency != nil {
		spec.ContainerConcurrency = *o.Concurrency
	}
	if o.TimeoutSeconds != nil {
		spec.RequestTimeoutSeconds = *o.TimeoutSeconds
	}
	if o.CPUAlways != nil {
		spec.CPUAlwaysAllocated = *o.CPUAlways
	}
	if o.HealthPath != nil {
		spec.HealthCheckPath = *o.HealthPath
	}
	// A Burstable size keeps its ceiling only while vCPU and memory stay;
	// a new size is sent without one and becomes Standard.
	if spec.CPUQuota == cur.CPUQuota && spec.MemoryMB == cur.MemoryMB && cur.CPUBurst > 0 {
		b := cur.CPUBurst
		spec.CPUBurst = &b
	}
	if o.Env != nil {
		spec.Env = o.Env
		spec.SecretEnvKeys = o.SecretKeys
	}
	return spec
}

// createInput is the POST /services body for a new Container.
func createInput(name, projectID, regionID string, o deployOpts) api.CreateService {
	in := api.CreateService{
		Kind:                 "container",
		Engine:               "image",
		Name:                 name,
		Slug:                 name,
		ProjectID:            projectID,
		RegionID:             regionID,
		Image:                o.Image,
		Env:                  o.Env,
		SecretEnvKeys:        o.SecretKeys,
		MinReplicas:          o.MinReplicas,
		AllowUnauthenticated: o.AllowUnauth,
	}
	if o.Port != nil {
		in.Port = *o.Port
	}
	if o.CPUQuota != nil {
		in.CPUQuota = *o.CPUQuota
	}
	if o.MemoryMB != nil {
		in.MemoryMB = *o.MemoryMB
	}
	if o.ComputeClass != nil {
		in.ComputeClass = *o.ComputeClass
	}
	if o.Command != nil {
		in.Command = *o.Command
	}
	if o.Args != nil {
		in.Args = *o.Args
	}
	if o.MaxReplicas != nil {
		in.MaxReplicas = *o.MaxReplicas
	}
	if o.Concurrency != nil {
		in.ContainerConcurrency = *o.Concurrency
	}
	if o.TimeoutSeconds != nil {
		in.RequestTimeoutSeconds = *o.TimeoutSeconds
	}
	if o.CPUAlways != nil {
		in.CPUAlwaysAllocated = *o.CPUAlways
	}
	if o.HealthPath != nil {
		in.HealthCheckPath = *o.HealthPath
	}
	return in
}

func vcpu(quota int64) string {
	return strconv.FormatFloat(float64(quota)/cpuPeriod, 'f', -1, 64)
}
