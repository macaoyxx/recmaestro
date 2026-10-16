// 指标采集与节点状态同步。
package scheduler

import (
	"context"
	"log"
	"time"

	"recmaestro/internal/metrics"
)

// RunMetricsCollector 启动指标采集循环与节点离线同步循环。
func (s *Server) RunMetricsCollector(ctx context.Context) {
	go s.metricsLoop(ctx)
	go s.nodeSyncLoop(ctx)
}

func (s *Server) metricsLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.collectMetrics(ctx)
		}
	}
}

func (s *Server) collectMetrics(ctx context.Context) {
	counts, err := s.tasks.CountByStatus(ctx)
	if err != nil {
		log.Printf("[warn] 统计任务失败: %v", err)
		return
	}
	// 先重置，清除已消失状态 label 的残留值
	metrics.TasksByStatus.Reset()
	for status, n := range counts {
		metrics.TasksByStatus.WithLabelValues(status).Set(float64(n))
	}
	if n, err := s.queue.QueueLen(ctx); err == nil {
		metrics.QueueSize.Set(float64(n))
	}
	if n, err := s.queue.ScheduledLen(ctx); err == nil {
		metrics.ScheduledSize.Set(float64(n))
	}
}

// nodeSyncLoop 定期把心跳超时的节点置为离线。
func (s *Server) nodeSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncNodeOffline(ctx)
		}
	}
}

func (s *Server) syncNodeOffline(ctx context.Context) {
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		log.Printf("[warn] 节点同步：查询节点列表失败: %v", err)
		return
	}
	for _, n := range nodes {
		alive, err := s.queue.WorkerAlive(ctx, n.NodeID)
		if err != nil {
			// Redis 故障时无法判活：跳过而非按"不在线"误删（节点表删了前端监控即消失，
			// 等 Redis 恢复后的下一轮再判定）
			log.Printf("[warn] 节点同步：查询节点 %s 租约失败，本轮跳过: %v", n.NodeID, err)
			continue
		}
		if !alive {
			// 下线即删除：节点表只保留在线节点，重新上线由 worker 心跳 Upsert 重新注册
			_ = s.nodes.Delete(ctx, n.NodeID)
			log.Printf("节点 %s 心跳超时，已从节点表移除", n.NodeID)
		}
	}
}
