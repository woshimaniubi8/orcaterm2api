package panel

import (
	"net/http"
	"regexp"
	"testing"
)

func TestNewUUIDFormat(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for i := 0; i < 20; i++ {
		if !re.MatchString(newUUID()) {
			t.Fatalf("bad uuid: %s", newUUID())
		}
	}
}

func TestExtractTokenBundleFromJSON(t *testing.T) {
	payload := map[string]any{
		"code": 0,
		"data": map[string]any{
			"accessToken":  "eyJhbGciOiJIUzI1NiJ9.eyJ1c2VySWQiOjF9.sig",
			"refreshToken": "dcf4baee-9a74-446d-85dd-91760a8139ca.0.GuNJFkQBr5V1l2Y2F9LTX1w-s5VwDKO71cxeR6HYsos",
			"userInfo":     map[string]any{"nickName": "测试"},
			"expiresIn":    7200,
		},
	}
	b := extractTokenBundle(payload, nil)
	if b.Access == "" || b.Refresh == "" || b.Name != "测试" {
		t.Fatalf("bundle=%+v", b)
	}
}

func TestExtractTokenBundleFromCookie(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJ1c2VySWQiOjF9.sig"
	cookies := []*http.Cookie{{Name: "ot_session", Value: jwt}}
	b := extractTokenBundle(map[string]any{"code": 0}, cookies)
	if b.Access != jwt {
		t.Fatalf("access=%q", b.Access)
	}
}

func TestMaskKey(t *testing.T) {
	if got := maskKey("sk-orca-1234567890"); got != "sk-orca-...7890" {
		t.Fatalf("mask=%q", got)
	}
}
