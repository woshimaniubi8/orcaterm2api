package gateway

import (
	"context"
	"strings"
	"sync"
	"time"

	"orcaterm2api/internal/config"
	"orcaterm2api/internal/upstream"
)

// Catalog 是运行时的模型目录（Rainbow 拉取，失败回退配置）。
type Catalog struct {
	mu        sync.RWMutex
	specs     []upstream.ModelSpec
	byKey     map[string]int
	source    string
	fetchedAt time.Time
	lastErr   string
}

// NewCatalog 构造目录。
func NewCatalog() *Catalog {
	return &Catalog{byKey: map[string]int{}}
}

// Replace 替换目录内容。
func (c *Catalog) Replace(specs []upstream.ModelSpec, source string) {
	byKey := make(map[string]int, len(specs)*3)
	for i, s := range specs {
		byKey[strings.ToLower(s.ID)] = i
		byKey[strings.ToLower(s.Upstream)] = i
		byKey[strings.ToLower(s.DisplayName)] = i
	}
	c.mu.Lock()
	c.specs = specs
	c.byKey = byKey
	c.source = source
	c.fetchedAt = time.Now()
	c.lastErr = ""
	c.mu.Unlock()
}

// SetError 记录最近一次拉取失败。
func (c *Catalog) SetError(err error) {
	c.mu.Lock()
	if err != nil {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
}

// List 返回目录副本。
func (c *Catalog) List() []upstream.ModelSpec {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]upstream.ModelSpec, len(c.specs))
	copy(out, c.specs)
	return out
}

// Resolve 解析模型（ID / 上游名 / 显示名 / 别名）。
func (c *Catalog) Resolve(id string, aliases map[string]string) (upstream.ModelSpec, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return upstream.ModelSpec{}, false
	}
	if alias, ok := aliases[id]; ok {
		id = alias
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if i, ok := c.byKey[strings.ToLower(id)]; ok {
		return c.specs[i], true
	}
	// 别名指向显示名/上游名时再查一次。
	if alias, ok := aliases[strings.ToLower(id)]; ok {
		if i, ok := c.byKey[strings.ToLower(alias)]; ok {
			return c.specs[i], true
		}
	}
	return upstream.ModelSpec{}, false
}

// Info 返回目录元信息。
func (c *Catalog) Info() (count int, fetchedAt time.Time, source, lastErr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.specs), c.fetchedAt, c.source, c.lastErr
}

// FallbackSpecs 把配置里的离线目录转成 ModelSpec。
func FallbackSpecs(cfg *config.Config) []upstream.ModelSpec {
	out := make([]upstream.ModelSpec, 0, len(cfg.Models.Enabled))
	for _, m := range cfg.Models.Enabled {
		name := m.Label
		if name == "" {
			name = m.ID
		}
		out = append(out, upstream.ModelSpec{
			ID:            m.ID,
			DisplayName:   name,
			Upstream:      m.Upstream,
			ContextLength: m.ContextLength,
			ShowThinking:  m.Reasoning,
		})
	}
	return out
}

// RefreshCatalog 从 Rainbow 刷新目录。
func (g *Gateway) RefreshCatalog(ctx context.Context) error {
	cfg := g.cfg.Snapshot()
	if !cfg.Models.CatalogEnabled {
		return nil
	}
	specs, err := upstream.FetchCatalog(ctx, upstream.CatalogConfig{
		Enabled:        cfg.Models.CatalogEnabled,
		URL:            cfg.Models.CatalogURL,
		Group:          cfg.Models.CatalogGroup,
		RefreshMinutes: cfg.Models.CatalogRefreshMinutes,
	})
	if err != nil {
		g.catalog.SetError(err)
		return err
	}
	g.catalog.Replace(specs, "rainbow:"+cfg.Models.CatalogGroup)
	return nil
}

// StartCatalogRefresher 周期刷新目录。
func (g *Gateway) StartCatalogRefresher(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_ = g.RefreshCatalog(rctx)
				cancel()
			}
		}
	}()
}

// Catalog 暴露目录。
func (g *Gateway) Catalog() *Catalog { return g.catalog }

// Resolve 用目录解析模型。
func (g *Gateway) Resolve(id string) (upstream.ModelSpec, bool) {
	cfg := g.cfg.Snapshot()
	if strings.TrimSpace(id) == "" {
		id = cfg.Upstream.DefaultModel
	}
	return g.catalog.Resolve(id, cfg.Models.Aliases)
}
