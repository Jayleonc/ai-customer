package model

import "time"

// Message 群聊消息记录
type Message struct {
	ID             string    `gorm:"primaryKey;size:36;column:id" json:"id"`
	ConversationID string    `gorm:"size:36;not null;column:conversation_id;index" json:"conversation_id"`
	Role           string    `gorm:"size:20;not null;column:role" json:"role"` // user / assistant / system
	SenderID       string    `gorm:"size:255;column:sender_id" json:"sender_id"`
	SenderName     string    `gorm:"size:255;column:sender_name" json:"sender_name"`
	Content        string    `gorm:"type:text;not null;column:content" json:"content"`
	Citations      string    `gorm:"type:jsonb;column:citations;default:'[]'" json:"citations,omitempty"`
	ToolCalls      string    `gorm:"type:jsonb;column:tool_calls;default:'[]'" json:"tool_calls,omitempty"`
	WecomMsgID     string    `gorm:"size:255;column:wecom_msg_id" json:"wecom_msg_id,omitempty"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}

func (Message) TableName() string { return "message" }
