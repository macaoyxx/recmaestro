package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestQueue 启动 miniredis 内存实例并返回指向它的 Queue（严禁连真实 Redis）。
func newTestQueue(t *testing.T) (*Queue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewQueue(rdb), mr
}

// TestScheduleAndPopDue 验证定时 zset：入队、到点弹出（含 score==now 边界）、弹出即移除。
func TestScheduleAndPopDue(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	now := time.Now()

	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	// 边界用整秒时间戳，确保 score == now 时也应被弹出（ZRangeByScore 的 Max 含边界）
	exact := time.Unix(now.Unix(), 0)

	for _, tc := range []struct {
		id string
		at time.Time
	}{
		{"task-past", past},
		{"task-exact", exact},
		{"task-future", future},
	} {
		if err := q.Schedule(ctx, tc.id, tc.at); err != nil {
			t.Fatalf("Schedule(%s) 失败: %v", tc.id, err)
		}
	}
	if n, err := q.ScheduledLen(ctx); err != nil || n != 3 {
		t.Fatalf("ScheduledLen 期望 3, 实际 %d (err=%v)", n, err)
	}

	// 第一轮：只弹出 <= now 的两个
	due, err := q.PopDue(ctx, now)
	if err != nil {
		t.Fatalf("PopDue 失败: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("PopDue 期望弹出 2 个, 实际 %d: %v", len(due), due)
	}
	got := map[string]bool{}
	for _, id := range due {
		got[id] = true
	}
	if !got["task-past"] || !got["task-exact"] {
		t.Errorf("PopDue 结果缺任务: %v", due)
	}
	if got["task-future"] {
		t.Errorf("未到点任务不应被弹出: %v", due)
	}

	// 弹出即移除：再弹一次为空
	due2, err := q.PopDue(ctx, now)
	if err != nil {
		t.Fatalf("第二次 PopDue 失败: %v", err)
	}
	if len(due2) != 0 {
		t.Errorf("第二次 PopDue 期望为空, 实际 %v", due2)
	}
	if n, _ := q.ScheduledLen(ctx); n != 1 {
		t.Errorf("弹出后 ScheduledLen 期望 1, 实际 %d", n)
	}

	// 时间推进到未来之后，剩余任务到点弹出
	due3, err := q.PopDue(ctx, future.Add(time.Second))
	if err != nil {
		t.Fatalf("第三轮 PopDue 失败: %v", err)
	}
	if len(due3) != 1 || due3[0] != "task-future" {
		t.Errorf("第三轮 PopDue 期望 [task-future], 实际 %v", due3)
	}
	if n, _ := q.ScheduledLen(ctx); n != 0 {
		t.Errorf("全部弹出后 ScheduledLen 期望 0, 实际 %d", n)
	}
}

// TestScheduleOverwrite 验证同一任务重复调度时覆盖原 score（zset 成员唯一）。
func TestScheduleOverwrite(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	now := time.Now()

	if err := q.Schedule(ctx, "task-1", now.Add(time.Hour)); err != nil {
		t.Fatalf("首次 Schedule 失败: %v", err)
	}
	// 编辑任务改期到更早
	if err := q.Schedule(ctx, "task-1", now.Add(-time.Minute)); err != nil {
		t.Fatalf("二次 Schedule 失败: %v", err)
	}
	if n, _ := q.ScheduledLen(ctx); n != 1 {
		t.Fatalf("重复调度后 ScheduledLen 期望 1, 实际 %d", n)
	}
	due, err := q.PopDue(ctx, now)
	if err != nil {
		t.Fatalf("PopDue 失败: %v", err)
	}
	if len(due) != 1 || due[0] != "task-1" {
		t.Errorf("改期后应立即到点, 实际 %v", due)
	}
}

// TestRemoveSchedule 验证从定时队列移除（编辑任务重新调度用）。
func TestRemoveSchedule(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	now := time.Now()

	if err := q.Schedule(ctx, "task-1", now.Add(-time.Minute)); err != nil {
		t.Fatalf("Schedule 失败: %v", err)
	}
	if err := q.RemoveSchedule(ctx, "task-1"); err != nil {
		t.Fatalf("RemoveSchedule 失败: %v", err)
	}
	if n, _ := q.ScheduledLen(ctx); n != 0 {
		t.Errorf("移除后 ScheduledLen 期望 0, 实际 %d", n)
	}
	due, err := q.PopDue(ctx, now)
	if err != nil {
		t.Fatalf("PopDue 失败: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("移除的任务不应被弹出: %v", due)
	}
	// 移除不存在的任务不报错
	if err := q.RemoveSchedule(ctx, "not-exist"); err != nil {
		t.Errorf("移除不存在的任务期望无错, 实际 %v", err)
	}
}

// TestEnqueueDequeueFIFO 验证执行队列先进先出与 QueueLen。
func TestEnqueueDequeueFIFO(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	want := []string{"task-a", "task-b", "task-c"}
	for _, id := range want {
		if err := q.Enqueue(ctx, id); err != nil {
			t.Fatalf("Enqueue(%s) 失败: %v", id, err)
		}
	}
	if n, _ := q.QueueLen(ctx); n != 3 {
		t.Fatalf("QueueLen 期望 3, 实际 %d", n)
	}
	for i, wantID := range want {
		id, err := q.Dequeue(ctx, 2*time.Second)
		if err != nil {
			t.Fatalf("第 %d 次 Dequeue 失败: %v", i+1, err)
		}
		if id != wantID {
			t.Errorf("第 %d 次 Dequeue 期望 %s, 实际 %s（应先进先出）", i+1, wantID, id)
		}
	}
	if n, _ := q.QueueLen(ctx); n != 0 {
		t.Errorf("消费完后 QueueLen 期望 0, 实际 %d", n)
	}
}

// TestDequeueTimeout 验证空队列弹出超时返回 redis.Nil。
func TestDequeueTimeout(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	id, err := q.Dequeue(ctx, time.Second) // go-redis 最小阻塞粒度 1s
	if !errors.Is(err, redis.Nil) {
		t.Fatalf("空队列超时期望 redis.Nil, 实际 err=%v", err)
	}
	if id != "" {
		t.Errorf("超时时返回值应为空串, 实际 %q", id)
	}
}

// TestDequeueBlockForever 验证 block=0 永久阻塞直到有任务入队。
func TestDequeueBlockForever(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	type result struct {
		id  string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		id, err := q.Dequeue(ctx, 0)
		ch <- result{id, err}
	}()
	time.Sleep(200 * time.Millisecond) // 先让 Dequeue 进入阻塞
	if err := q.Enqueue(ctx, "task-late"); err != nil {
		t.Fatalf("Enqueue 失败: %v", err)
	}
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("永久阻塞 Dequeue 失败: %v", r.err)
		}
		if r.id != "task-late" {
			t.Errorf("期望弹出 task-late, 实际 %q", r.id)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("永久阻塞 Dequeue 在入队后 5s 内未被唤醒")
	}
}

// TestRemoveEnqueued 验证从执行队列移除指定任务（含已被弹出/不存在的任务）。
func TestRemoveEnqueued(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	for _, id := range []string{"task-a", "task-b", "task-c"} {
		if err := q.Enqueue(ctx, id); err != nil {
			t.Fatalf("Enqueue(%s) 失败: %v", id, err)
		}
	}
	if err := q.RemoveEnqueued(ctx, "task-b"); err != nil {
		t.Fatalf("RemoveEnqueued 失败: %v", err)
	}
	if n, _ := q.QueueLen(ctx); n != 2 {
		t.Fatalf("移除后 QueueLen 期望 2, 实际 %d", n)
	}
	// 移除不存在的任务不报错（尽力移除语义）
	if err := q.RemoveEnqueued(ctx, "not-exist"); err != nil {
		t.Errorf("移除不存在任务期望无错, 实际 %v", err)
	}
	for i, wantID := range []string{"task-a", "task-c"} {
		id, err := q.Dequeue(ctx, 2*time.Second)
		if err != nil {
			t.Fatalf("第 %d 次 Dequeue 失败: %v", i+1, err)
		}
		if id != wantID {
			t.Errorf("第 %d 次 Dequeue 期望 %s, 实际 %s", i+1, wantID, id)
		}
	}
}

// TestHeartbeatLifecycle 验证任务心跳：写入、超时查询（含 cutoff 边界）、覆盖更新、移除。
func TestHeartbeatLifecycle(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	base := time.Unix(time.Now().Add(-time.Hour).Unix(), 0) // 整秒基准，避免亚秒截断干扰断言

	staleAt := base.Add(10 * time.Second)
	freshAt := base.Add(50 * time.Second)
	cutoff := base.Add(30 * time.Second) // 恰为整秒

	if err := q.Heartbeat(ctx, "task-stale", staleAt); err != nil {
		t.Fatalf("Heartbeat(task-stale) 失败: %v", err)
	}
	if err := q.Heartbeat(ctx, "task-fresh", freshAt); err != nil {
		t.Fatalf("Heartbeat(task-fresh) 失败: %v", err)
	}

	expired, err := q.ExpiredHeartbeats(ctx, cutoff)
	if err != nil {
		t.Fatalf("ExpiredHeartbeats 失败: %v", err)
	}
	if len(expired) != 1 || expired[0].TaskID != "task-stale" {
		t.Fatalf("期望仅 task-stale 超时, 实际 %+v", expired)
	}
	if !expired[0].LastAt.Equal(staleAt) {
		t.Errorf("超时心跳时间期望 %v, 实际 %v", staleAt, expired[0].LastAt)
	}

	// cutoff 早于所有心跳：无超时
	none, err := q.ExpiredHeartbeats(ctx, base.Add(5*time.Second))
	if err != nil {
		t.Fatalf("ExpiredHeartbeats(更早) 失败: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("更早 cutoff 期望无超时, 实际 %+v", none)
	}

	// 覆盖更新：stale 任务重新上报心跳后不再超时
	if err := q.Heartbeat(ctx, "task-stale", freshAt); err != nil {
		t.Fatalf("覆盖 Heartbeat 失败: %v", err)
	}
	expired2, err := q.ExpiredHeartbeats(ctx, cutoff)
	if err != nil {
		t.Fatalf("覆盖后 ExpiredHeartbeats 失败: %v", err)
	}
	if len(expired2) != 0 {
		t.Errorf("覆盖心跳后期望无超时, 实际 %+v", expired2)
	}

	// 终态移除心跳：所有心跳都已移除
	if err := q.RemoveHeartbeat(ctx, "task-fresh"); err != nil {
		t.Fatalf("RemoveHeartbeat 失败: %v", err)
	}
	if err := q.RemoveHeartbeat(ctx, "task-stale"); err != nil {
		t.Fatalf("RemoveHeartbeat 失败: %v", err)
	}
	all, err := q.ExpiredHeartbeats(ctx, freshAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("移除后 ExpiredHeartbeats 失败: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("移除后期望无心跳记录, 实际 %+v", all)
	}
}

// TestHeartbeatCutoffBoundary 验证 score == cutoff 的心跳被判定为超时（Max 含边界）。
func TestHeartbeatCutoffBoundary(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	at := time.Unix(time.Now().Add(-time.Minute).Unix(), 0)

	if err := q.Heartbeat(ctx, "task-boundary", at); err != nil {
		t.Fatalf("Heartbeat 失败: %v", err)
	}
	expired, err := q.ExpiredHeartbeats(ctx, at)
	if err != nil {
		t.Fatalf("ExpiredHeartbeats 失败: %v", err)
	}
	if len(expired) != 1 || expired[0].TaskID != "task-boundary" {
		t.Errorf("score==cutoff 应判超时, 实际 %+v", expired)
	}
}

// TestScheduleLockAcquireRelease 验证调度锁：抢占唯一、释放后可重抢。
func TestScheduleLockAcquireRelease(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	ok, err := q.AcquireScheduleLock(ctx, 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("首次抢锁期望成功, ok=%v err=%v", ok, err)
	}
	// 持锁期间再抢必失败（多副本防重复调度）
	ok2, err := q.AcquireScheduleLock(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("二次抢锁出错: %v", err)
	}
	if ok2 {
		t.Fatalf("持锁期间二次抢锁不应成功")
	}

	if err := q.ReleaseScheduleLock(ctx); err != nil {
		t.Fatalf("ReleaseScheduleLock 失败: %v", err)
	}
	ok3, err := q.AcquireScheduleLock(ctx, 5*time.Second)
	if err != nil || !ok3 {
		t.Fatalf("释放后重抢期望成功, ok=%v err=%v", ok3, err)
	}
}

// TestScheduleLockTTLExpiry 验证调度锁 TTL 到期自动失效（持锁副本宕机兜底）。
func TestScheduleLockTTLExpiry(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()

	ok, err := q.AcquireScheduleLock(ctx, time.Second)
	if err != nil || !ok {
		t.Fatalf("抢锁期望成功, ok=%v err=%v", ok, err)
	}
	mr.FastForward(2 * time.Second) // 推进 miniredis 时钟使锁过期
	ok2, err := q.AcquireScheduleLock(ctx, time.Second)
	if err != nil || !ok2 {
		t.Fatalf("锁过期后抢锁期望成功, ok=%v err=%v", ok2, err)
	}
}

// TestAcquireScheduleLockConcurrent 验证并发抢调度锁仅一个成功。
func TestAcquireScheduleLockConcurrent(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	run := func() (wins int) {
		const n = 16
		start := make(chan struct{})
		won := make(chan bool, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // 同时起跑，最大化竞争窗口
				ok, err := q.AcquireScheduleLock(ctx, 10*time.Second)
				if err != nil {
					t.Errorf("并发抢锁出错: %v", err)
				}
				won <- ok
			}()
		}
		close(start)
		wg.Wait()
		close(won)
		for ok := range won {
			if ok {
				wins++
			}
		}
		return wins
	}

	if wins := run(); wins != 1 {
		t.Errorf("16 个 goroutine 并发抢锁期望恰 1 个成功, 实际 %d 个", wins)
	}
	// 释放后再抢一轮，仍只能有一个成功
	if err := q.ReleaseScheduleLock(ctx); err != nil {
		t.Fatalf("ReleaseScheduleLock 失败: %v", err)
	}
	if wins := run(); wins != 1 {
		t.Errorf("释放后并发抢锁期望恰 1 个成功, 实际 %d 个", wins)
	}
}

// TestDequeueConcurrent 验证多 worker 并发消费同一队列：总领取数恰等于入队数且无重复、无丢失。
func TestDequeueConcurrent(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	const total = 50
	const workers = 8
	enqueued := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("task-%03d", i)
		if err := q.Enqueue(ctx, id); err != nil {
			t.Fatalf("Enqueue(%s) 失败: %v", id, err)
		}
		enqueued[id] = true
	}

	var mu sync.Mutex
	got := make([]string, 0, total)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				// 队列已空时超时返回 redis.Nil，worker 退出循环
				id, err := q.Dequeue(ctx, time.Second)
				if errors.Is(err, redis.Nil) {
					return
				}
				if err != nil {
					t.Errorf("并发 Dequeue 出错: %v", err)
					return
				}
				mu.Lock()
				got = append(got, id)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(got) != total {
		t.Fatalf("并发消费总数期望 %d, 实际 %d（丢失或重复）", total, len(got))
	}
	seen := make(map[string]bool, total)
	for _, id := range got {
		if !enqueued[id] {
			t.Errorf("领取到未入队任务 %q", id)
		}
		if seen[id] {
			t.Errorf("任务 %q 被重复领取", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Errorf("去重后领取数期望 %d, 实际 %d", total, len(seen))
	}
	if n, _ := q.QueueLen(ctx); n != 0 {
		t.Errorf("消费完后 QueueLen 期望 0, 实际 %d", n)
	}
}

// TestWorkerLease 验证节点存活租约：上报后存活、TTL 过期失联、续期恢复、节点间互不影响。
func TestWorkerLease(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()

	// 未上报过租约：不存活
	if alive, err := q.WorkerAlive(ctx, "node-1"); err != nil || alive {
		t.Fatalf("未上报租约期望不存活, alive=%v err=%v", alive, err)
	}
	if err := q.RenewWorkerLease(ctx, "node-1", 2*time.Second); err != nil {
		t.Fatalf("RenewWorkerLease 失败: %v", err)
	}
	if err := q.RenewWorkerLease(ctx, "node-2", time.Hour); err != nil {
		t.Fatalf("RenewWorkerLease(node-2) 失败: %v", err)
	}
	for _, node := range []string{"node-1", "node-2"} {
		if alive, err := q.WorkerAlive(ctx, node); err != nil || !alive {
			t.Errorf("节点 %s 续期后期望存活, alive=%v err=%v", node, alive, err)
		}
	}

	// node-1 租约到期失联，node-2 长租约不受影响
	mr.FastForward(3 * time.Second)
	if alive, _ := q.WorkerAlive(ctx, "node-1"); alive {
		t.Errorf("node-1 租约过期后不应存活")
	}
	if alive, err := q.WorkerAlive(ctx, "node-2"); err != nil || !alive {
		t.Errorf("node-2 期望仍存活, alive=%v err=%v", alive, err)
	}

	// 失联后重新续期恢复存活
	if err := q.RenewWorkerLease(ctx, "node-1", 2*time.Second); err != nil {
		t.Fatalf("重新续期失败: %v", err)
	}
	if alive, err := q.WorkerAlive(ctx, "node-1"); err != nil || !alive {
		t.Errorf("node-1 重新续期后期望存活, alive=%v err=%v", alive, err)
	}
}

// TestWorkerAddr 验证 worker 地址注册/读取/覆盖与未注册节点报 redis.Nil。
func TestWorkerAddr(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	addrs := map[string]string{
		"node-1": "10.0.0.1:8081",
		"node-2": "10.0.0.2:8081",
		"node-3": "10.0.0.3:8081",
	}
	for node, addr := range addrs {
		if err := q.SetWorkerAddr(ctx, node, addr, 10*time.Minute); err != nil {
			t.Fatalf("SetWorkerAddr(%s) 失败: %v", node, err)
		}
	}
	for node, want := range addrs {
		got, err := q.GetWorkerAddr(ctx, node)
		if err != nil {
			t.Fatalf("GetWorkerAddr(%s) 失败: %v", node, err)
		}
		if got != want {
			t.Errorf("GetWorkerAddr(%s) 期望 %s, 实际 %s", node, want, got)
		}
	}

	// 覆盖上报（节点重启换地址）
	if err := q.SetWorkerAddr(ctx, "node-1", "10.0.0.9:8081", 10*time.Minute); err != nil {
		t.Fatalf("覆盖 SetWorkerAddr 失败: %v", err)
	}
	if got, _ := q.GetWorkerAddr(ctx, "node-1"); got != "10.0.0.9:8081" {
		t.Errorf("覆盖后地址期望 10.0.0.9:8081, 实际 %s", got)
	}

	// 未注册节点返回 redis.Nil
	if _, err := q.GetWorkerAddr(ctx, "node-x"); !errors.Is(err, redis.Nil) {
		t.Errorf("未注册节点期望 redis.Nil, 实际 %v", err)
	}
}

// TestListWorkerAddrs 验证枚举全部在线 worker 地址：含全部存活节点、剔除过期节点。
func TestListWorkerAddrs(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()

	want := map[string]bool{
		"10.0.0.1:8081": false,
		"10.0.0.2:8081": false,
		"10.0.0.3:8081": false,
	}
	for node, addr := range map[string]string{
		"node-1": "10.0.0.1:8081",
		"node-2": "10.0.0.2:8081",
		"node-3": "10.0.0.3:8081",
		"node-4": "10.0.0.4:8081", // 短租约，随后过期
	} {
		if err := q.SetWorkerAddr(ctx, node, addr, 10*time.Minute); err != nil {
			t.Fatalf("SetWorkerAddr(%s) 失败: %v", node, err)
		}
	}
	// node-4 单独短租约覆盖，过期后不应出现在枚举结果里
	if err := q.SetWorkerAddr(ctx, "node-4", "10.0.0.4:8081", time.Second); err != nil {
		t.Fatalf("SetWorkerAddr(node-4) 失败: %v", err)
	}
	mr.FastForward(2 * time.Second)

	addrs, err := q.ListWorkerAddrs(ctx)
	if err != nil {
		t.Fatalf("ListWorkerAddrs 失败: %v", err)
	}
	if len(addrs) != len(want) {
		t.Fatalf("期望枚举 %d 个地址, 实际 %d: %v", len(want), len(addrs), addrs)
	}
	for _, addr := range addrs {
		if _, ok := want[addr]; !ok {
			t.Errorf("枚举到意外地址 %q（过期的 node-4 应被剔除）", addr)
			continue
		}
		want[addr] = true
	}
	for addr, hit := range want {
		if !hit {
			t.Errorf("枚举结果缺少地址 %q", addr)
		}
	}
}
