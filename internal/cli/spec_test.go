package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

func TestParseCPU(t *testing.T) {
	ok := map[string]int64{"0.5": 50000, "1": 100000, "2": 200000, "0.25": 25000, "500m": 50000, "1.5": 150000}
	for in, want := range ok {
		got, err := parseCPU(in)
		if err != nil || got != want {
			t.Errorf("parseCPU(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-1", "abc", "100", "NaN"} {
		if _, err := parseCPU(in); err == nil {
			t.Errorf("parseCPU(%q) should fail", in)
		}
	}
}

func TestParseMemory(t *testing.T) {
	ok := map[string]int{"512Mi": 512, "1Gi": 1024, "2G": 2048, "256": 256, "1.5Gi": 1536, "128MiB": 128, "4gb": 4096, "512M": 512}
	for in, want := range ok {
		got, err := parseMemory(in)
		if err != nil || got != want {
			t.Errorf("parseMemory(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-1Gi", "lots", "0.5Mi", "1Ti"} {
		if _, err := parseMemory(in); err == nil {
			t.Errorf("parseMemory(%q) should fail", in)
		}
	}
}

func TestParseTimeout(t *testing.T) {
	ok := map[string]int{"300s": 300, "5m": 300, "60": 60, "1h": 3600}
	for in, want := range ok {
		got, err := parseTimeout(in)
		if err != nil || got != want {
			t.Errorf("parseTimeout(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0", "500ms", "x"} {
		if _, err := parseTimeout(in); err == nil {
			t.Errorf("parseTimeout(%q) should fail", in)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"api", "a", "web-1", "a" + strings.Repeat("b", 62)} {
		if err := validateName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Api", "1api", "-api", "a_b", "a" + strings.Repeat("b", 63)} {
		if validateName(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestBuildEnv(t *testing.T) {
	env, secrets, err := buildEnv([]string{"A=1", "URL=x=y"}, []string{"B=2,C=3"}, []string{"TOKEN=s"})
	if err != nil {
		t.Fatal(err)
	}
	if env["A"] != "1" || env["B"] != "2" || env["C"] != "3" || env["URL"] != "x=y" || env["TOKEN"] != "s" {
		t.Fatalf("env = %v", env)
	}
	if len(secrets) != 1 || secrets[0] != "TOKEN" {
		t.Fatalf("secrets = %v", secrets)
	}
	if env, _, _ := buildEnv(nil, nil, nil); env != nil {
		t.Fatal("no flags must give nil env")
	}
	if _, _, err := buildEnv([]string{"novalue"}, nil, nil); err == nil {
		t.Fatal("KEY without = should fail")
	}
}

func current() api.Service {
	return api.Service{
		ID: "s1", Image: "reg/p/api@sha256:old", Port: 3000, CPUQuota: 50000, CPUBurst: 100000, MemoryMB: 512,
		ComputeClass: "general", Command: []string{"node"}, Args: []string{"server.js"}, MinReplicas: 1, MaxReplicas: 5,
		ContainerConcurrency: 80, RequestTimeoutSeconds: 300, CPUAlwaysAllocated: true, HealthCheckPath: "/healthz",
	}
}

func TestMergeSpecKeepsCurrent(t *testing.T) {
	spec := mergeSpec(current(), deployOpts{Image: "reg/p/api@sha256:new", Message: "m"})
	if spec.Image != "reg/p/api@sha256:new" || spec.Port != 3000 || spec.CPUQuota != 50000 || spec.MemoryMB != 512 ||
		spec.ComputeClass != "general" || spec.Command[0] != "node" || spec.Args[0] != "server.js" || *spec.MinReplicas != 1 ||
		spec.MaxReplicas != 5 || spec.ContainerConcurrency != 80 || spec.RequestTimeoutSeconds != 300 || !spec.CPUAlwaysAllocated ||
		spec.HealthCheckPath != "/healthz" || spec.Message != "m" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.CPUBurst == nil || *spec.CPUBurst != 100000 {
		t.Fatal("same size keeps its burst ceiling")
	}
	b, _ := json.Marshal(spec)
	if strings.Contains(string(b), `"env"`) {
		t.Fatalf("env must be left out without env flags: %s", b)
	}
}

func TestMergeSpecOverrides(t *testing.T) {
	cpu, mem, zero, port, cls := int64(200000), 2048, 0, 8080, "highcpu"
	cmd, args := []string{}, []string{"--fast"}
	f := false
	spec := mergeSpec(current(), deployOpts{
		CPUQuota: &cpu, MemoryMB: &mem, MinReplicas: &zero, Port: &port, ComputeClass: &cls,
		Command: &cmd, Args: &args, CPUAlways: &f, Env: map[string]string{"A": "1", "S": "x"}, SecretKeys: []string{"S"},
	})
	if spec.CPUQuota != 200000 || spec.MemoryMB != 2048 || *spec.MinReplicas != 0 || spec.Port != 8080 ||
		spec.ComputeClass != "highcpu" || len(spec.Command) != 0 || spec.Args[0] != "--fast" || spec.CPUAlwaysAllocated {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.CPUBurst != nil {
		t.Fatal("a new size must not carry the old burst ceiling")
	}
	if spec.Image != "reg/p/api@sha256:old" {
		t.Fatal("no new image keeps the current one")
	}
	b, _ := json.Marshal(spec)
	if !strings.Contains(string(b), `"env":{"A":"1","S":"x"}`) || !strings.Contains(string(b), `"secretEnvKeys":["S"]`) {
		t.Fatalf("env not sent: %s", b)
	}
	if !strings.Contains(string(b), `"minReplicas":0`) || !strings.Contains(string(b), `"command":[]`) {
		t.Fatalf("zero values must be sent: %s", b)
	}
}
