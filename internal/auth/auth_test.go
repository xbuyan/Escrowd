package auth

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestHashPassword_ProducesVerifiableHash(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed: %v", err)
	}
	if !ok {
		t.Fatal("expected correct password to verify")
	}
}

func TestHashPassword_DifferentSaltsEachTime(t *testing.T) {
	h1, err := HashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("expected different salts to produce different encoded hashes for the same password")
	}
}

func TestVerifyPassword_WrongPasswordRejected(t *testing.T) {
	hash, err := HashPassword("the-real-password")
	if err != nil {
		t.Fatal(err)
	}

	ok, err := VerifyPassword("not-the-real-password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword returned an error instead of false: %v", err)
	}
	if ok {
		t.Fatal("expected wrong password to fail verification")
	}
}

func TestVerifyPassword_MalformedHashRejected(t *testing.T) {
	cases := []string{
		"",
		"not-a-valid-hash",
		"missingcolon",
		":emptysalt",
		"emptyhash:",
	}
	for _, c := range cases {
		ok, err := VerifyPassword("anything", c)
		if ok {
			t.Fatalf("expected malformed hash %q to fail verification, got ok=true", c)
		}
		if err == nil {
			t.Fatalf("expected malformed hash %q to return an error", c)
		}
	}
}

func withJWTSecret(t *testing.T, secret string) {
	t.Helper()
	old := os.Getenv("JWT_SECRET")
	os.Setenv("JWT_SECRET", secret)
	t.Cleanup(func() { os.Setenv("JWT_SECRET", old) })
}

func TestIssueToken_RequiresSecret(t *testing.T) {
	withJWTSecret(t, "")
	if _, err := IssueToken("user-1", "alice", false); err == nil {
		t.Fatal("expected IssueToken to fail when JWT_SECRET is unset")
	}
}

func TestIssueAndVerifyToken_RoundTrip(t *testing.T) {
	withJWTSecret(t, "test-secret-do-not-use-in-prod")

	tok, err := IssueToken("user-42", "bob", true)
	if err != nil {
		t.Fatalf("IssueToken failed: %v", err)
	}

	claims, err := VerifyToken(tok)
	if err != nil {
		t.Fatalf("VerifyToken failed on a token we just issued: %v", err)
	}
	if claims.UserID != "user-42" {
		t.Errorf("UserID = %q, want %q", claims.UserID, "user-42")
	}
	if claims.UserName != "bob" {
		t.Errorf("UserName = %q, want %q", claims.UserName, "bob")
	}
	if !claims.IsAdmin {
		t.Error("expected IsAdmin to be true")
	}
}

func TestVerifyToken_WrongSecretRejected(t *testing.T) {
	withJWTSecret(t, "secret-a")
	tok, err := IssueToken("user-1", "alice", false)
	if err != nil {
		t.Fatal(err)
	}

	withJWTSecret(t, "secret-b")
	if _, err := VerifyToken(tok); err == nil {
		t.Fatal("expected token signed with a different secret to fail verification")
	}
}

func TestVerifyToken_ExpiredTokenRejected(t *testing.T) {
	withJWTSecret(t, "test-secret")

	claims := Claims{
		UserID:   "user-1",
		UserName: "alice",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-25 * time.Hour)),
			Issuer:    "escrowd",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := VerifyToken(signed); err == nil {
		t.Fatal("expected expired token to fail verification")
	}
}

func TestVerifyToken_RejectsUnexpectedSigningMethod(t *testing.T) {
	withJWTSecret(t, "test-secret")

	// A token signed with "none" (or any non-HMAC method) must be rejected —
	// this is the classic JWT "alg confusion" attack.
	claims := Claims{
		UserID: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := VerifyToken(signed); err == nil {
		t.Fatal("expected token signed with 'none' algorithm to be rejected")
	}
}

func TestVerifyToken_GarbageTokenRejected(t *testing.T) {
	withJWTSecret(t, "test-secret")
	if _, err := VerifyToken("not.a.jwt"); err == nil {
		t.Fatal("expected garbage input to fail verification")
	}
}
