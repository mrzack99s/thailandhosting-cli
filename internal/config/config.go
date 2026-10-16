// Package config reads and writes ~/.config/th/config.json (the OS user
// config directory + "/th").
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// DefaultEndpoint is the CLI API used when nothing else is set. The
// Console host (https://console.thailandhosting.com) answers too.
const DefaultEndpoint = "https://cloud.thailandhosting.com"

// Config is the saved CLI state.
type Config struct {
	Endpoint string `json:"endpoint,omitempty"`
	Token    string `json:"token,omitempty"`
	Email    string `json:"email,omitempty"`
	Project  string `json:"project,omitempty"`
	Region   string `json:"region,omitempty"`
}

// Keys are the properties `th config` may set.
var Keys = []string{"endpoint", "project", "region"}

// DefaultPath is <user config dir>/th/config.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "th", "config.json"), nil
}

// Load reads the file; a missing file is an empty config.
func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(b) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return c, nil
}

// Save writes the file with mode 0600, creating its directory.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Chmod(path, 0o600)
}

// Get returns a property by name.
func (c Config) Get(key string) (string, error) {
	switch key {
	case "endpoint":
		return c.Endpoint, nil
	case "project":
		return c.Project, nil
	case "region":
		return c.Region, nil
	}
	return "", unknownKey(key)
}

// Set changes a property by name; an empty value clears it.
func (c *Config) Set(key, value string) error {
	switch key {
	case "endpoint":
		c.Endpoint = value
	case "project":
		c.Project = value
	case "region":
		c.Region = value
	default:
		return unknownKey(key)
	}
	return nil
}

// Values lists the settable properties that have a value.
func (c Config) Values() map[string]string {
	out := map[string]string{}
	for _, k := range Keys {
		if v, _ := c.Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

func unknownKey(key string) error {
	keys := append([]string(nil), Keys...)
	sort.Strings(keys)
	return fmt.Errorf("unknown property %q (one of: %v)", key, keys)
}
