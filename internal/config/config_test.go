package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDurationJSON(t *testing.T) {
	var v struct {
		D Duration `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"d":"90s"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.D.D() != 90*time.Second {
		t.Fatalf("got %v", v.D.D())
	}
	b, _ := json.Marshal(v)
	if string(b) != `{"d":"1m30s"}` {
		t.Fatalf("marshal = %s", b)
	}
}

func TestDefaultNormalize(t *testing.T) {
	c := Default()
	c.Normalize()
	if c.Pool.MinInterval.D() != 3*time.Second || c.Pool.MaxPerMinute != 10 {
		t.Fatalf("pool defaults missing: %+v", c.Pool)
	}
	if c.Upstream.AssistantBase != "https://lightai.cloud.tencent.com/assistant" {
		t.Fatalf("base = %q", c.Upstream.AssistantBase)
	}
	if len(c.Models.Enabled) < 8 {
		t.Fatalf("catalog too small: %d", len(c.Models.Enabled))
	}
	if c.Listen != ":7864" {
		t.Fatalf("listen = %q", c.Listen)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	key, pwd, err := WriteDefault(path)
	if err != nil {
		t.Fatal(err)
	}
	if key == "" || pwd == "" {
		t.Fatal("secrets empty")
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKeys[0] != key || c.Panel.Password != pwd {
		t.Fatal("secrets not persisted")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerHotUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if _, _, err := WriteDefault(path); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(path)
	m := NewManager(path, c)
	if err := m.Update(func(cc *Config) error {
		cc.Pool.MaxInFlight = 5
		cc.Conversation.Mode = "sticky"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot()
	if snap.Pool.MaxInFlight != 5 || snap.Conversation.Mode != "sticky" {
		t.Fatalf("snapshot = %+v", snap.Pool)
	}
	// 落盘生效。
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Pool.MaxInFlight != 5 {
		t.Fatalf("disk max_in_flight = %d", reloaded.Pool.MaxInFlight)
	}
}

func TestValidateRejectsEmptyKeys(t *testing.T) {
	c := Default()
	c.APIKeys = nil
	c.Panel.Password = "x"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v", err)
	}
}

