package model

import "time"

// Robot 机器人状态
type Robot struct {
	RobotID     string     `gorm:"primaryKey;size:255;column:robot_id" json:"robot_id"`
	Name        string     `gorm:"size:255;column:name" json:"name"`
	Avatar      string     `gorm:"size:1024;column:avatar" json:"avatar,omitempty"`
	Phone       string     `gorm:"size:64;column:phone" json:"phone,omitempty"`
	Email       string     `gorm:"size:255;column:email" json:"email,omitempty"`
	CorpID      string     `gorm:"size:255;column:corp_id" json:"corp_id,omitempty"`
	CorpName    string     `gorm:"size:255;column:corp_name" json:"corp_name,omitempty"`
	AccountID   string     `gorm:"size:255;column:account_id" json:"account_id,omitempty"`
	MemberID    string     `gorm:"size:255;column:member_id" json:"member_id,omitempty"` // 机器人作为群成员时的 ID
	LoginStatus int        `gorm:"column:login_status;default:2" json:"login_status"`    // 1=online, 2=offline
	GroupCount  int        `gorm:"column:group_count;default:0" json:"group_count"`
	LastLoginAt *time.Time `gorm:"column:last_login_at" json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (Robot) TableName() string { return "robot" }
