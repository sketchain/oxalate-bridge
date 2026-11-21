// Command fakecpa runs fake CLIProxyAPI instances (with the cpa-quota plugin)
// for local development: one Claude account per instance on consecutive
// ports, each reporting quota, usage-bearing SSE / JSON responses and the
// unified rate-limit headers. Reported usage grows with every call and the
// windows roll over at their reset.
//
//	fakecpa -listen 127.0.0.1:18400 -n 4
//
// Instance i (1-based) listens on port+i-1 and is named demo-<i>; its CPA key
// is cpa-key-demo-<i> and its quota token quota-demo-<i> (bridge seed-demo
// imports them with the same names).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/sketchain/oxalate-bridge/backend/internal/fakecpa"
)

// profile shapes one demo account.
type profile struct {
	tier                 string
	fiveUsed, weekUsed   float64
	fiveTotal, weekTotal float64
	fiveReset, weekReset time.Duration
}

// profiles cover the states the panel shows: plenty of room, above the 5-hour
// watermark (penalized), a full 5-hour window, and a quiet account.
var profiles = []profile{
	{"max20x", 18, 32, 40, 400, 3 * time.Hour, 4 * 24 * time.Hour},
	{"max5x", 78, 55, 10, 100, 90 * time.Minute, 2 * 24 * time.Hour},
	{"max5x", 100, 71, 10, 100, 40 * time.Minute, 5 * 24 * time.Hour},
	{"pro", 5, 12, 4, 40, 4 * time.Hour, 6 * 24 * time.Hour},
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18400", "host:port of the first instance; the others use the next ports")
	n := flag.Int("n", 4, "number of instances (accounts)")
	delay := flag.Duration("delay", 300*time.Millisecond, "time to first byte of relay responses")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	host, portStr, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Error("bad -listen", "err", err)
		os.Exit(2)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || *n < 1 {
		log.Error("bad -listen port or -n")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	now := time.Now().UTC()
	var servers []*http.Server
	for i := 1; i <= *n; i++ {
		addr := net.JoinHostPort(host, strconv.Itoa(port+i-1))
		c := fakecpa.NewUnstarted(fmt.Sprintf("demo-%d", i))
		p := profiles[(i-1)%len(profiles)]
		c.Tier, c.FiveUsed, c.WeekUsed, c.FiveTotal, c.WeekTotal = p.tier, p.fiveUsed, p.weekUsed, p.fiveTotal, p.weekTotal
		c.FiveReset, c.WeekReset = now.Add(p.fiveReset).Truncate(time.Second), now.Add(p.weekReset).Truncate(time.Minute)
		c.BaseURL, c.Drift, c.Delay = "http://"+addr, true, *delay
		srv := &http.Server{Addr: addr, Handler: c, ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("listen", "addr", addr, "err", err)
				stop()
			}
		}()
		log.Info("fake CPA", "instance", c.Instance, "url", c.BaseURL, "cpa_key", c.Key, "quota_token", c.QuotaToken, "tier", c.Tier)
	}
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
}
