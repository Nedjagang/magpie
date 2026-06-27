// Package configs persists collector configurations.
package configs

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/magpie-project/magpie/control-plane/internal/db"
	"github.com/magpie-project/magpie/control-plane/internal/pagination"
)

const (
	DefaultProduct = "default"
	DefaultVariant = "default"
)

type Config struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Product   string    `json:"product"`
	Variant   string    `json:"variant"`
	YAML      string    `json:"yaml"`
	CreatedAt time.Time `json:"created_at"`
}

type ListFilter struct {
	Product string
	Variant string
}

type Store struct {
	conn *db.Conn
}

func NewStore(c *db.Conn) *Store {
	return &Store{conn: c}
}

// List returns configs matching the filter, newest-first, with cursor
// pagination. Pagination shape matches audit.Store.List — query LIMIT+1
// to detect a next page without a separate COUNT query, return the
// raw last-id as nextCursor for the handler to base64-wrap.
//
// Cursor semantics: "id < cursor" combined with the filter — so paging
// across product/variant filters is consistent (cursor from filter A
// returned to filter A; mixing cursors across filters is undefined and
// the schema doesn't help you).
func (s *Store) List(ctx context.Context, f ListFilter, p pagination.Params) (configs []Config, nextCursor string, err error) {
	limit := p.Limit
	if limit <= 0 {
		limit = pagination.DefaultLimit
	}
	fetchN := limit + 1

	q := `SELECT id, name, product, variant, yaml, created_at FROM configs`
	args := []any{}
	where := ""
	if f.Product != "" {
		where += ` WHERE product = ?`
		args = append(args, f.Product)
		if f.Variant != "" {
			where += ` AND variant = ?`
			args = append(args, f.Variant)
		}
	}
	if p.Cursor != "" {
		cursorID, perr := strconv.ParseInt(p.Cursor, 10, 64)
		if perr != nil {
			return nil, "", fmt.Errorf("invalid configs cursor: %w", perr)
		}
		if where == "" {
			where += ` WHERE id < ?`
		} else {
			where += ` AND id < ?`
		}
		args = append(args, cursorID)
	}

	rows, err := s.conn.Query(ctx, q+where+` ORDER BY id DESC LIMIT ?`, append(args, fetchN)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := make([]Config, 0, limit)
	for rows.Next() {
		var c Config
		if err := rows.Scan(&c.ID, &c.Name, &c.Product, &c.Variant, &c.YAML, &c.CreatedAt); err != nil {
			return nil, "", err
		}
		out = append(out, c)
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

// CountFiltered returns the count of configs matching the filter, used by
// the X-Total-Count pagination header. configs is bounded by (#products
// × #variants × #revisions) — typically hundreds — so a SELECT COUNT(*)
// here is cheap. If/when revision history grows large enough to make
// this slow, switch to an estimate or drop the header.
func (s *Store) CountFiltered(ctx context.Context, f ListFilter) (int, error) {
	q := `SELECT COUNT(*) FROM configs`
	args := []any{}
	where := ""
	if f.Product != "" {
		where += ` WHERE product = ?`
		args = append(args, f.Product)
		if f.Variant != "" {
			where += ` AND variant = ?`
			args = append(args, f.Variant)
		}
	}
	var n int
	if err := s.conn.QueryRow(ctx, q+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// LatestFor returns the most recent config for the given (product, variant) pair.
// If none exists, it falls back to (DefaultProduct, DefaultVariant). Returns
// (Config{}, false, nil) when even the default has no config.
func (s *Store) LatestFor(ctx context.Context, product, variant string) (Config, bool, error) {
	if product == "" {
		product = DefaultProduct
	}
	if variant == "" {
		variant = DefaultVariant
	}
	c, ok, err := s.latestExact(ctx, product, variant)
	if err != nil || ok {
		return c, ok, err
	}
	if product == DefaultProduct && variant == DefaultVariant {
		return Config{}, false, nil
	}
	return s.latestExact(ctx, DefaultProduct, DefaultVariant)
}

func (s *Store) latestExact(ctx context.Context, product, variant string) (Config, bool, error) {
	var c Config
	err := s.conn.QueryRow(ctx,
		`SELECT id, name, product, variant, yaml, created_at
		   FROM configs
		  WHERE product = ? AND variant = ?
		  ORDER BY id DESC LIMIT 1`,
		product, variant,
	).Scan(&c.ID, &c.Name, &c.Product, &c.Variant, &c.YAML, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	return c, true, nil
}

// Get returns the config with the given id.
func (s *Store) Get(ctx context.Context, id int64) (Config, bool, error) {
	var c Config
	err := s.conn.QueryRow(ctx,
		`SELECT id, name, product, variant, yaml, created_at FROM configs WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &c.Product, &c.Variant, &c.YAML, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	return c, true, nil
}

// DeleteProduct removes every config row for a product, along with all
// dependent rollouts and apply_state rows that reference those configs.
// Returns the number of config rows removed.
//
// Destructive: agents on this product fall back to default/default; rollout
// history for this product is also dropped (audit_log entries remain — they
// record the deletion, and audit isn't FK-constrained to configs/rollouts).
// All deletes run in a single transaction so a mid-cascade error leaves the
// schema consistent.
func (s *Store) DeleteProduct(ctx context.Context, product string) (int64, error) {
	if product == "" {
		return 0, nil
	}
	tx, err := s.conn.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Walk the FK chain leaf-first: apply_state → rollouts → configs.
	if _, err := tx.ExecContext(ctx, db.Rebind(s.conn.Dialect,`
		DELETE FROM apply_state
		WHERE rollout_id IN (
			SELECT id FROM rollouts
			WHERE config_id IN (SELECT id FROM configs WHERE product = ?)
		)`), product); err != nil {
		return 0, fmt.Errorf("delete apply_state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, db.Rebind(s.conn.Dialect,`
		DELETE FROM rollouts
		WHERE config_id       IN (SELECT id FROM configs WHERE product = ?)
		   OR prior_config_id IN (SELECT id FROM configs WHERE product = ?)`),
		product, product); err != nil {
		return 0, fmt.Errorf("delete rollouts: %w", err)
	}
	res, err := tx.ExecContext(ctx, db.Rebind(s.conn.Dialect,`DELETE FROM configs WHERE product = ?`), product)
	if err != nil {
		return 0, fmt.Errorf("delete configs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return n, nil
}

// DeleteVariant removes every config row for a (product, variant) pair,
// plus its dependent rollouts and apply_state rows. Transactional; see
// DeleteProduct for the cascade rationale.
func (s *Store) DeleteVariant(ctx context.Context, product, variant string) (int64, error) {
	if product == "" || variant == "" {
		return 0, nil
	}
	tx, err := s.conn.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, db.Rebind(s.conn.Dialect,`
		DELETE FROM apply_state
		WHERE rollout_id IN (
			SELECT id FROM rollouts
			WHERE config_id IN (SELECT id FROM configs WHERE product = ? AND variant = ?)
		)`), product, variant); err != nil {
		return 0, fmt.Errorf("delete apply_state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, db.Rebind(s.conn.Dialect,`
		DELETE FROM rollouts
		WHERE config_id       IN (SELECT id FROM configs WHERE product = ? AND variant = ?)
		   OR prior_config_id IN (SELECT id FROM configs WHERE product = ? AND variant = ?)`),
		product, variant, product, variant); err != nil {
		return 0, fmt.Errorf("delete rollouts: %w", err)
	}
	res, err := tx.ExecContext(ctx, db.Rebind(s.conn.Dialect,
		`DELETE FROM configs WHERE product = ? AND variant = ?`), product, variant)
	if err != nil {
		return 0, fmt.Errorf("delete configs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return n, nil
}

// Create inserts a new product+variant-scope config row and returns the
// resulting Config struct.
//
// Populates the v1.0 scope_kind/scope_ref columns (migration 00007)
// alongside the legacy product/variant columns. Both are written so
// v0.2-path readers and v1.0-path readers see a consistent row.
//
// Uses INSERT ... RETURNING for the round-trip — works on both Postgres
// (always) and SQLite 3.35+ (which modernc.org/sqlite ships).
func (s *Store) Create(ctx context.Context, product, variant, name, yaml string) (Config, error) {
	if product == "" {
		product = DefaultProduct
	}
	if variant == "" {
		variant = DefaultVariant
	}
	scopeRef := product + "/" + variant
	var c Config
	err := s.conn.QueryRow(ctx,
		`INSERT INTO configs (name, product, variant, yaml, scope_kind, scope_ref)
		      VALUES (?, ?, ?, ?, 'product_variant', ?)
		   RETURNING id, name, product, variant, yaml, created_at`,
		name, product, variant, yaml, scopeRef,
	).Scan(&c.ID, &c.Name, &c.Product, &c.Variant, &c.YAML, &c.CreatedAt)
	if err != nil {
		return Config{}, err
	}
	return c, nil
}

// CreateForInstance inserts a per-host (Shape 1) override config bound
// to a specific instance_uid. Used by the rollouts.Service when the
// operator publishes a Rollout with scope_kind=instance.
//
// product/variant are stored at "default" since the row isn't bound to
// any product+variant cohort — the instance scope_ref is the discriminator.
// Operators reading the row see scope_kind='instance' and scope_ref=<uid>.
func (s *Store) CreateForInstance(ctx context.Context, instanceUID, name, yaml string) (Config, error) {
	var c Config
	err := s.conn.QueryRow(ctx,
		`INSERT INTO configs (name, product, variant, yaml, scope_kind, scope_ref)
		      VALUES (?, ?, ?, ?, 'instance', ?)
		   RETURNING id, name, product, variant, yaml, created_at`,
		name, DefaultProduct, DefaultVariant, yaml, instanceUID,
	).Scan(&c.ID, &c.Name, &c.Product, &c.Variant, &c.YAML, &c.CreatedAt)
	if err != nil {
		return Config{}, err
	}
	return c, nil
}
