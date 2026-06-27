// Package db opens the control plane database and applies embedded migrations.
//
// Backend selection (ADR 0007 + docs/v1.0-plan.md §6.1 #1):
//   - SQLite (default) — single-file embedded store via modernc.org/sqlite
//     (pure Go, no CGO). Suitable up to ~500 hosts.
//   - Postgres (opt-in via MAGPIE_DATABASE_URL=postgres://...) — required
//     for HA + 2000-host scale targeted by v1.0. Driver: jackc/pgx/v5/stdlib.
//
// Migrations are dialect-specific: each backend has its own embed.FS and
// numbered migration set under migrations/<dialect>/. The numbering matches
// across dialects (00001-00009) so goose's version tracking is consistent
// regardless of which backend an operator chose.
//
// Stores ride the placeholder-syntax gap via *Conn — a thin wrapper over
// *sql.DB that knows its dialect and auto-Rebinds query strings (`?` ↔
// `$1, $2, …`) on the way through Exec/Query/QueryRow. Stores never see
// the raw *sql.DB unless they specifically need it (exposed via Conn.DB).
//
// Deliberate trade-offs in the cross-dialect schema:
//   - attributes_json stays TEXT (not JSONB) on Postgres so the wire
//     shape matches SQLite. We never query inside the JSON.
//   - healthy stays SMALLINT (not BOOLEAN) on Postgres so the three-state
//     semantic (sql.NullInt64 with 0/1/NULL) is identical to the SQLite
//     code path; no Go-side type changes per dialect.
//   - is_canary same: SMALLINT, identical behavior.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver name "pgx"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // database/sql driver name "sqlite"
)

// Dialect names a SQL backend.
type Dialect string

const (
	// DialectSQLite is the default backend for single-node, smaller-fleet
	// deployments. Backed by modernc.org/sqlite (pure Go).
	DialectSQLite Dialect = "sqlite"

	// DialectPostgres is the opt-in backend for HA / 2000-host scale.
	// Selected by MAGPIE_DATABASE_URL=postgres://... ; driver is pgx/v5
	// via its database/sql adapter.
	DialectPostgres Dialect = "postgres"
)

//go:embed migrations/sqlite/*.sql
var sqliteMigrationsFS embed.FS

//go:embed migrations/postgres/*.sql
var postgresMigrationsFS embed.FS

// Conn wraps *sql.DB with the dialect chosen at Open time and provides
// auto-Rebinding Exec/Query/QueryRow methods. Stores take *Conn rather
// than *sql.DB so query strings can stay portable (`?` placeholders),
// with Postgres's `$1, $2, …` syntax injected at the boundary.
//
// DB is exposed for cases that genuinely need raw *sql.DB access — e.g.
// transactions across multiple statements where the caller wants to
// manage Rebind itself. Most stores never touch DB directly.
type Conn struct {
	DB      *sql.DB
	Dialect Dialect
}

// Close releases the underlying *sql.DB.
func (c *Conn) Close() error { return c.DB.Close() }

// Exec runs a query whose placeholders are written `?`. On Postgres they
// are translated to `$1, $2, …` before being sent to the driver.
func (c *Conn) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.DB.ExecContext(ctx, Rebind(c.Dialect, query), args...)
}

// Query is the sibling of Exec for read paths that return multiple rows.
func (c *Conn) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.DB.QueryContext(ctx, Rebind(c.Dialect, query), args...)
}

// QueryRow is the sibling of Exec for read paths that return at most one
// row. Combine with `INSERT ... RETURNING id` to retrieve a generated id
// portably across both dialects (PG never supported LastInsertId; SQLite
// 3.35+ supports RETURNING, which modernc.org/sqlite ships with).
func (c *Conn) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return c.DB.QueryRowContext(ctx, Rebind(c.Dialect, query), args...)
}

// Open detects the dialect from dsn and opens the connection.
//
// dsn forms supported:
//
//   - "postgres://..." or "postgresql://..."
//     → DialectPostgres. Opened via pgx/v5/stdlib.
//
//   - everything else (including ":memory:" and bare file paths)
//     → DialectSQLite. Wrapped in the v0.2 pragma string for WAL +
//     foreign_keys + busy_timeout + secure_delete. The "sqlite:" /
//     "file:" prefixes are accepted and stripped, so "sqlite:magpie.db"
//     and "magpie.db" both resolve to the same backing file.
//
// The returned *Conn carries the dialect alongside the underlying
// *sql.DB so stores and call sites can adapt without re-detecting from
// the DSN (which may carry credentials we don't want to keep around).
func Open(dsn string) (*Conn, error) {
	dialect := detectDialect(dsn)

	switch dialect {
	case DialectPostgres:
		return openPostgres(dsn)
	case DialectSQLite:
		return openSQLite(dsn)
	default:
		return nil, fmt.Errorf("unsupported database dialect: %q", dialect)
	}
}

// detectDialect picks a Dialect from the dsn prefix. Anything that doesn't
// look like a postgres URL is treated as a sqlite file path, matching the
// v0.2 ergonomics where MAGPIE_DB_PATH was a bare filesystem path. Case-
// insensitive on the prefix because URL schemes conventionally are.
func detectDialect(dsn string) Dialect {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return DialectPostgres
	}
	return DialectSQLite
}

func openSQLite(dsn string) (*Conn, error) {
	// Accept "sqlite:" / "file:" prefixes and strip them. Bare paths and
	// ":memory:" stay as-is.
	path := strings.TrimPrefix(dsn, "sqlite:")
	path = strings.TrimPrefix(path, "file:")

	// secure_delete(ON): zero-overwrite freed pages on DELETE so revoked
	// configs (which can carry OTLP tokens) don't linger as readable bytes
	// in unallocated pages. Trades a small write-amplification cost for
	// "rotation actually rotates" semantics.
	pragmaDSN := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=secure_delete(ON)",
		path,
	)
	conn, err := sql.Open("sqlite", pragmaDSN)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(conn, DialectSQLite); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Conn{DB: conn, Dialect: DialectSQLite}, nil
}

func openPostgres(dsn string) (*Conn, error) {
	// pgx/v5/stdlib registers the driver name "pgx" with database/sql.
	// We pass the postgres:// URL through unchanged — pgx accepts the
	// standard libpq URL form, including TLS, statement_timeout, and
	// other params operators set in their connection string.
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if err := migrate(conn, DialectPostgres); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Conn{DB: conn, Dialect: DialectPostgres}, nil
}

func migrate(conn *sql.DB, dialect Dialect) error {
	goose.SetLogger(goose.NopLogger())

	switch dialect {
	case DialectSQLite:
		goose.SetBaseFS(sqliteMigrationsFS)
		if err := goose.SetDialect("sqlite3"); err != nil {
			return fmt.Errorf("goose dialect: %w", err)
		}
		if err := goose.Up(conn, "migrations/sqlite"); err != nil {
			return fmt.Errorf("goose up: %w", err)
		}
	case DialectPostgres:
		goose.SetBaseFS(postgresMigrationsFS)
		if err := goose.SetDialect("postgres"); err != nil {
			return fmt.Errorf("goose dialect: %w", err)
		}
		if err := goose.Up(conn, "migrations/postgres"); err != nil {
			return fmt.Errorf("goose up: %w", err)
		}
	default:
		return fmt.Errorf("unsupported migration dialect: %q", dialect)
	}
	return nil
}

// Rebind translates `?` placeholders to dialect-specific syntax.
//
// SQLite (and our Go store SQL by default) uses `?`. Postgres uses
// `$1, $2, …` numbered from 1. The helper is a flat byte walk — fine
// because our queries don't contain `?` inside string literals; the
// only place `?` appears is as a placeholder. If that invariant ever
// changes, replace this with a proper SQL tokenizer.
//
// SQLite returns the input unchanged (allocation-free fast path).
func Rebind(dialect Dialect, query string) string {
	if dialect != DialectPostgres {
		return query
	}
	if !strings.ContainsRune(query, '?') {
		return query
	}
	var sb strings.Builder
	sb.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			sb.WriteByte('$')
			sb.WriteString(strconv.Itoa(n))
			continue
		}
		sb.WriteByte(query[i])
	}
	return sb.String()
}
