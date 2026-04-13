package store

import (
	"context"
	"time"

	"git.pinquest.cn/ai-customer/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type RobotStore struct{ db *gorm.DB }

func NewRobotStore(db *gorm.DB) *RobotStore { return &RobotStore{db: db} }

// EnsureExists 确保机器人基础记录存在（用于被动事件兜底）
func (s *RobotStore) EnsureExists(ctx context.Context, robotID string) error {
	if robotID == "" {
		return nil
	}

	robot := model.Robot{
		RobotID:     robotID,
		LoginStatus: 2, // 未收到 login.success 前默认为 offline
	}

	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "robot_id"}},
			DoNothing: true,
		}).
		Create(&robot).Error
}

// UpsertOnLogin 登录成功时创建或更新机器人
func (s *RobotStore) UpsertOnLogin(ctx context.Context, data *model.LoginSuccessData, robotID string) error {
	now := time.Now()
	robot := model.Robot{
		RobotID:     robotID,
		Name:        data.Name,
		Avatar:      data.Avatar,
		Phone:       data.Phone,
		Email:       data.Email,
		CorpID:      data.CorpID,
		CorpName:    data.CorpName,
		AccountID:   data.AccountID,
		LoginStatus: 1,
		LastLoginAt: &now,
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "robot_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "avatar", "phone", "email", "corp_id", "corp_name", "account_id", "login_status", "last_login_at", "updated_at"}),
		}).
		Create(&robot).Error
}

// UpdateStatus 更新机器人在线状态
func (s *RobotStore) UpdateStatus(ctx context.Context, robotID string, status int) error {
	return s.db.WithContext(ctx).
		Model(&model.Robot{}).
		Where("robot_id = ?", robotID).
		Update("login_status", status).Error
}

// IncrGroupCount 增减机器人的群数量
func (s *RobotStore) IncrGroupCount(ctx context.Context, robotID string, delta int) error {
	return s.db.WithContext(ctx).
		Model(&model.Robot{}).
		Where("robot_id = ?", robotID).
		Update("group_count", gorm.Expr("GREATEST(0, group_count + ?)", delta)).Error
}

// FindByRobotOrMemberID 通过 robot_id 或 member_id 查找机器人
// 用于在 member.join.group 事件中判断新成员是否是已注册的机器人
func (s *RobotStore) FindByRobotOrMemberID(ctx context.Context, id string) (*model.Robot, error) {
	if id == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var r model.Robot
	err := s.db.WithContext(ctx).
		Where("robot_id = ? OR (member_id != '' AND member_id = ?)", id, id).
		First(&r).Error
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetByRobotID 按 robot_id 查询
func (s *RobotStore) GetByRobotID(ctx context.Context, robotID string) (*model.Robot, error) {
	var r model.Robot
	if err := s.db.WithContext(ctx).Where("robot_id = ?", robotID).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// ListAll 列出全部机器人（管理后台）
func (s *RobotStore) ListAll(ctx context.Context) ([]model.Robot, error) {
	var robots []model.Robot
	err := s.db.WithContext(ctx).Order("updated_at DESC").Find(&robots).Error
	return robots, err
}

// UpsertFromSync 用同步接口返回的数据刷新机器人基础信息
func (s *RobotStore) UpsertFromSync(ctx context.Context, r *model.Robot) error {
	if r == nil || r.RobotID == "" {
		return nil
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "robot_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"name", "avatar", "phone", "email", "member_id", "login_status", "updated_at",
			}),
		}).
		Create(r).Error
}
