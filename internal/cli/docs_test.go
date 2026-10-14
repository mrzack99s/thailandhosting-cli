package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// th __docs lists every command with its usage and flags, without running
// any of them (no server is reachable here).
func TestDocsExport(t *testing.T) {
	h := newHarness(t, "http://127.0.0.1:1")
	if code := h.run("__docs"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	var docs []docCommand
	if err := json.Unmarshal([]byte(h.out.String()), &docs); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, h.out.String()[:200])
	}
	byName := map[string]docCommand{}
	for _, d := range docs {
		if d.Usage == "" {
			t.Errorf("%s has no usage text", d.Command)
		}
		byName[d.Command] = d
	}
	deploy, ok := byName["container deploy"]
	if !ok || !strings.HasPrefix(deploy.Usage, "Usage: th container deploy") {
		t.Fatalf("container deploy: %+v", deploy)
	}
	var source, quiet bool
	for _, f := range deploy.Flags {
		source = source || (f.Name == "source" && !f.Global && f.Type == "string")
		quiet = quiet || (f.Name == "quiet" && f.Global && f.Type == "bool")
	}
	if !source || !quiet {
		t.Fatalf("flags: %+v", deploy.Flags)
	}
	if _, listed := byName["__docs"]; listed {
		t.Fatal("__docs lists itself")
	}
	if strings.Contains(h.out.String(), "th dev (") {
		t.Fatal("a command ran instead of being described")
	}
}
