package polls

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Zubimendi/vote-guard/internal/votes"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("not_found")
)

type OptionInput struct {
	Label        string `json:"label"`
	DisplayOrder int    `json:"display_order"`
}

type CreatePollInput struct {
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      string        `json:"status"`
	Options     []OptionInput `json:"options"`
}

type Option struct {
	ID           uuid.UUID `json:"id"`
	Label        string    `json:"label"`
	DisplayOrder int       `json:"display_order"`
}

type Poll struct {
	ID          uuid.UUID `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	OpensAt     *time.Time `json:"opens_at,omitempty"`
	ClosesAt    *time.Time `json:"closes_at,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Options     []Option  `json:"options"`
}

type IssuedToken struct {
	IdentityRef string    `json:"identity_ref"`
	Token       string    `json:"token"`
	TokenID     uuid.UUID `json:"token_id"`
}

type TallyOption struct {
	OptionID     uuid.UUID `json:"option_id"`
	Label        string    `json:"label"`
	DisplayOrder int       `json:"display_order"`
	VoteCount    int64     `json:"vote_count"`
}

type Results struct {
	PollID   uuid.UUID     `json:"poll_id"`
	Status   string        `json:"status"`
	Official bool          `json:"official"`
	Source   string        `json:"source"`
	Options  []TallyOption `json:"options"`
}

type ReconciliationRun struct {
	ID            uuid.UUID `json:"id"`
	PollID        uuid.UUID `json:"poll_id"`
	OptionID      uuid.UUID `json:"option_id"`
	CachedCount   int64     `json:"cached_count"`
	VerifiedCount int64     `json:"verified_count"`
	Drift         int64     `json:"drift"`
	CheckedAt     time.Time `json:"checked_at"`
}

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

func (s *Service) Create(ctx context.Context, in CreatePollInput) (Poll, error) {
	if in.Title == "" {
		return Poll{}, fmt.Errorf("title required")
	}
	if len(in.Options) == 0 {
		return Poll{}, fmt.Errorf("at least one option required")
	}
	status := in.Status
	if status == "" {
		status = "OPEN"
	}
	if status != "DRAFT" && status != "OPEN" && status != "CLOSED" {
		return Poll{}, fmt.Errorf("invalid status")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Poll{}, err
	}
	defer tx.Rollback(ctx)

	var p Poll
	err = tx.QueryRow(ctx, `
		INSERT INTO polls (title, description, status)
		VALUES ($1, $2, $3)
		RETURNING id, title, description, status, opens_at, closes_at, created_at
	`, in.Title, in.Description, status).Scan(
		&p.ID, &p.Title, &p.Description, &p.Status, &p.OpensAt, &p.ClosesAt, &p.CreatedAt,
	)
	if err != nil {
		return Poll{}, err
	}

	for i, opt := range in.Options {
		order := opt.DisplayOrder
		if order == 0 {
			order = i + 1
		}
		var o Option
		err = tx.QueryRow(ctx, `
			INSERT INTO poll_options (poll_id, label, display_order)
			VALUES ($1, $2, $3)
			RETURNING id, label, display_order
		`, p.ID, opt.Label, order).Scan(&o.ID, &o.Label, &o.DisplayOrder)
		if err != nil {
			return Poll{}, err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO poll_result_cache (poll_id, option_id, vote_count)
			VALUES ($1, $2, 0)
		`, p.ID, o.ID)
		if err != nil {
			return Poll{}, err
		}
		p.Options = append(p.Options, o)
	}

	if err := tx.Commit(ctx); err != nil {
		return Poll{}, err
	}
	return p, nil
}

func (s *Service) Close(ctx context.Context, pollID uuid.UUID) (Poll, error) {
	var p Poll
	err := s.pool.QueryRow(ctx, `
		UPDATE polls
		SET status = 'CLOSED', closes_at = COALESCE(closes_at, now())
		WHERE id = $1
		RETURNING id, title, description, status, opens_at, closes_at, created_at
	`, pollID).Scan(&p.ID, &p.Title, &p.Description, &p.Status, &p.OpensAt, &p.ClosesAt, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Poll{}, ErrNotFound
	}
	if err != nil {
		return Poll{}, err
	}
	opts, err := s.loadOptions(ctx, pollID)
	if err != nil {
		return Poll{}, err
	}
	p.Options = opts
	return p, nil
}

func (s *Service) IssueTokens(ctx context.Context, pollID uuid.UUID, identityRefs []string) ([]IssuedToken, error) {
	if len(identityRefs) == 0 {
		return nil, fmt.Errorf("identity_refs required")
	}

	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM polls WHERE id = $1)`, pollID).Scan(&exists)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	out := make([]IssuedToken, 0, len(identityRefs))
	for _, ref := range identityRefs {
		raw, err := generateToken()
		if err != nil {
			return nil, err
		}
		hash := votes.HashToken(raw)
		var tokenID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO voting_tokens (poll_id, token_hash, issued_to_identity_ref)
			VALUES ($1, $2, $3)
			RETURNING id
		`, pollID, hash, ref).Scan(&tokenID)
		if err != nil {
			return nil, err
		}
		out = append(out, IssuedToken{
			IdentityRef: ref,
			Token:       raw,
			TokenID:     tokenID,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) LiveResults(ctx context.Context, pollID uuid.UUID) (Results, error) {
	status, err := s.pollStatus(ctx, pollID)
	if err != nil {
		return Results{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.label, o.display_order, c.vote_count
		FROM poll_options o
		JOIN poll_result_cache c ON c.option_id = o.id AND c.poll_id = o.poll_id
		WHERE o.poll_id = $1
		ORDER BY o.display_order, o.label
	`, pollID)
	if err != nil {
		return Results{}, err
	}
	defer rows.Close()

	var options []TallyOption
	for rows.Next() {
		var t TallyOption
		if err := rows.Scan(&t.OptionID, &t.Label, &t.DisplayOrder, &t.VoteCount); err != nil {
			return Results{}, err
		}
		options = append(options, t)
	}
	return Results{
		PollID:   pollID,
		Status:   status,
		Official: false,
		Source:   "poll_result_cache",
		Options:  options,
	}, rows.Err()
}

func (s *Service) VerifiedResults(ctx context.Context, pollID uuid.UUID) (Results, error) {
	status, err := s.pollStatus(ctx, pollID)
	if err != nil {
		return Results{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.label, o.display_order, COALESCE(v.cnt, 0)
		FROM poll_options o
		LEFT JOIN (
			SELECT option_id, COUNT(*)::bigint AS cnt
			FROM votes
			WHERE poll_id = $1
			GROUP BY option_id
		) v ON v.option_id = o.id
		WHERE o.poll_id = $1
		ORDER BY o.display_order, o.label
	`, pollID)
	if err != nil {
		return Results{}, err
	}
	defer rows.Close()

	var options []TallyOption
	for rows.Next() {
		var t TallyOption
		if err := rows.Scan(&t.OptionID, &t.Label, &t.DisplayOrder, &t.VoteCount); err != nil {
			return Results{}, err
		}
		options = append(options, t)
	}
	return Results{
		PollID:   pollID,
		Status:   status,
		Official: status == "CLOSED",
		Source:   "votes",
		Options:  options,
	}, rows.Err()
}

func (s *Service) ListReconciliation(ctx context.Context, pollID uuid.UUID) ([]ReconciliationRun, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM polls WHERE id = $1)`, pollID).Scan(&exists)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, poll_id, option_id, cached_count, verified_count, drift, checked_at
		FROM reconciliation_runs
		WHERE poll_id = $1
		ORDER BY checked_at DESC, option_id
	`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []ReconciliationRun
	for rows.Next() {
		var r ReconciliationRun
		if err := rows.Scan(&r.ID, &r.PollID, &r.OptionID, &r.CachedCount, &r.VerifiedCount, &r.Drift, &r.CheckedAt); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	if runs == nil {
		runs = []ReconciliationRun{}
	}
	return runs, rows.Err()
}

func (s *Service) pollStatus(ctx context.Context, pollID uuid.UUID) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM polls WHERE id = $1`, pollID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

func (s *Service) loadOptions(ctx context.Context, pollID uuid.UUID) ([]Option, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, label, display_order FROM poll_options
		WHERE poll_id = $1 ORDER BY display_order, label
	`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var opts []Option
	for rows.Next() {
		var o Option
		if err := rows.Scan(&o.ID, &o.Label, &o.DisplayOrder); err != nil {
			return nil, err
		}
		opts = append(opts, o)
	}
	return opts, rows.Err()
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
