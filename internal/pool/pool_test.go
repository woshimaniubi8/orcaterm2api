package pool

import (
	"errors"
	"testing"
	"time"

	"orcaterm2api/internal/account"
	"orcaterm2api/internal/config"
	"orcaterm2api/internal/upstream"
)

func newTestPool(t *testing.T, n int) *Pool {
	t.Helper()
	dir := t.TempDir()
	for i := 1; i <= n; i++ {
		a := &account.Account{
			ID:          string(rune('0' + i)),
			Name:        "acct",
			AccessToken: "token-" + string(rune('0'+i)),
			ExpiresAt:   time.Now().Add(2 * time.Hour).UnixMilli(),
			Enabled:     true,
			CreatedAt:   time.Now().UnixMilli(),
		}
		if err := account.Save(dir, a); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default().Pool
	cfg.MaxInFlight = 1
	cfg.MinInterval = config.Duration(time.Second)
	cfg.MaxPerMinute = 10
	p := New(dir, cfg)
	if err := p.Load(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPickRespectsInFlight(t *testing.T) {
	p := newTestPool(t, 1)
	l, err := p.Pick("qwen3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Pick("qwen3", nil); !errors.Is(err, ErrRateLimited) && !errors.Is(err, ErrNoAccount) {
		t.Fatalf("second pick err = %v", err)
	}
	l.ReleaseOK()
}

func TestRateLimit(t *testing.T) {
	p := newTestPool(t, 1)
	cfg := p.cfg
	cfg.MinInterval = config.Duration(5 * time.Second)
	cfg.MaxPerMinute = 1
	p.Configure(cfg)

	l1, err := p.Pick("qwen3", nil)
	if err != nil {
		t.Fatal(err)
	}
	l1.ReleaseOK()
	if _, err := p.Pick("qwen3", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limited, got %v", err)
	}
}

func TestModelBlock(t *testing.T) {
	p := newTestPool(t, 1)
	l, err := p.Pick("qwen3", nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Release(&upstream.APIError{Kind: upstream.KindModel, Message: "no access"})
	if _, err := p.Pick("qwen3", nil); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("expected no account for blocked model, got %v", err)
	}
}

func TestCooldownOnProviderError(t *testing.T) {
	p := newTestPool(t, 1)
	cfg := p.cfg
	cfg.MinInterval = 0
	cfg.SoftCooldown = config.Duration(2 * time.Second)
	p.Configure(cfg)
	l, err := p.Pick("qwen3", nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Release(&upstream.APIError{Kind: upstream.KindProvider, Message: "boom"})
	if _, err := p.Pick("qwen3", nil); !errors.Is(err, ErrAllCooling) {
		t.Fatalf("expected all cooling, got %v", err)
	}
	snaps := p.Snapshots()
	if snaps[0].Health != "cooling" {
		t.Fatalf("health = %s", snaps[0].Health)
	}
}

func TestDisableAccount(t *testing.T) {
	p := newTestPool(t, 1)
	if err := p.SetEnabled("1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Pick("qwen3", nil); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("expected no account, got %v", err)
	}
	// 落盘生效。
	if err := p.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Pick("qwen3", nil); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("expected no account after reload, got %v", err)
	}
}

