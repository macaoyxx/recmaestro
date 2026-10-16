package scheduler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
	"recmaestro/internal/auth"
	"recmaestro/internal/store"
)

// handleListUsers 用户列表。
// @Summary      用户列表
// @Description  响应为统一信封，data 为用户数组（不含密码哈希）
// @Tags         用户
// @Produce      json
// @Success      200  {object}  api.Envelope
// @Security     BearerAuth
// @Router       /users [get]
func (s *Server) handleListUsers(c *gin.Context) {
	users, err := s.users.List(c.Request.Context())
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	// 不暴露密码哈希
	out := make([]gin.H, 0, len(users))
	for _, u := range users {
		out = append(out, gin.H{
			"id":       u.ID,
			"username": u.Username,
			"role":     u.Role,
			"status":   u.Status,
		})
	}
	api.OK(c, out)
}

type createUserRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role" binding:"required,oneof=admin user"`
}

// handleCreateUser 创建用户。
// @Summary      创建用户
// @Tags         用户
// @Accept       json
// @Produce      json
// @Param        body  body  createUserRequest  true  "用户参数"
// @Success      201   {object}  api.Envelope
// @Security     BearerAuth
// @Router       /users [post]
func (s *Server) handleCreateUser(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}

	ctx := c.Request.Context()
	exists, err := s.users.GetByUsername(ctx, req.Username)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if exists != nil {
		api.Fail(c, http.StatusConflict, "用户名已存在")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}

	u := &store.User{
		Username:     req.Username,
		PasswordHash: hash,
		Role:         req.Role,
		Status:       store.StatusEnabled,
	}
	if err := s.users.Create(ctx, u); err != nil {
		api.Fail(c, http.StatusInternalServerError, "创建用户失败")
		return
	}
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: store.AuditUserCreate, Resource: store.AuditResourceUser, ResourceID: strconv.FormatUint(u.ID, 10), ResourceName: req.Username, Detail: "创建用户 " + req.Username})
	api.Created(c, gin.H{"id": u.ID, "username": u.Username, "role": u.Role})
}

type updateStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=enabled disabled"`
}

// handleUpdateUserStatus 启用/禁用用户。
// @Summary      启用/禁用用户
// @Tags         用户
// @Accept       json
// @Produce      json
// @Param        id    path  int                 true  "用户 ID"
// @Param        body  body  updateStatusRequest  true  "状态"
// @Success      200   {object}  api.Envelope
// @Security     BearerAuth
// @Router       /users/{id}/status [patch]
func (s *Server) handleUpdateUserStatus(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		api.Fail(c, http.StatusBadRequest, "无效的用户 ID")
		return
	}
	var req updateStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	target, err := s.users.GetByID(c.Request.Context(), id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if target == nil {
		api.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	// 禁用护栏：内置管理员与当前登录账号不可禁用（启用不受限）
	if req.Status == store.StatusDisabled {
		if target.Username == store.BuiltinAdminUsername {
			api.Fail(c, http.StatusConflict, "内置管理员不允许禁用")
			return
		}
		if id == auditOperator(c) {
			api.Fail(c, http.StatusConflict, "不能禁用当前登录账号")
			return
		}
	}
	if err := s.users.UpdateStatus(c.Request.Context(), id, req.Status); err != nil {
		api.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	// 重新启用锁定账号（disabled + 失败计数≥5）必须同步清零计数，否则旧计数仍会把登录拦在门外
	if req.Status == store.StatusEnabled {
		if err := s.users.Unlock(c.Request.Context(), id); err != nil {
			api.Fail(c, http.StatusInternalServerError, "解锁失败")
			return
		}
	}
	action := store.AuditUserDisable
	if req.Status == store.StatusEnabled {
		action = store.AuditUserEnable
	}
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: action, Resource: store.AuditResourceUser, ResourceID: strconv.FormatUint(id, 10), ResourceName: target.Username, Detail: "用户状态置为 " + req.Status})
	api.OKMsg(c, gin.H{"id": id, "status": req.Status}, "用户状态已更新")
}

type resetPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// handleResetPassword 重置用户密码。
// @Summary      重置用户密码
// @Tags         用户
// @Accept       json
// @Produce      json
// @Param        id    path  int                  true  "用户 ID"
// @Param        body  body  resetPasswordRequest  true  "新密码"
// @Success      200   {object}  api.Envelope
// @Security     BearerAuth
// @Router       /users/{id}/password [patch]
func (s *Server) handleResetPassword(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		api.Fail(c, http.StatusBadRequest, "无效的用户 ID")
		return
	}
	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	target, err := s.users.GetByID(c.Request.Context(), id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if target == nil {
		api.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := s.users.UpdatePassword(c.Request.Context(), id, hash); err != nil {
		api.Fail(c, http.StatusInternalServerError, "重置密码失败")
		return
	}
	// 重置密码视为重新开始：清零失败计数，避免锁定账号换了新密码仍被旧计数拦住
	if err := s.users.Unlock(c.Request.Context(), id); err != nil {
		api.Fail(c, http.StatusInternalServerError, "解锁失败")
		return
	}
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: store.AuditUserResetPwd, Resource: store.AuditResourceUser, ResourceID: strconv.FormatUint(id, 10), ResourceName: target.Username, Detail: "重置用户密码"})
	api.OKMsg(c, gin.H{"id": id}, "密码已重置")
}

// handleDeleteUser 删除用户。
// @Summary      删除用户
// @Description  物理删除；护栏：内置管理员与当前登录账号不可删除。历史审计因名称快照仍可读出该用户
// @Tags         用户
// @Produce      json
// @Param        id   path  int  true  "用户 ID"
// @Success      200  {object}  api.Envelope
// @Security     BearerAuth
// @Router       /users/{id} [delete]
func (s *Server) handleDeleteUser(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		api.Fail(c, http.StatusBadRequest, "无效的用户 ID")
		return
	}
	ctx := c.Request.Context()
	target, err := s.users.GetByID(ctx, id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if target == nil {
		api.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	// 护栏零：内置管理员不可删除（系统的永久管理入口）
	if target.Username == store.BuiltinAdminUsername {
		api.Fail(c, http.StatusConflict, "内置管理员不允许删除")
		return
	}
	// 护栏一：不能删自己（防止把自己锁在门外）
	if id == auditOperator(c) {
		api.Fail(c, http.StatusConflict, "不能删除当前登录账号")
		return
	}
	// 护栏二：不能删最后一个启用的管理员（防止失去管理入口）
	if target.Role == store.RoleAdmin {
		n, err := s.users.CountOtherEnabledAdmins(ctx, id)
		if err != nil {
			api.Fail(c, http.StatusInternalServerError, "服务内部错误")
			return
		}
		if n == 0 {
			api.Fail(c, http.StatusConflict, "不能删除最后一个启用的管理员")
			return
		}
	}
	if err := s.users.Delete(ctx, id); err != nil {
		api.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: store.AuditUserDelete, Resource: store.AuditResourceUser, ResourceID: strconv.FormatUint(id, 10), ResourceName: target.Username, Detail: "删除用户 " + target.Username})
	api.OKMsg(c, gin.H{"id": id}, "已删除")
}
