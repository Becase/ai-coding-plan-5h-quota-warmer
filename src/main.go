package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// managementKeyEnv 优先于 config.yaml 中的 management_key。
const managementKeyEnv = "QUOTA_WARMER_MANAGEMENT_KEY"

func main() {
	configPath := flag.String("config", "config.yaml", "YAML 配置文件路径")
	flag.Parse()

	cfg, errConfig := LoadConfigFile(*configPath)
	if errConfig != nil {
		log.Fatalf("加载配置失败: %v", errConfig)
	}

	managementKey := cfg.ManagementKey
	if env := os.Getenv(managementKeyEnv); env != "" {
		managementKey = env
	}

	w := newWarmer(&http.Client{Timeout: 60 * time.Second})
	w.Configure(cfg) // 启动调度循环；启动时不执行预热

	s := newServer(w, managementKey, *configPath)
	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("%s v%s 监听 %s", serviceName, serviceVersion, cfg.Listen)
		if errListen := httpServer.ListenAndServe(); errListen != nil && errListen != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常退出: %v", errListen)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	log.Printf("收到退出信号，正在关闭")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	w.Stop()
	log.Printf("已退出")
}
