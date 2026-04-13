package model

import "time"

// GroupMember 群成员记录
type GroupMember struct {
	ID            uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	GroupID       string    `gorm:"size:255;not null;uniqueIndex:uk_group_member" json:"group_id"`
	MemberID      string    `gorm:"size:255;not null;uniqueIndex:uk_group_member;index" json:"member_id"`
	Nickname      string    `gorm:"size:255" json:"nickname"`
	Role          int       `gorm:"default:3" json:"role"`           // 2=群主, 3=普通成员
	JoinTime      int64     `gorm:"column:join_time;default:0" json:"join_time"`
	InvitorMemberID string  `gorm:"size:255" json:"invitor_member_id"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

func (GroupMember) TableName() string { return "group_member" }
