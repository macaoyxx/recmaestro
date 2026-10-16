// 登录接口 IP 限流：内存固定窗口计数，遏制撞库与暴力破解。
// 单机语义（多副本部署时各副本独立计数；现状 scheduler 单副本），不持久化，
// 重启即清零——限流目标是防瞬时暴力，可接受。
package scheduler

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
)

// loginRateLimit / loginRateWindow 每 IP 每窗口允许的最大登录尝试次数。
const (
	loginRateLimit  = 10
	loginRateWindow = time.Minute
)

// loginHits 记录一个 IP 在当前窗口内的尝试次数。
type loginHits struct {
	windowStart time.Time
	count       int
}

// loginLimiter 固定窗口计数器。
type loginLimiter struct {
	mu   sync.Mutex
	hits map[string]*loginHits
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{hits: map[string]*loginHits{}}
}

// allow 记录一次尝试并判断是否放行；窗口滚动时重置计数。
func (l *loginLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.hits[ip]
	if !ok || now.Sub(h.windowStart) >= loginRateWindow {
		l.gc(now)
		l.hits[ip] = &loginHits{windowStart: now, count: 1}
		return true
	}
	h.count++
	return h.count <= loginRateLimit
}

// gc 清理过期窗口条目，防 map 无界增长（正常 IP 数有限，恶意伪造来源可量级膨胀）。
// 仅在新建窗口时触发（低频），且设阈值避免每次全表扫描。
func (l *loginLimiter) gc(now time.Time) {
	if len(l.hits) < 10000 {
		return
	}
	for k, v := range l.hits {
		if now.Sub(v.windowStart) >= loginRateWindow {
			delete(l.hits, k)
		}
	}
}

// loginRateLimitMiddleware 登录限流中间件：每 IP 每分钟 10 次尝试，超限 429。
func (s *Server) loginRateLimitMiddleware() gin.HandlerFunc {
	limiter := newLoginLimiter()
	return func(c *gin.Context) {
		if !limiter.allow(c.ClientIP(), time.Now()) {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusTooManyRequests,
				api.Envelope{Code: http.StatusTooManyRequests, Msg: "登录尝试过于频繁，请稍后再试"})
			return
		}
		c.Next()
	}
}
