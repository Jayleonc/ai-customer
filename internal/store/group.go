package store

import (
	"context"
	"errors"
	"slices"

	"git.pinquest.cn/ai-customer/internal/model"
	"gorm.io/gorm"
)

type GroupStore struct{ db *gorm.DB }

func NewGroupStore(db *gorm.DB) *GroupStore { return &GroupStore{db: db} }

// GetByGroupID 获取活跃群
func (s *GroupStore) GetByGroupID(ctx context.Context, groupID string) (*model.EnterpriseGroup, error) {
	var g model.EnterpriseGroup
	if err := s.db.WithContext(ctx).Where("group_id = ? AND status = ?", groupID, 1).First(&g).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

// UpsertFromCallback 回调事件触发的群 upsert
// robotID 会被追加到 robot_ids 列表（不覆盖已有机器人，支持多机器人）
func (s *GroupStore) UpsertFromCallback(ctx context.Context, groupID, groupName, robotID, ownerID string) error {
	var g model.EnterpriseGroup
	err := s.db.WithContext(ctx).Where("group_id = ?", groupID).First(&g).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.WithContext(ctx).Create(&model.EnterpriseGroup{
			GroupID:   groupID,
			GroupName: groupName,
			OwnerID:   ownerID,
			RobotID:   robotID,
			RobotIDs:  []string{robotID},
			Status:    1,
		}).Error
	}
	if err != nil {
		return err
	}

	// 已存在：按需更新字段，追加 robotID
	if groupName != "" {
		g.GroupName = groupName
	}
	if ownerID != "" {
		g.OwnerID = ownerID
	}
	if g.RobotID == "" {
		g.RobotID = robotID
	}
	g.Status = 1
	if !slices.Contains(g.RobotIDs, robotID) {
		g.RobotIDs = append(g.RobotIDs, robotID)
	}
	return s.db.WithContext(ctx).Save(&g).Error
}

// RemoveRobotID 机器人退群时从 robot_ids 中移除
func (s *GroupStore) RemoveRobotID(ctx context.Context, groupID, robotID string) error {
	var g model.EnterpriseGroup
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).First(&g).Error; err != nil {
		return err
	}
	g.RobotIDs = slices.DeleteFunc(g.RobotIDs, func(id string) bool { return id == robotID })
	if g.RobotID == robotID {
		if len(g.RobotIDs) > 0 {
			g.RobotID = g.RobotIDs[0]
		} else {
			g.RobotID = ""
		}
	}
	return s.db.WithContext(ctx).Save(&g).Error
}

// UpdateName 更新群名
func (s *GroupStore) UpdateName(ctx context.Context, groupID, groupName string) error {
	return s.db.WithContext(ctx).
		Model(&model.EnterpriseGroup{}).
		Where("group_id = ?", groupID).
		Update("group_name", groupName).Error
}

// UpdateOwner 更新群主
func (s *GroupStore) UpdateOwner(ctx context.Context, groupID, ownerID string) error {
	return s.db.WithContext(ctx).
		Model(&model.EnterpriseGroup{}).
		Where("group_id = ?", groupID).
		Update("owner_id", ownerID).Error
}

// UpdateStatus 更新群状态 (1=正常, 2=解散)
func (s *GroupStore) UpdateStatus(ctx context.Context, groupID string, status int) error {
	return s.db.WithContext(ctx).
		Model(&model.EnterpriseGroup{}).
		Where("group_id = ?", groupID).
		Update("status", status).Error
}

// UpdateGroupInfo 更新群名和群主（GetRemoteGroup 回调后调用）
func (s *GroupStore) UpdateGroupInfo(ctx context.Context, groupID, groupName, ownerID string) error {
	updates := map[string]any{}
	if groupName != "" {
		updates["group_name"] = groupName
	}
	if ownerID != "" {
		updates["owner_id"] = ownerID
	}
	if len(updates) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Model(&model.EnterpriseGroup{}).
		Where("group_id = ?", groupID).
		Updates(updates).Error
}

// AddRobotIfAbsent 幂等地将 robotID 追加到群的 robot_ids 列表
// 返回 true 表示实际新增（之前不在列表中），用于判断是否需要更新 group_count
func (s *GroupStore) AddRobotIfAbsent(ctx context.Context, groupID, robotID string) (bool, error) {
	var g model.EnterpriseGroup
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).First(&g).Error; err != nil {
		return false, err
	}
	if slices.Contains(g.RobotIDs, robotID) {
		return false, nil // 已存在，跳过
	}
	g.RobotIDs = append(g.RobotIDs, robotID)
	if g.RobotID == "" {
		g.RobotID = robotID
	}
	return true, s.db.WithContext(ctx).Save(&g).Error
}

// Upsert 全量保存（管理后台用）
func (s *GroupStore) Upsert(ctx context.Context, g *model.EnterpriseGroup) error {
	return s.db.WithContext(ctx).Save(g).Error
}

// List 列出所有活跃群
func (s *GroupStore) List(ctx context.Context) ([]model.EnterpriseGroup, error) {
	var groups []model.EnterpriseGroup
	err := s.db.WithContext(ctx).Where("status = ?", 1).Find(&groups).Error
	return groups, err
}

// GetAnyByGroupID 获取任意状态群（管理后台）
func (s *GroupStore) GetAnyByGroupID(ctx context.Context, groupID string) (*model.EnterpriseGroup, error) {
	var g model.EnterpriseGroup
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).First(&g).Error; err != nil {
		return nil, err
	}
	return &g, nil
}

// ListAll 列出所有群（管理后台）
func (s *GroupStore) ListAll(ctx context.Context) ([]model.EnterpriseGroup, error) {
	var groups []model.EnterpriseGroup
	err := s.db.WithContext(ctx).Order("updated_at DESC").Find(&groups).Error
	return groups, err
}
