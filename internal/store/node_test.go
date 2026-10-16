package store

import (
	"context"
	"testing"
	"time"
)

// TestNodeUpsert 验证节点上报语义：首次插入；同 node_id 再次上报为原地更新，
// 不新增行且 registered_at 保留首次注册时间（DoUpdates 不含该列）。
func TestNodeUpsert(t *testing.T) {
	resetTables(t, "live_rec_nodes")
	ctx := context.Background()
	ns := NewNodeStore(testDB)

	firstHB := time.Now().Add(time.Hour).Truncate(time.Second)
	firstReg := time.Now().Add(-time.Hour).Truncate(time.Second)

	// 首次上报：插入
	if err := ns.Upsert(ctx, &Node{
		NodeID: "node-a", Host: "host-old", IP: "10.0.0.1", Status: "online",
		DiskTotal: 1000, DiskFree: 500, CPU: 0.1, Mem: 0.2,
		FFmpegVersion: "6.0", Capacity: 10, CurrentTasks: 3,
		LastHeartbeatAt: &firstHB, RegisteredAt: firstReg,
	}); err != nil {
		t.Fatalf("首次 Upsert 失败: %v", err)
	}

	nodes, err := ns.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("首次 Upsert 后节点数 = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.NodeID != "node-a" || n.Host != "host-old" || n.Capacity != 10 || n.CurrentTasks != 3 {
		t.Errorf("首次上报字段不匹配: %+v", n)
	}
	if !sameSecond(n.RegisteredAt, firstReg) {
		t.Errorf("registered_at = %v, want %v", n.RegisteredAt, firstReg)
	}

	// 二次上报：同 node_id 原地更新，指标刷新、注册时间保留
	secondHB := time.Now().Truncate(time.Second)
	secondReg := time.Now().Add(time.Minute).Truncate(time.Second)
	if err := ns.Upsert(ctx, &Node{
		NodeID: "node-a", Host: "host-new", IP: "10.0.0.2", Status: "online",
		DiskTotal: 2000, DiskFree: 1500, CPU: 0.5, Mem: 0.6,
		FFmpegVersion: "7.0", Capacity: 20, CurrentTasks: 0,
		LastHeartbeatAt: &secondHB, RegisteredAt: secondReg,
	}); err != nil {
		t.Fatalf("二次 Upsert 失败: %v", err)
	}

	nodes, err = ns.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("二次 Upsert 后节点数 = %d, want 1（不应新增行）", len(nodes))
	}
	n = nodes[0]
	if n.Host != "host-new" || n.IP != "10.0.0.2" {
		t.Errorf("更新后 host/ip 不匹配: host=%q ip=%q", n.Host, n.IP)
	}
	if n.Capacity != 20 || n.CurrentTasks != 0 || n.FFmpegVersion != "7.0" {
		t.Errorf("更新后指标不匹配: %+v", n)
	}
	if n.DiskTotal != 2000 || n.DiskFree != 1500 || n.CPU != 0.5 || n.Mem != 0.6 {
		t.Errorf("更新后资源信息不匹配: %+v", n)
	}
	if n.LastHeartbeatAt == nil || !sameSecond(*n.LastHeartbeatAt, secondHB) {
		t.Errorf("last_heartbeat_at = %+v, want %v", n.LastHeartbeatAt, secondHB)
	}
	if !sameSecond(n.RegisteredAt, firstReg) {
		t.Errorf("registered_at = %v, 应保留首次注册时间 %v", n.RegisteredAt, firstReg)
	}
}

// TestNodeListAndDelete 验证多节点列表按 id 升序、按 node_id 删除，
// 以及删除后重新上线可经 Upsert 再注册（节点表只保留在线节点的语义）。
func TestNodeListAndDelete(t *testing.T) {
	resetTables(t, "live_rec_nodes")
	ctx := context.Background()
	ns := NewNodeStore(testDB)

	now := time.Now().Truncate(time.Second)
	upsert := func(nodeID, host string) {
		t.Helper()
		if err := ns.Upsert(ctx, &Node{
			NodeID: nodeID, Host: host, IP: "10.0.0.1", Status: "online",
			Capacity: 10, CurrentTasks: 1, LastHeartbeatAt: &now, RegisteredAt: now,
		}); err != nil {
			t.Fatalf("Upsert %s 失败: %v", nodeID, err)
		}
	}
	upsert("node-b", "host-b")
	upsert("node-c", "host-c")

	nodes, err := ns.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("节点数 = %d, want 2", len(nodes))
	}
	if nodes[0].NodeID != "node-b" || nodes[1].NodeID != "node-c" {
		t.Errorf("列表应按 id 升序，实际 [%s %s]", nodes[0].NodeID, nodes[1].NodeID)
	}
	if nodes[0].ID >= nodes[1].ID {
		t.Errorf("列表应按 id 升序: %d >= %d", nodes[0].ID, nodes[1].ID)
	}

	// 心跳超时删除 node-b
	if err := ns.Delete(ctx, "node-b"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	nodes, err = ns.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "node-c" {
		t.Errorf("删除 node-b 后应只剩 node-c: %+v", nodes)
	}

	// 重新上线：删除后再次 Upsert 可重新注册
	upsert("node-b", "host-b-back")
	nodes, err = ns.List(ctx)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("重新上线后节点数 = %d, want 2", len(nodes))
	}
	var back *Node
	for i := range nodes {
		if nodes[i].NodeID == "node-b" {
			back = &nodes[i]
		}
	}
	if back == nil || back.Host != "host-b-back" {
		t.Errorf("重新注册的 node-b 不匹配: %+v", back)
	}
}
