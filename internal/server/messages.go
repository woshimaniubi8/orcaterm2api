package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"orcaterm2api/internal/gateway"
	"orcaterm2api/internal/reqlog"
)

// anthropicRequest 是 /v1/messages 请求。
type anthropicRequest struct {
	Model         string        `json:"model"`
	MaxTokens     int           `json:"max_tokens"`
	System        flexContent   `json:"system"`
	Messages      []chatMessage `json:"messages"`
	Stream        bool          `json:"stream"`
	Temperature   *float64      `json:"temperature"`
	TopP          *float64      `json:"top_p"`
	StopSequences []string      `json:"stop_sequences"`
	Metadata      map[string]any `json:"metadata"`
	Thinking      *anthropicThinking `json:"thinking"`
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "只支持 POST")
		return
	}
	started := time.Now()
	var req anthropicRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON: "+err.Error())
		return
	}
	msgs := make([]gateway.Message, 0, len(req.Messages)+1)
	if strings.TrimSpace(string(req.System)) != "" {
		msgs = append(msgs, gateway.Message{Role: "system", Content: string(req.System)})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, gateway.Message{Role: m.Role, Content: string(m.Content)})
	}
	if len(req.Messages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "messages 不能为空")
		return
	}
	entry := reqlog.Entry{
		ID:        newID("req_"),
		Time:      started.UnixMilli(),
		Endpoint:  "/v1/messages",
		Method:    r.Method,
		Model:     req.Model,
		Stream:    req.Stream,
		ClientIP:  clientIP(r),
		UserAgent: r.UserAgent(),
		APIKey:    maskAPIKey(extractAPIKey(r)),
		Prompt:    lastUserText(msgs),
	}
	defer func() { s.logs.Add(entry) }()

	out, err := s.gw.Complete(r.Context(), gateway.ChatInput{
		Model:            req.Model,
		Messages:         msgs,
		Stream:           req.Stream,
		UserKey:          firstMetadataString(req.Metadata, "user_id"),
		ConversationHint: r.Header.Get("X-Orca-Conversation"),
		MaxTokens:        req.MaxTokens,
		ReasoningEffort:  maxEffort(req.Thinking),
		ThinkingType:     anthropicThinkingType(req.Thinking),
	})
	if err != nil {
		status, code, msg := mapError(err)
		entry.Status = status
		entry.Error = err.Error()
		entry.DurationMS = time.Since(started).Milliseconds()
		writeAnthropicError(w, status, anthropicErrorType(code), msg)
		return
	}
	entry.Account = out.Lease.Account().ID
	entry.Model = out.Model
	entry.UpstreamModel = out.UpstreamModel
	entry.Conversation = out.ConversationID

	if !req.Stream {
		content, reasoning, uerr := drain(out)
		out.Lease.Release(uerr)
		entry.DurationMS = time.Since(started).Milliseconds()
		entry.TTFBMS = entry.DurationMS
		entry.CompletionChars = len([]rune(content))
		entry.Completion = content
		if uerr != nil {
			if errors.Is(uerr, context.Canceled) {
				out.Lease.ReleaseOK()
				entry.Status = 499
				entry.Error = "client canceled"
				return
			}
			status, code, msg := mapError(uerr)
			entry.Status = status
			entry.Error = uerr.Error()
			writeAnthropicError(w, status, anthropicErrorType(code), msg)
			return
		}
		entry.Status = http.StatusOK
		pt := estimateTokens(entry.Prompt)
		ct := estimateTokens(content) + estimateTokens(reasoning)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":            newID("msg_"),
			"type":          "message",
			"role":          "assistant",
			"model":         out.Model,
			"content":       []map[string]any{{"type": "text", "text": content}},
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]int{"input_tokens": pt, "output_tokens": ct},
		})
		return
	}

	s.streamMessages(w, r, out, &entry, started)
}

func (s *Server) streamMessages(w http.ResponseWriter, r *http.Request, out *gateway.ChatOutput, entry *reqlog.Entry, started time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		out.Lease.Release(errors.New("streaming unsupported"))
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "服务器不支持流式响应")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Orca-Conversation", out.ConversationID)
	w.WriteHeader(http.StatusOK)

	id := newID("msg_")
	event := func(name string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		flusher.Flush()
	}
	event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": out.Model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": estimateTokens(entry.Prompt), "output_tokens": 0},
		},
	})
	event("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})

	var firstByte time.Time
	emit := func(text string) {
		if text == "" {
			return
		}
		if firstByte.IsZero() {
			firstByte = time.Now()
			entry.TTFBMS = firstByte.Sub(started).Milliseconds()
		}
		entry.CompletionChars += len([]rune(text))
		entry.Completion += text
		event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": text},
		})
	}

	var streamErr error
	for {
		if r.Context().Err() != nil {
			streamErr = r.Context().Err()
			break
		}
		frame, err := out.Stream.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			streamErr = err
			break
		}
		for _, d := range out.Extractor.Feed(frame.Content) {
			emit(d.Content)
		}
	}
	final := out.Extractor.Finish()
	if streamErr == nil && !final.Streamed {
		emit(final.Content)
	}
	out.Stream.Close()
	if streamErr != nil {
		if errors.Is(streamErr, context.Canceled) {
			out.Lease.ReleaseOK()
			entry.Status = 499
			entry.Error = "client canceled"
			entry.DurationMS = time.Since(started).Milliseconds()
			return
		}
		out.Lease.Release(streamErr)
		status, code, msg := mapError(streamErr)
		entry.Status = status
		entry.Error = streamErr.Error()
		entry.DurationMS = time.Since(started).Milliseconds()
		event("error", map[string]any{"type": "error", "error": map[string]any{"type": anthropicErrorType(code), "message": msg}})
		return
	}
	out.Lease.ReleaseOK()
	entry.Status = http.StatusOK
	entry.DurationMS = time.Since(started).Milliseconds()
	if entry.TTFBMS == 0 {
		entry.TTFBMS = entry.DurationMS
	}
	event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": estimateTokens(entry.Completion)},
	})
	event("message_stop", map[string]any{"type": "message_stop"})
}

func anthropicErrorType(code string) string {
	switch code {
	case "invalid_request", "model_not_found":
		return "invalid_request_error"
	case "rate_limit_exceeded":
		return "rate_limit_error"
	case "upstream_auth":
		return "authentication_error"
	default:
		return "api_error"
	}
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	var req anthropicRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "请求体不是合法 JSON")
		return
	}
	total := len([]rune(string(req.System)))
	for _, m := range req.Messages {
		total += len([]rune(string(m.Content)))
	}
	writeJSON(w, http.StatusOK, map[string]int{"input_tokens": estimateTokens(strings.Repeat("x", total))})
}

func firstMetadataString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}



// anthropicThinking 是 Anthropic Messages 的 thinking 参数。
type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}
