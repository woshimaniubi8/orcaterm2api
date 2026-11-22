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
	"orcaterm2api/internal/pool"
	"orcaterm2api/internal/reqlog"
	"orcaterm2api/internal/upstream"
)

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持 POST")
		return
	}
	started := time.Now()
	var req chatCompletionRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_json", "请求体不是合法 JSON: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "messages 不能为空")
		return
	}
	msgs := make([]gateway.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, gateway.Message{Role: m.Role, Content: string(m.Content)})
	}
	in := gateway.ChatInput{
		Model:            req.Model,
		Messages:         msgs,
		Stream:           req.Stream,
		UserKey:          req.User,
		ConversationHint: firstNonEmpty(r.Header.Get("X-Orca-Conversation"), req.ConversationID),
		MaxTokens:        maxInt(req.MaxTokens, req.MaxCompletion),
		ReasoningEffort:  firstNonEmpty(req.ReasoningEffort, req.ThinkingLevel),
		ThinkingType:     thinkingType(req.Thinking),
	}
	entry := reqlog.Entry{
		ID:         newID("req_"),
		Time:       started.UnixMilli(),
		Endpoint:   "/v1/chat/completions",
		Method:     r.Method,
		Model:      req.Model,
		Stream:     req.Stream,
		ClientIP:   clientIP(r),
		UserAgent:  r.UserAgent(),
		APIKey:     maskAPIKey(extractAPIKey(r)),
		Prompt:     lastUserText(msgs),
	}
	defer func() { s.logs.Add(entry) }()

	out, err := s.gw.Complete(r.Context(), in)
	if err != nil {
		status, code, msg := mapError(err)
		entry.Status = status
		entry.Error = err.Error()
		entry.DurationMS = time.Since(started).Milliseconds()
		writeOpenAIError(w, status, code, msg)
		return
	}
	entry.Account = out.Lease.Account().ID
	entry.Model = out.Model
	entry.UpstreamModel = out.UpstreamModel
	entry.Conversation = out.ConversationID

	if !req.Stream {
		content, reasoning, uerr := drain(out)
		out.Lease.Release(uerr)
		entry.Status = http.StatusOK
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
			writeOpenAIError(w, status, code, msg)
			return
		}
		pt := estimateTokens(entry.Prompt)
		ct := estimateTokens(content) + estimateTokens(reasoning)
		writeJSON(w, http.StatusOK, chatCompletionResponse{
			ID:      newID("chatcmpl-"),
			Object:  "chat.completion",
			Created: out.Created,
			Model:   out.Model,
			Choices: []chatCompletionChoice{{
				Index:        0,
				Message:      chatRespMessage{Role: "assistant", Content: content, ReasoningContent: reasoning},
				FinishReason: "stop",
			}},
			Usage: usageInfo{PromptTokens: pt, CompletionTokens: ct, TotalTokens: pt + ct},
		})
		return
	}

	s.streamChat(w, r, out, &entry, started)
}

// streamChat 负责 OpenAI SSE 输出。
func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, out *gateway.ChatOutput, entry *reqlog.Entry, started time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		out.Lease.Release(errors.New("streaming unsupported"))
		writeOpenAIError(w, http.StatusInternalServerError, "stream_unsupported", "服务器不支持流式响应")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Orca-Conversation", out.ConversationID)
	w.WriteHeader(http.StatusOK)

	id := newID("chatcmpl-")
	created := out.Created
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	send(chatCompletionChunk{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: out.Model,
		Choices: []chunkChoice{{Index: 0, Delta: chunkDelta{Role: "assistant"}}},
	})

	var firstByte time.Time
	emit := func(d upstream.Delta) {
		if firstByte.IsZero() {
			firstByte = time.Now()
			entry.TTFBMS = firstByte.Sub(started).Milliseconds()
		}
		if d.Reasoning != "" {
			send(chatCompletionChunk{
				ID: id, Object: "chat.completion.chunk", Created: created, Model: out.Model,
				Choices: []chunkChoice{{Index: 0, Delta: chunkDelta{ReasoningContent: d.Reasoning}}},
			})
		}
		if d.Content != "" {
			entry.CompletionChars += len([]rune(d.Content))
			entry.Completion += d.Content
			send(chatCompletionChunk{
				ID: id, Object: "chat.completion.chunk", Created: created, Model: out.Model,
				Choices: []chunkChoice{{Index: 0, Delta: chunkDelta{Content: d.Content}}},
			})
		}
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
		if frame.ReasoningContent != "" {
			emit(upstream.Delta{Reasoning: frame.ReasoningContent})
		}
		for _, d := range out.Extractor.Feed(frame.Content) {
			emit(d)
		}
	}
	final := out.Extractor.Finish()
	if streamErr == nil && !final.Streamed && strings.TrimSpace(final.Content) != "" {
		emit(upstream.Delta{Content: final.Content})
	}
	out.Stream.Close()
	if streamErr != nil {
		if errors.Is(streamErr, context.Canceled) {
			// 客户端主动断开不应惩罚账号。
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
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "upstream_error", "code": code}})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	out.Lease.ReleaseOK()
	entry.Status = http.StatusOK
	entry.DurationMS = time.Since(started).Milliseconds()

	stop := "stop"
	send(chatCompletionChunk{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: out.Model,
		Choices: []chunkChoice{{Index: 0, Delta: chunkDelta{}, FinishReason: &stop}},
	})
	if entry.TTFBMS == 0 {
		entry.TTFBMS = entry.DurationMS
	}
	pt := estimateTokens(entry.Prompt)
	ct := estimateTokens(entry.Completion)
	send(chatCompletionChunk{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: out.Model,
		Choices: []chunkChoice{}, Usage: &usageInfo{PromptTokens: pt, CompletionTokens: ct, TotalTokens: pt + ct},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// drain 读完整条流并返回最终文本。
func drain(out *gateway.ChatOutput) (string, string, error) {
	defer out.Stream.Close()
	for {
		frame, err := out.Stream.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", "", err
		}
		out.Extractor.Feed(frame.Content)
	}
	res := out.Extractor.Finish()
	return res.Content, res.Reasoning, nil
}

func lastUserText(msgs []gateway.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.EqualFold(msgs[i].Role, "user") {
			return msgs[i].Content
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// mapError 把上游错误映射成 HTTP 状态与错误码。
func mapError(err error) (int, string, string) {
	if err == nil {
		return http.StatusOK, "", ""
	}
	if ae, ok := upstream.AsAPIError(err); ok {
		switch ae.Kind {
		case upstream.KindAuth:
			return http.StatusServiceUnavailable, "upstream_auth", "上游账号凭证失效，请刷新或重新登录：" + ae.Message
		case upstream.KindModel:
			return http.StatusNotFound, "model_not_found", "该账号无权使用此模型：" + ae.Message
		case upstream.KindRate:
			return http.StatusTooManyRequests, "rate_limit_exceeded", "上游限流，请稍后重试：" + ae.Message
		case upstream.KindRequest:
			return http.StatusBadRequest, "invalid_request", ae.Message
		case upstream.KindNetwork:
			return http.StatusGatewayTimeout, "upstream_timeout", "上游网络超时：" + ae.Message
		default:
			return http.StatusBadGateway, "upstream_error", "上游错误：" + ae.Message
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout", "请求超时"
	case errors.Is(err, context.Canceled):
		return 499, "canceled", "客户端取消了请求"
	case errors.Is(err, pool.ErrNoAccount):
		return http.StatusServiceUnavailable, "no_account", "没有可用账号（全部冷却或已禁用）"
	case errors.Is(err, pool.ErrAllCooling):
		return http.StatusServiceUnavailable, "all_cooling", "全部账号正在冷却，请稍后再试"
	case errors.Is(err, pool.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited", "账号请求过于频繁，请稍后再试"
	default:
		return http.StatusBadGateway, "upstream_error", err.Error()
	}
}
