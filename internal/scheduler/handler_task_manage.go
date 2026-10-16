package scheduler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
	"recmaestro/internal/store"
)

// editableStatus 判断任务是否可编辑（非运行中）。
func editableStatus(status store.TaskStatus) bool {
	return status != store.TaskRunning && status != store.TaskRecovering
}

type updateTaskRequest struct {
	Name      string `json:"name"`
	StreamURL string `json:"stream_url"`
	// 开始时间（RFC3339），覆盖式：传 null 或空串清空改回立即执行
	StartTime OptionalTime `json:"start_time"`
	// 截止时间（RFC3339），必填，须晚于当前时间；start_time 非空时须晚于开始时间
	EndTime        *time.Time `json:"end_time"`
	SegmentSeconds int        `json:"segment_seconds"`
}

// handleUpdateTask 编辑任务（仅非运行状态可编辑）。
// @Summary      编辑任务
// @Description  时间字段为覆盖式更新：提交什么存什么，start_time 传 null 即清空改回立即执行；end_time 必填且须晚于当前时间与 start_time；segment_seconds 非 0 须在 5-3600。
// @Tags         任务
// @Accept       json
// @Produce      json
// @Param        id    path  int                true "任务 ID"
// @Param        body  body  updateTaskRequest  true "编辑参数"
// @Success      200   {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks/{id} [put]
func (s *Server) handleUpdateTask(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	task, err := s.tasks.GetByID(ctx, id)
	if err != nil || task == nil {
		api.Fail(c, http.StatusNotFound, "任务不存在")
		return
	}
	if !editableStatus(task.Status) {
		api.Fail(c, http.StatusConflict, "任务运行中不可编辑，请先中止")
		return
	}

	var req updateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	// 时间字段为覆盖式语义：提交什么存什么（start_time 为 null/空串即清空改回立即执行），
	// 因此 req 中的时间值就是最终值，直接校验
	if msg := validateTaskTimes(req.StartTime.Time, req.EndTime, time.Now()); msg != "" {
		api.Fail(c, http.StatusBadRequest, msg)
		return
	}
	if msg := validateSegmentSeconds(req.SegmentSeconds); msg != "" {
		api.Fail(c, http.StatusBadRequest, msg)
		return
	}

	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.StreamURL != "" {
		updates["stream_url"] = req.StreamURL
	}
	updates["start_time"] = req.StartTime.Time
	updates["end_time"] = req.EndTime
	if req.SegmentSeconds > 0 {
		updates["segment_seconds"] = req.SegmentSeconds
	}
	ok, err := s.tasks.Update(ctx, id, updates)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if !ok {
		api.Fail(c, http.StatusConflict, "任务已开始录制，不可编辑")
		return
	}

	// pending/queued 任务编辑后按新 start_time 重新调度
	if task.Status == store.TaskPending || task.Status == store.TaskQueued {
		_ = s.queue.RemoveSchedule(ctx, id)
		now := time.Now()
		startAt := req.StartTime.Time
		if startAt != nil && startAt.After(now) {
			if task.Status == store.TaskQueued {
				// 移出执行队列改回 pending 等定时触发；状态用条件更新（拒 running），
				// 若期间被 worker 认领则以认领为准（worker 认领后会自行等待 start_time）
				_ = s.queue.RemoveEnqueued(ctx, id)
				if ok, _ := s.tasks.Update(ctx, id, map[string]interface{}{"status": store.TaskPending}); ok {
					_ = s.queue.Schedule(ctx, id, *startAt)
				}
			} else {
				_ = s.queue.Schedule(ctx, id, *startAt)
			}
		} else if task.Status == store.TaskPending {
			// 立即执行：pending → queued 入队（queued 已在执行队列，不动）
			_ = s.tasks.UpdateStatus(ctx, id, store.TaskQueued)
			_ = s.queue.Enqueue(ctx, id)
		}
	}

	s.logger.Log(id, store.LogLevelInfo, store.LogSourceScheduler, store.LogStageCreated, "编辑任务", nil, "")
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: store.AuditTaskEdit, Resource: store.AuditResourceTask, ResourceID: id, ResourceName: task.Name, Detail: "编辑任务"})
	api.OKMsg(c, gin.H{"id": id}, "已更新")
}

// handleAbortTask 中止任务（running/recovering → aborted）。
// @Summary      中止任务
// @Tags         任务
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks/{id}/abort [post]
func (s *Server) handleAbortTask(c *gin.Context) {
	id := c.Param("id")
	task, err := s.tasks.GetByID(c.Request.Context(), id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if task == nil {
		api.Fail(c, http.StatusNotFound, "任务不存在")
		return
	}
	ok, err := s.tasks.Abort(c.Request.Context(), id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if !ok {
		api.Fail(c, http.StatusConflict, "任务当前状态不可中止")
		return
	}
	s.logger.Log(id, store.LogLevelWarn, store.LogSourceScheduler, store.LogStageAborted, "中止任务", nil, "")
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: store.AuditTaskAbort, Resource: store.AuditResourceTask, ResourceID: id, ResourceName: task.Name, Detail: "中止任务"})
	api.OKMsg(c, gin.H{"id": id, "status": store.TaskAborted}, "已中止")
}

// handleDeleteTask 删除任务（运行中需先中止）。
// @Summary      删除任务
// @Tags         任务
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks/{id} [delete]
func (s *Server) handleDeleteTask(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	task, err := s.tasks.GetByID(ctx, id)
	if err != nil || task == nil {
		api.Fail(c, http.StatusNotFound, "任务不存在")
		return
	}
	if task.Status == store.TaskRunning || task.Status == store.TaskRecovering {
		api.Fail(c, http.StatusConflict, "任务运行中不可删除，请先中止")
		return
	}
	ok, err := s.tasks.Delete(ctx, id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if !ok {
		api.Fail(c, http.StatusConflict, "任务已开始录制，不可删除")
		return
	}
	_ = s.queue.RemoveHeartbeat(ctx, id)
	_ = s.queue.RemoveSchedule(ctx, id)
	s.logger.Log(id, store.LogLevelInfo, store.LogSourceScheduler, store.LogStageDeleted, "删除任务", nil, "")
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: store.AuditTaskDelete, Resource: store.AuditResourceTask, ResourceID: id, ResourceName: task.Name, Detail: "删除任务"})
	api.OKMsg(c, gin.H{"id": id}, "已删除")
}
