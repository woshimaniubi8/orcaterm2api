package panel

import (
	"bytes"
	"log"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"orcaterm2api/internal/account"
	"orcaterm2api/internal/upstream"
)

const orcaOrigin = "https://orcaterm.com"

// wechatConfig 是 OrcaTerm 微信扫码登录的公开配置。
type wechatConfig struct {
	AppID       string `json:"appid"`
	RedirectURI string `json:"redirect_uri"`
	State       string `json:"state"`
	Scope       string `json:"scope"`
}

// wechatConfig 拉取扫码配置（服务端代理，避免浏览器跨域）。
func (p *Panel) wechatConfig(ctx context.Context) (*wechatConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orcaOrigin+"/oauth/wechat/config", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", orcaOrigin+"/login")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var payload struct {
		Code int          `json:"code"`
		Data wechatConfig `json:"data"`
		Msg  string       `json:"message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("微信配置解析失败: %w", err)
	}
	if payload.Code != 0 || payload.Data.AppID == "" || payload.Data.State == "" || payload.Data.RedirectURI == "" {
		return nil, fmt.Errorf("微信配置不可用: %s", strings.TrimSpace(payload.Msg))
	}
	return &payload.Data, nil
}

func (p *Panel) httpClient() *http.Client {
	return &http.Client{Timeout: 25 * time.Second}
}

// handleWechatConfig GET /api/oauth/wechat/config
func (p *Panel) handleWechatConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cfg, err := p.wechatConfig(ctx)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, cfg)
}

// handleWechatExchange POST /api/oauth/wechat/exchange {code,state}
// 用扫码回调的 code/state 换取令牌；优先按桌面端（返回 token），失败再按 Web（Set-Cookie）。
func (p *Panel) handleWechatExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var req struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	req.State = strings.TrimSpace(req.State)
	if req.Code == "" || req.State == "" {
		writeJSON(w, 400, map[string]any{"error": "code / state 不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	bundle, debug, err := p.exchangeWechat(ctx, req.Code, req.State)
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	if bundle.Access == "" {
		writeJSON(w, 502, map[string]any{"error": "微信登录成功但未取到访问令牌", "debug": debug})
		return
	}
	acct, err := account.FromToken(bundle.Access, bundle.Refresh, "wechat-qr")
	if err != nil {
		writeJSON(w, 502, map[string]any{"error": "令牌解析失败: " + err.Error(), "debug": debug})
		return
	}
	if bundle.ExpiresAt > 0 {
		acct.ExpiresAt = bundle.ExpiresAt
	}
	if bundle.LoginCookie != "" {
		acct.LoginCookie = bundle.LoginCookie
	}
	if bundle.Name != "" {
		acct.Name = bundle.Name
	}
	if info, ierr := p.gw.Client().UserInfo(ctx, acct.AccessToken); ierr == nil {
		if info.UserID != "" {
			acct.ID = info.UserID.String()
		}
		if info.NickName != "" {
			acct.Name = info.NickName
		}
	}
	if err := p.gw.Pool().Add(acct); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":          true,
		"account":     acct.Masked(),
		"has_refresh": acct.HasRefresh(),
		"debug":       debug,
	})
}

type tokenBundle struct {
	Access      string
	Refresh     string
	LoginCookie string
	Name        string
	ExpiresAt   int64
}

// exchangeWechat 用 /oauth/wechat/config 发的 state 走 Web 兑换（Set-Cookie ot_session）。
// state 是一次性的，不能先试桌面模式再回退，否则第一次失败就把 state 消费掉了。
func (p *Panel) exchangeWechat(ctx context.Context, code, state string) (tokenBundle, map[string]any, error) {
	debug := map[string]any{}
	b, dbg, err := p.postWechatExchange(ctx, map[string]any{"code": code, "state": state})
	if err != nil {
		return tokenBundle{}, debug, err
	}
	if b.Access == "" {
		debug["web_keys"] = dbg
		return tokenBundle{}, debug, fmt.Errorf("上游未返回访问令牌（可能 code 已使用或 state 过期）")
	}
	return b, dbg, nil
}

func (p *Panel) postWechatExchange(ctx context.Context, body map[string]any) (tokenBundle, map[string]any, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, orcaOrigin+"/oauth/wechat/exchange", bytes.NewReader(raw))
	if err != nil {
		return tokenBundle{}, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", orcaOrigin)
	req.Header.Set("Referer", orcaOrigin+"/login")
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return tokenBundle{}, nil, err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	debug := map[string]any{"status": resp.StatusCode}
	cookieNames := make([]string, 0, len(resp.Cookies()))
	for _, c := range resp.Cookies() {
		cookieNames = append(cookieNames, c.Name)
	}
	debug["cookies"] = cookieNames
	log.Printf("[wechat] exchange status=%d cookies=%v body_keys=%v", resp.StatusCode, cookieNames, debug["body_keys"])
	var parsed any
	if json.Unmarshal(payload, &parsed) == nil {
		if m, ok := parsed.(map[string]any); ok {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			debug["body_keys"] = keys
		}
	}
	bundle := extractTokenBundle(parsed, resp.Cookies())
	if bundle.Access == "" {
		for _, c := range resp.Cookies() {
			if c.Name == "ot_login" && c.Value != "" {
				if tok, ierr := p.gw.Client().IssueAccessToken(ctx, c.Value); ierr == nil {
					bundle.Access = tok.AccessToken
				}
			}
		}
	}
	if resp.StatusCode != 200 {
		msg := strings.TrimSpace(string(payload))
		var e struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(payload, &e) == nil && e.Message != "" {
			msg = fmt.Sprintf("%v: %s", e.Code, e.Message)
		}
		return tokenBundle{}, debug, fmt.Errorf("微信登录失败（HTTP %d）%s", resp.StatusCode, truncateStr(msg, 200))
	}
	return bundle, debug, nil
}

// extractTokenBundle 从 JSON 任意层级 / Set-Cookie 里提取 token。
func extractTokenBundle(v any, cookies []*http.Cookie) tokenBundle {
	var out tokenBundle
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			for k, val := range n {
				lk := strings.ToLower(k)
				switch {
				case out.Access == "" && (lk == "accesstoken" || lk == "access_token" || lk == "ot_session"):
					if s, ok := val.(string); ok && strings.Count(s, ".") == 2 {
						out.Access = s
					}
				case out.Refresh == "" && (lk == "refreshtoken" || lk == "refresh_token" || strings.Contains(lk, "refresh")):
					if s, ok := val.(string); ok && s != "" {
						out.Refresh = s
					}
				case out.Name == "" && (lk == "nickname" || lk == "nick_name" || lk == "displayname"):
					if s, ok := val.(string); ok {
						out.Name = s
					}
				case out.ExpiresAt == 0 && (lk == "expiresat" || lk == "expires_at"):
					switch x := val.(type) {
					case float64:
						if x > 1e12 {
							out.ExpiresAt = int64(x)
						} else if x > 1e9 {
							out.ExpiresAt = int64(x) * 1000
						}
					}
				}
				walk(val)
			}
		case []any:
			for _, it := range n {
				walk(it)
			}
		}
	}
	walk(v)
	for _, c := range cookies {
		if c.Name == "ot_session" && out.Access == "" && strings.Count(c.Value, ".") == 2 {
			out.Access = c.Value
		}
		if c.Name == "ot_login" && out.LoginCookie == "" && c.Value != "" {
			out.LoginCookie = c.Value
		}
		ln := strings.ToLower(c.Name)
		if out.Refresh == "" && strings.Contains(ln, "refresh") && c.Value != "" {
			out.Refresh = c.Value
		}
		if out.LoginCookie == "" && strings.Contains(ln, "login") && c.Value != "" {
			out.LoginCookie = c.Value
		}
	}
	return out
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// handleOAuthPoll POST /api/oauth/poll {session_id}
// 旧的「浏览器打开授权链接」流程：扫码完成后直接轮询兑换，不依赖 return_url。
func (p *Panel) handleOAuthPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil || req.SessionID == "" {
		writeJSON(w, 400, map[string]any{"error": "session_id 不能为空"})
		return
	}
	p.mu.Lock()
	st, ok := p.oauth[req.SessionID]
	p.mu.Unlock()
	if !ok {
		writeJSON(w, 200, map[string]any{"status": "expired", "error": "扫码会话不存在或已过期"})
		return
	}
	if time.Since(st.created) > 15*time.Minute {
		p.mu.Lock()
		delete(p.oauth, req.SessionID)
		p.mu.Unlock()
		writeJSON(w, 200, map[string]any{"status": "expired", "error": "扫码会话已过期"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tok, err := p.gw.Client().Exchange(ctx, req.SessionID, st.verifier)
	if err != nil {
		if ae, ok := upstream.AsAPIError(err); ok && ae.Kind == upstream.KindAuth {
			writeJSON(w, 200, map[string]any{"status": "pending"})
			return
		}
		writeJSON(w, 502, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	acct, err := account.FromToken(tok.AccessToken, tok.RefreshToken, "wechat-oauth")
	if err != nil {
		writeJSON(w, 502, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	acct.ExpiresAt = tok.ExpiresAt(time.Now())
	if info, ierr := p.gw.Client().UserInfo(ctx, acct.AccessToken); ierr == nil {
		if info.UserID != "" {
			acct.ID = info.UserID.String()
		}
		if info.NickName != "" {
			acct.Name = info.NickName
		}
	}
	if err := p.gw.Pool().Add(acct); err != nil {
		writeJSON(w, 500, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	p.mu.Lock()
	delete(p.oauth, req.SessionID)
	p.mu.Unlock()
	writeJSON(w, 200, map[string]any{"status": "ok", "account": acct.Masked(), "has_refresh": acct.HasRefresh()})
}

