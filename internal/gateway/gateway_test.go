package gateway

import (
	"strings"
	"testing"

	"orcaterm2api/internal/config"
	"orcaterm2api/internal/session"
)

func TestResolveModelAlias(t *testing.T) {
	c := config.Default()
	c.Normalize()
	m, ok := ResolveModel(c, "gpt-4o")
	if !ok || m.Upstream != "deepseek-v4.1-flash" {
		t.Fatalf("alias resolved to %+v ok=%v", m, ok)
	}
	m, ok = ResolveModel(c, "qwen3")
	if !ok || m.ID != "qwen3" {
		t.Fatalf("direct model = %+v", m)
	}
	if _, ok := ResolveModel(c, "bad/model"); ok {
		t.Fatal("bad model should be rejected")
	}
}

func TestBuildPromptSystemPrefix(t *testing.T) {
	c := config.Default()
	c.Normalize()
	_, hist, last, chars, err := buildPrompt(c, []Message{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "第一问"},
		{Role: "assistant", Content: "第一答"},
		{Role: "user", Content: "第二问"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last, "你是助手") || !strings.Contains(last, "第二问") {
		t.Fatalf("last = %q", last)
	}
	if len(hist) != 2 || hist[0].Type != "human" || hist[1].Type != "ai" {
		t.Fatalf("history = %+v", hist)
	}
	if chars == 0 {
		t.Fatal("prompt chars = 0")
	}
}

func TestBuildPromptTrimsHistory(t *testing.T) {
	c := config.Default()
	c.Conversation.MaxHistoryMessages = 2
	c.Conversation.MaxHistoryChars = 10
	_, hist, _, _, err := buildPrompt(c, []Message{
		{Role: "user", Content: "1234567890"},
		{Role: "assistant", Content: "abcdefghij"},
		{Role: "user", Content: "最后"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("history len = %d (%+v)", len(hist), hist)
	}
	if hist[0].Content != "abcdefghij" {
		t.Fatalf("history = %+v", hist)
	}
}

func TestBuildPromptNoUser(t *testing.T) {
	c := config.Default()
	_, _, _, _, err := buildPrompt(c, []Message{{Role: "system", Content: "x"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildPromptDropsSystem(t *testing.T) {
	c := config.Default()
	c.Conversation.SystemPromptMode = "drop"
	_, _, last, _, err := buildPrompt(c, []Message{
		{Role: "system", Content: "SECRET"},
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(last, "SECRET") {
		t.Fatalf("system leaked: %q", last)
	}
}

func TestSessionKeyStable(t *testing.T) {
	a := session.Key("sys", "hello", "u1")
	b := session.Key("sys", "hello", "u1")
	if a != b || a == "" {
		t.Fatalf("key unstable: %q %q", a, b)
	}
	if a == session.Key("sys", "hello", "u2") {
		t.Fatal("different user should differ")
	}
}



