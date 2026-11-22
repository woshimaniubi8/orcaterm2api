package upstream

import (
	"strings"
	"testing"
)

func feedAll(ex *Extractor, chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		for _, d := range ex.Feed(c) {
			b.WriteString(d.Content)
		}
	}
	return b.String()
}

func TestExtractorStreamsTaskCompletion(t *testing.T) {
	raw := `{"action":"completion","thinking":"用户在问候","taskCompletion":"你好，我是 OrcaTerm AI。\n请多关照。"}`
	ex := NewExtractor()
	// 按 1 字符切片，模拟上游逐字下发。
	var chunks []string
	for _, r := range raw {
		chunks = append(chunks, string(r))
	}
	got := feedAll(ex, chunks...)
	want := "你好，我是 OrcaTerm AI。\n请多关照。"
	if got != want {
		t.Fatalf("streamed content mismatch:\n got=%q\nwant=%q", got, want)
	}
	res := ex.Finish()
	if res.Action != "completion" {
		t.Fatalf("action = %q", res.Action)
	}
	if res.Content != want {
		t.Fatalf("final content = %q", res.Content)
	}
	if !res.Streamed {
		t.Fatalf("expected Streamed=true")
	}
	if res.Reasoning != "用户在问候" {
		t.Fatalf("reasoning = %q", res.Reasoning)
	}
}

func TestExtractorCodeFenceAndPrefix(t *testing.T) {
	raw := "历史回显：用户说你好。```json\n{\n  \"action\": \"completion\",\n  \"thinking\": \"直接回答\",\n  \"taskCompletion\": \"PONG-123\"\n}\n```"
	ex := NewExtractor()
	got := feedAll(ex, raw[:10], raw[10:30], raw[30:])
	if got != "PONG-123" {
		t.Fatalf("got %q", got)
	}
	res := ex.Finish()
	if res.Content != "PONG-123" {
		t.Fatalf("final = %q", res.Content)
	}
}

func TestExtractorEscapes(t *testing.T) {
	raw := `{"action":"completion","taskCompletion":"line1\nline2\t\"quoted\"\\slash \u4f60\u597d \ud83d\ude00"}`
	ex := NewExtractor()
	got := feedAll(ex, raw[:25], raw[25:60], raw[60:])
	want := "line1\nline2\t\"quoted\"\\slash 你好 😀"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExtractorNestedAndNonTargetStrings(t *testing.T) {
	raw := `{"action":"review","tool":{"name":"taskCompletion","args":{"thinking":"nested"}},"taskCompletion":"真正结果"}`
	ex := NewExtractor()
	got := feedAll(ex, raw)
	if got != "真正结果" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractorPlainTextFallback(t *testing.T) {
	raw := "这是一段普通文本，没有 JSON。"
	ex := NewExtractor()
	got := feedAll(ex, raw)
	if got != "" {
		t.Fatalf("不该在流里吐出非 JSON 文本，got %q", got)
	}
	res := ex.Finish()
	if res.Content != raw {
		t.Fatalf("fallback = %q", res.Content)
	}
	if res.Streamed {
		t.Fatalf("fallback 不应标记 Streamed")
	}
}

func TestExtractorAskAction(t *testing.T) {
	raw := `{"action":"ask","question":"要部署到哪个区域？","options":["广州","上海"]}`
	ex := NewExtractor()
	feedAll(ex, raw)
	res := ex.Finish()
	if res.Action != "ask" {
		t.Fatalf("action=%q", res.Action)
	}
	if !strings.Contains(res.Content, "要部署到哪个区域？") || !strings.Contains(res.Content, "广州") {
		t.Fatalf("content=%q", res.Content)
	}
}
