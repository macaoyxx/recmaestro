// 任务日志定期清理：live_rec_task_logs 只增不删会无限膨胀，按 retention_days 分批删除。
package scheduler

import (
	"context"
	"log"
	"time"
)

// logCleanupInterval 清理巡检周期：1 小时足够及时（保留天数是粗粒度语义）。
const logCleanupInterval = time.Hour

// logCleanupBatch 单批删除上限：分批提交避免大事务长时间锁表。
const logCleanupBatch = 5000

// RunLogCleanup 启动任务日志定期清理循环（随 ctx 取消退出）。
// log.retention_days ≤0 视为显式关闭，启动时告警一次。
func (s *Server) RunLogCleanup(ctx context.Context) {
	if s.cfg.Log.RetentionDays <= 0 {
		log.Println("[warn] log.retention_days 未配置或为 0，任务日志定期清理未启用（日志表将持续增长）")
		return
	}
	go s.logCleanupLoop(ctx)
}

func (s *Server) logCleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(logCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupOldTaskLogs(ctx)
		}
	}
}

// cleanupOldTaskLogs 删除超过保留期的任务日志，循环分批直到不足一批。
func (s *Server) cleanupOldTaskLogs(ctx context.Context) {
	cutoff := time.Now().AddDate(0, 0, -s.cfg.Log.RetentionDays)
	for {
		n, err := s.logs.DeleteOlderThan(ctx, cutoff, logCleanupBatch)
		if err != nil {
			log.Printf("[warn] 清理过期任务日志失败（cutoff=%s）: %v", cutoff.Format(time.RFC3339), err)
			return
		}
		if n < logCleanupBatch {
			if n > 0 {
				log.Printf("已清理 %d 条过期任务日志（保留 %d 天）", n, s.cfg.Log.RetentionDays)
			}
			return
		}
	}
}
