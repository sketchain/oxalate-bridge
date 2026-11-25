// Package proxy implements the /v1 relay for the Claude Messages protocol:
// authentication, limits, tier admission, holds, forwarding to CPA with
// retries, usage capture and settlement.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	"github.com/sketchain/oxalate-bridge/backend/internal/authcache"
	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/keymeter"
	"github.com/sketchain/oxalate-bridge/backend/internal/metrics"
	"github.com/sketchain/oxalate-bridge/backend/internal/relaymsg"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/safeguard"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/sessionid"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
	"github.com/sketchain/oxalate-bridge/backend/internal/settle"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
	"github.com/sketchain/oxalate-bridge/backend/internal/usage"
)

// Lease and hold lifetimes; both are refreshed while a request runs.
const (
	leaseTTL     = 2 * time.Minute
	holdTTL      = 5 * time.Minute
	keepInterval = 30 * time.Second
	maxBody      = 64 << 20
	maxErrorBody = 1 << 20
)

// Proxy is the /v1 handler.
type Proxy struct {
	Auth      *authcache.Cache
	Router    *router.Router
	St        *rstate.State
	Settings  *settings.Store
	Prices    *billing.Prices
	Store     *store.Store
	Log       *slog.Logger
	Transport http.RoundTripper
	ClientIP  func(*http.Request) string
	// OnAccountError reloads routing after an account is marked error.
	OnAccountError func()
	// OnKeyExhausted runs (in its own goroutine) when a key is refused
	// because its total quota or a window is used up (notifications).
	OnKeyExhausted func(ctx context.Context, keyID uuid.UUID, code, window string, recoversAt *time.Time)
	Now            func() time.Time
	// Meter enforces key quotas and windows.
	Meter *keymeter.Meter
	// Status renders GET /v1/bridge/status (implemented by the panel API).
	Status func(context.Context, authcache.Info) (any, error)
	// Safeguard locks users out after their allowance of refusals, refuses
	// blocklisted prompts and records refusals.
	Safeguard *safeguard.Service
}

// NewTransport is the pooled transport used for CPA. There is no overall
// timeout; idle timeouts are enforced per request.
func NewTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
		ForceAttemptHTTP2:     true,
	}
}

type endpoint struct {
	path  string
	count bool
}

// endpoints are the relayed POST endpoints. Only the Claude Messages
// protocol is served; requests are accepted from any client and forwarded
// unchanged (client policy is enforced by CPA-side plugins).
var endpoints = map[string]endpoint{
	"/v1/messages":              {"/v1/messages", false},
	"/v1/messages/count_tokens": {"/v1/messages/count_tokens", true},
}

func (p *Proxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now().UTC()
}

// Routes registers the relay endpoints on mux-like registration.
func (p *Proxy) Routes(handle func(method, pattern string, h http.HandlerFunc)) {
	for path := range endpoints {
		handle(http.MethodPost, path, p.ServeRelay)
	}
	handle(http.MethodGet, "/v1/models", p.ServeModels)
	if p.Status != nil {
		handle(http.MethodGet, "/v1/bridge/status", p.ServeStatus)
	}
}

// ServeUnknown answers paths under /v1 that the relay does not serve.
func (p *Proxy) ServeUnknown(w http.ResponseWriter, r *http.Request) {
	p.writeMsg(w, http.StatusNotFound, "not_found_error", relaymsg.NotFound, nil)
}

// apiKey extracts the client's API key.
func apiKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if v, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(h, "bearer "); ok {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

func (p *Proxy) authenticate(w http.ResponseWriter, r *http.Request) (authcache.Info, bool) {
	key := apiKey(r)
	if key == "" {
		p.writeMsg(w, http.StatusUnauthorized, "authentication_error", relaymsg.MissingKey, nil)
		return authcache.Info{}, false
	}
	info, err := p.Auth.Lookup(r.Context(), key)
	if err != nil {
		p.Log.Error("auth lookup", "err", err)
		p.writeMsg(w, http.StatusServiceUnavailable, "api_error", relaymsg.AuthUnavailable, nil)
		return authcache.Info{}, false
	}
	if !info.Found {
		p.writeMsg(w, http.StatusUnauthorized, "authentication_error", relaymsg.InvalidKey, nil)
		return authcache.Info{}, false
	}
	if !info.Usable() {
		p.writeMsg(w, http.StatusForbidden, "permission_error", relaymsg.KeyDisabled, nil)
		return authcache.Info{}, false
	}
	return info, true
}

// ServeModels lists the union of the models served by enabled accounts, in
// the Anthropic list shape.
func (p *Proxy) ServeModels(w http.ResponseWriter, r *http.Request) {
	auth, ok := p.authenticate(w, r)
	if !ok {
		return
	}
	type model struct {
		ID          string `json:"id"`
		Object      string `json:"object"`
		Type        string `json:"type"`
		DisplayName string `json:"display_name"`
		OwnedBy     string `json:"owned_by"`
		Created     int64  `json:"created"`
	}
	ids := p.Router.ServedModels()
	data := make([]model, 0, len(ids))
	for _, id := range ids {
		if !keymeter.ModelAllowed(auth.ModelAllowlist, billing.NormalizeModel(id)) {
			continue
		}
		data = append(data, model{ID: id, Object: "model", Type: "model", DisplayName: id, OwnedBy: "oxalate-bridge"})
	}
	out := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		out["first_id"], out["last_id"] = data[0].ID, data[len(data)-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

var oneHourTTL = regexp.MustCompile(`"ttl"\s*:\s*"1h"`)

// reqInfo is what the relay reads from the request body.
type reqInfo struct {
	model     string
	normModel string
	stream    bool
	maxTokens int64
	hasMax    bool
	oneHour   bool
}

func parseBody(body []byte) reqInfo {
	var ri reqInfo
	res := gjson.GetManyBytes(body, "model", "stream", "max_tokens")
	ri.model = res[0].String()
	ri.normModel = billing.NormalizeModel(ri.model)
	ri.stream = res[1].Bool()
	if res[2].Exists() && res[2].Type == gjson.Number {
		ri.maxTokens, ri.hasMax = res[2].Int(), true
	}
	ri.oneHour = oneHourTTL.Match(body)
	return ri
}

// call is the state of one relay request.
type call struct {
	p         *Proxy
	r         *http.Request
	ep        endpoint
	auth      authcache.Info
	body      []byte
	ri        reqInfo
	requestID uuid.UUID
	start     time.Time
	session   string
	sticky    uuid.UUID
	set       settings.Values

	mu         sync.Mutex
	leaseKeys  []string
	holdWallet rstate.Wallet
	held       bool
	keyHeld    bool
	// keyRej is a key-level refusal met while holding: no further tier is tried.
	keyRej *keymeter.Rejection
	// balanceRej is an exhausted quota or window of a balance-scope key:
	// standard and force are refused, the plan tier is still tried.
	balanceRej *keymeter.Rejection
	// refusal is a safeguard refusal seen in the response.
	refusal *safeguard.Refusal
}

// admission is an admitted tier with its account candidates.
type admission struct {
	tier       string
	plan       *router.PlanTarget
	account    *router.Account
	candidates []router.Candidate
	wallet     rstate.Wallet
	tierMult   float64
	modelMult  float64
}

// ServeRelay handles the POST relay endpoints.
func (p *Proxy) ServeRelay(w http.ResponseWriter, r *http.Request) {
	ep, ok := endpoints[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	start := p.now()
	auth, ok := p.authenticate(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		p.writeMsg(w, http.StatusRequestEntityTooLarge, "invalid_request_error", relaymsg.BodyTooLarge, nil)
		return
	}
	requestID, _ := uuid.NewV7()
	c := &call{p: p, r: r, ep: ep, auth: auth, body: body, ri: parseBody(body), requestID: requestID, start: start, set: p.Settings.Get()}
	sid := sessionid.Extract(r.Header, body)
	if sid == "" {
		sid = "key:" + auth.KeyID.String()
	}
	c.session = secret.Hash("oxalate-bridge:session", sid)[:32]
	w.Header().Set("X-Relay-Request-Id", requestID.String())

	ctx := r.Context()
	if !c.checkSafeguard(ctx, w) {
		return
	}
	if rej := keymeter.CheckStatic(auth, p.ClientIP(r), c.ri.normModel, start); rej != nil {
		c.rejectKey(w, rej)
		return
	}
	if p.Meter != nil {
		rej, err := p.Meter.CheckLimits(ctx, auth, start)
		if err != nil {
			p.Log.Error("key limits", "err", err)
			p.writeMsg(w, http.StatusServiceUnavailable, "api_error", relaymsg.KeyLimitsUnavailable, nil)
			return
		}
		if rej.TierOnly() {
			c.balanceRej = rej
		} else if rej != nil {
			c.rejectKey(w, rej)
			return
		}
	}
	if ok := c.admitLimits(ctx, w); !ok {
		return
	}
	stopKeep := c.keepAlive(ctx)
	defer func() {
		stopKeep()
		c.releaseAll()
	}()

	if acc, ok := p.St.GetAffinity(ctx, c.session); ok {
		c.sticky = acc
	}
	adm, tier, rejections := RunTiers(ctx, auth.Tiers, func(ctx context.Context, tier string) (*admission, *router.Rejection) {
		if c.keyRej != nil {
			return nil, &router.Rejection{Tier: tier, Code: c.keyRej.Code, MsgID: c.keyRej.MsgID, MsgParams: c.keyRej.MsgParams}
		}
		if c.balanceRej != nil && c.auth.Counts(tier) {
			return nil, c.balanceTierRejection(tier)
		}
		return c.admitTier(ctx, tier)
	})
	if c.keyRej != nil {
		c.rejectKey(w, c.keyRej)
		return
	}
	if tier == "" {
		for _, rej := range rejections {
			metrics.Rejections.WithLabelValues(rej.Tier, rej.Code).Inc()
		}
		loc := c.set.RelayErrorLocale
		LocalizeRejections(loc, rejections)
		msg := RejectionMessage(loc, rejections)
		writeErrorWith(w, http.StatusTooManyRequests, "rate_limit_error", msg, map[string]any{"rejections": rejections})
		c.settleRejected(http.StatusTooManyRequests, "tier_rejected")
		return
	}
	w.Header().Set("X-Relay-Tier", tier)
	c.serve(w, adm)
}

// checkSafeguard refuses, before any tier is tried, every request of a user
// whose safeguard allowance is used up and every Messages request whose
// prompt is on the blocklist. Neither reaches CPA or uses the allowance.
func (c *call) checkSafeguard(ctx context.Context, w http.ResponseWriter) bool {
	p := c.p
	if p.Safeguard == nil {
		return true
	}
	if safeguard.Locked(c.auth.SafeguardUsed, c.auth.SafeguardAllowance, c.set.SafeguardDefaultAllowance) {
		metrics.Rejections.WithLabelValues("", "safeguard_locked").Inc()
		p.writeMsgCode(w, http.StatusForbidden, "permission_error", "safeguard_locked", relaymsg.SafeguardLocked, nil, nil)
		c.settleRejected(http.StatusForbidden, "safeguard_locked")
		return false
	}
	if c.ep.path != "/v1/messages" {
		return true
	}
	hash := safeguard.Hash(safeguard.Prompt(c.body), c.set.SafeguardHashMinChars)
	blocked, err := p.Safeguard.Blocked(ctx, hash)
	if err != nil {
		p.Log.Error("safeguard blocklist", "err", err)
		p.writeMsg(w, http.StatusServiceUnavailable, "api_error", relaymsg.SafeguardUnavailable, nil)
		return false
	}
	if !blocked {
		return true
	}
	metrics.Rejections.WithLabelValues("", "safeguard_prompt_blocked").Inc()
	if err := p.Safeguard.RecordHit(context.WithoutCancel(ctx), c.requestID, c.auth.UserID, c.auth.KeyID, hash, c.start); err != nil {
		p.Log.Error("record blocklist hit", "err", err)
	}
	p.writeMsgCode(w, http.StatusBadRequest, "invalid_request_error", "safeguard_prompt_blocked", relaymsg.SafeguardPromptBlocks, nil, nil)
	c.settleRejected(http.StatusBadRequest, "safeguard_prompt_blocked")
	return false
}

// recordRefusal stores a refusal the response carried (before the client
// sees the refusal, so an immediate retry already meets the lock or the
// blocklist).
func (c *call) recordRefusal(account *router.Account, status int, ref safeguard.Refusal) {
	if c.p.Safeguard == nil {
		return
	}
	accID := account.ID
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.p.Safeguard.Record(ctx, safeguard.Trigger{RequestID: c.requestID, UserID: c.auth.UserID, KeyID: c.auth.KeyID,
		AccountID: &accID, Model: c.ri.normModel, Refusal: ref, StatusCode: status, Stream: c.ri.stream, Body: c.body,
		At: c.start}); err != nil {
		c.p.Log.Error("record safeguard refusal", "request_id", c.requestID, "err", err)
	}
}

// balanceTierRejection renders c.balanceRej as the rejection of one tier.
func (c *call) balanceTierRejection(tier string) *router.Rejection {
	r := c.balanceRej
	return &router.Rejection{Tier: tier, Code: r.Code, MsgID: r.MsgID, MsgParams: r.MsgParams, Window: r.Window, RecoversAt: r.RecoversAt}
}

// rejectKey answers a key-level refusal (never falls through to a tier).
func (c *call) rejectKey(w http.ResponseWriter, rej *keymeter.Rejection) {
	metrics.Rejections.WithLabelValues("", rej.Code).Inc()
	if c.p.OnKeyExhausted != nil && (rej.Code == keymeter.CodeQuotaExhausted || rej.Code == keymeter.CodeWindowExhausted) {
		go c.p.OnKeyExhausted(context.Background(), c.auth.KeyID, rej.Code, rej.Window, rej.RecoversAt)
	}
	writeErrorWith(w, rej.Status, rej.Type, rej.Text(c.set.RelayErrorLocale), rej.Extra())
	c.settleRejected(rej.Status, rej.Code)
}

func (c *call) admitLimits(ctx context.Context, w http.ResponseWriter) bool {
	a := c.auth
	var conc, rpm []rstate.Limit
	if a.UserMaxConc > 0 {
		conc = append(conc, rstate.Limit{Key: c.p.St.ConcKey("u", a.UserID), Limit: int64(a.UserMaxConc)})
	}
	if a.KeyMaxConc != nil && *a.KeyMaxConc > 0 {
		conc = append(conc, rstate.Limit{Key: c.p.St.ConcKey("k", a.KeyID), Limit: int64(*a.KeyMaxConc)})
	}
	if a.UserRPM != nil && *a.UserRPM > 0 {
		rpm = append(rpm, rstate.Limit{Key: c.p.St.RPMKey("u", a.UserID), Limit: int64(*a.UserRPM)})
	}
	if a.KeyRPM != nil && *a.KeyRPM > 0 {
		rpm = append(rpm, rstate.Limit{Key: c.p.St.RPMKey("k", a.KeyID), Limit: int64(*a.KeyRPM)})
	}
	if a.UserMaxConc == 0 {
		// 0 means the user may not make requests at all.
		c.p.writeMsg(w, http.StatusTooManyRequests, "rate_limit_error", relaymsg.UserConcurrencyZero, nil)
		c.settleRejected(http.StatusTooManyRequests, "rate_limited")
		return false
	}
	ok, idx, err := c.p.St.Admit(ctx, c.requestID.String(), conc, rpm, leaseTTL, c.p.now())
	if err != nil {
		c.p.Log.Error("admit limits", "err", err)
		c.p.writeMsg(w, http.StatusServiceUnavailable, "api_error", relaymsg.RateLimiterUnavailable, nil)
		return false
	}
	if !ok {
		id := relaymsg.ConcurrencyLimited
		if idx >= len(conc) {
			id = relaymsg.RPMLimited
		}
		c.p.writeMsg(w, http.StatusTooManyRequests, "rate_limit_error", id, nil)
		c.settleRejected(http.StatusTooManyRequests, "rate_limited")
		return false
	}
	c.mu.Lock()
	for _, l := range conc {
		c.leaseKeys = append(c.leaseKeys, l.Key)
	}
	c.mu.Unlock()
	return true
}

// keepAlive refreshes leases and the hold while the request runs.
func (c *call) keepAlive(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(keepInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				now := c.p.now()
				c.mu.Lock()
				keys := append([]string(nil), c.leaseKeys...)
				w, held, keyHeld := c.holdWallet, c.held, c.keyHeld
				c.mu.Unlock()
				bg := context.WithoutCancel(ctx)
				c.p.St.ExtendLeases(bg, c.requestID.String(), keys, leaseTTL, now)
				if held {
					_ = c.p.St.ExtendHold(bg, w, c.requestID.String(), holdTTL, now)
				}
				if keyHeld {
					_ = c.p.St.ExtendKeyHold(bg, c.auth.KeyID, c.requestID.String(), holdTTL, now)
				}
			}
		}
	}()
	return func() { close(done) }
}

func (c *call) addLease(key string) {
	c.mu.Lock()
	c.leaseKeys = append(c.leaseKeys, key)
	c.mu.Unlock()
}

func (c *call) dropLease(key string) {
	c.p.St.ReleaseLeases(context.Background(), c.requestID.String(), []string{key})
	c.mu.Lock()
	for i, k := range c.leaseKeys {
		if k == key {
			c.leaseKeys = append(c.leaseKeys[:i], c.leaseKeys[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
}

func (c *call) releaseAll() {
	c.mu.Lock()
	keys := c.leaseKeys
	c.leaseKeys = nil
	w, held, keyHeld := c.holdWallet, c.held, c.keyHeld
	c.held, c.keyHeld = false, false
	c.mu.Unlock()
	bg := context.Background()
	c.p.St.ReleaseLeases(bg, c.requestID.String(), keys)
	if held {
		// Normally released by settlement; this covers early exits.
		_ = c.p.St.ReleaseHold(bg, w, c.requestID.String())
	}
	if keyHeld {
		_ = c.p.St.ReleaseKeyHold(bg, c.auth.KeyID, c.requestID.String())
	}
}

func (c *call) accountLimit(a *router.Account) int64 {
	if a.MaxConcurrency == nil {
		return 0
	}
	if *a.MaxConcurrency == 0 {
		return -1
	}
	return int64(*a.MaxConcurrency)
}

// acquireAccount takes an account concurrency lease.
func (c *call) acquireAccount(ctx context.Context, a *router.Account) bool {
	limit := c.accountLimit(a)
	if limit < 0 {
		return false
	}
	key := c.p.St.ConcKey("a", a.ID)
	ok, err := c.p.St.AcquireLease(ctx, key, c.requestID.String(), limit, leaseTTL, c.p.now())
	if err != nil || !ok {
		return false
	}
	c.addLease(key)
	return true
}

// baseFunc reads a wallet's committed (base, version) from PostgreSQL.
type baseFunc func(context.Context) (int64, int64, error)

// walletAvailable reads a wallet, syncing it from PostgreSQL if missing.
func (c *call) walletAvailable(ctx context.Context, w rstate.Wallet, base baseFunc) (int64, error) {
	avail, err := c.p.St.Available(ctx, w, c.p.now())
	if !errors.Is(err, rstate.ErrWalletMissing) {
		return avail, err
	}
	if err := c.rebuild(ctx, w, base); err != nil {
		return 0, err
	}
	return c.p.St.Available(ctx, w, c.p.now())
}

func (c *call) rebuild(ctx context.Context, w rstate.Wallet, base baseFunc) error {
	b, v, err := base(ctx)
	if err != nil {
		return err
	}
	return c.p.St.SyncBase(ctx, w, b, v)
}

func (c *call) userBase(ctx context.Context) (int64, int64, error) {
	row, err := c.p.Store.Q.GetUserBalance(ctx, c.auth.UserID)
	return row.BalanceMicro, row.BalanceVersion, err
}

func (c *call) periodBase(t *router.PlanTarget) baseFunc {
	return func(ctx context.Context) (int64, int64, error) {
		row, err := c.p.Store.Q.GetPeriod(ctx, sqlcGetPeriod(t.Sub.ID, t.Period.Start))
		if err != nil {
			if isNoRows(err) {
				return t.Sub.WeeklyQuota, 0, nil
			}
			return 0, 0, err
		}
		return row.QuotaMicro - row.UsedBilledMicro, row.Version, nil
	}
}

// hold reserves the request's estimated charge on a wallet and, when the
// tier counts toward the key's limits, on the key's quota and windows. A
// refusal by the key sets c.keyRej (no further tier is tried), or
// c.balanceRej for a balance-scope key (only standard and force are refused).
func (c *call) hold(ctx context.Context, tier string, w rstate.Wallet, tierMult, modelMult float64, base baseFunc) (bool, error) {
	maxTok := c.set.HoldDefaultMaxTokens
	if c.ri.hasMax && c.ri.maxTokens > 0 && c.ri.maxTokens < maxTok {
		maxTok = c.ri.maxTokens
	}
	est := billing.Apply(billing.Estimate(c.p.Prices.Table(), c.ri.model, len(c.body), maxTok), tierMult, modelMult)
	if est > c.set.HoldCapMicro {
		est = c.set.HoldCapMicro
	}
	var kh *rstate.KeyHold
	if lim := c.auth.Limits(); c.p.Meter != nil && lim.Any() && c.auth.Counts(tier) {
		kh = &rstate.KeyHold{Key: c.auth.KeyID, Limits: lim}
	}
	now := c.p.now()
	for attempt := 0; attempt < 4; attempt++ {
		res, err := c.p.St.HoldWithKey(ctx, w, kh, c.requestID.String(), est, holdTTL, now)
		if errors.Is(err, rstate.ErrWalletMissing) {
			if err := c.rebuild(ctx, w, base); err != nil {
				return false, err
			}
			continue
		}
		if errors.Is(err, rstate.ErrKeyBaseMissing) {
			if err := c.p.Meter.EnsureBase(ctx, c.auth.KeyID, now); err != nil {
				return false, err
			}
			continue
		}
		if err != nil {
			return false, err
		}
		if res.OK {
			c.mu.Lock()
			c.holdWallet, c.held, c.keyHeld = w, true, kh != nil
			c.mu.Unlock()
		} else if res.Reason != rstate.HoldWallet && kh != nil {
			if rej := c.p.Meter.HoldRejection(ctx, c.auth, res.Reason, now); rej.TierOnly() {
				c.balanceRej = rej
			} else {
				c.keyRej = rej
			}
		}
		return res.OK, nil
	}
	return false, rstate.ErrWalletMissing
}

// admitTier evaluates one tier and takes the account lease and hold.
func (c *call) admitTier(ctx context.Context, tier string) (*admission, *router.Rejection) {
	now := c.p.now()
	modelMult := c.set.ModelMultiplier(c.ri.normModel)
	tierMult := c.set.TierMultipliers.For(tier)
	switch tier {
	case router.TierPlan:
		t, rej := c.p.Router.EvalPlan(ctx, c.auth, c.ri.normModel, now)
		if rej != nil {
			return nil, rej
		}
		if !c.acquireAccount(ctx, t.Account) {
			return nil, &router.Rejection{Tier: tier, Code: router.CodeAccountBusy, MsgID: relaymsg.PlanAccountBusy}
		}
		if !c.ep.count {
			ok, err := c.hold(ctx, tier, t.Wallet, tierMult, modelMult, c.periodBase(t))
			if err != nil {
				c.dropLease(c.p.St.ConcKey("a", t.Account.ID))
				c.p.Log.Error("plan hold", "err", err)
				return nil, &router.Rejection{Tier: tier, Code: router.CodeStateUnavailable, MsgID: relaymsg.QuotaStateDown}
			}
			if !ok {
				c.dropLease(c.p.St.ConcKey("a", t.Account.ID))
				return nil, &router.Rejection{Tier: tier, Code: router.CodePlanExhausted, MsgID: relaymsg.PlanExhausted}
			}
		}
		return &admission{tier: tier, plan: t, account: t.Account, wallet: t.Wallet, tierMult: tierMult, modelMult: modelMult}, nil
	case router.TierStandard, router.TierForce:
		wallet := rstate.UserWallet(c.auth.UserID)
		if !c.ep.count {
			avail, err := c.walletAvailable(ctx, wallet, c.userBase)
			if err != nil {
				c.p.Log.Error("balance read", "err", err)
				return nil, &router.Rejection{Tier: tier, Code: router.CodeStateUnavailable, MsgID: relaymsg.BalanceStateDown}
			}
			if avail <= 0 {
				return nil, &router.Rejection{Tier: tier, Code: router.CodeInsufficientFunds, MsgID: relaymsg.InsufficientFunds}
			}
		}
		cands, err := c.p.Router.Candidates(ctx, tier, c.ri.normModel, now, nil)
		if err != nil {
			c.p.Log.Error("candidates", "err", err)
			return nil, &router.Rejection{Tier: tier, Code: router.CodeStateUnavailable, MsgID: relaymsg.RouteStateDown}
		}
		acc, rest, busy := c.pickAndLease(ctx, cands)
		if acc == nil {
			if busy {
				return nil, &router.Rejection{Tier: tier, Code: router.CodeAccountBusy, MsgID: relaymsg.AccountBusy}
			}
			return nil, &router.Rejection{Tier: tier, Code: router.CodeNoAccount, MsgID: relaymsg.NoAccount}
		}
		if !c.ep.count {
			ok, err := c.hold(ctx, tier, wallet, tierMult, modelMult, c.userBase)
			if err != nil || !ok {
				c.dropLease(c.p.St.ConcKey("a", acc.ID))
				if err != nil {
					c.p.Log.Error("balance hold", "err", err)
					return nil, &router.Rejection{Tier: tier, Code: router.CodeStateUnavailable, MsgID: relaymsg.BalanceStateDown}
				}
				if c.balanceRej != nil {
					return nil, c.balanceTierRejection(tier)
				}
				return nil, &router.Rejection{Tier: tier, Code: router.CodeInsufficientFunds, MsgID: relaymsg.InsufficientFunds}
			}
		}
		return &admission{tier: tier, account: acc, candidates: rest, wallet: wallet, tierMult: tierMult, modelMult: modelMult}, nil
	}
	return nil, &router.Rejection{Tier: tier, Code: router.CodeUnsupportedTier, MsgID: relaymsg.UnknownTier}
}

// pickAndLease picks a candidate (sticky first) whose account lease can be
// taken. It returns the remaining candidates for retries.
func (c *call) pickAndLease(ctx context.Context, cands []router.Candidate) (*router.Account, []router.Candidate, bool) {
	busy := false
	for len(cands) > 0 {
		pick, _ := c.p.Router.Pick(cands, c.sticky)
		rest := make([]router.Candidate, 0, len(cands)-1)
		for _, cand := range cands {
			if cand.Account.ID != pick.Account.ID {
				rest = append(rest, cand)
			}
		}
		if c.acquireAccount(ctx, pick.Account) {
			return pick.Account, rest, false
		}
		busy = true
		cands = rest
	}
	return nil, nil, busy
}

func isNoRows(err error) bool { return err != nil && strings.Contains(err.Error(), "no rows") }

func clientHeaderDrop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer",
		"transfer-encoding", "upgrade", "authorization", "x-api-key", "x-goog-api-key", "cookie", "host", "content-length",
		"accept-encoding", "forwarded", "x-real-ip":
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), "x-forwarded-")
}

func connectionTokens(h http.Header) map[string]bool {
	out := map[string]bool{}
	for _, v := range h.Values("Connection") {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				out[strings.ToLower(t)] = true
			}
		}
	}
	return out
}

func (c *call) buildRequest(ctx context.Context, a *router.Account, body []byte) (*http.Request, error) {
	target := strings.TrimRight(a.BaseURL, "/") + c.ep.path
	if c.r.URL.RawQuery != "" {
		target += "?" + c.r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	conn := connectionTokens(c.r.Header)
	for name, values := range c.r.Header {
		if clientHeaderDrop(name) || conn[strings.ToLower(name)] {
			continue
		}
		req.Header[name] = append([]string(nil), values...)
	}
	req.Header.Set("Authorization", "Bearer "+a.CPAKey)
	req.Header.Set("Accept-Encoding", "identity")
	req.ContentLength = int64(len(body))
	return req, nil
}

func responseHeaderDrop(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer",
		"transfer-encoding", "upgrade", "content-length":
		return true
	}
	return false
}

// idleReader cancels the upstream request when no byte arrives for d.
type idleReader struct {
	rc    io.ReadCloser
	timer *time.Timer
	d     time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 {
		r.timer.Reset(r.d)
	}
	return n, err
}

func (r *idleReader) Close() error {
	r.timer.Stop()
	return r.rc.Close()
}

// attemptResult is the outcome of one upstream attempt.
type attemptResult struct {
	resp    *http.Response
	err     error
	errBody []byte
	cancel  context.CancelFunc
	timer   *time.Timer
	firstMs int
}

func (c *call) attempt(a *router.Account, body []byte) attemptResult {
	idle := time.Duration(c.set.UpstreamIdleTimeoutSecs) * time.Second
	ctx, cancel := context.WithCancel(c.r.Context())
	timer := time.AfterFunc(idle, cancel)
	req, err := c.buildRequest(ctx, a, body)
	if err != nil {
		timer.Stop()
		cancel()
		return attemptResult{err: err}
	}
	sent := time.Now()
	resp, err := c.p.Transport.RoundTrip(req)
	if err != nil {
		timer.Stop()
		cancel()
		return attemptResult{err: err}
	}
	res := attemptResult{resp: resp, cancel: cancel, timer: timer, firstMs: int(time.Since(sent).Milliseconds())}
	resp.Body = &idleReader{rc: resp.Body, timer: timer, d: idle}
	if resp.StatusCode >= 400 {
		res.errBody, _ = io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		resp.Body.Close()
		timer.Stop()
		cancel()
	}
	return res
}

// retryable classifies a failed attempt and applies its account side effects.
func (c *call) handleFailure(a *router.Account, res attemptResult) (retry bool) {
	ctx := context.Background()
	now := c.p.now()
	if res.err != nil {
		if c.r.Context().Err() != nil {
			return false // client went away
		}
		metrics.UpstreamErrors.WithLabelValues("network").Inc()
		_ = c.p.St.SetCooldown(ctx, a.ID, now.Add(time.Duration(c.set.Cooldown5xxSeconds)*time.Second), "network", now)
		return true
	}
	status := res.resp.StatusCode
	switch {
	case status == http.StatusTooManyRequests:
		metrics.UpstreamErrors.WithLabelValues("429").Inc()
		_ = c.p.St.SetCooldown(ctx, a.ID, c.cooldownUntil429(a, res.resp, now), "upstream 429", now)
		return true
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		metrics.UpstreamErrors.WithLabelValues("auth").Inc()
		c.markAccountError(a, "upstream "+strconv.Itoa(status)+": "+truncate(string(res.errBody), 200))
		return true
	case status >= 500:
		metrics.UpstreamErrors.WithLabelValues("5xx").Inc()
		_ = c.p.St.SetCooldown(ctx, a.ID, now.Add(time.Duration(c.set.Cooldown5xxSeconds)*time.Second), "upstream "+strconv.Itoa(status), now)
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// cooldownUntil429 is Retry-After when given, else the reset of an exhausted
// window from the snapshot, else the default.
func (c *call) cooldownUntil429(a *router.Account, resp *http.Response, now time.Time) time.Time {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs > 0 {
			return now.Add(time.Duration(secs) * time.Second)
		}
		if t, err := http.ParseTime(ra); err == nil && t.After(now) {
			return t
		}
	}
	if snap := c.p.Router.Cat.Snapshot(a.ID); snap != nil && snap.HasData {
		acc := snap.Account
		var best *time.Time
		if acc.FiveHour != nil && acc.FiveHour.UsedPercent >= 100 && acc.FiveHour.ResetAt != nil && acc.FiveHour.ResetAt.After(now) {
			best = acc.FiveHour.ResetAt
		}
		if acc.Week != nil && acc.Week.UsedPercent >= 100 && acc.Week.ResetAt != nil && acc.Week.ResetAt.After(now) {
			if best == nil || acc.Week.ResetAt.After(*best) {
				best = acc.Week.ResetAt
			}
		}
		if best != nil {
			return *best
		}
	}
	return now.Add(time.Duration(c.set.Cooldown429Seconds) * time.Second)
}

func (c *call) markAccountError(a *router.Account, reason string) {
	ctx := context.Background()
	now := c.p.now()
	// Immediate effect on every replica until the catalog reloads.
	_ = c.p.St.SetCooldown(ctx, a.ID, now.Add(10*time.Minute), "error: "+reason, now)
	err := c.p.Store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.Q.SetAccountError(ctx, sqlcSetAccountError(a.ID, reason)); err != nil {
			return err
		}
		return tx.RoutingChanged(ctx)
	})
	if err != nil {
		c.p.Log.Error("mark account error", "account", a.ID, "err", err)
	}
	if c.p.OnAccountError != nil {
		c.p.OnAccountError()
	}
}

// serve forwards the request, retrying on another account of the same tier
// before any byte reached the client.
func (c *call) serve(w http.ResponseWriter, adm *admission) {
	body := c.body
	account := adm.account
	cands := adm.candidates
	retries := 0
	var res attemptResult
	for {
		res = c.attempt(account, body)
		if res.err == nil && res.resp.StatusCode < 400 {
			break
		}
		if res.err == nil && res.resp.StatusCode == http.StatusBadRequest {
			if ref, ok := safeguard.DetectError(res.errBody); ok {
				metrics.UpstreamErrors.WithLabelValues("refusal").Inc()
				c.recordRefusal(account, http.StatusBadRequest, ref)
				c.writeUpstreamError(w, res)
				c.settle(adm, account, http.StatusBadRequest, "safeguard_refusal", usage.Usage{}, retries, res.firstMs)
				return
			}
		}
		retry := c.handleFailure(account, res)
		if !retry || adm.tier == router.TierPlan || retries >= c.set.MaxRetries {
			break
		}
		// Retry on another eligible account of the same tier.
		next := c.nextAccount(adm, account, &cands)
		if next == nil {
			break
		}
		metrics.Retries.Inc()
		retries++
		account = next
	}
	if res.err != nil {
		status, typ := http.StatusBadGateway, "upstream_unreachable"
		if c.r.Context().Err() != nil {
			status, typ = 499, "client_closed"
		} else {
			c.p.writeMsg(w, http.StatusBadGateway, "api_error", relaymsg.UpstreamUnreachable, nil)
		}
		c.settle(adm, account, status, typ, usage.Usage{}, retries, 0)
		return
	}
	if res.resp.StatusCode >= 400 {
		c.writeUpstreamError(w, res)
		c.settle(adm, account, res.resp.StatusCode, "upstream_"+strconv.Itoa(res.resp.StatusCode), usage.Usage{}, retries, res.firstMs)
		return
	}
	metrics.FirstByte.WithLabelValues(adm.tier).Observe(float64(res.firstMs) / 1000)
	u, status, errType := c.stream(w, res, account)
	if c.refusal != nil {
		// A refusal after the response started: the usage reported so far
		// is charged like any request, and the request counts as a trigger.
		metrics.UpstreamErrors.WithLabelValues("refusal").Inc()
		if errType == "" {
			errType = "safeguard_refusal"
		}
	}
	c.settle(adm, account, status, errType, u, retries, res.firstMs)
}

func (c *call) nextAccount(adm *admission, failed *router.Account, cands *[]router.Candidate) *router.Account {
	ctx := c.r.Context()
	c.dropLease(c.p.St.ConcKey("a", failed.ID))
	// Re-evaluate: cooldowns just set exclude the failed account.
	fresh, err := c.p.Router.Candidates(ctx, adm.tier, c.ri.normModel, c.p.now(), map[uuid.UUID]bool{failed.ID: true})
	if err != nil {
		fresh = *cands
	}
	acc, rest, _ := c.pickAndLease(ctx, fresh)
	*cands = rest
	return acc
}

func (c *call) writeUpstreamError(w http.ResponseWriter, res attemptResult) {
	for name, values := range res.resp.Header {
		if responseHeaderDrop(name) {
			continue
		}
		w.Header()[name] = values
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(res.errBody)))
	w.WriteHeader(res.resp.StatusCode)
	_, _ = w.Write(res.errBody)
}

// stream copies the response to the client while reading usage.
func (c *call) stream(w http.ResponseWriter, res attemptResult, account *router.Account) (usage.Usage, int, string) {
	resp := res.resp
	defer func() {
		resp.Body.Close()
		res.cancel()
	}()
	for name, values := range resp.Header {
		if responseHeaderDrop(name) {
			continue
		}
		w.Header()[name] = values
	}
	isSSE := strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	if !isSSE && resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	rc := http.NewResponseController(w)

	var u usage.Usage
	if !isSSE {
		buf, err := io.ReadAll(resp.Body)
		if ref, ok := safeguard.DetectResponse(buf); ok && !c.ep.count {
			c.refusal = &ref
			c.recordRefusal(account, resp.StatusCode, ref)
		}
		if len(buf) > 0 {
			if _, werr := w.Write(buf); werr != nil {
				return usage.FromBody(buf), 499, "client_closed"
			}
		}
		u = usage.FromBody(buf)
		if err != nil {
			return u, resp.StatusCode, "upstream_read_error"
		}
		return u, resp.StatusCode, ""
	}

	var writeErr error
	write := func(b []byte) {
		if writeErr != nil || len(b) == 0 {
			return
		}
		if _, err := w.Write(b); err != nil {
			writeErr = err
			return
		}
		if flusher != nil {
			_ = rc.Flush()
		}
	}
	sc := &usage.SSEScanner{OnEvent: func(event string, data []byte) {
		u.Event(event, data)
		if c.refusal == nil {
			if ref, ok := safeguard.DetectEvent(event, data); ok {
				c.refusal = &ref
			}
		}
	}}
	recorded := false
	buf := make([]byte, 32<<10)
	var readErr error
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			sc.Feed(chunk)
			if c.refusal != nil && !recorded {
				recorded = true
				c.recordRefusal(account, resp.StatusCode, *c.refusal)
			}
			write(chunk)
			if writeErr != nil {
				break
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	sc.Flush()
	if c.refusal != nil && !recorded {
		c.recordRefusal(account, resp.StatusCode, *c.refusal)
	}
	switch {
	case writeErr != nil || c.r.Context().Err() != nil:
		return u, 499, "client_closed"
	case readErr != nil:
		return u, resp.StatusCode, "upstream_stream_error"
	}
	return u, resp.StatusCode, ""
}

// settle computes the charge and hands the record to Redis.
func (c *call) settle(adm *admission, account *router.Account, status int, errType string, u usage.Usage, retries, firstMs int) {
	now := c.p.now()
	rec := c.baseRecord(now, status, errType)
	rec.Tier = adm.tier
	rec.TierMult, rec.ModelMult = adm.tierMult, adm.modelMult
	rec.Retries, rec.FirstByteMs = retries, firstMs
	accID := account.ID
	rec.AccountID = &accID
	if adm.plan != nil {
		subID := adm.plan.Sub.ID
		ps, pe := adm.plan.Period.Start, adm.plan.Period.End
		rec.SubscriptionID, rec.PeriodStart, rec.PeriodEnd, rec.PeriodQuota = &subID, &ps, &pe, adm.plan.Sub.WeeklyQuota
	}

	table := c.p.Prices.Table()
	b := u.Breakdown()
	rec.InputTokens, rec.CacheReadTokens, rec.CacheWriteTokens, rec.OutputTokens, rec.ReasoningTokens =
		b.Uncached, b.CacheRead, b.CacheWrite, b.Output, b.Reasoning
	switch {
	case c.ep.count:
		// count_tokens is logged, never billed.
	case u.Seen:
		rec.UpstreamCost, rec.Unpriced = billing.Cost(table, u.Model, c.ri.model, b)
	case status == http.StatusOK:
		rec.UsageMissing = true
		rec.UpstreamCost, rec.Unpriced = billing.EstimateInput(table, c.ri.model, len(c.body))
	}
	if !c.ep.count {
		rec.Billed = billing.Apply(rec.UpstreamCost, adm.tierMult, adm.modelMult)
		rec.Wallet = string(adm.wallet)
	}
	c.mu.Lock()
	hasHold := c.held && c.holdWallet == adm.wallet
	c.held = false
	c.mu.Unlock()
	if !hasHold && rec.Billed == 0 {
		rec.Wallet = ""
	}
	raw, _ := json.Marshal(rec)
	op := rstate.SettleOp{RequestID: c.requestID.String(), Charge: rec.Billed, Account: account.ID,
		AccountCost: rec.UpstreamCost, At: now, SessionHash: c.session, AffinityTTL: c.p.Router.AffinityTTL(c.ri.oneHour), Record: raw,
		Key: c.auth.KeyID, KeyBalance: settle.BalanceTier(adm.tier)}
	c.mu.Lock()
	c.keyHeld = false
	c.mu.Unlock()
	if rec.Wallet != "" {
		op.Wallet, op.HasHold = adm.wallet, hasHold
	} else if hasHold {
		_ = c.p.St.ReleaseHold(context.Background(), adm.wallet, c.requestID.String())
	}
	if _, err := c.p.St.Settle(context.Background(), op); err != nil {
		c.p.Log.Error("settle request", "request_id", c.requestID, "err", err, "record", string(raw))
	}
	metrics.Requests.WithLabelValues(adm.tier, c.ep.path, statusClass(status)).Inc()
}

func (c *call) baseRecord(now time.Time, status int, errType string) settle.Record {
	return settle.Record{RequestID: c.requestID, CreatedAt: c.start, UserID: c.auth.UserID, KeyID: c.auth.KeyID,
		Endpoint: c.ep.path, ModelRaw: c.ri.model, Model: c.ri.normModel, Stream: c.ri.stream, StatusCode: status,
		ErrorType: errType, TierMult: 1, ModelMult: 1, LatencyMs: int(now.Sub(c.start).Milliseconds()),
		SessionHash: c.session, ClientIP: c.p.ClientIP(c.r), UserAgent: truncate(c.r.UserAgent(), 512)}
}

// settleRejected logs a request no tier (or limit) admitted.
func (c *call) settleRejected(status int, errType string) {
	now := c.p.now()
	rec := c.baseRecord(now, status, errType)
	raw, _ := json.Marshal(rec)
	if _, err := c.p.St.Settle(context.Background(), rstate.SettleOp{RequestID: c.requestID.String(), At: now, Record: raw, Key: c.auth.KeyID}); err != nil {
		c.p.Log.Error("settle rejected request", "err", err)
	}
	metrics.Requests.WithLabelValues("", c.ep.path, statusClass(status)).Inc()
}

func statusClass(status int) string {
	switch {
	case status == 499:
		return "499"
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	}
	return "2xx"
}

// writeMsg writes an Anthropic-shaped error whose message is relaymsg id in
// the configured locale.
func (p *Proxy) writeMsg(w http.ResponseWriter, status int, typ, id string, params map[string]string) {
	writeError(w, status, typ, relaymsg.Text(p.Settings.Get().RelayErrorLocale, id, params))
}

// writeMsgCode is writeMsg with error.code (and extra members).
func (p *Proxy) writeMsgCode(w http.ResponseWriter, status int, typ, code, id string, params map[string]string, extra map[string]any) {
	e := map[string]any{"code": code}
	for k, v := range extra {
		e[k] = v
	}
	writeErrorWith(w, status, typ, relaymsg.Text(p.Settings.Get().RelayErrorLocale, id, params), e)
}

// writeError writes an Anthropic-shaped error.
func writeError(w http.ResponseWriter, status int, typ, msg string) {
	writeErrorWith(w, status, typ, msg, nil)
}

func writeErrorWith(w http.ResponseWriter, status int, typ, msg string, extra map[string]any) {
	e := map[string]any{"type": typ, "message": msg}
	for k, v := range extra {
		e[k] = v
	}
	body := map[string]any{"type": "error", "error": e}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
