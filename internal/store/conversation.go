package store

import (
	"context"
	"strings"
	"time"

	"git.pinquest.cn/ai-customer/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ConversationStore struct{ db *gorm.DB }

func NewConversationStore(db *gorm.DB) *ConversationStore { return &ConversationStore{db: db} }

// FindOrCreateActive 查找该群内某个发送者的活跃会话，没有则创建
func (s *ConversationStore) FindOrCreateActive(ctx context.Context, groupID, senderID string) (*model.Conversation, error) {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		senderID = "__group__"
	}

	var conv model.Conversation
	tx := s.db.WithContext(ctx).
		Where("group_id = ? AND sender_id = ? AND status = ?", groupID, senderID, "active").
		Limit(1).
		Find(&conv)

	if tx.Error != nil {
		return nil, tx.Error
	}

	if tx.RowsAffected == 0 {
		conv = model.Conversation{
			ID:           uuid.NewString(),
			GroupID:      groupID,
			SenderID:     senderID,
			Status:       "active",
			LastActiveAt: time.Now(),
		}
		if err := s.db.WithContext(ctx).Create(&conv).Error; err != nil {
			return nil, err
		}
		return &conv, nil
	}

	s.db.WithContext(ctx).Model(&conv).Update("last_active_at", time.Now())
	return &conv, nil
}
