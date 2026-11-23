package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"orcaterm2api/internal/account"
)

// Tokens 是 OAuth 刷新/兑换结果。
type Tokens struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
}

// ExpiresAt 计算过期时间（unix 毫秒）。
func (t *Tokens) ExpiresAt(now time.Time) int64 {
	if t.ExpiresIn <= 0 {
		return 0
	}
	return now.Add(time.Duration(t.ExpiresIn) * time.Second).UnixMilli()
}

// UserInfo 是账号资料。
type UserInfo struct {
	UserID    account.FlexString `json:"userId"`
	NickName  string `json:"nickName"`
	Username  string `json:"username"`
	Account   string `json:"userAccountType"`
	LoginMode string `json:"userAccountLoginMethod"`
}

type cgiEnvelope struct {
	Data struct {
		Response json.RawMessage `json:"Response"`
	} `json:"data"`
}

type cgiError struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

// cgiCall 调一次 OrcaTerm CGI 接口。
func (c *Client) cgiCall(ctx context.Context, action string, data any, accessToken string) (json.RawMessage, error) {
	cfg := c.Config()
	body, _ := json.Marshal(map[string]any{"action": action, "data": data})
	u := fmt.Sprintf("%s?action=%s&uin=&ownerUin=", cfg.CGIBase, url.QueryEscape(action))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Product", cfg.Product)
	req.Header.Set("X-SeqId", newRequestID())
	req.Header.Set("Origin", "https://orcaterm.com")
	req.Header.Set("Referer", "https://orcaterm.com/")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if accessToken != "" {
		req.Header.Set("Cookie", "ot_session="+accessToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &APIError{Kind: KindNetwork, Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Kind: KindProvider, Message: fmt.Sprintf("cgi %s http %d: %s", action, resp.StatusCode, truncate(string(raw), 200)), HTTPStatus: resp.StatusCode}
	}
	var env cgiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &APIError{Kind: KindProvider, Message: "cgi 响应不是 JSON: " + truncate(string(raw), 200)}
	}
	if len(env.Data.Response) == 0 {
		return nil, &APIError{Kind: KindProvider, Message: "cgi 响应缺少 data.Response"}
	}
	// 先探测错误信封。
	var probe struct {
		Error *cgiError `json:"Error"`
		Code  any       `json:"Code"`
	}
	_ = json.Unmarshal(env.Data.Response, &probe)
	if probe.Error != nil && probe.Error.Code != "" {
		return nil, &APIError{Kind: cgiErrorKind(probe.Error.Code), Code: probe.Error.Code, Message: probe.Error.Message}
	}
	if probe.Code != nil {
		if code := fmt.Sprint(probe.Code); code != "" && code != "0" {
			var msg struct {
				Message string `json:"Message"`
			}
			_ = json.Unmarshal(env.Data.Response, &msg)
			return nil, &APIError{Kind: cgiErrorKind(code), Code: code, Message: msg.Message}
		}
	}
	return env.Data.Response, nil
}

func cgiErrorKind(code string) ErrorKind {
	switch strings.ToUpper(code) {
	case "SESSION_EXPIRED", "INVALID_REFRESH_TOKEN", "TOKEN_EXPIRED", "VERIFY_USER_FAILED", "OAUTH_SESSION_REQUIRED", "UNAUTHORIZED":
		return KindAuth
	case "MISSINGPARAMETER", "INVALIDPARAMETER", "INVALID_REQUEST":
		return KindRequest
	default:
		return KindProvider
	}
}

// Refresh 用 refresh token 换新令牌（会轮换 refresh token，调用方必须持久化）。
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	raw, err := c.cgiCall(ctx, "OAuthRefreshToken", map[string]any{"refreshToken": refreshToken}, "")
	if err != nil {
		return nil, err
	}
	var r struct {
		Code         int    `json:"Code"`
		Message      string `json:"Message"`
		AccessToken  string `json:"AccessToken"`
		RefreshToken string `json:"RefreshToken"`
		TokenType    string `json:"TokenType"`
		ExpiresIn    int    `json:"ExpiresIn"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, &APIError{Kind: KindProvider, Message: "refresh 响应解析失败: " + err.Error()}
	}
	if r.AccessToken == "" {
		return nil, &APIError{Kind: KindAuth, Code: fmt.Sprint(r.Code), Message: "refresh 未返回 AccessToken: " + r.Message}
	}
	return &Tokens{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, TokenType: r.TokenType, ExpiresIn: r.ExpiresIn}, nil
}

// Exchange 用登录会话换令牌（面板 OAuth 扫码登录用）。
func (c *Client) Exchange(ctx context.Context, sessionID, codeVerifier string) (*Tokens, error) {
	payload := map[string]any{"sessionId": sessionID}
	if codeVerifier != "" {
		payload["codeVerifier"] = codeVerifier
	}
	raw, err := c.cgiCall(ctx, "OAuthExchangeToken", payload, "")
	if err != nil {
		return nil, err
	}
	var r struct {
		Code         int    `json:"Code"`
		Message      string `json:"Message"`
		AccessToken  string `json:"AccessToken"`
		RefreshToken string `json:"RefreshToken"`
		TokenType    string `json:"TokenType"`
		ExpiresIn    int    `json:"ExpiresIn"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, &APIError{Kind: KindProvider, Message: "exchange 响应解析失败: " + err.Error()}
	}
	if r.AccessToken == "" {
		return nil, &APIError{Kind: KindAuth, Code: fmt.Sprint(r.Code), Message: "exchange 未返回 AccessToken: " + r.Message}
	}
	return &Tokens{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, TokenType: r.TokenType, ExpiresIn: r.ExpiresIn}, nil
}

// UserInfo 拉取账号资料。
func (c *Client) UserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	raw, err := c.cgiCall(ctx, "GetUserInfo", map[string]any{}, accessToken)
	if err != nil {
		return nil, err
	}
	var u UserInfo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, &APIError{Kind: KindProvider, Message: "GetUserInfo 解析失败: " + err.Error()}
	}
	if u.UserID == "" {
		return nil, &APIError{Kind: KindAuth, Message: "GetUserInfo 未返回 userId"}
	}
	return &u, nil
}

// ConversationList 返回当前账号的会话列表（仅用于核验账号可用）。
func (c *Client) ConversationList(ctx context.Context, accessToken string, limit int) (int, error) {
	cfg := c.Config()
	if limit <= 0 {
		limit = 20
	}
	u := fmt.Sprintf("%s/conversation/list?offset=0&limit=%d&orderBy=ts&order=desc&filter=%s",
		c.Base(), limit, url.QueryEscape(`{"tags":["orcaterm"]}`))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Product", cfg.Product)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, &APIError{Kind: KindNetwork, Message: err.Error()}
	}
	defer resp.Body.Close()
	var payload struct {
		Code int `json:"code"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return 0, &APIError{Kind: KindProvider, Message: "会话列表解析失败: " + err.Error()}
	}
	if payload.Code != 0 {
		return 0, &APIError{Kind: KindProvider, Code: fmt.Sprint(payload.Code), Message: payload.Message}
	}
	return len(payload.Data), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}



// IssueAccessToken 用 Web 登录 Cookie（ot_login）换 AccessToken。
func (c *Client) IssueAccessToken(ctx context.Context, loginCookie string) (*Tokens, error) {
	cfg := c.Config()
	body, _ := json.Marshal(map[string]any{"action": "OAuthIssueAccessToken", "data": map[string]any{}})
	u := fmt.Sprintf("%s?action=OAuthIssueAccessToken&uin=&ownerUin=", cfg.CGIBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Product", cfg.Product)
	req.Header.Set("Origin", "https://orcaterm.com")
	req.Header.Set("Referer", "https://orcaterm.com/")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Cookie", "ot_login="+loginCookie)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &APIError{Kind: KindNetwork, Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Data struct {
			Response struct {
				AccessToken  string `json:"AccessToken"`
				RefreshToken string `json:"RefreshToken"`
				ExpiresIn    int    `json:"ExpiresIn"`
				Error       *cgiError `json:"Error"`
			} `json:"Response"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &APIError{Kind: KindProvider, Message: "IssueAccessToken 解析失败: " + err.Error()}
	}
	if env.Data.Response.Error != nil {
		return nil, &APIError{Kind: cgiErrorKind(env.Data.Response.Error.Code), Code: env.Data.Response.Error.Code, Message: env.Data.Response.Error.Message}
	}
	if env.Data.Response.AccessToken == "" {
		return nil, &APIError{Kind: KindAuth, Message: "IssueAccessToken 未返回 AccessToken"}
	}
	return &Tokens{AccessToken: env.Data.Response.AccessToken, RefreshToken: env.Data.Response.RefreshToken, ExpiresIn: env.Data.Response.ExpiresIn, TokenType: "Bearer"}, nil
}
