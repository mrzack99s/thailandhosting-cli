package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

// Data-plane keys. Object Storage and the Queue Service take access keys,
// not the CLI token. With TH_OBJECT_STORAGE_KEY_ID / _SECRET (or
// TH_QUEUE_KEY_ID / _SECRET) set — CI, or a key made in the Console — the
// CLI uses that key. Otherwise a signed-in CLI asks the Console for a
// short-lived key of the Project (POST /api/v1/cli/data-key, valid 12 hours)
// and keeps it next to the config file (datakeys.json, mode 0600) until
// shortly before it expires.

// DataKey is an access key for a data plane.
type DataKey struct {
	KeyID     string    `json:"keyId"`
	Secret    string    `json:"secret"`
	ExpiresAt time.Time `json:"expiresAt"`
}

const (
	dataKeyObjects = "object-storage"
	dataKeyQueues  = "queue"
	dataKeyMargin  = 15 * time.Minute
)

var dataKeyEnv = map[string][2]string{
	dataKeyObjects: {"TH_OBJECT_STORAGE_KEY_ID", "TH_OBJECT_STORAGE_SECRET"},
	dataKeyQueues:  {"TH_QUEUE_KEY_ID", "TH_QUEUE_SECRET"},
}

func (c *cmd) dataKeyPath() string { return filepath.Join(filepath.Dir(c.path), "datakeys.json") }

// dataKey returns a key for service ("object-storage" or "queue") in the
// Project projectID.
func (c *cmd) dataKey(cl *api.Client, service, projectID string) (DataKey, error) {
	env := dataKeyEnv[service]
	if id, secret := c.app.Getenv(env[0]), c.app.Getenv(env[1]); id != "" || secret != "" {
		if id == "" || secret == "" {
			return DataKey{}, fmt.Errorf("set both %s and %s", env[0], env[1])
		}
		return DataKey{KeyID: id, Secret: secret}, nil
	}
	if _, isKey := c.token(); isKey {
		return DataKey{}, fmt.Errorf("with TH_API_KEY, set %s and %s to an access key made in the Console", env[0], env[1])
	}
	cache := map[string]DataKey{}
	if b, err := os.ReadFile(c.dataKeyPath()); err == nil {
		_ = json.Unmarshal(b, &cache)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return DataKey{}, err
	}
	slot := c.endpoint() + "|" + projectID + "|" + service
	if k, ok := cache[slot]; ok && time.Until(k.ExpiresAt) > dataKeyMargin {
		return k, nil
	}
	var k DataKey
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/cli/data-key", map[string]string{"projectId": projectID, "service": service}, &k); err != nil {
		return DataKey{}, err
	}
	for s, old := range cache {
		if time.Now().After(old.ExpiresAt) {
			delete(cache, s)
		}
	}
	cache[slot] = k
	if b, err := json.MarshalIndent(cache, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(c.dataKeyPath()), 0o700)
		_ = os.WriteFile(c.dataKeyPath(), b, 0o600)
	}
	return k, nil
}

// forgetDataKey drops a cached key the data plane refused (deleted early).
func (c *cmd) forgetDataKey(service, projectID string) {
	cache := map[string]DataKey{}
	b, err := os.ReadFile(c.dataKeyPath())
	if err != nil || json.Unmarshal(b, &cache) != nil {
		return
	}
	delete(cache, c.endpoint()+"|"+projectID+"|"+service)
	if b, err := json.MarshalIndent(cache, "", "  "); err == nil {
		_ = os.WriteFile(c.dataKeyPath(), b, 0o600)
	}
}
