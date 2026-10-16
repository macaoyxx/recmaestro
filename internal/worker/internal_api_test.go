// 内部 HTTP 接口测试：手动合并 / 删除文件 / 下载产物（httptest + gin，
// 临时目录造真实 TS 段与产物文件，MySQL 走测试库，Redis 不涉及）。
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"recmaestro/internal/config"
	"recmaestro/internal/store"
)

// testInternalToken 测试用内部接口共享密钥（与 newTestWorker 注入的配置一致）。
const testInternalToken = "unit-test-internal-token"

// makeTSSegment 用 ffmpeg lavfi 生成 1 秒真实 TS 段（合并测试的真实输入）。
func makeTSSegment(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=1:rate=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-f", "mpegts", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成 TS 段 %s 失败: %v (%s)", path, err, out)
	}
}

// createSegRecord 造段文件与对应 DB 记录，返回记录。real=false 时只写记录不落文件。
func createSegRecord(t *testing.T, env *testEnv, taskID, dir string, seq int, real bool) *store.TaskFile {
	t.Helper()
	name := segName(seq)
	path := filepath.Join(dir, name)
	if real {
		makeTSSegment(t, path)
	}
	f := &store.TaskFile{
		TaskID: taskID, NodeID: env.w.nodeID, Filename: name,
		Type: store.FileTypeSegment, Path: path, Seq: seq, Status: "ok",
	}
	if real {
		if info, err := os.Stat(path); err == nil {
			f.SizeBytes = info.Size()
		}
	}
	if err := env.w.files.Create(context.Background(), f); err != nil {
		t.Fatalf("造段记录 seq=%d 失败: %v", seq, err)
	}
	return f
}

// segName 生成与 recorder 一致的段文件名 seg_%05d.ts。
func segName(seq int) string {
	return fmt.Sprintf("seg_%05d.ts", seq)
}

// doJSON 发起带内部接口鉴权头的 JSON 请求并返回响应记录。
func doJSON(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(config.InternalTokenHeader, testInternalToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestInternalAuth 内部接口鉴权：无 token / 错 token 一律 401，正确 token 通过鉴权层。
func TestInternalAuth(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	r := env.w.InternalRouter()

	newReq := func(token string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/internal/tasks/wtest-none/merge", strings.NewReader(`{}`))
		if token != "" {
			req.Header.Set(config.InternalTokenHeader, token)
		}
		return req
	}

	// 无 token 头：401，且不会进到 handler（否则不存在任务返回 404）
	w := httptest.NewRecorder()
	r.ServeHTTP(w, newReq(""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 token 期望 401, 实际 %d（body %s）", w.Code, w.Body.String())
	}
	// 错误 token：401
	w = httptest.NewRecorder()
	r.ServeHTTP(w, newReq("wrong-token"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误 token 期望 401, 实际 %d（body %s）", w.Code, w.Body.String())
	}
	// 正确 token：通过鉴权到达 handler（任务不存在 → 404）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, newReq(testInternalToken))
	if w.Code != http.StatusNotFound {
		t.Fatalf("正确 token 应通过鉴权到达业务 handler（不存在任务 404）, 实际 %d（body %s）", w.Code, w.Body.String())
	}
}

// TestHealthzAnonymous /healthz、/metrics 与内部接口同 engine 但不受共享密钥鉴权拦截
// （外部监控匿名探测 worker 不能被 401 误判为节点不健康），内部接口匿名仍 401。
func TestHealthzAnonymous(t *testing.T) {
	t.Parallel()
	env := newTestWorker(t)
	r := env.w.InternalRouter()
	// 与 cmd/recmaestro/main.go 中 worker 角色的注册方式一致
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	r.GET("/metrics", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{"/healthz", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, path, nil) // 不带 token 头
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("匿名 %s 期望 200，实际 %d（body %s）", path, w.Code, w.Body.String())
		}
	}

	// 鉴权未放松：匿名访问内部接口仍被拒
	req := httptest.NewRequest(http.MethodGet, "/internal/tasks/wtest-none/download", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("匿名内部接口期望 401，实际 %d", w.Code)
	}
}

// TestInternalMerge 手动合并接口。
func TestInternalMerge(t *testing.T) {
	t.Parallel()
	t.Run("任务不存在返回404", func(t *testing.T) {
		env := newTestWorker(t)
		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/wtest-none/merge", `{"segment_ids":[]}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404, body %s", w.Code, w.Body.String())
		}
	})

	t.Run("无可合并段返回400", func(t *testing.T) {
		env := newTestWorker(t)
		newTestTask(t, env, testTaskPrefix+"merge-empty", store.TaskMergeFailed, nil)
		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+testTaskPrefix+"merge-empty/merge", `{"segment_ids":[]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400, body %s", w.Code, w.Body.String())
		}
	})

	t.Run("段文件缺失合并失败返回500", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "merge-broken"
		newTestTask(t, env, taskID, store.TaskMergeFailed, nil)
		dir := filepath.Join(env.w.cfg.Recorder.RecordsDir, taskID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		createSegRecord(t, env, taskID, dir, 1, false) // 记录指向不存在的文件

		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+taskID+"/merge", `{"segment_ids":[]}`)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("code = %d, want 500, body %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "合并失败") {
			t.Errorf("错误响应应包含\"合并失败\"，实际 %s", w.Body.String())
		}
		// 合并失败不改变任务状态
		task, err := env.w.tasks.GetByID(context.Background(), taskID)
		if err != nil || task == nil {
			t.Fatalf("查询任务失败: %v", err)
		}
		if task.Status != store.TaskMergeFailed {
			t.Errorf("合并失败后任务状态 = %s, want merge_failed", task.Status)
		}
	})

	t.Run("全量合并成功并上报产物", func(t *testing.T) {
		env := newTestWorker(t)
		ctx := context.Background()
		taskID := testTaskPrefix + "merge-all"
		newTestTask(t, env, taskID, store.TaskCompleted, nil)
		dir := filepath.Join(env.w.cfg.Recorder.RecordsDir, taskID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		createSegRecord(t, env, taskID, dir, 1, true)
		createSegRecord(t, env, taskID, dir, 2, true)

		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+taskID+"/merge", `{}`)
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200, body %s", w.Code, w.Body.String())
		}
		// 响应中的 output 字段（JSON 转义后路径）应与期望产物路径一致
		var resp struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析合并响应失败: %v (%s)", err, w.Body.String())
		}
		outputPath := filepath.Join(dir, "output.mp4")
		if resp.Output != outputPath {
			t.Errorf("响应产物路径 = %q, want %q", resp.Output, outputPath)
		}

		// 产物存在且时长约等于两段之和（每段 1s）
		info, err := os.Stat(outputPath)
		if err != nil {
			t.Fatalf("产物未生成: %v", err)
		}
		if info.Size() == 0 {
			t.Errorf("产物不应为空文件")
		}
		duration, err := env.w.rec.ProbeDuration(context.Background(), outputPath)
		if err != nil {
			t.Fatalf("探测产物时长失败: %v", err)
		}
		if duration < 1.4 || duration > 2.6 {
			t.Errorf("两段 1s 合并时长应约 2s，实际 %.2fs", duration)
		}

		// DB 上报了 output 记录，size 与磁盘一致；重复合并幂等不产生重复记录
		w2 := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+taskID+"/merge", `{}`)
		if w2.Code != http.StatusOK {
			t.Fatalf("重复合并 code = %d, want 200, body %s", w2.Code, w2.Body.String())
		}
		files, err := env.w.files.ListActiveByTask(ctx, taskID)
		if err != nil {
			t.Fatalf("查询文件记录失败: %v", err)
		}
		var outputs []store.TaskFile
		for _, f := range files {
			if f.Type == store.FileTypeOutput {
				outputs = append(outputs, f)
			}
		}
		if len(outputs) != 1 {
			t.Fatalf("重复合并后 output 记录应幂等为 1 条，实际 %d 条", len(outputs))
		}
		if outputs[0].SizeBytes != info.Size() {
			t.Errorf("产物记录 size = %d, want %d", outputs[0].SizeBytes, info.Size())
		}
	})

	t.Run("按segment_ids选择合并", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "merge-select"
		newTestTask(t, env, taskID, store.TaskCompleted, nil)
		dir := filepath.Join(env.w.cfg.Recorder.RecordsDir, taskID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		createSegRecord(t, env, taskID, dir, 1, true)
		createSegRecord(t, env, taskID, dir, 2, true)
		createSegRecord(t, env, taskID, dir, 3, true)

		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+taskID+"/merge", `{"segment_ids":[2]}`)
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200, body %s", w.Code, w.Body.String())
		}
		outputPath := filepath.Join(dir, "output.mp4")
		duration, err := env.w.rec.ProbeDuration(context.Background(), outputPath)
		if err != nil {
			t.Fatalf("探测产物时长失败: %v", err)
		}
		if duration < 0.4 || duration > 1.6 {
			t.Errorf("只选 1 段时长应约 1s，实际 %.2fs", duration)
		}
		// 未选中的段文件不被合并动作删除
		for _, name := range []string{"seg_00001.ts", "seg_00003.ts"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("未选中的段 %s 不应被删除: %v", name, err)
			}
		}
	})

	t.Run("merge_failed任务合并成功后置回completed", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "merge-recover"
		newTestTask(t, env, taskID, store.TaskMergeFailed, nil)
		dir := filepath.Join(env.w.cfg.Recorder.RecordsDir, taskID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		createSegRecord(t, env, taskID, dir, 1, true)

		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+taskID+"/merge", `{"segment_ids":[]}`)
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200, body %s", w.Code, w.Body.String())
		}
		task, _ := env.w.tasks.GetByID(context.Background(), taskID)
		if task == nil {
			t.Fatalf("任务记录丢失")
		}
		if task.Status != store.TaskCompleted {
			t.Errorf("合并成功后状态 = %s, want completed", task.Status)
		}
		if task.OutputPath != filepath.Join(dir, "output.mp4") {
			t.Errorf("任务 output_path = %q, want %q", task.OutputPath, filepath.Join(dir, "output.mp4"))
		}
	})
}

// TestInternalDelete 删除文件接口。
func TestInternalDelete(t *testing.T) {
	t.Parallel()
	t.Run("删除本任务文件", func(t *testing.T) {
		env := newTestWorker(t)
		ctx := context.Background()
		taskID := testTaskPrefix + "del-1"
		newTestTask(t, env, taskID, store.TaskCompleted, nil)
		dir := t.TempDir()

		ids := make([]uint64, 0, 2)
		for i, name := range []string{"seg_00001.ts", "output.mp4"} {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte("data-"+name), 0o644); err != nil {
				t.Fatalf("造文件失败: %v", err)
			}
			f := &store.TaskFile{TaskID: taskID, NodeID: env.w.nodeID, Filename: name,
				Type: store.FileTypeSegment, Path: path, Seq: i + 1, Status: "ok"}
			if err := env.w.files.Create(ctx, f); err != nil {
				t.Fatalf("造文件记录失败: %v", err)
			}
			ids = append(ids, f.ID)
		}

		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+taskID+"/files/delete",
			fmt.Sprintf(`{"file_ids":[%d,%d]}`, ids[0], ids[1]))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200, body %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"deleted":2`) {
			t.Errorf("应返回 deleted=2，实际 %s", w.Body.String())
		}
		for _, name := range []string{"seg_00001.ts", "output.mp4"} {
			if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Errorf("文件 %s 应被删除", name)
			}
		}
		for _, fid := range ids {
			f, err := env.w.files.GetByID(ctx, fid)
			if err != nil || f == nil {
				t.Fatalf("查询记录 %d 失败: %v", fid, err)
			}
			if f.DeletedAt == nil {
				t.Errorf("记录 %d 应被软删", fid)
			}
		}
	})

	t.Run("不删其他任务的文件", func(t *testing.T) {
		env := newTestWorker(t)
		ctx := context.Background()
		mine := testTaskPrefix + "del-2a"
		other := testTaskPrefix + "del-2b"
		newTestTask(t, env, mine, store.TaskCompleted, nil)
		newTestTask(t, env, other, store.TaskCompleted, nil)
		dir := t.TempDir()

		otherPath := filepath.Join(dir, "other.ts")
		if err := os.WriteFile(otherPath, []byte("other"), 0o644); err != nil {
			t.Fatalf("造文件失败: %v", err)
		}
		otherFile := &store.TaskFile{TaskID: other, NodeID: env.w.nodeID, Filename: "other.ts",
			Type: store.FileTypeSegment, Path: otherPath, Seq: 1, Status: "ok"}
		if err := env.w.files.Create(ctx, otherFile); err != nil {
			t.Fatalf("造记录失败: %v", err)
		}

		// 用任务 A 的接口删任务 B 的文件：不计数、不删除
		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+mine+"/files/delete",
			fmt.Sprintf(`{"file_ids":[%d]}`, otherFile.ID))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200, body %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"deleted":0`) {
			t.Errorf("他人任务文件不应计入删除，实际 %s", w.Body.String())
		}
		if _, err := os.Stat(otherPath); err != nil {
			t.Errorf("他人任务文件不应被删除: %v", err)
		}
		f, err := env.w.files.GetByID(ctx, otherFile.ID)
		if err != nil || f == nil {
			t.Fatalf("查询他人文件记录失败: %v", err)
		}
		if f.DeletedAt != nil {
			t.Errorf("他人任务记录不应被软删")
		}
	})

	t.Run("磁盘文件缺失仍软删记录", func(t *testing.T) {
		env := newTestWorker(t)
		ctx := context.Background()
		taskID := testTaskPrefix + "del-3"
		newTestTask(t, env, taskID, store.TaskCompleted, nil)
		// 记录指向不存在的路径
		f := &store.TaskFile{TaskID: taskID, NodeID: env.w.nodeID, Filename: "gone.ts",
			Type: store.FileTypeSegment, Path: filepath.Join(t.TempDir(), "gone.ts"), Seq: 1, Status: "ok"}
		if err := env.w.files.Create(ctx, f); err != nil {
			t.Fatalf("造记录失败: %v", err)
		}

		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+taskID+"/files/delete", fmt.Sprintf(`{"file_ids":[%d]}`, f.ID))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200, body %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"deleted":1`) {
			t.Errorf("文件缺失时仍应计数软删 1 条，实际 %s", w.Body.String())
		}
	})

	t.Run("请求参数错误返回400", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "del-4"
		newTestTask(t, env, taskID, store.TaskCompleted, nil)
		w := doJSON(t, env.w.InternalRouter(), http.MethodPost,
			"/internal/tasks/"+taskID+"/files/delete", `not-json`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400, body %s", w.Code, w.Body.String())
		}
	})
}

// TestInternalDownload 产物下载接口。
func TestInternalDownload(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*testEnv, string, string) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "dl"
		content := "fake-mp4-content"
		dir := t.TempDir()
		path := filepath.Join(dir, "output.mp4")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("造产物文件失败: %v", err)
		}
		newTestTask(t, env, taskID, store.TaskCompleted, func(task *store.Task) {
			task.OutputPath = path
		})
		return env, taskID, content
	}

	t.Run("正常下载", func(t *testing.T) {
		env, taskID, content := setup(t)
		req := httptest.NewRequest(http.MethodGet, "/internal/tasks/"+taskID+"/download", nil)
		req.Header.Set(config.InternalTokenHeader, testInternalToken)
		w := httptest.NewRecorder()
		env.w.InternalRouter().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200, body %s", w.Code, w.Body.String())
		}
		if w.Body.String() != content {
			t.Errorf("下载内容 = %q, want %q", w.Body.String(), content)
		}
	})

	t.Run("任务不存在返回404", func(t *testing.T) {
		env, _, _ := setup(t)
		req := httptest.NewRequest(http.MethodGet, "/internal/tasks/wtest-nope/download", nil)
		req.Header.Set(config.InternalTokenHeader, testInternalToken)
		w := httptest.NewRecorder()
		env.w.InternalRouter().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404, body %s", w.Code, w.Body.String())
		}
	})

	t.Run("无产物路径返回404", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "dl-noout"
		newTestTask(t, env, taskID, store.TaskRecordFailed, nil) // OutputPath 为空
		req := httptest.NewRequest(http.MethodGet, "/internal/tasks/"+taskID+"/download", nil)
		req.Header.Set(config.InternalTokenHeader, testInternalToken)
		w := httptest.NewRecorder()
		env.w.InternalRouter().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404, body %s", w.Code, w.Body.String())
		}
	})

	t.Run("产物文件不存在返回404", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "dl-gone"
		newTestTask(t, env, taskID, store.TaskCompleted, func(task *store.Task) {
			task.OutputPath = filepath.Join(t.TempDir(), "missing.mp4")
		})
		req := httptest.NewRequest(http.MethodGet, "/internal/tasks/"+taskID+"/download", nil)
		req.Header.Set(config.InternalTokenHeader, testInternalToken)
		w := httptest.NewRecorder()
		env.w.InternalRouter().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404, body %s", w.Code, w.Body.String())
		}
	})
}

// TestInternalFFmpegLog ffmpeg 原始日志留档下载：非法类型 400、任务不存在 404、
// 文件不存在 404、正常下载内容一致。
func TestInternalFFmpegLog(t *testing.T) {
	t.Parallel()

	doGet := func(r http.Handler, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(config.InternalTokenHeader, testInternalToken)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("非法日志类型返回400", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "fflog-bad"
		newTestTask(t, env, taskID, store.TaskCompleted, nil)
		w := doGet(env.w.InternalRouter(), "/internal/tasks/"+taskID+"/ffmpeg-log/bogus")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400, body %s", w.Code, w.Body.String())
		}
	})

	t.Run("任务不存在返回404", func(t *testing.T) {
		env := newTestWorker(t)
		w := doGet(env.w.InternalRouter(), "/internal/tasks/wtest-none/ffmpeg-log/record")
		if w.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404, body %s", w.Code, w.Body.String())
		}
	})

	t.Run("日志文件不存在返回404", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "fflog-miss"
		newTestTask(t, env, taskID, store.TaskCompleted, nil) // 任务存在但无留档文件
		w := doGet(env.w.InternalRouter(), "/internal/tasks/"+taskID+"/ffmpeg-log/record")
		if w.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404, body %s", w.Code, w.Body.String())
		}
	})

	t.Run("正常下载", func(t *testing.T) {
		env := newTestWorker(t)
		taskID := testTaskPrefix + "fflog-ok"
		newTestTask(t, env, taskID, store.TaskCompleted, nil)
		dir := filepath.Join(env.w.cfg.Recorder.RecordsDir, taskID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		content := "fake ffmpeg stderr archive"
		if err := os.WriteFile(filepath.Join(dir, "ffmpeg-record.log"), []byte(content), 0o644); err != nil {
			t.Fatalf("造留档失败: %v", err)
		}

		w := doGet(env.w.InternalRouter(), "/internal/tasks/"+taskID+"/ffmpeg-log/record")
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200, body %s", w.Code, w.Body.String())
		}
		if w.Body.String() != content {
			t.Errorf("下载内容 = %q, want %q", w.Body.String(), content)
		}
	})
}
