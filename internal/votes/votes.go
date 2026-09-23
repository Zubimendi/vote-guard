package votes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidToken = errors.New("invalid_token")
	ErrAlreadyUsed  = errors.New("already_used")
	ErrPollNotOpen  = errors.New("poll_not_open")
	ErrBadOption    = errors.New("bad_option")
)

type CastResult struct {
	VoteID   uuid.UUID `json:"vote_id"`
	PollID   uuid.UUID `json:"poll_id"`
	OptionID uuid.UUID `json:"option_id"`
	CastAt   time.Time `json:"cast_at"`
}

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CastRedeem redeems a single-use token and inserts a vote in one transaction.
// Lock order: FOR UPDATE token row → checks → insert vote → bump cache → mark redeemed.
func (s *Service) CastRedeem(ctx context.Context, pollID, optionID uuid.UUID, rawToken string) (CastResult, error) {
	hash := HashToken(rawToken)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CastResult{}, err
	}
	defer tx.Rollback(ctx)

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM polls WHERE id = $1`, pollID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return CastResult{}, ErrInvalidToken
	}
	if err != nil {
		return CastResult{}, err
	}
	if status != "OPEN" {
		return CastResult{}, ErrPollNotOpen
	}

	var (
		tokenID    uuid.UUID
		redeemedAt *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT id, redeemed_at
		FROM voting_tokens
		WHERE poll_id = $1 AND token_hash = $2
		FOR UPDATE
	`, pollID, hash).Scan(&tokenID, &redeemedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CastResult{}, ErrInvalidToken
	}
	if err != nil {
		return CastResult{}, err
	}
	if redeemedAt != nil {
		return CastResult{}, ErrAlreadyUsed
	}

	var optionPoll uuid.UUID
	err = tx.QueryRow(ctx, `SELECT poll_id FROM poll_options WHERE id = $1`, optionID).Scan(&optionPoll)
	if errors.Is(err, pgx.ErrNoRows) || optionPoll != pollID {
		return CastResult{}, ErrBadOption
	}
	if err != nil {
		return CastResult{}, err
	}

	var result CastResult
	err = tx.QueryRow(ctx, `
		INSERT INTO votes (poll_id, option_id, voter_token_id)
		VALUES ($1, $2, $3)
		RETURNING id, poll_id, option_id, cast_at
	`, pollID, optionID, tokenID).Scan(&result.VoteID, &result.PollID, &result.OptionID, &result.CastAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return CastResult{}, ErrAlreadyUsed
		}
		return CastResult{}, fmt.Errorf("insert vote: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE poll_result_cache
		SET vote_count = vote_count + 1, updated_at = now()
		WHERE poll_id = $1 AND option_id = $2
	`, pollID, optionID)
	if err != nil {
		return CastResult{}, fmt.Errorf("update cache: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return CastResult{}, fmt.Errorf("poll_result_cache row missing for option")
	}

	_, err = tx.Exec(ctx, `
		UPDATE voting_tokens SET redeemed_at = now() WHERE id = $1
	`, tokenID)
	if err != nil {
		return CastResult{}, fmt.Errorf("redeem token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return CastResult{}, err
	}
	return result, nil
}
