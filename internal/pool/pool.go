// Package pool 维护账号池：选择、并发租约、冷却与熔断。
package pool

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"orcaterm2api/internal/account"
	"orcaterm2api/internal/config"
	"orcaterm2api/internal/upstream"
)

var (
	// ErrNoAccount 表示当前没有可用账号。
	ErrNoAccount = errors.New("no available account")
	// ErrAllCooling 表示账号都在冷却。
	ErrAllCooling = errors.New("all accounts are cooling down")
	// ErrRateLimited 表示账号都在本账号频控窗口内。
	ErrRateLimited = errors.New("accounts are rate limited")
)

// Stats 是账号维度的累计统计。
type Stats struct {
	Requests int64 `json:"requests"`
	Errors   int64 `json:"errors"`
	LastUsed int64 `json:"last_used"`
}

// Entry 是账号 + 运行时状态。
type Entry struct {
	acct *account.Account

	mu            sync.Mutex
	inflight      int
	cooldownUntil time.Time
	fails         int
	blockedModels map[string]bool
	stats         Stats
	lastError     string
	refreshing    bool
	lastRefresh   time.Time
	needsLogin    bool

	tokens        float64
	lastFill      time.Time
	nextAvailable time.Time
}

// Account 返回账号副本。
func (e *Entry) Account() account.Account {
	e.mu.Lock()
	defer e.mu.Unlock()
	return *e.acct
}

func (e *Entry) setAccount(a *account.Account) {
	e.mu.Lock()
	e.acct = a
	e.mu.Unlock()
}

// Pool 是账号池。
type Pool struct {
	dir string
	cfg config.PoolConfig

	mu      sync.RWMutex
	entries []*Entry
	byID    map[string]*Entry

	totalInflight int
}

// New 构造账号池。
func New(dir string, cfg config.PoolConfig) *Pool {
	return &Pool{dir: dir, cfg: cfg, byID: map[string]*Entry{}}
}

// Configure 热更新池参数。
func (p *Pool) Configure(cfg config.PoolConfig) {
	p.mu.Lock()
	p.cfg = cfg
	p.mu.Unlock()
}

// Load 从 auths 目录重新加载（保留已有账号的运行时状态）。
func (p *Pool) Load() error {
	accts, err := account.LoadDir(p.dir)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.byID
	next := make([]*Entry, 0, len(accts))
	nextByID := make(map[string]*Entry, len(accts))
	for _, a := range accts {
		if e, ok := old[a.ID]; ok {
			e.setAccount(a)
			next = append(next, e)
			nextByID[a.ID] = e
		} else {
			e := &Entry{acct: a, blockedModels: map[string]bool{}}
			next = append(next, e)
			nextByID[a.ID] = e
		}
	}
	p.entries = next
	p.byID = nextByID
	return nil
}

// Add 新增或更新账号并落盘。
func (p *Pool) Add(a *account.Account) error {
	if err := account.Save(p.dir, a); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byID[a.ID]; ok {
		e.setAccount(a)
		e.ClearNeedsLogin()
		return nil
	}
	e := &Entry{acct: a, blockedModels: map[string]bool{}}
	p.entries = append(p.entries, e)
	p.byID[a.ID] = e
	return nil
}

// Update 保存账号改动（刷新令牌后调用），不改变池成员。
func (p *Pool) Update(a *account.Account) error {
	if err := account.Save(p.dir, a); err != nil {
		return err
	}
	p.mu.RLock()
	e, ok := p.byID[a.ID]
	p.mu.RUnlock()
	if ok {
		e.setAccount(a)
	}
	return nil
}

// Remove 删除账号。
func (p *Pool) Remove(id string) error {
	if err := account.Delete(p.dir, id); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.byID, id)
	out := p.entries[:0]
	for _, e := range p.entries {
		if e.acct.ID != id {
			out = append(out, e)
		}
	}
	p.entries = out
	return nil
}

// Entries 返回当前所有条目（只读遍历用）。
func (p *Pool) Entries() []*Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Entry, len(p.entries))
	copy(out, p.entries)
	return out
}

// ByID 取单个条目。
func (p *Pool) ByID(id string) *Entry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.byID[id]
}

// Lease 是一次账号占用；必须调用 Release/ReleaseOK。
type Lease struct {
	Entry *Entry
	pool  *Pool
	once  sync.Once
	acct  account.Account
	model string
}

// Account 返回租约账号副本。
func (l *Lease) Account() account.Account { return l.acct }

// ModelBlocked 判断账号是否对该模型被标记不可用。
func (e *Entry) ModelBlocked(model string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.blockedModels[model]
}

// blockModel 记录某账号无此模型权限。
func (e *Entry) blockModel(model string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.blockedModels == nil {
		e.blockedModels = map[string]bool{}
	}
	e.blockedModels[model] = true
}

// Pick 选择一个可用账号。
func (p *Pool) Pick(model string, exclude map[string]bool) (*Lease, error) {
	p.mu.RLock()
	cfg := p.cfg
	entries := append([]*Entry(nil), p.entries...)
	totalInflight := p.totalInflight
	p.mu.RUnlock()
	if cfg.MaxInFlightTotal > 0 && totalInflight >= cfg.MaxInFlightTotal {
		return nil, ErrRateLimited
	}

	now := time.Now()
	type cand struct {
		e     *Entry
		score float64
	}
	var cands []cand
	allCooling := false
	rateLimited := false
	for _, e := range entries {
		e.mu.Lock()
		a := e.acct
		if !a.Enabled {
			e.mu.Unlock()
			continue
		}
		if exclude != nil && exclude[a.ID] {
			e.mu.Unlock()
			continue
		}
		if e.inflight >= cfg.MaxInFlight {
			e.mu.Unlock()
			continue
		}
		if now.Before(e.cooldownUntil) {
			allCooling = true
			e.mu.Unlock()
			continue
		}
		if a.Expired(now) && !a.HasRefresh() {
			e.mu.Unlock()
			continue
		}
		if e.blockedModels[model] {
			e.mu.Unlock()
			continue
		}
		if e.needsLogin {
			e.mu.Unlock()
			continue
		}
		if !e.allowLocked(now, cfg) {
			rateLimited = true
			e.mu.Unlock()
			continue
		}
		inflight := e.inflight
		score := 1.0 / float64(1+inflight)
		if cfg.PreferFreshToken && a.ExpiresAt > 0 {
			remain := time.Duration(a.ExpiresAt-now.UnixMilli()) * time.Millisecond
			switch {
			case remain > 90*time.Minute:
				score *= 1.3
			case remain > 30*time.Minute:
				score *= 1.1
			case remain < 5*time.Minute:
				score *= 0.4
			}
		}
		// 少量随机抖动，避免所有请求同时压在同一个账号上。
		score *= 0.9 + rand.Float64()*0.2
		cands = append(cands, cand{e: e, score: score})
		e.mu.Unlock()
	}
	if len(cands) == 0 {
		if allCooling {
			return nil, ErrAllCooling
		}
		if rateLimited {
			return nil, ErrRateLimited
		}
		return nil, ErrNoAccount
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	chosen := cands[0].e
	chosen.mu.Lock()
	chosen.inflight++
	chosen.consumeLocked(now, cfg)
	acct := *chosen.acct
	chosen.stats.Requests++
	chosen.stats.LastUsed = now.UnixMilli()
	chosen.mu.Unlock()

	p.mu.Lock()
	p.totalInflight++
	p.mu.Unlock()
	return &Lease{Entry: chosen, pool: p, acct: acct, model: model}, nil
}

// allowLocked 判断令牌桶是否允许本次请求（调用方需持有 e.mu）。
func (e *Entry) allowLocked(now time.Time, cfg config.PoolConfig) bool {
	if now.Before(e.nextAvailable) {
		return false
	}
	rate := float64(cfg.MaxPerMinute)
	if rate <= 0 {
		return true
	}
	if e.lastFill.IsZero() {
		e.tokens = rate
	} else {
		e.tokens = math.Min(rate, e.tokens+now.Sub(e.lastFill).Minutes()*rate)
	}
	e.lastFill = now
	return e.tokens >= 1
}

// consumeLocked 扣一个令牌并推进最小间隔（调用方需持有 e.mu）。
func (e *Entry) consumeLocked(now time.Time, cfg config.PoolConfig) {
	e.tokens = math.Max(0, e.tokens-1)
	if d := cfg.MinInterval.D(); d > 0 {
		e.nextAvailable = now.Add(d)
	}
}

// ReleaseOK 正常归还。
func (l *Lease) ReleaseOK() { l.release(nil) }
// Release 带错误归还：按错误类型冷却/熔断/封禁模型。
func (l *Lease) Release(err error) { l.release(err) }

func (l *Lease) release(err error) {
	l.once.Do(func() {
		e := l.Entry
		now := time.Now()
		l.pool.mu.RLock()
		cfg := l.pool.cfg
		l.pool.mu.RUnlock()
		e.mu.Lock()
		if e.inflight > 0 {
			e.inflight--
		}
		if err != nil {
			e.stats.Errors++
			e.lastError = err.Error()
			e.fails++
			if ae, ok := upstream.AsAPIError(err); ok {
				switch ae.Kind {
				case upstream.KindModel:
					if e.blockedModels == nil {
						e.blockedModels = map[string]bool{}
					}
					if l.model != "" {
						e.blockedModels[l.model] = true
					}
				case upstream.KindAuth:
					e.cooldownUntil = now.Add(cfg.BreakerCooldown.D())
					e.fails = 0
				case upstream.KindRate:
					d := l.backoff(cfg, e.fails)
					// 上游模型侧 401 实测是「任务并发/频率」闸门，恢复要几分钟，
					// 60s 冷却会导致刚冷却完立刻又撞墙。
					if ae.Code == "UPSTREAM_401" && d < 5*time.Minute {
						d = 5 * time.Minute
					}
					e.cooldownUntil = now.Add(d)
				case upstream.KindProvider:
					e.cooldownUntil = now.Add(l.backoff(cfg, e.fails) / 2)
				default:
					e.cooldownUntil = now.Add(cfg.SoftCooldown.D())
				}
			} else {
				e.cooldownUntil = now.Add(cfg.SoftCooldown.D())
			}
			if e.fails >= cfg.BreakerThreshold {
				cooldown := cfg.BreakerCooldown.D() * time.Duration(e.fails-cfg.BreakerThreshold+1)
				if cooldown > cfg.MaxCooldown.D() {
					cooldown = cfg.MaxCooldown.D()
				}
				e.cooldownUntil = now.Add(cooldown)
			}
		} else {
			e.fails = 0
		}
		e.mu.Unlock()
		l.pool.mu.Lock()
		if l.pool.totalInflight > 0 {
			l.pool.totalInflight--
		}
		l.pool.mu.Unlock()
	})
}

// modelKey 用于把「某账号某模型不可用」记到具体模型上。
func (l *Lease) modelKey() string { return l.model }

func (l *Lease) backoff(cfg config.PoolConfig, fails int) time.Duration {
	base := cfg.SoftCooldown.D()
	if base <= 0 {
		base = 30 * time.Second
	}
	d := base
	for i := 1; i < fails && i < 6; i++ {
		d *= 2
	}
	if max := cfg.MaxCooldown.D(); max > 0 && d > max {
		d = max
	}
	return d
}

// Snapshot 是面板展示的账号状态。
type Snapshot struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	ExpiresAt     int64    `json:"expires_at"`
	InFlight      int      `json:"in_flight"`
	MaxInFlight   int      `json:"max_in_flight"`
	CooldownUntil int64    `json:"cooldown_until"`
	Fails         int      `json:"fails"`
	BlockedModels []string `json:"blocked_models"`
	Requests      int64    `json:"requests"`
	Errors        int64    `json:"errors"`
	LastUsed      int64    `json:"last_used"`
	LastError     string   `json:"last_error"`
	HasRefresh    bool     `json:"has_refresh"`
	Refreshable   bool     `json:"refreshable"`
	Health        string   `json:"health"`
	MaskedToken   string   `json:"masked_token"`
	MaskedRefresh string   `json:"masked_refresh"`
	NeedsLogin    bool     `json:"needs_login"`
}

// Snapshots 返回全部账号状态。
func (p *Pool) Snapshots() []Snapshot {
	p.mu.RLock()
	entries := append([]*Entry(nil), p.entries...)
	cfg := p.cfg
	p.mu.RUnlock()
	now := time.Now()
	out := make([]Snapshot, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		a := e.acct
		cooldownMS := int64(0)
		if !e.cooldownUntil.IsZero() {
			cooldownMS = e.cooldownUntil.UnixMilli()
		}
		s := Snapshot{
			ID:            a.ID,
			Name:          a.Name,
			Enabled:       a.Enabled,
			ExpiresAt:     a.ExpiresAt,
			InFlight:      e.inflight,
			MaxInFlight:   cfg.MaxInFlight,
			CooldownUntil: cooldownMS,
			Fails:         e.fails,
			Requests:      e.stats.Requests,
			Errors:        e.stats.Errors,
			LastUsed:      e.stats.LastUsed,
			LastError:     e.lastError,
			HasRefresh:    a.HasRefresh(),
			Refreshable:   a.HasRefresh(),
			MaskedToken:   account.Mask(a.AccessToken),
			MaskedRefresh: account.Mask(a.RefreshToken),
			BlockedModels: []string{},
			NeedsLogin:    e.needsLogin,
		}
		for m := range e.blockedModels {
			s.BlockedModels = append(s.BlockedModels, m)
		}
		sort.Strings(s.BlockedModels)
		switch {
		case !a.Enabled:
			s.Health = "disabled"
		case e.needsLogin:
			s.Health = "expired"
		case a.Expired(now):
			s.Health = "expired"
		case now.Before(e.cooldownUntil):
			s.Health = "cooling"
		case e.fails >= cfg.BreakerThreshold:
			s.Health = "breaker"
		default:
			s.Health = "healthy"
		}
		e.mu.Unlock()
		out = append(out, s)
	}
	return out
}

// Stats 返回池级汇总。
func (p *Pool) Stats() (accounts, healthy, cooling int, totalRequests, totalErrors int64) {
	p.mu.RLock()
	entries := append([]*Entry(nil), p.entries...)
	p.mu.RUnlock()
	now := time.Now()
	accounts = len(entries)
	for _, e := range entries {
		e.mu.Lock()
		totalRequests += e.stats.Requests
		totalErrors += e.stats.Errors
		switch {
		case !e.acct.Enabled:
		case e.needsLogin:
		case e.acct.Expired(now):
		case now.Before(e.cooldownUntil):
			cooling++
		default:
			healthy++
		}
		e.mu.Unlock()
	}
	return
}

// MarkNeedsLogin 标记账号需要重新登录（refresh token 链已断）。
func (e *Entry) MarkNeedsLogin(msg string) {
	e.mu.Lock()
	e.needsLogin = true
	e.lastError = msg
	e.cooldownUntil = time.Now().Add(24 * time.Hour)
	e.mu.Unlock()
}

// ClearNeedsLogin 在重新导入或刷新成功后清除标记。
func (e *Entry) ClearNeedsLogin() {
	e.mu.Lock()
	e.needsLogin = false
	e.cooldownUntil = time.Time{}
	e.fails = 0
	e.mu.Unlock()
}

// NeedsLogin 返回是否需要重新登录。
func (e *Entry) NeedsLogin() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.needsLogin
}

// MarkRefreshing 标记账号正在刷新，避免重复刷新。
func (e *Entry) MarkRefreshing() bool {	e.mu.Lock()
	defer e.mu.Unlock()
	if e.refreshing {
		return false
	}
	e.refreshing = true
	return true
}

// DoneRefreshing 结束刷新。
func (e *Entry) DoneRefreshing(ok bool) {
	e.mu.Lock()
	e.refreshing = false
	e.lastRefresh = time.Now()
	if ok {
		e.fails = 0
		e.cooldownUntil = time.Time{}
	}
	e.mu.Unlock()
}

// SetEnabled 启停账号并落盘。
func (p *Pool) SetEnabled(id string, enabled bool) error {
	p.mu.RLock()
	e, ok := p.byID[id]
	p.mu.RUnlock()
	if !ok {
		return errors.New("账号不存在")
	}
	e.mu.Lock()
	e.acct.Enabled = enabled
	a := *e.acct
	e.mu.Unlock()
	return account.Save(p.dir, &a)
}

// Save 把账号写回目录（供启停等操作）。
func (p *Pool) Save(a *account.Account) error { return account.Save(p.dir, a) }

