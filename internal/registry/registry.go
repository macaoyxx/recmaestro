// Package registry 服务注册引擎抽象：默认 Redis（零额外中间件依赖），可选 Consul。
// 注册 ID 规范：<服务名>-<ip>-<端口>，如 recmaestro-worker-172.21.0.6-8080。
package registry

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/redis/go-redis/v9"
)

// leaseTTL 注册租约时长：TTL 内未续期即视为下线。
const leaseTTL = 30 * time.Second

// Service 一个待注册的服务实例。
type Service struct {
	Name string // 服务名，如 recmaestro-worker
	ID   string // 实例 ID，规范 <Name>-<IP>-<Port>
	IP   string
	Port int
}

// Addr 返回 ip:port。
func (s Service) Addr() string {
	return net.JoinHostPort(s.IP, strconv.Itoa(s.Port))
}

// NewService 构造服务实例，ID 按"服务名-ip-端口"规范生成。
func NewService(name, ip string, port int) Service {
	return Service{Name: name, ID: fmt.Sprintf("%s-%s-%d", name, ip, port), IP: ip, Port: port}
}

// Registry 服务注册引擎。Register 启动时调用一次；Renew 由调用方带 ctx 的
// 心跳循环周期调用；进程优雅退出时调用 Deregister。
type Registry interface {
	Register(ctx context.Context, svc Service) error
	Renew(ctx context.Context, svc Service) error
	Deregister(ctx context.Context, svc Service) error
}

// New 按配置引擎创建注册器：engine 为空或 "redis" 用 Redis（默认），"consul" 用 Consul。
func New(engine string, rdb *redis.Client, consulAddr, consulToken, consulScheme string) (Registry, error) {
	if engine == "consul" {
		cfg := api.DefaultConfig()
		cfg.Address = consulAddr
		cfg.Scheme = consulScheme
		if consulToken != "" {
			cfg.Token = consulToken
		}
		client, err := api.NewClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("Consul 客户端初始化失败: %w", err)
		}
		return &consulRegistry{client: client}, nil
	}
	return &redisRegistry{rdb: rdb}, nil
}

// redisRegistry 基于 Redis TTL key 的注册：每实例一个带过期 key，续期重 SET，
// 过期即下线，无需清理任务。
type redisRegistry struct {
	rdb *redis.Client
}

func (r *redisRegistry) key(id string) string { return "recmaestro:registry:" + id }

func (r *redisRegistry) Register(ctx context.Context, svc Service) error {
	return r.Renew(ctx, svc)
}

func (r *redisRegistry) Renew(ctx context.Context, svc Service) error {
	return r.rdb.Set(ctx, r.key(svc.ID), svc.Addr(), leaseTTL).Err()
}

func (r *redisRegistry) Deregister(ctx context.Context, svc Service) error {
	return r.rdb.Del(ctx, r.key(svc.ID)).Err()
}

// consulRegistry 基于 Consul Agent 的注册（TTL 健康检查）。
type consulRegistry struct {
	client *api.Client
}

func (c *consulRegistry) Register(ctx context.Context, svc Service) error {
	if err := c.client.Agent().ServiceRegister(&api.AgentServiceRegistration{
		ID:      svc.ID,
		Name:    svc.Name,
		Address: svc.IP,
		Port:    svc.Port,
		Check:   &api.AgentServiceCheck{TTL: leaseTTL.String()},
	}); err != nil {
		return err
	}
	return c.client.Agent().UpdateTTL("service:"+svc.ID, "ok", api.HealthPassing)
}

func (c *consulRegistry) Renew(ctx context.Context, svc Service) error {
	return c.client.Agent().UpdateTTL("service:"+svc.ID, "ok", api.HealthPassing)
}

func (c *consulRegistry) Deregister(ctx context.Context, svc Service) error {
	return c.client.Agent().ServiceDeregister(svc.ID)
}

// LocalIP 探测本机对外 IP：向公网地址做 UDP 拨号（不实际发包），取路由出口 IP。
// 容器内即容器 IP；无路由环境兜底回环地址。
func LocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
