package audit_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/magpie-project/magpie/control-plane/internal/audit"
	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/pagination"
)

func newAuditStore(t *testing.T) (*audit.Store, *db.Conn) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return audit.NewStore(conn), conn
}

// TestRecordEventBuildsChain confirms that successive RecordEvent calls
// produce rows whose prev_hash matches the prior row's hash, and that
// VerifyChain reports the chain valid.
func TestRecordEventBuildsChain(t *testing.T) {
	store, _ := newAuditStore(t)
	ctx := context.Background()

	for i := range 5 {
		if err := store.RecordEvent(ctx, audit.Event{
			Actor:       "alice",
			Type:        audit.EventRolloutCreated,
			ScopeKind:   "product_variant",
			ScopeRef:    "ship/linux",
			PayloadJSON: `{"step": ` + itoa(i) + `}`,
		}); err != nil {
			t.Fatalf("RecordEvent %d: %v", i, err)
		}
	}

	validUpTo, brokenAt, err := store.VerifyChain(ctx)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if brokenAt != 0 {
		t.Errorf("brokenAt = %d, want 0 (chain should be intact)", brokenAt)
	}
	if validUpTo == 0 {
		t.Errorf("validUpTo = 0, want non-zero (5 events written)")
	}

	// Sanity: List should return all 5 with hash + prev_hash populated.
	entries, _, err := store.List(ctx, audit.ListFilter{}, pagination.Params{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("List returned %d entries, want 5", len(entries))
	}
	for _, e := range entries {
		if e.Hash == "" {
			t.Errorf("entry id=%d has empty hash", e.ID)
		}
	}
}

// TestVerifyChainDetectsTampering confirms that the chain validator
// catches an out-of-band UPDATE to a chain row. The audit_log triggers
// from migration 00006 prevent UPDATE/DELETE at the SQL layer, so to
// simulate tampering we DROP the triggers, mutate, then VerifyChain.
func TestVerifyChainDetectsTampering(t *testing.T) {
	store, conn := newAuditStore(t)
	ctx := context.Background()

	for i := range 3 {
		if err := store.RecordEvent(ctx, audit.Event{
			Actor:       "alice",
			Type:        audit.EventRolloutCreated,
			ScopeKind:   "product_variant",
			ScopeRef:    "ship/linux",
			PayloadJSON: `{"step": ` + itoa(i) + `}`,
		}); err != nil {
			t.Fatalf("RecordEvent %d: %v", i, err)
		}
	}

	// Drop the BEFORE UPDATE/DELETE triggers so we can simulate a DBA
	// bypassing append-only semantics. (In the real world a tamper of
	// this kind requires DB write access AND knowledge of the trigger
	// names — but the chain catches it regardless.)
	for _, drop := range []string{
		`DROP TRIGGER IF EXISTS audit_log_no_update`,
		`DROP TRIGGER IF EXISTS audit_log_no_delete`,
	} {
		if _, err := conn.DB.Exec(drop); err != nil {
			t.Fatalf("drop trigger: %v", err)
		}
	}

	// Tamper with row 2's actor — break the canonical content without
	// re-computing the hash.
	if _, err := conn.DB.Exec(`UPDATE audit_log SET actor = 'mallory' WHERE id = 2`); err != nil {
		t.Fatalf("tamper update: %v", err)
	}

	validUpTo, brokenAt, err := store.VerifyChain(ctx)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if brokenAt != 2 {
		t.Errorf("brokenAt = %d, want 2 (the tampered row)", brokenAt)
	}
	if validUpTo != 1 {
		t.Errorf("validUpTo = %d, want 1 (rows up to but not including the tamper)", validUpTo)
	}
}

// TestRecordCoexistsWithRecordEvent: legacy Record() rows have empty
// hash and aren't part of the chain. VerifyChain should still pass
// when chain rows are mixed with legacy rows.
func TestRecordCoexistsWithRecordEvent(t *testing.T) {
	store, _ := newAuditStore(t)
	ctx := context.Background()

	// One v0.2 legacy row.
	if err := store.Record(ctx, audit.Entry{
		Actor:  "alice",
		Action: "config.create",
		Detail: "legacy",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Two v1.0 chain rows.
	for i := range 2 {
		if err := store.RecordEvent(ctx, audit.Event{
			Actor:       "alice",
			Type:        audit.EventRolloutCreated,
			ScopeKind:   "product_variant",
			ScopeRef:    "ship/linux",
			PayloadJSON: `{"step": ` + itoa(i) + `}`,
		}); err != nil {
			t.Fatalf("RecordEvent %d: %v", i, err)
		}
	}

	// Another v0.2 row, then another chain row.
	if err := store.Record(ctx, audit.Entry{
		Actor:  "alice",
		Action: "agent.relabel",
		Detail: "legacy 2",
	}); err != nil {
		t.Fatalf("Record 2: %v", err)
	}
	if err := store.RecordEvent(ctx, audit.Event{
		Actor:       "alice",
		Type:        audit.EventRolloutAborted,
		ScopeKind:   "product_variant",
		ScopeRef:    "ship/linux",
		PayloadJSON: `{}`,
	}); err != nil {
		t.Fatalf("RecordEvent 3: %v", err)
	}

	_, brokenAt, err := store.VerifyChain(ctx)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if brokenAt != 0 {
		t.Errorf("brokenAt = %d, want 0 (chain rows interleaved with legacy rows should still verify)", brokenAt)
	}
}

// itoa avoids a strconv import for the small constants in this file.
func itoa(n int) string {
	if n < 0 || n > 9 {
		return "?"
	}
	return string(rune('0' + n))
}
