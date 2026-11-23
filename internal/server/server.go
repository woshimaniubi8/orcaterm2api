// Package server 提供 OpenAI / Anthropic 兼容的 HTTP 接口。
package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"orcaterm2api/internal/config"
	"orcaterm2api/internal/gateway"
	"orcaterm2api/internal/reqlog"
	"orcaterm2api/internal/version"
)

// Server 是 HTTP 层。
type Server struct {
	cfg   *config.Manager
	gw    *gateway.Gateway
	logs  *reqlog.Log
	start time.Time
}

// New 构造 Server。
func New(cfg *config.Manager, gw *gateway.Gateway, logs *reqlog.Log) *Server {
	return &Server{cfg: cfg, gw: gw, logs: logs, start: time.Now()}
}

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	// /v1/models 公开：模型目录不含敏感信息，sub2api 等上游同步工具经常不带鉴权。
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/models/", s.handleModels)
	mux.HandleFunc("/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.withAuth(s.handleChat))
	mux.HandleFunc("/v1/messages", s.withAuth(s.handleMessages))
	mux.HandleFunc("/v1/messages/count_tokens", s.withAuth(s.handleCountTokens))
	return mux
}

// Handle 暴露给面板复用鉴权外的公共处理。
func (s *Server) Handle(path string, h http.HandlerFunc) { http.HandleFunc(path, h) }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	accounts, healthy, cooling, _, _ := s.gw.Pool().Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"service":       "orcaterm2api",
		"version":       version.Version,
		"uptime_seconds": int64(time.Since(s.start).Seconds()),
		"accounts":      accounts,
		"healthy":       healthy,
		"cooling":       cooling,
	})
}

// withAuth 校验 API Key。
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := extractAPIKey(r)
		if key == "" {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "缺少 API Key（Authorization: Bearer …）")
			return
		}
		cfg := s.cfg.Snapshot()
		ok := false
		for _, k := range cfg.APIKeys {
			if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
				ok = true
				break
			}
		}
		if !ok {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "API Key 无效")
			return
		}
		next(w, r)
	}
}

func extractAPIKey(r *http.Request) string {
	if h := strings.TrimSpace(r.Header.Get("Authorization")); h != "" {
		if strings.HasPrefix(strings.ToLower(h), "bearer ") {
			return strings.TrimSpace(h[7:])
		}
		// 兼容不带 Bearer 前缀的客户端
		return h
	}
	for _, name := range []string{"x-api-key", "api-key", "x-auth-token", "x-goog-api-key"} {
		if h := strings.TrimSpace(r.Header.Get(name)); h != "" {
			return h
		}
	}
	q := r.URL.Query()
	for _, name := range []string{"api_key", "key", "token"} {
		if v := strings.TrimSpace(q.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOpenAIError 输出 OpenAI 风格错误。
func writeOpenAIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}

// writeAnthropicError 输出 Anthropic 风格错误。
func writeAnthropicError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    code,
			"message": msg,
		},
	})
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		parts := strings.Split(v, ",")
		return strings.TrimSpace(parts[0])
	}
	if v := r.Header.Get("X-Real-Ip"); v != "" {
		return v
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func maskAPIKey(k string) string {
	if len(k) <= 10 {
		return "***"
	}
	return k[:6] + "..." + k[len(k)-4:]
}

// estimateTokens 按字符类型粗略估算 token（日志/usage 展示用）。
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, other := 0, 0
	for _, r := range s {
		if r >= 0x2E80 {
			cjk++
		} else {
			other++
		}
	}
	n := cjk + other/4
	if n == 0 {
		n = 1
	}
	return n
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = cryptoRandRead(b)
	return fmt.Sprintf("%s%x", prefix, b)
}

// recordUsage 把估算 token 写入请求日志（上游不返回 usage）。
func recordUsage(e *reqlog.Entry, pt, ct int) {
	e.PromptTokens = pt
	e.CompletionTokens = ct
	e.TotalTokens = pt + ct
	if e.DurationMS > 0 {
		e.TokensPerSecond = float64(ct) / (float64(e.DurationMS) / 1000.0)
	}
}
