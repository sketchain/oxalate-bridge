package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
)

// Delivery parameters.
const (
	// MessageTTL is how long a push service keeps an undelivered message.
	MessageTTL = time.Hour
	// DefaultDedupTTL: one notification per user, event and object.
	DefaultDedupTTL = 30 * time.Minute
	maxAttempts     = 5
	retryBase       = 10 * time.Second
	vapidSettingKey = "vapid_keys"
)

// Service owns the VAPID keys, the queue and the senders.
type Service struct {
	Store    *store.Store
	St       *rstate.State
	Box      *secret.Box
	Settings *settings.Store
	Log      *slog.Logger
	// Subject returns the VAPID contact in effect (mailto: or https:).
	Subject func() string
	// Location formats dates in texts.
	Location *time.Location
	// HTTPClient sends to push services (default: 15 s timeout).
	HTTPClient *http.Client
	// AllowAnyEndpoint accepts http and any host (tests with a fake push
	// service). Otherwise endpoints must be https on a known push service.
	AllowAnyEndpoint bool
	// DedupTTL overrides DefaultDedupTTL (tests).
	DedupTTL time.Duration
	// Now overrides the clock of the state detectors (tests); the queue
	// always uses the wall clock.
	Now func() time.Time

	// queued counts first deliveries put on the queue (retries excluded).
	queued atomic.Int64

	mu      sync.RWMutex
	public  string
	private string
	kick    chan struct{}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) dedupTTL() time.Duration {
	if s.DedupTTL > 0 {
		return s.DedupTTL
	}
	return DefaultDedupTTL
}

func (s *Service) loc() *time.Location {
	if s.Location != nil {
		return s.Location
	}
	return time.UTC
}

// ---- VAPID ------------------------------------------------------------------

type storedKeys struct {
	Public     string `json:"public_key"`
	PrivateEnc string `json:"private_key_enc"`
}

// EnsureVAPID loads the key pair from the settings table, generating it on
// the first start (the first replica to insert wins).
func (s *Service) EnsureVAPID(ctx context.Context) error {
	raw, err := s.Store.Q.GetSetting(ctx, vapidSettingKey)
	if errors.Is(err, pgx.ErrNoRows) {
		priv, pub, gerr := webpush.GenerateVAPIDKeys()
		if gerr != nil {
			return fmt.Errorf("generate vapid keys: %w", gerr)
		}
		val, _ := json.Marshal(storedKeys{Public: pub, PrivateEnc: base64.StdEncoding.EncodeToString(s.Box.Seal(priv))})
		if err := s.Store.Q.InsertSettingIfAbsent(ctx, sqlc.InsertSettingIfAbsentParams{Key: vapidSettingKey, Value: val}); err != nil {
			return fmt.Errorf("store vapid keys: %w", err)
		}
		raw, err = s.Store.Q.GetSetting(ctx, vapidSettingKey)
	}
	if err != nil {
		return fmt.Errorf("load vapid keys: %w", err)
	}
	var k storedKeys
	if err := json.Unmarshal(raw, &k); err != nil {
		return fmt.Errorf("decode vapid keys: %w", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(k.PrivateEnc)
	if err != nil {
		return fmt.Errorf("decode vapid private key: %w", err)
	}
	priv, err := s.Box.Open(sealed)
	if err != nil {
		return fmt.Errorf("open vapid private key (BRIDGE_SECRET_KEY changed?): %w", err)
	}
	s.mu.Lock()
	s.public, s.private = k.Public, priv
	s.mu.Unlock()
	return nil
}

// PublicKey is the VAPID public key for PushManager.subscribe.
func (s *Service) PublicKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.public
}

// ---- endpoints and devices ----------------------------------------------------

var pushHosts = []string{
	"fcm.googleapis.com", "android.googleapis.com", // Chrome, Edge (Android)
	"push.services.mozilla.com", // Firefox
	"push.apple.com",            // Safari / iOS (web.push.apple.com)
	"notify.windows.com",        // Edge (Windows)
	"push.api.chromium.org",     // Chromium
}

// ValidEndpoint checks a subscription endpoint: https on a known push
// service, so the bridge never posts to arbitrary hosts.
func (s *Service) ValidEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("endpoint is not a URL")
	}
	if s.AllowAnyEndpoint {
		return nil
	}
	if u.Scheme != "https" {
		return errors.New("endpoint must use https")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range pushHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return fmt.Errorf("endpoint host %q is not a known push service", host)
}

// DeviceLabel guesses "device · browser" from a User-Agent.
func DeviceLabel(ua string) string {
	l := strings.ToLower(ua)
	device := ""
	switch {
	case strings.Contains(l, "iphone"):
		device = "iPhone"
	case strings.Contains(l, "ipad"):
		device = "iPad"
	case strings.Contains(l, "android"):
		device = "Android"
	case strings.Contains(l, "macintosh") || strings.Contains(l, "mac os x"):
		device = "Mac"
	case strings.Contains(l, "windows"):
		device = "Windows"
	case strings.Contains(l, "cros"):
		device = "ChromeOS"
	case strings.Contains(l, "linux"):
		device = "Linux"
	}
	browser := ""
	switch {
	case strings.Contains(l, "edg/") || strings.Contains(l, "edga/") || strings.Contains(l, "edgios/"):
		browser = "Edge"
	case strings.Contains(l, "firefox/") || strings.Contains(l, "fxios/"):
		browser = "Firefox"
	case strings.Contains(l, "opr/"):
		browser = "Opera"
	case strings.Contains(l, "samsungbrowser/"):
		browser = "Samsung Internet"
	case strings.Contains(l, "chrome/") || strings.Contains(l, "crios/"):
		browser = "Chrome"
	case strings.Contains(l, "safari/"):
		browser = "Safari"
	}
	switch {
	case device != "" && browser != "":
		return device + " · " + browser
	case device != "":
		return device
	case browser != "":
		return browser
	}
	return "Unknown device"
}

// ---- preferences --------------------------------------------------------------

// Pref is the effective switch of one event for one user.
type Pref struct {
	Spec
	Enabled bool
}

// Prefs returns the events a user can receive with their switches, plus the
// balance threshold.
func (s *Service) Prefs(ctx context.Context, user uuid.UUID, role string) ([]Pref, int64, error) {
	rows, err := s.Store.Q.ListNotificationPrefs(ctx, user)
	if err != nil {
		return nil, 0, err
	}
	stored := map[string]sqlc.NotificationPref{}
	for _, r := range rows {
		stored[r.Event] = r
	}
	threshold := DefaultBalanceThreshold
	if r, ok := stored[BalanceLow]; ok && r.ThresholdMicro != nil {
		threshold = *r.ThresholdMicro
	}
	var out []Pref
	for _, sp := range Catalog {
		if !Visible(sp, role) {
			continue
		}
		p := Pref{Spec: sp, Enabled: sp.DefaultEnabled}
		if r, ok := stored[sp.Event]; ok {
			p.Enabled = r.Enabled
		}
		out = append(out, p)
	}
	return out, threshold, nil
}

// SetPrefs stores switches and / or the balance threshold.
func (s *Service) SetPrefs(ctx context.Context, user uuid.UUID, role string, enabled map[string]bool, threshold *int64) error {
	cur, curThreshold, err := s.Prefs(ctx, user, role)
	if err != nil {
		return err
	}
	if threshold == nil {
		threshold = &curThreshold
	}
	return s.Store.InTx(ctx, func(tx *store.Tx) error {
		for _, p := range cur {
			on, changed := enabled[p.Event]
			if !changed {
				on = p.Enabled
			}
			if !changed && p.Event != BalanceLow {
				continue
			}
			var th *int64
			if p.Event == BalanceLow {
				th = threshold
			}
			if err := tx.Q.UpsertNotificationPref(ctx, sqlc.UpsertNotificationPrefParams{UserID: user, Event: p.Event,
				Enabled: on, ThresholdMicro: th}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- notifications --------------------------------------------------------------

// Notification is one event occurrence. Admin events go to every active
// admin; user events go to User.
type Notification struct {
	Event string
	// Object identifies what the event is about (account, subscription…);
	// together with the user and event it is the dedup key.
	Object string
	User   uuid.UUID
	// URL is the panel path opened on click.
	URL string
	// Params renders the text parameters in a locale.
	Params func(locale string) map[string]string
}

type recipient struct {
	id     uuid.UUID
	locale string
}

// Notify sends n to every recipient whose preference allows it, once per
// recipient within the dedup window (Redis SET NX EX, shared by replicas).
// It returns the number of devices queued.
func (s *Service) Notify(ctx context.Context, n Notification) (int, error) {
	sp, ok := SpecOf(n.Event)
	if !ok {
		return 0, fmt.Errorf("unknown event %q", n.Event)
	}
	var recips []recipient
	if sp.Group == GroupAdmin {
		rows, err := s.Store.Q.ListActiveAdmins(ctx)
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			recips = append(recips, recipient{id: r.ID, locale: r.Locale})
		}
	} else {
		u, err := s.Store.Q.GetUser(ctx, n.User)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil
			}
			return 0, err
		}
		if u.Status != "active" {
			return 0, nil
		}
		recips = append(recips, recipient{id: u.ID, locale: u.Locale})
	}
	if len(recips) == 0 {
		return 0, nil
	}
	prefRows, err := s.Store.Q.ListNotificationPrefsForEvent(ctx, n.Event)
	if err != nil {
		return 0, err
	}
	prefs := map[uuid.UUID]bool{}
	for _, p := range prefRows {
		prefs[p.UserID] = p.Enabled
	}
	queued := 0
	for _, r := range recips {
		on, ok := prefs[r.id]
		if !ok {
			on = sp.DefaultEnabled
		}
		if !on {
			continue
		}
		devices, err := s.Store.Q.ListPushSubscriptions(ctx, r.id)
		if err != nil {
			return queued, err
		}
		if len(devices) == 0 {
			continue
		}
		first, err := s.St.R.SetNX(ctx, s.St.Key("push", "dedup", r.id.String(), n.Event, n.Object), "1", s.dedupTTL()).Result()
		if err != nil {
			return queued, err
		}
		if !first {
			continue
		}
		var params map[string]string
		if n.Params != nil {
			params = n.Params(r.locale)
		}
		title, body := Render(n.Event, r.locale, params)
		urgency := webpush.UrgencyNormal
		if sp.High {
			urgency = webpush.UrgencyHigh
		}
		for _, d := range devices {
			if err := s.enqueue(ctx, job{Sub: d.ID, Event: n.Event, Title: title, Body: body, URL: n.URL,
				Tag: n.Event + ":" + n.Object, Urgency: string(urgency)}, time.Now()); err != nil {
				return queued, err
			}
			queued++
		}
	}
	return queued, nil
}

// SendTest queues a test notification to every device of user (no dedup,
// preferences ignored).
func (s *Service) SendTest(ctx context.Context, user uuid.UUID, locale string) (int, error) {
	devices, err := s.Store.Q.ListPushSubscriptions(ctx, user)
	if err != nil {
		return 0, err
	}
	title, body := Render(testEvent, locale, nil)
	for _, d := range devices {
		if err := s.enqueue(ctx, job{Sub: d.ID, Event: testEvent, Title: title, Body: body, URL: "/settings",
			Tag: "test", Urgency: string(webpush.UrgencyNormal)}, time.Now()); err != nil {
			return 0, err
		}
	}
	return len(devices), nil
}

// ---- queue ----------------------------------------------------------------------

type job struct {
	ID      string    `json:"id"`
	Sub     uuid.UUID `json:"sub"`
	Event   string    `json:"event"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	URL     string    `json:"url"`
	Tag     string    `json:"tag"`
	Urgency string    `json:"urgency"`
	Attempt int       `json:"attempt"`
}

// Payload is what the service worker receives.
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
	Event string `json:"event"`
}

func (s *Service) queueKey() string { return s.St.Key("push", "queue") }

func (s *Service) enqueue(ctx context.Context, j job, due time.Time) error {
	if j.ID == "" {
		j.ID = uuid.NewString()
	}
	if j.Attempt == 0 {
		s.queued.Add(1)
	}
	raw, _ := json.Marshal(j)
	if err := s.St.R.ZAdd(ctx, s.queueKey(), redis.Z{Score: float64(due.UnixMilli()), Member: raw}).Err(); err != nil {
		return err
	}
	s.Kick()
	return nil
}

// Kick wakes the local worker.
func (s *Service) Kick() {
	s.mu.Lock()
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	ch := s.kick
	s.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// popScript takes due jobs atomically so that every replica can run a
// worker without sending a job twice.
var popScript = redis.NewScript(`
local items = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, tonumber(ARGV[2]))
for _, it in ipairs(items) do redis.call('ZREM', KEYS[1], it) end
return items
`)

// Run delivers queued notifications until ctx ends (one worker per replica).
func (s *Service) Run(ctx context.Context) {
	s.Kick()
	s.mu.RLock()
	ch := s.kick
	s.mu.RUnlock()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-ch:
		}
		s.drain(ctx)
	}
}

// drain sends every due job.
func (s *Service) drain(ctx context.Context) {
	for ctx.Err() == nil {
		items, err := popScript.Run(ctx, s.St.R, []string{s.queueKey()}, time.Now().UnixMilli(), 20).StringSlice()
		if err != nil {
			if !errors.Is(err, redis.Nil) && ctx.Err() == nil {
				s.Log.Warn("push queue", "err", err)
			}
			return
		}
		if len(items) == 0 {
			return
		}
		var wg sync.WaitGroup
		for _, it := range items {
			var j job
			if json.Unmarshal([]byte(it), &j) != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.deliver(ctx, j)
			}()
		}
		wg.Wait()
	}
}

// deliver sends one job; 404 / 410 delete the subscription, other failures
// are retried with exponential backoff.
func (s *Service) deliver(ctx context.Context, j job) {
	sub, err := s.Store.Q.GetPushSubscription(ctx, j.Sub)
	if err != nil {
		return // removed meanwhile
	}
	status, err := s.send(ctx, sub, j)
	switch {
	case err == nil && status >= 200 && status < 300:
		_ = s.Store.Q.PushSucceeded(ctx, sub.ID)
		return
	case status == http.StatusNotFound || status == http.StatusGone:
		_ = s.Store.Q.DeletePushSubscriptionByID(ctx, sub.ID)
		s.Log.Info("push subscription expired, removed", "subscription", sub.ID, "status", status)
		return
	}
	_ = s.Store.Q.PushFailed(ctx, sub.ID)
	permanent := status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusForbidden || status == http.StatusUnauthorized
	if permanent || j.Attempt+1 >= maxAttempts {
		s.Log.Warn("push dropped", "subscription", sub.ID, "status", status, "err", err, "attempts", j.Attempt+1)
		return
	}
	j.Attempt++
	delay := retryBase << (j.Attempt - 1)
	if err := s.enqueue(context.WithoutCancel(ctx), j, time.Now().Add(delay)); err != nil {
		s.Log.Warn("requeue push", "err", err)
	}
}

func (s *Service) send(ctx context.Context, sub sqlc.PushSubscription, j job) (int, error) {
	s.mu.RLock()
	pub, priv := s.public, s.private
	s.mu.RUnlock()
	if pub == "" {
		return 0, errors.New("vapid keys not loaded")
	}
	payload, _ := json.Marshal(Payload{Title: j.Title, Body: j.Body, URL: j.URL, Tag: j.Tag, Event: j.Event})
	subject := ""
	if s.Subject != nil {
		subject = s.Subject()
	}
	// webpush-go adds "mailto:" itself unless the subject is an https URL.
	subject = strings.TrimPrefix(subject, "mailto:")
	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := webpush.SendNotificationWithContext(cctx, payload, &webpush.Subscription{Endpoint: sub.Endpoint,
		Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}}, &webpush.Options{HTTPClient: client, Subscriber: subject,
		TTL: int(MessageTTL.Seconds()), Urgency: webpush.Urgency(j.Urgency), VAPIDPublicKey: pub, VAPIDPrivateKey: priv})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// Queued is the number of deliveries queued since start (retries excluded).
func (s *Service) Queued() int64 { return s.queued.Load() }

// QueueLen is the number of queued deliveries (tests, metrics).
func (s *Service) QueueLen(ctx context.Context) int64 {
	n, _ := s.St.R.ZCard(ctx, s.queueKey()).Result()
	return n
}
