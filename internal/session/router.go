// Package session 维护粘性会话到上游 conversationId 的映射。
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type item struct {
	cid      string
	account  string
	lastSeen time.Time
}

// Router 是带 TTL 的会话路由表。
type Router struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[string]item
}

// New 构造路由表。
func New(ttl time.Duration) *Router {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	r := &Router{ttl: ttl, m: map[string]item{}}
	go r.gcLoop()
	return r
}

// Get 查会话绑定的 cid 与账号。
func (r *Router) Get(key string) (cid, accountID string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.m[key]
	if !ok {
		return "", "", false
	}
	if time.Since(it.lastSeen) > r.ttl {
		delete(r.m, key)
		return "", "", false
	}
	it.lastSeen = time.Now()
	r.m[key] = it
	return it.cid, it.account, true
}

// Set 绑定会话。
func (r *Router) Set(key, cid, accountID string) {
	r.mu.Lock()
	r.m[key] = item{cid: cid, account: accountID, lastSeen: time.Now()}
	r.mu.Unlock()
}

// Delete 解除绑定。
func (r *Router) Delete(key string) {
	r.mu.Lock()
	delete(r.m, key)
	r.mu.Unlock()
}

// Len 返回当前会话数。
func (r *Router) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.m)
}

// TTL 返回配置的过期时间。
func (r *Router) TTL() time.Duration { return r.ttl }

func (r *Router) gcLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		cut := time.Now().Add(-r.ttl)
		r.mu.Lock()
		for k, v := range r.m {
			if v.lastSeen.Before(cut) {
				delete(r.m, k)
			}
		}
		r.mu.Unlock()
	}
}

// Key 由 system + 首条 user + 客户端标识派生会话键。
func Key(system, firstUser, userKey string) string {
	h := sha256.New()
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(firstUser))
	h.Write([]byte{0})
	h.Write([]byte(userKey))
	return "s-" + hex.EncodeToString(h.Sum(nil)[:8])
}
