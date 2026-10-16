// Package recorder 实现 ffmpeg 录制内核：拉流、TS 分段、断流重连、合并转 MP4、时长校验。
package recorder

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Segment 是一段录制产物（TS 段）。
type Segment struct {
	Seq  int
	Path string
	Size int64
}

// 合并与探测的兜底超时常量：ffmpeg/ffprobe 遇损坏流或 IO 挂起可能无限卡死，
// 到时强杀子进程、返回错误走调用方既有的失败/重试路径，不允许挂住 worker 执行槽。
const (
	mergeTimeout     = 10 * time.Minute // 单次合并上限（正常合并远快于此）
	probeFileTimeout = 30 * time.Second // 单文件时长探测上限
)

// RecordOptions 录制参数。
type RecordOptions struct {
	StreamURL   string
	RecordsDir  string
	SegmentSecs int
	StartSeq    int
	EndTime     *time.Time
	OnSegment   func(Segment)     // 每发现新段回调（上报元数据）
	OnHeartbeat func()            // 扫描周期回调（用于心跳）
	OnLog       func(line string) // ffmpeg stderr 每行回调（日志采集）
}

// ffmpeg 原始 stderr 留档文件名（固定落在任务录制目录，worker 内部接口按名下载）。
const (
	FFmpegRecordLog = "ffmpeg-record.log"
	FFmpegMergeLog  = "ffmpeg-merge.log"
)

// Recorder 是 ffmpeg 录制内核。
type Recorder struct {
	ffmpegPath        string
	ffprobePath       string
	logLevel          string // ffmpeg 录制日志级别（来自 config ffmpeg_loglevel，空串回退 warning）
	reconnectDelayMax int    // HTTP 断流重连最大间隔秒（来自 config reconnect.delay_max，≤0 回退 10）
}

// New 创建 Recorder。ffmpegPath 为空时使用 PATH 中的 ffmpeg；
// logLevel / reconnectDelayMax 为 ffmpeg 运行参数（对应配置 recorder.ffmpeg_loglevel
// 与 recorder.reconnect.delay_max），零值时回退内置默认。
func New(ffmpegPath, logLevel string, reconnectDelayMax int) *Recorder {
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	// ffprobe 与 ffmpeg 同目录、同名前缀：只替换最后一段文件名中的 ffmpeg，
	// 避免整串替换误伤目录名里的 ffmpeg 字样（如 C:\ffmpeg\bin\ffmpeg.exe）。
	// 用字符串切分而非 filepath.Split/Join：后者按平台分隔符解析，会改写路径风格
	var ffprobePath string
	if i := strings.LastIndexAny(ffmpegPath, `/\`); i < 0 {
		ffprobePath = strings.Replace(ffmpegPath, "ffmpeg", "ffprobe", 1)
	} else {
		ffprobePath = ffmpegPath[:i+1] + strings.Replace(ffmpegPath[i+1:], "ffmpeg", "ffprobe", 1)
	}
	return &Recorder{
		ffmpegPath:        ffmpegPath,
		ffprobePath:       ffprobePath,
		logLevel:          logLevel,
		reconnectDelayMax: reconnectDelayMax,
	}
}

// recordLogLevel 返回录制日志级别（未配置回退 warning，与历史行为一致）。
func (r *Recorder) recordLogLevel() string {
	if r.logLevel == "" {
		return "warning"
	}
	return r.logLevel
}

// httpReconnectDelayMax 返回 HTTP 断流重连最大间隔秒（未配置回退 10）。
func (r *Recorder) httpReconnectDelayMax() int {
	if r.reconnectDelayMax <= 0 {
		return 10
	}
	return r.reconnectDelayMax
}

// Record 拉流录制 TS 段，阻塞直到 ctx 取消、endTime 到达或 ffmpeg 异常退出。
// 返回按 seq 升序的段列表。
func (r *Recorder) Record(ctx context.Context, opts RecordOptions) ([]Segment, error) {
	if err := os.MkdirAll(opts.RecordsDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建录制目录失败: %w", err)
	}

	segSecs := opts.SegmentSecs
	if segSecs <= 0 {
		segSecs = 300
	}

	args := []string{
		"-hide_banner",
		"-loglevel", r.recordLogLevel(),
		"-nostats",
		// IO 读超时（微秒）：断流时服务端常保持连接不发数据，ffmpeg 会无限挂起；
		// 加读超时让其主动退出，触发 worker 层断流检测。直播数据连续，10s 足够宽裕
		"-rw_timeout", "10000000",
	}

	// -reconnect 系列是 HTTP 协议专属选项，RTMP/SRT 等协议不支持（会导致 ffmpeg 报错退出）。
	// 非 HTTP 协议的断流由 worker 层的续录循环兜底。
	if useHTTPReconnect(opts.StreamURL) {
		args = append(args,
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_at_eof", "1",
			"-reconnect_delay_max", strconv.Itoa(r.httpReconnectDelayMax()),
		)
	}

	args = append(args,
		"-i", opts.StreamURL,
		"-c", "copy",
		"-f", "segment",
		"-segment_time", strconv.Itoa(segSecs),
		"-segment_format", "mpegts",
		"-reset_timestamps", "1",
		"-segment_start_number", strconv.Itoa(opts.StartSeq),
		filepath.Join(opts.RecordsDir, "seg_%05d.ts"),
	)

	cmd := exec.Command(r.ffmpegPath, args...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("获取 stderr 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 ffmpeg 失败: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// stderr 采集：逐行回调 OnLog，同时留档用于错误摘要
	var stderrBuf bytes.Buffer
	var stderrMu sync.Mutex
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			stderrMu.Lock()
			stderrBuf.WriteString(line + "\n")
			stderrMu.Unlock()
			if opts.OnLog != nil {
				opts.OnLog(line)
			}
		}
	}()

	// 段扫描 goroutine
	seen := map[int]Segment{}
	stopScan := make(chan struct{})
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopScan:
				r.scanSegments(opts.RecordsDir, seen, opts.OnSegment)
				return
			case <-ticker.C:
				r.scanSegments(opts.RecordsDir, seen, opts.OnSegment)
				if opts.OnHeartbeat != nil {
					opts.OnHeartbeat()
				}
			}
		}
	}()

	// 结束定时器
	var endTimer *time.Timer
	var endCh <-chan time.Time
	if opts.EndTime != nil {
		endTimer = time.NewTimer(time.Until(*opts.EndTime))
		defer endTimer.Stop()
		endCh = endTimer.C
	}

	// 等待结束条件
	var runErr error
	ffmpegExited := false
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
	case <-endCh:
		// 到点正常结束
	case err := <-done:
		ffmpegExited = true
		stderrMu.Lock()
		summary := lastLine(stderrBuf.String())
		stderrMu.Unlock()
		runErr = fmt.Errorf("ffmpeg 退出: %w (stderr: %s)", err, summary)
	}

	// 若 ffmpeg 仍在运行（ctx 取消/到点触发），优雅停止
	if !ffmpegExited {
		stopFFmpeg(cmd, done)
	}

	// 等 stderr 读取完成（ffmpeg 退出后管道关闭）
	<-stderrDone

	// 停止扫描并做最终扫描（ffmpeg 已停，段都写完，size 准确）
	close(stopScan)
	<-scanDone
	r.scanSegments(opts.RecordsDir, seen, opts.OnSegment)

	// ffmpeg stderr 全量留档：一次性写入录制目录（失败不影响录制结果，仅缺留档）
	stderrMu.Lock()
	archive := stderrBuf.Bytes()
	stderrMu.Unlock()
	_ = os.WriteFile(filepath.Join(opts.RecordsDir, FFmpegRecordLog), archive, 0o644)

	segments := sortedSegments(seen)

	if ctx.Err() != nil {
		return segments, ctx.Err()
	}
	if runErr != nil {
		return segments, runErr
	}
	return segments, nil
}

// Merge 用 concat demuxer 无损合并 TS 段为 MP4。onLog 用于采集 ffmpeg 合并日志。
// ctx 取消或超时（mergeTimeout）时强杀合并进程并返回错误，防损坏流让 ffmpeg 无限挂起。
func (r *Recorder) Merge(ctx context.Context, segments []Segment, recordsDir, outputPath string, onLog func(string)) error {
	if len(segments) == 0 {
		return fmt.Errorf("没有可合并的 TS 段")
	}
	listPath := filepath.Join(recordsDir, "list.txt")
	var buf bytes.Buffer
	for _, seg := range segments {
		// concat 的 file 路径相对 list.txt 所在目录解析，list 与段同目录，写文件名即可
		fmt.Fprintf(&buf, "file '%s'\n", filepath.Base(seg.Path))
	}
	if err := os.WriteFile(listPath, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写合并列表失败: %w", err)
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-c", "copy",
		outputPath,
	}
	mctx, cancel := context.WithTimeout(ctx, mergeTimeout)
	defer cancel()
	cmd := exec.CommandContext(mctx, r.ffmpegPath, args...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("获取 stderr 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动合并 ffmpeg 失败: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var out bytes.Buffer
	var mu sync.Mutex
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			out.WriteString(line + "\n")
			mu.Unlock()
			if onLog != nil {
				onLog(line)
			}
		}
	}()

	if err := <-done; err != nil {
		mu.Lock()
		detail := out.String()
		mu.Unlock()
		// stderr 全量留档（成功与失败都留，失败排障更有价值；留档失败不影响合并结果）
		_ = os.WriteFile(filepath.Join(recordsDir, FFmpegMergeLog), []byte(detail), 0o644)
		if mctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("合并超时（%s），已强制终止 ffmpeg", mergeTimeout)
		}
		return fmt.Errorf("合并失败: %w (stderr: %s)", err, detail)
	}
	// 退出码为 0 不代表合并完整：concat 读到损坏段时报段级错误后把已读部分封盘退出，
	// 仅凭退出码判成功会静默丢弃损坏段之后的内容（宕机恢复实测发现），必须复查 stderr
	mu.Lock()
	detail := out.String()
	mu.Unlock()
	_ = os.WriteFile(filepath.Join(recordsDir, FFmpegMergeLog), []byte(detail), 0o644)
	if bad, line := mergeOutputAnomaly(detail); bad {
		return fmt.Errorf("合并输出异常（疑似损坏段导致内容缺失）: %s", line)
	}
	return nil
}

// mergeOutputAnomaly 识别 concat 合并输出中的段级错误（大小写不敏感子串匹配）。
func mergeOutputAnomaly(stderr string) (bool, string) {
	for _, line := range strings.Split(stderr, "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "impossible to open") ||
			strings.Contains(l, "invalid data found") ||
			strings.Contains(l, "error during demuxing") {
			return true, strings.TrimSpace(line)
		}
	}
	return false, ""
}

// MaxSeqOnDisk 返回目录中已有 TS 段的最大段号（无段返回 0）。
// 续录起始段号用它兜底：宕机可能落在段扫描周期内（DB 还没来得及记最新段），
// 只按 DB 计算会算小，导致 ffmpeg -segment_start_number 覆盖重写磁盘上已有的段文件。
func MaxSeqOnDisk(recordsDir string) int {
	matches, _ := filepath.Glob(filepath.Join(recordsDir, "seg_*.ts"))
	max := 0
	for _, p := range matches {
		if s := parseSeq(p); s > max {
			max = s
		}
	}
	return max
}

// ProbeDuration 用 ffprobe 探测文件时长（秒）。
// 单文件探测带超时（probeFileTimeout），损坏文件让 ffprobe 挂死时强制终止。
func (r *Recorder) ProbeDuration(ctx context.Context, path string) (float64, error) {
	pctx, cancel := context.WithTimeout(ctx, probeFileTimeout)
	defer cancel()
	cmd := exec.CommandContext(pctx, r.ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		path,
	)
	out, err := cmd.Output()
	if pctx.Err() == context.DeadlineExceeded {
		return 0, fmt.Errorf("探测超时（%s）: %s", probeFileTimeout, filepath.Base(path))
	}
	if err != nil {
		return 0, fmt.Errorf("ffprobe 探测失败: %w", err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("解析时长失败: %w", err)
	}
	return d, nil
}

// ProbeStream 探测源流是否可拉取：ffprobe 读流头，拿到流信息即认为可用。
// timeout 兜底整个探测时长（连接挂死时由 context 超时杀掉）。
func (r *Recorder) ProbeStream(ctx context.Context, streamURL string, timeout time.Duration) error {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(pctx, r.ffprobePath,
		"-v", "error",
		"-show_entries", "stream=index",
		"-of", "csv=p=0",
		streamURL,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if pctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("探测超时（%s），源流不可用", timeout)
	}
	if err != nil {
		return fmt.Errorf("源流不可用: %w (ffprobe: %s)", err, lastLine(stderr.String()))
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("源流无可用轨道")
	}
	return nil
}

func (r *Recorder) scanSegments(dir string, seen map[int]Segment, onSegment func(Segment)) {
	matches, _ := filepath.Glob(filepath.Join(dir, "seg_*.ts"))
	for _, path := range matches {
		seq := parseSeq(path)
		if seq < 0 {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		seg := Segment{Seq: seq, Path: path, Size: info.Size()}
		if old, ok := seen[seq]; ok {
			if old.Size != seg.Size {
				seen[seq] = seg // 段正在写，size 会增长，持续更新
			}
			continue
		}
		seen[seq] = seg
		if onSegment != nil {
			onSegment(seg)
		}
	}
}

func parseSeq(path string) int {
	base := filepath.Base(path)
	trimmed := strings.TrimSuffix(strings.TrimPrefix(base, "seg_"), ".ts")
	seq, err := strconv.Atoi(trimmed)
	if err != nil {
		return -1
	}
	return seq
}

func sortedSegments(seen map[int]Segment) []Segment {
	segs := make([]Segment, 0, len(seen))
	for _, s := range seen {
		segs = append(segs, s)
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Seq < segs[j].Seq })
	return segs
}

func stopFFmpeg(cmd *exec.Cmd, done chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(无输出)"
	}
	lines := strings.Split(s, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// useHTTPReconnect 判断该地址是否支持 ffmpeg 的 reconnect 输入选项（仅 HTTP 系协议支持）。
func useHTTPReconnect(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}
