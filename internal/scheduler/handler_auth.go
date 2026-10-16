package scheduler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
	"recmaestro/internal/auth"
	"recmaestro/internal/store"
)

// maxFailedLoginAttempts 是连续登录失败达到锁定阈值的次数。
const maxFailedLoginAttempts = 5

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// handleLogin 用户登录。
// @Summary      用户登录
// @Description  响应为统一信封，data 含 access_token / refresh_token / user
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        body  body  loginRequest  true  "登录参数"
// @Success      200   {object}  api.Envelope
// @Failure      401   {object}  api.Envelope
// @Router       /login [post]
func (s *Server) handleLogin(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}

	ctx := c.Request.Context()
	u, err := s.users.GetByUsername(ctx, req.Username)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	// 用户不存在：返回与密码错误一致的提示，避免用户名枚举
	if u == nil {
		api.Fail(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	// 锁定检查必须先于禁用检查：连续密码错误的锁定复用 disabled 状态实现（见下方 UpdateStatus），
	// 仅凭 status 无法区分成因；失败计数≥5 即锁定成因（disabled 状态下不会累计计数），<5 则为管理员禁用
	if u.FailedLoginCount >= maxFailedLoginAttempts {
		s.audit(c, auditEvent{OperatorID: u.ID, OperatorName: u.Username, Action: store.AuditLoginLocked, Resource: store.AuditResourceUser, ResourceID: strconv.FormatUint(u.ID, 10), ResourceName: u.Username, Detail: "锁定状态登录被拒绝"})
		api.Fail(c, http.StatusForbidden, "账号已锁定，请联系管理员")
		return
	}
	if u.Status == store.StatusDisabled {
		api.Fail(c, http.StatusForbidden, "账号已被禁用")
		return
	}

	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		count, err := s.users.IncrFailedLogin(ctx, u.ID)
		if err != nil {
			api.Fail(c, http.StatusInternalServerError, "服务内部错误")
			return
		}
		if count >= maxFailedLoginAttempts {
			_ = s.users.UpdateStatus(ctx, u.ID, store.StatusDisabled)
			s.audit(c, auditEvent{OperatorID: u.ID, OperatorName: u.Username, Action: store.AuditLoginLocked, Resource: store.AuditResourceUser, ResourceID: strconv.FormatUint(u.ID, 10), ResourceName: u.Username, Detail: "密码错误次数过多，账号已锁定"})
			api.Fail(c, http.StatusForbidden, "密码错误次数过多，账号已锁定")
			return
		}
		s.audit(c, auditEvent{OperatorID: u.ID, OperatorName: u.Username, Action: store.AuditLoginFail, Resource: store.AuditResourceUser, ResourceID: strconv.FormatUint(u.ID, 10), ResourceName: u.Username, Detail: "密码错误"})
		api.Fail(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}

	if err := s.users.ResetFailedLogin(ctx, u.ID, time.Now()); err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	s.audit(c, auditEvent{OperatorID: u.ID, OperatorName: u.Username, Action: store.AuditLoginSuccess, Resource: store.AuditResourceUser, ResourceID: strconv.FormatUint(u.ID, 10), ResourceName: u.Username, Detail: "登录成功"})

	access, err := s.authMgr.SignAccess(u.ID, u.Username, u.Role)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "签发 token 失败")
		return
	}
	refresh, err := s.authMgr.SignRefresh(u.ID, u.Username, u.Role)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "签发 token 失败")
		return
	}

	api.OK(c, gin.H{
		"access_token":  access,
		"refresh_token": refresh,
		"user": gin.H{
			"id":       u.ID,
			"username": u.Username,
			"role":     u.Role,
		},
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// handleRefresh 刷新 token。
// @Summary      刷新 access token
// @Tags         认证
// @Accept       json
// @Produce      json
// @Param        body  body  refreshRequest  true  "refresh token"
// @Success      200   {object}  api.Envelope
// @Failure      401   {object}  api.Envelope
// @Router       /refresh [post]
func (s *Server) handleRefresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		api.Fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}

	claims, err := s.authMgr.Parse(req.RefreshToken)
	if err != nil || claims.Type != auth.TokenRefresh {
		api.Fail(c, http.StatusUnauthorized, "refresh token 无效或已过期")
		return
	}

	ctx := c.Request.Context()
	u, err := s.users.GetByID(ctx, claims.UserID)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	if u == nil || u.Status == store.StatusDisabled {
		api.Fail(c, http.StatusUnauthorized, "账号不可用")
		return
	}

	// 用数据库最新信息签发新 access token
	access, err := s.authMgr.SignAccess(u.ID, u.Username, u.Role)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "签发 token 失败")
		return
	}
	api.OK(c, gin.H{"access_token": access})
}

// handleMe 当前用户信息。
// @Summary      当前用户信息
// @Tags         认证
// @Produce      json
// @Success      200  {object}  api.Envelope
// @Security     BearerAuth
// @Router       /me [get]
func (s *Server) handleMe(c *gin.Context) {
	api.OK(c, gin.H{
		"id":       auth.UserID(c),
		"username": auth.Username(c),
		"role":     auth.Role(c),
	})
}
