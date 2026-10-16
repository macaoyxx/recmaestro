package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// TaskStatus 是任务状态枚举。
type TaskStatus string

// 任务状态枚举值。
const (
	TaskPending      TaskStatus = "pending"
	TaskQueued       TaskStatus = "queued"
	TaskRunning      TaskStatus = "running"
	TaskRecovering   TaskStatus = "recovering"
	TaskCompleted    TaskStatus = "completed"
	TaskRecordFailed TaskStatus = "record_failed"
	TaskMergeFailed  TaskStatus = "merge_failed"
	TaskInterrupted  TaskStatus = "interrupted"
	TaskCancelled    TaskStatus = "cancelled"
	TaskAborted      TaskStatus = "aborted"
)

// String 返回状态字符串。
func (s TaskStatus) String() string { return string(s) }

// Task 对应 live_rec_tasks 表。
type Task struct {
	ID              string     `gorm:"column:id;primaryKey" json:"id"`
	Name            string     `gorm:"column:name" json:"name"`
	CreatedBy       *uint64    `gorm:"column:created_by" json:"created_by"`
	StreamURL       string     `gorm:"column:stream_url" json:"stream_url"`
	StartTime       *time.Time `gorm:"column:start_time" json:"start_time"`
	EndTime         *time.Time `gorm:"column:end_time" json:"end_time"`
	ActualStartTime *time.Time `gorm:"column:actual_start_time" json:"actual_start_time"`
	ActualEndTime   *time.Time `gorm:"column:actual_end_time" json:"actual_end_time"`
	Status          TaskStatus `gorm:"column:status" json:"status"`
	WorkerID        string     `gorm:"column:worker_id" json:"worker_id"`
	Priority        int        `gorm:"column:priority" json:"priority"`
	SegmentSeconds  int        `gorm:"column:segment_seconds" json:"segment_seconds"`
	MaxRetry        int        `gorm:"column:max_retry" json:"max_retry"`
	RetryCount      int        `gorm:"column:retry_count" json:"retry_count"`
	OutputPath      string     `gorm:"column:output_path" json:"output_path"`
	ErrorCode       string     `gorm:"column:error_code" json:"error_code"`
	ErrorMsg        string     `gorm:"column:error_msg" json:"error_msg"`
	LastHeartbeatAt *time.Time `gorm:"column:last_heartbeat_at" json:"last_heartbeat_at"`
	CreatedAt       time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 返回表名。
func (Task) TableName() string { return "live_rec_tasks" }

// TaskStore 封装对 live_rec_tasks 表的数据访问。
type TaskStore struct {
	db *gorm.DB
}

// NewTaskStore 创建 TaskStore。
func NewTaskStore(db *gorm.DB) *TaskStore {
	return &TaskStore{db: db}
}

// Create 插入任务。
func (s *TaskStore) Create(ctx context.Context, t *Task) error {
	return s.db.WithContext(ctx).Create(t).Error
}

// GetByID 按 ID 查询；不存在返回 (nil, nil)。
func (s *TaskStore) GetByID(ctx context.Context, id string) (*Task, error) {
	var t Task
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// List 列出任务，支持状态筛选、名称/地址模糊搜索、分页。
func (s *TaskStore) List(ctx context.Context, status, keyword string, page, pageSize int) ([]Task, int64, error) {
	q := s.db.WithContext(ctx).Model(&Task{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		// OR 必须整体加括号：否则 AND 优先级更高，与 status 条件组合后
		// 被解析为 (status = ? AND name LIKE ?) OR stream_url LIKE ?，
		// 关键词命中其他状态任务的地址时会绕过状态筛选。
		q = q.Where("(name LIKE ? OR stream_url LIKE ?)", like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if pageSize <= 0 {
		pageSize = 10
	}
	if page <= 0 {
		page = 1
	}

	var tasks []Task
	err := q.Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks).Error
	return tasks, total, err
}

// UpdateStatus 更新任务状态。
func (s *TaskStore) UpdateStatus(ctx context.Context, id string, status TaskStatus) error {
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).Update("status", status).Error
}

// MarkQueued 条件置 queued：仅 pending/record_failed/interrupted 允许（定时到点与
// 失败重试的合法入口），防重复投递把 running/queued/recovering 任务打回 queued 造成双录。
// 返回是否成功。
func (s *TaskStore) MarkQueued(ctx context.Context, id string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status IN ?", id, []TaskStatus{TaskPending, TaskRecordFailed, TaskInterrupted}).
		Update("status", TaskQueued)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ListByStatus 按状态查全量任务（队列重建兜底用）。
func (s *TaskStore) ListByStatus(ctx context.Context, status TaskStatus) ([]Task, error) {
	var tasks []Task
	err := s.db.WithContext(ctx).Where("status = ?", status).Find(&tasks).Error
	return tasks, err
}

// Claim 原子认领：仅当 status=queued 时更新为 running 并绑定 worker。
// 返回 true 表示认领成功（防止多 worker 重复认领）。
func (s *TaskStore) Claim(ctx context.Context, id string, workerID string, now time.Time) (bool, error) {
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status = ?", id, TaskQueued).
		Updates(map[string]interface{}{
			"status":            TaskRunning,
			"worker_id":         workerID,
			"actual_start_time": now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// Complete 标记任务完成（无条件；手动合并等人工路径使用，允许从 merge_failed 置回）。
func (s *TaskStore) Complete(ctx context.Context, id string, outputPath string, now time.Time) error {
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":          TaskCompleted,
			"output_path":     outputPath,
			"actual_end_time": now,
		}).Error
}

// CompleteRunning 条件完成任务：仅 running 可置 completed，返回是否生效。
// 收尾链路与自愈补写使用：防止把人工中止/取消等并发处置覆盖回 completed。
// RowsAffected=0 且无错表示任务已被并发处置，视为无需再写（幂等）。
func (s *TaskStore) CompleteRunning(ctx context.Context, id string, outputPath string, now time.Time) (bool, error) {
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status = ?", id, TaskRunning).
		Updates(map[string]interface{}{
			"status":          TaskCompleted,
			"output_path":     outputPath,
			"actual_end_time": now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// Fail 标记任务失败。status 可取 record_failed / merge_failed。
func (s *TaskStore) Fail(ctx context.Context, id string, status TaskStatus, code, msg string) error {
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":     status,
			"error_code": code,
			"error_msg":  msg,
		}).Error
}

// FailWithRetry 标记录制失败并自增 retry_count，一条 UPDATE 原子完成（自动重试用）。
// 仅 running 状态可标记，防覆盖并发的人工中止/取消；返回 (新 retry_count, 是否成功)。
func (s *TaskStore) FailWithRetry(ctx context.Context, id string, code, msg string) (int, bool, error) {
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status = ?", id, TaskRunning).
		Updates(map[string]interface{}{
			"status":      TaskRecordFailed,
			"error_code":  code,
			"error_msg":   msg,
			"retry_count": gorm.Expr("retry_count + 1"),
		})
	if res.Error != nil {
		return 0, false, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, false, nil
	}
	var t Task
	if err := s.db.WithContext(ctx).Select("retry_count").Where("id = ?", id).First(&t).Error; err != nil {
		return 0, true, err
	}
	return t.RetryCount, true, nil
}

// Retry 原子重试：仅 record_failed/interrupted 状态可重试，置 queued 并自增 retry_count。
// 单条条件 UPDATE，天然防并发重复重试；返回 (是否成功, 自增后的 retry_count)。
func (s *TaskStore) Retry(ctx context.Context, id string) (bool, int, error) {
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status IN ?", id, []TaskStatus{TaskRecordFailed, TaskInterrupted}).
		Updates(map[string]interface{}{
			"status":      TaskQueued,
			"retry_count": gorm.Expr("retry_count + 1"),
		})
	if res.Error != nil {
		return false, 0, res.Error
	}
	if res.RowsAffected == 0 {
		return false, 0, nil
	}
	var t Task
	if err := s.db.WithContext(ctx).Select("retry_count").Where("id = ?", id).First(&t).Error; err != nil {
		return true, 0, err
	}
	return true, t.RetryCount, nil
}

// Cancel 取消尚未完成的任务（pending/queued/running/recovering）。
func (s *TaskStore) Cancel(ctx context.Context, id string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status IN ?", id, []TaskStatus{TaskPending, TaskQueued, TaskRunning, TaskRecovering}).
		Update("status", TaskCancelled)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// MarkRecovering 将任务置为 recovering（心跳超时）。
func (s *TaskStore) MarkRecovering(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND status = ?", id, TaskRunning).
		Update("status", TaskRecovering).Error
}

// MarkInterrupted 将长期未恢复的任务置为 interrupted。
func (s *TaskStore) MarkInterrupted(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND status = ?", id, TaskRecovering).
		Update("status", TaskInterrupted).Error
}

// Resume 将 recovering 任务恢复为 running（worker 续租）。
func (s *TaskStore) Resume(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND status = ?", id, TaskRecovering).
		Update("status", TaskRunning).Error
}

// ListRecoveringByWorker 查询某 worker 名下待恢复的任务。
func (s *TaskStore) ListRecoveringByWorker(ctx context.Context, workerID string) ([]Task, error) {
	var tasks []Task
	err := s.db.WithContext(ctx).
		Where("worker_id = ? AND status = ?", workerID, TaskRecovering).
		Find(&tasks).Error
	return tasks, err
}

// ListRunningByWorker 查询某 worker 名下 running 的任务（worker 自愈扫描用）。
func (s *TaskStore) ListRunningByWorker(ctx context.Context, workerID string) ([]Task, error) {
	var tasks []Task
	err := s.db.WithContext(ctx).
		Where("worker_id = ? AND status = ?", workerID, TaskRunning).
		Find(&tasks).Error
	return tasks, err
}

// Update 编辑任务字段（条件更新，仅非运行状态，防并发脏数据）。返回 true 表示更新成功。
func (s *TaskStore) Update(ctx context.Context, id string, updates map[string]interface{}) (bool, error) {
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status NOT IN ?", id, []TaskStatus{TaskRunning, TaskRecovering}).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// Abort 中止任务（running/recovering → aborted）。返回 true 表示中止成功。
func (s *TaskStore) Abort(ctx context.Context, id string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND status IN ?", id, []TaskStatus{TaskRunning, TaskRecovering}).
		Update("status", TaskAborted)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// Delete 删除任务（条件软删，仅非运行状态，防并发脏数据）。返回 true 表示删除成功。
func (s *TaskStore) Delete(ctx context.Context, id string) (bool, error) {
	res := s.db.WithContext(ctx).Where("id = ? AND status NOT IN ?", id, []TaskStatus{TaskRunning, TaskRecovering}).Delete(&Task{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// Heartbeat 更新任务心跳时间。
func (s *TaskStore) Heartbeat(ctx context.Context, id string, now time.Time) error {
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Update("last_heartbeat_at", now).Error
}

// CountByStatus 按状态统计任务数。
func (s *TaskStore) CountByStatus(ctx context.Context) (map[string]int64, error) {
	type row struct {
		Status string
		Count  int64
	}
	var rows []row
	err := s.db.WithContext(ctx).Model(&Task{}).
		Select("status, count(*) AS count").Group("status").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	m := make(map[string]int64, len(rows))
	for _, r := range rows {
		m[r.Status] = r.Count
	}
	return m, nil
}
