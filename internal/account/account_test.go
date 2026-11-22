package account

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func makeJWT(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	enc := base64.RawURLEncoding.EncodeToString(b)
	return "eyJhbGciOiJIUzI1NiJ9." + enc + ".sig"
}

func TestFromTokenNumericUserID(t *testing.T) {
	tok := makeJWT(map[string]any{"userId": 467706832488, "nickname": "n", "exp": 1795347964})
	a, err := FromToken(tok, "rt", "test")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "467706832488" {
		t.Fatalf("id=%q", a.ID)
	}
	if a.ExpiresAt != 1795347964000 {
		t.Fatalf("exp=%d", a.ExpiresAt)
	}
}

func TestImportLocalNumericUserID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	tok := makeJWT(map[string]any{"userId": 42, "exp": 1895347964})
	raw := map[string]any{
		"userId":             42,
		"oauth_access_token": tok,
		"oauth_expires_at":   1795347964000,
		"auth_identity":      `{"nickName":"测试昵称"}`,
	}
	b, _ := json.Marshal(raw)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := ImportLocal(path, "rt")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "42" || a.Name != "测试昵称" {
		t.Fatalf("acct=%+v", a)
	}
}
