package db_test

import (
	"path/filepath"
	"testing"

	"github.com/magpie-project/magpie/control-plane/internal/db"
)

func TestOpenSQLitePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open(%q): %v", path, err)
	}
	defer conn.Close()
	if conn.Dialect != db.DialectSQLite {
		t.Errorf("dialect = %q, want %q", conn.Dialect, db.DialectSQLite)
	}
	if err := conn.DB.Ping(); err != nil {
		t.Errorf("ping: %v", err)
	}
}

func TestOpenSQLitePrefixesStripped(t *testing.T) {
	// Operators may set MAGPIE_DATABASE_URL to "sqlite:..." or "file:..."
	// — both should resolve to the same backing file the bare path does.
	dir := t.TempDir()
	for _, prefix := range []string{"sqlite:", "file:"} {
		path := filepath.Join(dir, "test-"+prefix+"x.db")
		conn, err := db.Open(prefix + path)
		if err != nil {
			t.Errorf("db.Open(%q+%q): %v", prefix, path, err)
			continue
		}
		conn.Close()
		if conn.Dialect != db.DialectSQLite {
			t.Errorf("dialect with prefix %q = %q, want %q", prefix, conn.Dialect, db.DialectSQLite)
		}
	}
}

func TestOpenAppliesV1Migrations(t *testing.T) {
	// Smoke-test that the v1.0 migrations (00007 / 00008 / 00009) actually
	// applied — without this, a future migration that breaks at startup
	// could ship silently because the existing agents tests only exercise
	// what their own queries touch.
	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer conn.Close()

	// Each of these should return zero rows, not "no such table".
	for _, table := range []string{"rollouts", "apply_state", "configs", "audit_log", "agents"} {
		var n int
		if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Errorf("table %q not present after migrations: %v", table, err)
		}
	}

	// Spot-check a v1.0-specific column made it through migration 00007.
	var n int
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM configs WHERE scope_kind = 'product_variant'`).Scan(&n); err != nil {
		t.Errorf("configs.scope_kind missing after migration 00007: %v", err)
	}

	// And a column from migration 00009 (audit hash chain extension).
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE hash = ''`).Scan(&n); err != nil {
		t.Errorf("audit_log.hash missing after migration 00009: %v", err)
	}
}

func TestRebindSQLitePassthrough(t *testing.T) {
	// SQLite must not rewrite `?` — the modernc driver handles them
	// natively. Rebind on the SQLite path should be allocation-free
	// in spirit; we just check the output equals the input.
	q := "SELECT * FROM x WHERE a = ? AND b = ? ORDER BY id LIMIT ?"
	if got := db.Rebind(db.DialectSQLite, q); got != q {
		t.Errorf("SQLite Rebind altered query:\n  in:  %s\n  out: %s", q, got)
	}
}

func TestRebindPostgresNumbered(t *testing.T) {
	// Postgres needs `$1, $2, ...` numbered from 1. Verify the count
	// matches the number of `?` and the numbering is sequential.
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "SELECT * FROM x WHERE a = ?",
			want: "SELECT * FROM x WHERE a = $1",
		},
		{
			in:   "INSERT INTO x (a, b, c) VALUES (?, ?, ?)",
			want: "INSERT INTO x (a, b, c) VALUES ($1, $2, $3)",
		},
		{
			in:   "SELECT 1",
			want: "SELECT 1", // no ? → no-op
		},
		{
			in:   "UPDATE x SET a = ?, b = ? WHERE id = ? AND v < ?",
			want: "UPDATE x SET a = $1, b = $2 WHERE id = $3 AND v < $4",
		},
	}
	for _, tc := range cases {
		if got := db.Rebind(db.DialectPostgres, tc.in); got != tc.want {
			t.Errorf("Rebind(postgres, %q) =\n  %q\nwant:\n  %q", tc.in, got, tc.want)
		}
	}
}
