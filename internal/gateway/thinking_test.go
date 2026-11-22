package gateway

import (
	"testing"

	"orcaterm2api/internal/upstream"
)

func TestThinkingModelDesc(t *testing.T) {
	spec := upstream.ModelSpec{
		ShowThinking: true,
		ThinkingLevels: []upstream.ThinkingLevel{
			{Label: "低", Value: "low", RequestParams: map[string]any{"reasoning_effort": "low"}},
			{Label: "高", Value: "high", Default: true, RequestParams: map[string]any{"reasoning_effort": "high"}},
			{Label: "超高", Value: "max", RequestParams: map[string]any{"reasoning_effort": "max"}},
			{Label: "关闭", Value: "off", RequestParams: map[string]any{"thinking": map[string]any{"type": "disabled"}}},
		},
	}
	d := thinkingModelDesc(spec, ChatInput{})
	kw, _ := d["kwargs"].(map[string]any)
	if kw["reasoning_effort"] != "high" {
		t.Fatalf("default = %+v", d)
	}
	d = thinkingModelDesc(spec, ChatInput{ReasoningEffort: "max"})
	kw, _ = d["kwargs"].(map[string]any)
	if kw["reasoning_effort"] != "max" {
		t.Fatalf("max = %+v", d)
	}
	d = thinkingModelDesc(spec, ChatInput{ThinkingType: "disabled"})
	kw, _ = d["kwargs"].(map[string]any)
	if _, ok := kw["thinking"]; !ok {
		t.Fatalf("off = %+v", d)
	}
	if thinkingModelDesc(upstream.ModelSpec{ShowThinking: false}, ChatInput{}) != nil {
		t.Fatal("non-thinking model should return nil")
	}
}

