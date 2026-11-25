// Package settle defines the settlement record written to the Redis stream
// and the consumer that persists records to PostgreSQL in batches.
package settle

import (
	"time"

	"github.com/google/uuid"
)

// Record is one finished request, as appended to the settlement stream.
// CreatedAt and RequestID come from the record itself, so a redelivered
// record conflicts on (created_at, request_id) and is skipped.
type Record struct {
	RequestID      uuid.UUID  `json:"request_id"`
	CreatedAt      time.Time  `json:"created_at"`
	UserID         uuid.UUID  `json:"user_id"`
	KeyID          uuid.UUID  `json:"key_id"`
	AccountID      *uuid.UUID `json:"account_id,omitempty"`
	Tier           string     `json:"tier"`
	SubscriptionID *uuid.UUID `json:"subscription_id,omitempty"`
	PeriodStart    *time.Time `json:"period_start,omitempty"`
	PeriodEnd      *time.Time `json:"period_end,omitempty"`
	PeriodQuota    int64      `json:"period_quota,omitempty"`

	Endpoint   string `json:"endpoint"`
	ModelRaw   string `json:"model_raw"`
	Model      string `json:"model"`
	Stream     bool   `json:"stream"`
	StatusCode int    `json:"status_code"`
	ErrorType  string `json:"error_type,omitempty"`

	InputTokens      int64 `json:"input_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`

	UpstreamCost int64   `json:"upstream_cost_micro"`
	Billed       int64   `json:"billed_micro"`
	TierMult     float64 `json:"tier_multiplier"`
	ModelMult    float64 `json:"model_multiplier"`
	Unpriced     bool    `json:"unpriced"`
	UsageMissing bool    `json:"usage_missing"`

	LatencyMs   int    `json:"latency_ms"`
	FirstByteMs int    `json:"first_byte_ms"`
	Retries     int    `json:"retries"`
	SessionHash string `json:"session_hash,omitempty"`
	ClientIP    string `json:"client_ip,omitempty"`
	UserAgent   string `json:"user_agent,omitempty"`

	// Wallet is the Redis wallet charged with Billed ("" when nothing was
	// charged); its pending counter is decremented once the record is in PG.
	Wallet string `json:"wallet,omitempty"`
}

// BalanceTier reports whether a tier charges the user's balance.
func BalanceTier(tier string) bool { return tier == "standard" || tier == "force" }

// Summary is the request_settled event broadcast to admin SSE clients.
type Summary struct {
	RequestID    uuid.UUID  `json:"request_id"`
	CreatedAt    time.Time  `json:"created_at"`
	UserID       uuid.UUID  `json:"user_id"`
	AccountID    *uuid.UUID `json:"account_id,omitempty"`
	Tier         string     `json:"tier"`
	Model        string     `json:"model"`
	StatusCode   int        `json:"status_code"`
	UpstreamCost int64      `json:"upstream_cost_micro"`
	Billed       int64      `json:"billed_micro"`
	LatencyMs    int        `json:"latency_ms"`
}

// Summarize renders the event form of a record.
func (r Record) Summarize() Summary {
	return Summary{RequestID: r.RequestID, CreatedAt: r.CreatedAt, UserID: r.UserID, AccountID: r.AccountID, Tier: r.Tier,
		Model: r.Model, StatusCode: r.StatusCode, UpstreamCost: r.UpstreamCost, Billed: r.Billed, LatencyMs: r.LatencyMs}
}
