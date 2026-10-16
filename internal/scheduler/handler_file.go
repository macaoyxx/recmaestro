package scheduler

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/api"
	"recmaestro/internal/config"
	"recmaestro/internal/store"
)

// handleListFiles 列出任务的文件（含节点可达性）。
// handleListFiles 任务文件列表。
// @Summary      任务文件列表
// @Tags         文件
// @Produce      json
// @Param        id   path      int  true  "任务 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks/{id}/files [get]
func (s *Server) handleListFiles(c *gin.Context) {
	id := c.Param("id")
	files, err := s.files.ListActiveByTask(c.Request.Context(), id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return
	}
	out := make([]gin.H, 0, len(files))
	for _, f := range files {
		reachable := true
		if f.NodeID != "" {
			alive, _ := s.queue.WorkerAlive(c.Request.Context(), f.NodeID)
			reachable = alive
		}
		out = append(out, gin.H{
			"id":         f.ID,
			"task_id":    f.TaskID,
			"node_id":    f.NodeID,
			"filename":   f.Filename,
			"type":       f.Type,
			"path":       f.Path,
			"seq":        f.Seq,
			"size_bytes": f.SizeBytes,
			"status":     f.Status,
			"reachable":  reachable,
		})
	}
	api.OK(c, out)
}

// handleMerge 手动合并：把请求转发到持有文件的 worker 执行。
// handleMerge 手动合并。
// @Summary      手动合并所选段
// @Tags         文件
// @Accept       json
// @Produce      json
// @Param        id    path  int     true "任务 ID"
// @Param        body  body  object  false "{\"segment_ids\":[1,2]}"
// @Success      200   {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks/{id}/merge [post]
func (s *Server) handleMerge(c *gin.Context) {
	id := c.Param("id")
	task, ok := s.taskNode(c, id)
	if !ok {
		return
	}
	if err := s.forwardToWorker(c, task.WorkerID, "POST", "/internal/tasks/"+id+"/merge"); err != nil {
		api.Fail(c, http.StatusBadGateway, "下发合并失败: "+err.Error())
		return
	}
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: store.AuditFileMerge, Resource: store.AuditResourceTask, ResourceID: id, ResourceName: task.Name, Detail: "手动合并 TS 段"})
	api.OKMsg(c, gin.H{"id": id}, "合并已下发")
}

// handleDeleteFiles 手动删除：把请求转发到持有文件的 worker 执行。
// handleDeleteFiles 删除文件。
// @Summary      删除所选文件
// @Tags         文件
// @Accept       json
// @Produce      json
// @Param        id    path  int     true "任务 ID"
// @Param        body  body  object  false "{\"file_ids\":[1,2]}"
// @Success      200   {object}  map[string]interface{}
// @Security     BearerAuth
// @Router       /tasks/{id}/files/delete [post]
func (s *Server) handleDeleteFiles(c *gin.Context) {
	id := c.Param("id")
	task, ok := s.taskNode(c, id)
	if !ok {
		return
	}
	if err := s.forwardToWorker(c, task.WorkerID, "POST", "/internal/tasks/"+id+"/files/delete"); err != nil {
		api.Fail(c, http.StatusBadGateway, "下发删除失败: "+err.Error())
		return
	}
	s.audit(c, auditEvent{OperatorID: auditOperator(c), OperatorName: auditOperatorName(c), Action: store.AuditFileDelete, Resource: store.AuditResourceTask, ResourceID: id, ResourceName: task.Name, Detail: "删除任务文件"})
	api.OKMsg(c, gin.H{"id": id}, "删除已下发")
}

// handleDownloadOutput 下载产物：反向代理到持有产物的 worker。
// handleDownloadOutput 下载产物。
// @Summary      下载录制产物
// @Tags         文件
// @Produce      application/octet-stream
// @Param        id   path  int  true  "任务 ID"
// @Success      200  {file}  file
// @Security     BearerAuth
// @Router       /tasks/{id}/download [get]
func (s *Server) handleDownloadOutput(c *gin.Context) {
	id := c.Param("id")
	task, ok := s.taskNode(c, id)
	if !ok {
		return
	}
	addr, ok := s.workerAddrForTask(c, id, task.WorkerID)
	if !ok {
		return
	}
	s.proxyToWorkerInternal(c, addr, "/internal/tasks/"+id+"/download")
}

// handleDownloadFFmpegLog 下载 ffmpeg 原始 stderr 留档。
// handleDownloadFFmpegLog 下载 ffmpeg 原始日志。
// @Summary      下载 ffmpeg 原始日志
// @Description  name 取 record（录制）/ merge（合并），文件由 worker 在录制/合并结束时留档
// @Tags         日志
// @Produce      application/octet-stream
// @Param        id    path   int     true  "任务 ID"
// @Param        name  path   string  true  "日志类型: record | merge"
// @Success      200   {file}  file
// @Security     BearerAuth
// @Router       /tasks/{id}/ffmpeg-log/{name} [get]
func (s *Server) handleDownloadFFmpegLog(c *gin.Context) {
	id := c.Param("id")
	name := c.Param("name")
	if name != "record" && name != "merge" {
		api.Fail(c, http.StatusBadRequest, "非法日志类型（仅支持 record/merge）")
		return
	}
	task, ok := s.taskNode(c, id)
	if !ok {
		return
	}
	addr, ok := s.workerAddrForTask(c, id, task.WorkerID)
	if !ok {
		return
	}
	s.proxyToWorkerInternal(c, addr, "/internal/tasks/"+id+"/ffmpeg-log/"+name)
}

// workerAddrForTask 解析任务绑定节点的内部接口地址；绑定节点已消亡（如容器重建
// 导致 node_id 漂移）时兜底任一在线 worker——共享存储部署下文件都在。
// 失败时写响应并返回 false。
func (s *Server) workerAddrForTask(c *gin.Context, id, nodeID string) (string, bool) {
	addr, err := s.queue.GetWorkerAddr(c.Request.Context(), nodeID)
	if err != nil || addr == "" {
		addrs, _ := s.queue.ListWorkerAddrs(c.Request.Context())
		if len(addrs) == 0 {
			api.Fail(c, http.StatusBadGateway, "worker 地址不可用")
			return "", false
		}
		addr = addrs[0]
		log.Printf("[warn] 任务 %s 绑定节点 %s 不可达，兜底路由至 %s", id, nodeID, addr)
	}
	return addr, true
}

// proxyToWorkerInternal 把当前请求反向代理到 worker 内部接口的指定路径（携带共享密钥头）。
func (s *Server) proxyToWorkerInternal(c *gin.Context, addr, internalPath string) {
	target, err := url.Parse("http://" + addr)
	if err != nil {
		api.Fail(c, http.StatusBadGateway, "worker 地址非法: "+addr)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalPath := c.Request.URL.Path
	c.Request.URL.Path = internalPath
	// 内部接口已启用共享密钥鉴权，反代请求同样携带 token 头
	c.Request.Header.Set(config.InternalTokenHeader, s.cfg.Server.InternalAPIToken)
	proxy.ServeHTTP(c.Writer, c.Request)
	c.Request.URL.Path = originalPath
}

// taskNode 返回任务（调用方取 WorkerID 路由、Name 做审计快照），失败时写响应并返回 false。
func (s *Server) taskNode(c *gin.Context, id string) (*store.Task, bool) {
	task, err := s.tasks.GetByID(c.Request.Context(), id)
	if err != nil {
		api.Fail(c, http.StatusInternalServerError, "服务内部错误")
		return nil, false
	}
	if task == nil {
		api.Fail(c, http.StatusNotFound, "任务不存在")
		return nil, false
	}
	if task.WorkerID == "" {
		api.Fail(c, http.StatusConflict, "任务未绑定 worker")
		return nil, false
	}
	return task, true
}

// workerHTTPClient 转发 worker 内部接口的专用客户端：超时须覆盖 worker 端合并上限
// （mergeTimeout=10min）加余量；不用无超时的 DefaultClient，避免 worker 端卡住时长期挂起。
var workerHTTPClient = &http.Client{Timeout: 15 * time.Minute}

// forwardToWorker 把请求体转发到 worker 的内部接口。
func (s *Server) forwardToWorker(c *gin.Context, nodeID, method, path string) error {
	addr, err := s.queue.GetWorkerAddr(c.Request.Context(), nodeID)
	if err != nil || addr == "" {
		return fmt.Errorf("worker 地址不可用")
	}
	body, _ := io.ReadAll(c.Request.Body)
	req, err := http.NewRequestWithContext(c.Request.Context(), method, "http://"+addr+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(config.InternalTokenHeader, s.cfg.Server.InternalAPIToken)
	resp, err := workerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("worker 返回 %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
