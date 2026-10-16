package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// 文件类型。
const (
	FileTypeSegment = "segment"
	FileTypeOutput  = "output"
)

// TaskFile 对应 live_rec_task_files 表，记录录制产物的元数据。
type TaskFile struct {
	ID         uint64     `gorm:"column:id;primaryKey" json:"id"`
	TaskID     string     `gorm:"column:task_id" json:"task_id"`
	NodeID     string     `gorm:"column:node_id" json:"node_id"`
	Filename   string     `gorm:"column:filename" json:"filename"`
	Type       string     `gorm:"column:type" json:"type"`
	Path       string     `gorm:"column:path" json:"path"`
	Seq        int        `gorm:"column:seq" json:"seq"`
	SizeBytes  int64      `gorm:"column:size_bytes" json:"size_bytes"`
	DurationMS int64      `gorm:"column:duration_ms" json:"duration_ms"`
	FileHash   string     `gorm:"column:file_hash" json:"file_hash"`
	Status     string     `gorm:"column:status" json:"status"`
	CreatedAt  time.Time  `gorm:"column:created_at" json:"created_at"`
	DeletedAt  *time.Time `gorm:"column:deleted_at" json:"deleted_at"`
}

// TableName 返回表名。
func (TaskFile) TableName() string { return "live_rec_task_files" }

// TaskFileStore 封装对 live_rec_task_files 表的数据访问。
type TaskFileStore struct {
	db *gorm.DB
}

// NewTaskFileStore 创建 TaskFileStore。
func NewTaskFileStore(db *gorm.DB) *TaskFileStore {
	return &TaskFileStore{db: db}
}

// Create 插入文件记录。
func (s *TaskFileStore) Create(ctx context.Context, f *TaskFile) error {
	return s.db.WithContext(ctx).Create(f).Error
}

// CreateOutputIfAbsent 幂等创建产物记录：同 task_id+filename+type 已存在则跳过。
// 产物写库带重试与自愈补写（高并发-3）：重试时上次可能已实际写入，不能产生重复行。
func (s *TaskFileStore) CreateOutputIfAbsent(ctx context.Context, f *TaskFile) error {
	return s.db.WithContext(ctx).
		Where(TaskFile{TaskID: f.TaskID, Filename: f.Filename, Type: f.Type}).
		FirstOrCreate(f).Error
}

// CreateSegmentIfAbsent 幂等创建段记录：同 task_id+seq+type 已存在则跳过。
// 用于续录场景下旧段被重新扫描时避免重复上报。
func (s *TaskFileStore) CreateSegmentIfAbsent(ctx context.Context, f *TaskFile) error {
	return s.db.WithContext(ctx).
		Where(TaskFile{TaskID: f.TaskID, Seq: f.Seq, Type: f.Type}).
		FirstOrCreate(f).Error
}

// GetByID 按 ID 查询文件；不存在返回 (nil, nil)。
func (s *TaskFileStore) GetByID(ctx context.Context, id uint64) (*TaskFile, error) {
	var f TaskFile
	err := s.db.WithContext(ctx).First(&f, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// MarkDeletedByID 按 ID 软删文件记录。
func (s *TaskFileStore) MarkDeletedByID(ctx context.Context, id uint64) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&TaskFile{}).Where("id = ?", id).Update("deleted_at", now).Error
}

// UpdateSegmentSize 更新段文件的 size（录制结束后修正为最终值）。
func (s *TaskFileStore) UpdateSegmentSize(ctx context.Context, taskID string, seq int, size int64) error {
	return s.db.WithContext(ctx).Model(&TaskFile{}).
		Where("task_id = ? AND seq = ? AND type = ?", taskID, seq, FileTypeSegment).
		Update("size_bytes", size).Error
}

// MarkSegmentCorrupt 标记段损坏（合并前预检发现异常截断，隔离改名后不再参与合并与续录）。
func (s *TaskFileStore) MarkSegmentCorrupt(ctx context.Context, taskID string, seq int, path string) error {
	return s.db.WithContext(ctx).Model(&TaskFile{}).
		Where("task_id = ? AND seq = ? AND type = ?", taskID, seq, FileTypeSegment).
		Updates(map[string]interface{}{"status": "corrupt", "path": path}).Error
}

// MarkDeletedByType 软删某任务的指定类型文件记录（如合并后清理 TS 段）。
func (s *TaskFileStore) MarkDeletedByType(ctx context.Context, taskID string, fileType string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&TaskFile{}).
		Where("task_id = ? AND type = ? AND deleted_at IS NULL", taskID, fileType).
		Update("deleted_at", now).Error
}

// ListExpiredSegments 查询某节点上创建早于 cutoff 的未删除段（用于定期兜底清理）。
func (s *TaskFileStore) ListExpiredSegments(ctx context.Context, nodeID string, cutoff time.Time) ([]TaskFile, error) {
	var files []TaskFile
	err := s.db.WithContext(ctx).
		Where("node_id = ? AND type = ? AND deleted_at IS NULL AND created_at < ?", nodeID, FileTypeSegment, cutoff).
		Find(&files).Error
	return files, err
}

// ListActiveByTask 列出任务未删除且未损坏的文件（损坏段已隔离，不参与合并），按 seq 升序。
func (s *TaskFileStore) ListActiveByTask(ctx context.Context, taskID string) ([]TaskFile, error) {
	var files []TaskFile
	err := s.db.WithContext(ctx).
		Where("task_id = ? AND deleted_at IS NULL AND (status IS NULL OR status <> 'corrupt')", taskID).
		Order("seq").Find(&files).Error
	return files, err
}

// ListByTask 列出任务的文件，按 seq 升序。
func (s *TaskFileStore) ListByTask(ctx context.Context, taskID string) ([]TaskFile, error) {
	var files []TaskFile
	err := s.db.WithContext(ctx).Where("task_id = ?", taskID).Order("seq").Find(&files).Error
	return files, err
}

// MaxSegment 返回任务最大的 TS 段（续录用）；无则返回 (nil, nil)。
func (s *TaskFileStore) MaxSegment(ctx context.Context, taskID string) (*TaskFile, error) {
	var f TaskFile
	err := s.db.WithContext(ctx).
		Where("task_id = ? AND type = ?", taskID, FileTypeSegment).
		Order("seq DESC").First(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}
