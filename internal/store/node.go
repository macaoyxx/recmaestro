package store

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Node 对应 live_rec_nodes 表，记录 worker 节点状态。
type Node struct {
	ID              uint64     `gorm:"column:id;primaryKey" json:"id"`
	NodeID          string     `gorm:"column:node_id" json:"node_id"`
	Host            string     `gorm:"column:host" json:"host"`
	IP              string     `gorm:"column:ip" json:"ip"`
	Status          string     `gorm:"column:status" json:"status"`
	DiskTotal       int64      `gorm:"column:disk_total" json:"disk_total"`
	DiskFree        int64      `gorm:"column:disk_free" json:"disk_free"`
	CPU             float64    `gorm:"column:cpu" json:"cpu"`
	Mem             float64    `gorm:"column:mem" json:"mem"`
	FFmpegVersion   string     `gorm:"column:ffmpeg_version" json:"ffmpeg_version"`
	Capacity        int        `gorm:"column:capacity" json:"capacity"`
	CurrentTasks    int        `gorm:"column:current_tasks" json:"current_tasks"`
	LastHeartbeatAt *time.Time `gorm:"column:last_heartbeat_at" json:"last_heartbeat_at"`
	RegisteredAt    time.Time  `gorm:"column:registered_at" json:"registered_at"`
}

// TableName 返回表名。
func (Node) TableName() string { return "live_rec_nodes" }

// NodeStore 封装对 live_rec_nodes 表的数据访问。
type NodeStore struct {
	db *gorm.DB
}

// NewNodeStore 创建 NodeStore。
func NewNodeStore(db *gorm.DB) *NodeStore {
	return &NodeStore{db: db}
}

// Upsert 按 node_id 上报节点状态（存在则更新，不存在则插入）。
func (s *NodeStore) Upsert(ctx context.Context, n *Node) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"host", "ip", "status", "disk_total", "disk_free", "cpu", "mem", "ffmpeg_version", "capacity", "current_tasks", "last_heartbeat_at"}),
	}).Create(n).Error
}

// List 列出全部节点。
func (s *NodeStore) List(ctx context.Context) ([]Node, error) {
	var nodes []Node
	err := s.db.WithContext(ctx).Order("id").Find(&nodes).Error
	return nodes, err
}

// Delete 删除节点记录（心跳超时即移除：节点表只保留在线节点，重新上线时 Upsert 重新注册）。
func (s *NodeStore) Delete(ctx context.Context, nodeID string) error {
	return s.db.WithContext(ctx).Where("node_id = ?", nodeID).Delete(&Node{}).Error
}
