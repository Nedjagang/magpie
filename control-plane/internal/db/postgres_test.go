//go:build pg

// This file is gated behind the `pg` build tag so the default
// `go test ./...` doesn't pay the cost of downloading + booting an
// embedded Postgres binary. Run it with:
//
//	go test -tags pg ./internal/db/...
//
// Or via the convenience target:
//
//	make test-pg
//
// The embedded-postgres library downloads a Postgres tarball on first
// run (~30 MB) and caches it in $HOME/.embedded-postgres-go/. Subsequent
// runs reuse the cache; startup is ~3s. CI environments without outbound
// network to the Zonky-io download URL will fail; in that case set
// `MAGPIE_TEST_POSTGRES_URL` to an externally-running Postgres and skip
// the embedded path. (That env-var path isn't built yet — file-tagged
// for future expansion.)
package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/magpie-project/magpie/control-plane/internal/agents"
	"github.com/magpie-project/magpie/control-plane/internal/audit"
	"github.com/magpie-project/magpie/control-plane/internal/configs"
	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/labels"
	"github.com/magpie-project/magpie/control-plane/internal/opamp"
	"github.com/magpie-project/magpie/control-plane/internal/pagination"
)

var pgDSN string

// TestMain starts a single embedded Postgres for the file. Each test that
// wants a clean schema calls freshDB() which drops/recreates `public` and
// re-applies migrations — cheap (< 1s) compared to a full PG start/stop.
//
// Port 56789 is non-default to avoid colliding with a host Postgres an
// operator might already have on 5432.
func TestMain(m *testing.M) {
	pg := embeddedpostgres.NewDatabase(
		embeddedpostgres.DefaultConfig().
			Port(56789).
			Database("magpietest").
			Username("magpietest").
			Password("magpietest").
			StartTimeout(60 * time.Second),
	)
	if err := pg.Start(); err != nil {
		log.Fatalf("embedded postgres start: %v", err)
	}
	pgDSN = "postgres://magpietest:magpietest@localhost:56789/magpietest?sslmode=disable"

	code := m.Run()

	if err := pg.Stop(); err != nil {
		log.Printf("embedded postgres stop: %v", err)
	}
	os.Exit(code)
}

// freshDB drops all v1.0 tables + the goose version table and re-applies
// migrations through db.Open. Returns a *db.Conn that's clean of any
// state from prior tests. Cheap because PG itself stays running.
//
// We DROP SCHEMA public CASCADE rather than TRUNCATE-ing each table —
// the cascade also drops the plpgsql function created by migration
// 00006, the indexes, and the goose version row, leaving us with a
// truly fresh slate that mirrors a brand-new install.
func freshDB(t *testing.T) *db.Conn {
	t.Helper()

	raw, err := sql.Open("pgx", pgDSN)
	if err != nil {
		t.Fatalf("raw sql.Open: %v", err)
	}
	if _, err := raw.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO magpietest;`); err != nil {
		raw.Close()
		t.Fatalf("reset schema: %v", err)
	}
	raw.Close()

	conn, err := db.Open(pgDSN)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// ─────────────────────────────────────────────────────────────────────
// Migration porting validation
// ─────────────────────────────────────────────────────────────────────

func TestPostgresMigrationsApply(t *testing.T) {
	conn := freshDB(t)

	if conn.Dialect != db.DialectPostgres {
		t.Errorf("dialect = %q, want postgres", conn.Dialect)
	}

	// Every v1.0 table should be present.
	for _, table := range []string{
		"rollouts", "apply_state", "configs", "audit_log", "agents", "agent_labels",
	} {
		var n int
		if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Errorf("table %q missing after PG migrations: %v", table, err)
		}
	}

	// Migration 00007 — Scope columns.
	var n int
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM configs WHERE scope_kind = 'product_variant'`).Scan(&n); err != nil {
		t.Errorf("configs.scope_kind missing after migration 00007: %v", err)
	}

	// Migration 00009 — audit hash-chain extension.
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE hash = ''`).Scan(&n); err != nil {
		t.Errorf("audit_log.hash missing after migration 00009: %v", err)
	}

	// Migration 00006 — append-only triggers must fire on UPDATE/DELETE.
	// First insert a row so there's something to attempt mutation on.
	if _, err := conn.DB.Exec(
		`INSERT INTO audit_log (actor, action, detail) VALUES ('test', 'test.action', 'seed')`,
	); err != nil {
		t.Fatalf("seed audit_log: %v", err)
	}

	if _, err := conn.DB.Exec(`UPDATE audit_log SET actor = 'hacker' WHERE actor = 'test'`); err == nil {
		t.Errorf("audit_log UPDATE should be blocked by trigger but succeeded")
	}
	if _, err := conn.DB.Exec(`DELETE FROM audit_log WHERE actor = 'test'`); err == nil {
		t.Errorf("audit_log DELETE should be blocked by trigger but succeeded")
	}

	// Verify the row is still there (mutations were blocked).
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE actor = 'test'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("seeded audit row missing after blocked mutations: count=%d err=%v", n, err)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Store roundtrip — configs
// ─────────────────────────────────────────────────────────────────────

func TestPostgresConfigsRoundTrip(t *testing.T) {
	conn := freshDB(t)
	store := configs.NewStore(conn)
	ctx := context.Background()

	// Create exercises INSERT ... RETURNING — the PG-portable id round-trip
	// that replaced LastInsertId().
	c, err := store.Create(ctx, "ship", "linux", "ship-linux-v1", "receivers: {}\nexporters: {}\n")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID == 0 {
		t.Fatalf("Create returned id=0 — RETURNING didn't populate id")
	}
	if c.Product != "ship" || c.Variant != "linux" || c.Name != "ship-linux-v1" {
		t.Errorf("Create returned wrong fields: %+v", c)
	}

	// Get exercises a single-row scan with `?` placeholder rebinding.
	got, ok, err := store.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("Get: not found, expected the row we just created")
	}
	if got.Name != c.Name || got.YAML != c.YAML {
		t.Errorf("Get round-trip mismatch: got %+v, want %+v", got, c)
	}

	// LatestFor exercises the fallback chain.
	latest, ok, err := store.LatestFor(ctx, "ship", "linux")
	if err != nil || !ok {
		t.Fatalf("LatestFor ship/linux: ok=%v err=%v", ok, err)
	}
	if latest.ID != c.ID {
		t.Errorf("LatestFor returned different row: got id=%d, want %d", latest.ID, c.ID)
	}

	// LatestFor on a non-existent scope → fall back to default/default,
	// which has nothing → (Config{}, false, nil).
	if _, ok, err := store.LatestFor(ctx, "doesnotexist", "linux"); err != nil || ok {
		t.Errorf("LatestFor non-existent scope: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// Pagination — Create a few more, then List with limit.
	for i := 2; i <= 5; i++ {
		if _, err := store.Create(ctx, "ship", "linux", fmt.Sprintf("ship-linux-v%d", i), "x: y\n"); err != nil {
			t.Fatalf("Create v%d: %v", i, err)
		}
	}
	page, nextCursor, err := store.List(ctx, configs.ListFilter{Product: "ship", Variant: "linux"}, pagination.Params{Limit: 3})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 3 {
		t.Errorf("List returned %d rows, want 3", len(page))
	}
	if nextCursor == "" {
		t.Errorf("nextCursor empty, expected more pages (5 total, requested 3)")
	}

	// Follow the cursor — should get 2 more rows + nextCursor empty.
	page2, nextCursor2, err := store.List(ctx, configs.ListFilter{Product: "ship", Variant: "linux"}, pagination.Params{Limit: 3, Cursor: nextCursor})
	if err != nil {
		t.Fatalf("List (page 2): %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("List page 2 returned %d rows, want 2", len(page2))
	}
	if nextCursor2 != "" {
		t.Errorf("nextCursor2 = %q, want empty (end of list)", nextCursor2)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Store roundtrip — agents (exercises ON CONFLICT and SMALLINT-as-bool)
// ─────────────────────────────────────────────────────────────────────

func TestPostgresAgentsUpsertAll(t *testing.T) {
	conn := freshDB(t)
	store := agents.NewStore(conn)
	ctx := context.Background()

	// First write — INSERT path.
	healthy := true
	now := time.Now().UTC().Truncate(time.Second)
	a := &opamp.Agent{
		InstanceUID:      "test-uid-1",
		Attributes:       map[string]string{"service.name": "mpa", "host.name": "test-host"},
		Healthy:          &healthy,
		LastStatus:       "running",
		ConnectedAt:      now,
		LastSeen:         now,
		AppliedConfigHex: "abc123",
		ConfigStatus:     "applied",
	}
	if err := store.Upsert(ctx, a); err != nil {
		t.Fatalf("Upsert (insert): %v", err)
	}

	// Second write same uid — UPDATE path via ON CONFLICT.
	notHealthy := false
	a.Healthy = &notHealthy
	a.LastStatus = "stopped"
	a.LastSeen = now.Add(time.Minute)
	if err := store.Upsert(ctx, a); err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}

	// All round-trip — preserves the three-state Healthy via SMALLINT NULL.
	all, err := store.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	got, ok := all["test-uid-1"]
	if !ok {
		t.Fatalf("uid not in All output")
	}
	if got.LastStatus != "stopped" {
		t.Errorf("LastStatus = %q, want %q", got.LastStatus, "stopped")
	}
	if got.Healthy == nil || *got.Healthy != false {
		t.Errorf("Healthy = %v, want false (after second upsert)", got.Healthy)
	}
	if got.Attributes["host.name"] != "test-host" {
		t.Errorf("Attributes round-trip lost host.name: %+v", got.Attributes)
	}

	// Three-state preservation: agent with nil Healthy.
	b := &opamp.Agent{
		InstanceUID: "test-uid-2",
		ConnectedAt: now,
		LastSeen:    now,
		// Healthy intentionally left nil (never reported)
	}
	if err := store.Upsert(ctx, b); err != nil {
		t.Fatalf("Upsert nil-healthy: %v", err)
	}
	all, _ = store.All(ctx)
	if got2, ok := all["test-uid-2"]; !ok {
		t.Errorf("uid-2 missing")
	} else if got2.Healthy != nil {
		t.Errorf("Healthy = %v, want nil (never reported)", *got2.Healthy)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Store roundtrip — audit (exercises trigger-allowed INSERT path)
// ─────────────────────────────────────────────────────────────────────

func TestPostgresAuditRecordList(t *testing.T) {
	conn := freshDB(t)
	store := audit.NewStore(conn)
	ctx := context.Background()

	// Record a few entries.
	for i := 0; i < 5; i++ {
		if err := store.Record(ctx, audit.Entry{
			Actor:   "alice",
			Action:  fmt.Sprintf("test.action.%d", i),
			Product: "ship",
			Variant: "linux",
			Detail:  fmt.Sprintf("entry %d", i),
		}); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	// List newest-first.
	entries, nextCursor, err := store.List(ctx, pagination.Params{Limit: 3})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("List returned %d, want 3", len(entries))
	}
	if entries[0].Action != "test.action.4" {
		t.Errorf("List ordering: first = %q, want test.action.4", entries[0].Action)
	}
	if nextCursor == "" {
		t.Errorf("nextCursor empty, expected more pages")
	}
}

// ─────────────────────────────────────────────────────────────────────
// Store roundtrip — labels (exercises ON CONFLICT + CURRENT_TIMESTAMP)
// ─────────────────────────────────────────────────────────────────────

func TestPostgresLabelsSetGetClear(t *testing.T) {
	conn := freshDB(t)
	store := labels.NewStore(conn)
	ctx := context.Background()

	if err := store.Set(ctx, "uid-1", "ship", "linux"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := store.Get(ctx, "uid-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Product != "ship" || got.Variant != "linux" {
		t.Errorf("Get returned %+v, want ship/linux", got)
	}

	// Set again — UPDATE path.
	if err := store.Set(ctx, "uid-1", "pay", "linux"); err != nil {
		t.Fatalf("Set (update): %v", err)
	}
	got, _, _ = store.Get(ctx, "uid-1")
	if got.Product != "pay" {
		t.Errorf("After update, Product = %q, want pay", got.Product)
	}

	// Clear.
	if err := store.Clear(ctx, "uid-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok, err := store.Get(ctx, "uid-1"); err != nil || ok {
		t.Errorf("Get after Clear: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}
