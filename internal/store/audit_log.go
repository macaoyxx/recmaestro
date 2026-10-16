package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// 审计资源类型（枚举：key 落库，前端按 key 取展示名）。
const (
	AuditResourceUser = "user"
	AuditResourceTask = "task"
)

// 常用审计动作（写入点约定，新增动作请在此登记）。
const (
	AuditLoginSuccess = "login_success"       // 登录成功
	AuditLoginFail    = "login_fail"          // 密码错误
	AuditLoginLocked  = "login_locked"        // 失败次数达阈值账号锁定
	AuditUserCreate   = "user_create"         // 创建用户
	AuditUserEnable   = "user_enable"         // 启用用户（含解锁）
	AuditUserDisable  = "user_disable"        // 禁用用户
	AuditUserResetPwd = "user_reset_password" // 管理员重置密码
	AuditUserDelete   = "user_delete"         // 删除用户
	AuditTaskEdit     = "task_edit"           // 编辑任务
	AuditTaskAbort    = "task_abort"          // 中止任务
	AuditTaskDelete   = "task_delete"         // 删除任务
	AuditFileMerge    = "file_merge"          // 手动合并段
	AuditFileDelete   = "file_delete"         // 删除文件
)

// AuditLog 对应 live_rec_audit_logs 表，记录安全与管理类操作审计。
// 对象三元组：Resource(类型枚举 key，前端取展示名) + ResourceID + ResourceName——
// ResourceName 是唯一通用的对象名称列（用户→用户名、任务→任务名，新增资源类型复用，零加列）。
// OperatorName 为操作者名称快照（与 UserID 配对）。名称写入时落库：资源日后被删
// 除，审计行仍能读出名称。
type AuditLog struct {
	ID           uint64    `gorm:"column:id;primaryKey" json:"id"`
	UserID       *uint64   `gorm:"column:user_id" json:"user_id"`
	OperatorName string    `gorm:"column:operator_name" json:"operator_name"`
	Action       string    `gorm:"column:action" json:"action"`
	Resource     string    `gorm:"column:resource" json:"resource"`
	ResourceID   string    `gorm:"column:resource_id" json:"resource_id"`
	ResourceName string    `gorm:"column:resource_name" json:"resource_name"`
	Detail       string    `gorm:"column:detail" json:"detail"`
	IP           string    `gorm:"column:ip" json:"ip"`
	UserAgent    string    `gorm:"column:user_agent" json:"user_agent"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
}

// TableName 返回表名，前缀 live_rec_ 与迁移 SQL 保持一致。
func (AuditLog) TableName() string { return "live_rec_audit_logs" }

// AuditStore 封装对 live_rec_audit_logs 表的数据访问。
type AuditStore struct {
	db *gorm.DB
}

// NewAuditStore 创建 AuditStore。
func NewAuditStore(db *gorm.DB) *AuditStore {
	return &AuditStore{db: db}
}

// Create 插入审计日志。
func (s *AuditStore) Create(ctx context.Context, a *AuditLog) error {
	return s.db.WithContext(ctx).Create(a).Error
}

// List 分页查询审计日志，按时间倒序（新事件在前）。
func (s *AuditStore) List(ctx context.Context, page, pageSize int) ([]AuditLog, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&AuditLog{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	if page <= 0 {
		page = 1
	}
	var logs []AuditLog
	err := s.db.WithContext(ctx).Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error
	return logs, total, err
}
