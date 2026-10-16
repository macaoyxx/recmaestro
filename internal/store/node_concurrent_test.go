package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestNodeUpsertConcurrent 并发用例：20 个 goroutine 同时 Upsert 同一 node_id
// （模拟节点启动时多处同时注册/上报心跳）。依赖 uk_node_id 唯一索引 +
// ON DUPLICATE KEY UPDATE 语义：全部成功且最终必须只留 1 行，
// 落库指标为其中一次上报的值，registered_at 保留首次注册时间（DoUpdates 不含该列）。
func TestNodeUpsertConcurrent(t *testing.T) {
	resetTables(t, "live_rec_nodes")
	ctx := context.Background()
	ns := NewNodeStore(testDB)

	const n = 20
	const nodeID = "node-race"
	firstReg := time.Now().Add(-time.Hour).Truncate(time.Second)
	hb := time.Now().Truncate(time.Second)

	hostSet := make(map[string]bool, n)
	capSet := make(map[int]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		host := fmt.Sprintf("host-%02d", i)
		capacity := i + 1
		hostSet[host] = true
		capSet[capacity] = true
		wg.Add(1)
		go func(i int, host string, capacity int) {
			defer wg.Done()
			<-start // 统一发令，让 20 个请求同时打向数据库
			errs[i] = ns.Upsert(ctx, &Node{
				NodeID: nodeID, Host: host, IP: "10.0.0.9", Status: "online",
				Capacity: capacity, CurrentTasks: 0,
				LastHeartbeatAt: &hb, RegisteredAt: firstReg,
			})
		}(i, host, capacity)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d Upsert 报错: %v", i, err)
		}
	}

	nodes, err := ns.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("并发 Upsert 后节点数 = %d, want 恰好 1", len(nodes))
	}
	got := nodes[0]
	if got.NodeID != nodeID {
		t.Errorf("node_id = %q, want %q", got.NodeID, nodeID)
	}
	if !hostSet[got.Host] {
		t.Errorf("host = %q, 应为 20 个上报值之一", got.Host)
	}
	if !capSet[got.Capacity] {
		t.Errorf("capacity = %d, 应为 20 个上报值之一", got.Capacity)
	}
	if !sameSecond(got.RegisteredAt, firstReg) {
		t.Errorf("registered_at = %v, 应保留首次注册时间 %v", got.RegisteredAt, firstReg)
	}
	if got.LastHeartbeatAt == nil || !sameSecond(*got.LastHeartbeatAt, hb) {
		t.Errorf("last_heartbeat_at 不匹配: %+v", got.LastHeartbeatAt)
	}
}
