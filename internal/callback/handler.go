package callback

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/internal/dispatcher"
	"git.pinquest.cn/ai-customer/internal/model"
	"git.pinquest.cn/ai-customer/pkg/crypto"
	"github.com/gin-gonic/gin"
)

// Handler 企微回调网关：验签 → 解密 → 分发
type Handler struct {
	cfg        config.WecomConfig
	dispatcher *dispatcher.Dispatcher
}

func NewHandler(cfg config.WecomConfig, d *dispatcher.Dispatcher) *Handler {
	return &Handler{cfg: cfg, dispatcher: d}
}

func (h *Handler) Handle(c *gin.Context) {
	var p model.CallbackPayload
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	// 1. 验签
	ok, _ := crypto.VerifySignature(p.AppKey, h.cfg.Callback.Token, p.Timestamp, p.Nonce, p.EncodingContent, p.Signature)
	if !ok {
		slog.Warn("callback signature mismatch", "app_key", p.AppKey)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "signature mismatch"})
		return
	}

	// 2. 解密
	plain, err := crypto.AESCBCDecryptBase64(p.EncodingContent, h.cfg.Callback.AESKey)
	if err != nil {
		slog.Error("callback decrypt failed", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "decrypt failed"})
		return
	}

	// 3. 解析 event_type
	var meta struct {
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal([]byte(plain), &meta); err != nil {
		slog.Error("callback unmarshal meta failed", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid event json"})
		return
	}

	slog.Info("callback received", "event_type", meta.EventType)

	// 4. 分发
	if err := h.dispatcher.Dispatch(c.Request.Context(), meta.EventType, []byte(plain)); err != nil {
		slog.Error("callback dispatch failed", "event_type", meta.EventType, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("dispatch error: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
