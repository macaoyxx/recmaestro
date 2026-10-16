// RecMaestro 统一入口：用 --role 区分 scheduler / worker 两种角色，共用一套代码。
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hashicorp/consul/api"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "recmaestro/docs"
	"recmaestro/internal/auth"
	"recmaestro/internal/config"
	"recmaestro/internal/migrate"
	"recmaestro/internal/scheduler"
	"recmaestro/internal/store"
	"recmaestro/internal/web"
	"recmaestro/internal/worker"
)

// @title           RecMaestro API
// @version         V0.1.0
// @description     直播录制调度平台 API（调度器）
// @BasePath        /api
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func main() {
	var (
		roleFlag = flag.String("role", "", "运行角色: scheduler | worker")
		envFlag  = flag.String("env", "", "运行环境: dev | test | local | prod")
	)
	flag.Parse()

	env := *envFlag
	if env == "" {
		env = os.Getenv("APP_ENV")
	}

	cfg, err := config.Load(env)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	role := *roleFlag
	if role == "" {
		role = os.Getenv("APP_ROLE")
	}
	if role == "" {
		role = cfg.Server.Role
	}
	if role != "scheduler" && role != "worker" {
		log.Fatalf("无效的角色 %q，必须是 scheduler 或 worker", role)
	}

	log.Printf("RecMaestro 启动: role=%s env=%s", role, cfg.Server.Env)

	if err := migrate.Up(cfg.MySQL); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}
	log.Println("数据库迁移完成")

	checkDependencies(cfg)

	switch role {
	case "scheduler":
		err = runScheduler(cfg)
	case "worker":
		err = runWorker(cfg)
	}
	if err != nil {
		log.Fatalf("运行 %s 失败: %v", role, err)
	}
}

// runScheduler 启动调度器：数据库、种子管理员、调度循环、认证与任务 API。
// 捕获 SIGTERM/SIGINT 优雅停机：先 Shutdown HTTP（等在途请求收尾），再取消后台
// 循环上下文（调度/指标/日志清理/服务注册随 ctx 退出，任务日志批量写入器 flush 后退出）。
func runScheduler(cfg *config.Config) error {
	if cfg.JWT.Secret == "" {
		return fmt.Errorf("未设置 JWT_SECRET 环境变量")
	}
	if cfg.Server.InternalAPIToken == "" {
		return fmt.Errorf("未设置 INTERNAL_API_TOKEN 环境变量（scheduler 下发 worker 内部接口的共享密钥）")
	}
	db, err := store.NewDB(cfg.MySQL)
	if err != nil {
		return err
	}
	if err := seedAdmin(store.NewUserStore(db)); err != nil {
		return err
	}
	rdb := newRedisClient(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	srv := scheduler.NewServer(cfg, db, rdb)
	srv.Logger().Start(ctx)  // 启动日志批量写入（ctx 取消时 flush 剩余缓冲后退出）
	srv.RebuildQueues(ctx)   // 队列重建兜底（Redis 数据丢失后任务不卡死）
	srv.RegisterService(ctx) // 服务注册（引擎可配置：redis/consul）
	srv.RunDispatcher(ctx)
	srv.RunMetricsCollector(ctx)
	srv.RunLogCleanup(ctx)

	router := srv.Router()
	router.GET("/healthz", healthzHandler(cfg, "scheduler"))
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	// 前端 SPA 静态托管（NoRoute 兜底，避免 StaticFS 的 catch-all 与 /api 冲突）
	distFS, _ := fs.Sub(web.Dist, "dist")
	fileServer := http.FileServer(http.FS(distFS))
	router.NoRoute(func(c *gin.Context) {
		fileServer.ServeHTTP(c.Writer, c.Request)
	})

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpSrv := &http.Server{Addr: addr, Handler: router}
	srvErr := make(chan error, 1)
	go func() { srvErr <- httpSrv.ListenAndServe() }()
	log.Printf("HTTP 服务监听 %s", addr)

	select {
	case err := <-srvErr:
		return err
	case <-ctx.Done():
		log.Println("收到停止信号，scheduler 优雅停机（等待在途请求收尾，最长 5s）...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[warn] HTTP 停机超时: %v", err)
		}
		// ctx 已取消：各后台循环退出、日志缓冲 flush；稍候让 flush 落盘后再退出进程
		time.Sleep(time.Second)
		log.Println("scheduler 已停止")
		return nil
	}
}

// runWorker 启动 worker：认领循环 + 心跳（后台）+ 健康检查 HTTP 服务。
// 捕获 SIGTERM/SIGINT 优雅停机：先让在录任务优雅停止 ffmpeg 并收尾段文件（避免
// 在写段被强杀截断成损坏段），等任务收尾后再退出进程；docker stop 的 10s 宽限内
// 尽力完成，超时由强杀兜底（未收尾任务走既有宕机恢复机制接管）。
func runWorker(cfg *config.Config) error {
	if cfg.Server.InternalAPIToken == "" {
		return fmt.Errorf("未设置 INTERNAL_API_TOKEN 环境变量（worker 内部接口的共享密钥，未配置时内部接口拒绝全部请求）")
	}
	db, err := store.NewDB(cfg.MySQL)
	if err != nil {
		return err
	}
	rdb := newRedisClient(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	w := worker.NewWorker(cfg, db, rdb)
	go w.Run(ctx)

	router := w.InternalRouter()
	router.GET("/healthz", healthzHandler(cfg, "worker"))
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("HTTP 服务监听 %s", addr)
	srvErr := make(chan error, 1)
	go func() { srvErr <- router.Run(addr) }()

	select {
	case err := <-srvErr:
		return err
	case <-ctx.Done():
		log.Println("收到停止信号，等待在录任务收尾（最长 8s）...")
		idle := make(chan struct{})
		go func() {
			w.Wait()
			close(idle)
		}()
		select {
		case <-idle:
			log.Println("在录任务已收尾，worker 退出")
		case <-time.After(8 * time.Second):
			log.Println("[warn] 等待任务收尾超时，强制退出（未收尾任务由宕机恢复机制接管）")
		}
		return nil
	}
}

func newRedisClient(cfg *config.Config) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
}

func healthzHandler(cfg *config.Config, role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "recmaestro",
			"role":    role,
			"env":     cfg.Server.Env,
			"time":    time.Now().Format(time.RFC3339),
		})
	}
}

// seedAdmin 首次启动时创建初始管理员，密码从 ADMIN_INITIAL_PASSWORD 环境变量读取。
func seedAdmin(users *store.UserStore) error {
	ctx := context.Background()
	admin, err := users.GetByUsername(ctx, "admin")
	if err != nil {
		return fmt.Errorf("查询管理员失败: %w", err)
	}
	if admin != nil {
		return nil
	}

	password := os.Getenv("ADMIN_INITIAL_PASSWORD")
	if password == "" {
		return fmt.Errorf("首次启动需要设置环境变量 ADMIN_INITIAL_PASSWORD 以创建初始管理员")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("加密管理员密码失败: %w", err)
	}
	u := &store.User{
		Username:     "admin",
		PasswordHash: hash,
		Role:         store.RoleAdmin,
		Status:       store.StatusEnabled,
	}
	if err := users.Create(ctx, u); err != nil {
		// 多副本首启并发建号时，另一副本可能已创建成功（唯一键冲突），视为成功
		if existing, qerr := users.GetByUsername(ctx, "admin"); qerr == nil && existing != nil {
			return nil
		}
		return fmt.Errorf("创建管理员失败: %w", err)
	}
	log.Println("已创建初始管理员账号 admin")
	return nil
}

// checkDependencies 对 Redis（必依赖）/ Consul（仅 registry.engine=consul 时）做连通性自检，
// 失败仅告警不退出。
func checkDependencies(cfg *config.Config) {
	rdb := newRedisClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("[warn] Redis 连接失败 (%s): %v", cfg.Redis.Addr, err)
	} else {
		log.Printf("Redis 连接正常: %s", cfg.Redis.Addr)
	}
	cancel()
	_ = rdb.Close()

	if cfg.Registry.Engine != "consul" {
		return // 默认 redis 注册引擎，不依赖 Consul
	}

	consulCfg := api.DefaultConfig()
	consulCfg.Address = cfg.Consul.Addr
	consulCfg.Scheme = cfg.Consul.Scheme
	if cfg.Consul.Token != "" {
		consulCfg.Token = cfg.Consul.Token
	}
	client, err := api.NewClient(consulCfg)
	if err != nil {
		log.Printf("[warn] Consul 客户端初始化失败: %v", err)
		return
	}
	if _, err := client.Status().Leader(); err != nil {
		log.Printf("[warn] Consul 连接失败 (%s): %v", cfg.Consul.Addr, err)
	} else {
		log.Printf("Consul 连接正常: %s", cfg.Consul.Addr)
	}
}
