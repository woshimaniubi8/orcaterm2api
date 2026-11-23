package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orcaterm2api/internal/account"
	"orcaterm2api/internal/config"
	"orcaterm2api/internal/gateway"
	"orcaterm2api/internal/pool"
	"orcaterm2api/internal/reqlog"
	"orcaterm2api/internal/session"
	"orcaterm2api/internal/upstream"
)

// fakeUpstream 模拟 OrcaTerm assistant 的 SSE：把一段 JSON 逐字下发。
func fakeUpstream(t *testing.T, payload string, check func(body map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat") {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if check != nil {
			check(body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, r := range payload {
			b, _ := json.Marshal(map[string]any{"id": "chatcmpl-x", "content": string(r), "type": "ai", "code": 0})
			fmt.Fprintf(w, "data: %s\n\n", b)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
}

func newTestServer(t *testing.T, upstreamURL string) (*httptest.Server, *config.Manager) {
	t.Helper()
	dir := t.TempDir()
	acct := &account.Account{
		ID: "u1", Name: "tester", AccessToken: "tok", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli(),
		Enabled: true, CreatedAt: time.Now().UnixMilli(),
	}
	if err := account.Save(dir, acct); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.APIKeys = []string{"test-key"}
	cfg.AuthDir = dir
	cfg.Upstream.AssistantBase = upstreamURL + "/assistant"
	cfg.Upstream.TimeoutSeconds = 30
	cfg.Pool.MaxInFlight = 2
	cfg.Pool.MinInterval = config.Duration(0)
	cfg.Pool.MaxPerMinute = 0
	cfg.Conversation.Mode = "stateless"
	cfg.Logging.RingSize = 20
	cfg.Logging.ArchiveEnabled = false
	cfg.Panel.Enabled = false
	cfgPath := filepath.Join(dir, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	mgr := config.NewManager(cfgPath, cfg)
	p := pool.New(dir, cfg.Pool)
	if err := p.Load(); err != nil {
		t.Fatal(err)
	}
	client := upstream.New(cfg.Upstream)
	gw := gateway.New(mgr, p, client, session.New(time.Minute))
	logs := reqlog.New(20, "", false, false)
	return httptest.NewServer(New(mgr, gw, logs).Handler()), mgr
}

func TestChatCompletionNonStream(t *testing.T) {
	payload := `{"action":"completion","thinking":"想一想","taskCompletion":"PONG-123"}`
	up := fakeUpstream(t, payload, func(body map[string]any) {
		input, _ := body["input"].(map[string]any)
		text, _ := input["input"].(string)
		if !strings.Contains(text, "hi") || !strings.Contains(text, "sys") || input["type"] != "start" {
			t.Errorf("unexpected input: %+v", input)
		}
		if body["conversationId"] == "" {
			t.Error("missing conversationId")
		}
	})
	defer up.Close()
	srv, _ := newTestServer(t, up.URL)
	defer srv.Close()

	reqBody := `{"model":"qwen3","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "PONG-123" {
		t.Fatalf("resp=%+v", out)
	}
	if out.Choices[0].Message.ReasoningContent != "想一想" {
		t.Fatalf("reasoning=%q", out.Choices[0].Message.ReasoningContent)
	}
	if out.Usage.TotalTokens == 0 {
		t.Fatal("usage missing")
	}
}

func TestChatCompletionStream(t *testing.T) {
	payload := `{"action":"completion","taskCompletion":"STREAM-OK"}`
	up := fakeUpstream(t, payload, nil)
	defer up.Close()
	srv, _ := newTestServer(t, up.URL)
	defer srv.Close()

	reqBody := `{"model":"qwen3","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type=%s", ct)
	}
	var all strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		all.Write(buf[:n])
		if err != nil {
			break
		}
	}
	body := all.String()
	if !strings.Contains(body, "STREAM-OK") && !strings.Contains(body, `"S"`) {
		t.Fatalf("stream missing content: %s", body[:min(len(body), 400)])
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatal("missing [DONE]")
	}
}

func TestChatAuthRequired(t *testing.T) {
	srv, _ := newTestServer(t, "http://127.0.0.1:1")
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestAnthropicMessages(t *testing.T) {
	payload := `{"action":"completion","taskCompletion":"ANTHRO-OK"}`
	up := fakeUpstream(t, payload, nil)
	defer up.Close()
	srv, _ := newTestServer(t, up.URL)
	defer srv.Close()

	reqBody := `{"model":"qwen3","max_tokens":64,"system":"be brief","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", srv.URL+"/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Type    string `json:"type"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Type != "message" || len(out.Content) != 1 || out.Content[0].Text != "ANTHRO-OK" {
		t.Fatalf("out=%+v", out)
	}
}

func TestFlexContent(t *testing.T) {
	var m chatMessage
	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if string(m.Content) != "a\nb" {
		t.Fatalf("content=%q", m.Content)
	}
}

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Fatal("empty should be 0")
	}
	if estimateTokens("abcd") != 1 {
		t.Fatalf("ascii=%d", estimateTokens("abcd"))
	}
	if estimateTokens("你好") != 2 {
		t.Fatalf("cjk=%d", estimateTokens("你好"))
	}
}

func TestModelsPublic(t *testing.T) {
	srv, _ := newTestServer(t, "http://127.0.0.1:1")
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "list" || len(out.Data) == 0 {
		t.Fatalf("out=%+v", out)
	}
}
