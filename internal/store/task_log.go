package store

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"
)

// 日志来源。
const (
	LogSourceScheduler    = "scheduler"
	LogSourceWorker       = "worker"
	LogSourceFFmpegRecord = "ffmpeg-record"
	LogSourceFFmpegMerge  = "ffmpeg-merge"
)

// 日志级别。
const (
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// 生命周期阶段。
const (
	LogStageCreated     = "created"
	LogStageQueued      = "queued"
	LogStageClaimed     = "claimed"
	LogStageStarted     = "started"
	LogStageRecording   = "recording"
	LogStageReconnect   = "reconnect"
	LogStageStopped     = "stopped"
	LogStageMerging     = "merging"
	LogStageMerged      = "merged"
	LogStageValidated   = "validated"
	LogStageCompleted   = "completed"
	LogStageFailed      = "failed"
	LogStageCancelled   = "cancelled"
	LogStageRetry       = "retry"
	LogStageRecovering  = "recovering"
	LogStageInterrupted = "interrupted"
	LogStageAborted     = "aborted"
	LogStageDeleted     = "deleted"
)

// TaskLog 对应 live_rec_task_logs 表，记录任务全生命周期日志。
type TaskLog struct {
	ID       uint64    `gorm:"column:id;primaryKey" json:"id"`
	TaskID   string    `gorm:"column:task_id" json:"task_id"`
	Ts       time.Time `gorm:"column:ts" json:"ts"`
	Level    string    `gorm:"column:level" json:"level"`
	Source   string    `gorm:"column:source" json:"source"`
	Stage    string    `gorm:"column:stage" json:"stage"`
	Message  string    `gorm:"column:message" json:"message"`
	ExitCode *int      `gorm:"column:exit_code" json:"exit_code"`
	NodeID   string    `gorm:"column:node_id" json:"node_id"`
}

// TableName 返回表名。
func (TaskLog) TableName() string { return "live_rec_task_logs" }

// TaskLogStore 封装对 live_rec_task_logs 表的数据访问。
type TaskLogStore struct {
	db *gorm.DB
}

// NewTaskLogStore 创建 TaskLogStore。
func NewTaskLogStore(db *gorm.DB) *TaskLogStore {
	return &TaskLogStore{db: db}
}

// List 按任务查询日志，支持级别/阶段筛选与分页（before 为游标 ID，倒序）。
func (s *TaskLogStore) List(ctx context.Context, taskID string, level, stage string, before int64, limit int) ([]TaskLog, error) {
	if limit <= 0 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Where("task_id = ?", taskID)
	if level != "" {
		q = q.Where("level = ?", level)
	}
	if stage != "" {
		q = q.Where("stage = ?", stage)
	}
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	var logs []TaskLog
	err := q.Order("id DESC").Limit(limit).Find(&logs).Error
	return logs, err
}

// DeleteOlderThan 分批删除早于 cutoff 的任务日志，返回本批删除条数。
// limit 限制单批大小（避免大事务长时间锁表），调用方循环调用直到返回值小于 limit。
// 按 ts 过滤而非 id：ts 是日志的业务时间且带索引前缀（idx_task_ts 以 task_id 打头，
// 纯 ts 过滤走全表扫描，量大时可考虑为 ts 单独建索引）。
func (s *TaskLogStore) DeleteOlderThan(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 5000
	}
	res := s.db.WithContext(ctx).
		Where("ts < ?", cutoff).
		Limit(limit).
		Delete(&TaskLog{})
	return res.RowsAffected, res.Error
}

// HasStage 判断任务是否已有指定阶段的日志。自愈补写用它做铁证判定：
// validated 日志仅在时长对账通过后写入，是"录制内容已验收"的持久化证据。
func (s *TaskLogStore) HasStage(ctx context.Context, taskID string, stage string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&TaskLog{}).
		Where("task_id = ? AND stage = ?", taskID, stage).Count(&count).Error
	return count > 0, err
}

// TaskLogger 是异步批量日志写入器：缓冲日志到 channel，定时/满批时批量落库。
type TaskLogger struct {
	db *gorm.DB
	ch chan *TaskLog
}

// NewTaskLogger 创建 TaskLogger。
func NewTaskLogger(db *gorm.DB) *TaskLogger {
	return &TaskLogger{db: db, ch: make(chan *TaskLog, 2048)}
}

// Start 启动批量写入循环。
func (l *TaskLogger) Start(ctx context.Context) {
	go l.flushLoop(ctx)
}

// Log 异步写入一条日志；缓冲满时丢弃（日志非关键路径，不阻塞业务）。
func (l *TaskLogger) Log(taskID string, level, source, stage, message string, exitCode *int, nodeID string) {
	entry := &TaskLog{
		TaskID:   taskID,
		Ts:       time.Now(),
		Level:    level,
		Source:   source,
		Stage:    stage,
		Message:  message,
		ExitCode: exitCode,
		NodeID:   nodeID,
	}
	select {
	case l.ch <- entry:
	default:
		log.Printf("[warn] 日志缓冲已满，丢弃一条任务 %s 日志", taskID)
	}
}

func (l *TaskLogger) flushLoop(ctx context.Context) {
	batch := make([]*TaskLog, 0, 100)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := l.db.WithContext(ctx).Create(batch).Error; err != nil {
			log.Printf("[warn] 批量写日志失败: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case entry := <-l.ch:
			batch = append(batch, entry)
			if len(batch) >= 100 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
