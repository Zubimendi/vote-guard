package reconcile

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{pool: pool, log: log}
}

// RunAll reconciles open and recently-closed polls (closed within last 7 days).
func (s *Service) RunAll(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM polls
		WHERE status = 'OPEN'
		   OR (status = 'CLOSED' AND COALESCE(closes_at, created_at) > now() - interval '7 days')
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range ids {
		if err := s.RunPoll(ctx, id); err != nil {
			s.log.Error("reconcile poll failed", "poll_id", id, "err", err)
		}
	}
	return nil
}

// RunPoll compares cache vs COUNT(*) per option and always records reconciliation_runs.
func (s *Service) RunPoll(ctx context.Context, pollID uuid.UUID) error {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id,
		       COALESCE(c.vote_count, 0),
		       COALESCE(v.cnt, 0)
		FROM poll_options o
		LEFT JOIN poll_result_cache c ON c.poll_id = o.poll_id AND c.option_id = o.id
		LEFT JOIN (
			SELECT option_id, COUNT(*)::bigint AS cnt
			FROM votes
			WHERE poll_id = $1
			GROUP BY option_id
		) v ON v.option_id = o.id
		WHERE o.poll_id = $1
	`, pollID)
	if err != nil {
		return fmt.Errorf("query tallies: %w", err)
	}
	defer rows.Close()

	type row struct {
		optionID uuid.UUID
		cached   int64
		verified int64
	}
	var items []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.optionID, &r.cached, &r.verified); err != nil {
			return err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, item := range items {
		drift := item.cached - item.verified
		_, err := tx.Exec(ctx, `
			INSERT INTO reconciliation_runs (poll_id, option_id, cached_count, verified_count, drift)
			VALUES ($1, $2, $3, $4, $5)
		`, pollID, item.optionID, item.cached, item.verified, drift)
		if err != nil {
			return err
		}
		if drift != 0 {
			s.log.Warn("tally drift detected",
				"poll_id", pollID,
				"option_id", item.optionID,
				"cached", item.cached,
				"verified", item.verified,
				"drift", drift,
			)
		}
	}
	return tx.Commit(ctx)
}
