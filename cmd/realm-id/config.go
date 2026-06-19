package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// defaultBFFURL / defaultIssuerURL are the production endpoints; overridable
// via config or the REALM_ID_BFF / REALM_ID_ISSUER env vars.
const (
	defaultBFFURL    = "https://api.realmid.dev"
	defaultIssuerURL = "https://auth.realmid.dev"
)

// envOr reads an environment variable via os.Environ() and returns def when
// unset. (This standalone CLI reads env directly; the repo's GoFr-service
// lint rule about config access does not apply here.)
func envOr(key, def string) string {
	prefix := key + "="
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return def
}

// Config is the persisted CLI state at ~/.config/realm-id/config.json (0600).
// SessionToken is the BFF session bearer minted by `auth login`.
type Config struct {
	BFFURL       string `json:"bff_url,omitempty"`
	IssuerURL    string `json:"issuer_url,omitempty"`
	Platform     string `json:"platform,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
}

// configPath honours XDG (~/.config/realm-id/config.json), with a
// REALM_ID_CONFIG override.
func configPath() (string, error) {
	if p := envOr("REALM_ID_CONFIG", ""); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", herr
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "realm-id", "config.json"), nil
}

func loadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // empty config is fine
		}
		return nil, err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func saveConfig(cfg *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file holds the session bearer.
	return os.WriteFile(p, b, 0o600)
}

// bffURL resolves the effective BFF base URL: env > config > default.
func (c *Config) bffURL() string {
	if v := envOr("REALM_ID_BFF", ""); v != "" {
		return v
	}
	if c.BFFURL != "" {
		return c.BFFURL
	}
	return defaultBFFURL
}

// issuerURL resolves the effective issuer base URL: env > config > default.
func (c *Config) issuerURL() string {
	if v := envOr("REALM_ID_ISSUER", ""); v != "" {
		return v
	}
	if c.IssuerURL != "" {
		return c.IssuerURL
	}
	return defaultIssuerURL
}
