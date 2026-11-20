// Package billing prices requests in micro-USD and keeps the toll ratio
// table current.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/pricing"
)

// NormalizeModel is the normalized model name used for allowed models, model
// multipliers and logs: lower case, no "(option)" suffix, no namespace.
func NormalizeModel(model string) string { return pricing.ModelKey(model) }

// USDToMicro converts dollars to micro-USD, rounding half away from zero.
func USDToMicro(usd float64) int64 {
	if math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0
	}
	return int64(math.Round(usd * 1e6))
}

// MicroToUSD converts micro-USD to dollars.
func MicroToUSD(micro int64) float64 { return float64(micro) / 1e6 }

// Apply multiplies an upstream cost by the tier and model multipliers.
func Apply(upstream int64, tierMult, modelMult float64) int64 {
	return int64(math.Round(float64(upstream) * tierMult * modelMult))
}

// Prices holds the current ratio table.
type Prices struct {
	table     atomic.Pointer[pricing.Table]
	mu        sync.Mutex
	status    Status
	HTTP      *http.Client
	Log       *slog.Logger
	onRefresh func()
}

// Status describes the loaded table for the admin API.
type Status struct {
	SourceURL     string    `json:"source_url"`
	Origin        string    `json:"origin"` // remote | database | none
	Models        int       `json:"models"`
	FetchedAt     time.Time `json:"fetched_at"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	LastError     string    `json:"last_error"`
}

// NewPrices returns an empty price holder.
func NewPrices(log *slog.Logger) *Prices {
	return &Prices{HTTP: &http.Client{Timeout: 15 * time.Second}, Log: log, status: Status{Origin: "none"}}
}

// Table returns the current table (nil when none is loaded).
func (p *Prices) Table() *pricing.Table { return p.table.Load() }

// Status returns the table status.
func (p *Prices) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// SetBody installs a table from raw JSON (tests, PG/Redis load).
func (p *Prices) SetBody(body []byte, origin, source string, fetched time.Time) error {
	t, err := pricing.Parse(body)
	if err != nil {
		return err
	}
	p.table.Store(t)
	p.mu.Lock()
	p.status.Origin, p.status.SourceURL, p.status.Models, p.status.FetchedAt = origin, source, t.Len(), fetched
	p.mu.Unlock()
	return nil
}

// LoadStored installs the table saved in PostgreSQL, if any.
func (p *Prices) LoadStored(ctx context.Context, q *sqlc.Queries) error {
	row, err := q.GetPriceTable(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return p.SetBody(row.Body, "database", row.SourceUrl, row.FetchedAt)
}

// Refresh fetches the table from url; on failure the old table stays.
func (p *Prices) Refresh(ctx context.Context, q *sqlc.Queries, url string) error {
	now := time.Now().UTC()
	body, err := p.fetch(ctx, url)
	if err == nil {
		err = p.SetBody(body, "remote", url, now)
	}
	p.mu.Lock()
	p.status.LastAttemptAt = now
	if err != nil {
		p.status.LastError = err.Error()
	} else {
		p.status.LastError = ""
	}
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if q != nil {
		if err := q.UpsertPriceTable(ctx, sqlc.UpsertPriceTableParams{SourceUrl: url, Body: body, Models: int32(p.Table().Len()), FetchedAt: now}); err != nil {
			p.Log.Warn("save price table", "err", err)
		}
	}
	if p.onRefresh != nil {
		p.onRefresh()
	}
	return nil
}

func (p *Prices) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("price table: status %d", resp.StatusCode)
	}
	if !json.Valid(body) {
		return nil, errors.New("price table: invalid json")
	}
	return body, nil
}

// Cost prices a breakdown. upstream is the model the response names (may be
// empty), alias the model the client requested. unpriced is true when the
// model is not in the table.
func Cost(t *pricing.Table, upstream, alias string, b pricing.Breakdown) (micro int64, unpriced bool) {
	if upstream == "" {
		upstream = alias
	}
	rates, ok := t.Lookup(upstream, alias)
	if !ok {
		return 0, true
	}
	return USDToMicro(pricing.Cost(rates, b)), false
}

// Estimate is the pre-hold estimate in upstream micro-USD: bodyBytes/3
// tokens at the input rate (no cache discount) plus maxTokens at the output
// rate. Unpriced models estimate 0.
func Estimate(t *pricing.Table, model string, bodyBytes int, maxTokens int64) int64 {
	rates, ok := t.Lookup(model, "")
	if !ok {
		return 0
	}
	in := float64(bodyBytes) / 3
	usd := in/1e6*rates.Input + float64(maxTokens)/1e6*rates.Output
	return USDToMicro(usd)
}

// EstimateInput is the input-only part of Estimate, billed when a 200
// response carried no usage.
func EstimateInput(t *pricing.Table, model string, bodyBytes int) (int64, bool) {
	rates, ok := t.Lookup(model, "")
	if !ok {
		return 0, true
	}
	return USDToMicro(float64(bodyBytes) / 3 / 1e6 * rates.Input), false
}
