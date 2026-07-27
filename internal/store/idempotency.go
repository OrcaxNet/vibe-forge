package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/OrcaxNet/vibe-forge/contracts"
)

// idempotencyTTL is the window within which a repeated key replays the original
// result (contract §idempotency.ttlSeconds).
func idempotencyTTL() time.Duration {
	return time.Duration(contracts.Load().Idempotency.TTLSeconds) * time.Second
}

// idempotentResult is what runIdempotent returns to the HTTP layer.
type idempotentResult struct {
	status   int
	body     []byte
	replayed bool
}

// runIdempotent executes fn inside a transaction and makes it idempotent on
// (endpoint, key): if the key was seen within ttlSeconds it replays the stored
// 2xx response verbatim; otherwise it runs fn and, on a 2xx outcome, records the
// response. Only 2xx results are cached so a client can fix a validation error
// and retry with the same key.
//
// fn receives the transaction so its side effects commit atomically with the
// ledger insert - two concurrent identical requests cannot both miss the cache
// and create duplicates.
func (s *Store) runIdempotent(
	ctx context.Context,
	endpoint, key string,
	fn func(tx *sql.Tx) (int, []byte, error),
) (idempotentResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return idempotentResult{}, fmt.Errorf("begin idempotency tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	var storedStatus int
	var storedBody string
	var createdAt string
	err = tx.QueryRowContext(ctx,
		`SELECT status_code, response_body, created_at FROM idempotency_records WHERE key = ? AND endpoint = ?`,
		key, endpoint).Scan(&storedStatus, &storedBody, &createdAt)
	switch {
	case err == nil:
		// Hit. Replay if still within TTL; otherwise evict and proceed fresh.
		if s.withinTTL(createdAt) {
			if err := tx.Commit(); err != nil {
				return idempotentResult{}, fmt.Errorf("commit idempotency replay: %w", err)
			}
			return idempotentResult{status: storedStatus, body: []byte(storedBody), replayed: true}, nil
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM idempotency_records WHERE key = ? AND endpoint = ?`, key, endpoint); err != nil {
			return idempotentResult{}, fmt.Errorf("evict expired idempotency: %w", err)
		}
	case isNoRows(err):
		// Miss - fall through to fn.
	default:
		return idempotentResult{}, fmt.Errorf("query idempotency: %w", err)
	}

	status, body, err := fn(tx)
	if err != nil {
		return idempotentResult{}, err
	}
	if status >= 200 && status < 300 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO idempotency_records(key, endpoint, status_code, response_body, created_at)
			 VALUES(?, ?, ?, ?, ?)`,
			key, endpoint, status, string(body), s.nowTS()); err != nil {
			return idempotentResult{}, fmt.Errorf("record idempotency: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return idempotentResult{}, fmt.Errorf("commit idempotency: %w", err)
	}
	return idempotentResult{status: status, body: body, replayed: false}, nil
}

// withinTTL reports whether an RFC3339 timestamp is within the idempotency TTL.
func (s *Store) withinTTL(rfc3339 string) bool {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return false
	}
	return s.now().Sub(t) < idempotencyTTL()
}

// GCIdempotency deletes ledger entries older than the TTL. Safe to run
// periodically; a no-op when there is nothing to collect.
func (s *Store) GCIdempotency(ctx context.Context) (int64, error) {
	cutoff := s.now().Add(-idempotencyTTL()).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_records WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("gc idempotency: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func isNoRows(err error) bool { return err == sql.ErrNoRows }
