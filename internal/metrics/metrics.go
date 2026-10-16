// Package metrics 定义 Prometheus 指标。指标用 promauto 注册到默认 registry，
// 通过 promhttp.Handler() 暴露 /metrics。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// TasksByStatus 任务总数，按状态分组。
	TasksByStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "recmaestro",
		Name:      "tasks_total",
		Help:      "任务总数，按状态分组",
	}, []string{"status"})

	// TasksCompletedTotal 累计完成任务数。
	TasksCompletedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "recmaestro",
		Name:      "tasks_completed_total",
		Help:      "累计完成任务数",
	})

	// TasksFailedTotal 累计失败任务数。
	TasksFailedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "recmaestro",
		Name:      "tasks_failed_total",
		Help:      "累计失败任务数",
	})

	// RecordDurationSeconds 录制时长（秒）。
	RecordDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "recmaestro",
		Name:      "record_duration_seconds",
		Help:      "录制时长（秒）",
		Buckets:   prometheus.DefBuckets,
	})

	// FFmpegErrorsTotal 累计 ffmpeg 错误数。
	FFmpegErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "recmaestro",
		Name:      "ffmpeg_errors_total",
		Help:      "累计 ffmpeg 错误数",
	})

	// QueueSize 待执行任务队列积压。
	QueueSize = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "recmaestro",
		Name:      "queue_size",
		Help:      "待执行任务队列积压",
	})

	// ScheduledSize 定时任务队列积压。
	ScheduledSize = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "recmaestro",
		Name:      "scheduled_size",
		Help:      "定时任务队列积压",
	})

	// WorkerCurrentTasks worker 当前任务数。
	WorkerCurrentTasks = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "recmaestro",
		Name:      "worker_current_tasks",
		Help:      "worker 当前任务数",
	}, []string{"node_id"})

	// WorkerDiskFreeBytes worker 磁盘可用字节。
	WorkerDiskFreeBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "recmaestro",
		Name:      "worker_disk_free_bytes",
		Help:      "worker 磁盘可用字节",
	}, []string{"node_id"})

	// WorkerCPUPercent worker CPU 使用率。
	WorkerCPUPercent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "recmaestro",
		Name:      "worker_cpu_percent",
		Help:      "worker CPU 使用率",
	}, []string{"node_id"})

	// WorkerMemPercent worker 内存使用率。
	WorkerMemPercent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "recmaestro",
		Name:      "worker_mem_percent",
		Help:      "worker 内存使用率",
	}, []string{"node_id"})
)
