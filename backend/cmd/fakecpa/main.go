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

func main() {
	listen := flag.String("listen", "127.0.0.1:18400", "host:port of the first instance; the others use the next ports")
	n := flag.Int("n", 4, "number of instances (accounts)")
	delay := flag.Duration("delay", 300*time.Millisecond, "time to first byte of relay responses")
	health := flag.Bool("healthcheck", false, "probe the instances given by -listen and -n and exit 0 when all answer (container health checks)")
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
	if *health {
		os.Exit(probe(host, port, *n))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	now := time.Now().UTC()
	var servers []*http.Server
	for i := 1; i <= *n; i++ {
		addr := net.JoinHostPort(host, strconv.Itoa(port+i-1))
		// The quota figures come from fakecpa.DemoProfiles, which seed-demo
		// also uses for the demonstration cpa-quota estimates.
		c := fakecpa.NewUnstarted(fakecpa.DemoInstance(i))
		c.ApplyDemo(fakecpa.DemoProfileFor(i), now)
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

// probe checks /_fake/healthz on every instance; "0.0.0.0" or "" is probed
// on the loopback address.
func probe(host string, port, n int) int {
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	hc := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < n; i++ {
		resp, err := hc.Get("http://" + net.JoinHostPort(host, strconv.Itoa(port+i)) + "/_fake/healthz") //nolint:noctx // CLI probe
		if err != nil {
			fmt.Fprintln(os.Stderr, "healthcheck:", err)
			return 1
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			fmt.Fprintln(os.Stderr, "healthcheck:", resp.Status)
			return 1
		}
	}
	return 0
}
