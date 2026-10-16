package scheduler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
)

// handleListLogs 任务日志。
// @Summary      任务日志
// @Description  游标分页（before 为上一页最后一条 ID，倒序）；响应为统一信封，data 为日志数组
// @Tags         日志
// @Produce      json
// @Param        id     path   int     true  "任务 ID"
// @Param        level  query  string  false "日志级别"
// @Param        stage  query  string  false "阶段"
// @Param        before query  int     false "游标 ID"
// @Param        limit  query  int     false "每页条数"
// @Success      200    {object}  api.Envelope
// @Security     BearerAuth
// @Router       /tasks/{id}/logs [get]
func (s *Server) handleListLogs(c *gin.Context) {
	id := c.Param("id")
	level := c.Query("level")
	stage := c.Query("stage")
	before, _ := strconv.ParseInt(c.Query("before"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))

	logs, err := s.logs.List(c.Request.Context(), id, level, stage, before, limit)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	api.OK(c, logs)
}

// handleDownloadLogs 下载日志。
// @Summary      下载任务日志
// @Tags         日志
// @Produce      text/plain
// @Param        id   path  int  true  "任务 ID"
// @Success      200  {string}  string
// @Security     BearerAuth
// @Router       /tasks/{id}/logs/download [get]
func (s *Server) handleDownloadLogs(c *gin.Context) {
	id := c.Param("id")
	logs, err := s.logs.List(c.Request.Context(), id, "", "", 0, 10000)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}

	var b strings.Builder
	for i := len(logs) - 1; i >= 0; i-- {
		l := logs[i]
		b.WriteString(l.Ts.Format(time.RFC3339))
		b.WriteString(" [")
		b.WriteString(l.Level)
		b.WriteString("] [")
		b.WriteString(l.Source)
		b.WriteString("] [")
		b.WriteString(l.Stage)
		b.WriteString("] ")
		b.WriteString(l.Message)
		b.WriteString("\n")
	}

	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=task-"+id+".log")
	c.String(http.StatusOK, b.String())
}
