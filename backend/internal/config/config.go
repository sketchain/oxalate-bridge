// Package config reads the process configuration from environment variables.
// Runtime-tunable parameters live in the settings table instead.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strings"
)

// Config is the static process configuration.
type Config struct {
	Listen                 string
	MetricsListen          string
	DatabaseURL            string
	Schema                 string
	CPAQuotaSchema         string
	RedisURL               string
	SecretKey              []byte
	TrustedProxies         []netip.Prefix
	BootstrapAdminUsername string
	BootstrapAdminPassword string
	// CookieSecure marks the session cookie Secure; on by default.
	CookieSecure bool
}

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// Load reads the configuration from the environment.
func Load() (Config, error) {
	return FromLookup(os.LookupEnv)
}

// FromLookup reads the configuration through lookup, for tests.
func FromLookup(lookup func(string) (string, bool)) (Config, error) {
	get := func(name, def string) string {
		if v, ok := lookup(name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	cfg := Config{
		Listen:                 get("BRIDGE_LISTEN", ":8080"),
		MetricsListen:          get("BRIDGE_METRICS_LISTEN", "127.0.0.1:9090"),
		DatabaseURL:            get("BRIDGE_DATABASE_URL", ""),
		Schema:                 get("BRIDGE_SCHEMA", "bridge"),
		CPAQuotaSchema:         get("BRIDGE_CPA_QUOTA_SCHEMA", "cpa_quota"),
		RedisURL:               get("BRIDGE_REDIS_URL", ""),
		BootstrapAdminUsername: get("BRIDGE_BOOTSTRAP_ADMIN_USERNAME", ""),
		BootstrapAdminPassword: get("BRIDGE_BOOTSTRAP_ADMIN_PASSWORD", ""),
		CookieSecure:           get("BRIDGE_COOKIE_SECURE", "true") != "false",
	}
	var errs []error
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("BRIDGE_DATABASE_URL is required"))
	}
	if cfg.RedisURL == "" {
		errs = append(errs, errors.New("BRIDGE_REDIS_URL is required"))
	}
	if !identRe.MatchString(cfg.Schema) {
		errs = append(errs, fmt.Errorf("BRIDGE_SCHEMA %q is not a plain lower-case identifier", cfg.Schema))
	}
	if !identRe.MatchString(cfg.CPAQuotaSchema) {
		errs = append(errs, fmt.Errorf("BRIDGE_CPA_QUOTA_SCHEMA %q is not a plain lower-case identifier", cfg.CPAQuotaSchema))
	}
	key, err := base64.StdEncoding.DecodeString(get("BRIDGE_SECRET_KEY", ""))
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("BRIDGE_SECRET_KEY is not base64: %w", err))
	case len(key) != 32:
		errs = append(errs, fmt.Errorf("BRIDGE_SECRET_KEY must decode to 32 bytes, got %d", len(key)))
	}
	cfg.SecretKey = key
	for _, raw := range strings.Split(get("BRIDGE_TRUSTED_PROXIES", ""), ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				errs = append(errs, fmt.Errorf("BRIDGE_TRUSTED_PROXIES: %w", err))
				continue
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("BRIDGE_TRUSTED_PROXIES: %w", err))
			continue
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, prefix.Masked())
	}
	if (cfg.BootstrapAdminUsername == "") != (cfg.BootstrapAdminPassword == "") {
		errs = append(errs, errors.New("BRIDGE_BOOTSTRAP_ADMIN_USERNAME and BRIDGE_BOOTSTRAP_ADMIN_PASSWORD must be set together"))
	}
	return cfg, errors.Join(errs...)
}
