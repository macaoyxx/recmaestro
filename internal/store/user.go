package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 用户角色。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// 用户状态。
const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
)

// BuiltinAdminUsername 内置管理员账号（seedAdmin 创建）：不可删除、不可禁用，
// 保证系统永远保有管理入口；其余管理员（含用户自建）均可正常处置。
const BuiltinAdminUsername = "admin"

// User 对应 live_rec_users 表。
type User struct {
	ID               uint64     `gorm:"column:id;primaryKey"`
	Username         string     `gorm:"column:username;uniqueIndex"`
	PasswordHash     string     `gorm:"column:password_hash"`
	Role             string     `gorm:"column:role;default:user"`
	Status           string     `gorm:"column:status;default:enabled"`
	FailedLoginCount int        `gorm:"column:failed_login_count;default:0"`
	LastLoginAt      *time.Time `gorm:"column:last_login_at"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"`
}

// TableName 返回表名，前缀 live_rec_ 与迁移 SQL 保持一致。
func (User) TableName() string { return "live_rec_users" }

// UserStore 封装对 live_rec_users 表的数据访问。
type UserStore struct {
	db *gorm.DB
}

// NewUserStore 创建 UserStore。
func NewUserStore(db *gorm.DB) *UserStore {
	return &UserStore{db: db}
}

// Create 插入用户。
func (s *UserStore) Create(ctx context.Context, u *User) error {
	return s.db.WithContext(ctx).Create(u).Error
}

// GetByUsername 按用户名查询；不存在返回 (nil, nil)。
func (s *UserStore) GetByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := s.db.WithContext(ctx).Where("username = ?", username).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetByID 按 ID 查询；不存在返回 (nil, nil)。
func (s *UserStore) GetByID(ctx context.Context, id uint64) (*User, error) {
	var u User
	err := s.db.WithContext(ctx).First(&u, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// List 返回全部用户，按 ID 升序。
func (s *UserStore) List(ctx context.Context) ([]User, error) {
	var users []User
	err := s.db.WithContext(ctx).Order("id").Find(&users).Error
	return users, err
}

// UpdateStatus 更新用户状态（enabled / disabled）。
func (s *UserStore) UpdateStatus(ctx context.Context, id uint64, status string) error {
	return s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("status", status).Error
}

// UpdatePassword 更新密码哈希。
func (s *UserStore) UpdatePassword(ctx context.Context, id uint64, hash string) error {
	return s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("password_hash", hash).Error
}

// IncrFailedLogin 原子递增失败次数并返回最新值。
func (s *UserStore) IncrFailedLogin(ctx context.Context, id uint64) (int, error) {
	if err := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).
		UpdateColumn("failed_login_count", gorm.Expr("failed_login_count + 1")).Error; err != nil {
		return 0, err
	}
	u, err := s.GetByID(ctx, id)
	if err != nil {
		return 0, err
	}
	if u == nil {
		// GetByID 对不存在用户返回 (nil, nil)，直接取字段会空指针解引用
		return 0, fmt.Errorf("用户 %d 不存在，无法累计登录失败次数", id)
	}
	return u.FailedLoginCount, nil
}

// ResetFailedLogin 登录成功后清零失败次数并记录最后登录时间。
func (s *UserStore) ResetFailedLogin(ctx context.Context, id uint64, at time.Time) error {
	return s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"failed_login_count": 0,
			"last_login_at":      at,
		}).Error
}

// Delete 物理删除用户。审计行的名称快照保证删除后历史审计仍可读出该用户。
func (s *UserStore) Delete(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&User{}).Error
}

// CountOtherEnabledAdmins 统计除指定用户外启用中的管理员数量，
// 供"不能删除最后一个启用的管理员"护栏判断。
func (s *UserStore) CountOtherEnabledAdmins(ctx context.Context, excludeID uint64) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&User{}).
		Where("role = ? AND status = ? AND id != ?", RoleAdmin, StatusEnabled, excludeID).
		Count(&n).Error
	return n, err
}

// Unlock 清零登录失败次数（管理员启用账号或重置密码后调用）。
// 锁定账号 = disabled + failed_login_count≥5，只改状态不清计数会导致账号永久无法登录；
// 不动 last_login_at：重置密码/启用不是一次成功登录。
func (s *UserStore) Unlock(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).
		Update("failed_login_count", 0).Error
}
