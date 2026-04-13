package model

import "time"

// Conversation 以「群 + 发送者」为维度的会话
type Conversation struct {
	ID           string    `gorm:"primaryKey;size:36;column:id" json:"id"`
	GroupID      string    `gorm:"size:255;not null;column:group_id;index:idx_conv_group_sender_status,priority:1" json:"group_id"`
	SenderID     string    `gorm:"size:255;column:sender_id;index:idx_conv_group_sender_status,priority:2" json:"sender_id"`
	Status       string    `gorm:"size:20;column:status;default:active" json:"status"` // active / closed
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	LastActiveAt time.Time `gorm:"column:last_active_at;autoUpdateTime" json:"last_active_at"`
}

func (Conversation) TableName() string { return "conversation" }
