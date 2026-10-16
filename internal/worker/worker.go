// Package worker 实现录制执行器：认领任务、ffmpeg 录制、合并、心跳、上报。
package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"gorm.io/gorm"

	"recmaestro/internal/config"
	"recmaestro/internal/metrics"
	"recmaestro/internal/queue"
	"recmaestro/internal/recorder"
	"recmaestro/internal/registry"
	"recmaestro/internal/store"
)

// 任务执行参数常量。
const (
	probeTimeout         = 8 * time.Second // 开录前源流探测超时
	noProgressFailRounds = 2               // 断流重连连续无新数据的判定轮数（约 2×重连延迟）
	reconnectDelay       = 5 * time.Second // 断流重连间隔
	mergeMaxAttempts     = 3               // 合并重试次数，仍失败保留 TS 段人工介入
	mergeRetryDelay      = 5 * time.Second // 合并重试间隔
	durationTolerance    = 0.10            // 合并后产物时长与段时长合计的容差（10%），超差视为合并不完整
	finalizeMaxAttempts  = 4               // 收尾关键写库（产物记录/终态）尝试次数：1 次 + 3 次退避重试
	finalizeRetryDelay   = 1 * time.Second // 收尾写库重试退避基数，指数递增 1s/2s/4s
	healInterval         = 15 * time.Second // 自愈扫描周期（与 resumeLoop 一致）
)

// Worker 是录制执行器。
type Worker struct {
	nodeID        string
	nodeIP        string
	cfg           *config.Config
	rec           *recorder.Recorder
	tasks         *store.TaskStore
	files         *store.TaskFileStore
	nodes         *store.NodeStore
	queue         *queue.Queue
	logger        *store.TaskLogger
	logs          *store.TaskLogStore
	ffmpegVersion string
	currentTasks  atomic.Int64
	taskWG        sync.WaitGroup // 在跑任务等待组：优雅停机时等待录制收尾
	inflight      sync.Map       // 在跑任务 ID 集合：healLoop 据此区分「在录/收尾中」与「执行体已退出的残留 running」
	registry      registry.Registry
	regSvc        registry.Service
}

// NewWorker 创建 worker。nodeID 是任务/文件路由身份，默认用规范服务 ID
// "recmaestro-worker-<ip>-<port>"（与注册 ID 一致：同机同端口重启仍是同一节点，
// 节点表只有一条记录），可用 NODE_ID 环境变量显式覆盖。
func NewWorker(cfg *config.Config, db *gorm.DB, rdb *redis.Client) *Worker {
	nodeIP := registry.LocalIP()
	svc := registry.NewService("recmaestro-worker", nodeIP, cfg.Server.Port)
	nodeID := os.Getenv("NODE_ID")
	if nodeID == "" {
		nodeID = svc.ID
	}

	// 服务注册引擎（默认 redis，可配置 consul）
	reg, err := registry.New(cfg.Registry.Engine, rdb, cfg.Consul.Addr, cfg.Consul.Token, cfg.Consul.Scheme)
	if err != nil {
		log.Printf("[warn] %v，退化为不注册", err)
		reg = nil
	}

	return &Worker{
		nodeID:   nodeID,
		nodeIP:   nodeIP,
		cfg:      cfg,
		rec:      recorder.New(cfg.Recorder.FFmpegPath, cfg.Recorder.FFmpegLogLevel, cfg.Recorder.Reconnect.DelayMax),
		tasks:    store.NewTaskStore(db),
		files:    store.NewTaskFileStore(db),
		nodes:    store.NewNodeStore(db),
		queue:    queue.NewQueue(rdb),
		logger:   store.NewTaskLogger(db),
		logs:     store.NewTaskLogStore(db),
		registry: reg,
		regSvc:   svc,
	}
}

// Run 启动 worker 主循环。
func (w *Worker) Run(ctx context.Context) {
	log.Printf("worker 启动: node_id=%s ip=%s", w.nodeID, w.nodeIP)
	w.logger.Start(ctx)

	w.ffmpegVersion = detectFFmpegVersion(w.cfg.Recorder.FFmpegPath)

	// 立即上报内部接口地址与节点状态
	_ = w.queue.SetWorkerAddr(ctx, w.nodeID, w.workerAddr(), 30*time.Second)
	w.reportNodeStatus(ctx)

	// 服务注册（引擎可配置：redis 默认 / consul），随心跳续期
	if w.registry != nil {
		if err := w.registry.Register(ctx, w.regSvc); err != nil {
			log.Printf("[warn] 服务注册失败: %v", err)
		} else {
			log.Printf("已注册服务: %s", w.regSvc.ID)
		}
		defer func() { _ = w.registry.Deregister(context.Background(), w.regSvc) }()
	}

	w.resumeOwnTasks(ctx)

	go w.nodeHeartbeatLoop(ctx)
	go w.cleanupLoop(ctx)
	go w.resumeLoop(ctx)
	go w.healLoop(ctx)
	w.claimLoop(ctx)
}

// resumeLoop 周期检查本节点名下 recovering 的任务并恢复续录。
// 场景：worker 短暂宕机后快速重启，任务当时仍是 running；心跳超时窗口过后
// 才被 scheduler 置 recovering——此时 worker 已启动完，启动时的 resumeOwnTasks
// 已错过，必须靠本循环持续接管。
func (w *Worker) resumeLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.resumeOwnTasks(ctx)
		}
	}
}

// resumeOwnTasks 恢复本节点名下 recovering 的任务为 running 并继续执行。
func (w *Worker) resumeOwnTasks(ctx context.Context) {
	tasks, err := w.tasks.ListRecoveringByWorker(ctx, w.nodeID)
	if err != nil {
		log.Printf("[warn] 查询待恢复任务失败: %v", err)
		return
	}
	for _, t := range tasks {
		// 先登记 in-flight 再改状态：healLoop 的快照若含此任务，必能查到占坑，
		// 否则会在 Resume 提交与 goExecuteTask 登记之间把续录任务误判为残留 running
		w.inflight.Store(t.ID, struct{}{})
		if err := w.tasks.Resume(ctx, t.ID); err == nil {
			w.currentTasks.Add(1) // 与 claimLoop 认领对称，恢复执行同样占槽
			log.Printf("恢复任务 %s 为 running", t.ID)
			w.goExecuteTask(ctx, t.ID)
		} else {
			w.inflight.Delete(t.ID)
		}
	}
}

// goExecuteTask 异步执行任务并纳入等待组（优雅停机时等待在录任务收尾）。
func (w *Worker) goExecuteTask(ctx context.Context, id string) {
	w.taskWG.Add(1)
	w.inflight.Store(id, struct{}{})
	go func() {
		defer w.taskWG.Done()
		defer w.inflight.Delete(id)
		w.executeTask(ctx, id)
	}()
}

// Wait 阻塞等待全部在跑任务收尾（优雅停机用）。
func (w *Worker) Wait() { w.taskWG.Wait() }

// nodeHeartbeatLoop 定期续期节点租约（TTL 内未续期视为节点失联）。
func (w *Worker) nodeHeartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	addr := w.workerAddr()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.queue.RenewWorkerLease(ctx, w.nodeID, 30*time.Second); err != nil {
				log.Printf("[warn] 续期节点租约失败: %v", err)
			}
			_ = w.queue.SetWorkerAddr(ctx, w.nodeID, addr, 30*time.Second)
			if w.registry != nil {
				_ = w.registry.Renew(ctx, w.regSvc)
			}
			w.reportNodeStatus(ctx)
		}
	}
}

// workerAddr 返回 worker 对外可访问的内部接口地址。
// Docker 编排下可通过 WORKER_ADDR 覆盖；默认用本机 IP（容器网络内可路由），
// 不再用监听地址 0.0.0.0（不可路由）。
func (w *Worker) workerAddr() string {
	if v := os.Getenv("WORKER_ADDR"); v != "" {
		return v
	}
	return fmt.Sprintf("%s:%d", w.nodeIP, w.cfg.Server.Port)
}

// reportNodeStatus 采集系统信息并上报节点状态 + 更新 Prometheus 指标。
func (w *Worker) reportNodeStatus(ctx context.Context) {
	var diskTotal, diskFree int64
	if usage, err := disk.Usage(w.cfg.Recorder.RecordsDir); err == nil {
		diskTotal = int64(usage.Total)
		diskFree = int64(usage.Free)
	}

	var cpuPercent, memPercent float64
	if v, err := cpu.Percent(0, false); err == nil && len(v) > 0 {
		cpuPercent = v[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		memPercent = vm.UsedPercent
	}

	host, _ := os.Hostname()
	currentTasks := int(w.currentTasks.Load())
	now := time.Now()

	_ = w.nodes.Upsert(ctx, &store.Node{
		NodeID:          w.nodeID,
		Host:            host,
		IP:              w.nodeIP,
		Status:          "online",
		DiskTotal:       diskTotal,
		DiskFree:        diskFree,
		CPU:             cpuPercent,
		Mem:             memPercent,
		FFmpegVersion:   w.ffmpegVersion,
		Capacity:        w.cfg.Recorder.WorkerCapacity,
		CurrentTasks:    currentTasks,
		LastHeartbeatAt: &now,
		RegisteredAt:    now,
	})

	metrics.WorkerCurrentTasks.WithLabelValues(w.nodeID).Set(float64(currentTasks))
	metrics.WorkerDiskFreeBytes.WithLabelValues(w.nodeID).Set(float64(diskFree))
	metrics.WorkerCPUPercent.WithLabelValues(w.nodeID).Set(cpuPercent)
	metrics.WorkerMemPercent.WithLabelValues(w.nodeID).Set(memPercent)
}

// detectFFmpegVersion 探测 ffmpeg 版本（截断到列长限制内）。
func detectFFmpegVersion(path string) string {
	if path == "" {
		path = "ffmpeg"
	}
	out, err := exec.Command(path, "-version").Output()
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	const maxLen = 60
	if len(version) > maxLen {
		version = version[:maxLen]
	}
	return version
}

// claimLoop 阻塞认领任务并异步执行。
func (w *Worker) claimLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 容量检查：当前任务数达到上限则不认领，等待让出给其他节点
		if int(w.currentTasks.Load()) >= w.cfg.Recorder.WorkerCapacity {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}

		id, err := w.queue.Dequeue(ctx, 2*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// BLPop 2s 窗口无任务是正常空转；真实错误（Redis 故障）时立即返回会
			// 形成忙循环狂打 Redis，退避 2s 再试
			if !errors.Is(err, redis.Nil) {
				log.Printf("[warn] 认领任务出队失败: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
			continue
		}

		// 先登记 in-flight 再认领（改状态）：与 resumeOwnTasks 同理，防 healLoop
		// 在 Claim 提交与 goExecuteTask 登记之间把重试任务误判为残留 running
		w.inflight.Store(id, struct{}{})
		ok, err := w.tasks.Claim(ctx, id, w.nodeID, time.Now())
		if err != nil {
			w.inflight.Delete(id)
			log.Printf("[warn] 认领任务 %s 失败: %v", id, err)
			continue
		}
		if !ok {
			w.inflight.Delete(id)
			continue
		}
		w.currentTasks.Add(1)
		w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageClaimed, "worker 认领任务", nil, w.nodeID)
		w.goExecuteTask(ctx, id)
	}
}

// executeTask 执行任务：ffmpeg 拉流录制 TS 段 → 合并转 MP4 → 校验 → 清理 → 上报产物。
func (w *Worker) executeTask(ctx context.Context, id string) {
	defer w.currentTasks.Add(-1)
	task, err := w.tasks.GetByID(ctx, id)
	if err != nil || task == nil {
		log.Printf("[warn] 获取任务 %s 失败: %v", id, err)
		return
	}

	recordsDir := filepath.Join(w.cfg.Recorder.RecordsDir, id)

	// 续录：起始段号 = 已有最大段号 + 1（DB 与磁盘对账，见 nextStartSeq）
	startSeq := w.nextStartSeq(ctx, id, recordsDir)

	rec := w.rec

	// 录制 ctx（可取消，用于中止检测）
	recordCtx, cancelRecord := context.WithCancel(ctx)
	defer cancelRecord()

	// 录制期间心跳 + 中止检测
	stopHeartbeat := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ticker.C:
				now := time.Now()
				_ = w.queue.Heartbeat(ctx, id, now)
				_ = w.tasks.Heartbeat(ctx, id, now)
				// 中止/取消检测：任务被中止或取消时停止录制
				cur, e := w.tasks.GetByID(ctx, id)
				if e == nil && cur != nil && (cur.Status == store.TaskAborted || cur.Status == store.TaskCancelled) {
					cancelRecord()
					return
				}
			}
		}
	}()
	defer close(stopHeartbeat)

	// 未到开始时间则不录制，等待到点
	if task.StartTime != nil && task.StartTime.After(time.Now()) {
		w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageStarted,
			fmt.Sprintf("等待开始时间 %s", task.StartTime.Format("2006-01-02 15:04:05")), nil, w.nodeID)
		log.Printf("任务 %s 等待开始时间 %s", id, task.StartTime.Format(time.RFC3339))
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(*task.StartTime)):
		}
	}

	// 源流探测：无效流快速失败进入自动重试，不进录制循环占用执行槽
	if err := rec.ProbeStream(ctx, task.StreamURL, probeTimeout); err != nil {
		if ctx.Err() != nil {
			return // worker 停止，靠恢复机制接管
		}
		metrics.FFmpegErrorsTotal.Inc()
		w.failAndRetry(ctx, task, "stream_unavailable", err.Error())
		return
	}

	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageStarted,
		fmt.Sprintf("开始录制 %s (start_seq=%d)", task.StreamURL, startSeq), nil, w.nodeID)
	log.Printf("任务 %s 开始录制: %s (start_seq=%d)", id, task.StreamURL, startSeq)

	var allSegments []recorder.Segment
	prevSegCount := 0     // 上一轮 Record 出错时的目录段数，用于判断重连期间有无新数据
	noProgressRounds := 0 // 连续无新数据的重连轮数

	for {
		if task.EndTime != nil && !time.Now().Before(*task.EndTime) {
			break // 已到点
		}

		segments, err := rec.Record(recordCtx, recorder.RecordOptions{
			StreamURL:   task.StreamURL,
			RecordsDir:  recordsDir,
			SegmentSecs: task.SegmentSeconds,
			StartSeq:    startSeq,
			EndTime:     task.EndTime,
			OnSegment: func(seg recorder.Segment) {
				// 实时上报段记录（size 暂不填，录制结束后统一修正）。
				// 实时记录是为了续录时能通过 MaxSegment 拿到正确起始段号；
				// 幂等创建避免续录时旧段被重新扫描造成重复记录。
				_ = w.files.CreateSegmentIfAbsent(ctx, &store.TaskFile{
					TaskID:   id,
					NodeID:   w.nodeID,
					Filename: filepath.Base(seg.Path),
					Type:     store.FileTypeSegment,
					Path:     seg.Path,
					Seq:      seg.Seq,
					Status:   "ok",
				})
			},
			OnLog: func(line string) {
				w.logger.Log(id, store.LogLevelWarn, store.LogSourceFFmpegRecord, store.LogStageRecording, line, nil, w.nodeID)
			},
		})

		if err == nil {
			allSegments = segments // 到点正常结束：Record 返回目录全量段即最终段
			break
		}
		if ctx.Err() != nil {
			return
		}
		// 中止/取消：recordCtx 被取消（外层 ctx 未取消），停止录制不合并
		if recordCtx.Err() != nil {
			log.Printf("任务 %s 被中止，停止录制", id)
			return
		}
		if task.EndTime != nil && !time.Now().Before(*task.EndTime) {
			allSegments = segments // 断流但已到点，正常结束
			break
		}

		// 断流重连：Record 返回的是目录全量段，与上一轮比较判断有无新数据；
		// 连续无新数据达到阈值则快速退出走自动重试，不占执行槽干等
		if len(segments) > prevSegCount {
			noProgressRounds = 0
		} else {
			noProgressRounds++
		}
		prevSegCount = len(segments)

		if noProgressRounds >= noProgressFailRounds {
			metrics.FFmpegErrorsTotal.Inc()
			w.failAndRetry(ctx, task, "stream_lost",
				fmt.Sprintf("断流且连续 %d 轮重连无新数据: %v", noProgressRounds, err))
			return
		}

		startSeq = w.nextStartSeq(ctx, id, recordsDir)
		w.logger.Log(id, store.LogLevelWarn, store.LogSourceWorker, store.LogStageReconnect,
			fmt.Sprintf("录制中断，续录 (start_seq=%d)", startSeq), nil, w.nodeID)
		log.Printf("任务 %s 录制中断，续录 (start_seq=%d)", id, startSeq)

		// 重连延迟，避免拉流失败时频繁启停 ffmpeg 形成 tight loop
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}

	// 到点/断流收尾：本轮全量段为空时（如等待开始时间期间截止时间已到、循环开头直接
	// break），目录可能仍有自动重试续录的历史段，先从 DB 补齐再判空，避免历史段被误判为"未录制到任何段"
	if len(allSegments) == 0 {
		allSegments = w.loadSegmentFiles(ctx, id)
	}
	if len(allSegments) == 0 {
		w.failAndRetry(ctx, task, "record_failed", "未录制到任何段")
		return
	}
	w.finishTask(ctx, task, recordsDir, allSegments)
}

// nextStartSeq 计算续录起始段号 = max(DB 最大段号, 磁盘最大段号) + 1。
// 磁盘兜底：宕机/异常退出可能落在段扫描周期内（DB 还没记上最新段），只看 DB 会算小，
// 导致 ffmpeg -segment_start_number 覆盖重写磁盘上已有的段文件。
func (w *Worker) nextStartSeq(ctx context.Context, id, recordsDir string) int {
	startSeq := 1
	if last, err := w.files.MaxSegment(ctx, id); err == nil && last != nil {
		startSeq = last.Seq + 1
	}
	if diskMax := recorder.MaxSeqOnDisk(recordsDir); diskMax >= startSeq {
		startSeq = diskMax + 1
	}
	return startSeq
}

// loadSegmentFiles 从 DB 读取任务的 TS 段记录构造合并输入（自动重试续录后的历史段）。
func (w *Worker) loadSegmentFiles(ctx context.Context, id string) []recorder.Segment {
	files, err := w.files.ListActiveByTask(ctx, id)
	if err != nil {
		return nil
	}
	segs := make([]recorder.Segment, 0, len(files))
	for _, f := range files {
		if f.Type == store.FileTypeSegment {
			segs = append(segs, recorder.Segment{Seq: f.Seq, Path: f.Path, Size: f.SizeBytes})
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Seq < segs[j].Seq })
	return segs
}

// finishTask 合并 TS 段为 MP4（就地重试）→ 逐段预检与时长对账 → 上报产物 → 清理 → 完成任务。
// 任何一步不通过都保留 TS 段人工介入（详情页可手动合并），绝不静默丢数据、绝不在对账前删段。
func (w *Worker) finishTask(ctx context.Context, task *store.Task, recordsDir string, allSegments []recorder.Segment) {
	id := task.ID
	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageStopped,
		fmt.Sprintf("录制结束，共 %d 段", len(allSegments)), nil, w.nodeID)

	// 修正段 size（录制结束，段都写完，size 准确）
	for _, seg := range allSegments {
		_ = w.files.UpdateSegmentSize(ctx, id, seg.Seq, seg.Size)
	}

	// 合并前逐段健康预检：异常退出（强杀/宕机）可能在磁盘上留下截断的损坏段，
	// 混进清单会让 concat 中止且退出码为 0，静默丢弃其后所有内容。
	// 损坏段：改名 .corrupt 隔离保留、DB 标记 corrupt、error 日志告警，不进合并清单
	healthy := make([]recorder.Segment, 0, len(allSegments))
	expectedDur := 0.0 // 健康段时长合计，作为合并后对账基准
	for _, seg := range allSegments {
		d, err := w.rec.ProbeDuration(ctx, seg.Path)
		if err != nil || d <= 0 {
			corruptPath := seg.Path + ".corrupt"
			if rnErr := os.Rename(seg.Path, corruptPath); rnErr != nil {
				log.Printf("[warn] 任务 %s 损坏段 %s 隔离改名失败: %v", id, filepath.Base(seg.Path), rnErr)
			}
			_ = w.files.MarkSegmentCorrupt(ctx, id, seg.Seq, corruptPath)
			w.logger.Log(id, store.LogLevelError, store.LogSourceWorker, store.LogStageMerging,
				fmt.Sprintf("段 %s 探测失败（疑似异常截断），已隔离不参与合并: %v", filepath.Base(seg.Path), err), nil, w.nodeID)
			log.Printf("[warn] 任务 %s 段 %s 判定损坏，已隔离: %v", id, filepath.Base(seg.Path), err)
			continue
		}
		expectedDur += d
		healthy = append(healthy, seg)
	}
	if len(healthy) == 0 {
		msg := fmt.Sprintf("全部 %d 个段探测失败，无法合并，已保留 TS 段", len(allSegments))
		w.logger.Log(id, store.LogLevelError, store.LogSourceWorker, store.LogStageFailed, msg, nil, w.nodeID)
		metrics.TasksFailedTotal.Inc()
		log.Printf("[warn] 任务 %s %s", id, msg)
		_ = w.tasks.Fail(ctx, id, store.TaskMergeFailed, "segments_corrupt", msg)
		return
	}

	// 合并 TS 段为 MP4：就地重试，连续失败保留 TS 段人工介入（详情页可手动合并）
	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageMerging,
		fmt.Sprintf("开始合并 TS 段（健康 %d 段，已隔离损坏 %d 段）", len(healthy), len(allSegments)-len(healthy)), nil, w.nodeID)
	outputPath := filepath.Join(recordsDir, "output.mp4")
	var mergeErr error
	for attempt := 1; attempt <= mergeMaxAttempts; attempt++ {
		if mergeErr = w.rec.Merge(ctx, healthy, recordsDir, outputPath, func(line string) {
			w.logger.Log(id, store.LogLevelWarn, store.LogSourceFFmpegMerge, store.LogStageMerging, line, nil, w.nodeID)
		}); mergeErr == nil {
			break
		}
		w.logger.Log(id, store.LogLevelError, store.LogSourceFFmpegMerge, store.LogStageMerging,
			fmt.Sprintf("合并失败(第 %d/%d 次): %v", attempt, mergeMaxAttempts, mergeErr), nil, w.nodeID)
		if attempt < mergeMaxAttempts {
			select {
			case <-ctx.Done():
				return
			case <-time.After(mergeRetryDelay):
			}
		}
	}
	if mergeErr != nil {
		w.logger.Log(id, store.LogLevelError, store.LogSourceWorker, store.LogStageFailed,
			fmt.Sprintf("合并连续 %d 次失败，已保留 TS 段，请到详情页手动合并", mergeMaxAttempts), nil, w.nodeID)
		metrics.TasksFailedTotal.Inc()
		metrics.FFmpegErrorsTotal.Inc()
		log.Printf("[warn] 任务 %s 合并失败(已重试 %d 次): %v", id, mergeMaxAttempts, mergeErr)
		_ = w.tasks.Fail(ctx, id, store.TaskMergeFailed, "merge_failed", mergeErr.Error())
		return
	}
	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageMerged, "合并完成", nil, w.nodeID)

	// 时长对账：产物时长须与段时长合计基本一致，堵住"退出码 0 但内容缺斤短两"的静默丢失；
	// 探测失败或超差都不置完成、不清理段
	duration, err := w.rec.ProbeDuration(ctx, outputPath)
	if err != nil {
		msg := fmt.Sprintf("产物时长探测失败，疑似合并不完整，已保留 TS 段: %v", err)
		w.logger.Log(id, store.LogLevelError, store.LogSourceWorker, store.LogStageFailed, msg, nil, w.nodeID)
		metrics.TasksFailedTotal.Inc()
		log.Printf("[warn] 任务 %s %s", id, msg)
		_ = w.tasks.Fail(ctx, id, store.TaskMergeFailed, "probe_failed", msg)
		return
	}
	if expectedDur > 0 {
		if dev := math.Abs(duration-expectedDur) / expectedDur; dev > durationTolerance {
			msg := fmt.Sprintf("产物时长 %.1fs 与段合计 %.1fs 偏差 %.0f%%，疑似合并不完整，已保留 TS 段",
				duration, expectedDur, dev*100)
			w.logger.Log(id, store.LogLevelError, store.LogSourceWorker, store.LogStageFailed, msg, nil, w.nodeID)
			metrics.TasksFailedTotal.Inc()
			log.Printf("[warn] 任务 %s %s", id, msg)
			_ = w.tasks.Fail(ctx, id, store.TaskMergeFailed, "duration_mismatch", msg)
			return
		}
	}
	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageValidated,
		fmt.Sprintf("产物时长 %.1fs（段合计 %.1fs，对账通过）", duration, expectedDur), nil, w.nodeID)

	// 上报产物元数据 + 终态落库（对账通过才允许走到这里）。
	// 两步均带瞬态错误有界重试且绝不吞错（高并发-3：停录洪峰 MySQL Error 1040 曾把
	// 这两步的失败静默丢弃，任务永久卡 running、产物记录缺失）；穷尽仍失败则保留
	// TS 段与产物现场并删除任务心跳，由 healLoop 对「日志已 validated + 磁盘产物在」
	// 的任务幂等补写，保证三表最终一致。
	if info, statErr := os.Stat(outputPath); statErr == nil {
		if err := w.finalizeDBOp(ctx, "产物文件记录", id, func() error {
			return w.files.CreateOutputIfAbsent(ctx, &store.TaskFile{
				TaskID:    id,
				NodeID:    w.nodeID,
				Filename:  "output.mp4",
				Type:      store.FileTypeOutput,
				Path:      outputPath,
				SizeBytes: info.Size(),
				Status:    "ok",
			})
		}); err != nil {
			w.reportFinalizeFailure(ctx, id, "产物文件记录", err)
			return
		}
	}
	if err := w.finalizeDBOp(ctx, "任务终态", id, func() error {
		_, err := w.tasks.CompleteRunning(ctx, id, outputPath, time.Now())
		return err
	}); err != nil {
		w.reportFinalizeFailure(ctx, id, "任务终态", err)
		return
	}

	// TS 段清理放在终态落库之后：MySQL 是真相源，终态对用户可见后才销毁段现场
	//（防线不变：清理仍严格在时长对账通过之后；终态写库失败时上面已提前 return 保留段）。
	// 损坏段已改名 .corrupt，不匹配 seg_*.ts，自然保留取证
	if w.cfg.Recorder.SegmentCleanup.CleanupEnabled {
		w.cleanupSegments(ctx, id, recordsDir)
	}

	_ = w.queue.RemoveHeartbeat(ctx, id)
	metrics.TasksCompletedTotal.Inc()
	if duration > 0 {
		metrics.RecordDurationSeconds.Observe(duration)
	}
	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageCompleted,
		fmt.Sprintf("完成，产物 %s (时长 %.1fs)", outputPath, duration), nil, w.nodeID)
	log.Printf("任务 %s 完成: %s (时长 %.1fs, 段数 %d/%d)", id, outputPath, duration, len(healthy), len(allSegments))
}

// finalizeDBOp 收尾关键写库（产物记录/终态）的有界退避重试包装：停录洪峰等瞬态 DB
// 错误（如 Error 1040 Too many connections）重试跨过；穷尽仍失败返回错误由调用方
// 保留现场并响亮报出，绝不静默吞掉。
func (w *Worker) finalizeDBOp(ctx context.Context, what, taskID string, op func() error) error {
	var err error
	for attempt := 1; attempt <= finalizeMaxAttempts; attempt++ {
		if err = op(); err == nil {
			return nil
		}
		log.Printf("[error] 任务 %s %s写库失败(第 %d/%d 次): %v", taskID, what, attempt, finalizeMaxAttempts, err)
		if attempt == finalizeMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err() // worker 停止，剩余交自愈/宕机恢复机制
		case <-time.After(finalizeRetryDelay << (attempt - 1)):
		}
	}
	return err
}

// reportFinalizeFailure 收尾写库重试穷尽失败的统一出口：ERROR 级响亮报出（容器
// stdout + 任务日志表双通道），删除 Redis 任务心跳（录制已结束，不删会被 scheduler
// recoverLoop 误判心跳超时走重录路径），保留 TS 段与产物现场，等待 healLoop 自愈补写。
func (w *Worker) reportFinalizeFailure(ctx context.Context, id, what string, err error) {
	log.Printf("[error] 任务 %s %s写库重试 %d 次仍失败: %v（已保留现场，等待自愈补写）",
		id, what, finalizeMaxAttempts, err)
	w.logger.Log(id, store.LogLevelError, store.LogSourceWorker, store.LogStageFailed,
		fmt.Sprintf("%s写库失败（%v），已保留现场，等待自动补写", what, err), nil, w.nodeID)
	_ = w.queue.RemoveHeartbeat(ctx, id)
}

// healLoop 周期扫描本节点名下 running 但执行体已退出的任务，对「录制内容已验收、
// 仅终态写库失败」的做幂等补写（高并发-3 自愈路径）：判定铁证 = 任务日志有
// validated（时长对账通过的持久化记录）+ 磁盘产物存在且可探测。补写产物记录
//（幂等）与终态，收敛主表/文件表/日志表的最终一致；worker 重启后同样接管。
func (w *Worker) healLoop(ctx context.Context) {
	ticker := time.NewTicker(healInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.healFinalizePending(ctx)
		}
	}
}

func (w *Worker) healFinalizePending(ctx context.Context) {
	tasks, err := w.tasks.ListRunningByWorker(ctx, w.nodeID)
	if err != nil {
		log.Printf("[warn] 自愈扫描查询 running 任务失败: %v", err)
		return
	}
	for _, t := range tasks {
		if _, running := w.inflight.Load(t.ID); running {
			continue // 录制/合并/收尾进行中，不属于自愈范畴
		}
		w.healTask(ctx, &t)
	}
}

// healTask 对单个残留 running 任务做自愈判定与补写。任一判定不成立直接返回
//（交宕机恢复机制或人工介入），绝不把未验收的任务补成 completed。
func (w *Worker) healTask(ctx context.Context, t *store.Task) {
	id := t.ID
	outputPath := filepath.Join(w.cfg.Recorder.RecordsDir, id, "output.mp4")
	info, err := os.Stat(outputPath)
	if err != nil || info.Size() == 0 {
		return // 磁盘无产物：不是「收尾写库失败」，走宕机恢复续录
	}
	validated, err := w.logs.HasStage(ctx, id, store.LogStageValidated)
	if err != nil || !validated {
		return // 无时长对账通过的持久化证据（可能是合并中被杀），不补写
	}
	duration, err := w.rec.ProbeDuration(ctx, outputPath)
	if err != nil || duration <= 0 {
		return // 产物已损坏无法探测，不补写；保持 running 由人工介入
	}

	// 补写链与 finishTask 收尾一致（幂等 + 有界重试），任何一步仍失败等下一轮
	if err := w.finalizeDBOp(ctx, "产物文件记录", id, func() error {
		return w.files.CreateOutputIfAbsent(ctx, &store.TaskFile{
			TaskID:    id,
			NodeID:    w.nodeID,
			Filename:  "output.mp4",
			Type:      store.FileTypeOutput,
			Path:      outputPath,
			SizeBytes: info.Size(),
			Status:    "ok",
		})
	}); err != nil {
		log.Printf("[warn] 任务 %s 自愈补写产物记录失败，等待下一轮: %v", id, err)
		return
	}
	if err := w.finalizeDBOp(ctx, "任务终态", id, func() error {
		_, err := w.tasks.CompleteRunning(ctx, id, outputPath, time.Now())
		return err
	}); err != nil {
		log.Printf("[warn] 任务 %s 自愈补写终态失败，等待下一轮: %v", id, err)
		return
	}
	_ = w.queue.RemoveHeartbeat(ctx, id)
	metrics.TasksCompletedTotal.Inc()
	if duration > 0 {
		metrics.RecordDurationSeconds.Observe(duration)
	}
	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageCompleted,
		fmt.Sprintf("自愈补写：终态与产物记录补写一致（此前写库失败，产物 %s 时长 %.1fs）", outputPath, duration), nil, w.nodeID)
	log.Printf("任务 %s 自愈补写完成: %s (时长 %.1fs)", id, outputPath, duration)
}

// failSuffix 重试过的任务失败时附加次数，日志中区分第几次重试后失败。
// 运行中任务不可能被并发重试，认领时读到的 RetryCount 不会过期。
func failSuffix(retryCount int) string {
	if retryCount > 0 {
		return fmt.Sprintf("（第 %d 次重试后失败）", retryCount)
	}
	return ""
}

// failAndRetry 录制阶段失败处理：未过截止时间则置失败并按退避自动重试（等待在
// Redis 定时队列，不占 worker 执行槽，到点由 dispatcher 重新入队）；已过截止
// 时间或无截止时间（存量任务）则终态失败。人工中止/取消在进入本路径前已提前返回。
// 自动重试受 max_retry 约束（用尽后终态失败，不再排期）；人工重试是显式操作，不受限。
func (w *Worker) failAndRetry(ctx context.Context, task *store.Task, code, msg string) {
	now := time.Now()
	if task.EndTime == nil || !now.Before(*task.EndTime) {
		w.logger.Log(task.ID, store.LogLevelError, store.LogSourceWorker, store.LogStageFailed,
			msg+failSuffix(task.RetryCount), nil, w.nodeID)
		metrics.TasksFailedTotal.Inc()
		_ = w.tasks.Fail(ctx, task.ID, store.TaskRecordFailed, code, msg)
		return
	}

	retryCount, ok, err := w.tasks.FailWithRetry(ctx, task.ID, code, msg)
	if err != nil {
		log.Printf("[warn] 任务 %s 标记失败失败: %v", task.ID, err)
		return
	}
	if !ok {
		return // 已被人工中止/取消等并发处置，不再排重试
	}
	// 自动重试上限：状态已由 FailWithRetry 置 record_failed，不再排期即为终态，
	// 用户仍可在详情页显式重试
	if retryCount >= task.MaxRetry {
		w.logger.Log(task.ID, store.LogLevelError, store.LogSourceWorker, store.LogStageFailed,
			fmt.Sprintf("%s，自动重试次数已用尽（%d/%d），终态失败", msg, retryCount, task.MaxRetry), nil, w.nodeID)
		metrics.TasksFailedTotal.Inc()
		log.Printf("任务 %s 自动重试次数用尽（%d/%d），终态失败", task.ID, retryCount, task.MaxRetry)
		return
	}
	backoff := w.cfg.Recorder.RetryBackoffFor(retryCount)
	if err := w.queue.Schedule(ctx, task.ID, now.Add(backoff)); err != nil {
		log.Printf("[warn] 任务 %s 排自动重试失败: %v", task.ID, err)
		return
	}
	w.logger.Log(task.ID, store.LogLevelWarn, store.LogSourceWorker, store.LogStageFailed,
		fmt.Sprintf("%s，%s 后自动重试（第 %d 次）", msg, backoff, retryCount), nil, w.nodeID)
	log.Printf("任务 %s %s，%s 后自动重试（第 %d 次）", task.ID, msg, backoff, retryCount)
}

// cleanupSegments 删除任务的 TS 段文件并软删段记录（合并完成校验通过后调用）。
func (w *Worker) cleanupSegments(ctx context.Context, id string, recordsDir string) {
	files, _ := filepath.Glob(filepath.Join(recordsDir, "seg_*.ts"))
	for _, f := range files {
		_ = os.Remove(f)
	}
	_ = w.files.MarkDeletedByType(ctx, id, store.FileTypeSegment)
}

// cleanupLoop 定期兜底清理：删除超过 retention_days 的遗留 TS 段。
func (w *Worker) cleanupLoop(ctx context.Context) {
	if w.cfg.Recorder.SegmentCleanup.CleanupEnabled {
		return // 已启用立即清理，无需兜底
	}
	interval := time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.cleanupExpiredSegments(ctx)
		}
	}
}

func (w *Worker) cleanupExpiredSegments(ctx context.Context) {
	cutoff := time.Now().Add(-time.Duration(w.cfg.Recorder.SegmentCleanup.RetentionDays) * 24 * time.Hour)
	files, err := w.files.ListExpiredSegments(ctx, w.nodeID, cutoff)
	if err != nil {
		log.Printf("[warn] 查询过期段失败: %v", err)
		return
	}
	for _, f := range files {
		_ = os.Remove(f.Path)
		// 按文件粒度软删：同任务可能同时存在过期段与未过期段（续录场景），
		// 不能按任务整类型软删，否则未过期段的记录会被误删而文件仍留在磁盘
		_ = w.files.MarkDeletedByID(ctx, f.ID)
	}
}
