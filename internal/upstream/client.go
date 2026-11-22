// Package upstream 封装 OrcaTerm / LightAI 的上游协议。
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"io"
	"net/http"
	"strings"
	"time"

	"orcaterm2api/internal/account"
	"orcaterm2api/internal/config"
)

// ErrorKind 用于池子做不同冷却策略。
type ErrorKind string

const (
	KindAuth     ErrorKind = "auth"     // 令牌失效，需要刷新/重新登录
	KindModel    ErrorKind = "model"    // 该账号无此模型权限
	KindRate     ErrorKind = "rate"     // 限流/并发过高
	KindProvider ErrorKind = "provider" // 上游模型服务错误
	KindNetwork  ErrorKind = "network"  // 网络/超时
	KindRequest  ErrorKind = "request"  // 请求本身不合法
)

// APIError 是上游错误的结构化表示。
type APIError struct {
	Kind       ErrorKind
	Code       string
	Message    string
	HTTPStatus int
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (%s): %s", e.Kind, e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

// AsAPIError 提取 *APIError。
func AsAPIError(err error) (*APIError, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

// HistoryMessage 是上游 input.historyMessages 的一条。
type HistoryMessage struct {
	Type    string `json:"type"` // human / ai
	Content string `json:"content"`
}

// File 是上游 input.files 的元素。
type File struct {
	Name     string         `json:"name"`
	MimeType string         `json:"mimeType,omitempty"`
	URI      string         `json:"uri,omitempty"`
	Bytes    string         `json:"bytes,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

// ChatRequest 是一次 assistant/chat 调用。
type ChatRequest struct {
	ConversationID string
	Model          string
	Input          string
	History        []HistoryMessage
	Files          []File
	UserID         string
	// ModelDesc 对应 user.setting.modelDesc（思考强度等 kwargs）。
	ModelDesc map[string]any
	// Setting 可覆盖 user.setting 中的字段（如 enableThinking）。
	Setting map[string]any
}

// Client 是上游 HTTP 客户端。
type Client struct {
	mu   sync.RWMutex
	cfg  config.UpstreamConfig
	http *http.Client
}

// New 构造客户端。
func New(cfg config.UpstreamConfig) *Client {
	return NewWithHTTP(cfg, nil)
}

// NewWithHTTP 允许注入自定义 http.Client（测试用）。
func NewWithHTTP(cfg config.UpstreamConfig, hc *http.Client) *Client {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: time.Duration(cfg.HeaderTimeoutSeconds) * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if hc == nil {
		hc = &http.Client{Transport: tr, Timeout: timeout}
	}
	return &Client{cfg: cfg, http: hc}
}

// Base 返回 assistant 根地址。
func (c *Client) Base() string { return c.Config().AssistantBase }

// Config 返回当前上游配置快照。
func (c *Client) Config() config.UpstreamConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// SetConfig 热更新上游配置。
func (c *Client) SetConfig(cfg config.UpstreamConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
}

func (c *Client) headers(acct *account.Account, stream bool) http.Header {
	cfg := c.Config()
	h := make(http.Header)
	h.Set("X-Product", cfg.Product)
	h.Set("Authorization", "Bearer "+acct.AccessToken)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	if stream {
		h.Set("Accept", "text/event-stream")
	} else {
		h.Set("Accept", "application/json")
	}
	if ua := cfg.UserAgent; ua != "" {
		h.Set("User-Agent", ua)
	}
	h.Set("X-Request-Id", newRequestID())
	return h
}

// Chat 发起一次流式对话。
func (c *Client) Chat(ctx context.Context, acct *account.Account, req ChatRequest) (*Stream, error) {
	cfg := c.Config()
	if strings.TrimSpace(req.Input) == "" {
		return nil, &APIError{Kind: KindRequest, Message: "input 不能为空"}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = cfg.DefaultModel
	}
	setting := defaultSetting(model)
	if len(req.ModelDesc) > 0 {
		setting["modelDesc"] = req.ModelDesc
	}
	for k, v := range req.Setting {
		setting[k] = v
	}
	userID := req.UserID
	if userID == "" {
		userID = acct.ID
	}
	files := req.Files
	if files == nil {
		files = []File{}
	}
	input := map[string]any{
		"id":    newRequestID(),
		"type":  "start",
		"input": req.Input,
		"files": files,
	}
	if len(req.History) > 0 {
		input["historyMessages"] = req.History
	}
	body := map[string]any{
		"conversationId": req.ConversationID,
		"user": map[string]any{
			"id":      userID,
			"setting": setting,
		},
		"input":  input,
		"stream": true,
	}
	raw, _ := json.Marshal(body)
	url := fmt.Sprintf("%s/chat?name=%s&mode=%d&csrfCode=", c.Base(), cfg.AssistantName, cfg.Mode)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	hreq.Header = c.headers(acct, true)
	hreq.Header.Set("x-seqid", newRequestID())
	resp, err := c.http.Do(hreq)
	if err != nil {
		return nil, &APIError{Kind: KindNetwork, Message: err.Error()}
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.Contains(ct, "text/event-stream") {
		defer resp.Body.Close()
		return nil, parseJSONError(resp)
	}
	return newStream(resp.Body), nil
}

func defaultSetting(model string) map[string]any {
	return map[string]any{
		"model":                       model,
		"tools":                       []any{},
		"mcpServers":                  []any{},
		"uiServers":                   []any{},
		"approvedTools":               []any{},
		"approvedMCPTools":            []any{},
		"enableAutoSubtaskExecution":  false,
		"env":                         []any{},
		"modelDesc":                   map[string]any{},
		"extra":                       nil,
		"selectedMCPTools":            map[string]any{},
		"selectedUITools":             map[string]any{},
		"a2a":                         map[string]any{},
		"hideToolMessage":             true,
		"language":                    "zh",
		"multimodal":                  nil,
		"enableThinking":              nil,
	}
}

// Frame 是 SSE 里的一帧。
type Frame struct {
	ID               string `json:"id"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoningContent"`
	Reasoning        bool   `json:"reasoning"`
	Type             string `json:"type"`
	Code             int    `json:"code"`
}

// Stream 封装 SSE 读取。
type Stream struct {
	body    io.ReadCloser
	sc      *bufio.Scanner
	pending []byte
	done    bool
}

func newStream(body io.ReadCloser) *Stream {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &Stream{body: body, sc: sc}
}

// Close 关闭响应体。
func (s *Stream) Close() error { return s.body.Close() }

// Next 读取下一帧；流结束返回 io.EOF。
func (s *Stream) Next() (*Frame, error) {
	for {
		if s.done {
			return nil, io.EOF
		}
		if !s.sc.Scan() {
			if err := s.sc.Err(); err != nil {
				return nil, &APIError{Kind: KindNetwork, Message: err.Error()}
			}
			s.done = true
			return nil, io.EOF
		}
		line := s.sc.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			s.done = true
			return nil, io.EOF
		}
		if strings.HasPrefix(payload, "[ERROR]") {
			return nil, classifyStreamError(payload)
		}
		var f Frame
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			// 非 JSON 帧：当作正文片段兜底。
			return &Frame{Content: payload, Type: "ai"}, nil
		}
		if f.Code != 0 {
			return nil, &APIError{Kind: KindProvider, Code: fmt.Sprint(f.Code), Message: "上游返回错误帧"}
		}
		return &f, nil
	}
}

func classifyStreamError(payload string) error {
	msg := strings.TrimSpace(strings.TrimPrefix(payload, "[ERROR]:"))
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "[LLM_ERROR]:"))
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "Invalid or expired AccessToken"), strings.Contains(msg, "NEED_UIN_AND_SKEY"):
		return &APIError{Kind: KindAuth, Message: msg}
	case strings.Contains(msg, "无访问此模型权限"), strings.Contains(low, "model not found"), strings.Contains(low, "invalid model"):
		return &APIError{Kind: KindModel, Message: msg}
	case strings.Contains(low, "rate limit"), strings.Contains(low, "too many"), strings.Contains(msg, "429"), strings.Contains(low, "concurrency"):
		return &APIError{Kind: KindRate, Message: msg}
	case strings.Contains(msg, "401 status code"):
		// 上游模型侧 401：并发/额度触发的软失败，不是网关 token 失效。
		return &APIError{Kind: KindRate, Code: "UPSTREAM_401", Message: msg}
	default:
		return &APIError{Kind: KindProvider, Message: msg}
	}
}

func parseJSONError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload struct {
		Code    any             `json:"code"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(b, &payload)
	msg := payload.Message
	if msg == "" {
		msg = strings.TrimSpace(string(b))
	}
	if msg == "" {
		msg = resp.Status
	}
	code := fmt.Sprint(payload.Code)
	if code == "<nil>" {
		code = ""
	}
	kind := KindProvider
	switch {
	case code == "10050000" || strings.Contains(strings.ToLower(msg), "invalid or expired accesstoken"):
		kind = KindAuth
	case code == "500" && strings.Contains(msg, "NEED_UIN_AND_SKEY"):
		kind = KindAuth
	case resp.StatusCode == http.StatusTooManyRequests:
		kind = KindRate
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		kind = KindAuth
	case resp.StatusCode >= 500:
		kind = KindProvider
	}
	return &APIError{Kind: kind, Code: code, Message: msg, HTTPStatus: resp.StatusCode}
}


