package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"orcaterm2api/internal/account"
	"orcaterm2api/internal/config"
	"orcaterm2api/internal/pool"
	"orcaterm2api/internal/session"
	"orcaterm2api/internal/upstream"
)

func TestTestAccountResolvesUpstreamModel(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if user, ok := body["user"].(map[string]any); ok {
			if setting, ok := user["setting"].(map[string]any); ok {
				gotModel, _ = setting["model"].(string)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		payload := `{"action":"completion","taskCompletion":"TEST-OK"}`
		for _, ch := range payload {
			b, _ := json.Marshal(map[string]any{"content": string(ch), "type": "ai", "code": 0})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	dir := t.TempDir()
	acct := &account.Account{ID: "u1", AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Enabled: true, CreatedAt: time.Now().UnixMilli()}
	if err := account.Save(dir, acct); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.APIKeys = []string{"k"}
	cfg.AuthDir = dir
	cfg.Upstream.AssistantBase = srv.URL + "/assistant"
	cfg.Models.CatalogEnabled = false
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
	g := New(mgr, p, client, session.New(time.Minute))
	g.catalog.Replace([]upstream.ModelSpec{{
		ID: "deepseek-v4.1-flash", DisplayName: "DeepSeek-V4.1-Flash", Upstream: "TokenHub/deepseek-v4.1-flash", ShowThinking: true,
	}}, "test")

	reply, err := g.TestAccount(context.Background(), "u1", "deepseek-v4.1-flash", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "TEST-OK" {
		t.Fatalf("reply=%q", reply)
	}
	if gotModel != "TokenHub/deepseek-v4.1-flash" {
		t.Fatalf("upstream model=%q", gotModel)
	}
}

