package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/xbuyan/Escrowd/internal/auth"
)

func withJWTSecret(t *testing.T, secret string) {
	t.Helper()
	old := os.Getenv("JWT_SECRET")
	os.Setenv("JWT_SECRET", secret)
	t.Cleanup(func() { os.Setenv("JWT_SECRET", old) })
}

func TestRequireAuth_MissingHeaderRejected(t *testing.T) {
	called := false
	handler := requireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/api/deals", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Error("expected next handler NOT to be called without an Authorization header")
	}
}

func TestRequireAuth_MalformedHeaderRejected(t *testing.T) {
	handler := requireAuth(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for a malformed header")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/deals", nil)
	req.Header.Set("Authorization", "NotBearer sometoken")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestRequireAuth_InvalidTokenRejected(t *testing.T) {
	withJWTSecret(t, "test-secret")
	handler := requireAuth(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for an invalid token")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/deals", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestRequireAuth_ValidTokenPassesClaimsThrough(t *testing.T) {
	withJWTSecret(t, "test-secret")
	tok, err := auth.IssueToken("user-1", "alice", true)
	if err != nil {
		t.Fatal(err)
	}

	var gotUserID, gotUserName, gotIsAdmin string
	handler := requireAuth(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = r.Header.Get("X-User-ID")
		gotUserName = r.Header.Get("X-User-Name")
		gotIsAdmin = r.Header.Get("X-Is-Admin")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/deals", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if gotUserID != "user-1" || gotUserName != "alice" || gotIsAdmin != "true" {
		t.Errorf("claims not passed through correctly: id=%q name=%q admin=%q", gotUserID, gotUserName, gotIsAdmin)
	}
}

func TestRequireAuth_NonAdminTokenDoesNotSetAdminHeader(t *testing.T) {
	withJWTSecret(t, "test-secret")
	tok, err := auth.IssueToken("user-2", "bob", false)
	if err != nil {
		t.Fatal(err)
	}

	var gotIsAdmin string
	handler := requireAuth(func(w http.ResponseWriter, r *http.Request) {
		gotIsAdmin = r.Header.Get("X-Is-Admin")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/deals", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if gotIsAdmin != "" {
		t.Errorf("expected no X-Is-Admin header for a non-admin token, got %q", gotIsAdmin)
	}
}

func TestRequireAdmin_RejectsWithoutAdminHeader(t *testing.T) {
	called := false
	handler := requireAdmin(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/api/admin/deals", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if called {
		t.Error("expected next handler NOT to be called without admin header")
	}
}

func TestRequireAdmin_AllowsWithAdminHeader(t *testing.T) {
	called := false
	handler := requireAdmin(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/api/admin/deals", nil)
	req.Header.Set("X-Is-Admin", "true")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if !called {
		t.Error("expected next handler to be called with a valid admin header")
	}
}

// requireAdmin trusts the X-Is-Admin header exactly, which only makes sense
// because requireAuth is the only thing that ever sets it (from a verified
// JWT). This test locks in that assumption: nothing except that exact
// value should grant access.
func TestRequireAdmin_RejectsSpoofedNonTrueValue(t *testing.T) {
	handler := requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for a spoofed non-'true' admin header")
	})

	for _, v := range []string{"True", "1", "yes", "TRUE "} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/deals", nil)
		req.Header.Set("X-Is-Admin", v)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("X-Is-Admin=%q: status = %d, want %d", v, rec.Code, http.StatusForbidden)
		}
	}
}

func TestWithCORS_SetsHeadersAndCallsNext(t *testing.T) {
	called := false
	handler := withCORS(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if !called {
		t.Error("expected next handler to be called for a normal GET request")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS origin header to be set")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("expected X-Frame-Options header to be set")
	}
}

func TestWithCORS_HandlesPreflightWithoutCallingNext(t *testing.T) {
	handler := withCORS(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler should not be called for an OPTIONS preflight request")
	})

	req := httptest.NewRequest(http.MethodOptions, "/api/deals", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
