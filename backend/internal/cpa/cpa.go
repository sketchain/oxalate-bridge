// Package cpa talks to CLIProxyAPI instances: the cpa-quota plugin's quota
// endpoint and the model list. It also defines the quota snapshot the bridge
// keeps per account and the local window-reset rule.
package cpa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// QuotaPath is the cpa-quota plugin's read-only quota endpoint.
const QuotaPath = "/v0/resource/plugins/cpa-quota/quota"

// Window is a five_hour or week window of the quota contract.
type Window struct {
	UsedPercent  float64    `json:"used_percent"`
	ResetAt      *time.Time `json:"reset_at"`
	TotalUSD     float64    `json:"total_usd"`
	RemainingUSD float64    `json:"remaining_usd"`
}

// Account is accounts[i] of the quota contract (only the fields the bridge
// relies on).
type Account struct {
	AuthIndex    string     `json:"auth_index"`
	Provider     string     `json:"provider"`
	Subscription string     `json:"subscription"`
	Tier         string     `json:"tier"`
	State        string     `json:"state"`
	SafetyFactor float64    `json:"safety_factor"`
	ObservedAt   *time.Time `json:"observed_at"`
	// K is the plugin's k = week total ÷ 5-hour total (absent on older plugins).
	K        *float64 `json:"k,omitempty"`
	FiveHour *Window  `json:"five_hour"`
	Week     *Window  `json:"week"`
}

// QuotaResponse is the quota endpoint response.
type QuotaResponse struct {
	Instance     string `json:"instance"`
	Subscription string `json:"subscription"`
	Tier         string `json:"tier"`
	Database     struct {
		Status string `json:"status"`
	} `json:"database"`
	Estimates struct {
		Loaded bool `json:"loaded"`
	} `json:"estimates"`
	Accounts []Account `json:"accounts"`
}

// SubscriptionClaude is the only subscription the bridge serves.
const SubscriptionClaude = "claude"

// IsClaude reports whether a /quota response describes a Claude
// subscription (both the instance and its account must say so).
func IsClaude(q *QuotaResponse) bool {
	if q == nil || q.Subscription != SubscriptionClaude || len(q.Accounts) == 0 {
		return false
	}
	s := q.Accounts[0].Subscription
	return s == "" || s == SubscriptionClaude
}

// Client calls CPA instances.
type Client struct {
	HTTP *http.Client
}

// NewClient returns a client with short timeouts suited to control calls.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// HTTPError is a non-2xx response.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("status %d: %s", e.Status, e.Body)
}

func joinURL(base, path string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid base url %q", base)
	}
	return strings.TrimRight(base, "/") + path, nil
}

func (c *Client) get(ctx context.Context, base, path, token string, out any) error {
	target, err := joinURL(base, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		snippet := string(body)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return &HTTPError{Status: resp.StatusCode, Body: snippet}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// Quota fetches the cpa-quota estimate.
func (c *Client) Quota(ctx context.Context, base, token string) (*QuotaResponse, error) {
	var out QuotaResponse
	if err := c.get(ctx, base, QuotaPath, token, &out); err != nil {
		return nil, err
	}
	if len(out.Accounts) == 0 {
		return nil, errors.New("quota response lists no account")
	}
	return &out, nil
}

// Models fetches the model ids an instance serves (/v1/models).
func (c *Client) Models(ctx context.Context, base, key string) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.get(ctx, base, "/v1/models", key, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// Snapshot is what the bridge stores per account after each poll.
type Snapshot struct {
	FetchedAt      time.Time `json:"fetched_at"`
	LastAttemptAt  time.Time `json:"last_attempt_at"`
	LastError      string    `json:"last_error,omitempty"`
	FailingSince   time.Time `json:"failing_since,omitzero"`
	DatabaseStatus string    `json:"database_status"`
	EstimatesReady bool      `json:"estimates_loaded"`
	Instance       string    `json:"instance"`
	Subscription   string    `json:"subscription"`
	Tier           string    `json:"tier"`
	Account        Account   `json:"account"`
	HasData        bool      `json:"has_data"`
}

// Stale reports whether the snapshot is too old or the plugin's database is
// unavailable.
func (s *Snapshot) Stale(now time.Time, staleAfter time.Duration) bool {
	if s == nil || !s.HasData {
		return true
	}
	if s.DatabaseStatus == "unavailable" {
		return true
	}
	return now.Sub(s.FetchedAt) > staleAfter
}

// EffectiveWindow applies the local reset rule: once now has passed the
// window's reset_at the window counts as fresh (used 0, remaining =
// safety_factor × total) without waiting for the next poll. The returned
// time is the instant from which local spend counts against the window
// (observed_at, or the reset when it happened after it).
func EffectiveWindow(w *Window, safety float64, observed time.Time, now time.Time) (Window, time.Time, bool) {
	if w == nil {
		return Window{}, observed, false
	}
	out := *w
	since := observed
	if w.ResetAt != nil && !now.Before(*w.ResetAt) {
		out.UsedPercent = 0
		out.RemainingUSD = safety * w.TotalUSD
		reset := *w.ResetAt
		if reset.After(since) {
			since = reset
		}
	}
	return out, since, true
}

// HardExhausted reports whether an account cannot take any request: paused
// by the plugin or a window at or above 100 % (after local resets).
func HardExhausted(a Account, now time.Time) (bool, string) {
	if Paused(a, now) {
		return true, "paused"
	}
	observed := time.Time{}
	if a.ObservedAt != nil {
		observed = *a.ObservedAt
	}
	if w, _, ok := EffectiveWindow(a.FiveHour, a.SafetyFactor, observed, now); ok && w.UsedPercent >= 100 {
		return true, "5h_exhausted"
	}
	if w, _, ok := EffectiveWindow(a.Week, a.SafetyFactor, observed, now); ok && w.UsedPercent >= 100 {
		return true, "week_exhausted"
	}
	return false, ""
}

// Paused reports whether the plugin paused the account and the pause still
// holds locally (see HardExhausted).
func Paused(a Account, now time.Time) bool {
	return a.State == "paused" && !pausedWindowsReset(a, now)
}

// pausedWindowsReset reports whether a paused account was paused by full
// windows that have all reset since: the pause is then over locally.
func pausedWindowsReset(a Account, now time.Time) bool {
	full := 0
	for _, w := range []*Window{a.FiveHour, a.Week} {
		if w == nil || w.UsedPercent < 100 {
			continue
		}
		full++
		if w.ResetAt == nil || now.Before(*w.ResetAt) {
			return false
		}
	}
	return full > 0
}
