// Package audit persists append-only records of control-plane changes.
//
// As of v1.0 the table carries a structured + hash-chained event format
// alongside the v0.2 free-text rows (per migration 00009). The two
// shapes coexist: legacy callers (config publish, agent relabel, etc.)
// keep using Record(); v1.0 callers (rollouts.Service, host-drawer
// override flows, etc.) use RecordEvent() which maintains the chain.
//
// Hash chain semantics (matches docs/v1.0-rollout-spec.md §11):
//   - Each v1.0 row's prev_hash matches the most recent v1.0 row's hash.
//   - Pre-v1.0 rows (hash = '') aren't part of any chain — VerifyChain
//     skips them.
//   - The hash is computed by the application layer over a canonical
//     pipe-separated serialization of the row's content fields. Keeping
//     the canonicalisation in app code (not a DB function) lets us
//     evolve it without a schema migration.
//   - Tamper-evidence: editing any field in a chain row breaks all
//     downstream prev_hash matches. Combined with the BEFORE UPDATE /
//     DELETE triggers from migration 00006, mutating the table requires
//     write access AND re-computing every downstream hash. For stronger
//     evidence the deployment ships audit rows off-host (planned, v1.x).
package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/pagination"
)

// EventType is the structured-event vocabulary from spec §11.
// Stored as TEXT so v1.x can extend without a schema migration.
type EventType string

// Rollout life-cycle event types.
const (
	EventRolloutCreated      EventType = "RolloutCreated"
	EventRolloutInstant      EventType = "RolloutInstant"
	EventRolloutAdvanced     EventType = "RolloutAdvanced"
	EventRolloutPaused       EventType = "RolloutPaused"
	EventRolloutAborted      EventType = "RolloutAborted"
	EventRolloutPromoted     EventType = "RolloutPromoted"
	EventRolloutFastPromoted EventType = "RolloutFastPromoted"
)

// Override life-cycle event types (Shape 1 + label override).
const (
	EventOverrideApplied EventType = "OverrideApplied"
	EventOverrideCleared EventType = "OverrideCleared"
)

// Host life-cycle event types.
const (
	EventLabelChanged EventType = "LabelChanged"
	EventLabelCleared EventType = "LabelCleared"
	EventHostDeleted  EventType = "HostDeleted"
)

// Config / catalog life-cycle event types. Distinct from rollout events
// because they describe direct portal actions on the configs catalog
// (create a new revision, rollback to an older one) and the cohort
// structure (delete a whole product, delete a single variant). v1.0
// always pairs these with a structured payload so post-incident review
// can reconstruct what changed.
const (
	EventConfigCreated   EventType = "ConfigCreated"
	EventConfigRollback  EventType = "ConfigRollback"
	EventConfigRepushed  EventType = "ConfigRepushed"
	EventProductDeleted  EventType = "ProductDeleted"
	EventVariantDeleted  EventType = "VariantDeleted"
)

// Entry is the persisted shape — carries both v0.2 free-text fields
// (Action, Product, Variant, TargetID, Detail) and v1.0 structured
// fields (Type, ScopeKind, ScopeRef, ConfigRef, HostRef, PayloadJSON,
// PrevHash, Hash). The omitempty tags mean v0.2 readers see the same
// JSON shape they always did; v1.0 readers see the new fields populated
// for v1.0 events and empty for legacy events.
type Entry struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Actor    string    `json:"actor"`
	Action   string    `json:"action"`
	Product  string    `json:"product,omitempty"`
	Variant  string    `json:"variant,omitempty"`
	TargetID *int64    `json:"target_id,omitempty"`
	Detail   string    `json:"detail,omitempty"`

	// v1.0 structured fields. Empty / zero on legacy rows.
	Type        string `json:"type,omitempty"`
	ScopeKind   string `json:"scope_kind,omitempty"`
	ScopeRef    string `json:"scope_ref,omitempty"`
	ConfigRef   *int64 `json:"config_ref,omitempty"`
	HostRef     string `json:"host_ref,omitempty"`
	PayloadJSON string `json:"payload_json,omitempty"`

	// Hash chain. Empty on legacy rows.
	PrevHash string `json:"prev_hash,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

// Event is the structured input to RecordEvent — a v1.0-shaped audit
// emission with explicit type + entity refs + JSON payload. Callers
// build one of these instead of building free-text Detail strings.
type Event struct {
	Actor       string
	Type        EventType
	ScopeKind   string // "product_variant" | "instance" | ""
	ScopeRef    string
	ConfigRef   *int64
	HostRef     string
	PayloadJSON string // raw JSON; if empty, "{}" is stored

	// Convenience denormalised fields — populated into the v0.2-shape
	// columns so legacy readers (the v0.2 UI's audit list) still see
	// useful context. For RolloutCreated on ship/linux the natural
	// values are Product=ship, Variant=linux; for OverrideApplied on
	// a host they're empty. Set what makes sense; defaults to empty.
	Product  string
	Variant  string
	TargetID *int64
}

type Store struct {
	conn *db.Conn

	// chainMu serializes RecordEvent so concurrent writers don't read
	// the same prev_hash and produce a forked chain. Audit writes are
	// not high-throughput; the lock is cheap.
	chainMu sync.Mutex
}

func NewStore(c *db.Conn) *Store { return &Store{conn: c} }

// Record writes a legacy (v0.2-shape) audit row. Used by callers that
// haven't been migrated to RecordEvent — config publish, agent relabel,
// product/variant delete. The new v1.0 columns default to empty / NULL
// and the row doesn't participate in the hash chain.
func (s *Store) Record(ctx context.Context, e Entry) error {
	var target any
	if e.TargetID != nil {
		target = *e.TargetID
	}
	_, err := s.conn.Exec(ctx,
		`INSERT INTO audit_log (actor, action, product, variant, target_id, detail)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.Actor, e.Action, e.Product, e.Variant, target, e.Detail,
	)
	return err
}

// RecordEvent writes a v1.0-shape structured audit row, populating the
// hash chain. The chain links via prev_hash → most recent v1.0 row's
// hash; chain validation walks rows where hash != ''.
//
// Serialization: chainMu ensures only one RecordEvent runs at a time
// for the lifetime of the Store, so concurrent goroutines don't race
// on prev_hash lookup. SQLite/WAL serialises writes anyway; the mutex
// guarantees the read-prev_hash → compute → insert sequence is atomic
// in a way that doesn't depend on backend-specific isolation levels.
func (s *Store) RecordEvent(ctx context.Context, e Event) error {
	s.chainMu.Lock()
	defer s.chainMu.Unlock()

	// Most-recent v1.0 row's hash, or "" for the first chain entry.
	var prevHash string
	row := s.conn.QueryRow(ctx,
		`SELECT hash FROM audit_log
		 WHERE hash != '' ORDER BY id DESC LIMIT 1`)
	if err := row.Scan(&prevHash); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read prev_hash: %w", err)
		}
		prevHash = ""
	}

	now := time.Now().UTC()
	payload := e.PayloadJSON
	if payload == "" {
		payload = "{}"
	}
	hash := computeChainHash(prevHash, e, now, payload)

	var configRef any
	if e.ConfigRef != nil {
		configRef = *e.ConfigRef
	}
	var targetID any
	if e.TargetID != nil {
		targetID = *e.TargetID
	}

	// `action` mirrors `type` for v0.2 readers; the v0.2 UI reads action
	// to render the row's verb. New v1.0 readers prefer `type`.
	_, err := s.conn.Exec(ctx, `
		INSERT INTO audit_log (
			at, actor, action,
			product, variant, target_id, detail,
			prev_hash, hash, type, scope_kind, scope_ref, config_ref, host_ref, payload_json
		) VALUES (
			?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?
		)`,
		now, e.Actor, string(e.Type),
		e.Product, e.Variant, targetID, "",
		prevHash, hash, string(e.Type), e.ScopeKind, e.ScopeRef, configRef, e.HostRef, payload,
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// computeChainHash is the canonical serialization the chain hashes over.
// Pipe-separated form is unambiguous because none of the field values
// can themselves contain a literal pipe — actor / scope refs / instance
// uids are alphanumeric, type names are constants, payload is JSON
// (where pipes don't appear at the top level). The format is documented
// here so it stays stable across releases; field order MUST NOT change
// without a chain-rebuild pass on existing rows.
//
// Fields hashed (in order): prev_hash | type | actor | scope_kind |
// scope_ref | config_ref | host_ref | payload_json | at(RFC3339Nano UTC).
//
// Note: id is NOT in the hash. Schema-imposed AUTOINCREMENT means id
// follows insertion order anyway, and including id would force an
// insert-then-update flow (forbidden by the BEFORE UPDATE trigger from
// migration 00006).
func computeChainHash(prev string, e Event, at time.Time, payload string) string {
	var sb strings.Builder
	sb.Grow(64 + len(prev) + len(payload))
	sb.WriteString(prev)
	sb.WriteByte('|')
	sb.WriteString(string(e.Type))
	sb.WriteByte('|')
	sb.WriteString(e.Actor)
	sb.WriteByte('|')
	sb.WriteString(e.ScopeKind)
	sb.WriteByte('|')
	sb.WriteString(e.ScopeRef)
	sb.WriteByte('|')
	if e.ConfigRef != nil {
		sb.WriteString(strconv.FormatInt(*e.ConfigRef, 10))
	}
	sb.WriteByte('|')
	sb.WriteString(e.HostRef)
	sb.WriteByte('|')
	sb.WriteString(payload)
	sb.WriteByte('|')
	sb.WriteString(at.UTC().Format(time.RFC3339Nano))

	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// VerifyChain walks every v1.0 chain row in id order and confirms each
// row's prev_hash matches the previous v1.0 row's hash AND each row's
// hash matches the canonical re-computation.
//
// Returns:
//   - validUpToID: id of the last verified row (0 if nothing verified)
//   - brokenAt: id of the first row that fails verification (0 if chain
//     is intact through the whole table)
//
// Operators / ops tooling can call this to detect tampering; a non-zero
// brokenAt with validUpToID > 0 means rows up to validUpToID are
// unmodified, and the row at brokenAt was edited (or one of its content
// fields changed without a corresponding hash update — the BEFORE UPDATE
// trigger should make this impossible at the engine level, but if
// someone bypasses the trigger via a new connection or a DBA tool, the
// verification catches it).
func (s *Store) VerifyChain(ctx context.Context) (validUpToID, brokenAt int64, err error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, at, actor, type, scope_kind, scope_ref,
		       config_ref, host_ref, payload_json,
		       prev_hash, hash
		FROM audit_log
		WHERE hash != ''
		ORDER BY id`)
	if err != nil {
		return 0, 0, fmt.Errorf("verify: query: %w", err)
	}
	defer rows.Close()

	expectedPrev := ""
	for rows.Next() {
		var (
			id          int64
			at          time.Time
			actor       string
			typeStr     string
			scopeKind   string
			scopeRef    string
			configRef   sql.NullInt64
			hostRef     string
			payloadJSON string
			prevHash    string
			hash        string
		)
		if err := rows.Scan(&id, &at, &actor, &typeStr, &scopeKind, &scopeRef,
			&configRef, &hostRef, &payloadJSON, &prevHash, &hash); err != nil {
			return validUpToID, 0, fmt.Errorf("verify: scan: %w", err)
		}

		if prevHash != expectedPrev {
			return validUpToID, id, nil
		}

		var configRefPtr *int64
		if configRef.Valid {
			v := configRef.Int64
			configRefPtr = &v
		}
		recomputed := computeChainHash(prevHash, Event{
			Actor:     actor,
			Type:      EventType(typeStr),
			ScopeKind: scopeKind,
			ScopeRef:  scopeRef,
			ConfigRef: configRefPtr,
			HostRef:   hostRef,
		}, at, payloadJSON)
		if recomputed != hash {
			return validUpToID, id, nil
		}

		validUpToID = id
		expectedPrev = hash
	}
	if err := rows.Err(); err != nil {
		return validUpToID, 0, err
	}
	return validUpToID, 0, nil
}

// ListFilter narrows the result set returned by List. Empty fields are
// ignored. Combined with the cursor in pagination.Params for newest-first
// drill-down via Load more.
//
// All filters use the indexed columns from migration 00009 (idx_audit_host,
// idx_audit_scope_v1, idx_audit_type) so the queries stay bounded even
// at millions of rows. Multi-criteria queries combine via AND.
//
// Text filters (ScopeRef, Actor) match case-insensitively as substrings —
// operators searching "Shipper" find "Shipper/windows", "praneeth" finds
// "authenticated:Praneeth". HostRef stays exact-match because instance_uids
// are random and not human-typed; the UI cross-link from host drawers
// passes them verbatim.
type ListFilter struct {
	HostRef    string    // exact-match against host_ref column
	ScopeKind  string    // "product_variant" | "instance" — pairs with ScopeRef
	ScopeRef   string    // case-insensitive substring match against scope_ref column
	Type       string    // exact-match against type column (e.g. "RolloutCreated")
	Actor      string    // case-insensitive substring match against actor column
	Since      time.Time // events with at >= Since are returned (zero value disables)
	HideLegacy bool      // when true, exclude pre-v1.0 rows (hash = '')
}

// List returns audit entries newest-first with cursor pagination + optional
// filter narrowing.
//
// Pagination model (matches docs/v1.0-plan.md §6.1 #2): query LIMIT+1 rows
// and trim to LIMIT for the caller; the extra row is the "is there a next
// page" sentinel without requiring a separate COUNT query. nextCursor is
// the raw last-returned id; the handler base64-wraps it into X-Next-Cursor.
//
// cursor of "" returns the newest page. A cursor from a previous response
// returns the page immediately older.
func (s *Store) List(ctx context.Context, filter ListFilter, p pagination.Params) (entries []Entry, nextCursor string, err error) {
	limit := p.Limit
	if limit <= 0 {
		limit = pagination.DefaultLimit
	}
	fetchN := limit + 1

	const selectCols = `id, at, actor, action, product, variant, target_id, detail,
	                    type, scope_kind, scope_ref, config_ref, host_ref, payload_json,
	                    prev_hash, hash`

	// Build WHERE dynamically. Each filter field appends a clause + arg.
	// The cursor and the filter fields all join with AND, so the order
	// in which we append doesn't matter for correctness.
	where := []string{}
	args := []any{}
	if filter.HostRef != "" {
		where = append(where, "host_ref = ?")
		args = append(args, filter.HostRef)
	}
	if filter.ScopeKind != "" {
		where = append(where, "scope_kind = ?")
		args = append(args, filter.ScopeKind)
	}
	if filter.ScopeRef != "" {
		// LOWER on both sides works on SQLite + Postgres; pattern wraps with
		// % so any substring match wins. Index can't help for leading-% LIKE
		// but at v1.0 audit volumes (thousands of rows) a full scan is fine.
		where = append(where, "LOWER(scope_ref) LIKE LOWER(?)")
		args = append(args, "%"+filter.ScopeRef+"%")
	}
	if filter.Type != "" {
		where = append(where, "type = ?")
		args = append(args, filter.Type)
	}
	if filter.Actor != "" {
		where = append(where, "LOWER(actor) LIKE LOWER(?)")
		args = append(args, "%"+filter.Actor+"%")
	}
	if !filter.Since.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, filter.Since.UTC())
	}
	if filter.HideLegacy {
		where = append(where, "hash != ''")
	}
	if p.Cursor != "" {
		cursorID, perr := strconv.ParseInt(p.Cursor, 10, 64)
		if perr != nil {
			return nil, "", fmt.Errorf("invalid audit cursor: %w", perr)
		}
		where = append(where, "id < ?")
		args = append(args, cursorID)
	}

	q := `SELECT ` + selectCols + ` FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, fetchN)

	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := make([]Entry, 0, limit)
	for rows.Next() {
		var (
			e         Entry
			target    sql.NullInt64
			configRef sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Product, &e.Variant, &target, &e.Detail,
			&e.Type, &e.ScopeKind, &e.ScopeRef, &configRef, &e.HostRef, &e.PayloadJSON,
			&e.PrevHash, &e.Hash); err != nil {
			return nil, "", err
		}
		if target.Valid {
			v := target.Int64
			e.TargetID = &v
		}
		if configRef.Valid {
			v := configRef.Int64
			e.ConfigRef = &v
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	if len(out) > limit {
		out = out[:limit]
		nextCursor = strconv.FormatInt(out[len(out)-1].ID, 10)
	}
	return out, nextCursor, nil
}
