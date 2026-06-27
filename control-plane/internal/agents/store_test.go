package agents_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/magpie-project/magpie/control-plane/internal/agents"
	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/opamp"
)

// openTestDB opens an isolated SQLite DB with migrations applied. SQLite
// stores DATETIME as ISO strings at second resolution (via modernc.org's
// converter), so tests truncate timestamps to whole seconds before compare.
func openTestDB(t *testing.T) *agents.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agents.NewStore(conn)
}

func TestStoreRoundTripOneAgent(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	healthy := true
	now := time.Now().UTC().Truncate(time.Second)
	want := &opamp.Agent{
		InstanceUID:      "abc123",
		Attributes:       map[string]string{"service.name": "mpa", "magpie.product": "demo"},
		Healthy:          &healthy,
		LastStatus:       "running",
		ConnectedAt:      now.Add(-1 * time.Hour),
		LastSeen:         now,
		AppliedConfigHex: "deadbeef",
		ConfigStatus:     "APPLIED",
		ConfigError:      "",
	}
	if err := store.Upsert(ctx, want); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	loaded, err := store.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	got, ok := loaded["abc123"]
	if !ok {
		t.Fatalf("agent abc123 missing from All; got keys %v", keysOf(loaded))
	}

	if got.InstanceUID != want.InstanceUID {
		t.Errorf("InstanceUID = %q, want %q", got.InstanceUID, want.InstanceUID)
	}
	if !reflect.DeepEqual(got.Attributes, want.Attributes) {
		t.Errorf("Attributes = %v, want %v", got.Attributes, want.Attributes)
	}
	if got.Healthy == nil || *got.Healthy != *want.Healthy {
		t.Errorf("Healthy = %v, want %v", got.Healthy, want.Healthy)
	}
	if got.LastStatus != want.LastStatus {
		t.Errorf("LastStatus = %q, want %q", got.LastStatus, want.LastStatus)
	}
	if !got.ConnectedAt.Equal(want.ConnectedAt) {
		t.Errorf("ConnectedAt = %v, want %v", got.ConnectedAt, want.ConnectedAt)
	}
	if !got.LastSeen.Equal(want.LastSeen) {
		t.Errorf("LastSeen = %v, want %v", got.LastSeen, want.LastSeen)
	}
	if got.AppliedConfigHex != want.AppliedConfigHex {
		t.Errorf("AppliedConfigHex = %q, want %q", got.AppliedConfigHex, want.AppliedConfigHex)
	}
	if got.ConfigStatus != want.ConfigStatus {
		t.Errorf("ConfigStatus = %q, want %q", got.ConfigStatus, want.ConfigStatus)
	}
}

// TestStoreHealthyTriState proves NULL survives the round-trip — important
// because "never reported health" is semantically distinct from "reported
// unhealthy", and the UI relies on that to show "starting" vs "red".
func TestStoreHealthyTriState(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// No health ever reported → Healthy should come back nil.
	unknown := &opamp.Agent{InstanceUID: "u1", ConnectedAt: now, LastSeen: now}
	// Reported unhealthy explicitly.
	h := false
	unhealthy := &opamp.Agent{InstanceUID: "u2", ConnectedAt: now, LastSeen: now, Healthy: &h}

	if err := store.Upsert(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, unhealthy); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded["u1"].Healthy != nil {
		t.Errorf("u1 Healthy = %v, want nil", loaded["u1"].Healthy)
	}
	if loaded["u2"].Healthy == nil || *loaded["u2"].Healthy {
		t.Errorf("u2 Healthy = %v, want false pointer", loaded["u2"].Healthy)
	}
}

// TestStoreUpsertPreservesConnectedAt guards the ON CONFLICT clause: when
// an agent reconnects (second Upsert), last_seen advances but connected_at
// must remain the original first-seen timestamp.
func TestStoreUpsertPreservesConnectedAt(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	t0 := time.Now().UTC().Truncate(time.Second).Add(-1 * time.Hour)
	t1 := t0.Add(30 * time.Minute)

	first := &opamp.Agent{InstanceUID: "u", ConnectedAt: t0, LastSeen: t0}
	if err := store.Upsert(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Simulate reconnect: caller passes a NEW ConnectedAt, but the store
	// must ignore it for the existing row.
	second := &opamp.Agent{InstanceUID: "u", ConnectedAt: t1, LastSeen: t1}
	if err := store.Upsert(ctx, second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded["u"]
	if !got.ConnectedAt.Equal(t0) {
		t.Errorf("ConnectedAt after reconnect = %v, want first-seen %v", got.ConnectedAt, t0)
	}
	if !got.LastSeen.Equal(t1) {
		t.Errorf("LastSeen after reconnect = %v, want %v", got.LastSeen, t1)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
