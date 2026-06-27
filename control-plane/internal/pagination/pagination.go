// Package pagination provides cursor-based pagination helpers for list
// endpoints.
//
// Magpie v1.0 invariant on response size (docs/v1.0-plan.md §6.1 #2):
// at 2000 hosts, polling /api/v1/agents every 2.5 seconds without bounds
// ships multi-MB JSON each turn. The current UI doesn't paginate yet —
// the v1.0 Fleet view spec will teach it to follow cursors — but the
// server side has to bound responses now so the upgrade path doesn't
// require a synchronized server+client release.
//
// Design:
//   - Cursor is opaque to clients (base64 of an internal id-or-key).
//     Operators must not parse or generate cursors; only echo back
//     whatever the server returned in X-Next-Cursor.
//   - Server-side, the cursor is just a small string that means "give me
//     the next page after this row." Stores translate it to a SQL
//     `WHERE id < ?` (or in-memory `uid > ?`) clause.
//   - Pagination is reads-only. Writes don't paginate; they're scoped to
//     a specific resource by id.
//   - Default limits are sized for "operator polls every 2.5s and wants
//     to see the fleet." Defaults can be overridden per endpoint via
//     the defaultLimit argument to Parse.
//   - MaxLimit caps abuse; a misbehaving client asking for limit=1e9
//     gets MaxLimit instead of being rejected, matching how the v0.2
//     audit endpoint handled overshoots.
package pagination

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

const (
	// DefaultLimit is the fallback when a caller omits ?limit=. Sized to
	// leave 2000-agent fleets four round-trips of /agents data, which keeps
	// per-poll bandwidth and decode cost bounded without forcing operators
	// into deep pagination just to see their fleet.
	DefaultLimit = 500

	// MaxLimit caps the largest permissible page. 5000 lets a single page
	// cover any v1.0 list endpoint at expected scale (2000 agents, hundreds
	// of configs) so operators with a small fleet never hit pagination at
	// all, while still capping a misbehaving caller asking for the whole
	// universe.
	MaxLimit = 5000
)

// Params holds parsed pagination inputs in their post-decoded form.
// Callers compare Cursor against raw stored ids/keys; the base64 wrapper
// is invisible to them.
type Params struct {
	Limit  int
	Cursor string // empty means "first page"
}

// ErrInvalidLimit is returned by Parse when ?limit= is non-numeric or zero/negative.
var ErrInvalidLimit = errors.New("invalid limit")

// ErrInvalidCursor is returned by Parse when ?cursor= is not valid base64.
var ErrInvalidCursor = errors.New("invalid cursor")

// Parse extracts limit + cursor from a query, applying defaults and caps.
// On invalid input returns one of the sentinels above so handlers can
// reply 400 with the offending parameter named.
//
// limit is always returned at a sensible value even on parse failure (the
// caller is expected to bail before using it). Cursor is "" on failure or
// when not provided.
func Parse(q url.Values, defaultLimit int) (Params, error) {
	if defaultLimit <= 0 {
		defaultLimit = DefaultLimit
	}
	p := Params{Limit: defaultLimit}

	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return p, ErrInvalidLimit
		}
		if n > MaxLimit {
			n = MaxLimit
		}
		p.Limit = n
	}

	if c := q.Get("cursor"); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			return p, ErrInvalidCursor
		}
		p.Cursor = string(raw)
	}

	return p, nil
}

// EncodeCursor wraps a raw cursor value (e.g. "12345" or an instance_uid)
// into the opaque base64 form clients see in the X-Next-Cursor header.
//
// RawURLEncoding is intentional: no padding (cleaner header values), URL-
// safe alphabet (no manual escaping when echoed back into a query string).
func EncodeCursor(raw string) string {
	if raw == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// WriteHeaders sets X-Total-Count and X-Next-Cursor on the response.
//
// total < 0 means "skip" — used for endpoints where computing the total
// is expensive (e.g. /audit at millions of rows would require a full
// SELECT COUNT(*) per request). Endpoints with cheap or bounded totals
// (in-memory list, small SQL table) include the count for UI use.
//
// nextRaw is the un-encoded value (Encode is applied here). Pass empty
// when there's no next page so the header is omitted entirely — clients
// detect "end of list" by absence of the header.
func WriteHeaders(w http.ResponseWriter, total int, nextRaw string) {
	if total >= 0 {
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
	}
	if nextRaw != "" {
		w.Header().Set("X-Next-Cursor", EncodeCursor(nextRaw))
	}
}
