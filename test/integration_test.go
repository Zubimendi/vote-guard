package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zubimendi/vote-guard/internal/db"
	"github.com/Zubimendi/vote-guard/internal/polls"
	"github.com/Zubimendi/vote-guard/internal/reconcile"
	"github.com/Zubimendi/vote-guard/internal/votes"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://voteguard:voteguard@localhost:5432/voteguard?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	migDir := filepath.Join(repoRoot(t), "migrations")
	if err := db.Migrate(ctx, pool, migDir); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func createOpenPoll(t *testing.T, pollSvc *polls.Service) (polls.Poll, string) {
	t.Helper()
	p, err := pollSvc.Create(context.Background(), polls.CreatePollInput{
		Title:       "test-" + uuid.NewString(),
		Description: "integration",
		Status:      "OPEN",
		Options: []polls.OptionInput{
			{Label: "A", DisplayOrder: 1},
			{Label: "B", DisplayOrder: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := pollSvc.IssueTokens(context.Background(), p.ID, []string{"voter-1"})
	if err != nil {
		t.Fatal(err)
	}
	return p, tokens[0].Token
}

func TestSingleUseToken(t *testing.T) {
	pool := testPool(t)
	pollSvc := polls.NewService(pool)
	voteSvc := votes.NewService(pool)

	p, raw := createOpenPoll(t, pollSvc)
	opt := p.Options[0].ID

	if _, err := voteSvc.CastRedeem(context.Background(), p.ID, opt, raw); err != nil {
		t.Fatalf("first cast: %v", err)
	}
	_, err := voteSvc.CastRedeem(context.Background(), p.ID, opt, raw)
	if !errors.Is(err, votes.ErrAlreadyUsed) {
		t.Fatalf("expected already_used, got %v", err)
	}

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM votes WHERE poll_id = $1`, p.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want 1 vote, got %d", count)
	}
}

func TestConcurrentSameToken(t *testing.T) {
	pool := testPool(t)
	pollSvc := polls.NewService(pool)
	voteSvc := votes.NewService(pool)

	p, raw := createOpenPoll(t, pollSvc)
	opt := p.Options[0].ID

	const n = 32
	var okCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := voteSvc.CastRedeem(context.Background(), p.ID, opt, raw)
			if err == nil {
				okCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if okCount.Load() != 1 {
		t.Fatalf("want exactly 1 success, got %d", okCount.Load())
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM votes WHERE poll_id = $1`, p.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want 1 vote row, got %d", count)
	}
}

func TestVotesImmutable(t *testing.T) {
	pool := testPool(t)
	pollSvc := polls.NewService(pool)
	voteSvc := votes.NewService(pool)

	p, raw := createOpenPoll(t, pollSvc)
	res, err := voteSvc.CastRedeem(context.Background(), p.ID, p.Options[0].ID, raw)
	if err != nil {
		t.Fatal(err)
	}

	_, err = pool.Exec(context.Background(), `UPDATE votes SET option_id = $1 WHERE id = $2`, p.Options[1].ID, res.VoteID)
	if err == nil {
		t.Fatal("expected UPDATE to fail")
	}
	_, err = pool.Exec(context.Background(), `DELETE FROM votes WHERE id = $1`, res.VoteID)
	if err == nil {
		t.Fatal("expected DELETE to fail")
	}
}

func TestForgedTokenRejected(t *testing.T) {
	pool := testPool(t)
	pollSvc := polls.NewService(pool)
	voteSvc := votes.NewService(pool)

	p, _ := createOpenPoll(t, pollSvc)
	_, err := voteSvc.CastRedeem(context.Background(), p.ID, p.Options[0].ID, "forged-token-value")
	if !errors.Is(err, votes.ErrInvalidToken) {
		t.Fatalf("expected invalid_token, got %v", err)
	}
}

func TestCacheMatchesVerified(t *testing.T) {
	pool := testPool(t)
	pollSvc := polls.NewService(pool)
	voteSvc := votes.NewService(pool)

	p, err := pollSvc.Create(context.Background(), polls.CreatePollInput{
		Title:  "tally-" + uuid.NewString(),
		Status: "OPEN",
		Options: []polls.OptionInput{
			{Label: "A", DisplayOrder: 1},
			{Label: "B", DisplayOrder: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	refs := []string{"a", "b", "c"}
	tokens, err := pollSvc.IssueTokens(context.Background(), p.ID, refs)
	if err != nil {
		t.Fatal(err)
	}
	for i, tok := range tokens {
		opt := p.Options[i%2].ID
		if _, err := voteSvc.CastRedeem(context.Background(), p.ID, opt, tok.Token); err != nil {
			t.Fatal(err)
		}
	}

	live, err := pollSvc.LiveResults(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := pollSvc.VerifiedResults(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Options) != len(verified.Options) {
		t.Fatalf("option length mismatch")
	}
	for i := range live.Options {
		if live.Options[i].VoteCount != verified.Options[i].VoteCount {
			t.Fatalf("mismatch option %s: live=%d verified=%d",
				live.Options[i].OptionID, live.Options[i].VoteCount, verified.Options[i].VoteCount)
		}
	}
}

func TestReconcileDetectsDrift(t *testing.T) {
	pool := testPool(t)
	pollSvc := polls.NewService(pool)
	voteSvc := votes.NewService(pool)
	recon := reconcile.NewService(pool, nil)

	p, raw := createOpenPoll(t, pollSvc)
	if _, err := voteSvc.CastRedeem(context.Background(), p.ID, p.Options[0].ID, raw); err != nil {
		t.Fatal(err)
	}

	_, err := pool.Exec(context.Background(), `
		UPDATE poll_result_cache SET vote_count = vote_count + 5
		WHERE poll_id = $1 AND option_id = $2
	`, p.ID, p.Options[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := recon.RunPoll(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}

	var drift int64
	err = pool.QueryRow(context.Background(), `
		SELECT drift FROM reconciliation_runs
		WHERE poll_id = $1 AND option_id = $2
		ORDER BY checked_at DESC LIMIT 1
	`, p.ID, p.Options[0].ID).Scan(&drift)
	if err != nil {
		t.Fatal(err)
	}
	if drift != 5 {
		t.Fatalf("want drift 5, got %d", drift)
	}

	verified, err := pollSvc.VerifiedResults(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range verified.Options {
		if o.OptionID == p.Options[0].ID && o.VoteCount != 1 {
			t.Fatalf("verified should still be 1, got %d", o.VoteCount)
		}
	}
}
