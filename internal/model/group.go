package model

import "time"

// EnterpriseGroup 企微群 ↔ 客户 ↔ 特性标签映射
type EnterpriseGroup struct {
	GroupID       string    `gorm:"primaryKey;size:255;column:group_id" json:"group_id"`
	GroupName     string    `gorm:"size:255;column:group_name" json:"group_name"`
	OwnerID       string    `gorm:"size:255;column:owner_id" json:"owner_id"`
	CustomerID    string    `gorm:"size:255;column:customer_id" json:"customer_id"`
	CustomerName  string    `gorm:"size:255;column:customer_name" json:"customer_name"`
	RobotID       string    `gorm:"size:255;column:robot_id" json:"robot_id"`                           // 默认回复机器人（多机器人群时用于指定唯一回复者）
	RobotMemberID string    `gorm:"size:255;column:robot_member_id" json:"robot_member_id"`             // 默认回复机器人在群里的 member_id
	RobotIDs      []string  `gorm:"serializer:json;column:robot_ids;default:'[]'" json:"robot_ids"`     // 允许在此群响应的机器人 ID 列表
	DatasetIDs    []string  `gorm:"serializer:json;column:dataset_ids;default:'[]'" json:"dataset_ids"` // kh 知识库 ID 列表，支持多知识库
	FeatureTag    string    `gorm:"type:jsonb;column:feature_tag;default:'{}'" json:"feature_tag"`
	SystemPrompt  string    `gorm:"type:text;column:system_prompt" json:"system_prompt"` // 每个群可以有独立的 system prompt
	Status        int       `gorm:"column:status;default:1" json:"status"`               // 1=正常, 2=解散
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (EnterpriseGroup) TableName() string { return "enterprise_group" }
