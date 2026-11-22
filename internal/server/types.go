package server

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

func cryptoRandRead(b []byte) (int, error) { return rand.Read(b) }

// flexContent 兼容 OpenAI/Anthropic 的 content：字符串或内容块数组。
type flexContent string

func (c *flexContent) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*c = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = flexContent(s)
		return nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &blocks); err == nil {
		var parts []string
		for _, blk := range blocks {
			if blk.Text != "" {
				parts = append(parts, blk.Text)
			}
		}
		*c = flexContent(strings.Join(parts, "\n"))
		return nil
	}
	var anyv any
	if err := json.Unmarshal(b, &anyv); err != nil {
		return err
	}
	*c = flexContent(fmt.Sprint(anyv))
	return nil
}

func (c flexContent) MarshalJSON() ([]byte, error) { return json.Marshal(string(c)) }

// chatMessage 是 OpenAI 风格消息。
type chatMessage struct {
	Role    string      `json:"role"`
	Content flexContent `json:"content"`
	Name    string      `json:"name,omitempty"`
}

// chatCompletionRequest 是 /v1/chat/completions 请求。
type chatCompletionRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Stream         bool          `json:"stream"`
	MaxTokens      int           `json:"max_tokens"`
	MaxCompletion  int           `json:"max_completion_tokens"`
	Temperature    *float64      `json:"temperature"`
	TopP           *float64      `json:"top_p"`
	User           string        `json:"user"`
	ConversationID string        `json:"conversation_id"`
	StreamOptions  *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Metadata map[string]any `json:"metadata"`
	// ReasoningEffort / ThinkingLevel 是扩展字段：low / high / max / off。
	ReasoningEffort string `json:"reasoning_effort"`
	ThinkingLevel   string `json:"thinking_level"`
	Thinking        *thinkingSpec `json:"thinking"`
}

// chatCompletionResponse 是非流式响应。
type chatCompletionResponse struct {
	ID                string                 `json:"id"`
	Object            string                 `json:"object"`
	Created           int64                  `json:"created"`
	Model             string                 `json:"model"`
	SystemFingerprint string                 `json:"system_fingerprint,omitempty"`
	Choices           []chatCompletionChoice `json:"choices"`
	Usage             usageInfo              `json:"usage"`
}

type chatCompletionChoice struct {
	Index        int             `json:"index"`
	Message      chatRespMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type chatRespMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type usageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// chatCompletionChunk 是流式块。
type chatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usageInfo    `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int          `json:"index"`
	Delta        chunkDelta   `json:"delta"`
	FinishReason *string      `json:"finish_reason"`
}

type chunkDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// thinkingSpec 是 OpenAI 扩展的 thinking 对象。
type thinkingSpec struct {
	Type string `json:"type"`
}
