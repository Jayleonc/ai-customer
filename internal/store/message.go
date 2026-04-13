package store

import (
	"context"

	"git.pinquest.cn/ai-customer/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type MessageStore struct{ db *gorm.DB }

func NewMessageStore(db *gorm.DB) *MessageStore { return &MessageStore{db: db} }

// Create 创建消息，返回是否实际插入（用于去重）
func (s *MessageStore) Create(ctx context.Context, msg *model.Message) (bool, error) {
	if msg.ID == "" {
		msg.ID = uuid.NewString()
	}
	if msg.Citations == "" {
		msg.Citations = "[]"
	}
	if msg.ToolCalls == "" {
		msg.ToolCalls = "[]"
	}

	// 企微消息去重
	if msg.WecomMsgID != "" {
		var count int64
		s.db.WithContext(ctx).Model(&model.Message{}).Where("wecom_msg_id = ?", msg.WecomMsgID).Count(&count)
		if count > 0 {
			return false, nil
		}
	}

	if err := s.db.WithContext(ctx).Create(msg).Error; err != nil {
		return false, err
	}
	return true, nil
}

// ListRecent 获取最近 N 条消息（用于注入 Agent 历史）
func (s *MessageStore) ListRecent(ctx context.Context, conversationID string, limit int) ([]model.Message, error) {
	var messages []model.Message
	err := s.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("created_at DESC").
		Limit(limit).
		Find(&messages).Error
	if err != nil {
		return nil, err
	}
	// 反转为时间正序
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

// ListRecentByGroup 获取某个群最近 N 条用户消息（跨会话）
func (s *MessageStore) ListRecentByGroup(ctx context.Context, groupID string, limit int) ([]model.Message, error) {
	var messages []model.Message
	err := s.db.WithContext(ctx).
		Table(`"message" AS m`).
		Select("m.*").
		Joins(`JOIN "conversation" c ON c.id = m.conversation_id`).
		Where("c.group_id = ? AND m.role = ?", groupID, "user").
		Order("m.created_at DESC").
		Limit(limit).
		Find(&messages).Error
	if err != nil {
		return nil, err
	}
	// 反转为时间正序
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

// ListRecentByGroupAndSender 获取某个群内某个发送者最近 N 条用户消息（跨会话）
func (s *MessageStore) ListRecentByGroupAndSender(ctx context.Context, groupID, senderID string, limit int) ([]model.Message, error) {
	var messages []model.Message
	err := s.db.WithContext(ctx).
		Table(`"message" AS m`).
		Select("m.*").
		Joins(`JOIN "conversation" c ON c.id = m.conversation_id`).
		Where("c.group_id = ? AND m.role = ? AND m.sender_id = ?", groupID, "user", senderID).
		Order("m.created_at DESC").
		Limit(limit).
		Find(&messages).Error
	if err != nil {
		return nil, err
	}
	// 反转为时间正序
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}
