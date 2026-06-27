package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// stubRolloutAggregator returns canned rollout aggregates for the
// rollout-collector tests. Doesn't touch a real DB.
type stubRolloutAggregator struct {
	apply, phase map[string]int
}

func (s *stubRolloutAggregator) ApplyStateCounts(_ context.Context) (map[string]int, error) {
	return s.apply, nil
}

func (s *stubRolloutAggregator) RolloutPhaseCounts(_ context.Context) (map[string]int, error) {
	return s.phase, nil
}

// TestRegistryExposes covers the happy path: create a Registry, run a
// request through the middleware, scrape /metrics, and assert the four
// metrics this drop promises are present and shaped correctly.
//
// Not exhaustive — the goal is to catch regressions where a refactor
// silently drops a metric or breaks the chi RoutePattern lookup.
func TestRegistryExposes(t *testing.T) {
	reg := New("v1.0.0-test", AgentCounterFunc(func() int { return 7 }), nil)

	r := chi.NewRouter()
	r.Use(reg.Middleware())
	r.Get("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	})
	r.Handle("/metrics", reg.Handler())

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Drive one observed request through the middleware so the HTTP
	// counter has something non-zero to report.
	resp, err := http.Get(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET /api/v1/agents: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agents endpoint status: got %d want 200", resp.StatusCode)
	}

	// Scrape /metrics. promhttp serves the standard exposition format;
	// we don't need a parser — string assertions are stable enough for
	// the four metrics we own here.
	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status: got %d want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	out := string(body)

	// magpie_info — version label must round-trip.
	if !strings.Contains(out, `magpie_info{version="v1.0.0-test"} 1`) {
		t.Errorf("magpie_info with version label not found in /metrics output:\n%s", out)
	}

	// magpie_connected_agents — pulls AgentCounter at scrape time.
	if !strings.Contains(out, `magpie_connected_agents 7`) {
		t.Errorf("magpie_connected_agents = 7 not found in /metrics output:\n%s", out)
	}

	// magpie_http_requests_total — we made one observed request to the
	// /agents route. Both /agents and /metrics flow through the
	// middleware, but we only assert the agents counter to avoid
	// coupling the test to ordering of scrape vs. request observation.
	if !strings.Contains(out, `magpie_http_requests_total{method="GET",route="/api/v1/agents",status="200"} 1`) {
		t.Errorf("magpie_http_requests_total for /api/v1/agents not found:\n%s", out)
	}

	// magpie_http_request_duration_seconds — histogram has _count and
	// _sum companion series. Just check the _count exists for the route.
	if !strings.Contains(out, `magpie_http_request_duration_seconds_count{method="GET",route="/api/v1/agents"}`) {
		t.Errorf("magpie_http_request_duration_seconds histogram not found for /api/v1/agents:\n%s", out)
	}
}

// TestUnmatchedRouteLabel ensures requests that don't match any chi route
// still get observed under a stable label rather than exploding cardinality
// with the raw path. Important at fleet scale where misconfigured scrapers
// or scanners can fire arbitrary URLs.
func TestUnmatchedRouteLabel(t *testing.T) {
	reg := New("v1.0.0-test", AgentCounterFunc(func() int { return 0 }), nil)

	r := chi.NewRouter()
	r.Use(reg.Middleware())
	r.Handle("/metrics", reg.Handler())

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Hit a nonexistent path; should 404 and increment with route="unmatched".
	resp, err := http.Get(srv.URL + "/does/not/exist/at/all")
	if err != nil {
		t.Fatalf("GET unknown: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	if !strings.Contains(out, `route="unmatched"`) {
		t.Errorf("expected route=\"unmatched\" label for 404 path, got:\n%s", out)
	}
	// Sanity: must not contain the raw path as a label value.
	if strings.Contains(out, `route="/does/not/exist/at/all"`) {
		t.Errorf("raw path leaked into route label — cardinality risk:\n%s", out)
	}
}

// TestRolloutAwareMetricsExpose covers the v1.0 rollout collector + the
// validation-latency histogram. Confirms:
//   - the rollout-aware collectors only register when an aggregator is provided
//   - apply_state and rollout_phase emit a series per canonical state/phase
//     even when the aggregator returns empty (no gaps in dashboards)
//   - non-zero counts pass through correctly
//   - validation latency observations land in the histogram
func TestRolloutAwareMetricsExpose(t *testing.T) {
	agg := &stubRolloutAggregator{
		apply: map[string]int{"applied": 12, "pending": 3},
		phase: map[string]int{"canary": 1, "soak": 2},
	}
	reg := New("v1.0.0-test", AgentCounterFunc(func() int { return 1 }), agg)
	reg.ObserveValidationLatency(50 * time.Millisecond)

	r := chi.NewRouter()
	r.Handle("/metrics", reg.Handler())
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	// Aggregator-derived counts pass through.
	if !strings.Contains(out, `magpie_apply_state_total{state="applied"} 12`) {
		t.Errorf("magpie_apply_state_total{state=applied} = 12 missing:\n%s", out)
	}
	if !strings.Contains(out, `magpie_rollout_phase_active{phase="canary"} 1`) {
		t.Errorf("magpie_rollout_phase_active{phase=canary} = 1 missing:\n%s", out)
	}

	// Canonical states emit even when the aggregator omits them — operators
	// shouldn't see gaps in dashboards when a state has zero rows.
	if !strings.Contains(out, `magpie_apply_state_total{state="failed"} 0`) {
		t.Errorf("magpie_apply_state_total{state=failed} = 0 missing:\n%s", out)
	}
	if !strings.Contains(out, `magpie_rollout_phase_active{phase="paused"} 0`) {
		t.Errorf("magpie_rollout_phase_active{phase=paused} = 0 missing:\n%s", out)
	}

	// Validation latency observation landed.
	if !strings.Contains(out, `magpie_validation_latency_seconds_count 1`) {
		t.Errorf("magpie_validation_latency_seconds_count = 1 missing:\n%s", out)
	}
}

// TestRolloutAwareMetricsOmitWhenAggregatorNil confirms that passing
// nil for the aggregator omits the rollout-aware metrics entirely
// (rather than emitting all-zeros). Useful for v0.2-style deploys
// that don't have rollouts wired yet.
func TestRolloutAwareMetricsOmitWhenAggregatorNil(t *testing.T) {
	reg := New("v1.0.0-test", AgentCounterFunc(func() int { return 0 }), nil)
	r := chi.NewRouter()
	r.Handle("/metrics", reg.Handler())
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	if strings.Contains(out, "magpie_apply_state_total") {
		t.Errorf("magpie_apply_state_total should be absent when aggregator=nil:\n%s", out)
	}
	if strings.Contains(out, "magpie_rollout_phase_active") {
		t.Errorf("magpie_rollout_phase_active should be absent when aggregator=nil:\n%s", out)
	}
}
