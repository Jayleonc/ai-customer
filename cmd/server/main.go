package main

import (
	"log/slog"

	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/pkg/logger"
)

func main() {
	logger.Init()

	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("load config failed", "error", err)
		return
	}

	engine, err := InitializeServer(cfg)
	if err != nil {
		slog.Error("initialize server failed", "error", err)
		return
	}

	slog.Info("starting ai-customer server", "port", cfg.Server.Port)
	if err := engine.Run(":" + cfg.Server.Port); err != nil {
		slog.Error("server exited", "error", err)
	}
}
