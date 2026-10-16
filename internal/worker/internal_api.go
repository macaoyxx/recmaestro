// 内部接口：供 scheduler 下发文件操作（手动合并 / 删除 / 下载产物）。
// /internal 路由经 internalAuth 共享密钥鉴权（环境变量 INTERNAL_API_TOKEN，与 scheduler
// 共享）；/healthz、/metrics 由 main.go 注册在同一 engine 上，保持匿名供外部监控探测。
package worker

import (
	"crypto/subtle"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/config"
	"recmaestro/internal/recorder"
	"recmaestro/internal/store"
)

// InternalRouter 返回 worker 的内部接口路由。
// 鉴权只挂 /internal 分组（不挂 engine 级 r.Use）：/healthz、/metrics 也注册在
// 这个 engine 上，须保持匿名，否则外部监控探测会被 401 误判为节点不健康。
func (w *Worker) InternalRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	internal := r.Group("/internal", w.internalAuth())
	internal.POST("/tasks/:id/merge", w.handleInternalMerge)
	internal.POST("/tasks/:id/files/delete", w.handleInternalDelete)
	internal.GET("/tasks/:id/download", w.handleInternalDownload)
	internal.GET("/tasks/:id/ffmpeg-log/:name", w.handleInternalFFmpegLog)
	return r
}

// ffmpegLogNames 是 ffmpeg 原始日志留档的白名单映射（同时防路径穿越）。
var ffmpegLogNames = map[string]string{
	"record": recorder.FFmpegRecordLog,
	"merge":  recorder.FFmpegMergeLog,
}

// handleInternalFFmpegLog 下载 ffmpeg 原始 stderr 留档（record=录制 / merge=合并）。
func (w *Worker) handleInternalFFmpegLog(c *gin.Context) {
	id := c.Param("id")
	filename, ok := ffmpegLogNames[c.Param("name")]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法日志类型（仅支持 record/merge）"})
		return
	}
	// 先查 DB 确认任务存在：id 来自 URL，直接拼路径有穿越风险，经 DB 的任务 ID 均为合法字符串
	task, err := w.tasks.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务内部错误"})
		return
	}
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	path := filepath.Join(w.cfg.Recorder.RecordsDir, id, filename)
	if _, err := os.Stat(path); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "日志文件不存在（任务可能尚未录制或合并）"})
		return
	}
	c.File(path)
}

// internalAuth 校验内部接口共享密钥（常数时间比较防时序侧信道）。
// 未配置 token 时拒绝全部请求：内部接口能合并/删除/下载文件，绝不裸奔（fail closed）。
func (w *Worker) internalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		want := w.cfg.Server.InternalAPIToken
		got := c.GetHeader(config.InternalTokenHeader)
		if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "内部接口鉴权失败"})
			return
		}
		c.Next()
	}
}

// handleInternalMerge 手动合并（segment_ids 为空则全选）。
func (w *Worker) handleInternalMerge(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()
	task, err := w.tasks.GetByID(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务内部错误"})
		return
	}
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	recordsDir := filepath.Join(w.cfg.Recorder.RecordsDir, id)

	var req struct {
		SegmentIDs []int `json:"segment_ids"`
	}
	_ = c.ShouldBindJSON(&req)
	selected := map[int]bool{}
	for _, sid := range req.SegmentIDs {
		selected[sid] = true
	}

	files, err := w.files.ListActiveByTask(ctx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务内部错误"})
		return
	}
	var segments []recorder.Segment
	for _, f := range files {
		if f.Type != store.FileTypeSegment {
			continue
		}
		if len(selected) > 0 && !selected[f.Seq] {
			continue
		}
		segments = append(segments, recorder.Segment{Seq: f.Seq, Path: f.Path, Size: f.SizeBytes})
	}
	if len(segments) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有可合并的段"})
		return
	}

	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageMerging, "手动合并", nil, w.nodeID)
	outputPath := filepath.Join(recordsDir, "output.mp4")
	if err := w.rec.Merge(ctx, segments, recordsDir, outputPath, func(line string) {
		w.logger.Log(id, store.LogLevelWarn, store.LogSourceFFmpegMerge, store.LogStageMerging, line, nil, w.nodeID)
	}); err != nil {
		w.logger.Log(id, store.LogLevelError, store.LogSourceWorker, store.LogStageFailed, err.Error(), nil, w.nodeID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "合并失败: " + err.Error()})
		return
	}

	// 上报产物（幂等）
	if info, statErr := os.Stat(outputPath); statErr == nil {
		_ = w.files.CreateSegmentIfAbsent(ctx, &store.TaskFile{
			TaskID:    id,
			NodeID:    w.nodeID,
			Filename:  "output.mp4",
			Type:      store.FileTypeOutput,
			Path:      outputPath,
			SizeBytes: info.Size(),
			Status:    "ok",
		})
	}

	// 若任务处于 merge_failed，合并成功后置回 completed
	if task.Status == store.TaskMergeFailed {
		_ = w.tasks.Complete(ctx, id, outputPath, time.Now())
	}
	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageMerged, "手动合并完成", nil, w.nodeID)
	c.JSON(http.StatusOK, gin.H{"message": "合并完成", "output": outputPath})
}

// handleInternalDelete 删除指定文件（file_ids）。
func (w *Worker) handleInternalDelete(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		FileIDs []uint64 `json:"file_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误"})
		return
	}

	ctx := c.Request.Context()
	deleted := 0
	for _, fid := range req.FileIDs {
		f, err := w.files.GetByID(ctx, fid)
		if err != nil || f == nil || f.TaskID != id {
			continue
		}
		_ = os.Remove(f.Path)
		_ = w.files.MarkDeletedByID(ctx, fid)
		deleted++
	}
	w.logger.Log(id, store.LogLevelInfo, store.LogSourceWorker, store.LogStageValidated,
		"删除文件 "+strconv.Itoa(deleted)+" 个", nil, w.nodeID)
	c.JSON(http.StatusOK, gin.H{"message": "已删除", "deleted": deleted})
}

// handleInternalDownload 流式下载任务产物（output.mp4）。
func (w *Worker) handleInternalDownload(c *gin.Context) {
	id := c.Param("id")
	task, err := w.tasks.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "服务内部错误"})
		return
	}
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	if task.OutputPath == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "产物不存在"})
		return
	}
	if _, err := os.Stat(task.OutputPath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "产物文件不存在"})
		return
	}
	c.File(task.OutputPath)
}
