package data

import (
	"log/slog"

	"git.pinquest.cn/ai-customer/internal/config"
	"git.pinquest.cn/ai-customer/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func NewDB(cfg *config.Config) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.Database.DSN), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(
		&model.EnterpriseGroup{},
		&model.Conversation{},
		&model.Message{},
		&model.Robot{},
		&model.GroupMember{},
	); err != nil {
		return nil, err
	}

	slog.Info("database connected and migrated")
	return db, nil
}
