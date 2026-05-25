package store

import (
	"context"
	"encoding/json"
	"errors"
	"escrowd/internal/escrow"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool    *pgxpool.Pool
	AuditDB *AuditStore
}

type AuditStore struct {
	pool *pgxpool.Pool
}

func New(path string) (*Store, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, errors.New("DATABASE_URL environment variable not set")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		return nil, fmt.Errorf("could not connect to database: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("could not ping database: %w", err)
	}

	s := &Store{
		pool:    pool,
		AuditDB: &AuditStore{pool: pool},
	}

	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return s, nil
}

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS escrows (
			id          TEXT PRIMARY KEY,
			data        JSONB NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id          TEXT PRIMARY KEY,
			escrow_id   TEXT NOT NULL,
			event       TEXT NOT NULL,
			actor_id    TEXT NOT NULL,
			actor_name  TEXT NOT NULL,
			detail      TEXT NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_escrow_id ON audit_log(escrow_id)`,
		`CREATE INDEX IF NOT EXISTS idx_escrows_updated ON escrows(updated_at)`,
	}

	for _, q := range queries {
		if _, err := s.pool.Exec(context.Background(), q); err != nil {
			return fmt.Errorf("migration query failed: %w", err)
		}
	}

	return nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Save(deal escrow.Escrow) error {
	data, err := json.Marshal(deal)
	if err != nil {
		return fmt.Errorf("could not marshal deal: %w", err)
	}

	_, err = s.pool.Exec(context.Background(), `
		INSERT INTO escrows (id, data, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (id) DO UPDATE
		SET data = $2, updated_at = NOW()
	`, deal.ID, data)

	return err
}

func (s *Store) Get(id string) (escrow.Escrow, error) {
	var data []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT data FROM escrows WHERE id = $1`, id,
	).Scan(&data)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return escrow.Escrow{}, fmt.Errorf("deal not found: %s", id)
		}
		return escrow.Escrow{}, err
	}

	var deal escrow.Escrow
	if err := json.Unmarshal(data, &deal); err != nil {
		return escrow.Escrow{}, fmt.Errorf("could not unmarshal deal: %w", err)
	}

	return deal, nil
}

func (s *Store) Delete(id string) error {
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM escrows WHERE id = $1`, id)
	return err
}

func (s *Store) ListIDs() ([]string, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id FROM escrows`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	return ids, rows.Err()
}

func (s *Store) DeleteUserData(userID string) (int, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, data FROM escrows`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type row struct {
		id   string
		data []byte
	}
	var toUpdate []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.data); err != nil {
			return 0, err
		}
		toUpdate = append(toUpdate, r)
	}
	rows.Close()

	anonymized := 0
	for _, r := range toUpdate {
		var deal escrow.Escrow
		if err := json.Unmarshal(r.data, &deal); err != nil {
			continue
		}

		changed := false
		if deal.SenderID == userID {
			deal.SenderID = "deleted-user"
			deal.SenderName = "deleted-user"
			changed = true
		}
		if deal.ReceiverID == userID {
			deal.ReceiverID = "deleted-user"
			deal.ReceiverName = "deleted-user"
			changed = true
		}
		if deal.Dispute != nil && deal.Dispute.RaisedByID == userID {
			deal.Dispute.RaisedByID = "deleted-user"
			deal.Dispute.RaisedByName = "deleted-user"
			changed = true
		}

		if changed {
			if err := s.Save(deal); err != nil {
				continue
			}
			anonymized++
		}
	}

	return anonymized, nil
}

func (s *Store) MigrateUsers() error {
	return s.migrateUsers()
}
