package store

import (
	"context"

	"git.pinquest.cn/ai-customer/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GroupMemberStore struct{ db *gorm.DB }

func NewGroupMemberStore(db *gorm.DB) *GroupMemberStore { return &GroupMemberStore{db: db} }

// Upsert 插入或更新群成员
// nickname 只在有值时才更新，避免空值覆盖已有昵称
func (s *GroupMemberStore) Upsert(ctx context.Context, m *model.GroupMember) error {
	updateCols := []string{"role", "updated_at"}
	if m.Nickname != "" {
		updateCols = append(updateCols, "nickname")
	}
	if m.JoinTime != 0 {
		updateCols = append(updateCols, "join_time")
	}
	if m.InvitorMemberID != "" {
		updateCols = append(updateCols, "invitor_member_id")
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "group_id"}, {Name: "member_id"}},
			DoUpdates: clause.AssignmentColumns(updateCols),
		}).
		Create(m).Error
}

// Remove 移除指定群成员
func (s *GroupMemberStore) Remove(ctx context.Context, groupID string, memberIDs []string) error {
	if len(memberIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("group_id = ? AND member_id IN ?", groupID, memberIDs).
		Delete(&model.GroupMember{}).Error
}

// ClearByGroup 清空群所有成员（群解散时用）
func (s *GroupMemberStore) ClearByGroup(ctx context.Context, groupID string) error {
	return s.db.WithContext(ctx).
		Where("group_id = ?", groupID).
		Delete(&model.GroupMember{}).Error
}

// GetByMemberID 获取指定群成员
func (s *GroupMemberStore) GetByMemberID(ctx context.Context, groupID, memberID string) (*model.GroupMember, error) {
	var m model.GroupMember
	err := s.db.WithContext(ctx).
		Where("group_id = ? AND member_id = ?", groupID, memberID).
		First(&m).Error
	return &m, err
}

// ListByGroup 列出群所有成员
func (s *GroupMemberStore) ListByGroup(ctx context.Context, groupID string) ([]model.GroupMember, error) {
	var members []model.GroupMember
	err := s.db.WithContext(ctx).
		Where("group_id = ?", groupID).
		Order("role DESC, join_time ASC").
		Find(&members).Error
	return members, err
}
