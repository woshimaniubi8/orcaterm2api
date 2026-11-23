package gateway

import (
	"strings"
	"testing"

	"orcaterm2api/internal/config"
)

func TestPromptPrime(t *testing.T) {
	cfg := config.Default()
	cfg.Normalize()

	prime := promptPrime(cfg)
	if len(prime) != 2 || prime[0].Type != "human" || prime[1].Type != "ai" {
		t.Fatalf("prime=%+v", prime)
	}
	if !strings.Contains(prime[0].Content, "通用 AI 助手") {
		t.Fatalf("neutral prompt=%q", prime[0].Content)
	}

	cfg.Prompt.Mode = "upstream"
	if got := promptPrime(cfg); got != nil {
		t.Fatalf("upstream should not prime: %+v", got)
	}

	cfg.Prompt.Mode = "custom"
	cfg.Prompt.Text = "你是测试机器人"
	cfg.Prompt.Ack = "收到"
	prime = promptPrime(cfg)
	if len(prime) != 2 || prime[0].Content != "你是测试机器人" || prime[1].Content != "收到" {
		t.Fatalf("custom=%+v", prime)
	}

	off := false
	cfg.Prompt.Mode = "neutral"
	cfg.Prompt.Prime = &off
	if got := promptPrime(cfg); got != nil {
		t.Fatalf("prime off should be nil: %+v", got)
	}
}
