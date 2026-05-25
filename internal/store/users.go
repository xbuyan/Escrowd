package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	IsAdmin      bool
	CreatedAt    time.Time
}

func (s *Store) migrateUsers() error {
	_, err := s.pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS users (
			id            TEXT PRIMARY KEY,
			username      TEXT UNIQUE NOT NULL,
			email         TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

func (s *Store) CreateUser(user User) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO users (id, username, email, password_hash, is_admin)
		VALUES ($1, $2, $3, $4, $5)
	`, user.ID, user.Username, user.Email, user.PasswordHash, user.IsAdmin)
	if err != nil {
		return fmt.Errorf("could not create user: %w", err)
	}
	return nil
}

func (s *Store) GetUserByEmail(email string) (User, error) {
	var u User
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, username, email, password_hash, is_admin, created_at
		FROM users WHERE email = $1
	`, email).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt)
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
		SELECT id, username, email, password_hash, is_admin, created_at
		FROM users WHERE id = $1
	`, id).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt)
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
