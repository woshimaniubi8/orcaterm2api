package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCatalogParsesRainbowRows(t *testing.T) {
	rows := []map[string]any{
		{
			"displayName": "DeepSeek-V4.1-Flash", "model": "TokenHub/deepseek-v4.1-flash",
			"description": "DeepSeek 旗舰模型，支持 1M 上下文窗口",
			"capabilities": `["推理","长上下文","图片识别"]`,
			"tags":         `[{"text":"限时免费","type":"free"},{"text":"新","type":"new"}]`,
			"thinkingLevels": `[{"label":"低","value":"low","requestParams":{"reasoning_effort":"low"}},` +
				`{"label":"高","value":"high","default":true,"requestParams":{"reasoning_effort":"high"}},` +
				`{"label":"关闭","value":"off","requestParams":{"thinking":{"type":"disabled"}}}]`,
			"showThinking": "1", "supportImg": "1", "sort": "70", "priceRate": "0", "showPriceRate": "0",
		},
		{
			"displayName": "Hy4-preview", "model": "Hunyuan3/hy4-preview", "sort": "10",
			"capabilities": `["推理"]`, "showThinking": "1",
		},
		{
			"displayName": "Hy4", "model": "Hunyuan3/hy4", "sort": "10",
			"capabilities": `["推理","长上下文"]`, "showThinking": "1",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": []any{map[string]any{"group": "orcaterm.内置模型", "rows": rows}},
		})
	}))
	defer srv.Close()
	specs, err := FetchCatalog(context.Background(), CatalogConfig{Enabled: true, URL: srv.URL, Group: "orcaterm.内置模型"})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("specs=%d %+v", len(specs), specs)
	}
	var v41 *ModelSpec
	for i := range specs {
		if specs[i].DisplayName == "DeepSeek-V4.1-Flash" {
			v41 = &specs[i]
		}
		if specs[i].DisplayName == "Hy4-preview" {
			t.Fatalf("preview should be dropped: %+v", specs)
		}
	}
	if v41 == nil {
		t.Fatal("missing V4.1")
	}
	if v41.ID != "deepseek-v4.1-flash" || v41.Upstream != "TokenHub/deepseek-v4.1-flash" {
		t.Fatalf("id/upstream: %+v", v41)
	}
	if !v41.Free || !v41.SupportImg || v41.ContextLength != 1000000 {
		t.Fatalf("flags: %+v", v41)
	}
	if len(v41.ThinkingLevels) != 3 || v41.ThinkingLevels[1].Value != "high" || !v41.ThinkingLevels[1].Default {
		t.Fatalf("levels: %+v", v41.ThinkingLevels)
	}
	if len(v41.Capabilities) != 3 {
		t.Fatalf("caps: %+v", v41.Capabilities)
	}
}

func TestDedupPrefersFormalModel(t *testing.T) {
	specs := DedupCatalog([]ModelSpec{
		{DisplayName: "Deepseek-V4-Pro", Upstream: "deepseek-chat-latest", ID: "deepseek-chat-latest", Sort: 9},
		{DisplayName: "Deepseek-V4-Pro", Upstream: "TokenHub/deepseek-v4-pro-0813", ID: "deepseek-v4-pro-0813", Sort: 100},
		{DisplayName: "Deepseek-V4-Pro", Upstream: "TokenHub/deepseek-v4-pro", ID: "deepseek-v4-pro", Sort: 100},
	})
	if len(specs) != 1 {
		t.Fatalf("dedup=%+v", specs)
	}
	if specs[0].Upstream != "TokenHub/deepseek-v4-pro" {
		t.Fatalf("chosen=%+v", specs[0])
	}
}
