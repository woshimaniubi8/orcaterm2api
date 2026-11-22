// Package config 负责配置加载、默认值、热更新与原子落盘。
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Duration 让 JSON 里可以直接写 "30s" / "5m" / "2h"。
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		// 也接受纯数字（纳秒），兼容旧配置。
		var n int64
		if err2 := json.Unmarshal(b, &n); err2 != nil {
			return err
		}
		*d = Duration(n)
		return nil
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UpstreamConfig 是上游（OrcaTerm / LightAI）接入参数。
type UpstreamConfig struct {
	// AssistantBase 是 assistant 服务根地址；国内默认 lightai.cloud.tencent.com。
	AssistantBase string `json:"assistant_base"`
	// CGIBase 是 OrcaTerm 账号体系（OAuth / 用户信息 / 刷新）的接口根。
	CGIBase string `json:"cgi_base"`
	// AssistantName 决定服务端注入哪套系统提示词；orcaterm 为终端运维助手。
	AssistantName string `json:"assistant_name"`
	// Mode 对应客户端 mode 参数：0=RE_ACT（默认）, 1=PLAN_ACT, 2=AUTO_ACT。
	Mode int `json:"mode"`
	// DefaultModel 是请求未指定模型时的默认值。
	DefaultModel string `json:"default_model"`
	// Product 是 X-Product 头，桌面端为 orcaterm。
	Product string `json:"product"`
	// UserAgent 出站 UA；留空使用内置默认。
	UserAgent string `json:"user_agent"`
	// TimeoutSeconds 单次上游请求（含流式）的总超时。
	TimeoutSeconds int `json:"timeout_seconds"`
	// HeaderTimeoutSeconds 等待响应头的超时。
	HeaderTimeoutSeconds int `json:"header_timeout_seconds"`
	// MaxTokens 是给上游 input 的软上限提示（0 = 不传）。
	MaxTokens int `json:"max_tokens"`
}

// ModelConfig 描述一个对外暴露的模型。
type ModelConfig struct {
	ID      string `json:"id"`
	Upstream string `json:"upstream"`
	Label   string `json:"label,omitempty"`
	// Reasoning 表示该模型会产出 thinking 字段（对外暴露 reasoning_content）。
	Reasoning bool `json:"reasoning,omitempty"`
	// ContextLength 仅用于 /v1/models 展示。
	ContextLength int `json:"context_length,omitempty"`
}

type ModelsConfig struct {
	// Enabled 是离线兜底目录（目录接口不可用时使用）。
	Enabled []ModelConfig     `json:"enabled"`
	// CatalogEnabled 启用 Rainbow 在线模型目录（描述/能力/思考等级/价格）。
	CatalogEnabled bool `json:"catalog_enabled"`
	CatalogURL     string `json:"catalog_url"`
	CatalogGroup   string `json:"catalog_group"`
	CatalogRefreshMinutes int `json:"catalog_refresh_minutes"`
	Aliases map[string]string `json:"aliases"`
	// Hidden 不出现在 /v1/models，但允许直接点名调用。
	Hidden []string `json:"hidden"`
}

type PoolConfig struct {
	MaxInFlight      int      `json:"max_in_flight"`
	MaxInFlightTotal int      `json:"max_in_flight_total"`
	BreakerThreshold int      `json:"breaker_threshold"`
	BreakerCooldown  Duration `json:"breaker_cooldown"`
	SoftCooldown     Duration `json:"soft_cooldown"`
	MaxCooldown      Duration `json:"max_cooldown"`
	// PreferFreshToken 让剩余有效期长的账号权重更高。
	PreferFreshToken bool `json:"prefer_fresh_token"`
	// MinInterval 同一账号两次请求的最小间隔（防上游并发/频控）。
	MinInterval Duration `json:"min_interval"`
	// MaxPerMinute 单账号每分钟最多请求数（令牌桶）。
	MaxPerMinute int `json:"max_per_minute"`
}

type ConversationConfig struct {
	// Mode: stateless（默认，每次带全量 history）/ sticky（按会话复用 cid，历史留在服务端）。
	Mode string `json:"mode"`
	// StickyTTL 粘性会话空闲过期时间。
	StickyTTL Duration `json:"sticky_ttl"`
	// MaxHistoryMessages 最多回填多少条历史（不含最后一条）。
	MaxHistoryMessages int `json:"max_history_messages"`
	// MaxHistoryChars 历史文本总长上限。
	MaxHistoryChars int `json:"max_history_chars"`
	// SystemPromptMode: prefix（默认，拼进首条用户消息）/ drop（丢弃 system）。
	SystemPromptMode string `json:"system_prompt_mode"`
}

type LoggingConfig struct {
	RingSize        int    `json:"ring_size"`
	ArchiveEnabled  bool   `json:"archive_enabled"`
	ArchiveFile     string `json:"archive_file"`
	IncludePrompts  bool   `json:"include_prompts"`
}

type PanelConfig struct {
	Enabled       bool   `json:"enabled"`
	Path          string `json:"path"`
	Password      string `json:"password"`
	SessionTTL    Duration `json:"session_ttl"`
	LocalDataBin  string `json:"local_data_bin"`
}

type Config struct {
	Listen        string             `json:"listen"`
	APIKeys       []string           `json:"api_keys"`
	AuthDir       string             `json:"auth_dir"`
	Upstream      UpstreamConfig     `json:"upstream"`
	Models        ModelsConfig       `json:"models"`
	Pool          PoolConfig         `json:"pool"`
	Conversation  ConversationConfig `json:"conversation"`
	Logging       LoggingConfig      `json:"logging"`
	Panel         PanelConfig        `json:"panel"`
}

// DefaultCatalog 是实测可用的模型目录（账号级权限可能不同）。
func DefaultCatalog() []ModelConfig {
	return []ModelConfig{
		{ID: "deepseek-chat-latest", Upstream: "deepseek-chat-latest", Label: "DeepSeek Chat (latest)", ContextLength: 131072},
		{ID: "deepseek-chat", Upstream: "deepseek-chat", Label: "DeepSeek Chat", ContextLength: 131072},
		{ID: "qwen3", Upstream: "qwen3", Label: "Qwen3", ContextLength: 131072},
		{ID: "kimi-latest", Upstream: "kimi-latest", Label: "Kimi Latest", ContextLength: 131072},
		{ID: "kimi-k2", Upstream: "kimi-k2", Label: "Kimi K2", ContextLength: 131072},
		{ID: "kimi-k2-thinking", Upstream: "kimi-k2-thinking", Label: "Kimi K2 Thinking", Reasoning: true, ContextLength: 131072},
		{ID: "kimi-k2.5", Upstream: "kimi-k2.5", Label: "Kimi K2.5", Reasoning: true, ContextLength: 262144},
		{ID: "kimi-k2.5-thinking", Upstream: "kimi-k2.5-thinking", Label: "Kimi K2.5 Thinking", Reasoning: true, ContextLength: 262144},
		{ID: "hunyuan-t1", Upstream: "hunyuan-t1", Label: "Hunyuan T1", Reasoning: true, ContextLength: 131072},
		{ID: "hunyuan-turbos", Upstream: "hunyuan-turbos", Label: "Hunyuan TurboS", ContextLength: 131072},
		{ID: "glm-4.7", Upstream: "glm-4.7", Label: "GLM-4.7", ContextLength: 131072},
		{ID: "glm-4.7-thinking", Upstream: "glm-4.7-thinking", Label: "GLM-4.7 Thinking", Reasoning: true, ContextLength: 131072},
	}
}

// Default 返回推荐配置。
func Default() *Config {
	return &Config{
		Listen:  ":7864",
		APIKeys: []string{},
		AuthDir:  "./auths",
		Upstream: UpstreamConfig{
			AssistantBase:        "https://lightai.cloud.tencent.com/assistant",
			CGIBase:              "https://orcaterm.com/cgi/api",
			AssistantName:        "orcaterm",
			Mode:                 0,
			DefaultModel:         "deepseek-v4.1-flash",
			Product:              "orcaterm",
			TimeoutSeconds:       300,
			HeaderTimeoutSeconds: 120,
		},
		Models: ModelsConfig{
			Enabled:               DefaultCatalog(),
			CatalogEnabled:        true,
			CatalogURL:            "https://dnspod.cloud.tencent.com/ajax/rainbow?action=getRainbowConfig",
			CatalogGroup:          "orcaterm.内置模型",
			CatalogRefreshMinutes: 10,
			Aliases: map[string]string{
				"gpt-4o":       "deepseek-v4.1-flash",
				"gpt-4o-mini":  "deepseek-v4-flash",
				"claude-3-5-sonnet": "glm-5.3",
			},
		},
		Pool: PoolConfig{
			MaxInFlight:      2,
			MaxInFlightTotal: 0,
			BreakerThreshold: 3,
			BreakerCooldown:  Duration(5 * time.Minute),
			SoftCooldown:     Duration(3 * time.Minute),
			MaxCooldown:      Duration(30 * time.Minute),
			PreferFreshToken: true,
			MinInterval:      Duration(3 * time.Second),
			MaxPerMinute:     10,
		},
		Conversation: ConversationConfig{
			Mode:               "stateless",
			StickyTTL:          Duration(30 * time.Minute),
			MaxHistoryMessages: 40,
			MaxHistoryChars:    24000,
			SystemPromptMode:   "prefix",
		},
		Logging: LoggingConfig{
			RingSize:       500,
			ArchiveEnabled: true,
			ArchiveFile:    "./data/requests.jsonl",
			IncludePrompts: false,
		},
		Panel: PanelConfig{
			Enabled:      true,
			Path:         "/panel",
			Password:     "",
			SessionTTL:   Duration(12 * time.Hour),
			LocalDataBin: "",
		},
	}
}

// randomKey 生成 URL 安全随机串。
func randomKey(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// FillSecrets 给空密钥/空面板密码填随机值（首启生成配置时用）。
func (c *Config) FillSecrets() (apiKey, panelPassword string) {
	if len(c.APIKeys) == 0 || strings.TrimSpace(c.APIKeys[0]) == "" {
		c.APIKeys = []string{"sk-orca-" + randomKey(24)}
	}
	if strings.TrimSpace(c.Panel.Password) == "" {
		c.Panel.Password = randomKey(12)
	}
	return c.APIKeys[0], c.Panel.Password
}

// Normalize 修正空值并做边界收敛。
func (c *Config) Normalize() {
	d := Default()
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = d.Listen
	}
	if strings.TrimSpace(c.AuthDir) == "" {
		c.AuthDir = d.AuthDir
	}
	if strings.TrimSpace(c.Upstream.AssistantBase) == "" {
		c.Upstream.AssistantBase = d.Upstream.AssistantBase
	}
	c.Upstream.AssistantBase = strings.TrimRight(c.Upstream.AssistantBase, "/")
	if strings.TrimSpace(c.Upstream.CGIBase) == "" {
		c.Upstream.CGIBase = d.Upstream.CGIBase
	}
	if strings.TrimSpace(c.Upstream.AssistantName) == "" {
		c.Upstream.AssistantName = d.Upstream.AssistantName
	}
	if strings.TrimSpace(c.Upstream.DefaultModel) == "" {
		c.Upstream.DefaultModel = d.Upstream.DefaultModel
	}
	if strings.TrimSpace(c.Upstream.Product) == "" {
		c.Upstream.Product = d.Upstream.Product
	}
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = d.Upstream.TimeoutSeconds
	}
	if c.Upstream.HeaderTimeoutSeconds <= 0 {
		c.Upstream.HeaderTimeoutSeconds = d.Upstream.HeaderTimeoutSeconds
	}
	if len(c.Models.Enabled) == 0 {
		c.Models.Enabled = d.Models.Enabled
	}
	if c.Models.CatalogURL == "" {
		c.Models.CatalogURL = d.Models.CatalogURL
	}
	if c.Models.CatalogGroup == "" {
		c.Models.CatalogGroup = d.Models.CatalogGroup
	}
	if c.Models.CatalogRefreshMinutes <= 0 {
		c.Models.CatalogRefreshMinutes = d.Models.CatalogRefreshMinutes
	}
	if c.Models.Aliases == nil {
		c.Models.Aliases = map[string]string{}
	}
	if c.Pool.MaxInFlight <= 0 {
		c.Pool.MaxInFlight = d.Pool.MaxInFlight
	}
	if c.Pool.BreakerThreshold <= 0 {
		c.Pool.BreakerThreshold = d.Pool.BreakerThreshold
	}
	if c.Pool.BreakerCooldown <= 0 {
		c.Pool.BreakerCooldown = d.Pool.BreakerCooldown
	}
	if c.Pool.SoftCooldown <= 0 {
		c.Pool.SoftCooldown = d.Pool.SoftCooldown
	}
	if c.Pool.MaxCooldown <= 0 {
		c.Pool.MaxCooldown = d.Pool.MaxCooldown
	}
	if c.Pool.MinInterval < 0 {
		c.Pool.MinInterval = d.Pool.MinInterval
	}
	if c.Pool.MaxPerMinute <= 0 {
		c.Pool.MaxPerMinute = d.Pool.MaxPerMinute
	}
	if c.Conversation.Mode == "" {
		c.Conversation.Mode = d.Conversation.Mode
	}
	if c.Conversation.StickyTTL <= 0 {
		c.Conversation.StickyTTL = d.Conversation.StickyTTL
	}
	if c.Conversation.MaxHistoryMessages <= 0 {
		c.Conversation.MaxHistoryMessages = d.Conversation.MaxHistoryMessages
	}
	if c.Conversation.MaxHistoryChars <= 0 {
		c.Conversation.MaxHistoryChars = d.Conversation.MaxHistoryChars
	}
	if c.Conversation.SystemPromptMode == "" {
		c.Conversation.SystemPromptMode = d.Conversation.SystemPromptMode
	}
	if c.Logging.RingSize <= 0 {
		c.Logging.RingSize = d.Logging.RingSize
	}
	if c.Logging.ArchiveFile == "" {
		c.Logging.ArchiveFile = d.Logging.ArchiveFile
	}
	if !c.Panel.Enabled && c.Panel.Path == "" {
		c.Panel.Path = d.Panel.Path
	}
	if c.Panel.Path == "" {
		c.Panel.Path = d.Panel.Path
	}
	if !strings.HasPrefix(c.Panel.Path, "/") {
		c.Panel.Path = "/" + c.Panel.Path
	}
	if c.Panel.SessionTTL <= 0 {
		c.Panel.SessionTTL = d.Panel.SessionTTL
	}
}

// Validate 做启动前校验。
func (c *Config) Validate() error {
	if len(c.APIKeys) == 0 {
		return errors.New("api_keys 不能为空")
	}
	for _, k := range c.APIKeys {
		if strings.TrimSpace(k) == "" {
			return errors.New("api_keys 中存在空值")
		}
	}
	switch c.Conversation.Mode {
	case "stateless", "sticky":
	default:
		return fmt.Errorf("conversation.mode 只能是 stateless 或 sticky，当前 %q", c.Conversation.Mode)
	}
	if c.Panel.Enabled && strings.TrimSpace(c.Panel.Password) == "" {
		return errors.New("panel.password 不能为空（面板启用时）")
	}
	return nil
}

// Load 读取配置文件；不存在返回 fs.ErrNotExist 包装。
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.Normalize()
	return c, nil
}

// Save 原子写回配置（保留未知字段不做处理：本网关配置结构固定）。
func Save(path string, c *Config) error {
	c.Normalize()
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// WriteDefault 生成推荐配置（含随机密钥）并落盘。
func WriteDefault(path string) (apiKey, panelPassword string, err error) {
	c := Default()
	apiKey, panelPassword = c.FillSecrets()
	if err := Save(path, c); err != nil {
		return "", "", err
	}
	return apiKey, panelPassword, nil
}

// Manager 持有当前配置，支持并发快照与热更新。
type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  *Config
}

func NewManager(path string, cfg *Config) *Manager {
	cfg.Normalize()
	return &Manager{path: path, cfg: cfg}
}

func (m *Manager) Path() string { return m.path }

// Snapshot 返回副本，调用方可以随意读取。
func (m *Manager) Snapshot() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := *m.cfg
	cp.APIKeys = append([]string(nil), m.cfg.APIKeys...)
	cp.Models.Enabled = append([]ModelConfig(nil), m.cfg.Models.Enabled...)
	cp.Models.Hidden = append([]string(nil), m.cfg.Models.Hidden...)
	cp.Models.Aliases = map[string]string{}
	for k, v := range m.cfg.Models.Aliases {
		cp.Models.Aliases[k] = v
	}
	return cp
}

// Update 应用修改并落盘；mutate 返回错误则不落盘。
func (m *Manager) Update(mutate func(*Config) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := *m.cfg
	next.APIKeys = append([]string(nil), m.cfg.APIKeys...)
	next.Models.Enabled = append([]ModelConfig(nil), m.cfg.Models.Enabled...)
	next.Models.Hidden = append([]string(nil), m.cfg.Models.Hidden...)
	next.Models.Aliases = map[string]string{}
	for k, v := range m.cfg.Models.Aliases {
		next.Models.Aliases[k] = v
	}
	if err := mutate(&next); err != nil {
		return err
	}
	next.Normalize()
	if err := next.Validate(); err != nil {
		return err
	}
	if err := Save(m.path, &next); err != nil {
		return err
	}
	m.cfg = &next
	return nil
}

// Secret 返回面板登录用的密码（热更新后立即生效）。
func (m *Manager) Secret() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Panel.Password
}
