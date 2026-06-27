// Command magpied is the Magpie control plane server.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/magpie-project/magpie/control-plane/internal/agents"
	"github.com/magpie-project/magpie/control-plane/internal/audit"
	"github.com/magpie-project/magpie/control-plane/internal/configs"
	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/install"
	"github.com/magpie-project/magpie/control-plane/internal/labels"
	"github.com/magpie-project/magpie/control-plane/internal/metrics"
	"github.com/magpie-project/magpie/control-plane/internal/opamp"
	"github.com/magpie-project/magpie/control-plane/internal/pagination"
	"github.com/magpie-project/magpie/control-plane/internal/releases"
	"github.com/magpie-project/magpie/control-plane/internal/rollouts"
	"github.com/magpie-project/magpie/control-plane/internal/semantic"
)

// version is overwritten at build time via -ldflags.
var version = "0.2.0-dev"

// maxWriteBodyBytes caps the request body for state-changing endpoints. 1 MiB
// covers a generous YAML config (the largest legitimate payload) plus headroom.
// Anything larger is rejected with 413 instead of buffered into memory or DB.
// JSON-only endpoints (label PUT, agent delete) need far less; the same cap
// applies for simplicity.
const maxWriteBodyBytes = 1 << 20

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	logger.Info("magpie control plane starting", "version", version)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// MAGPIE_DATABASE_URL takes precedence (per ADR 0007 and v1.0 plan §6.1
	// #1), falling back to MAGPIE_DB_PATH for v0.2 backwards compatibility.
	// Operators who set neither get the default "./magpie.db" SQLite file —
	// same as v0.2.
	dsn := os.Getenv("MAGPIE_DATABASE_URL")
	if dsn == "" {
		dsn = envOr("MAGPIE_DB_PATH", "magpie.db")
	}
	conn, err := db.Open(dsn)
	if err != nil {
		// We never log the raw DSN — postgres:// URLs commonly carry
		// credentials. The error from db.Open already contains backend
		// context ("ping postgres", "ping sqlite", etc.).
		logger.Error("database open failed", "err", err)
		os.Exit(1)
	}
	defer conn.Close()
	logger.Info("database ready", "dialect", string(conn.Dialect))

	configStore := configs.NewStore(conn)
	auditStore := audit.NewStore(conn)
	labelStore := labels.NewStore(conn)
	agentStore := agents.NewStore(conn)
	releaseStore := releases.NewStore(envOr("MAGPIE_RELEASES_DIR", "releases"), version)

	// Warm the in-memory label-override cache from disk.
	overrider := newLabelOverrider(labelStore)
	if err := overrider.warm(ctx); err != nil {
		logger.Warn("could not warm label overrides", "err", err)
	}

	registry := opamp.NewRegistry()
	registry.SetOverrider(overrider)
	// Install the agent persister BEFORE warming, then hydrate the in-memory
	// map from whatever we persisted on the last run. Without this, the UI's
	// Hosts view blanks on every magpied restart until hosts heartbeat back.
	registry.SetPersister(agentStore, logger)
	if err := registry.Warm(ctx); err != nil {
		logger.Warn("could not warm agent registry", "err", err)
	}

	// v1.0 self-observability (docs/v1.0-plan.md §2 invariant 5). Sampled
	// at scrape time from registry.List(); no event-driven coupling needed
	// for the 2000-agent target — len() over an in-memory map is microseconds.
	// rolloutStore (constructed below) implements metrics.RolloutAggregator,
	// driving magpie_apply_state_total and magpie_rollout_phase_active at
	// scrape time. Constructed here, registered on the metrics registry below.
	rolloutStore := rollouts.NewStore(conn)
	metricsReg := metrics.New(version, metrics.AgentCounterFunc(func() int {
		return len(registry.List())
	}), rolloutStore)

	authToken := os.Getenv("MAGPIE_API_TOKEN")
	if authToken == "" {
		logger.Warn("MAGPIE_API_TOKEN is unset — running in v0.1-compatible no-auth mode. " +
			"Anyone with network reachability can publish configs to every agent. " +
			"Set MAGPIE_API_TOKEN to a 32+ character random string to enable auth.")
	} else {
		logger.Info("bearer-token auth enabled", "token_sha256_prefix", fmt.Sprintf("%x", sha256.Sum256([]byte(authToken)))[:12])
	}

	policy := configs.Policy{
		Receivers:  parseCSVList(os.Getenv("MAGPIE_ALLOWED_RECEIVERS")),
		Processors: parseCSVList(os.Getenv("MAGPIE_ALLOWED_PROCESSORS")),
		Exporters:  parseCSVList(os.Getenv("MAGPIE_ALLOWED_EXPORTERS")),
		Extensions: parseCSVList(os.Getenv("MAGPIE_ALLOWED_EXTENSIONS")),
		Connectors: parseCSVList(os.Getenv("MAGPIE_ALLOWED_CONNECTORS")),
	}
	switch {
	case len(policy.Receivers)+len(policy.Processors)+len(policy.Exporters)+len(policy.Extensions)+len(policy.Connectors) == 0:
		logger.Warn("no MAGPIE_ALLOWED_* component allowlists set — any otelcol component the operator deploys " +
			"will pass validation. Set MAGPIE_ALLOWED_RECEIVERS / _PROCESSORS / _EXPORTERS to constrain.")
	default:
		logger.Info("config component allowlist active",
			"receivers", policy.Receivers,
			"processors", policy.Processors,
			"exporters", policy.Exporters,
			"extensions", policy.Extensions,
			"connectors", policy.Connectors)
	}

	// v1.0 Rollout primitive (docs/v1.0-rollout-spec.md). Constructed
	// before the OpAMP server because (a) the server's ConfigProvider
	// delegates resolution to rolloutSvc.ResolveConfigFor, and (b) the
	// server's HeartbeatHook is rolloutSvc.HandleHeartbeat. The
	// registryHostResolver adapter lets rollouts pick canary targets
	// from the live OpAMP registry without opamp depending on rollouts.
	// rolloutStore was constructed earlier (alongside metricsReg) so
	// the metrics registry could register the rollout-aware collectors.
	rolloutSvc := rollouts.NewService(
		rolloutStore, configStore,
		registryHostResolver{registry: registry},
		policy, auditStore, logger,
	)
	// Wire the validation-latency histogram. Adapter is implicit:
	// metricsReg.ObserveValidationLatency satisfies rollouts.ValidationObserver.
	rolloutSvc.SetValidationObserver(metricsReg)

	// Semantic validation is opt-in (docs/v1.0-semantic-validation.md):
	// enabled only when MAGPIE_OTELCOL_VALIDATOR_PATH points at a real
	// otelcol-contrib binary. Default is structural YAML validation only —
	// trust the agent's own otelcol to accept/reject at apply time and
	// report via ApplyState. The platform-bound nature of the validator
	// (a Windows binary can't validate Linux-only journald) makes
	// publish-side gating more brittle than helpful by default.
	if validatorPath := semantic.LookupBinary(os.Getenv("MAGPIE_OTELCOL_VALIDATOR_PATH")); validatorPath != "" {
		validator, err := semantic.NewOtelcolValidator(validatorPath)
		if err != nil {
			logger.Error("semantic validator init failed; structural-only validation will run", "path", validatorPath, "err", err)
		} else {
			rolloutSvc.SetSemanticValidator(validator)
			logger.Info("semantic validation enabled (opt-in)", "binary", validatorPath)
		}
	} else {
		logger.Info("semantic validation off — set MAGPIE_OTELCOL_VALIDATOR_PATH to enable")
	}

	opampSrv, err := opamp.NewServer(logger, registry, configProvider{store: configStore, rolloutSvc: rolloutSvc})
	if err != nil {
		logger.Error("opamp server init failed", "err", err)
		os.Exit(1)
	}
	// Heartbeat hook drives apply_state transitions from agent reports.
	opampSrv.SetHeartbeatHook(rolloutSvc)

	// v1.0 background ticker — calls rollouts.AdvancePhase for every
	// non-terminal rollout every 10 seconds (matches the gate-evaluation
	// cadence in spec §4.3). The state machine itself is single-tick
	// idempotent — repeated calls to AdvancePhase on a rollout that
	// has nothing new to advance return changed=false without side
	// effects, so the cadence isn't safety-critical.
	go runRolloutTicker(ctx, rolloutSvc, rolloutStore, logger)

	addr := envOr("MAGPIE_HTTP_ADDR", ":12002")
	srv := &http.Server{
		Addr:              addr,
		Handler:           router(logger, authToken, policy, configStore, auditStore, labelStore, agentStore, overrider, registry, opampSrv, releaseStore, metricsReg, rolloutSvc),
		ReadHeaderTimeout: 5 * time.Second,
		// Pin TLS 1.2 as the floor. Go's default already starts at TLS
		// 1.2 in current versions; setting it explicitly survives a
		// future Go release that changes the default and locks operators
		// out of accidentally weaker negotiation. Cipher selection is
		// left to Go's default (TLS 1.3 + AEAD-only TLS 1.2 suites).
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	tlsCert := os.Getenv("MAGPIE_TLS_CERT")
	tlsKey := os.Getenv("MAGPIE_TLS_KEY")
	tlsEnabled := tlsCert != "" && tlsKey != ""
	switch {
	case tlsEnabled:
		logger.Info("TLS enabled", "cert", tlsCert, "key_path_set", true)
	case tlsCert != "" || tlsKey != "":
		logger.Error("partial TLS config: both MAGPIE_TLS_CERT and MAGPIE_TLS_KEY must be set; refusing to start half-configured")
		os.Exit(1)
	default:
		logger.Info("TLS disabled — listening on plain HTTP/WS. Front with a TLS-terminating reverse proxy in production.")
	}

	go func() {
		var err error
		if tlsEnabled {
			logger.Info("https server listening", "addr", addr)
			err = srv.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			logger.Info("http server listening", "addr", addr)
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("magpie control plane shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "err", err)
	}
}

// actorOf composes the audit "actor" string for an inbound request.
//
// With auth enabled (MAGPIE_API_TOKEN set) the prefix is "authenticated"
// — meaning "presented the shared API token" — and X-Magpie-Actor, if any,
// is appended as a label after a colon. So an audit row can read
// "authenticated:alice@corp" or just "authenticated" if the operator
// didn't set the header. The token-holder identity is the primary fact;
// the X-Magpie-Actor label is a hint, never a security claim.
//
// With auth disabled (v0.1-compatible) we fall back to the legacy
// X-Magpie-Actor or "anonymous", and the audit row should be treated
// as suspect — anyone could have submitted it. That's still a useful
// signal during the migration window, just not a forensic anchor.
func actorOf(req *http.Request) string {
	state, _ := req.Context().Value(authContextKey{}).(authState)
	label := strings.TrimSpace(req.Header.Get("X-Magpie-Actor"))
	switch {
	case state.authenticated && label != "":
		return "authenticated:" + label
	case state.authenticated:
		return "authenticated"
	case label != "":
		return "anonymous:" + label
	default:
		return "anonymous"
	}
}

// ─────────────────────────────────────────────────────────────────────────
// Label overrider — in-memory cache over labels.Store, satisfies
// opamp.LabelOverrider.
// ─────────────────────────────────────────────────────────────────────────

type labelOverrider struct {
	store *labels.Store
	mu    sync.RWMutex
	cache map[string]labels.Override
}

func newLabelOverrider(store *labels.Store) *labelOverrider {
	return &labelOverrider{store: store, cache: map[string]labels.Override{}}
}

func (l *labelOverrider) warm(ctx context.Context) error {
	all, err := l.store.All(ctx)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.cache = all
	l.mu.Unlock()
	return nil
}

func (l *labelOverrider) Override(uid string) (string, string, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	o, ok := l.cache[uid]
	if !ok {
		return "", "", false
	}
	return o.Product, o.Variant, true
}

func (l *labelOverrider) set(ctx context.Context, uid, product, variant string) error {
	if err := l.store.Set(ctx, uid, product, variant); err != nil {
		return err
	}
	l.mu.Lock()
	l.cache[uid] = labels.Override{InstanceUID: uid, Product: product, Variant: variant}
	l.mu.Unlock()
	return nil
}

func (l *labelOverrider) clear(ctx context.Context, uid string) error {
	if err := l.store.Clear(ctx, uid); err != nil {
		return err
	}
	l.mu.Lock()
	delete(l.cache, uid)
	l.mu.Unlock()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────
// HTTP routing
// ─────────────────────────────────────────────────────────────────────────

func router(
	logger *slog.Logger,
	authToken string,
	policy configs.Policy,
	store *configs.Store,
	auditStore *audit.Store,
	_ *labels.Store,
	agentStore *agents.Store,
	overrider *labelOverrider,
	registry *opamp.Registry,
	opampSrv *opamp.Server,
	releaseStore *releases.Store,
	metricsReg *metrics.Registry,
	rolloutSvc *rollouts.Service,
) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	// Metrics middleware sits early in the chain so every request — including
	// unmatched-route 404s and auth-rejected 401s — is observed. Placed after
	// Recoverer so a panic doesn't break observation, but before security
	// headers / CORS so we count those rejections too. WrapResponseWriter
	// inside the middleware forwards Hijacker, so /v1/opamp's WebSocket
	// upgrade still works.
	r.Use(metricsReg.Middleware())
	r.Use(securityHeadersMiddleware)
	r.Use(corsMiddleware(parseCSVList(os.Getenv("MAGPIE_ALLOWED_ORIGINS"))))

	// /healthz stays unauthenticated so load balancers / liveness probes
	// can call it without holding the bearer token. It returns version
	// + status only — no fleet data.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
	})

	// /metrics is unauthenticated, matching /healthz. Standard Prometheus
	// scrapers don't carry bearer tokens by default, and forcing them to
	// would spread MAGPIE_API_TOKEN into a second process for marginal
	// threat-model benefit on internal-network deployments. Operators
	// who want it authed can front it with a reverse proxy. Trade-off
	// documented in internal/metrics/metrics.go.
	r.Handle("/metrics", metricsReg.Handler())

	auth := bearerAuthMiddleware(authToken)
	r.With(auth).Handle("/v1/opamp", opampSrv.Handler())

	// One-line bootstrap for new hosts. Returns a generated bash or
	// PowerShell script with the operator's chosen product/variant +
	// this magpied's URL baked in. Lives under /api/v1 so any reverse
	// proxy that already routes the API to magpied (Ingress, Caddy,
	// nginx) routes these too with no extra rules — root-level paths
	// were a reliability footgun in mixed UI+magpied deployments.
	installHandler := func(shell string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			params, err := install.FromRequest(req)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			// Bake the server's own token into the rendered script so the
			// new agent can present it on its first OpAMP connect. The
			// auth middleware on /api/v1 already verified this caller is
			// allowed to be holding the token, so we're not leaking it
			// to a stranger — we're handing it back to the caller in a
			// form they can deploy.
			params.Token = authToken
			body, err := install.Render(shell, params)
			if err != nil {
				logger.Error("render install script", "shell", shell, "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "render failed"})
				return
			}
			w.Header().Set("Content-Type", install.ContentType(shell))
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(body)
		}
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(auth)
		r.Get("/install.sh", installHandler("bash"))
		r.Get("/install.ps1", installHandler("powershell"))
		// ── Agents
		// Cursor pagination over the in-memory registry snapshot
		// (docs/v1.0-plan.md §6.1 #2). Stable ordering by InstanceUID so
		// pagination is deterministic across requests — last_seen-based
		// ordering would churn as agents heartbeat. Default 500 leaves
		// 2000-host fleets four round-trips.
		r.Get("/agents", func(w http.ResponseWriter, req *http.Request) {
			p, err := pagination.Parse(req.URL.Query(), pagination.DefaultLimit)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			all := registry.List()
			sort.Slice(all, func(i, j int) bool { return all[i].InstanceUID < all[j].InstanceUID })

			start := 0
			if p.Cursor != "" {
				start = sort.Search(len(all), func(i int) bool { return all[i].InstanceUID > p.Cursor })
			}
			end := min(start+p.Limit, len(all))
			page := all[start:end]

			var nextCursor string
			if end < len(all) && len(page) > 0 {
				nextCursor = page[len(page)-1].InstanceUID
			}
			pagination.WriteHeaders(w, len(all), nextCursor)
			writeJSON(w, http.StatusOK, page)
		})

		r.Get("/agents/{uid}", func(w http.ResponseWriter, req *http.Request) {
			a, ok := registry.Get(chi.URLParam(req, "uid"))
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			writeJSON(w, http.StatusOK, a)
		})

		r.Put("/agents/{uid}/labels", func(w http.ResponseWriter, req *http.Request) {
			uid := chi.URLParam(req, "uid")
			var body struct {
				Product string `json:"product"`
				Variant string `json:"variant"`
			}
			if !decodeJSONBody(w, req, &body) {
				return
			}
			if body.Product == "" || body.Variant == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "product and variant are required"})
				return
			}
			if _, exists := registry.Get(uid); !exists {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
				return
			}
			if err := overrider.set(req.Context(), uid, body.Product, body.Variant); err != nil {
				logger.Error("set label override", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			labelPayload, _ := json.Marshal(map[string]any{
				"product": body.Product, "variant": body.Variant,
			})
			if err := auditStore.RecordEvent(req.Context(), audit.Event{
				Actor: actorOf(req), Type: audit.EventLabelChanged,
				ScopeKind: "instance", ScopeRef: uid, HostRef: uid,
				PayloadJSON: string(labelPayload),
				Product:     body.Product, Variant: body.Variant,
			}); err != nil {
				logger.Error("audit record failed", "type", audit.EventLabelChanged, "err", err)
			}
			opampSrv.Reconcile(req.Context())
			w.WriteHeader(http.StatusNoContent)
		})

		r.Delete("/agents/{uid}/labels", func(w http.ResponseWriter, req *http.Request) {
			uid := chi.URLParam(req, "uid")
			if err := overrider.clear(req.Context(), uid); err != nil {
				logger.Error("clear label override", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			if err := auditStore.RecordEvent(req.Context(), audit.Event{
				Actor: actorOf(req), Type: audit.EventLabelCleared,
				ScopeKind: "instance", ScopeRef: uid, HostRef: uid,
				PayloadJSON: "{}",
			}); err != nil {
				logger.Error("audit record failed", "type", audit.EventLabelCleared, "err", err)
			}
			opampSrv.Reconcile(req.Context())
			w.WriteHeader(http.StatusNoContent)
		})

		// Delete an agent record entirely. Removes the in-memory registry
		// entry, the persisted row, and any label override for the uid. A
		// live agent that's still connected will re-register on its next
		// heartbeat — with a deterministic InstanceUid that lands back in
		// the same uid, so deleting a live host is harmless.
		//
		// Idempotent: returns 204 even if the uid is unknown, so a UI that
		// double-clicks delete doesn't surface a confusing 404.
		r.Delete("/agents/{uid}", func(w http.ResponseWriter, req *http.Request) {
			uid := chi.URLParam(req, "uid")
			// Best-effort label-override cleanup. We keep going on error
			// because the primary intent is to remove the agent row;
			// leaving a dangling override entry is a smaller wart than
			// refusing the delete.
			if err := overrider.clear(req.Context(), uid); err != nil {
				logger.Warn("clear label override during agent delete", "uid", uid, "err", err)
			}
			registry.Remove(uid)
			if err := agentStore.Delete(req.Context(), uid); err != nil {
				logger.Error("delete agent row", "uid", uid, "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			if err := auditStore.RecordEvent(req.Context(), audit.Event{
				Actor: actorOf(req), Type: audit.EventHostDeleted,
				ScopeKind: "instance", ScopeRef: uid, HostRef: uid,
				PayloadJSON: "{}",
			}); err != nil {
				logger.Error("audit record failed", "type", audit.EventHostDeleted, "err", err)
			}
			w.WriteHeader(http.StatusNoContent)
		})

		// Operator escape hatch: force a server-initiated re-push of the
		// currently-resolved config to one agent. Used when apply_state
		// has been stuck in "applying" for longer than reasonable —
		// typically because the prior push was suppressed somewhere in
		// the OpAMP delivery path. The endpoint bypasses the "agent
		// already reports this hash applied" short-circuit so the wire
		// retry actually happens.
		r.Post("/agents/{uid}/repush", func(w http.ResponseWriter, req *http.Request) {
			uid := chi.URLParam(req, "uid")
			sent, err := opampSrv.RepushForUID(req.Context(), uid)
			if err != nil {
				logger.Error("repush", "uid", uid, "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if !sent {
				writeJSON(w, http.StatusConflict, map[string]string{
					"error": "agent is not currently connected; nothing to push to",
				})
				return
			}
			if err := auditStore.RecordEvent(req.Context(), audit.Event{
				Actor: actorOf(req), Type: audit.EventConfigRepushed,
				ScopeKind: "instance", ScopeRef: uid, HostRef: uid,
				PayloadJSON: "{}",
			}); err != nil {
				logger.Error("audit record failed", "type", audit.EventConfigRepushed, "err", err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"sent": true})
		})

		// ── Configs
		// Cursor pagination on configs.List, with X-Total-Count from
		// CountFiltered. Configs are bounded by (#products × #variants ×
		// #revisions) so the count query is cheap; if revision history
		// ever grows large enough to hurt, switch to estimate or drop.
		r.Get("/configs", func(w http.ResponseWriter, req *http.Request) {
			p, err := pagination.Parse(req.URL.Query(), pagination.DefaultLimit)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			filter := configs.ListFilter{
				Product: req.URL.Query().Get("product"),
				Variant: req.URL.Query().Get("variant"),
			}
			list, nextCursor, err := store.List(req.Context(), filter, p)
			if err != nil {
				logger.Error("list configs", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			total, terr := store.CountFiltered(req.Context(), filter)
			if terr != nil {
				// Count failure shouldn't fail the request — total is
				// informational. Sentinel -1 tells WriteHeaders to skip it.
				logger.Warn("count configs", "err", terr)
				total = -1
			}
			pagination.WriteHeaders(w, total, nextCursor)
			writeJSON(w, http.StatusOK, list)
		})

		r.Post("/configs", func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				Name    string `json:"name"`
				Product string `json:"product"`
				Variant string `json:"variant"`
				YAML    string `json:"yaml"`
			}
			if !decodeJSONBody(w, req, &body) {
				return
			}
			if body.Name == "" || body.YAML == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and yaml are required"})
				return
			}
			if err := configs.ValidateWith(body.YAML, policy); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			c, err := store.Create(req.Context(), body.Product, body.Variant, body.Name, body.YAML)
			if err != nil {
				logger.Error("create config", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			createPayload, _ := json.Marshal(map[string]any{
				"name": c.Name, "bytes": len(c.YAML),
			})
			cfgID := c.ID
			if err := auditStore.RecordEvent(req.Context(), audit.Event{
				Actor: actorOf(req), Type: audit.EventConfigCreated,
				ScopeKind: "product_variant", ScopeRef: c.Product + "/" + c.Variant,
				ConfigRef:   &cfgID,
				PayloadJSON: string(createPayload),
				Product:     c.Product, Variant: c.Variant, TargetID: &cfgID,
			}); err != nil {
				logger.Error("audit record failed", "type", audit.EventConfigCreated, "err", err)
			}
			opampSrv.Reconcile(req.Context())
			writeJSON(w, http.StatusCreated, c)
		})

		r.Post("/configs/{id}/rollback", func(w http.ResponseWriter, req *http.Request) {
			id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
				return
			}
			prev, ok, err := store.Get(req.Context(), id)
			if err != nil {
				logger.Error("get config", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			newName := prev.Name + " (rollback)"
			c, err := store.Create(req.Context(), prev.Product, prev.Variant, newName, prev.YAML)
			if err != nil {
				logger.Error("rollback create", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			rollbackPayload, _ := json.Marshal(map[string]any{
				"from_id": prev.ID, "new_name": c.Name,
			})
			cfgID := c.ID
			if err := auditStore.RecordEvent(req.Context(), audit.Event{
				Actor: actorOf(req), Type: audit.EventConfigRollback,
				ScopeKind: "product_variant", ScopeRef: c.Product + "/" + c.Variant,
				ConfigRef:   &cfgID,
				PayloadJSON: string(rollbackPayload),
				Product:     c.Product, Variant: c.Variant, TargetID: &cfgID,
			}); err != nil {
				logger.Error("audit record failed", "type", audit.EventConfigRollback, "err", err)
			}
			opampSrv.Reconcile(req.Context())
			writeJSON(w, http.StatusCreated, c)
		})

		// ── Destructive product / variant removal
		r.Delete("/products/{product}", func(w http.ResponseWriter, req *http.Request) {
			product := chi.URLParam(req, "product")
			n, err := store.DeleteProduct(req.Context(), product)
			if err != nil {
				logger.Error("delete product", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			productDeletePayload, _ := json.Marshal(map[string]any{
				"product":         product,
				"configs_removed": n,
			})
			if err := auditStore.RecordEvent(req.Context(), audit.Event{
				Actor: actorOf(req), Type: audit.EventProductDeleted,
				PayloadJSON: string(productDeletePayload),
				Product:     product,
			}); err != nil {
				logger.Error("audit record failed", "type", audit.EventProductDeleted, "err", err)
			}
			opampSrv.Reconcile(req.Context())
			writeJSON(w, http.StatusOK, map[string]any{"removed": n})
		})

		r.Delete("/products/{product}/variants/{variant}", func(w http.ResponseWriter, req *http.Request) {
			product := chi.URLParam(req, "product")
			variant := chi.URLParam(req, "variant")
			n, err := store.DeleteVariant(req.Context(), product, variant)
			if err != nil {
				logger.Error("delete variant", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			variantDeletePayload, _ := json.Marshal(map[string]any{
				"product": product, "variant": variant,
				"configs_removed": n,
			})
			if err := auditStore.RecordEvent(req.Context(), audit.Event{
				Actor: actorOf(req), Type: audit.EventVariantDeleted,
				ScopeKind: "product_variant", ScopeRef: product + "/" + variant,
				PayloadJSON: string(variantDeletePayload),
				Product:     product, Variant: variant,
			}); err != nil {
				logger.Error("audit record failed", "type", audit.EventVariantDeleted, "err", err)
			}
			opampSrv.Reconcile(req.Context())
			writeJSON(w, http.StatusOK, map[string]any{"removed": n})
		})

		// ── Audit chain verification (v1.0)
		//
		// GET /audit/verify walks every chained row and confirms each
		// row's prev_hash matches the prior row's hash AND each row's
		// hash matches the canonical re-computation. Operators / tooling
		// hit this to detect tampering. Returns:
		//   200 OK { "valid": true,  "valid_up_to_id": N }
		//   200 OK { "valid": false, "valid_up_to_id": N, "broken_at_id": M }
		// 200 in both cases — a broken chain is a finding, not a
		// server error.
		r.Get("/audit/verify", func(w http.ResponseWriter, req *http.Request) {
			validUpTo, brokenAt, err := auditStore.VerifyChain(req.Context())
			if err != nil {
				logger.Error("audit verify", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			body := map[string]any{
				"valid":          brokenAt == 0,
				"valid_up_to_id": validUpTo,
			}
			if brokenAt != 0 {
				body["broken_at_id"] = brokenAt
			}
			writeJSON(w, http.StatusOK, body)
		})

		// ── Audit
		// Cursor pagination + filter params per docs/v1.0-audit-query-surface-spec.md §11.2.
		// Filters available: host_ref, scope_kind+scope_ref, type, actor,
		// since (a window like 1h/24h/7d/30d/all). All combined via AND
		// at the Store. Indexes from migration 00009 (idx_audit_host,
		// idx_audit_scope_v1, idx_audit_type) cover the typical queries.
		// X-Total-Count skipped — SELECT COUNT(*) on audit_log scales badly.
		r.Get("/audit", func(w http.ResponseWriter, req *http.Request) {
			p, err := pagination.Parse(req.URL.Query(), 100)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			q := req.URL.Query()
			filter := audit.ListFilter{
				HostRef:   q.Get("host_ref"),
				ScopeKind: q.Get("scope_kind"),
				ScopeRef:  q.Get("scope_ref"),
				Type:      q.Get("type"),
				Actor:     q.Get("actor"),
			}
			// Spec §4.5: since-window picker maps to a Time cutoff. "all"
			// means unbounded (omit the filter); empty defaults to "all"
			// for backwards compat with v0.2 callers that just hit /audit.
			if since := parseSinceWindow(q.Get("since")); !since.IsZero() {
				filter.Since = since
			}

			entries, nextCursor, err := auditStore.List(req.Context(), filter, p)
			if err != nil {
				logger.Error("list audit", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			pagination.WriteHeaders(w, -1, nextCursor)
			writeJSON(w, http.StatusOK, entries)
		})

		// ── Rollouts (v1.0 primitive — see docs/v1.0-rollout-spec.md §9)
		//
		// Maps rollouts.Service onto the API surface from the Rollout
		// spec §9. Each handler is a thin shim: parse params, call the
		// service, map errors to status codes, write JSON. The service
		// owns state-machine validation and audit emission.

		// POST /rollouts — create. Phased default; explicit Instant for emergencies.
		r.Post("/rollouts", func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				ScopeKind  string `json:"scope_kind"`
				ScopeRef   string `json:"scope_ref"`
				Config     struct {
					Name string `json:"name"`
					YAML string `json:"yaml"`
				} `json:"config"`
				RolloutKind string `json:"rollout_kind"`
				Canary      *struct {
					Pct   *int `json:"pct,omitempty"`
					Count *int `json:"count,omitempty"`
				} `json:"canary,omitempty"`
				SoakSeconds *int   `json:"soak_seconds,omitempty"`
				GateMode    string `json:"gate_mode,omitempty"`
			}
			if !decodeJSONBody(w, req, &body) {
				return
			}
			cReq := rollouts.CreateRequest{
				ScopeKind:   rollouts.ScopeKind(body.ScopeKind),
				ScopeRef:    body.ScopeRef,
				ConfigName:  body.Config.Name,
				ConfigYAML:  body.Config.YAML,
				Kind:        rollouts.Kind(body.RolloutKind),
				SoakSeconds: body.SoakSeconds,
				GateMode:    rollouts.GateMode(body.GateMode),
			}
			if body.Canary != nil {
				cReq.CanaryPct = body.Canary.Pct
				cReq.CanaryCount = body.Canary.Count
			}
			r, err := rolloutSvc.Create(req.Context(), cReq, actorOf(req))
			switch {
			case err == nil:
				writeJSON(w, http.StatusCreated, r)
			case errors.As(err, new(*rollouts.ErrInFlight)):
				var ein *rollouts.ErrInFlight
				_ = errors.As(err, &ein)
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":               "rollout_in_progress",
					"existing_rollout_id": ein.ExistingID,
					"existing_state":      string(ein.State),
				})
			case errors.As(err, new(*rollouts.ErrValidate)):
				var ev *rollouts.ErrValidate
				_ = errors.As(err, &ev)
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error":   "validate_failed",
					"details": ev.Detail,
				})
			default:
				logger.Error("create rollout", "err", err)
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
		})

		// GET /rollouts — paginated list with optional Scope + state filters.
		r.Get("/rollouts", func(w http.ResponseWriter, req *http.Request) {
			p, err := pagination.Parse(req.URL.Query(), pagination.DefaultLimit)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			f := rollouts.ListFilter{
				ScopeKind: rollouts.ScopeKind(req.URL.Query().Get("scope_kind")),
				ScopeRef:  req.URL.Query().Get("scope_ref"),
				InFlight:  req.URL.Query().Get("state") == "in_flight",
			}
			list, nextCursor, err := rolloutSvc.List(req.Context(), f, p)
			if err != nil {
				logger.Error("list rollouts", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			pagination.WriteHeaders(w, -1, nextCursor)
			writeJSON(w, http.StatusOK, list)
		})

		// GET /rollouts/{id} — read with apply_state aggregate.
		r.Get("/rollouts/{id}", func(w http.ResponseWriter, req *http.Request) {
			id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
				return
			}
			r, agg, ok, err := rolloutSvc.Get(req.Context(), id)
			if err != nil {
				logger.Error("get rollout", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"rollout":              r,
				"apply_state_summary":  agg,
			})
		})

		// GET /rollouts/{id}/apply-state — per-host detail, paginated.
		// Optional ?host_ref=<instance_uid> restricts the result to a
		// single host (used by the host-drawer in-flight strip so per-poll
		// fetch is one row instead of up to a 5000-row page).
		r.Get("/rollouts/{id}/apply-state", func(w http.ResponseWriter, req *http.Request) {
			id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
				return
			}
			p, err := pagination.Parse(req.URL.Query(), pagination.DefaultLimit)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			hostRef := req.URL.Query().Get("host_ref")
			rows, nextCursor, err := rolloutSvc.ListApplyState(req.Context(), id, hostRef, p)
			if err != nil {
				logger.Error("list apply_state", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				return
			}
			pagination.WriteHeaders(w, -1, nextCursor)
			writeJSON(w, http.StatusOK, rows)
		})

		// Operator actions. All five share the same id-parse + service-call
		// + error-mapping shape; helper closure wraps that.
		rolloutAction := func(action string, fn func(ctx context.Context, id int64, actor, body string) (*rollouts.Rollout, error)) http.HandlerFunc {
			return func(w http.ResponseWriter, req *http.Request) {
				id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
					return
				}
				// Body is optional; only Abort uses it for an operator-supplied reason.
				var body struct {
					Reason string `json:"reason,omitempty"`
				}
				if req.ContentLength > 0 {
					if !decodeJSONBody(w, req, &body) {
						return
					}
				}
				r, err := fn(req.Context(), id, actorOf(req), body.Reason)
				switch {
				case err == nil:
					writeJSON(w, http.StatusOK, r)
				case rollouts.IsNotFound(err):
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
				case errors.Is(err, rollouts.ErrIllegalOperatorAction):
					writeJSON(w, http.StatusConflict, map[string]string{
						"error":  "illegal_action",
						"action": action,
					})
				default:
					logger.Error(action+" rollout", "err", err)
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				}
			}
		}

		r.Post("/rollouts/{id}/pause", rolloutAction("pause",
			func(ctx context.Context, id int64, actor, _ string) (*rollouts.Rollout, error) {
				return rolloutSvc.Pause(ctx, id, actor)
			}))
		r.Post("/rollouts/{id}/resume", rolloutAction("resume",
			func(ctx context.Context, id int64, actor, _ string) (*rollouts.Rollout, error) {
				return rolloutSvc.Resume(ctx, id, actor)
			}))
		r.Post("/rollouts/{id}/abort", rolloutAction("abort",
			func(ctx context.Context, id int64, actor, reason string) (*rollouts.Rollout, error) {
				return rolloutSvc.Abort(ctx, id, actor, reason)
			}))
		r.Post("/rollouts/{id}/fast-promote", rolloutAction("fast-promote",
			func(ctx context.Context, id int64, actor, _ string) (*rollouts.Rollout, error) {
				return rolloutSvc.FastPromote(ctx, id, actor)
			}))
		r.Post("/rollouts/{id}/promote", rolloutAction("promote",
			func(ctx context.Context, id int64, actor, _ string) (*rollouts.Rollout, error) {
				return rolloutSvc.Promote(ctx, id, actor)
			}))

		// ── Releases (agent + collector binary distribution)
		//
		// Powers the "Download agent" button in the UI onboarding flow.
		// The catalog lets the UI enable/disable platforms based on what
		// has actually been published; the zip endpoint streams the binary
		// bundle for a chosen platform.
		r.Get("/releases", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, releaseStore.Catalog())
		})

		r.Get("/releases/{os}/{arch}", func(w http.ResponseWriter, req *http.Request) {
			osName := chi.URLParam(req, "os")
			arch := chi.URLParam(req, "arch")
			filename := fmt.Sprintf("magpie-%s-%s.zip", osName, arch)
			// Set headers BEFORE streaming; once WriteZip writes a byte we
			// can no longer change the status code or content-type.
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
			if err := releaseStore.WriteZip(w, osName, arch); err != nil {
				// Errors from pre-flight checks (invalid platform / not
				// available / missing binaries) fire before any body is
				// written, so JSON is still safe. If we ever reach an
				// error mid-stream, the client sees a truncated zip —
				// acceptable because platformBinaries pre-flights both
				// files before the zip writer is created.
				w.Header().Del("Content-Disposition")
				switch {
				case errors.Is(err, releases.ErrInvalidPlatform):
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				case errors.Is(err, releases.ErrPlatformNotAvailable), errors.Is(err, releases.ErrBinariesMissing):
					writeJSON(w, http.StatusNotFound, map[string]string{
						"error": err.Error(),
						"hint":  "drop built binaries into " + releaseStore.Dir() + "/<os>-<arch>/ or run the release workflow",
					})
				default:
					logger.Error("release zip", "err", err, "os", osName, "arch", arch)
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
				}
			}
		})

		// Cosign sidecar files for the agent binary. Served only when the
		// release workflow has produced them; absence is a clean 404 rather
		// than a 500 so install scripts can decide whether to insist on
		// verification or warn-and-skip. Content-type is text/plain — both
		// .sig and .cert are PEM-encoded ASCII.
		attestationHandler := func(kind releases.AttestationKind) http.HandlerFunc {
			return func(w http.ResponseWriter, req *http.Request) {
				osName := chi.URLParam(req, "os")
				arch := chi.URLParam(req, "arch")
				w.Header().Set("Content-Type", "text/plain; charset=us-ascii")
				w.Header().Set("Cache-Control", "no-store")
				if err := releaseStore.WriteAttestation(w, osName, arch, kind); err != nil {
					switch {
					case errors.Is(err, releases.ErrInvalidPlatform):
						writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					case errors.Is(err, releases.ErrAttestationMissing),
						errors.Is(err, releases.ErrPlatformNotAvailable),
						errors.Is(err, releases.ErrBinariesMissing):
						writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
					default:
						logger.Error("release attestation", "err", err, "kind", kind, "os", osName, "arch", arch)
						writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
					}
				}
			}
		}
		r.Get("/releases/{os}/{arch}/signature", attestationHandler(releases.AttestationSignature))
		r.Get("/releases/{os}/{arch}/certificate", attestationHandler(releases.AttestationCertificate))
	})

	return r
}

// authContextKey is the type used as a context key for the authentication
// state attached by bearerAuthMiddleware. Concrete type rather than string
// to avoid collisions with any other middleware that touches context.
type authContextKey struct{}

// authState records what bearerAuthMiddleware found out about the caller.
// "authenticated=true, no token configured" is the v0.1-compatible no-auth
// path; we still let the request through but tag actor as anonymous in
// audit so operators can distinguish unauthenticated activity in logs.
type authState struct {
	enabled       bool // token-based auth is active
	authenticated bool // current request presented a valid bearer token
}

// bearerFromHeader extracts the token from an `Authorization: Bearer <t>`
// header. Returns "" if the header is missing or malformed.
func bearerFromHeader(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

// bearerAuthMiddleware enforces a single shared bearer token on protected
// routes. Empty token disables enforcement (logged loudly at startup) so
// v0.1 → v0.2 upgrades don't immediately break existing fleets — the
// operator opts in by setting MAGPIE_API_TOKEN, which is the documented
// migration path.
//
// Constant-time comparison via crypto/subtle, with sha256 hashing first to
// equalize length differences (keeps timing-attack window flat regardless
// of how close a guess is to the real token).
//
// Verifying at every request rather than caching anything per-connection:
// the token cost is one sha256 over a short string, well below other
// per-request costs (DB query, JSON encode).
func bearerAuthMiddleware(expected string) func(http.Handler) http.Handler {
	if expected == "" {
		// No token configured → pass-through, but tag the request so audit
		// records the actor as anonymous and downstream code can detect.
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(r.Context(), authContextKey{}, authState{enabled: false})
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		}
	}
	expectedHash := sha256.Sum256([]byte(expected))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := bearerFromHeader(r)
			if got == "" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="magpie"`)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
				return
			}
			gotHash := sha256.Sum256([]byte(got))
			if subtle.ConstantTimeCompare(gotHash[:], expectedHash[:]) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid bearer token"})
				return
			}
			ctx := context.WithValue(r.Context(), authContextKey{}, authState{enabled: true, authenticated: true})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// securityHeadersMiddleware sets a small, conservative set of response
// headers on every reply: nosniff (don't let a JSON body get rendered as
// HTML), DENY framing (the API is never legitimately embedded), and a
// strict referrer policy. CSP is intentionally NOT set here — magpied
// returns JSON / shell scripts / zip bytes, not HTML — so a CSP would
// just be a misleading badge. The UI's CSP is the UI's responsibility.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// parseSinceWindow maps the audit-section since chip ("1h"/"24h"/"7d"/"30d")
// to a time.Time cutoff. "all" or empty returns the zero Time, which
// audit.ListFilter treats as "no since filter." Per docs/v1.0-audit-query-surface-spec.md §4.5.
func parseSinceWindow(v string) time.Time {
	now := time.Now().UTC()
	switch v {
	case "1h":
		return now.Add(-1 * time.Hour)
	case "24h":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	case "30d":
		return now.Add(-30 * 24 * time.Hour)
	default:
		return time.Time{}
	}
}

// parseCSVList splits a comma-separated env-var value, trims whitespace,
// and drops empties. Returns nil on empty input — callers downstream
// treat nil as "feature off" (same-origin only for CORS, no enforcement
// for component allowlists).
func parseCSVList(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// corsMiddleware enforces an explicit origin allowlist. Defaults to closed:
// without MAGPIE_ALLOWED_ORIGINS set, no cross-origin requests succeed (the
// expected prod posture is UI and API behind one reverse proxy → same-origin,
// no CORS needed). For dev, set MAGPIE_ALLOWED_ORIGINS=http://localhost:12001.
//
// Reverses prior wildcard-ACAO behavior, which let any site on the operator's
// VPN drive authed writes via a victim browser.
func corsMiddleware(allowed []string) func(http.Handler) http.Handler {
	allowSet := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		allowSet[o] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				// Browsers send Origin on same-origin POST/fetch calls when
				// behind a reverse proxy (Origin == scheme+host of the page).
				// Treat that as same-origin and pass through without CORS
				// headers — no cross-origin dance needed.
				requestOrigin := "https://" + r.Host
				if r.TLS == nil {
					requestOrigin = "http://" + r.Host
				}
				sameOrigin := origin == requestOrigin
				if _, ok := allowSet[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Magpie-Actor")
				} else if !sameOrigin && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
					// Cross-origin AND not in allowlist AND a state-changing
					// method: reject so an unapproved origin cannot drive
					// writes via a victim browser.
					http.Error(w, "cross-origin request not permitted", http.StatusForbidden)
					return
				}
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// configProvider adapts rollouts.Service onto opamp.ConfigProvider.
//
// v1.0: rollouts.Service.ResolveConfigFor handles the full resolution
// chain — in-flight rollouts (with apply_state side-effects), then
// instance-scope live Config, then product+variant-scope live Config,
// then the v0.2 fallback through configs.Store.LatestFor.
//
// The store field is retained for the rare case where rolloutSvc isn't
// wired (defensive — main.go always sets it). If rolloutSvc is nil,
// we fall through to the v0.2 LatestFor path so the OpAMP loop never
// returns a confusing "no provider" error.
type configProvider struct {
	store      *configs.Store
	rolloutSvc *rollouts.Service
}

func (p configProvider) ResolveFor(ctx context.Context, instanceUID, product, variant string) (string, bool, error) {
	if p.rolloutSvc != nil {
		yaml, _, ok, err := p.rolloutSvc.ResolveConfigFor(ctx, instanceUID, product, variant)
		if err != nil || ok {
			return yaml, ok, err
		}
		return "", false, nil
	}
	c, ok, err := p.store.LatestFor(ctx, product, variant)
	if err != nil || !ok {
		return "", false, err
	}
	return c.YAML, true, nil
}

// runRolloutTicker drives rollouts.AdvancePhase for every non-terminal
// rollout every 10 seconds. Returns when ctx is cancelled (shutdown).
//
// Cadence chosen to match the gate-evaluation cadence in spec §4.3 —
// soak-window evaluation happens every 10s server-side, so calling
// AdvancePhase at the same cadence gives operators a consistent
// "checked X seconds ago" semantic.
//
// At fleet scale, this is cheap: NonTerminal returns ids only (~tens at
// worst); each AdvancePhase call is a couple of indexed reads + a
// bounded set of writes. If the active-rollout set ever grows past
// dozens (multi-thousand-host fleet with many concurrent rollouts) we
// can shard or back off to per-rollout cadence.
func runRolloutTicker(ctx context.Context, svc *rollouts.Service, store *rollouts.Store, logger *slog.Logger) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ids, err := store.NonTerminal(ctx)
			if err != nil {
				logger.Warn("rollout ticker: list non-terminal", "err", err)
				continue
			}
			for _, id := range ids {
				if _, err := svc.AdvancePhase(ctx, id); err != nil {
					logger.Warn("rollout ticker: advance", "rollout_id", id, "err", err)
				}
			}
		}
	}
}

// registryHostResolver adapts *opamp.Registry to rollouts.HostResolver.
// Defined here in main.go so the opamp package doesn't take a dep on
// rollouts. Walks the registry snapshot at call time — at fleet sizes
// targeted by v1.0 (≤ 2000), this is microseconds.
type registryHostResolver struct{ registry *opamp.Registry }

func (a registryHostResolver) ConnectedForScope(kind rollouts.ScopeKind, ref string) []string {
	all := a.registry.List()
	switch kind {
	case rollouts.ScopeProductVariant:
		// scope_ref encodes "product/variant".
		product, variant, ok := strings.Cut(ref, "/")
		if !ok {
			return nil
		}
		out := make([]string, 0, len(all))
		for _, agent := range all {
			if agent.EffectiveProduct == product && agent.EffectiveVariant == variant {
				out = append(out, agent.InstanceUID)
			}
		}
		return out
	case rollouts.ScopeInstance:
		// scope_ref is the agent instance_uid; include only if connected.
		for _, agent := range all {
			if agent.InstanceUID == ref {
				return []string{agent.InstanceUID}
			}
		}
		return nil
	default:
		return nil
	}
}

// decodeJSONBody reads a JSON body with a hard size cap. On overrun returns
// 413 (matches the underlying http.MaxBytesError semantic); on parse failure
// returns 400. Keeps every write handler one-liner-clean instead of repeating
// the MaxBytesReader + Decode + status-mapping dance.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": fmt.Sprintf("body exceeds %d-byte limit", maxWriteBodyBytes),
			})
			return false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("json response encode failed", "err", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
