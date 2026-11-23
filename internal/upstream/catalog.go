package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ModelTag 是模型卡片上的标签（限时免费 / 新 等）。
type ModelTag struct {
	Text string `json:"text"`
	Type string `json:"type"`
}

// ThinkingLevel 是一个可选思考强度。
type ThinkingLevel struct {
	Label         string         `json:"label"`
	Value         string         `json:"value"`
	Default       bool           `json:"default,omitempty"`
	RequestParams map[string]any `json:"request_params,omitempty"`
}

// ModelSpec 是上游内置模型目录的一条（来自 Rainbow 配置）。
type ModelSpec struct {
	ID             string          `json:"id"`
	DisplayName    string          `json:"display_name"`
	Upstream       string          `json:"upstream"`
	Vendor         string          `json:"vendor,omitempty"`
	Description    string          `json:"description,omitempty"`
	Capabilities   []string        `json:"capabilities,omitempty"`
	Tags           []ModelTag      `json:"tags,omitempty"`
	PriceRate      float64         `json:"price_rate"`
	ShowPriceRate  bool            `json:"show_price_rate"`
	ShowThinking   bool            `json:"show_thinking"`
	SupportImg     bool            `json:"support_img"`
	ThinkingLevels []ThinkingLevel `json:"thinking_levels,omitempty"`
	Sort           int             `json:"sort"`
	Free           bool            `json:"free"`
	ContextLength  int             `json:"context_length,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
}

// CatalogConfig 是 Rainbow 模型目录的拉取参数。
type CatalogConfig struct {
	Enabled        bool   `json:"enabled"`
	URL            string `json:"url"`
	Group          string `json:"group"`
	RefreshMinutes int    `json:"refresh_minutes"`
}

// DefaultCatalogConfig 返回默认目录配置。
func DefaultCatalogConfig() CatalogConfig {
	return CatalogConfig{
		Enabled:        true,
		URL:            "https://dnspod.cloud.tencent.com/ajax/rainbow?action=getRainbowConfig",
		Group:          "orcaterm.内置模型",
		RefreshMinutes: 10,
	}
}

type rainbowRow struct {
	AutoID         string `json:"_auto_id"`
	Capabilities   string `json:"capabilities"`
	Description    string `json:"description"`
	DisplayName    string `json:"displayName"`
	IsExternal     string `json:"isExternal"`
	Model          string `json:"model"`
	PriceRate      string `json:"priceRate"`
	ShowPriceRate  string `json:"showPriceRate"`
	ShowThinking   string `json:"showThinking"`
	Sort           string `json:"sort"`
	SupportImg     string `json:"supportImg"`
	Tags           string `json:"tags"`
	Text           string `json:"text"`
	ThinkingLevels string `json:"thinkingLevels"`
}

// FetchCatalog 从 Rainbow 拉取内置模型目录并规范化。
func FetchCatalog(ctx context.Context, cfg CatalogConfig) ([]ModelSpec, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("catalog disabled")
	}
	group := cfg.Group
	if group == "" {
		group = "orcaterm.内置模型"
	}
	payload := map[string]any{
		"app":     "ai",
		"groups":  []string{group},
		"options": map[string]any{"enableEnv": 1},
	}
	raw, _ := json.Marshal(payload)
	u := cfg.URL
	if u == "" {
		u = DefaultCatalogConfig().URL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rainbow http %d", resp.StatusCode)
	}
	var envelope struct {
		Code int `json:"code"`
		Data []struct {
			Group string       `json:"group"`
			Rows  []rainbowRow `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("rainbow 解析失败: %w", err)
	}
	if len(envelope.Data) == 0 {
		return nil, fmt.Errorf("rainbow 返回空数据")
	}
	specs := make([]ModelSpec, 0, len(envelope.Data[0].Rows))
	for _, row := range envelope.Data[0].Rows {
		spec := parseRainbowRow(row)
		if spec == nil {
			continue
		}
		specs = append(specs, *spec)
	}
	return DedupCatalog(specs), nil
}

func parseRainbowRow(row rainbowRow) *ModelSpec {
	model := strings.TrimSpace(row.Model)
	name := strings.TrimSpace(row.DisplayName)
	if model == "" || name == "" {
		return nil
	}
	spec := &ModelSpec{
		ID:            modelID(model),
		DisplayName:   name,
		Upstream:      model,
		Vendor:        strings.TrimSpace(row.Text),
		Description:   strings.TrimSpace(row.Description),
		ShowPriceRate: row.ShowPriceRate == "1" || row.ShowPriceRate == "true",
		ShowThinking:  row.ShowThinking == "1" || row.ShowThinking == "true",
		SupportImg:    row.SupportImg == "1" || row.SupportImg == "true",
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(row.PriceRate), 64); err == nil {
		spec.PriceRate = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(row.Sort)); err == nil {
		spec.Sort = v
	}
	spec.Capabilities = parseStringArray(row.Capabilities)
	if tags := parseTags(row.Tags); len(tags) > 0 {
		spec.Tags = tags
		for _, t := range tags {
			if t.Type == "free" {
				spec.Free = true
			}
		}
	}
	spec.ThinkingLevels = parseThinkingLevels(row.ThinkingLevels)
	spec.ContextLength = contextLengthFromDescription(row.Description)
	seedCtx, seedOut := seedModelMeta(spec.ID)
	if spec.ContextLength == 0 {
		spec.ContextLength = seedCtx
	}
	spec.MaxOutputTokens = seedOut
	return spec
}

func parseStringArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return out
	}
	return nil
}

func parseTags(raw string) []ModelTag {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []ModelTag
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func parseThinkingLevels(raw string) []ThinkingLevel {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []ThinkingLevel
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

var ctxRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*([MK])\b`)

func contextLengthFromDescription(desc string) int {
	m := ctxRe.FindStringSubmatch(desc)
	if len(m) != 3 {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	switch strings.ToUpper(m[2]) {
	case "M":
		return int(v * 1_000_000)
	case "K":
		return int(v * 1_000)
	}
	return 0
}

// modelID 从上游模型名派生一个干净的对外 ID：TokenHub/deepseek-v4.1-flash -> deepseek-v4.1-flash。
func modelID(model string) string {
	id := model
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = strings.ToLower(strings.TrimSpace(id))
	repl := strings.NewReplacer(" ", "-", "_", "-")
	return repl.Replace(id)
}

// DedupCatalog 去重、过滤预览版并排序。
func DedupCatalog(specs []ModelSpec) []ModelSpec {
	// 同名保留排序最高、模型名最短（优先正式名，排除 -0813 这类日期后缀）。
	best := map[string]ModelSpec{}
	for _, s := range specs {
		prev, ok := best[s.DisplayName]
		if !ok || s.Sort > prev.Sort || (s.Sort == prev.Sort && len(s.Upstream) < len(prev.Upstream)) {
			best[s.DisplayName] = s
		}
	}
	// 有正式版时去掉 -preview。
	names := map[string]bool{}
	for name := range best {
		names[strings.ToLower(name)] = true
	}
	out := make([]ModelSpec, 0, len(best))
	for name, s := range best {
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, "-preview") {
			base := strings.TrimSuffix(lower, "-preview")
			if names[base] {
				continue
			}
		}
		out = append(out, s)
	}
	// ID 冲突时追加序号。
	used := map[string]int{}
	for i := range out {
		id := out[i].ID
		if n := used[id]; n > 0 {
			used[id] = n + 1
			out[i].ID = fmt.Sprintf("%s-%d", id, n+1)
		} else {
			used[id] = 1
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sort != out[j].Sort {
			return out[i].Sort > out[j].Sort
		}
		return out[i].DisplayName < out[j].DisplayName
	})
	return out
}

// seedModelMeta 是内置模型的参考上下文 / 最大输出（上游目录没有这两个字段，
// 用公开规格 + workbuddy2api 的同名模型值做种子；可用 config.models.metadata 覆盖）。
func seedModelMeta(id string) (ctx, out int) {
	switch strings.ToLower(id) {
	case "deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4.1-flash":
		return 1000000, 64000
	case "glm-5.2", "glm-5.3":
		return 1000000, 131000
	case "glm-5.3-flash":
		return 200000, 64000
	case "kimi-k3":
		return 1000000, 64000
	case "hy3":
		return 192000, 64000
	case "hy4":
		return 256000, 64000
	default:
		return 0, 0
	}
}
