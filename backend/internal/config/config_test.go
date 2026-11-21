package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func lookup(extra map[string]string) func(string) (string, bool) {
	env := map[string]string{
		"BRIDGE_DATABASE_URL": "postgres://x", "BRIDGE_REDIS_URL": "redis://x",
		"BRIDGE_SECRET_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	for k, v := range extra {
		env[k] = v
	}
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestTimezone(t *testing.T) {
	cfg, err := FromLookup(lookup(nil))
	if err != nil || cfg.Location == nil || cfg.Location.String() != "Asia/Shanghai" {
		t.Fatalf("default: %v %v", cfg.Location, err)
	}
	cfg, err = FromLookup(lookup(map[string]string{"BRIDGE_TIMEZONE": "UTC"}))
	if err != nil || cfg.Location.String() != "UTC" {
		t.Fatalf("utc: %v %v", cfg.Location, err)
	}
	if _, err = FromLookup(lookup(map[string]string{"BRIDGE_TIMEZONE": "Mars/Base"})); err == nil || !strings.Contains(err.Error(), "BRIDGE_TIMEZONE") {
		t.Fatalf("invalid zone accepted: %v", err)
	}
}
