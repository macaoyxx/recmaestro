package scheduler

// handler_audit.go — 审计日志：写入助手（各 handler 成功路径调用）+ 管理端查询接口。
// 审计范围是安全与管理类操作（登录、用户管理、任务人工干预、文件操作）；
// 常规任务创建/取消不审计（量大且非敏感，任务日志已有全生命周期记录）。

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
	"recmaestro/internal/auth"
	"recmaestro/internal/store"
)

// userAgent 上限与 live_rec_audit_logs.user_agent 列宽一致，超长截断防写入失败。
const auditUAMaxLen = 512

// auditEvent 一次审计事件的完整信息。
// 名称（操作者用户名 / 资源实例名称）在此快照落库：资源日后被删除，审计行仍能读出"是谁/哪个任务"。
type auditEvent struct {
	OperatorID   uint64 // 操作者用户 ID（登录类事件即登录者本人；0 表示未知/匿名）
	OperatorName string // 操作者用户名快照
	Action       string
	Resource     string // 资源类型枚举：store.AuditResourceUser / AuditResourceTask
	ResourceID   string
	ResourceName string // 资源实例名称快照（目标用户名 / 任务名）
	Detail       string
}

// audit 写一条审计日志：同步落库，失败仅告警不阻断业务响应（审计不是关键路径）。
func (s *Server) audit(c *gin.Context, e auditEvent) {
	ua := c.Request.UserAgent()
	if len(ua) > auditUAMaxLen {
		ua = ua[:auditUAMaxLen]
	}
	entry := &store.AuditLog{
		OperatorName: e.OperatorName,
		Action:       e.Action,
		Resource:     e.Resource,
		ResourceID:   e.ResourceID,
		ResourceName: e.ResourceName,
		Detail:       e.Detail,
		IP:           c.ClientIP(),
		UserAgent:    ua,
	}
	if e.OperatorID != 0 {
		uid := e.OperatorID
		entry.UserID = &uid
	}
	if err := s.audits.Create(c.Request.Context(), entry); err != nil {
		log.Printf("[warn] 写审计日志失败（action=%s %s/%s）: %v", e.Action, e.Resource, e.ResourceID, err)
	}
}

// auditOperator 从认证上下文取操作者 ID（未认证上下文返回 0）。
func auditOperator(c *gin.Context) uint64 { return auth.UserID(c) }

// auditOperatorName 从认证上下文取操作者用户名（登录等公开接口上为空串）。
func auditOperatorName(c *gin.Context) string { return auth.Username(c) }

// listAuditLogsRequest 审计日志查询入参：分页基类（POST body）。
type listAuditLogsRequest struct {
	api.PageQuery
}

// handleListAuditLogs 审计日志列表（仅管理员，分页）。
// handleListAuditLogs 审计日志列表。
// @Summary      审计日志列表
// @Description  POST 分页查询，统一信封 data 为日志数组、顶层 total；入参 JSON：page/page_size。行内含 username/resource_name 名称快照
// @Tags         审计
// @Accept       json
// @Produce      json
// @Param        body  body  api.PageQuery  true  "分页参数"
// @Success      200   {object}  api.Envelope
// @Security     BearerAuth
// @Router       /audit-logs/list [post]
func (s *Server) handleListAuditLogs(c *gin.Context) {
	var req listAuditLogsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	req.Normalize()
	logs, total, err := s.audits.List(c.Request.Context(), req.Page, req.PageSize)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	api.OKPage(c, logs, total)
}
