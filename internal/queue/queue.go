// Package queue 封装任务调度相关的 Redis 操作：定时队列、任务队列、心跳租约、分布式锁。
package queue

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis key 常量。
const (
	keyScheduled      = "recmaestro:scheduled"       // zset: task_id -> 计划开始时间戳
	keyTaskQueue      = "recmaestro:task_queue"      // list: task_id
	keyTaskHeartbeats = "recmaestro:task_heartbeats" // zset: task_id -> 最后心跳时间戳
	keyScheduleLock   = "recmaestro:schedule_lock"   // 调度分布式锁
	keyWorkerLease    = "recmaestro:worker:"         // 前缀 + node_id，节点租约
	keyWorkerAddr     = "recmaestro:worker_addr:"    // 前缀 + node_id，节点内部接口地址
)

// Queue 封装任务调度相关的 Redis 操作。
type Queue struct {
	rdb *redis.Client
}

// NewQueue 创建 Queue。
func NewQueue(rdb *redis.Client) *Queue {
	return &Queue{rdb: rdb}
}

// Schedule 将任务加入定时队列，score 为计划开始时间。
func (q *Queue) Schedule(ctx context.Context, taskID string, start time.Time) error {
	return q.rdb.ZAdd(ctx, keyScheduled, redis.Z{
		Score:  float64(start.Unix()),
		Member: taskID,
	}).Err()
}

// RemoveSchedule 从定时队列移除任务（编辑任务重新调度用）。
func (q *Queue) RemoveSchedule(ctx context.Context, taskID string) error {
	return q.rdb.ZRem(ctx, keyScheduled, taskID).Err()
}

// PopDue 取出到点（score <= now）的任务 ID 并从定时队列移除。
func (q *Queue) PopDue(ctx context.Context, now time.Time) ([]string, error) {
	members, err := q.rdb.ZRangeByScore(ctx, keyScheduled, &redis.ZRangeBy{
		Min: "0",
		Max: strconv.FormatInt(now.Unix(), 10),
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(members) > 0 {
		args := make([]interface{}, len(members))
		for i, m := range members {
			args[i] = m
		}
		if err := q.rdb.ZRem(ctx, keyScheduled, args...).Err(); err != nil {
			return nil, err
		}
	}
	return parseIDs(members), nil
}

// Enqueue 将任务放入待执行队列（list 尾部）。
func (q *Queue) Enqueue(ctx context.Context, taskID string) error {
	return q.rdb.RPush(ctx, keyTaskQueue, taskID).Err()
}

// Dequeue 阻塞弹出待执行任务；block 为 0 表示永久阻塞。
func (q *Queue) Dequeue(ctx context.Context, block time.Duration) (string, error) {
	vals, err := q.rdb.BLPop(ctx, block, keyTaskQueue).Result()
	if err != nil {
		return "", err
	}
	// BLPop 返回 [key, value]
	if len(vals) < 2 {
		return "", redis.Nil
	}
	return vals[1], nil
}

// RemoveEnqueued 从待执行队列尽力移除任务（编辑 queued 任务改回未来定时用）。
// 若任务已被 worker 弹出，此处移除 0 条，由调用方的状态条件更新兜底。
func (q *Queue) RemoveEnqueued(ctx context.Context, taskID string) error {
	return q.rdb.LRem(ctx, keyTaskQueue, 0, taskID).Err()
}

// Heartbeat 记录任务心跳时间。
func (q *Queue) Heartbeat(ctx context.Context, taskID string, ts time.Time) error {
	return q.rdb.ZAdd(ctx, keyTaskHeartbeats, redis.Z{
		Score:  float64(ts.Unix()),
		Member: taskID,
	}).Err()
}

// Heartbeat 是任务心跳记录（含最后心跳时间）。
type Heartbeat struct {
	TaskID string
	LastAt time.Time
}

// ExpiredHeartbeats 返回心跳早于 cutoff 的任务（即心跳超时）。
func (q *Queue) ExpiredHeartbeats(ctx context.Context, cutoff time.Time) ([]Heartbeat, error) {
	zs, err := q.rdb.ZRangeByScoreWithScores(ctx, keyTaskHeartbeats, &redis.ZRangeBy{
		Min: "0",
		Max: strconv.FormatInt(cutoff.Unix(), 10),
	}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Heartbeat, 0, len(zs))
	for _, z := range zs {
		out = append(out, Heartbeat{TaskID: z.Member.(string), LastAt: time.Unix(int64(z.Score), 0)})
	}
	return out, nil
}

// RemoveHeartbeat 移除任务心跳记录（任务终态后调用）。
func (q *Queue) RemoveHeartbeat(ctx context.Context, taskID string) error {
	return q.rdb.ZRem(ctx, keyTaskHeartbeats, taskID).Err()
}

// AcquireScheduleLock 抢调度锁，防止多 scheduler 副本重复调度。
func (q *Queue) AcquireScheduleLock(ctx context.Context, ttl time.Duration) (bool, error) {
	ok, err := q.rdb.SetNX(ctx, keyScheduleLock, "1", ttl).Result()
	return ok, err
}

// ReleaseScheduleLock 释放调度锁。
func (q *Queue) ReleaseScheduleLock(ctx context.Context) error {
	return q.rdb.Del(ctx, keyScheduleLock).Err()
}

// RenewWorkerLease 续期节点租约（TTL 内未续期视为节点失联）。
func (q *Queue) RenewWorkerLease(ctx context.Context, nodeID string, ttl time.Duration) error {
	return q.rdb.Set(ctx, keyWorkerLease+nodeID, "1", ttl).Err()
}

// SetWorkerAddr 上报节点内部接口地址（scheduler 下发文件操作时用）。
func (q *Queue) SetWorkerAddr(ctx context.Context, nodeID, addr string, ttl time.Duration) error {
	return q.rdb.Set(ctx, keyWorkerAddr+nodeID, addr, ttl).Err()
}

// GetWorkerAddr 查询节点内部接口地址。
func (q *Queue) GetWorkerAddr(ctx context.Context, nodeID string) (string, error) {
	return q.rdb.Get(ctx, keyWorkerAddr+nodeID).Result()
}

// ListWorkerAddrs 返回全部在线 worker 的内部接口地址（下载路由兜底用：
// 任务绑定的节点已消亡时，共享存储部署下任一 worker 都能提供文件）。
func (q *Queue) ListWorkerAddrs(ctx context.Context) ([]string, error) {
	keys, _, err := q.rdb.Scan(ctx, 0, keyWorkerAddr+"*", 100).Result()
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	vals, err := q.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(vals))
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			addrs = append(addrs, s)
		}
	}
	return addrs, nil
}

// WorkerAlive 判断节点租约是否存活（文件可达性判断）。
func (q *Queue) WorkerAlive(ctx context.Context, nodeID string) (bool, error) {
	n, err := q.rdb.Exists(ctx, keyWorkerLease+nodeID).Result()
	return n > 0, err
}

// QueueLen 返回待执行任务队列长度。
func (q *Queue) QueueLen(ctx context.Context) (int64, error) {
	return q.rdb.LLen(ctx, keyTaskQueue).Result()
}

// ScheduledLen 返回定时任务队列长度。
func (q *Queue) ScheduledLen(ctx context.Context) (int64, error) {
	return q.rdb.ZCard(ctx, keyScheduled).Result()
}

func parseIDs(members []string) []string {
	return members
}
