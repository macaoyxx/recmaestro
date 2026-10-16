package scheduler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
	"recmaestro/internal/auth"
	"recmaestro/internal/id"
	"recmaestro/internal/store"
)

type createTaskRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	StreamURL string `json:"stream_url" binding:"required"`
	// 开始时间（RFC3339），为空（含空串）或已过则立即执行
	StartTime OptionalTime `json:"start_time"`
	// 截止时间（RFC3339），必填，须晚于当前时间；start_time 非空时须晚于开始时间
	EndTime        *time.Time `json:"end_time"`
	SegmentSeconds int        `json:"segment_seconds"`
}

// handleCreateTask 创建任务。
// @Summary      创建任务
// @Tags         任务
// @Accept       json
// @Produce      json
// @Param        body  body  createTaskRequest  true  "任务参数"
// @Success      201   {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks [post]
func (s *Server) handleCreateTask(c *gin.Context) {
	var req createTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if msg := validateTaskTimes(req.StartTime.Time, req.EndTime, time.Now()); msg != "" {
		api.Fail(c, http.StatusBadRequest, msg)
		return
	}
	if msg := validateSegmentSeconds(req.SegmentSeconds); msg != "" {
		api.Fail(c, http.StatusBadRequest, msg)
		return
	}

	ctx := c.Request.Context()

	// ID：外部传入则校验唯一性，为空则生成雪花 ID
	var taskID string
	if req.ID != "" {
		if exists, _ := s.tasks.GetByID(ctx, req.ID); exists != nil {
			api.Fail(c, http.StatusConflict, "任务 ID 已存在")
			return
		}
		taskID = req.ID
	} else {
		taskID = id.NextString()
	}

	t := &store.Task{
		ID:             taskID,
		Name:           req.Name,
		StreamURL:      req.StreamURL,
		StartTime:      req.StartTime.Time,
		EndTime:        req.EndTime,
		SegmentSeconds: req.SegmentSeconds,
		Status:         store.TaskPending,
		MaxRetry:       3,
	}
	if t.SegmentSeconds == 0 {
		t.SegmentSeconds = s.cfg.Recorder.SegmentSeconds
	}
	if uid := auth.UserID(c); uid != 0 {
		t.CreatedBy = &uid
	}

	if err := s.tasks.Create(ctx, t); err != nil {
		// 并发创建同 ID 时前置查重存在竞态窗口，由 DB 唯一键兜底返回 409
		if strings.Contains(err.Error(), "Error 1062") {
			api.Fail(c, http.StatusConflict, "任务 ID 已存在")
			return
		}
		api.Fail(c, http.StatusInternalServerError, "创建任务失败")
		return
	}
	s.logger.Log(t.ID, store.LogLevelInfo, store.LogSourceScheduler, store.LogStageCreated, "创建任务", nil, "")

	// 立即任务（start_time 为空或已到）直接入队；否则写入定时队列。
	now := time.Now()
	finalStatus := store.TaskPending
	if t.StartTime == nil || !t.StartTime.After(now) {
		if err := s.tasks.UpdateStatus(ctx, t.ID, store.TaskQueued); err != nil {
			api.Fail(c, http.StatusInternalServerError, "更新任务状态失败")
			return
		}
		if err := s.queue.Enqueue(ctx, t.ID); err != nil {
			api.Fail(c, http.StatusInternalServerError, "任务入队失败")
			return
		}
		s.logger.Log(t.ID, store.LogLevelInfo, store.LogSourceScheduler, store.LogStageQueued, "任务入队", nil, "")
		finalStatus = store.TaskQueued
	} else if err := s.queue.Schedule(ctx, t.ID, *t.StartTime); err != nil {
		api.Fail(c, http.StatusInternalServerError, "任务调度失败")
		return
	}

	api.Created(c, gin.H{"id": t.ID, "status": finalStatus})
}

// listTasksRequest 任务列表查询入参：分页基类 + 筛选条件，POST body 一把传（不再逐个拼 query）。
type listTasksRequest struct {
	api.PageQuery
	Status  string `json:"status"`
	Keyword string `json:"keyword"`
}

// handleListTasks 任务列表（分页查询）。
// @Summary      任务列表
// @Description  POST 分页查询，统一信封 data 为任务数组、顶层 total；入参 JSON：page/page_size/status/keyword
// @Tags         任务
// @Accept       json
// @Produce      json
// @Param        body  body  listTasksRequest  true  "查询参数"
// @Success      200   {object}  api.Envelope
// @Security     BearerAuth
// @Router       /tasks/list [post]
func (s *Server) handleListTasks(c *gin.Context) {
	var req listTasksRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	req.Normalize()
	tasks, total, err := s.tasks.List(c.Request.Context(), req.Status, req.Keyword, req.Page, req.PageSize)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	api.OKPage(c, tasks, total)
}

// handleGetTask 任务详情。
// @Summary      任务详情
// @Tags         任务
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  store.Task
// @Security     BearerAuth
// @Router       /tasks/{id} [get]
func (s *Server) handleGetTask(c *gin.Context) {
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
	api.OK(c, task)
}

// handleCancelTask 取消任务。
// @Summary      取消任务
// @Tags         任务
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks/{id}/cancel [post]
func (s *Server) handleCancelTask(c *gin.Context) {
	id := c.Param("id")
	ok, err := s.tasks.Cancel(c.Request.Context(), id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if !ok {
		api.Fail(c, http.StatusConflict, "任务当前状态不可取消")
		return
	}
	_ = s.queue.RemoveHeartbeat(c.Request.Context(), id)
	_ = s.queue.RemoveSchedule(c.Request.Context(), id)
	s.logger.Log(id, store.LogLevelInfo, store.LogSourceScheduler, store.LogStageCancelled, "取消任务", nil, "")
	api.OKMsg(c, gin.H{"id": id, "status": store.TaskCancelled}, "已取消")
}

// handleRetryTask 重试任务。
// @Summary      重试任务
// @Description  仅失败/中断任务可重试；截止时间缺失或已过返回 409，请先编辑修改。成功后状态置 queued，retry_count 自增，响应含 retry_count。
// @Tags         任务
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks/{id}/retry [post]
func (s *Server) handleRetryTask(c *gin.Context) {
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
	// 截止时间已过/缺失的任务重试后会立即失败，先拦截
	if msg := validateTaskTimes(task.StartTime, task.EndTime, time.Now()); msg != "" {
		api.Fail(c, http.StatusConflict, msg+"，无法重试，请先编辑修改")
		return
	}
	// 原子重试：状态检查 + 置 queued + retry_count 自增由一条条件 UPDATE 完成，防并发重复重试
	ok, retryCount, err := s.tasks.Retry(ctx, id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if !ok {
		api.Fail(c, http.StatusConflict, "仅失败或中断的任务可重试")
		return
	}
	// 清掉可能存在的自动重试排期，避免与手动重试重复入队
	_ = s.queue.RemoveSchedule(ctx, id)
	if err := s.queue.Enqueue(ctx, id); err != nil {
		api.Fail(c, http.StatusInternalServerError, "任务入队失败")
		return
	}
	s.logger.Log(id, store.LogLevelInfo, store.LogSourceScheduler, store.LogStageRetry,
		fmt.Sprintf("第 %d 次重试，重新入队", retryCount), nil, "")
	api.OK(c, gin.H{"id": id, "status": store.TaskQueued, "retry_count": retryCount})
}
