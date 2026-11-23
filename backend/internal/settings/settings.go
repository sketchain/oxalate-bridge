// Package settings holds the runtime-tunable parameters. Values are stored in
// the settings table; every replica keeps a copy in memory and reloads it
// when a change is announced over Redis pub/sub.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync/atomic"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
)

// Values is the full set of runtime settings.
type Values struct {
	DefaultUserConcurrency   int                `json:"default_user_concurrency"`
	Watermark5hPercent       float64            `json:"watermark_5h_percent"`
	WatermarkPenalty         float64            `json:"watermark_penalty"`
	StandardMinHeadroomMicro int64              `json:"standard_min_headroom_micro"`
	StickyTTLSeconds         int                `json:"sticky_ttl_seconds"`
	StickyTTL1hSeconds       int                `json:"sticky_ttl_1h_seconds"`
	TierMultipliers          TierMultipliers    `json:"tier_multipliers"`
	ModelMultipliers         map[string]float64 `json:"model_multipliers"`
	HoldCapMicro             int64              `json:"hold_cap_micro"`
	HoldDefaultMaxTokens     int64              `json:"hold_default_max_tokens"`
	MaxRetries               int                `json:"max_retries"`
	QuotaPollSeconds         int                `json:"quota_poll_seconds"`
	StaleAfterSeconds        int                `json:"stale_after_seconds"`
	ModelsPollMinutes        int                `json:"models_poll_minutes"`
	PriceURL                 string             `json:"price_url"`
	PriceRefreshHours        int                `json:"price_refresh_hours"`
	UpstreamIdleTimeoutSecs  int                `json:"upstream_idle_timeout_seconds"`
	AutoRenewLeadHours       int                `json:"auto_renew_lead_hours"`
	Cooldown5xxSeconds       int                `json:"cooldown_5xx_seconds"`
	Cooldown429Seconds       int                `json:"cooldown_429_default_seconds"`
	PublicQueryIPPerMinute   int                `json:"public_query_ip_per_minute"`
	PublicQueryKeyPerMinute  int                `json:"public_query_key_per_minute"`
	StatusKeyPerMinute       int                `json:"status_key_per_minute"`
	AdminTokenPerMinute      int                `json:"admin_token_per_minute"`
	StreamBacklogThreshold   int                `json:"stream_backlog_threshold"`
	// VAPIDSubject is the Web Push contact; empty = BRIDGE_VAPID_SUBJECT.
	VAPIDSubject string `json:"vapid_subject"`
}

// Bounds of the public endpoint rate limits (requests per minute).
const (
	MinPublicRate = 1
	MaxPublicRate = 10000
)

// TierMultipliers are the billing multipliers of the three tiers.
type TierMultipliers struct {
	Plan     float64 `json:"plan"`
	Standard float64 `json:"standard"`
	Force    float64 `json:"force"`
}

// For returns the multiplier of a tier; unknown tiers bill at 1.
func (t TierMultipliers) For(tier string) float64 {
	switch tier {
	case "plan":
		return t.Plan
	case "standard":
		return t.Standard
	case "force":
		return t.Force
	}
	return 1
}

// Defaults are the values used for keys absent from the table.
func Defaults() Values {
	return Values{
		DefaultUserConcurrency:   3,
		Watermark5hPercent:       70,
		WatermarkPenalty:         0.1,
		StandardMinHeadroomMicro: 500_000,
		StickyTTLSeconds:         300,
		StickyTTL1hSeconds:       3600,
		TierMultipliers:          TierMultipliers{Plan: 1, Standard: 1, Force: 1},
		ModelMultipliers:         map[string]float64{},
		HoldCapMicro:             1_000_000,
		HoldDefaultMaxTokens:     4096,
		MaxRetries:               2,
		QuotaPollSeconds:         30,
		StaleAfterSeconds:        180,
		ModelsPollMinutes:        10,
		PriceURL:                 "https://toll.icmp.day/api/ratio_config",
		PriceRefreshHours:        24,
		UpstreamIdleTimeoutSecs:  300,
		AutoRenewLeadHours:       24,
		Cooldown5xxSeconds:       30,
		Cooldown429Seconds:       60,
		PublicQueryIPPerMinute:   30,
		PublicQueryKeyPerMinute:  30,
		StatusKeyPerMinute:       60,
		AdminTokenPerMinute:      120,
		StreamBacklogThreshold:   1000,
	}
}

// ModelMultiplier returns the multiplier of a normalized model, default 1.
func (v Values) ModelMultiplier(model string) float64 {
	if m, ok := v.ModelMultipliers[model]; ok {
		return m
	}
	return 1
}

// Validate rejects values that would break routing or billing.
func (v Values) Validate() error {
	switch {
	case v.DefaultUserConcurrency < 0:
		return fmt.Errorf("default_user_concurrency must be >= 0")
	case v.Watermark5hPercent < 0 || v.Watermark5hPercent > 100:
		return fmt.Errorf("watermark_5h_percent must be within 0..100")
	case v.WatermarkPenalty < 0 || v.WatermarkPenalty > 1:
		return fmt.Errorf("watermark_penalty must be within 0..1")
	case v.StickyTTLSeconds < 1 || v.StickyTTL1hSeconds < 1:
		return fmt.Errorf("sticky TTLs must be positive")
	case v.TierMultipliers.Plan < 0 || v.TierMultipliers.Standard < 0 || v.TierMultipliers.Force < 0:
		return fmt.Errorf("tier multipliers must be >= 0")
	case v.HoldCapMicro < 0:
		return fmt.Errorf("hold_cap_micro must be >= 0")
	case v.HoldDefaultMaxTokens < 1:
		return fmt.Errorf("hold_default_max_tokens must be positive")
	case v.MaxRetries < 0 || v.MaxRetries > 10:
		return fmt.Errorf("max_retries must be within 0..10")
	case v.QuotaPollSeconds < 5:
		return fmt.Errorf("quota_poll_seconds must be >= 5")
	case v.StaleAfterSeconds < v.QuotaPollSeconds:
		return fmt.Errorf("stale_after_seconds must be >= quota_poll_seconds")
	case v.ModelsPollMinutes < 1:
		return fmt.Errorf("models_poll_minutes must be >= 1")
	case v.PriceRefreshHours < 1:
		return fmt.Errorf("price_refresh_hours must be >= 1")
	case v.UpstreamIdleTimeoutSecs < 10:
		return fmt.Errorf("upstream_idle_timeout_seconds must be >= 10")
	case v.AutoRenewLeadHours < 0:
		return fmt.Errorf("auto_renew_lead_hours must be >= 0")
	case v.Cooldown5xxSeconds < 0 || v.Cooldown429Seconds < 0:
		return fmt.Errorf("cooldowns must be >= 0")
	case !rateOK(v.PublicQueryIPPerMinute):
		return fmt.Errorf("public_query_ip_per_minute must be within %d..%d", MinPublicRate, MaxPublicRate)
	case !rateOK(v.PublicQueryKeyPerMinute):
		return fmt.Errorf("public_query_key_per_minute must be within %d..%d", MinPublicRate, MaxPublicRate)
	case !rateOK(v.StatusKeyPerMinute):
		return fmt.Errorf("status_key_per_minute must be within %d..%d", MinPublicRate, MaxPublicRate)
	case !rateOK(v.AdminTokenPerMinute):
		return fmt.Errorf("admin_token_per_minute must be within %d..%d", MinPublicRate, MaxPublicRate)
	case v.StreamBacklogThreshold < 1:
		return fmt.Errorf("stream_backlog_threshold must be >= 1")
	case v.VAPIDSubject != "" && !strings.HasPrefix(v.VAPIDSubject, "mailto:") && !strings.HasPrefix(v.VAPIDSubject, "https://"):
		return fmt.Errorf("vapid_subject must start with mailto: or https://")
	}
	for model, m := range v.ModelMultipliers {
		if m < 0 {
			return fmt.Errorf("model multiplier of %q must be >= 0", model)
		}
	}
	return nil
}

// Store is the in-memory copy of the settings.
type Store struct {
	cur atomic.Pointer[Values]
}

// NewStore returns a store holding the defaults.
func NewStore() *Store {
	s := &Store{}
	d := Defaults()
	s.cur.Store(&d)
	return s
}

// Get returns the current values; callers must not mutate maps in it.
func (s *Store) Get() Values { return *s.cur.Load() }

// Set replaces the values (used by tests and Load).
func (s *Store) Set(v Values) { s.cur.Store(&v) }

// Load reads the table and overlays it on the defaults.
func (s *Store) Load(ctx context.Context, q *sqlc.Queries) error {
	rows, err := q.ListSettings(ctx)
	if err != nil {
		return err
	}
	v, err := Merge(Defaults(), rows)
	if err != nil {
		return err
	}
	s.Set(v)
	return nil
}

// Merge overlays stored rows on base. Unknown keys are ignored.
func Merge(base Values, rows []sqlc.Setting) (Values, error) {
	raw, err := json.Marshal(base)
	if err != nil {
		return base, err
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return base, err
	}
	for _, row := range rows {
		if _, ok := m[row.Key]; ok {
			m[row.Key] = row.Value
		}
	}
	raw, _ = json.Marshal(m)
	out := Values{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return base, fmt.Errorf("decode settings: %w", err)
	}
	if out.ModelMultipliers == nil {
		out.ModelMultipliers = map[string]float64{}
	}
	return out, nil
}

// ToMap renders values as key -> JSON for storage and the API.
func ToMap(v Values) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	return m, json.Unmarshal(raw, &m)
}

// Patch applies a partial key -> JSON update to v, rejecting unknown keys.
func Patch(v Values, patch map[string]json.RawMessage) (Values, []string, error) {
	cur, err := ToMap(v)
	if err != nil {
		return v, nil, err
	}
	next := maps.Clone(cur)
	var changed []string
	for k, val := range patch {
		if _, ok := cur[k]; !ok {
			return v, nil, fmt.Errorf("unknown setting %q", k)
		}
		next[k] = val
		changed = append(changed, k)
	}
	raw, _ := json.Marshal(next)
	out := Values{}
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return v, nil, fmt.Errorf("invalid settings: %w", err)
	}
	if out.ModelMultipliers == nil {
		out.ModelMultipliers = map[string]float64{}
	}
	if err := out.Validate(); err != nil {
		return v, nil, err
	}
	return out, changed, nil
}

func rateOK(n int) bool { return n >= MinPublicRate && n <= MaxPublicRate }
