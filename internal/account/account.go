// Package account 定义 OrcaTerm 账号凭证与落盘格式。
package account

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Account 是一个 OrcaTerm 账号（ot_session JWT + 可选 refresh token）。
type Account struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	// LoginCookie 是 Web 登录的 ot_login Cookie（可反复换 AccessToken）。
	LoginCookie   string `json:"login_cookie,omitempty"`
	// ExpiresAt 访问令牌过期时间（unix 毫秒）。
	ExpiresAt     int64  `json:"expires_at"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     int64  `json:"created_at"`
	LastRefreshAt int64  `json:"last_refresh_at,omitempty"`
	Source        string `json:"source,omitempty"`
	Note          string `json:"note,omitempty"`
}

// Clone 深拷贝。
func (a *Account) Clone() *Account { cp := *a; return &cp }

// HasRefresh 是否有刷新令牌。
func (a *Account) HasRefresh() bool {
	return strings.TrimSpace(a.RefreshToken) != "" || strings.TrimSpace(a.LoginCookie) != ""
}

// Expired 判断访问令牌是否已过期（留 60 秒余量）。
func (a *Account) Expired(now time.Time) bool {
	if a.ExpiresAt <= 0 {
		return false
	}
	return now.Add(60 * time.Second).UnixMilli() >= a.ExpiresAt
}

// NeedsRefresh 提前 45 分钟刷新。
// 实测 refresh token 自身 TTL 约 2 小时（与 access token 同源签发），
// 太晚刷新会撞上 SESSION_EXPIRED，因此留足窗口。
func (a *Account) NeedsRefresh(now time.Time) bool {
	if a.ExpiresAt <= 0 {
		return false
	}
	return now.Add(45 * time.Minute).UnixMilli() >= a.ExpiresAt
}

// Masked 面板展示用脱敏。
func (a *Account) Masked() Account {
	cp := *a
	cp.AccessToken = Mask(cp.AccessToken)
	cp.RefreshToken = Mask(cp.RefreshToken)
	cp.LoginCookie = Mask(cp.LoginCookie)
	return cp
}

// Mask 保留头尾各 6 字符。
func Mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 18 {
		return s[:min(4, len(s))] + "***"
	}
	return s[:6] + "..." + s[len(s)-6:]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// FlexString 兼容 JSON 里的字符串或数字（上游 userId 有时是 number）。
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = FlexString(n.String())
	return nil
}

// String 返回字符串值。
func (f FlexString) String() string { return string(f) }

// Claims 是 JWT 里我们关心的字段。
type Claims struct {
	UserID   FlexString `json:"userId"`
	Provider string     `json:"provider"`
	Nickname string     `json:"providerUsername"`
	Source   string     `json:"source"`
	IssuedAt int64      `json:"iat"`
	Expires  int64      `json:"exp"`
	JTI      string     `json:"jti"`
}

// ParseClaims 解析 JWT payload（不校验签名，仅取元数据）。
func ParseClaims(token string) (*Claims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return nil, errors.New("token 不是合法 JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		if payload, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return nil, fmt.Errorf("JWT payload base64 解码失败: %w", err)
		}
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("JWT payload 解析失败: %w", err)
	}
	return &c, nil
}

// Fingerprint 取 token 的短哈希，用于日志里识别账号而不泄露凭证。
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:4])
}

// FromToken 用访问令牌构造账号：自动解析 userId / 过期时间。
func FromToken(token, refresh, source string) (*Account, error) {
	token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(token), "Bearer "))
	c, err := ParseClaims(token)
	if err != nil {
		return nil, err
	}
	if c.UserID == "" {
		return nil, errors.New("token 缺少 userId")
	}
	exp := c.Expires * 1000
	if exp == 0 {
		exp = time.Now().Add(2 * time.Hour).UnixMilli()
	}
	a := &Account{
		ID:           c.UserID.String(),
		Name:         c.Nickname,
		AccessToken:  token,
		RefreshToken: strings.TrimSpace(refresh),
		ExpiresAt:    exp,
		Enabled:      true,
		CreatedAt:    time.Now().UnixMilli(),
		Source:       source,
	}
	return a, nil
}

// LoadDir 读取目录下所有账号 JSON（*.json）。
func LoadDir(dir string) ([]*Account, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Account
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var a Account
		if err := json.Unmarshal(b, &a); err != nil {
			continue
		}
		if strings.TrimSpace(a.AccessToken) == "" {
			continue
		}
		if a.ID == "" {
			if c, err := ParseClaims(a.AccessToken); err == nil {
				a.ID = c.UserID.String()
			}
		}
		if a.ID == "" {
			continue
		}
		if a.CreatedAt == 0 {
			a.CreatedAt = time.Now().UnixMilli()
		}
		out = append(out, &a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// Save 以 <id>.json 原子落盘。
func Save(dir string, a *Account) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	path := filepath.Join(dir, safeName(a.ID)+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Delete 删除账号文件；不存在不报错。
func Delete(dir, id string) error {
	err := os.Remove(filepath.Join(dir, safeName(id)+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func safeName(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "account"
	}
	return b.String()
}

// LocalDataBin 是本机 OrcaTerm 的 data.bin 结构（只取需要的字段）。
type LocalDataBin struct {
	UserID           FlexString `json:"userId"`
	AccessToken      string     `json:"oauth_access_token"`
	ExpiresAt        int64      `json:"oauth_expires_at"`
	RefreshToken     string     `json:"oauth_refresh_token_v2"`
	AuthIdentity     string     `json:"auth_identity"`
}

// ImportLocal 从 data.bin 导入（refresh token 需要另外从应用 secret 存储读取）。
func ImportLocal(path, refresh string) (*Account, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d LocalDataBin
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("解析 data.bin 失败: %w", err)
	}
	if strings.TrimSpace(d.AccessToken) == "" {
		return nil, errors.New("data.bin 里没有 oauth_access_token，请先在 OrcaTerm 登录")
	}
	a, err := FromToken(d.AccessToken, refresh, "local-import")
	if err != nil {
		return nil, err
	}
	if d.ExpiresAt > 0 {
		a.ExpiresAt = d.ExpiresAt
	}
	if d.UserID != "" {
		a.ID = d.UserID.String()
	}
	if d.AuthIdentity != "" {
		var id struct {
			NickName string `json:"nickName"`
		}
		if json.Unmarshal([]byte(d.AuthIdentity), &id) == nil && id.NickName != "" {
			a.Name = id.NickName
		}
	}
	return a, nil
}

// DefaultLocalDataBin 返回 Windows 上 OrcaTerm 默认 data.bin 路径。
func DefaultLocalDataBin() string {
	if v := os.Getenv("ORCATERM_DATA_BIN"); strings.TrimSpace(v) != "" {
		return v
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return ""
	}
	return filepath.Join(appData, "com.orcaterm-desktop.app", "data.bin")
}

