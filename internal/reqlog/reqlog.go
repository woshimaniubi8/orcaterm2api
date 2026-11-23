// Package reqlog 保存最近请求日志（内存环 + 可选 JSONL 归档）。
package reqlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry 是一条请求记录。
type Entry struct {
	ID              string `json:"id"`
	Time            int64  `json:"time"`
	Endpoint        string `json:"endpoint"`
	Method          string `json:"method"`
	Account         string `json:"account,omitempty"`
	Model           string `json:"model,omitempty"`
	UpstreamModel   string `json:"upstream_model,omitempty"`
	Stream          bool   `json:"stream"`
	Status          int    `json:"status"`
	DurationMS      int64  `json:"duration_ms"`
	TTFBMS          int64  `json:"ttfb_ms"`
	PromptChars     int    `json:"prompt_chars"`
	CompletionChars int    `json:"completion_chars"`
	PromptTokens    int     `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens     int     `json:"total_tokens"`
	TokensPerSecond float64 `json:"tokens_per_second"`
	Conversation    string `json:"conversation,omitempty"`
	ClientIP        string `json:"client_ip,omitempty"`
	UserAgent       string `json:"user_agent,omitempty"`
	APIKey          string `json:"api_key,omitempty"`
	Error           string `json:"error,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	Completion      string `json:"completion,omitempty"`
}

// Stats 是日志汇总。
type Stats struct {
	Total        int64            `json:"total"`
	Success      int64            `json:"success"`
	Errors       int64            `json:"errors"`
	Streaming    int64            `json:"streaming"`
	AvgDuration  int64            `json:"avg_duration_ms"`
	AvgTTFB      int64            `json:"avg_ttfb_ms"`
	TotalTokens  int64            `json:"total_tokens"`
	PromptTokens int64            `json:"prompt_tokens"`
	CompletionTokens int64        `json:"completion_tokens"`
	AvgTPS       float64          `json:"avg_tps"`
	// 累计值（跨重启，存 data/usage.json）
	LifetimeRequests   int64   `json:"lifetime_requests"`
	LifetimePrompt     int64   `json:"lifetime_prompt_tokens"`
	LifetimeCompletion int64   `json:"lifetime_completion_tokens"`
	LifetimeTotal      int64   `json:"lifetime_total_tokens"`
	LifetimeAvgTPS     float64 `json:"lifetime_avg_tps"`
	ByModel      map[string]int64 `json:"by_model"`
	ByAccount    map[string]int64 `json:"by_account"`
	Last24h      int64            `json:"last_24h"`
}

// Log 是环形日志。
type Log struct {
	mu             sync.RWMutex
	ring           []Entry
	size           int
	next           int
	full           bool
	archivePath    string
	archiveEnabled bool
	includePrompts bool
	usagePath      string
	saveMu         sync.Mutex
	life           lifetime
}

type lifetime struct {
	Requests   int64   `json:"requests"`
	Prompt     int64   `json:"prompt_tokens"`
	Completion int64   `json:"completion_tokens"`
	Total      int64   `json:"total_tokens"`
	TPSSum     float64 `json:"tps_sum"`
	TPSCount   int64   `json:"tps_count"`
}

// New 构造日志。
func New(size int, archivePath string, archiveEnabled, includePrompts bool) *Log {
	if size <= 0 {
		size = 500
	}
	l := &Log{size: size, ring: make([]Entry, size), archivePath: archivePath, archiveEnabled: archiveEnabled, includePrompts: includePrompts}
	if archivePath != "" {
		dir := filepath.Dir(archivePath)
		if dir == "" || dir == "." {
			dir = "."
		}
		l.usagePath = filepath.Join(dir, "usage.json")
		l.loadUsage()
	}
	return l
}

// Add 写入一条记录。
func (l *Log) Add(e Entry) {
	if e.Time == 0 {
		e.Time = time.Now().UnixMilli()
	}
	if !l.includePrompts {
		e.Prompt = ""
		e.Completion = ""
	}
	l.mu.Lock()
	l.ring[l.next] = e
	l.next = (l.next + 1) % l.size
	if l.next == 0 {
		l.full = true
	}
	l.life.Requests++
	l.life.Prompt += int64(e.PromptTokens)
	l.life.Completion += int64(e.CompletionTokens)
	l.life.Total += int64(e.TotalTokens)
	if e.TokensPerSecond > 0 {
		l.life.TPSSum += e.TokensPerSecond
		l.life.TPSCount++
	}
	l.mu.Unlock()
	l.saveUsage()
	if l.archiveEnabled && l.archivePath != "" {
		l.archive(e)
	}
}

func (l *Log) archive(e Entry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	dir := filepath.Dir(l.archivePath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o750)
	}
	f, err := os.OpenFile(l.archivePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return
	}
	_, _ = f.Write(b)
	_ = f.Close()
}

// List 返回最近的记录（新的在前），可按关键词过滤。
func (l *Log) List(limit int, q string) []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	n := l.next
	if l.full {
		n = l.size
	}
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Entry, 0, limit)
	lower := strings.ToLower(strings.TrimSpace(q))
	for i := 0; i < n && len(out) < limit; i++ {
		idx := l.next - 1 - i
		if idx < 0 {
			idx += l.size
		}
		e := l.ring[idx]
		if lower != "" {
			hay := strings.ToLower(e.Model + " " + e.Account + " " + e.Error + " " + e.ClientIP + " " + e.Endpoint + " " + e.Conversation)
			if !strings.Contains(hay, lower) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// Stats 汇总日志。
func (l *Log) Stats() Stats {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := Stats{ByModel: map[string]int64{}, ByAccount: map[string]int64{}}
	n := l.next
	if l.full {
		n = l.size
	}
	var dur, ttfb, tokens int64
	var tpsCount int64
	var tpsSum float64
	cut := time.Now().Add(-24 * time.Hour).UnixMilli()
	for i := 0; i < n; i++ {
		e := l.ring[i]
		s.Total++
		if e.Status == 200 {
			s.Success++
		} else {
			s.Errors++
		}
		if e.Stream {
			s.Streaming++
		}
		dur += e.DurationMS
		ttfb += e.TTFBMS
		tokens += int64(e.TotalTokens)
		s.PromptTokens += int64(e.PromptTokens)
		s.CompletionTokens += int64(e.CompletionTokens)
		if e.TokensPerSecond > 0 {
			tpsSum += e.TokensPerSecond
			tpsCount++
		}
		if e.Model != "" {
			s.ByModel[e.Model]++
		}
		if e.Account != "" {
			s.ByAccount[e.Account]++
		}
		if e.Time >= cut {
			s.Last24h++
		}
	}
	if s.Total > 0 {
		s.AvgDuration = dur / s.Total
		s.AvgTTFB = ttfb / s.Total
	}
	s.TotalTokens = tokens
	if tpsCount > 0 {
		s.AvgTPS = tpsSum / float64(tpsCount)
	}
	s.LifetimeRequests = l.life.Requests
	s.LifetimePrompt = l.life.Prompt
	s.LifetimeCompletion = l.life.Completion
	s.LifetimeTotal = l.life.Total
	if l.life.TPSCount > 0 {
		s.LifetimeAvgTPS = l.life.TPSSum / float64(l.life.TPSCount)
	}
	return s
}

func (l *Log) loadUsage() {
	if l.usagePath == "" {
		return
	}
	b, err := os.ReadFile(l.usagePath)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &l.life)
}

func (l *Log) saveUsage() {
	if l.usagePath == "" {
		return
	}
	l.mu.RLock()
	snapshot := l.life
	l.mu.RUnlock()
	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	b, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return
	}
	tmp := l.usagePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err == nil {
		_ = os.Rename(tmp, l.usagePath)
	}
}
