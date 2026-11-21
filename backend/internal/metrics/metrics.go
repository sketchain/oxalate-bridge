// Package metrics defines the Prometheus metrics of the bridge.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Registry holds the bridge metrics (plus Go and process collectors).
var Registry = prometheus.NewRegistry()

var (
	// Requests counts finished /v1 requests by tier, endpoint and status class.
	Requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "bridge_requests_total",
		Help: "Finished relay requests."}, []string{"tier", "endpoint", "status"})
	// Rejections counts tier rejections by tier and code.
	Rejections = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "bridge_tier_rejections_total",
		Help: "Tier admission rejections."}, []string{"tier", "code"})
	// UpstreamErrors counts upstream failures by kind.
	UpstreamErrors = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "bridge_upstream_errors_total",
		Help: "Upstream failures (429, auth, 5xx, network, refusal)."}, []string{"kind"})
	// Retries counts same-tier retries on another account.
	Retries = prometheus.NewCounter(prometheus.CounterOpts{Name: "bridge_retries_total", Help: "Retries on another account."})
	// FirstByte observes time to the first upstream byte.
	FirstByte = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "bridge_first_byte_seconds",
		Help: "Time to first upstream byte.", Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 4, 8, 16, 32, 64}}, []string{"tier"})
	// Headroom is each account's H_A in USD.
	Headroom = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "bridge_account_headroom_usd",
		Help: "Account standard-tier headroom H_A in USD."}, []string{"account"})
	// StreamLag is the settlement stream backlog (entries not yet delivered or acked).
	StreamLag = prometheus.NewGauge(prometheus.GaugeOpts{Name: "bridge_settle_stream_backlog", Help: "Settlement stream entries not yet persisted."})
	// SettledRecords counts records persisted by consumers.
	SettledRecords = prometheus.NewCounter(prometheus.CounterOpts{Name: "bridge_settled_records_total", Help: "Settlement records persisted."})
	// ConsumerErrors counts failed settlement batches.
	ConsumerErrors = prometheus.NewCounter(prometheus.CounterOpts{Name: "bridge_settle_batch_errors_total", Help: "Failed settlement batches."})
	// ReconcileBalanceMismatches is the number of users whose ledger sum differs from balance.
	ReconcileBalanceMismatches = prometheus.NewGauge(prometheus.GaugeOpts{Name: "bridge_reconcile_balance_mismatches",
		Help: "Users whose ledger sum differs from users.balance_micro at the last reconcile."})
	// ReconcileChargeDiff is request_logs billed (standard/force) minus api_charge total, micro-USD.
	ReconcileChargeDiff = prometheus.NewGauge(prometheus.GaugeOpts{Name: "bridge_reconcile_charge_diff_micro",
		Help: "Billed balance-tier request total minus api_charge total at the last reconcile."})
)

func init() {
	Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		Requests, Rejections, UpstreamErrors, Retries, FirstByte, Headroom, StreamLag, SettledRecords, ConsumerErrors,
		ReconcileBalanceMismatches, ReconcileChargeDiff)
}
