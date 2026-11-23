// Package server wires the bridge together: storage, caches, background
// workers and the HTTP handlers.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/sketchain/oxalate-bridge/backend/internal/api"
	"github.com/sketchain/oxalate-bridge/backend/internal/authcache"
	"github.com/sketchain/oxalate-bridge/backend/internal/avail"
	"github.com/sketchain/oxalate-bridge/backend/internal/billing"
	"github.com/sketchain/oxalate-bridge/backend/internal/config"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpa"
	"github.com/sketchain/oxalate-bridge/backend/internal/cpaquota"
	"github.com/sketchain/oxalate-bridge/backend/internal/db"
	"github.com/sketchain/oxalate-bridge/backend/internal/db/sqlc"
	"github.com/sketchain/oxalate-bridge/backend/internal/events"
	"github.com/sketchain/oxalate-bridge/backend/internal/jobs"
	"github.com/sketchain/oxalate-bridge/backend/internal/keymeter"
	"github.com/sketchain/oxalate-bridge/backend/internal/metrics"
	"github.com/sketchain/oxalate-bridge/backend/internal/notify"
	"github.com/sketchain/oxalate-bridge/backend/internal/outbox"
	"github.com/sketchain/oxalate-bridge/backend/internal/proxy"
	"github.com/sketchain/oxalate-bridge/backend/internal/router"
	"github.com/sketchain/oxalate-bridge/backend/internal/rstate"
	"github.com/sketchain/oxalate-bridge/backend/internal/secret"
	"github.com/sketchain/oxalate-bridge/backend/internal/settings"
	"github.com/sketchain/oxalate-bridge/backend/internal/settle"
	"github.com/sketchain/oxalate-bridge/backend/internal/store"
	"github.com/sketchain/oxalate-bridge/backend/internal/subs"
	"github.com/sketchain/oxalate-bridge/backend/internal/version"
	"github.com/sketchain/oxalate-bridge/backend/internal/webui"
)

// Options tune an App (mostly for tests).
type Options struct {
	// Transport overrides the upstream transport.
	Transport http.RoundTripper
	// DisableJobs skips the leader-elected background jobs.
	DisableJobs bool
	// DisableConsumer skips the settlement stream consumer.
	DisableConsumer bool
	// SkipPriceFetch avoids fetching the ratio table at startup.
	SkipPriceFetch bool
	// InstanceID names this replica (leader lock, consumer name).
	InstanceID string
	// AllowAnyPushEndpoint accepts Web Push endpoints on any host and over
	// http (tests with a fake push service).
	AllowAnyPushEndpoint bool
}

// App is one bridge replica.
type App struct {
	Cfg      config.Config
	Log      *slog.Logger
	Pool     *pgxpool.Pool
	Redis    redis.UniversalClient
	St       *rstate.State
	Store    *store.Store
	Box      *secret.Box
	Settings *settings.Store
	Prices   *billing.Prices
	Cat      *router.Catalog
	Router   *router.Router
	Auth     *authcache.Cache
	Hub      *events.Hub
	Outbox   *outbox.Dispatcher
	Consumer *settle.Consumer
	Jobs     *jobs.Runner
	Avail    *avail.Service
	Subs     *subs.Service
	Proxy    *proxy.Proxy
	API      *api.Server
	CPA      *cpa.Client
	Notify   *notify.Service

	opts     Options
	reloadDB chan struct{}
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

// New connects to PostgreSQL and Redis, migrates and loads state.
func New(ctx context.Context, cfg config.Config, log *slog.Logger, opts Options) (*App, error) {
	if opts.InstanceID == "" {
		host, _ := os.Hostname()
		opts.InstanceID = fmt.Sprintf("%s-%s", host, uuid.NewString()[:8])
	}
	box, err := secret.NewBox(cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.Schema)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, pool, cfg.Schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := db.EnsurePartitions(ctx, pool, time.Now().UTC(), 2); err != nil {
		pool.Close()
		return nil, err
	}
	ropts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(ropts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	a := &App{Cfg: cfg, Log: log, Pool: pool, Redis: rdb, Box: box, opts: opts, reloadDB: make(chan struct{}, 1)}
	a.St = rstate.New(rdb, "ob:"+cfg.Schema+":")
	a.Store = store.New(pool)
	a.Settings = settings.NewStore()
	if err := a.Settings.Load(ctx, a.Store.Q); err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	a.Prices = billing.NewPrices(log)
	a.loadPrices(ctx)
	a.CPA = cpa.NewClient()
	a.Cat = router.NewCatalog(a.Store.Q, a.St, box, log)
	if err := a.Cat.ReloadDB(ctx); err != nil {
		return nil, err
	}
	_ = a.Cat.ReloadRedis(ctx)
	a.Router = router.New(a.Cat, a.St, a.Settings)
	a.Auth = authcache.New(a.Store.Q, a.St, log)
	a.Hub = events.NewHub(a.St, log)
	a.Outbox = outbox.New(a.Store, a.St, a.Auth, log)
	a.Store.Kick = a.Outbox.Kick
	a.Notify = &notify.Service{Store: a.Store, St: a.St, Box: box, Settings: a.Settings, Log: log, Location: cfg.Location,
		AllowAnyEndpoint: opts.AllowAnyPushEndpoint}
	if err := a.Notify.EnsureVAPID(ctx); err != nil {
		return nil, err
	}
	a.Subs = &subs.Service{Store: a.Store, St: a.St, Cat: a.Cat, Settings: a.Settings,
		OnPending: a.Notify.SubscriptionPending, OnRenewFailed: a.Notify.AutoRenewFailed}
	a.Consumer = &settle.Consumer{St: a.St, Store: a.Store, Log: log, Name: opts.InstanceID,
		OnSettled: func(sums []settle.Summary) {
			for _, s := range sums {
				a.Hub.Publish(context.Background(), "request_settled", s)
			}
		}}
	a.Avail = &avail.Service{Router: a.Router, St: a.St, Log: log}
	a.Jobs = &jobs.Runner{St: a.St, Store: a.Store, Box: box, CPA: a.CPA, Cat: a.Cat, Router: a.Router, Settings: a.Settings,
		Prices: a.Prices, Subs: a.Subs, Hub: a.Hub, Log: log, ID: opts.InstanceID, Avail: a.Avail, Notify: a.Notify}
	a.Jobs.RefreshPricesFn = a.refreshPrices
	transport := opts.Transport
	if transport == nil {
		transport = proxy.NewTransport()
	}
	meter := &keymeter.Meter{St: a.St, Q: a.Store.Q}
	a.Proxy = &proxy.Proxy{Auth: a.Auth, Router: a.Router, St: a.St, Settings: a.Settings, Prices: a.Prices, Store: a.Store,
		Log: log, Transport: transport, ClientIP: a.ClientIP, OnAccountError: a.requestCatalogReload, Meter: meter,
		OnKeyExhausted: a.Notify.KeyExhausted}
	a.API = &api.Server{Store: a.Store, St: a.St, Box: box, Settings: a.Settings, Router: a.Router, Cat: a.Cat, Subs: a.Subs,
		Prices: a.Prices, CPA: a.CPA, Hub: a.Hub, Auth: a.Auth, Log: log, CookieSecure: cfg.CookieSecure,
		RefreshPrices: a.refreshPrices, Avail: a.Avail, Location: cfg.Location, CPAQuota: &cpaquota.Reader{Pool: pool, Schema: cfg.CPAQuotaSchema},
		Meter: meter, ClientIPOf: a.ClientIP, VAPIDSubject: cfg.VAPIDSubject, Notify: a.Notify}
	a.Notify.Subject = func() string { return a.API.VAPIDSubjectInEffect() }
	a.Proxy.Status = func(ctx context.Context, info authcache.Info) (any, error) { return a.API.KeyStatusOf(ctx, info) }
	a.Hub.OnInvalidate = a.Auth.DropL1
	a.Hub.OnSettings = func() {
		if err := a.Settings.Load(context.Background(), a.Store.Q); err != nil {
			log.Warn("reload settings", "err", err)
		}
		a.loadPrices(context.Background())
	}
	a.Hub.OnRouting = a.requestCatalogReload
	a.Hub.OnResync = func() {
		a.Auth.DropAllL1()
		a.Hub.OnSettings()
		a.requestCatalogReload()
	}
	if err := a.bootstrapAdmin(ctx); err != nil {
		return nil, err
	}
	a.checkEnvironment(ctx)
	if err := settle.EnsureGroup(ctx, a.St); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *App) requestCatalogReload() {
	select {
	case a.reloadDB <- struct{}{}:
	default:
	}
}

// loadPrices installs the ratio table cached in Redis, else the one in PG.
func (a *App) loadPrices(ctx context.Context) {
	if raw, err := a.St.R.Get(ctx, a.St.PriceKey()).Bytes(); err == nil {
		row, perr := a.Store.Q.GetPriceTable(ctx)
		fetched, source := time.Now().UTC(), ""
		if perr == nil {
			fetched, source = row.FetchedAt, row.SourceUrl
			if a.Prices.Table() != nil && a.Prices.Status().FetchedAt.Equal(row.FetchedAt) {
				return // this replica fetched it
			}
		}
		if err := a.Prices.SetBody(raw, "database", source, fetched); err == nil {
			return
		}
	}
	if err := a.Prices.LoadStored(ctx, a.Store.Q); err != nil {
		a.Log.Warn("load stored price table", "err", err)
	}
}

func (a *App) refreshPrices(ctx context.Context) error {
	url := a.Settings.Get().PriceURL
	if err := a.Prices.Refresh(ctx, a.Store.Q, url); err != nil {
		return err
	}
	if row, err := a.Store.Q.GetPriceTable(ctx); err == nil {
		_ = a.St.R.Set(ctx, a.St.PriceKey(), row.Body, 0).Err()
	}
	return a.St.R.Publish(ctx, a.St.ChanSettings(), "prices").Err()
}

// checkEnvironment warns about deployment problems that do not stop startup.
func (a *App) checkEnvironment(ctx context.Context) {
	if res, err := a.Redis.ConfigGet(ctx, "maxmemory-policy").Result(); err == nil {
		if policy := res["maxmemory-policy"]; policy != "" && policy != "noeviction" {
			a.Log.Warn("redis maxmemory-policy is not noeviction; balance and quota keys may be evicted", "policy", policy)
		}
	}
	var reg *string
	if err := a.Pool.QueryRow(ctx, "SELECT to_regclass($1)::text", a.Cfg.CPAQuotaSchema+".estimates").Scan(&reg); err == nil && reg == nil {
		a.Log.Warn("cpa-quota tables not found in this database", "schema", a.Cfg.CPAQuotaSchema)
	}
}

func (a *App) bootstrapAdmin(ctx context.Context) error {
	if a.Cfg.BootstrapAdminUsername == "" {
		return nil
	}
	n, err := a.Store.Q.CountAdmins(ctx)
	if err != nil || n > 0 {
		return err
	}
	id, _ := uuid.NewV7()
	_, err = a.Store.Q.CreateUser(ctx, sqlc.CreateUserParams{ID: id, Username: a.Cfg.BootstrapAdminUsername,
		PasswordHash: secret.HashPassword(a.Cfg.BootstrapAdminPassword), Role: "admin",
		MaxConcurrency: int32(a.Settings.Get().DefaultUserConcurrency)})
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	a.Log.Info("bootstrap admin created", "username", a.Cfg.BootstrapAdminUsername)
	return nil
}

// Start launches the background workers.
func (a *App) Start(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)
	run := func(f func(context.Context)) {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			f(ctx)
		}()
	}
	run(a.Hub.Run)
	run(func(ctx context.Context) { a.Cat.Run(ctx, a.reloadDB) })
	run(a.Outbox.Run)
	run(a.Notify.Run)
	if !a.opts.DisableConsumer {
		run(a.Consumer.Run)
	}
	if !a.opts.DisableJobs {
		run(a.Jobs.Run)
	}
	if !a.opts.SkipPriceFetch && a.Prices.Table() == nil {
		run(func(ctx context.Context) {
			if err := a.refreshPrices(ctx); err != nil {
				a.Log.Warn("initial price fetch", "err", err)
			}
		})
	}
}

// Close stops workers and closes connections.
func (a *App) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	_ = a.Redis.Close()
	a.Pool.Close()
}

// ClientIP returns the client address, honoring X-Forwarded-For only from
// trusted proxies (the rightmost untrusted hop wins).
func (a *App) ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if !a.trusted(remote) {
		return remote.Unmap().String()
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		hops := strings.Split(strings.Join(xff, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			if !a.trusted(addr) || i == 0 {
				return addr.Unmap().String()
			}
		}
	}
	if real := r.Header.Get("X-Real-Ip"); real != "" {
		if addr, err := netip.ParseAddr(strings.TrimSpace(real)); err == nil {
			return addr.Unmap().String()
		}
	}
	return remote.Unmap().String()
}

func (a *App) trusted(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range a.Cfg.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Handler builds the public HTTP handler.
func (a *App) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q}`, version.Version)
	})
	r.Get("/readyz", a.ready)
	a.Proxy.Routes(func(method, pattern string, h http.HandlerFunc) { r.Method(method, pattern, h) })
	// Anything else under /v1 (e.g. the removed /v1/responses and
	// /v1/chat/completions) is an Anthropic-shaped 404.
	r.Handle("/v1/*", http.HandlerFunc(a.Proxy.ServeUnknown))

	r.Route("/api/v1", func(sub chi.Router) {
		sub.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(api.WithClientIP(req.Context(), a.ClientIP(req))))
			})
		})
		sub.Use(a.API.Middleware("/api/v1"))
		strict := api.NewStrictHandlerWithOptions(a.API, nil, a.API.StrictOptions())
		api.HandlerWithOptions(strict, api.ChiServerOptions{BaseRouter: sub,
			ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
				api.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			}})
		sub.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			api.WriteError(w, http.StatusNotFound, "not_found", "no such endpoint")
		})
	})
	r.NotFound(webui.Handler().ServeHTTP)
	return r
}

func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var errs []error
	if err := a.Pool.Ping(ctx); err != nil {
		errs = append(errs, fmt.Errorf("postgres: %w", err))
	}
	if err := a.Redis.Ping(ctx).Err(); err != nil {
		errs = append(errs, fmt.Errorf("redis: %w", err))
	}
	w.Header().Set("Content-Type", "application/json")
	if err := errors.Join(errs...); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, `{"status":"unavailable","error":%q}`, err.Error())
		return
	}
	_, _ = fmt.Fprint(w, `{"status":"ready"}`)
}

// MetricsHandler serves Prometheus metrics.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{})
}
