// Package panel 提供内嵌 Web 管理面板与运维 API。
package panel

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"orcaterm2api/internal/account"
	"orcaterm2api/internal/config"
	"orcaterm2api/internal/gateway"
	"orcaterm2api/internal/reqlog"
	"orcaterm2api/internal/upstream"
	"orcaterm2api/internal/version"
)

//go:embed assets/*
var assets embed.FS

// Panel 是面板实例。
type Panel struct {
	cfg   *config.Manager
	gw    *gateway.Gateway
	logs  *reqlog.Log
	start time.Time

	mu       sync.Mutex
	sessions map[string]time.Time
	oauth    map[string]oauthState
}

type oauthState struct {
	verifier string
	created  time.Time
}

// New 构造面板。
func New(cfg *config.Manager, gw *gateway.Gateway, logs *reqlog.Log) *Panel {
	return &Panel{
		cfg: cfg, gw: gw, logs: logs, start: time.Now(),
		sessions: map[string]time.Time{},
		oauth:    map[string]oauthState{},
	}
}

// Register 把面板路由挂到 mux 上。
func (p *Panel) Register(mux *http.ServeMux) {
	cfg := p.cfg.Snapshot()
	base := strings.TrimRight(cfg.Panel.Path, "/")
	if base == "" {
		base = "/panel"
	}
	mux.HandleFunc(base+"/", p.handleIndex)
	mux.HandleFunc(base+"/assets/", p.handleAsset)
	mux.HandleFunc(base+"/api/", p.handleAPI)
	mux.HandleFunc(base+"/oauth/callback", p.handleOAuthCallback)
}

func (p *Panel) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != p.base()+"/" && r.URL.Path != p.base() {
		http.NotFound(w, r)
		return
	}
	b, err := assets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "panel asset missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (p *Panel) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, p.base()+"/assets/")
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (p *Panel) base() string {
	cfg := p.cfg.Snapshot()
	b := strings.TrimRight(cfg.Panel.Path, "/")
	if b == "" {
		b = "/panel"
	}
	return b
}

// ---------- 鉴权 ----------

func (p *Panel) cookieName() string { return "orca_panel" }

func (p *Panel) sign(exp int64) string {
	secret := p.cfg.Secret()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "panel:%d", exp)
	return hex.EncodeToString(mac.Sum(nil)) + "." + fmt.Sprint(exp)
}

func (p *Panel) verify(v string) bool {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return false
	}
	var exp int64
	if _, err := fmt.Sscan(parts[1], &exp); err != nil {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(v), []byte(p.sign(exp)))
}

func (p *Panel) authed(r *http.Request) bool {
	c, err := r.Cookie(p.cookieName())
	if err != nil {
		return false
	}
	return p.verify(c.Value)
}

// ---------- API 路由 ----------

func (p *Panel) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, p.base()+"/api")
	path = strings.TrimRight(path, "/")
	if path == "/login" {
		p.handleLogin(w, r)
		return
	}
	if path == "/logout" {
		http.SetCookie(w, &http.Cookie{Name: p.cookieName(), Value: "", Path: p.base(), MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if !p.authed(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	switch {
	case path == "/session" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"ok": true, "version": version.Version})
	case path == "/overview" && r.Method == http.MethodGet:
		p.handleOverview(w, r)
	case path == "/accounts" && r.Method == http.MethodGet:
		p.handleAccountsList(w, r)
	case path == "/accounts" && r.Method == http.MethodPost:
		p.handleAccountAdd(w, r)
	case path == "/accounts/import-local" && r.Method == http.MethodPost:
		p.handleImportLocal(w, r)
	case strings.HasPrefix(path, "/accounts/"):
		p.handleAccountAction(w, r, strings.TrimPrefix(path, "/accounts/"))
	case path == "/models" && r.Method == http.MethodGet:
		p.handleModels(w, r)
	case path == "/logs" && r.Method == http.MethodGet:
		p.handleLogs(w, r)
	case path == "/config" && r.Method == http.MethodGet:
		p.handleConfigGet(w, r)
	case path == "/config" && r.Method == http.MethodPut:
		p.handleConfigPut(w, r)
	case path == "/oauth/start" && r.Method == http.MethodPost:
		p.handleOAuthStart(w, r)
	case path == "/oauth/wechat/config" && r.Method == http.MethodGet:
		p.handleWechatConfig(w, r)
	case path == "/oauth/wechat/exchange" && r.Method == http.MethodPost:
		p.handleWechatExchange(w, r)
	case path == "/oauth/poll" && r.Method == http.MethodPost:
		p.handleOAuthPoll(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func (p *Panel) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	if !hmac.Equal([]byte(req.Password), []byte(p.cfg.Secret())) {
		time.Sleep(300 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "密码错误"})
		return
	}
	cfg := p.cfg.Snapshot()
	exp := time.Now().Add(cfg.Panel.SessionTTL.D()).Unix()
	http.SetCookie(w, &http.Cookie{
		Name: p.cookieName(), Value: p.sign(exp), Path: p.base(),
		Expires: time.Unix(exp, 0), HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (p *Panel) handleOverview(w http.ResponseWriter, r *http.Request) {
	cfg := p.cfg.Snapshot()
	accounts, healthy, cooling, requests, errors := p.gw.Pool().Stats()
	stats := p.logs.Stats()
	writeJSON(w, 200, map[string]any{
		"version":        version.Version,
		"uptime_seconds": int64(time.Since(p.start).Seconds()),
		"listen":         cfg.Listen,
		"accounts":       map[string]any{"total": accounts, "healthy": healthy, "cooling": cooling},
		"pool_requests":  requests,
		"pool_errors":    errors,
		"requests":       stats,
		"conversations":  p.gw.Session().Len(),
		"mode":           cfg.Conversation.Mode,
		"default_model":  cfg.Upstream.DefaultModel,
	})
}

func (p *Panel) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	snaps := p.gw.Pool().Snapshots()
	writeJSON(w, 200, map[string]any{"data": snaps})
}

func (p *Panel) handleAccountAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Name         string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	acct, err := p.buildAccount(r, req.AccessToken, req.RefreshToken, "manual")
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if req.Name != "" {
		acct.Name = req.Name
	}
	if err := p.gw.Pool().Add(acct); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "account": acct.Masked()})
}

func (p *Panel) buildAccount(r *http.Request, token, refresh, source string) (*account.Account, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("access_token 不能为空")
	}
	acct, err := account.FromToken(token, refresh, source)
	if err != nil {
		return nil, err
	}
	// 用 GetUserInfo 验证令牌并补昵称（失败则回退 JWT 信息）。
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if info, err := p.gw.Client().UserInfo(ctx, acct.AccessToken); err == nil {
		if info.UserID != "" {
			acct.ID = info.UserID.String()
		}
		if info.NickName != "" {
			acct.Name = info.NickName
		}
	} else if ae, ok := upstream.AsAPIError(err); ok && ae.Kind == upstream.KindAuth {
		return nil, fmt.Errorf("令牌无效或已过期：%s", ae.Message)
	}
	return acct, nil
}

func (p *Panel) handleImportLocal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path         string `json:"path"`
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req)
	path := req.Path
	if strings.TrimSpace(path) == "" {
		cfg := p.cfg.Snapshot()
		path = cfg.Panel.LocalDataBin
	}
	if strings.TrimSpace(path) == "" {
		path = account.DefaultLocalDataBin()
	}
	if path == "" {
		writeJSON(w, 400, map[string]any{"error": "未找到本机 OrcaTerm data.bin，请手动填写路径"})
		return
	}
	acct, err := account.ImportLocal(path, req.RefreshToken)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	if err := p.gw.Pool().Add(acct); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "account": acct.Masked(), "path": path})
}

func (p *Panel) handleAccountAction(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	id := parts[0]
	if id == "" {
		writeJSON(w, 404, map[string]any{"error": "account not found"})
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case action == "" && r.Method == http.MethodDelete:
		if err := p.gw.Pool().Remove(id); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case action == "refresh" && r.Method == http.MethodPost:
		e := p.gw.Pool().ByID(id)
		if e == nil {
			writeJSON(w, 404, map[string]any{"error": "account not found"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		if err := p.gw.RefreshAccount(ctx, e); err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case action == "toggle" && r.Method == http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
		if err := p.gw.Pool().SetEnabled(id, req.Enabled); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case action == "test" && r.Method == http.MethodPost:
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req)
		cfg := p.cfg.Snapshot()
		model := req.Model
		if model == "" {
			model = cfg.Upstream.DefaultModel
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		out, err := p.gw.TestAccount(ctx, id, model, req.Prompt)
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "reply": out})
	case action == "quota" && r.Method == http.MethodGet:
		e := p.gw.Pool().ByID(id)
		if e == nil {
			writeJSON(w, 404, map[string]any{"error": "account not found"})
			return
		}
		acct := e.Account()
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		q, err := p.gw.Client().AIQuota(ctx, acct.AccessToken)
		if err != nil {
			writeJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "quota": q})
	default:
		writeJSON(w, 404, map[string]any{"error": "not found"})
	}
}

func (p *Panel) handleModels(w http.ResponseWriter, r *http.Request) {
	cfg := p.cfg.Snapshot()
	count, fetchedAt, source, lastErr := p.gw.Catalog().Info()
	catalog := map[string]any{
		"count": count,
		"source": source,
	}
	if !fetchedAt.IsZero() {
		catalog["fetched_at"] = fetchedAt.UnixMilli()
	}
	if lastErr != "" {
		catalog["error"] = lastErr
	}
	writeJSON(w, 200, map[string]any{
		"data":         p.gw.Catalog().List(),
		"aliases":      cfg.Models.Aliases,
		"hidden":       cfg.Models.Hidden,
		"default":      cfg.Upstream.DefaultModel,
		"assistant":    cfg.Upstream.AssistantName,
		"mode":         cfg.Upstream.Mode,
		"base":         cfg.Upstream.AssistantBase,
		"catalog":      catalog,
	})
}

func (p *Panel) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscan(v, &limit)
	}
	writeJSON(w, 200, map[string]any{"data": p.logs.List(limit, q), "stats": p.logs.Stats()})
}

// ---------- 配置 ----------

type configView struct {
	Listen       string               `json:"listen"`
	APIKeys      []string             `json:"api_keys_mask"`
	PanelPath    string               `json:"panel_path"`
	Upstream     config.UpstreamConfig `json:"upstream"`
	Models       config.ModelsConfig  `json:"models"`
	Pool         config.PoolConfig    `json:"pool"`
	Conversation config.ConversationConfig `json:"conversation"`
	Logging      config.LoggingConfig `json:"logging"`
}

func (p *Panel) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg := p.cfg.Snapshot()
	keys := make([]string, 0, len(cfg.APIKeys))
	for _, k := range cfg.APIKeys {
		keys = append(keys, maskKey(k))
	}
	writeJSON(w, 200, configView{
		Listen: cfg.Listen, APIKeys: keys, PanelPath: cfg.Panel.Path,
		Upstream: cfg.Upstream, Models: cfg.Models, Pool: cfg.Pool,
		Conversation: cfg.Conversation, Logging: cfg.Logging,
	})
}

type configPatch struct {
	APIKeys      *[]string               `json:"api_keys"`
	PanelPassword *string                `json:"panel_password"`
	Upstream     *config.UpstreamConfig  `json:"upstream"`
	Models       *config.ModelsConfig    `json:"models"`
	Pool         *config.PoolConfig      `json:"pool"`
	Conversation *config.ConversationConfig `json:"conversation"`
	Logging      *config.LoggingConfig   `json:"logging"`
}

func (p *Panel) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var patch configPatch
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&patch); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	err := p.cfg.Update(func(c *config.Config) error {
		if patch.APIKeys != nil {
			keys := make([]string, 0, len(*patch.APIKeys))
			for _, k := range *patch.APIKeys {
				k = strings.TrimSpace(k)
				if k != "" {
					keys = append(keys, k)
				}
			}
			if len(keys) == 0 {
				return errors.New("api_keys 不能为空")
			}
			c.APIKeys = keys
		}
		if patch.PanelPassword != nil && strings.TrimSpace(*patch.PanelPassword) != "" {
			c.Panel.Password = *patch.PanelPassword
		}
		if patch.Upstream != nil {
			u := *patch.Upstream
			if strings.TrimSpace(u.AssistantBase) == "" {
				u.AssistantBase = c.Upstream.AssistantBase
			}
			if strings.TrimSpace(u.CGIBase) == "" {
				u.CGIBase = c.Upstream.CGIBase
			}
			if strings.TrimSpace(u.AssistantName) == "" {
				u.AssistantName = c.Upstream.AssistantName
			}
			if strings.TrimSpace(u.DefaultModel) == "" {
				u.DefaultModel = c.Upstream.DefaultModel
			}
			if strings.TrimSpace(u.Product) == "" {
				u.Product = c.Upstream.Product
			}
			c.Upstream = u
		}
		if patch.Models != nil {
			c.Models = *patch.Models
		}
		if patch.Pool != nil {
			c.Pool = *patch.Pool
		}
		if patch.Conversation != nil {
			c.Conversation = *patch.Conversation
		}
		if patch.Logging != nil {
			patch.Logging.ArchiveFile = c.Logging.ArchiveFile
			c.Logging = *patch.Logging
		}
		return nil
	})
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error()})
		return
	}
	// 热生效：池参数与上游配置。
	cfg := p.cfg.Snapshot()
	p.gw.Pool().Configure(cfg.Pool)
	p.gw.Client().SetConfig(cfg.Upstream)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func maskKey(k string) string {
	if len(k) <= 12 {
		return "***"
	}
	return k[:8] + "..." + k[len(k)-4:]
}

// ---------- OAuth ----------

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// newUUID 生成标准 UUID v4（上游 OAuthExchangeToken 要求 sessionId 是 GUID）。
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (p *Panel) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReturnURL string `json:"return_url"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req)
	sessionID := newUUID()
	verifier := randomToken(48)
	p.mu.Lock()
	p.oauth[sessionID] = oauthState{verifier: verifier, created: time.Now()}
	p.mu.Unlock()
	base := strings.TrimRight(req.ReturnURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	callback := base + p.base() + "/oauth/callback"
	q := url.Values{}
	q.Set("provider", "wechat")
	q.Set("source", "desktop")
	q.Set("session_id", sessionID)
	q.Set("codeChallenge", pkceChallenge(verifier))
	q.Set("codeChallengeMethod", "S256")
	q.Set("return_url", callback)
	authURL := "https://orcaterm.com/oauth/authorize?" + q.Encode()
	qrURL := resolveWechatQRURL(r.Context(), authURL)
	writeJSON(w, 200, map[string]any{"url": authURL, "qr_url": qrURL, "session_id": sessionID, "callback": callback})
}

// resolveWechatQRURL 把 /oauth/authorize 的 302 解析成微信二维码 URL，
// 面板可以直接用 iframe 渲染二维码，不必打开外部浏览器。
func resolveWechatQRURL(ctx context.Context, authURL string) string {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 orcaterm2api")
	req.Header.Set("Referer", "https://orcaterm.com/login")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if strings.Contains(loc, "open.weixin.qq.com") {
		return loc
	}
	return ""
}

func (p *Panel) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		p.redirectResult(w, r, "missing_session", "")
		return
	}
	p.mu.Lock()
	st, ok := p.oauth[sessionID]
	delete(p.oauth, sessionID)
	p.mu.Unlock()
	if !ok || time.Since(st.created) > 15*time.Minute {
		p.redirectResult(w, r, "session_expired", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tok, err := p.gw.Client().Exchange(ctx, sessionID, st.verifier)
	if err != nil {
		p.redirectResult(w, r, "exchange_failed", err.Error())
		return
	}
	acct, err := account.FromToken(tok.AccessToken, tok.RefreshToken, "oauth")
	if err != nil {
		p.redirectResult(w, r, "bad_token", err.Error())
		return
	}
	acct.ExpiresAt = tok.ExpiresAt(time.Now())
	if info, err := p.gw.Client().UserInfo(ctx, acct.AccessToken); err == nil {
		if info.UserID != "" {
			acct.ID = info.UserID.String()
		}
		if info.NickName != "" {
			acct.Name = info.NickName
		}
	}
	if err := p.gw.Pool().Add(acct); err != nil {
		p.redirectResult(w, r, "save_failed", err.Error())
		return
	}
	p.redirectResult(w, r, "ok", acct.Name)
}

func (p *Panel) redirectResult(w http.ResponseWriter, r *http.Request, status, msg string) {
	q := url.Values{}
	q.Set("oauth", status)
	if msg != "" {
		q.Set("msg", msg)
	}
	http.Redirect(w, r, p.base()+"/?"+q.Encode(), http.StatusFound)
}




