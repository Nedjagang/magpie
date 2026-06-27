// Package metrics exposes magpied's self-observability surface.
//
// Magpie v1.0 invariant 5 (docs/v1.0-rollout-spec.md §1, docs/v1.0-plan.md §2):
// "Magpied is observable about itself." Operators running 2000+ agents cannot
// diagnose a control plane that doesn't emit its own metrics — they would be
// flying blind during the exact window where things are most likely to go
// wrong (rollout fan-out, push-queue saturation, db lag). This package is
// the first concrete step toward that invariant: an unauthenticated
// /metrics endpoint plus a middleware that observes per-request latency
// and counts.
//
// Scope of this package after the v1.0 rollout metrics drop:
//   - magpie_info{version=...}                   — build info constant 1
//   - magpie_connected_agents                    — gauge, scrape-time pull
//   - magpie_http_requests_total{route,method,status}
//   - magpie_http_request_duration_seconds{route,method}
//   - magpie_apply_state_total{state}            — gauge, scrape-time pull
//   - magpie_rollout_phase_active{phase}         — gauge, scrape-time pull
//   - magpie_validation_latency_seconds          — histogram, observed inline
//
// Deferred to later v1.0 increments:
//   - magpie_db_query_duration_seconds
//   - magpie_audit_log_entries_total
//
// /metrics is intentionally unauthenticated, matching the precedent set by
// /healthz. Standard Prometheus scrapers don't carry bearer tokens by
// default, and forcing them to spreads MAGPIE_API_TOKEN into a second
// process for marginal threat-model benefit on internal-network-only
// deployments. Operators who want the endpoint authed can front it with
// a reverse proxy that adds auth there. This trade-off is documented in
// the v1.0 plan's threat model.
package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// AgentCounter is the minimal interface the connected-agents gauge needs.
// Defined here on the consumer side so internal/opamp doesn't have to take
// a dependency on this package — the wiring in cmd/magpied/main.go adapts
// the Registry's List() to this shape.
type AgentCounter interface {
	Count() int
}

// AgentCounterFunc lets a plain closure satisfy AgentCounter without a
// dedicated type, so call sites can do
//   metrics.New(version, metrics.AgentCounterFunc(func() int { return len(registry.List()) }))
// rather than declaring a wrapper struct.
type AgentCounterFunc func() int

func (f AgentCounterFunc) Count() int { return f() }

// RolloutAggregator returns rollout-state aggregates at scrape time.
// Implemented by *rollouts.Store via the ApplyStateCounts and
// RolloutPhaseCounts methods. Defined here on the consumer side so
// the rollouts package doesn't take a dependency on this one — the
// adapter wiring happens in cmd/magpied/main.go.
type RolloutAggregator interface {
	ApplyStateCounts(ctx context.Context) (map[string]int, error)
	RolloutPhaseCounts(ctx context.Context) (map[string]int, error)
}

// ValidationObserver receives validation-call latency observations.
// rollouts.Service implements this through a small adapter wired in
// main.go (the rollouts package itself doesn't import metrics).
type ValidationObserver interface {
	ObserveValidationLatency(d time.Duration)
}

// Registry holds magpied's Prometheus collectors and a handful of metrics
// that the HTTP middleware + rollouts service write into. One instance
// per process; threaded through to anywhere that needs to record.
type Registry struct {
	reg *prometheus.Registry

	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
	validationLatency   prometheus.Histogram
}

// New wires up the collectors and returns a Registry ready to mount.
//
// version is exported as a label on magpie_info so operators can join
// metrics across magpied restarts and version bumps without losing the
// continuity of the underlying time series.
//
// agents is sampled at scrape time via a GaugeFunc — there's no event
// stream from the registry into this package, which keeps the coupling
// one-way and avoids a callback maze for what amounts to "len of an
// in-memory map."
//
// rolloutAgg is optional. When non-nil it drives the rollout-aware
// gauges (`magpie_apply_state_total`, `magpie_rollout_phase_active`)
// at scrape time via a custom Collector. Pass nil to omit those
// metrics — useful for tests + for v0.2-style deployments that haven't
// flipped to v1.0 rollouts yet.
func New(version string, agents AgentCounter, rolloutAgg RolloutAggregator) *Registry {
	reg := prometheus.NewRegistry()

	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "magpie_info",
		Help: "Build info for magpied; constant value 1, useful for joining version labels onto other metrics.",
	}, []string{"version"})
	info.WithLabelValues(version).Set(1)
	reg.MustRegister(info)

	connected := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "magpie_connected_agents",
		Help: "Number of agents currently connected via OpAMP. Sampled at scrape time from the in-memory registry.",
	}, func() float64 { return float64(agents.Count()) })
	reg.MustRegister(connected)

	httpReqs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "magpie_http_requests_total",
		Help: "Total HTTP requests handled by magpied, labelled by chi route pattern, method, and status.",
	}, []string{"route", "method", "status"})
	reg.MustRegister(httpReqs)

	// DefBuckets covers 5ms..10s which is the right shape for magpied's
	// surface: most requests are sub-100ms (DB read, JSON encode), the
	// occasional zip download or audit page is in the hundreds of ms.
	// If/when a slow path appears that genuinely lives above 10s, switch
	// to ExponentialBuckets without breaking existing dashboards.
	httpDur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "magpie_http_request_duration_seconds",
		Help:    "Latency of HTTP requests handled by magpied, labelled by chi route pattern and method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method"})
	reg.MustRegister(httpDur)

	// Validation latency — the time configs.ValidateWith spends in the
	// rollouts.Service.Create flow. Smaller buckets than DefBuckets
	// because validation is cheap (sub-50ms structural; sub-second
	// once semantic validation lands). Tail above 1s indicates the
	// semantic validator went off the rails.
	validationDur := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "magpie_validation_latency_seconds",
		Help:    "Latency of structural + semantic config validation in the rollout publish path.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	})
	reg.MustRegister(validationDur)

	// Rollout-aware collectors fire at scrape time. Only registered if
	// the aggregator is provided so test harnesses that don't have a
	// rollouts.Store don't have to stub it out.
	if rolloutAgg != nil {
		reg.MustRegister(&rolloutCollector{
			aggregator: rolloutAgg,
			applyStateDesc: prometheus.NewDesc(
				"magpie_apply_state_total",
				"Per-host apply_state counts across non-terminal rollouts. Labelled by state (pending/applying/applied/failed). Sampled at scrape time.",
				[]string{"state"}, nil,
			),
			rolloutPhaseDesc: prometheus.NewDesc(
				"magpie_rollout_phase_active",
				"Number of non-terminal rollouts in each state (validating/canary/soak/promoting/paused). Sampled at scrape time.",
				[]string{"phase"}, nil,
			),
		})
	}

	return &Registry{
		reg:                 reg,
		httpRequestsTotal:   httpReqs,
		httpRequestDuration: httpDur,
		validationLatency:   validationDur,
	}
}

// ObserveValidationLatency records one observation. Wired through
// rollouts.Service via a small adapter in main.go so the rollouts
// package doesn't depend on metrics.
func (m *Registry) ObserveValidationLatency(d time.Duration) {
	m.validationLatency.Observe(d.Seconds())
}

// rolloutCollector emits the two rollout-aware gauges at scrape time.
// Implementing prometheus.Collector directly (rather than a flock of
// individual GaugeFuncs) lets us emit one time series per state /
// phase without pre-declaring every label value — the set of states
// and phases is small and stable, but operators can extend the state
// machine in v1.x without churning this collector.
type rolloutCollector struct {
	aggregator       RolloutAggregator
	applyStateDesc   *prometheus.Desc
	rolloutPhaseDesc *prometheus.Desc
}

func (c *rolloutCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.applyStateDesc
	ch <- c.rolloutPhaseDesc
}

func (c *rolloutCollector) Collect(ch chan<- prometheus.Metric) {
	// Use a background context with a short cap so a slow / hung DB
	// doesn't block scrapes indefinitely. 2s is generous for an
	// indexed COUNT GROUP BY query at v1.0 fleet scale; if a real
	// scrape hangs this long the operator has bigger problems than
	// missing rollout metrics.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Apply-state counts. Always emit the four canonical states even
	// when zero so operators don't get gaps in their dashboards when
	// nothing's in flight.
	applyCounts, err := c.aggregator.ApplyStateCounts(ctx)
	if err != nil {
		// Don't break the scrape; metrics for this scrape just miss.
		// Operators investigating gaps should check magpied logs.
		applyCounts = map[string]int{}
	}
	for _, state := range []string{"pending", "applying", "applied", "failed"} {
		ch <- prometheus.MustNewConstMetric(c.applyStateDesc, prometheus.GaugeValue, float64(applyCounts[state]), state)
	}

	// Rollout phase counts. Always emit the five non-terminal phases.
	phaseCounts, err := c.aggregator.RolloutPhaseCounts(ctx)
	if err != nil {
		phaseCounts = map[string]int{}
	}
	for _, phase := range []string{"validating", "canary", "soak", "promoting", "paused"} {
		ch <- prometheus.MustNewConstMetric(c.rolloutPhaseDesc, prometheus.GaugeValue, float64(phaseCounts[phase]), phase)
	}
}

// Handler returns the http.Handler that serves /metrics in Prometheus
// text exposition format. Wire it into the unauthenticated section of
// the router (alongside /healthz).
func (m *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Middleware observes per-request latency and counts. Place it early in
// the chain (after RequestID/Recoverer, before route dispatch) so it
// captures every request including ones that hit a 404 or get rejected
// by auth. Cardinality is bounded by registered chi routes × ~7 methods
// × ~10 status codes, so it stays manageable on a single instance.
//
// The route label is the chi RoutePattern (e.g.
// "/api/v1/configs/{id}/rollback") rather than the raw path — without
// that, a 2000-agent fleet's per-host calls would explode cardinality.
// Unmatched paths get the literal "unmatched" label so operators see
// them aggregated rather than lost.
//
// We use chi's WrapResponseWriter rather than rolling our own because
// /v1/opamp does a WebSocket Hijack — a hand-written wrapper that
// doesn't forward the Hijacker interface would silently break OpAMP
// connections. WrapResponseWriter forwards Hijacker, Flusher, Pusher
// correctly.
func (m *Registry) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			status := strconv.Itoa(ww.Status())
			if ww.Status() == 0 {
				// chi reports Status()=0 when the handler never called
				// WriteHeader (default 200) or after a hijack. Treat
				// the never-WriteHeader case as 200; a hijacked OpAMP
				// connection has no meaningful HTTP status anyway.
				status = "200"
			}

			m.httpRequestsTotal.WithLabelValues(route, r.Method, status).Inc()
			m.httpRequestDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		})
	}
}
