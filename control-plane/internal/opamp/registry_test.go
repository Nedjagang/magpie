package opamp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePersister captures Upsert calls and serves canned All() data. Keeps
// the registry test self-contained — no sqlite, no migrations, no real DB.
type fakePersister struct {
	upserted map[string]*Agent
	all      map[string]*Agent
	allErr   error
}

func (f *fakePersister) Upsert(_ context.Context, a *Agent) error {
	if f.upserted == nil {
		f.upserted = map[string]*Agent{}
	}
	// Copy so later mutations by the registry don't alias our map.
	cp := *a
	f.upserted[a.InstanceUID] = &cp
	return nil
}

func (f *fakePersister) All(_ context.Context) (map[string]*Agent, error) {
	return f.all, f.allErr
}

// TestRegistryWarmPopulatesFromPersister is the regression test for the
// fleet-disappears-on-restart bug. We simulate the restart by creating a
// fresh Registry with a persister that returns pre-existing agents; after
// Warm, the registry must expose them via List() / Get() exactly as if
// they had connected live.
func TestRegistryWarmPopulatesFromPersister(t *testing.T) {
	h := true
	prior := map[string]*Agent{
		"abc": {
			InstanceUID: "abc",
			Attributes:  map[string]string{"magpie.product": "demo", "magpie.variant": "linux"},
			Healthy:     &h,
			LastStatus:  "running",
			ConnectedAt: time.Now().Add(-1 * time.Hour),
			LastSeen:    time.Now(),
		},
	}
	persister := &fakePersister{all: prior}

	r := NewRegistry()
	r.SetPersister(persister, nil)
	if err := r.Warm(context.Background()); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1 — warm didn't hydrate", len(list))
	}
	if list[0].InstanceUID != "abc" {
		t.Errorf("list[0].InstanceUID = %q, want abc", list[0].InstanceUID)
	}
	// Effective labels must resolve from advertised attributes.
	if list[0].EffectiveProduct != "demo" || list[0].EffectiveVariant != "linux" {
		t.Errorf("effective labels = (%s, %s), want (demo, linux)",
			list[0].EffectiveProduct, list[0].EffectiveVariant)
	}

	got, ok := r.Get("abc")
	if !ok {
		t.Fatal("Get(abc) = not found after warm")
	}
	if got.LastStatus != "running" {
		t.Errorf("Get(abc).LastStatus = %q, want running", got.LastStatus)
	}
}

// TestRegistryWarmDoesNotClobberLiveAgents: if an agent reconnects between
// SetPersister and Warm (possible in practice — server starts accepting
// before warm completes), the live state must win. The Warm contract is
// "hydrate missing entries", not "replace the world".
func TestRegistryWarmDoesNotClobberLiveAgents(t *testing.T) {
	liveStatus := "live-after-reconnect"
	r := NewRegistry()
	r.mu.Lock()
	r.agents["live"] = &Agent{InstanceUID: "live", LastStatus: liveStatus}
	r.mu.Unlock()

	r.SetPersister(&fakePersister{all: map[string]*Agent{
		"live": {InstanceUID: "live", LastStatus: "stale-from-db"},
	}}, nil)

	if err := r.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get("live")
	if got.LastStatus != liveStatus {
		t.Errorf("LastStatus = %q, want %q (live state must beat DB snapshot)", got.LastStatus, liveStatus)
	}
}

// TestRegistryWarmNoPersister: Warm must no-op when no persister is
// installed. This is the default for tests that don't care about
// persistence.
func TestRegistryWarmNoPersister(t *testing.T) {
	r := NewRegistry()
	if err := r.Warm(context.Background()); err != nil {
		t.Errorf("Warm with no persister: %v", err)
	}
	if len(r.List()) != 0 {
		t.Errorf("List len = %d, want 0", len(r.List()))
	}
}

// TestRegistryWarmPropagatesError: a real DB error (disk full, corrupt
// file) must reach the caller so main.go can log it clearly — we don't
// want silent failure that shows an empty UI without saying why.
func TestRegistryWarmPropagatesError(t *testing.T) {
	sentinel := errors.New("disk exploded")
	r := NewRegistry()
	r.SetPersister(&fakePersister{allErr: sentinel}, nil)
	err := r.Warm(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("Warm err = %v, want wrapping disk exploded", err)
	}
}
