package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"recmaestro/internal/auth"
	"recmaestro/internal/config"
	"recmaestro/internal/queue"
	"recmaestro/internal/registry"
	"recmaestro/internal/store"
)

// refreshExpire 是 refresh token 的固定有效期。
const refreshExpire = 7 * 24 * time.Hour

// Server 是调度器的 HTTP 服务。
type Server struct {
	cfg      *config.Config
	users    *store.UserStore
	tasks    *store.TaskStore
	files    *store.TaskFileStore
	logs     *store.TaskLogStore
	nodes    *store.NodeStore
	audits   *store.AuditStore
	queue    *queue.Queue
	authMgr  *auth.Manager
	logger   *store.TaskLogger
	registry registry.Registry
	regSvc   registry.Service
}

// NewServer 创建调度器服务。
func NewServer(cfg *config.Config, db *gorm.DB, rdb *redis.Client) *Server {
	// 服务注册引擎（默认 redis，可配置 consul）
	reg, err := registry.New(cfg.Registry.Engine, rdb, cfg.Consul.Addr, cfg.Consul.Token, cfg.Consul.Scheme)
	if err != nil {
		log.Printf("[warn] %v，退化为不注册", err)
		reg = nil
	}

	return &Server{
		cfg:      cfg,
		users:    store.NewUserStore(db),
		tasks:    store.NewTaskStore(db),
		files:    store.NewTaskFileStore(db),
		logs:     store.NewTaskLogStore(db),
		nodes:    store.NewNodeStore(db),
		audits:   store.NewAuditStore(db),
		queue:    queue.NewQueue(rdb),
		authMgr:  auth.NewManager(cfg.JWT.Secret, cfg.JWT.Expire, refreshExpire),
		logger:   store.NewTaskLogger(db),
		registry: reg,
		regSvc:   registry.NewService("recmaestro-scheduler", registry.LocalIP(), cfg.Server.Port),
	}
}

// RegisterService 注册调度器服务（引擎可配置），并启动带 ctx 的后台续期循环。
func (s *Server) RegisterService(ctx context.Context) {
	if s.registry == nil {
		return
	}
	if err := s.registry.Register(ctx, s.regSvc); err != nil {
		log.Printf("[warn] 服务注册失败: %v", err)
		return
	}
	log.Printf("已注册服务: %s", s.regSvc.ID)

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = s.registry.Deregister(context.Background(), s.regSvc)
				return
			case <-ticker.C:
				_ = s.registry.Renew(ctx, s.regSvc)
			}
		}
	}()
}

// Logger 返回任务日志写入器（供 main 启动批量写入循环）。
func (s *Server) Logger() *store.TaskLogger { return s.logger }

// Router 构建调度器路由。
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	api := r.Group("/api")
	{
		// 公开接口（登录带 IP 限流，防撞库）
		api.POST("/login", s.loginRateLimitMiddleware(), s.handleLogin)
		api.POST("/refresh", s.handleRefresh)

		// 需登录
		authed := api.Group("", auth.Middleware(s.authMgr))
		{
			authed.GET("/me", s.handleMe)
			authed.POST("/tasks/list", s.handleListTasks)
			authed.POST("/tasks", s.handleCreateTask)
			authed.GET("/tasks/:id", s.handleGetTask)
			authed.GET("/tasks/:id/logs", s.handleListLogs)
			authed.GET("/tasks/:id/logs/download", s.handleDownloadLogs)
			authed.GET("/tasks/:id/ffmpeg-log/:name", s.handleDownloadFFmpegLog)
			authed.GET("/tasks/:id/files", s.handleListFiles)
			authed.GET("/tasks/:id/download", s.handleDownloadOutput)
			authed.GET("/tasks/:id/progress", s.handleTaskProgress)
			authed.GET("/nodes", s.handleListNodes)
			authed.GET("/stats", s.handleStats)

			// 仅管理员
			admin := authed.Group("", auth.RequireRole(store.RoleAdmin))
			{
				admin.GET("/users", s.handleListUsers)
				admin.POST("/users", s.handleCreateUser)
				admin.PATCH("/users/:id/status", s.handleUpdateUserStatus)
				admin.PATCH("/users/:id/password", s.handleResetPassword)
				admin.DELETE("/users/:id", s.handleDeleteUser)
				admin.POST("/tasks/:id/cancel", s.handleCancelTask)
				admin.POST("/tasks/:id/retry", s.handleRetryTask)
				admin.PUT("/tasks/:id", s.handleUpdateTask)
				admin.POST("/tasks/:id/abort", s.handleAbortTask)
				admin.DELETE("/tasks/:id", s.handleDeleteTask)
				admin.POST("/tasks/:id/merge", s.handleMerge)
				admin.POST("/tasks/:id/files/delete", s.handleDeleteFiles)
				admin.POST("/audit-logs/list", s.handleListAuditLogs)
			}
		}
	}
	return r
}
