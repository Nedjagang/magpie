// Package labels persists server-side overrides of the (product, variant)
// labels an agent self-reports. Purpose: re-assign a host's cohort from the UI
// without SSHing to change its env vars. Resolution order:
//  1. override row if present in agent_labels
//  2. otherwise whatever the agent advertised in AgentDescription
package labels

import (
	"context"
	"database/sql"

	"github.com/magpie-project/magpie/control-plane/internal/db"
)

type Override struct {
	InstanceUID string `json:"instance_uid"`
	Product     string `json:"product"`
	Variant     string `json:"variant"`
}

type Store struct{ conn *db.Conn }

func NewStore(c *db.Conn) *Store { return &Store{conn: c} }

// Set upserts an override for the given agent.
func (s *Store) Set(ctx context.Context, uid, product, variant string) error {
	_, err := s.conn.Exec(ctx,
		`INSERT INTO agent_labels (instance_uid, product, variant, updated_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(instance_uid) DO UPDATE SET
		   product = excluded.product,
		   variant = excluded.variant,
		   updated_at = CURRENT_TIMESTAMP`,
		uid, product, variant,
	)
	return err
}

// Clear removes any override for the agent, reverting to advertised labels.
func (s *Store) Clear(ctx context.Context, uid string) error {
	_, err := s.conn.Exec(ctx, `DELETE FROM agent_labels WHERE instance_uid = ?`, uid)
	return err
}

// Get returns the override for an agent, if any.
func (s *Store) Get(ctx context.Context, uid string) (Override, bool, error) {
	var o Override
	err := s.conn.QueryRow(ctx,
		`SELECT instance_uid, product, variant FROM agent_labels WHERE instance_uid = ?`, uid,
	).Scan(&o.InstanceUID, &o.Product, &o.Variant)
	if err == sql.ErrNoRows {
		return Override{}, false, nil
	}
	if err != nil {
		return Override{}, false, err
	}
	return o, true, nil
}

// All returns every override in the table; used to warm a cache on startup
// or when evaluating labels in bulk.
func (s *Store) All(ctx context.Context) (map[string]Override, error) {
	rows, err := s.conn.Query(ctx,
		`SELECT instance_uid, product, variant FROM agent_labels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]Override)
	for rows.Next() {
		var o Override
		if err := rows.Scan(&o.InstanceUID, &o.Product, &o.Variant); err != nil {
			return nil, err
		}
		out[o.InstanceUID] = o
	}
	return out, rows.Err()
}
