// 节点监控与任务进度查询。
package scheduler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
)

// handleListNodes 节点列表。
// @Summary      节点列表
// @Description  响应为统一信封，data 为节点数组
// @Tags         监控
// @Produce      json
// @Success      200  {object}  api.Envelope
// @Security     BearerAuth
// @Router       /nodes [get]
func (s *Server) handleListNodes(c *gin.Context) {
	nodes, err := s.nodes.List(c.Request.Context())
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	api.OK(c, nodes)
}

// handleStats 全局统计。
// @Summary      全局统计
// @Description  响应为统一信封，data 含 tasks_by_status / nodes_total / nodes_online / queue_size / scheduled_size
// @Tags         监控
// @Produce      json
// @Success      200  {object}  api.Envelope
// @Security     BearerAuth
// @Router       /stats [get]
func (s *Server) handleStats(c *gin.Context) {
	ctx := c.Request.Context()
	counts, err := s.tasks.CountByStatus(ctx)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	nodes, err := s.nodes.List(ctx)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	online := 0
	for _, n := range nodes {
		if n.Status == "online" {
			online++
		}
	}
	queueSize, _ := s.queue.QueueLen(ctx)
	scheduledSize, _ := s.queue.ScheduledLen(ctx)

	api.OK(c, gin.H{
		"tasks_by_status": counts,
		"nodes_total":     len(nodes),
		"nodes_online":    online,
		"queue_size":      queueSize,
		"scheduled_size":  scheduledSize,
	})
}

// handleTaskProgress 任务进度。
// @Summary      任务进度
// @Description  响应为统一信封，data 含任务状态/绑定节点/段数/产物路径
// @Tags         监控
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  api.Envelope
// @Security     BearerAuth
// @Router       /tasks/{id}/progress [get]
func (s *Server) handleTaskProgress(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	task, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if task == nil {
		api.Fail(c, http.StatusNotFound, "任务不存在")
		return
	}
	files, _ := s.files.ListActiveByTask(ctx, id)
	segments := 0
	for _, f := range files {
		if f.Type == "segment" {
			segments++
		}
	}
	api.OK(c, gin.H{
		"id":            task.ID,
		"status":        task.Status,
		"worker_id":     task.WorkerID,
		"segment_count": segments,
		"output_path":   task.OutputPath,
		"error_msg":     task.ErrorMsg,
	})
}
