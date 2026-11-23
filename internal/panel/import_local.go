package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"orcaterm2api/internal/account"
)

// handleImportLocalAuto 从本机 OrcaTerm 导入账号，并自动读取系统密钥存储里的 refresh token。
// 依赖 Node（scripts/cdp_secret.mjs）与运行中的 OrcaTerm；不需要打开桌面授权链接。
func (p *Panel) handleImportLocalAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	path := account.DefaultLocalDataBin()
	acct, err := account.ImportLocal(path, "")
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "hint": "请先在 OrcaTerm 客户端登录"})
		return
	}
	rt, err := p.localRefreshToken()
	if err != nil {
		writeJSON(w, 502, map[string]any{
			"error": "读取本机 refresh token 失败：" + err.Error(),
			"hint":  "请确认 OrcaTerm 客户端已登录并在运行，且本机安装了 Node.js（用于读取系统密钥存储）",
		})
		return
	}
	acct.RefreshToken = rt
	acct.Source = "local-auto"
	if err := p.gw.Pool().Add(acct); err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "account": acct.Masked(), "has_refresh": true})
}

// localRefreshToken 调 scripts/cdp_secret.mjs 读取 oauth_refresh_token_v2。
func (p *Panel) localRefreshToken() (string, error) {
	script := findCDPScript()
	if script == "" {
		return "", errors.New("找不到 scripts/cdp_secret.mjs")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return "", errors.New("未安装 Node.js")
	}
	out, err := runCDPSecret(node, script)
	if err == nil {
		return out, nil
	}
	// CDP 未开：尝试以调试端口拉起 OrcaTerm，再读一次。
	if !cdpAlive() {
		if exe := localOrcaTermExe(); exe != "" {
			cmd := exec.Command(exe)
			cmd.Env = append(os.Environ(), "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9333 --remote-allow-origins=*")
			_ = cmd.Start()
			for i := 0; i < 30; i++ {
				time.Sleep(500 * time.Millisecond)
				if cdpAlive() {
					break
				}
			}
			if cdpAlive() {
				if v, err2 := runCDPSecret(node, script); err2 == nil {
					return v, nil
				}
			}
		}
	}
	return "", err
}

func runCDPSecret(node, script string) (string, error) {
	cmd := exec.Command(node, script, "9333")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	var res struct {
		OK           bool   `json:"ok"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("CDP 输出解析失败: %s", strings.TrimSpace(string(out)))
	}
	if !res.OK || strings.TrimSpace(res.RefreshToken) == "" {
		if res.Error == "" {
			res.Error = "未返回 refresh_token"
		}
		return "", errors.New(res.Error)
	}
	return res.RefreshToken, nil
}

func findCDPScript() string {
	candidates := []string{
		filepath.Join("scripts", "cdp_secret.mjs"),
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "scripts", "cdp_secret.mjs"),
			filepath.Join(dir, "..", "scripts", "cdp_secret.mjs"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

func localOrcaTermExe() string {
	p := filepath.Join(os.Getenv("LOCALAPPDATA"), "OrcaTerm", "OrcaTerm.exe")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func cdpAlive() bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:9333/json/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}
