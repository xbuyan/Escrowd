package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xbuyan/Escrowd/internal/audit"
	"github.com/xbuyan/Escrowd/internal/bruteforce"
	"github.com/xbuyan/Escrowd/internal/store"
)

// setupAPITest wires up the package-level db/auditLog/shield vars against a
// real Postgres instance, the same way Start() does — these handlers reach
// straight into those package vars, so there's no clean way to test them
// without a real (test) database behind them.
//
// Set TEST_DATABASE_URL to run these; skipped otherwise (see store_test.go
// for the same pattern and setup instructions).
func setupAPITest(t *testing.T) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping api integration tests")
	}

	withJWTSecret(t, "test-secret-do-not-use-in-prod")

	oldURL := os.Getenv("DATABASE_URL")
	os.Setenv("DATABASE_URL", dbURL)
	t.Cleanup(func() { os.Setenv("DATABASE_URL", oldURL) })

	s, err := store.New("")
	if err != nil {
		t.Fatalf("could not connect to test database: %v", err)
	}
	if err := s.MigrateUsers(); err != nil {
		t.Fatalf("could not run user migrations: %v", err)
	}

	// Clean slate for every test — connect directly since Store doesn't
	// expose its pool outside the store package.
	cleanupPool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("cleanup: could not connect: %v", err)
	}
	for _, table := range []string{"escrows", "audit_log", "email_verification_tokens", "users"} {
		if _, err := cleanupPool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			cleanupPool.Close()
			t.Fatalf("cleanup: could not clear %s: %v", table, err)
		}
	}
	cleanupPool.Close()

	db = s
	auditLog = audit.New(s.AuditDB)
	shield = bruteforce.New()

	t.Cleanup(func() { db.Close() })
}

func doJSON(t *testing.T, handler http.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("could not decode response body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestHandleRegister_Success(t *testing.T) {
	setupAPITest(t)

	rec := doJSON(t, handleRegister, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "alice",
		"email":    "alice@example.com",
		"password": "correct-horse-battery",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["email_verified"] != false {
		t.Errorf("expected email_verified=false on registration, got %v", body["email_verified"])
	}
}

func TestHandleRegister_DuplicateEmailRejected(t *testing.T) {
	setupAPITest(t)

	payload := map[string]string{
		"username": "alice",
		"email":    "dup@example.com",
		"password": "correct-horse-battery",
	}
	doJSON(t, handleRegister, http.MethodPost, "/api/auth/register", payload)

	rec := doJSON(t, handleRegister, http.MethodPost, "/api/auth/register", payload)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestHandleRegister_RejectsWeakInput(t *testing.T) {
	setupAPITest(t)

	cases := []struct {
		name string
		body map[string]string
	}{
		{"short username", map[string]string{"username": "ab", "email": "a@b.com", "password": "longenoughpw"}},
		{"invalid email", map[string]string{"username": "alice", "email": "not-an-email", "password": "longenoughpw"}},
		{"short password", map[string]string{"username": "alice", "email": "a@b.com", "password": "short"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := doJSON(t, handleRegister, http.MethodPost, "/api/auth/register", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestHandleLogin_UnverifiedEmailBlocked(t *testing.T) {
	setupAPITest(t)

	doJSON(t, handleRegister, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "bob",
		"email":    "bob@example.com",
		"password": "correct-horse-battery",
	})

	rec := doJSON(t, handleLogin, http.MethodPost, "/api/auth/login", map[string]string{
		"email":    "bob@example.com",
		"password": "correct-horse-battery",
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (unverified email should block login)", rec.Code, http.StatusForbidden)
	}
}

func TestHandleLogin_WrongPasswordRejected(t *testing.T) {
	setupAPITest(t)

	doJSON(t, handleRegister, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "carol",
		"email":    "carol@example.com",
		"password": "correct-horse-battery",
	})

	rec := doJSON(t, handleLogin, http.MethodPost, "/api/auth/login", map[string]string{
		"email":    "carol@example.com",
		"password": "totally-wrong-password",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleLogin_NonExistentEmailGivesGenericError(t *testing.T) {
	setupAPITest(t)

	rec := doJSON(t, handleLogin, http.MethodPost, "/api/auth/login", map[string]string{
		"email":    "nobody@example.com",
		"password": "whatever",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	body := decodeBody(t, rec)
	// Must not reveal whether the email is registered — same message either way.
	if body["error"] != "invalid email or password" {
		t.Errorf("error message leaks account existence: %v", body["error"])
	}
}

func TestHandleLogin_SuccessAfterEmailVerification(t *testing.T) {
	setupAPITest(t)

	doJSON(t, handleRegister, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "dave",
		"email":    "dave@example.com",
		"password": "correct-horse-battery",
	})

	user, err := db.GetUserByEmail("dave@example.com")
	if err != nil {
		t.Fatalf("could not look up registered user: %v", err)
	}
	// Simulate clicking the verification link.
	token := "test-verify-token"
	if err := db.CreateVerificationToken(token, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.VerifyEmailToken(token); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, handleLogin, http.MethodPost, "/api/auth/login", map[string]string{
		"email":    "dave@example.com",
		"password": "correct-horse-battery",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["token"] == "" || body["token"] == nil {
		t.Error("expected a JWT token in the response")
	}
}

func TestHandleResendVerification_DoesNotLeakAccountExistence(t *testing.T) {
	setupAPITest(t)

	recExisting := doJSON(t, handleResendVerification, http.MethodPost, "/api/auth/resend-verification", map[string]string{
		"email": "nobody-registered@example.com",
	})
	bodyExisting := decodeBody(t, recExisting)

	doJSON(t, handleRegister, http.MethodPost, "/api/auth/register", map[string]string{
		"username": "erin",
		"email":    "erin@example.com",
		"password": "correct-horse-battery",
	})
	recRegistered := doJSON(t, handleResendVerification, http.MethodPost, "/api/auth/resend-verification", map[string]string{
		"email": "erin@example.com",
	})
	bodyRegistered := decodeBody(t, recRegistered)

	if bodyExisting["message"] != bodyRegistered["message"] {
		t.Errorf("resend-verification response differs based on registration status — leaks account existence:\n"+
			"unregistered: %v\nregistered:   %v", bodyExisting["message"], bodyRegistered["message"])
	}
}
