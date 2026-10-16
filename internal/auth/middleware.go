package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
)

// context 中存放用户信息的键。
const (
	ctxKeyUserID   = "auth_user_id"
	ctxKeyUsername = "auth_username"
	ctxKeyRole     = "auth_role"
)

// Middleware 校验 Authorization: Bearer <token>，并把用户信息写入 context。
func Middleware(m *Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, api.Envelope{Code: http.StatusUnauthorized, Msg: "未提供认证信息"})
			return
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := m.Parse(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, api.Envelope{Code: http.StatusUnauthorized, Msg: "token 无效或已过期"})
			return
		}
		if claims.Type != TokenAccess {
			c.AbortWithStatusJSON(http.StatusUnauthorized, api.Envelope{Code: http.StatusUnauthorized, Msg: "token 类型错误"})
			return
		}

		c.Set(ctxKeyUserID, claims.UserID)
		c.Set(ctxKeyUsername, claims.Username)
		c.Set(ctxKeyRole, claims.Role)
		c.Next()
	}
}

// RequireRole 要求当前用户具备指定角色（如 admin）。
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(c *gin.Context) {
		if _, ok := allowed[Role(c)]; !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, api.Envelope{Code: http.StatusForbidden, Msg: "无权限执行该操作"})
			return
		}
		c.Next()
	}
}

// UserID 从 context 读取当前用户 ID。
func UserID(c *gin.Context) uint64 {
	v, _ := c.Get(ctxKeyUserID)
	id, _ := v.(uint64)
	return id
}

// Username 从 context 读取当前用户名。
func Username(c *gin.Context) string {
	v, _ := c.Get(ctxKeyUsername)
	name, _ := v.(string)
	return name
}

// Role 从 context 读取当前用户角色。
func Role(c *gin.Context) string {
	v, _ := c.Get(ctxKeyRole)
	role, _ := v.(string)
	return role
}
