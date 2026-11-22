// Command server 启动 orcaterm2api 网关（OpenAI / Anthropic 兼容 + 管理面板）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"orcaterm2api/internal/config"
	"orcaterm2api/internal/gateway"
	"orcaterm2api/internal/panel"
	"orcaterm2api/internal/pool"
	"orcaterm2api/internal/reqlog"
	"orcaterm2api/internal/server"
	"orcaterm2api/internal/session"
	"orcaterm2api/internal/upstream"
	"orcaterm2api/internal/version"
)

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			apiKey, panelPwd, werr := config.WriteDefault(*cfgPath)
			if werr != nil {
				log.Fatalf("生成默认配置失败: %v", werr)
			}
			log.Printf("未找到配置，已生成 %s", *cfgPath)
			log.Printf("API Key     : %s", apiKey)
			log.Printf("面板密码    : %s", panelPwd)
			cfg, err = config.Load(*cfgPath)
		}
		if err != nil {
			log.Fatalf("加载配置失败: %v", err)
		}
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("配置校验失败: %v", err)
	}

	mgr := config.NewManager(*cfgPath, cfg)

	p := pool.New(cfg.AuthDir, cfg.Pool)
	if err := p.Load(); err != nil {
		log.Fatalf("加载账号失败: %v", err)
	}
	accounts, healthy, _, _, _ := p.Stats()
	log.Printf("账号池: %d 个账号（可用 %d），目录 %s", accounts, healthy, cfg.AuthDir)

	client := upstream.New(cfg.Upstream)
	sess := session.New(cfg.Conversation.StickyTTL.D())
	gw := gateway.New(mgr, p, client, sess)
	// 在线模型目录（Rainbow）：启动时拉一次，失败回退配置内置目录。
	{
		rctx, rcancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := gw.RefreshCatalog(rctx); err != nil {
			log.Printf("模型目录拉取失败（使用内置目录）: %v", err)
		} else {
			n, _, _, _ := gw.Catalog().Info()
			log.Printf("模型目录已加载: %d 个模型", n)
		}
		rcancel()
	}

	if cfg.Logging.ArchiveFile != "" {
		dir := filepath.Dir(cfg.Logging.ArchiveFile)
		if dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0o750)
		}
	}
	logs := reqlog.New(cfg.Logging.RingSize, cfg.Logging.ArchiveFile, cfg.Logging.ArchiveEnabled, cfg.Logging.IncludePrompts)

	api := server.New(mgr, gw, logs)
	mux := http.NewServeMux()
	mux.Handle("/", api.Handler())

	panelPath := ""
	if cfg.Panel.Enabled {
		panel.New(mgr, gw, logs).Register(mux)
		panelPath = cfg.Panel.Path
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	gw.StartRefresher(ctx, 15*time.Minute)
	gw.StartCatalogRefresher(ctx, time.Duration(cfg.Models.CatalogRefreshMinutes)*time.Minute)

	go func() {
		log.Printf("orcaterm2api %s 监听 %s", version.Version, cfg.Listen)
		log.Printf("OpenAI 兼容: http://%s/v1  (Authorization: Bearer <api_key>)", displayHost(cfg.Listen))
		if panelPath != "" {
			log.Printf("管理面板  : http://%s%s/", displayHost(cfg.Listen), panelPath)
		}
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("收到退出信号，正在关闭…")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}

func displayHost(listen string) string {
	if listen == "" {
		return "127.0.0.1:7864"
	}
	if listen[0] == ':' {
		return "127.0.0.1" + listen
	}
	if len(listen) > 4 && listen[:4] == "0.0.0.0" {
		return "127.0.0.1" + listen[4:]
	}
	return listen
}

var _ = fmt.Sprintf

