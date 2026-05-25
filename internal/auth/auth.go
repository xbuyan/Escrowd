package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
	tokenExpiry  = 24 * time.Hour
)

// Claims is the JWT payload — what gets signed and verified.
type Claims struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	IsAdmin  bool   `json:"is_admin"`
	jwt.RegisteredClaims
}

// HashPassword hashes a plaintext password using Argon2id.
// Returns a string in the format: $argon2id$salt$hash (base64 encoded).
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("could not generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	encoded := fmt.Sprintf("%s:%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
	return encoded, nil
}

// VerifyPassword checks a plaintext password against a stored hash.
// Uses constant-time comparison to prevent timing attacks.
func VerifyPassword(password, encoded string) (bool, error) {
	var saltB64, hashB64 string
	if _, err := fmt.Sscanf(encoded, "%s", &encoded); err != nil {
		return false, errors.New("invalid hash format")
	}

	// Split salt:hash
	for i, c := range encoded {
		if c == ':' {
			saltB64 = encoded[:i]
			hashB64 = encoded[i+1:]
			break
		}
	}
	if saltB64 == "" || hashB64 == "" {
		return false, errors.New("invalid hash format")
	}

	salt, err := base64.RawStdEncoding.DecodeString(saltB64)
	if err != nil {
		return false, fmt.Errorf("could not decode salt: %w", err)
	}
	storedHash, err := base64.RawStdEncoding.DecodeString(hashB64)
	if err != nil {
		return false, fmt.Errorf("could not decode hash: %w", err)
	}

	// Recompute hash with same parameters
	computedHash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// Constant-time comparison — prevents timing attacks
	if subtle.ConstantTimeCompare(storedHash, computedHash) != 1 {
		return false, nil
	}
	return true, nil
}

// IssueToken creates a signed JWT for a verified user.
func IssueToken(userID, userName string, isAdmin bool) (string, error) {
	secret := jwtSecret()
	if secret == "" {
		return "", errors.New("JWT_SECRET not set")
	}

	claims := Claims{
		UserID:   userID,
		UserName: userName,
		IsAdmin:  isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(tokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "escrowd",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// VerifyToken parses and validates a JWT string.
// Returns the claims if valid, error if expired, tampered, or missing.
func VerifyToken(tokenStr string) (*Claims, error) {
	secret := jwtSecret()
	if secret == "" {
		return nil, errors.New("JWT_SECRET not set")
	}

	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}

func jwtSecret() string {
	return os.Getenv("JWT_SECRET")
}
