package router

import (
	"context"
	"log/slog"

	"git.pinquest.cn/ai-customer/internal/callback"
	"git.pinquest.cn/ai-customer/internal/khclient"
	"git.pinquest.cn/ai-customer/internal/store"
	"git.pinquest.cn/ai-customer/internal/wecom"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func SetupRouter(
	callbackHandler *callback.Handler,
	groupStore *store.GroupStore,
	robotStore *store.RobotStore,
	khClient *khclient.Client,
	wecomClient *wecom.Client,
) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(cors.Default())

	registerConfigPageRoutes(r, groupStore, robotStore, khClient, wecomClient)
	go func() {
		if n, err := syncRobotsFromPlatform(context.Background(), wecomClient, robotStore); err != nil {
			slog.Warn("startup robot sync failed", "error", err)
		} else {
			slog.Info("startup robot sync finished", "count", n)
		}
	}()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// 企微回调入口
	r.POST("/callback", callbackHandler.Handle)

	return r
}
