package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 测试替身：fake ffmpeg ----
// 用 go build 把 testdata/fakeffmpeg 编译成可执行文件，Record 会把它当 ffmpeg 启动；
// 它把收到的命令行参数写档后挂起，等停止文件出现再退出。整个测试进程只编译一次。

var (
	fakeOnce    sync.Once
	fakeDir     string
	fakeFFExe   string
	fakeBuildEr error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fakeDir != "" {
		_ = os.RemoveAll(fakeDir) // 清理编译产物
	}
	os.Exit(code)
}

// fakeFFmpeg 返回测试替身可执行文件路径；编译失败时让当前用例 Fatal。
func fakeFFmpeg(t *testing.T) string {
	t.Helper()
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "recorder-fake-ffmpeg-*")
		if err != nil {
			fakeBuildEr = fmt.Errorf("创建临时目录失败: %w", err)
			return
		}
		exe := filepath.Join(dir, "fakeffmpeg")
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		// 显式相对路径可以构建 testdata 下的包（./... 通配才会忽略 testdata）
		if out, err := exec.Command("go", "build", "-o", exe, "./testdata/fakeffmpeg").CombinedOutput(); err != nil {
			fakeBuildEr = fmt.Errorf("编译测试替身失败: %w (输出: %s)", err, out)
			return
		}
		fakeDir, fakeFFExe = dir, exe
	})
	if fakeBuildEr != nil {
		t.Fatalf("准备 fake ffmpeg 失败: %v", fakeBuildEr)
	}
	return fakeFFExe
}

// waitFor 轮询等待条件成立，超时 Fatal。
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时（%s）", what, timeout)
}

// runFakeRecord 用测试替身跑一次 Record：等 fake 把 ffmpeg 参数写档后执行 after
// （由用例决定如何结束录制；after 为 nil 时直接写停止文件让 fake 退出）。
// 返回捕获的 ffmpeg 参数、OnLog 收到的 stderr 行、Record 的段结果与错误。
func runFakeRecord(t *testing.T, rec *Recorder, opts RecordOptions, after func(cancel context.CancelFunc, recordsDir string)) ([]string, []string, []Segment, error) {
	t.Helper()
	capDir := t.TempDir()
	argsFile := filepath.Join(capDir, "args.json")
	stopFile := filepath.Join(capDir, "stop")
	t.Setenv("FAKE_ARGS_FILE", argsFile)
	t.Setenv("FAKE_STOP_FILE", stopFile)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var mu sync.Mutex
	var logs []string
	userOnLog := opts.OnLog
	opts.OnLog = func(line string) {
		mu.Lock()
		logs = append(logs, line)
		mu.Unlock()
		if userOnLog != nil {
			userOnLog(line)
		}
	}

	type recordResult struct {
		segs []Segment
		err  error
	}
	resCh := make(chan recordResult, 1)
	go func() {
		segs, err := rec.Record(ctx, opts)
		resCh <- recordResult{segs: segs, err: err}
	}()

	waitFor(t, 15*time.Second, "fake ffmpeg 参数写档", func() bool {
		_, err := os.Stat(argsFile)
		return err == nil
	})
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("读取 fake 参数文件失败: %v", err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatalf("解析 fake 参数失败: %v (内容: %s)", err, data)
	}

	if after != nil {
		after(cancel, opts.RecordsDir)
	}
	// after 返回后统一写停止文件，确保 fake 退出、Record 能结束
	if err := os.WriteFile(stopFile, nil, 0o644); err != nil {
		t.Fatalf("写停止文件失败: %v", err)
	}

	res := <-resCh
	mu.Lock()
	defer mu.Unlock()
	return args, logs, res.segs, res.err
}

// argIndex 返回参数键的下标，不存在返回 -1。
func argIndex(args []string, key string) int {
	for i, a := range args {
		if a == key {
			return i
		}
	}
	return -1
}

// argValue 断言参数键存在并返回其值。
func argValue(t *testing.T, args []string, key string) string {
	t.Helper()
	i := argIndex(args, key)
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("ffmpeg 参数缺少 %s 或其取值: %v", key, args)
	}
	return args[i+1]
}

// requireBin 环境缺少指定二进制时跳过依赖它的用例（真实录制链路由部署实测覆盖）。
func requireBin(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("环境缺少 %s，跳过依赖真实 ffmpeg 的用例: %v", name, err)
	}
}

// genTestVideo 用 lavfi 源生成测试视频（H.264），按扩展名决定容器格式。
func genTestVideo(t *testing.T, path string, durationSecs int) {
	t.Helper()
	src := fmt.Sprintf("testsrc=duration=%d:size=160x120:rate=10", durationSecs)
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", src,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		path).CombinedOutput()
	if err != nil {
		t.Fatalf("生成测试视频 %s 失败: %v (输出: %s)", path, err, out)
	}
}

// ---- New ----

func TestNewFFprobePath(t *testing.T) {
	cases := []struct {
		name        string
		ffmpegPath  string
		wantFfmpeg  string
		wantFfprobe string
	}{
		{"空路径回退 PATH", "", "ffmpeg", "ffprobe"},
		{"unix 绝对路径", "/usr/local/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/usr/local/bin/ffprobe"},
		{"windows 路径", `C:\ffmpeg\bin\ffmpeg.exe`, `C:\ffmpeg\bin\ffmpeg.exe`, `C:\ffmpeg\bin\ffprobe.exe`},
		{"windows 正斜杠路径", "C:/ffmpeg/bin/ffmpeg.exe", "C:/ffmpeg/bin/ffmpeg.exe", "C:/ffmpeg/bin/ffprobe.exe"},
		{"裸文件名", "ffmpeg.exe", "ffmpeg.exe", "ffprobe.exe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := New(c.ffmpegPath, "", 0)
			if r.ffmpegPath != c.wantFfmpeg {
				t.Errorf("ffmpegPath = %q, want %q", r.ffmpegPath, c.wantFfmpeg)
			}
			if r.ffprobePath != c.wantFfprobe {
				t.Errorf("ffprobePath = %q, want %q", r.ffprobePath, c.wantFfprobe)
			}
		})
	}
}

// ---- 协议分支 ----

func TestUseHTTPReconnect(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://live.example.com/live/stream.flv", true},
		{"https://live.example.com/live/index.m3u8", true},
		{"rtmp://live.example.com/live/stream", false},
		{"rtmps://live.example.com/live/stream", false},
		{"rtmpt://live.example.com/live/stream", false}, // rtmp 走 http 隧道，但仍非 http 前缀
		{"srt://192.168.1.10:9000?mode=caller", false},
		{"udp://@239.1.1.1:5000", false},
		{"HTTP://live.example.com/live.flv", false}, // 前缀匹配大小写敏感，如实记录现有行为
		{"httpx://live.example.com/live.flv", false},
		{"", false},
	}
	for _, c := range cases {
		if got := useHTTPReconnect(c.url); got != c.want {
			t.Errorf("useHTTPReconnect(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

// ---- Record 命令构造（协议分支 + 分段参数 + 录制目录创建）----

func TestRecordArgs(t *testing.T) {
	rec := New(fakeFFmpeg(t), "", 0)
	cases := []struct {
		name          string
		url           string
		segSecs       int
		startSeq      int
		wantReconnect bool
		wantSegTime   string
	}{
		{"http 源带 reconnect 系列参数", "http://live.example.com/live/stream.flv", 0, 0, true, "300"},
		{"https 源带 reconnect 系列参数", "https://live.example.com/live/index.m3u8", 30, 0, true, "30"},
		{"rtmp 源不带 reconnect 参数", "rtmp://live.example.com/live/stream", 0, 0, false, "300"},
		{"rtmps 源不带 reconnect 参数", "rtmps://live.example.com/live/stream", 0, 0, false, "300"},
		{"srt 源不带 reconnect 参数", "srt://192.168.1.10:9000?mode=caller", 0, 7, false, "300"},
		{"分段时长为负走默认 300", "rtmp://live.example.com/live", -5, 0, false, "300"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recordsDir := filepath.Join(t.TempDir(), "rec") // 预置不存在的嵌套目录，验证自动创建
			opts := RecordOptions{StreamURL: c.url, RecordsDir: recordsDir, SegmentSecs: c.segSecs, StartSeq: c.startSeq}
			args, logs, segs, err := runFakeRecord(t, rec, opts, nil)

			if _, statErr := os.Stat(recordsDir); statErr != nil {
				t.Errorf("Record 未自动创建录制目录: %v", statErr)
			}

			// 通用录制参数
			if got := argValue(t, args, "-rw_timeout"); got != "10000000" {
				t.Errorf("-rw_timeout = %q, want 10000000", got)
			}
			if argIndex(args, "-rw_timeout") > argIndex(args, "-i") {
				t.Errorf("-rw_timeout 必须在 -i 之前（输入选项）: %v", args)
			}
			if got := argValue(t, args, "-i"); got != c.url {
				t.Errorf("-i = %q, want %q", got, c.url)
			}
			if got := argValue(t, args, "-c"); got != "copy" {
				t.Errorf("-c = %q, want copy（无损）", got)
			}
			if got := argValue(t, args, "-f"); got != "segment" {
				t.Errorf("-f = %q, want segment", got)
			}
			if got := argValue(t, args, "-segment_time"); got != c.wantSegTime {
				t.Errorf("-segment_time = %q, want %q", got, c.wantSegTime)
			}
			if got := argValue(t, args, "-segment_format"); got != "mpegts" {
				t.Errorf("-segment_format = %q, want mpegts", got)
			}
			if got := argValue(t, args, "-reset_timestamps"); got != "1" {
				t.Errorf("-reset_timestamps = %q, want 1", got)
			}
			if got := argValue(t, args, "-segment_start_number"); got != strconv.Itoa(c.startSeq) {
				t.Errorf("-segment_start_number = %q, want %d", got, c.startSeq)
			}
			if last := args[len(args)-1]; last != filepath.Join(recordsDir, "seg_%05d.ts") {
				t.Errorf("输出段模板 = %q, want %q", last, filepath.Join(recordsDir, "seg_%05d.ts"))
			}

			// reconnect 系列是 HTTP 专属输入选项，且必须在 -i 之前
			reconnectKeys := []string{"-reconnect", "-reconnect_streamed", "-reconnect_at_eof", "-reconnect_delay_max"}
			for _, key := range reconnectKeys {
				idx := argIndex(args, key)
				if c.wantReconnect {
					if idx < 0 {
						t.Errorf("HTTP 源缺少 %s: %v", key, args)
					} else if idx > argIndex(args, "-i") {
						t.Errorf("%s 必须在 -i 之前: %v", key, args)
					}
				} else if idx >= 0 {
					t.Errorf("非 HTTP 源不应带 %s: %v", key, args)
				}
			}
			if c.wantReconnect {
				if got := argValue(t, args, "-reconnect"); got != "1" {
					t.Errorf("-reconnect = %q, want 1", got)
				}
				if got := argValue(t, args, "-reconnect_delay_max"); got != "10" {
					t.Errorf("-reconnect_delay_max = %q, want 10", got)
				}
			}

			// fake 正常退出（exit 0）也被视为"流结束"错误，由 worker 层续录兜底
			if err == nil || !strings.Contains(err.Error(), "ffmpeg 退出") {
				t.Errorf("fake 正常退出后期望返回 ffmpeg 退出错误, got %v", err)
			}
			if len(segs) != 0 {
				t.Errorf("无段产出, 期望空段列表, got %v", segs)
			}
			// stderr 逐行采集接线
			found := false
			for _, l := range logs {
				if strings.Contains(l, "fake-ffmpeg started") {
					found = true
				}
			}
			if !found {
				t.Errorf("OnLog 未收到 fake ffmpeg 的 stderr 行, logs = %v", logs)
			}

			// ffmpeg stderr 全量留档：Record 结束后录制目录应有 ffmpeg-record.log
			if _, statErr := os.Stat(filepath.Join(recordsDir, FFmpegRecordLog)); statErr != nil {
				t.Errorf("录制结束应在录制目录留档 %s: %v", FFmpegRecordLog, statErr)
			}
		})
	}
}

// TestRecordArgsConfigured 注入的日志级别与重连间隔须反映到 ffmpeg 参数
// （对应 config 的 ffmpeg_loglevel / reconnect.delay_max 接线）。
func TestRecordArgsConfigured(t *testing.T) {
	rec := New(fakeFFmpeg(t), "info", 5)
	recordsDir := t.TempDir()
	opts := RecordOptions{StreamURL: "http://live.example.com/live/stream.flv", RecordsDir: recordsDir, SegmentSecs: 30, StartSeq: 1}
	args, _, _, err := runFakeRecord(t, rec, opts, nil)
	if err == nil {
		t.Fatal("fake ffmpeg 正常退出应返回错误（Record 以流结束错误返回）")
	}
	if got := argValue(t, args, "-loglevel"); got != "info" {
		t.Errorf("-loglevel = %q, want info（注入配置生效）", got)
	}
	if got := argValue(t, args, "-reconnect_delay_max"); got != "5" {
		t.Errorf("-reconnect_delay_max = %q, want 5（注入配置生效）", got)
	}
}

// ---- Record 结束条件 ----

func TestRecordCtxCancel(t *testing.T) {
	rec := New(fakeFFmpeg(t), "", 0)
	opts := RecordOptions{StreamURL: "rtmp://live.example.com/live", RecordsDir: t.TempDir()}
	_, _, _, err := runFakeRecord(t, rec, opts, func(cancel context.CancelFunc, _ string) {
		cancel() // 先取消，再让 fake 退出，确保走 ctx 结束分支
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ctx 取消后 Record 错误 = %v, want context.Canceled", err)
	}
}

func TestRecordEndTime(t *testing.T) {
	rec := New(fakeFFmpeg(t), "", 0)
	end := time.Now().Add(300 * time.Millisecond)
	opts := RecordOptions{StreamURL: "rtmp://live.example.com/live", RecordsDir: t.TempDir(), EndTime: &end}
	start := time.Now()
	_, _, _, err := runFakeRecord(t, rec, opts, func(_ context.CancelFunc, _ string) {
		// 到点触发正常结束分支后再停 fake（Windows 下 SIGTERM 不生效，靠停止文件让 Wait 尽快返回）
		time.Sleep(600 * time.Millisecond)
	})
	if err != nil {
		t.Fatalf("到点结束应为正常返回, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("到点结束耗时 %s, 不应早于 EndTime", elapsed)
	}
}

func TestRecordCallbacks(t *testing.T) {
	rec := New(fakeFFmpeg(t), "", 0)
	dir := t.TempDir()
	var mu sync.Mutex
	var gotSegs []Segment
	heartbeats := 0
	opts := RecordOptions{
		StreamURL:   "rtmp://live.example.com/live",
		RecordsDir:  dir,
		SegmentSecs: 60,
		OnSegment: func(s Segment) {
			mu.Lock()
			gotSegs = append(gotSegs, s)
			mu.Unlock()
		},
		OnHeartbeat: func() {
			mu.Lock()
			heartbeats++
			mu.Unlock()
		},
	}
	_, _, resultSegs, err := runFakeRecord(t, rec, opts, func(_ context.CancelFunc, recordsDir string) {
		seg0 := filepath.Join(recordsDir, "seg_00000.ts")
		if writeErr := os.WriteFile(seg0, make([]byte, 10), 0o644); writeErr != nil {
			t.Errorf("写入首个段失败: %v", writeErr)
		}
		// 扫描周期 2 秒，等首个段回调（同一 tick 内先扫描回调、再心跳回调）
		waitFor(t, 8*time.Second, "OnSegment 首段回调", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(gotSegs) > 0
		})
		mu.Lock()
		hb := heartbeats
		mu.Unlock()
		if hb == 0 {
			t.Errorf("扫描 tick 触发后期望 OnHeartbeat 至少被调用一次")
		}
		// 段正在写、size 增长：等下一轮 tick 让 seen 里的 size 更新
		if writeErr := os.WriteFile(seg0, make([]byte, 20), 0o644); writeErr != nil {
			t.Errorf("增长段大小失败: %v", writeErr)
		}
		time.Sleep(2500 * time.Millisecond)
	})
	_ = err // fake 退出导致的"ffmpeg 退出"错误与本用例无关

	mu.Lock()
	defer mu.Unlock()
	if len(gotSegs) != 1 || gotSegs[0].Seq != 0 {
		t.Errorf("OnSegment 回调 = %v, 期望仅首个段回调一次（size 变化不重复回调）", gotSegs)
	}
	if len(resultSegs) != 1 || resultSegs[0].Size != 20 {
		t.Errorf("Record 返回段 = %v, 期望 size 更新为 20", resultSegs)
	}
}

// ---- 段扫描 ----

func TestScanSegments(t *testing.T) {
	r := &Recorder{}
	dir := t.TempDir()
	write := func(name string, size int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
	}
	write("seg_00002.ts", 200)
	write("seg_00000.ts", 10)
	write("seg_abc.ts", 5)     // 序号非数字，应跳过
	write("seg_00003.txt", 5)  // 扩展名不符，glob 不命中
	write("other_00004.ts", 5) // 前缀不符，glob 不命中

	seen := map[int]Segment{}
	var called []Segment
	r.scanSegments(dir, seen, func(s Segment) { called = append(called, s) })

	if len(called) != 2 {
		t.Fatalf("新段回调次数 = %d, want 2 (回调: %v)", len(called), called)
	}
	if called[0].Seq != 0 || called[1].Seq != 2 { // glob 返回按文件名排序
		t.Errorf("回调顺序 = %v, 期望按 seq 0,2", called)
	}
	if seen[0].Size != 10 || seen[2].Size != 200 {
		t.Errorf("seen 记录 size 有误: %+v", seen)
	}

	// 无变化的重复扫描：不新增回调、不改动 seen
	r.scanSegments(dir, seen, func(s Segment) { called = append(called, s) })
	if len(called) != 2 {
		t.Errorf("重复扫描不应再次回调, called = %v", called)
	}

	// 段增长：seen 更新为新 size，但不重复回调
	write("seg_00000.ts", 20)
	r.scanSegments(dir, seen, func(s Segment) { called = append(called, s) })
	if seen[0].Size != 20 {
		t.Errorf("段增长后 seen[0].Size = %d, want 20", seen[0].Size)
	}
	if len(called) != 2 {
		t.Errorf("段增长不应触发新回调, called = %v", called)
	}
}

func TestParseSeq(t *testing.T) {
	cases := []struct {
		path string
		want int
	}{
		{filepath.Join("dir", "seg_00000.ts"), 0},
		{filepath.Join("dir", "seg_00123.ts"), 123},
		{"seg_7.ts", 7},                     // 位数不限（ffmpeg %05d 之外手工放置的短名也能识别）
		{"seg_99999999999999999999.ts", -1}, // Atoi 溢出
		{"seg_abc.ts", -1},
		{"seg_.ts", -1},
		{"other_00001.ts", -1},
		{"seg_00001.tmp", -1},
	}
	for _, c := range cases {
		if got := parseSeq(c.path); got != c.want {
			t.Errorf("parseSeq(%q) = %d, want %d", c.path, got, c.want)
		}
	}
}

func TestSortedSegments(t *testing.T) {
	got := sortedSegments(map[int]Segment{
		3: {Seq: 3}, 1: {Seq: 1}, 2: {Seq: 2},
	})
	if len(got) != 3 {
		t.Fatalf("长度 = %d, want 3", len(got))
	}
	for i, s := range got {
		if s.Seq != i+1 {
			t.Errorf("sortedSegments 顺序 = %v, 期望 seq 1,2,3", got)
			break
		}
	}
	if empty := sortedSegments(map[int]Segment{}); len(empty) != 0 {
		t.Errorf("空 map 应返回空列表, got %v", empty)
	}
}

func TestLastLine(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "(无输出)"},
		{"  \n ", "(无输出)"},
		{"单行", "单行"},
		{"a\nb", "b"},
		{"a\nb\n", "b"},
		{"a\n  b  ", "b"}, // 尾部空白被去掉
	}
	for _, c := range cases {
		if got := lastLine(c.in); got != c.want {
			t.Errorf("lastLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- Merge ----

func TestMergeEmpty(t *testing.T) {
	rec := New("ffmpeg", "", 0) // 空段提前返回，不会真的启动 ffmpeg
	err := rec.Merge(context.Background(), nil, t.TempDir(), filepath.Join(t.TempDir(), "out.mp4"), nil)
	if err == nil || !strings.Contains(err.Error(), "没有可合并的 TS 段") {
		t.Errorf("空段合并错误 = %v, 期望提示没有可合并的 TS 段", err)
	}
}

func TestMergeListFileAndOutput(t *testing.T) {
	requireBin(t, "ffmpeg")
	requireBin(t, "ffprobe")
	rec := New("", "", 0)
	dir := t.TempDir()
	p0 := filepath.Join(dir, "seg_00000.ts")
	p1 := filepath.Join(dir, "seg_00001.ts")
	genTestVideo(t, p0, 1)
	genTestVideo(t, p1, 1)

	out := filepath.Join(dir, "merged.mp4")
	segs := []Segment{{Seq: 0, Path: p0}, {Seq: 1, Path: p1}}
	if err := rec.Merge(context.Background(), segs, dir, out, nil); err != nil {
		t.Fatalf("合并失败: %v", err)
	}

	// list.txt 相对路径规则：concat 的 file 相对 list.txt 所在目录解析，只写文件名
	list, err := os.ReadFile(filepath.Join(dir, "list.txt"))
	if err != nil {
		t.Fatalf("读 list.txt 失败: %v", err)
	}
	want := "file 'seg_00000.ts'\nfile 'seg_00001.ts'\n"
	if string(list) != want {
		t.Errorf("list.txt 内容 = %q, want %q", list, want)
	}

	if _, err := os.Stat(out); err != nil {
		t.Fatalf("合并产物不存在: %v", err)
	}
	// 合并 stderr 全量留档：合并结束后目录应有 ffmpeg-merge.log
	if _, err := os.Stat(filepath.Join(dir, FFmpegMergeLog)); err != nil {
		t.Fatalf("合并结束应在录制目录留档 %s: %v", FFmpegMergeLog, err)
	}
	d, err := rec.ProbeDuration(context.Background(), out)
	if err != nil {
		t.Fatalf("探测合并产物失败: %v", err)
	}
	if d < 1.6 || d > 2.5 {
		t.Errorf("合并产物时长 = %.2fs, 期望约 2s（两段各 1s 无损拼接）", d)
	}
}

// ---- Probe ----

func TestProbeDuration(t *testing.T) {
	requireBin(t, "ffmpeg")
	requireBin(t, "ffprobe")
	rec := New("", "", 0)
	mp4 := filepath.Join(t.TempDir(), "probe.mp4")
	genTestVideo(t, mp4, 2)

	d, err := rec.ProbeDuration(context.Background(), mp4)
	if err != nil {
		t.Fatalf("探测时长失败: %v", err)
	}
	if d < 1.8 || d > 2.5 {
		t.Errorf("探测时长 = %.2fs, 期望约 2s", d)
	}

	_, err = rec.ProbeDuration(context.Background(), filepath.Join(t.TempDir(), "missing.mp4"))
	if err == nil || !strings.Contains(err.Error(), "ffprobe 探测失败") {
		t.Errorf("探测不存在文件的错误 = %v, 期望 ffprobe 探测失败", err)
	}
}

func TestProbeStream(t *testing.T) {
	requireBin(t, "ffmpeg")
	requireBin(t, "ffprobe")
	rec := New("", "", 0)
	mp4 := filepath.Join(t.TempDir(), "probe.mp4")
	genTestVideo(t, mp4, 2)

	t.Run("本地文件可探测", func(t *testing.T) {
		if err := rec.ProbeStream(context.Background(), mp4, 5*time.Second); err != nil {
			t.Errorf("本地文件探测失败: %v", err)
		}
	})

	t.Run("HTTP 源可探测", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f, err := os.Open(mp4)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer f.Close()
			http.ServeContent(w, r, "probe.mp4", time.Time{}, f)
		}))
		defer srv.Close()
		if err := rec.ProbeStream(context.Background(), srv.URL, 10*time.Second); err != nil {
			t.Errorf("HTTP 源探测失败: %v", err)
		}
	})

	t.Run("源无响应探测超时", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done() // 不发任何数据，直到客户端断开
		}))
		defer srv.Close()
		err := rec.ProbeStream(context.Background(), srv.URL, 800*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "探测超时") {
			t.Errorf("挂死源错误 = %v, 期望探测超时", err)
		}
	})

	t.Run("源不可达报错", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		err := rec.ProbeStream(context.Background(), srv.URL, 5*time.Second)
		if err == nil || !strings.Contains(err.Error(), "源流不可用") {
			t.Errorf("404 源错误 = %v, 期望源流不可用", err)
		}
	})
}

// ---- Merge 健壮性（P-2：损坏段不得静默丢内容）----

func TestMergeOutputAnomaly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"打不开段文件", "[mpegts @ 0x1] Impossible to open '/records/x/seg_00003.ts'", true},
		{"无效数据", "[mpegts @ 0x1] Invalid data found when processing input", true},
		{"demux 错误", "Error during demuxing: Invalid data", true},
		{"读包失败含无效数据", "Error retrieving a packet from demuxer: Invalid data found when processing input", true},
		{"无输出", "", false},
		{"非目标告警不误判", "[mp4 @ 0x1] Application provided invalid, non monotonically increasing dts", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := mergeOutputAnomaly(c.in)
			if got != c.want {
				t.Errorf("mergeOutputAnomaly(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestMergeRejectsCorruptSegment 清单中间混入损坏段（异常退出截断）时，Merge 必须
// 报错而不是仅凭退出码 0 放行——否则损坏段之后的内容被静默丢弃（宕机恢复实测问题）。
func TestMergeRejectsCorruptSegment(t *testing.T) {
	requireBin(t, "ffmpeg")
	requireBin(t, "ffprobe")
	rec := New("", "", 0)
	dir := t.TempDir()
	p0 := filepath.Join(dir, "seg_00000.ts")
	p1 := filepath.Join(dir, "seg_00001.ts") // 损坏段：占位垃圾字节，模拟在写段被硬截断
	p2 := filepath.Join(dir, "seg_00002.ts")
	genTestVideo(t, p0, 1)
	genTestVideo(t, p1, 1)
	genTestVideo(t, p2, 1)
	if err := os.WriteFile(p1, []byte("not a mpegts stream at all, truncated garbage"), 0o644); err != nil {
		t.Fatalf("写损坏段失败: %v", err)
	}

	err := rec.Merge(context.Background(), []Segment{{Seq: 0, Path: p0}, {Seq: 1, Path: p1}, {Seq: 2, Path: p2}},
		dir, filepath.Join(dir, "out.mp4"), nil)
	if err == nil {
		t.Fatalf("损坏段在清单中间时 Merge 应报错（不允许静默丢内容），实际返回 nil")
	}
}

// TestMaxSeqOnDisk 验证续录起始段号的磁盘兜底：多段取最大、忽略非段文件与已隔离损坏段。
func TestMaxSeqOnDisk(t *testing.T) {
	dir := t.TempDir()
	if got := MaxSeqOnDisk(dir); got != 0 {
		t.Errorf("空目录 MaxSeqOnDisk = %d, 期望 0", got)
	}
	for _, name := range []string{"seg_00001.ts", "seg_00007.ts", "seg_00003.ts", "list.txt", "seg_00002.ts.corrupt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("写 %s 失败: %v", name, err)
		}
	}
	if got := MaxSeqOnDisk(dir); got != 7 {
		t.Errorf("MaxSeqOnDisk = %d, 期望 7（取最大段号，忽略 list.txt 与 .corrupt）", got)
	}
}
