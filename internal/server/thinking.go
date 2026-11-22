package server

import "strings"

// thinkingType 读取 OpenAI 扩展 thinking.type。
func thinkingType(t *thinkingSpec) string {
	if t == nil {
		return ""
	}
	return strings.TrimSpace(t.Type)
}

// anthropicThinkingType 把 Anthropic thinking 映射为网关的 enabled/disabled。
func anthropicThinkingType(t *anthropicThinking) string {
	if t == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "disabled", "off", "none":
		return "disabled"
	case "enabled", "on":
		return "enabled"
	default:
		return ""
	}
}

// maxEffort 把 Anthropic thinking.budget_tokens 映射成强度档位。
func maxEffort(t *anthropicThinking) string {
	if t == nil || strings.EqualFold(t.Type, "disabled") {
		return ""
	}
	switch {
	case t.BudgetTokens >= 8000:
		return "max"
	case t.BudgetTokens >= 2000:
		return "high"
	case t.BudgetTokens > 0:
		return "low"
	}
	return ""
}
