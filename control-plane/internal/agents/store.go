// Package agents persists the fleet-registry state the OpAMP server keeps
// in memory. Purpose: when magpied restarts, the UI's Hosts view must not
// blank out. Reconnecting agents re-populate this table on their next
// heartbeat, but between restart and first heartbeat we need the last
// known snapshot.
//
// This is a pure I/O layer — it does not hold state, does not cache, does
// not retry. The opamp.Registry remains the authoritative in-memory view;
// this package just lets it outlive restarts.
package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/opamp"
)

// Store writes and reads opamp.Agent snapshots to the `agents` table.
// Safe for concurrent use — the *db.Conn wraps *sql.DB which serialises
// writes (SQLite/WAL) or pools them (Postgres).
type Store struct {
	conn *db.Conn
}

func NewStore(c *db.Conn) *Store { return &Store{conn: c} }

// Upsert writes the agent's current state. Called from opamp.Registry on
// every AgentToServer message — so this fires at fleet-heartbeat rate.
// Kept intentionally simple (single INSERT ... ON CONFLICT); if write
// volume becomes a bottleneck the fix is batching at the registry layer,
// not here.
//
// The ON CONFLICT ... DO UPDATE syntax is portable across SQLite and
// Postgres; both accept the lowercased `excluded` reference.
func (s *Store) Upsert(ctx context.Context, a *opamp.Agent) error {
	attrsJSON, err := json.Marshal(a.Attributes)
	if err != nil {
		return fmt.Errorf("marshal attributes: %w", err)
	}
	// Represent the Go *bool as INTEGER + NULL (SQLite) / SMALLINT + NULL
	// (Postgres) so the three-state semantic (unknown / healthy /
	// unhealthy) survives a round-trip on either backend.
	var healthy any
	if a.Healthy != nil {
		if *a.Healthy {
			healthy = int64(1)
		} else {
			healthy = int64(0)
		}
	}
	_, err = s.conn.Exec(ctx, `
		INSERT INTO agents (
			instance_uid, attributes_json, healthy, last_status,
			connected_at, last_seen, applied_config_hash, config_status, config_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_uid) DO UPDATE SET
			attributes_json     = excluded.attributes_json,
			healthy             = excluded.healthy,
			last_status         = excluded.last_status,
			-- connected_at is first-seen — never overwritten on reconnect
			last_seen           = excluded.last_seen,
			applied_config_hash = excluded.applied_config_hash,
			config_status       = excluded.config_status,
			config_error        = excluded.config_error
	`,
		a.InstanceUID, string(attrsJSON), healthy, a.LastStatus,
		a.ConnectedAt, a.LastSeen, a.AppliedConfigHex, a.ConfigStatus, a.ConfigError,
	)
	if err != nil {
		return fmt.Errorf("upsert agent %s: %w", a.InstanceUID, err)
	}
	return nil
}

// Delete removes a persisted agent record by instance_uid. Idempotent:
// returns nil even when no row matches, so callers can safely delete the
// same uid twice (e.g. after a duplicate UI click). The registry's
// in-memory entry must be removed separately — see opamp.Registry.Remove.
func (s *Store) Delete(ctx context.Context, instanceUID string) error {
	if _, err := s.conn.Exec(ctx, `DELETE FROM agents WHERE instance_uid = ?`, instanceUID); err != nil {
		return fmt.Errorf("delete agent %s: %w", instanceUID, err)
	}
	return nil
}

// All returns every persisted agent keyed by instance_uid. Called once
// at magpied startup to hydrate the in-memory registry before the
// OpAMP server starts accepting connections — so the UI has content
// immediately on restart rather than waiting for heartbeats.
func (s *Store) All(ctx context.Context) (map[string]*opamp.Agent, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT instance_uid, attributes_json, healthy, last_status,
		       connected_at, last_seen, applied_config_hash,
		       config_status, config_error
		FROM agents
	`)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	out := make(map[string]*opamp.Agent)
	for rows.Next() {
		var (
			a           opamp.Agent
			attrsJSON   string
			healthy     sql.NullInt64
			connectedAt time.Time
			lastSeen    time.Time
		)
		if err := rows.Scan(
			&a.InstanceUID, &attrsJSON, &healthy, &a.LastStatus,
			&connectedAt, &lastSeen, &a.AppliedConfigHex,
			&a.ConfigStatus, &a.ConfigError,
		); err != nil {
			return nil, fmt.Errorf("scan agent row: %w", err)
		}
		a.ConnectedAt = connectedAt
		a.LastSeen = lastSeen
		if attrsJSON != "" && attrsJSON != "null" {
			// Ignore malformed attributes rather than fail warm-up —
			// we'd rather start with a partial snapshot than refuse
			// to boot. The next heartbeat from that agent will fix it.
			_ = json.Unmarshal([]byte(attrsJSON), &a.Attributes)
		}
		if healthy.Valid {
			h := healthy.Int64 != 0
			a.Healthy = &h
		}
		out[a.InstanceUID] = &a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	return out, nil
}
