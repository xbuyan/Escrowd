package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID            string
	Username      string
	Email         string
	PasswordHash  string
	IsAdmin       bool
	EmailVerified bool
	CreatedAt     time.Time
}

func (s *Store) migrateUsers() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id             TEXT PRIMARY KEY,
			username       TEXT UNIQUE NOT NULL,
			email          TEXT UNIQUE NOT NULL,
			password_hash  TEXT NOT NULL,
			is_admin       BOOLEAN NOT NULL DEFAULT FALSE,
			email_verified BOOLEAN NOT NULL DEFAULT FALSE,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS email_verification_tokens (
			token      TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_verification_user ON email_verification_tokens(user_id)`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified BOOLEAN NOT NULL DEFAULT FALSE`,
	}
	for _, q := range queries {
		if _, err := s.pool.Exec(context.Background(), q); err != nil {
			return fmt.Errorf("user migration query failed: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateUser(user User) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO users (id, username, email, password_hash, is_admin, email_verified)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, user.ID, user.Username, user.Email, user.PasswordHash, user.IsAdmin, user.EmailVerified)
	if err != nil {
		return fmt.Errorf("could not create user: %w", err)
	}
	return nil
}

func (s *Store) GetUserByEmail(email string) (User, error) {
	var u User
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, username, email, password_hash, is_admin, email_verified, created_at
		FROM users WHERE email = $1
	`, email).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.EmailVerified, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, errors.New("user not found")
		}
		return User{}, fmt.Errorf("could not get user: %w", err)
	}
	return u, nil
}

func (s *Store) GetUserByID(id string) (User, error) {
	var u User
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, username, email, password_hash, is_admin, email_verified, created_at
		FROM users WHERE id = $1
	`, id).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.EmailVerified, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, errors.New("user not found")
		}
		return User{}, fmt.Errorf("could not get user: %w", err)
	}
	return u, nil
}

func (s *Store) UserExists(email string) (bool, error) {
	var count int
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM users WHERE email = $1
	`, email).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateVerificationToken stores a new email verification token for a user.
// Tokens expire after 24 hours.
func (s *Store) CreateVerificationToken(token, userID string) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO email_verification_tokens (token, user_id, expires_at)
		VALUES ($1, $2, NOW() + INTERVAL '24 hours')
	`, token, userID)
	if err != nil {
		return fmt.Errorf("could not create verification token: %w", err)
	}
	return nil
}

// VerifyEmailToken checks a token, marks the user as verified if valid,
// and deletes the token (single use). Returns error if token is invalid or expired.
func (s *Store) VerifyEmailToken(token string) error {
	var userID string
	var expiresAt time.Time

	err := s.pool.QueryRow(context.Background(), `
		SELECT user_id, expires_at FROM email_verification_tokens WHERE token = $1
	`, token).Scan(&userID, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("invalid or already-used verification token")
		}
		return fmt.Errorf("could not look up token: %w", err)
	}

	if time.Now().After(expiresAt) {
		// Clean up expired token
		s.pool.Exec(context.Background(), `DELETE FROM email_verification_tokens WHERE token = $1`, token)
		return errors.New("verification token expired — please request a new one")
	}

	_, err = s.pool.Exec(context.Background(), `
		UPDATE users SET email_verified = TRUE WHERE id = $1
	`, userID)
	if err != nil {
		return fmt.Errorf("could not mark user verified: %w", err)
	}

	// Single-use — delete after success
	s.pool.Exec(context.Background(), `DELETE FROM email_verification_tokens WHERE token = $1`, token)

	return nil
}

// DeleteVerificationTokensForUser removes any existing tokens before issuing a new one.
// Prevents token accumulation when a user requests multiple resend attempts.
func (s *Store) DeleteVerificationTokensForUser(userID string) error {
	_, err := s.pool.Exec(context.Background(), `
		DELETE FROM email_verification_tokens WHERE user_id = $1
	`, userID)
	return err
}
