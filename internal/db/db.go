// Package db is the Postgres-backed implementation of store.Persister. It exists so a redeploy
// (or crash) doesn't wipe the daily leaderboard or — more importantly — a paid round's
// registration (roundId/endTime), which the fund-safety gate in internal/api depends on to
// know which submissions are eligible. Every method is best-effort: a DB hiccup is logged, not
// fatal — gameplay keeps working in-memory even if persistence briefly fails.
package db

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wordbreak/backend/internal/store"
)

const timeout = 5 * time.Second

const schema = `
CREATE TABLE IF NOT EXISTS players (
	address    TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS daily_rounds (
	date_key   TEXT PRIMARY KEY,
	letters    TEXT NOT NULL,
	paid       BOOLEAN NOT NULL DEFAULT false,
	round_id   TEXT NOT NULL DEFAULT '',
	end_time   TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS daily_submissions (
	date_key     TEXT NOT NULL,
	address      TEXT NOT NULL,
	score        INT NOT NULL,
	words        INT NOT NULL,
	submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (date_key, address)
);
`

// DB is a Postgres connection pool implementing store.Persister.
type DB struct {
	pool *pgxpool.Pool
}

var _ store.Persister = (*DB)(nil)

// New connects to databaseURL and ensures the schema exists.
func New(ctx context.Context, databaseURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := pool.Exec(pingCtx, schema); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool}, nil
}

func (db *DB) Close() { db.pool.Close() }

func (db *DB) UpsertDailyRound(dateKey, letters string, paid bool, roundID string, endTime time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var et *time.Time
	if !endTime.IsZero() {
		et = &endTime
	}
	_, err := db.pool.Exec(ctx, `
		INSERT INTO daily_rounds (date_key, letters, paid, round_id, end_time)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (date_key) DO UPDATE SET
			paid = EXCLUDED.paid, round_id = EXCLUDED.round_id, end_time = EXCLUDED.end_time
	`, dateKey, letters, paid, roundID, et)
	if err != nil {
		log.Printf("[db] UpsertDailyRound(%s): %v", dateKey, err)
	}
}

func (db *DB) LoadDailyRound(dateKey string) (letters string, paid bool, roundID string, endTime time.Time, found bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var et *time.Time
	err := db.pool.QueryRow(ctx,
		`SELECT letters, paid, round_id, end_time FROM daily_rounds WHERE date_key = $1`, dateKey,
	).Scan(&letters, &paid, &roundID, &et)
	if err != nil {
		return "", false, "", time.Time{}, false
	}
	if et != nil {
		endTime = *et
	}
	return letters, paid, roundID, endTime, true
}

func (db *DB) UpsertSubmission(dateKey string, sub store.Submission) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := db.pool.Exec(ctx, `
		INSERT INTO daily_submissions (date_key, address, score, words, submitted_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (date_key, address) DO UPDATE SET
			score = EXCLUDED.score, words = EXCLUDED.words, submitted_at = EXCLUDED.submitted_at
		WHERE daily_submissions.score < EXCLUDED.score
	`, dateKey, sub.Address, sub.Score, sub.Words, sub.At)
	if err != nil {
		log.Printf("[db] UpsertSubmission(%s, %s): %v", dateKey, sub.Address, err)
	}
}

func (db *DB) LoadSubmissions(dateKey string) []store.Submission {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	rows, err := db.pool.Query(ctx,
		`SELECT address, score, words, submitted_at FROM daily_submissions WHERE date_key = $1`, dateKey,
	)
	if err != nil {
		log.Printf("[db] LoadSubmissions(%s): %v", dateKey, err)
		return nil
	}
	defer rows.Close()

	var out []store.Submission
	for rows.Next() {
		var s store.Submission
		if err := rows.Scan(&s.Address, &s.Score, &s.Words, &s.At); err != nil {
			log.Printf("[db] LoadSubmissions(%s) scan: %v", dateKey, err)
			continue
		}
		out = append(out, s)
	}
	return out
}

func (db *DB) UpsertPlayerName(address, name string) {
	if name == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := db.pool.Exec(ctx, `
		INSERT INTO players (address, name, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (address) DO UPDATE SET name = EXCLUDED.name, updated_at = now()
	`, address, name)
	if err != nil {
		log.Printf("[db] UpsertPlayerName(%s): %v", address, err)
	}
}
