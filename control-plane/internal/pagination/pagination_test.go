package pagination

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParseDefaultLimit(t *testing.T) {
	p, err := Parse(url.Values{}, 250)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.Limit != 250 {
		t.Errorf("default limit not applied: got %d want 250", p.Limit)
	}
	if p.Cursor != "" {
		t.Errorf("cursor should be empty when not provided, got %q", p.Cursor)
	}
}

func TestParseDefaultLimitZeroFallsBack(t *testing.T) {
	// A handler that passes 0 as the default should still get a sensible
	// limit — we use the package DefaultLimit rather than 0 (which would
	// silently return zero rows and confuse callers).
	p, err := Parse(url.Values{}, 0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.Limit != DefaultLimit {
		t.Errorf("zero defaultLimit should fall back to DefaultLimit, got %d", p.Limit)
	}
}

func TestParseLimitClampedToMax(t *testing.T) {
	q := url.Values{"limit": []string{"100000"}}
	p, err := Parse(q, DefaultLimit)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.Limit != MaxLimit {
		t.Errorf("limit not clamped: got %d want %d", p.Limit, MaxLimit)
	}
}

func TestParseInvalidLimit(t *testing.T) {
	cases := []string{"abc", "0", "-1", " "}
	for _, c := range cases {
		q := url.Values{"limit": []string{c}}
		_, err := Parse(q, DefaultLimit)
		if !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("Parse(limit=%q) err = %v, want ErrInvalidLimit", c, err)
		}
	}
}

func TestParseCursorRoundTrip(t *testing.T) {
	encoded := EncodeCursor("12345")
	q := url.Values{"cursor": []string{encoded}}
	p, err := Parse(q, DefaultLimit)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if p.Cursor != "12345" {
		t.Errorf("cursor round-trip failed: got %q want %q", p.Cursor, "12345")
	}
}

func TestParseInvalidCursor(t *testing.T) {
	// Non-base64 garbage should fail with ErrInvalidCursor, not silently fall
	// back to no-cursor (which would page from the start and lose the
	// caller's context).
	q := url.Values{"cursor": []string{"not!base64@@"}}
	_, err := Parse(q, DefaultLimit)
	if !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("Parse(bad cursor) err = %v, want ErrInvalidCursor", err)
	}
}

func TestEncodeCursorEmpty(t *testing.T) {
	// EncodeCursor of empty must be empty so WriteHeaders' "skip when empty"
	// path is consistent across stores that return "" for "no next page."
	if got := EncodeCursor(""); got != "" {
		t.Errorf("EncodeCursor(\"\") = %q, want empty", got)
	}
}

func TestWriteHeadersHappyPath(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteHeaders(rec, 1840, "999")

	if got := rec.Header().Get("X-Total-Count"); got != "1840" {
		t.Errorf("X-Total-Count = %q, want 1840", got)
	}
	// Should be base64 of "999".
	want := EncodeCursor("999")
	if got := rec.Header().Get("X-Next-Cursor"); got != want {
		t.Errorf("X-Next-Cursor = %q, want %q", got, want)
	}
}

func TestWriteHeadersNoMore(t *testing.T) {
	// End of list — nextRaw="" means omit X-Next-Cursor entirely so callers
	// detect end-of-list by header absence rather than parsing an empty value.
	rec := httptest.NewRecorder()
	WriteHeaders(rec, 42, "")

	if got := rec.Header().Get("X-Total-Count"); got != "42" {
		t.Errorf("X-Total-Count = %q, want 42", got)
	}
	if got := rec.Header().Get("X-Next-Cursor"); got != "" {
		t.Errorf("X-Next-Cursor should be absent at end of list, got %q", got)
	}
}

func TestWriteHeadersSkipsTotal(t *testing.T) {
	// total=-1 sentinel means "skip X-Total-Count" — used by /audit where
	// SELECT COUNT(*) at scale is too expensive to run on every request.
	rec := httptest.NewRecorder()
	WriteHeaders(rec, -1, "abc")

	if got := rec.Header().Get("X-Total-Count"); got != "" {
		t.Errorf("X-Total-Count should be skipped on total=-1, got %q", got)
	}
	if got := rec.Header().Get("X-Next-Cursor"); got == "" {
		t.Errorf("X-Next-Cursor should still be set even when total is skipped")
	}
}
