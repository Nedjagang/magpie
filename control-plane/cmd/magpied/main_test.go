package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dummyHandler responds 200 / "ok". Used by middleware tests so they
// can distinguish "middleware passed through" from "middleware blocked"
// just by status code, with no real handler logic in the way.
var dummyHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

func TestBearerAuth_NoTokenConfigured_PassesThrough(t *testing.T) {
	mw := bearerAuthMiddleware("")
	rec := httptest.NewRecorder()
	mw(dummyHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no-auth mode)", rec.Code)
	}
}

func TestBearerAuth_MissingHeader_Returns401(t *testing.T) {
	mw := bearerAuthMiddleware("supersecret")
	rec := httptest.NewRecorder()
	mw(dummyHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want to contain Bearer", got)
	}
}

func TestBearerAuth_WrongToken_Returns401(t *testing.T) {
	mw := bearerAuthMiddleware("supersecret")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	r.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	mw(dummyHandler).ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestBearerAuth_CorrectToken_Allows(t *testing.T) {
	mw := bearerAuthMiddleware("supersecret")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
	r.Header.Set("Authorization", "Bearer supersecret")
	rec := httptest.NewRecorder()
	mw(dummyHandler).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestBearerAuth_MalformedScheme_Returns401(t *testing.T) {
	mw := bearerAuthMiddleware("supersecret")
	for _, h := range []string{
		"supersecret",         // missing scheme
		"Basic supersecret",   // wrong scheme
		"bearer supersecret",  // case-sensitive on scheme; spec says "Bearer "
		"Bearer  supersecret", // extra space — current impl treats as "valid", we accept; this case documents that
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
		r.Header.Set("Authorization", h)
		rec := httptest.NewRecorder()
		mw(dummyHandler).ServeHTTP(rec, r)
		// Three of the four should fail auth. The "extra space" case is a
		// known leniency (TrimSpace inside bearerFromHeader). If you tighten
		// the parser, drop this branch.
		if h == "Bearer  supersecret" {
			if rec.Code != http.StatusOK {
				t.Errorf("input %q: status = %d, want 200 (extra space tolerated)", h, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("input %q: status = %d, want 401", h, rec.Code)
		}
	}
}

func TestActorOf_AuthenticatedWithLabel(t *testing.T) {
	mw := bearerAuthMiddleware("t")

	var got string
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = actorOf(r)
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer t")
	r.Header.Set("X-Magpie-Actor", "alice@corp")
	mw(probe).ServeHTTP(httptest.NewRecorder(), r)

	if got != "authenticated:alice@corp" {
		t.Fatalf("actorOf = %q, want authenticated:alice@corp", got)
	}
}

func TestActorOf_AuthenticatedNoLabel(t *testing.T) {
	mw := bearerAuthMiddleware("t")

	var got string
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = actorOf(r)
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer t")
	mw(probe).ServeHTTP(httptest.NewRecorder(), r)

	if got != "authenticated" {
		t.Fatalf("actorOf = %q, want authenticated", got)
	}
}

func TestActorOf_NoAuthMode(t *testing.T) {
	mw := bearerAuthMiddleware("") // disabled

	var got string
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = actorOf(r)
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Magpie-Actor", "self-declared")
	mw(probe).ServeHTTP(httptest.NewRecorder(), r)

	if got != "anonymous:self-declared" {
		t.Fatalf("actorOf = %q, want anonymous:self-declared (label kept, no auth)", got)
	}
}

func TestCORS_AllowedOrigin_Echoed(t *testing.T) {
	mw := corsMiddleware([]string{"http://localhost:12001"})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "http://localhost:12001")
	rec := httptest.NewRecorder()
	mw(dummyHandler).ServeHTTP(rec, r)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:12001" {
		t.Errorf("ACAO = %q, want echoed origin", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}
}

func TestCORS_DisallowedOrigin_Read_NoACAO(t *testing.T) {
	mw := corsMiddleware([]string{"http://localhost:12001"})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	mw(dummyHandler).ServeHTTP(rec, r)

	// GET from a non-allowlisted origin: handler runs (browsers will still
	// block the response without an ACAO header), but no header is echoed.
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("ACAO = %q, want empty (origin not allowlisted)", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (GET from disallowed origin allowed at server)", rec.Code)
	}
}

func TestCORS_DisallowedOrigin_Write_403(t *testing.T) {
	mw := corsMiddleware([]string{"http://localhost:12001"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/configs", nil)
	r.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	mw(dummyHandler).ServeHTTP(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (cross-origin write blocked)", rec.Code)
	}
}

func TestCORS_NoOriginHeader_NoBlock(t *testing.T) {
	mw := corsMiddleware([]string{"http://localhost:12001"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/configs", nil)
	// no Origin header — non-browser caller (curl, agent, server-to-server)
	rec := httptest.NewRecorder()
	mw(dummyHandler).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no-origin caller is fine)", rec.Code)
	}
}

func TestParseCSVList(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"http://a", []string{"http://a"}},
		{"http://a, http://b ,http://c", []string{"http://a", "http://b", "http://c"}},
		{",,,", nil},
		{"otlp,batch,memory_limiter", []string{"otlp", "batch", "memory_limiter"}},
	}
	for _, tt := range tests {
		got := parseCSVList(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("parseCSVList(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseCSVList(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}
