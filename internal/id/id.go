// Package id 提供分布式唯一 ID 生成（雪花算法）。
package id

import (
	"os"
	"strconv"
	"sync"

	"github.com/bwmarrin/snowflake"
)

var (
	once sync.Once
	node *snowflake.Node
)

// Next 生成一个雪花 ID（int64，全局唯一、趋势递增）。
func Next() int64 {
	once.Do(initNode)
	return node.Generate().Int64()
}

// NextString 生成雪花 ID 字符串（用于任务 ID，支持与 UUID 混用）。
func NextString() string {
	return strconv.FormatInt(Next(), 10)
}

func initNode() {
	// 节点号从环境变量 SNOWFLAKE_NODE 读取，集群部署时每个实例用不同节点号（0~1023）
	n := int64(1)
	if v := os.Getenv("SNOWFLAKE_NODE"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil && parsed >= 0 && parsed < 1024 {
			n = parsed
		}
	}
	var err error
	node, err = snowflake.NewNode(n)
	if err != nil {
		panic(err)
	}
}
