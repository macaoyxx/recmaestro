// loadgen — RecMaestro 高并发压测工具（仅 Go 标准库，独立于主模块）。
//
// 子命令：
//   mixed      混合读压测：tasks/list、stats、audit-logs/list 按权重 5:3:2 混合，
//              固定并发闭环打满指定时长，输出 RPS / p50 / p95 / p99 / 错误数。
//   loginburst 登录限流验证：串行连发登录（用正确密码，不触发账号锁定），
//              观察第 11 次起 429；等待限流窗口滚动后可再单发验证恢复。
//   storm      任务风暴：并发批量创建录制任务（start_time / end_time 统一，
//              全部走定时队列同时到期），输出每个任务的创建结果并把任务 ID 落盘。
//
// 用法示例（WSL 内，token 由 scripts/login.sh 产出）：
//   go run . mixed   -base http://127.0.0.1:8080 -c 50 -d 60 -token "$tok"
//   go run . loginburst -base http://127.0.0.1:8080 -n 12 -user admin -pass "$pw"
//   go run . storm    -base http://127.0.0.1:8080 -n 100 -c 10 -prefix 压测 \
//                    -start 2026-10-15T12:00:30+08:00 -end 2026-10-15T12:03:30+08:00 \
//                    -token "$tok" -out /tmp/storm_ids.txt
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------------- 通用

type envelope struct {
	Code  int             `json:"code"`
	Msg   string          `json:"msg"`
	Data  json.RawMessage `json:"data"`
}

func newClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:          300,
			MaxIdleConnsPerHost:   300,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 14 * time.Second,
		},
	}
}

// doJSON 发请求并解信封。返回 (httpCode, 信封, err)；HTTP 200 但信封 code 非 0 视为业务错误。
func doJSON(c *http.Client, method, url, token string, body any) (int, *envelope, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	env := &envelope{}
	_ = json.Unmarshal(raw, env)
	if resp.StatusCode == http.StatusOK && env.Code != 0 {
		return resp.StatusCode, env, fmt.Errorf("信封 code=%d msg=%s", env.Code, env.Msg)
	}
	return resp.StatusCode, env, nil
}

// ---------------------------------------------------------------- 统计

type stat struct {
	name  string
	lat   []float64 // 毫秒
	errN  int
	total int
}

func (s *stat) add(lat float64, isErr bool) {
	s.total++
	s.lat = append(s.lat, lat)
	if isErr {
		s.errN++
	}
}

// pct 返回分位延迟（毫秒）。l 必须已排序。
func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)) * p / 100)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return sorted[idx]
}

func fmtMs(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// ---------------------------------------------------------------- mixed 子命令

func cmdMixed(fs *flag.FlagSet) error {
	base := fs.String("base", "http://127.0.0.1:8080", "API 基地址")
	conc := fs.Int("c", 50, "并发数")
	dur := fs.Int("d", 60, "时长（秒）")
	token := fs.String("token", "", "Bearer token")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	type ep struct {
		name   string
		method string
		path   string
		body   func() any
	}
	eps := []ep{
		{"tasks/list", "POST", "/api/tasks/list", func() any {
			return map[string]any{"page": 1, "page_size": 20}
		}},
		{"stats", "GET", "/api/stats", nil},
		{"audit-logs/list", "POST", "/api/audit-logs/list", func() any {
			return map[string]any{"page": 1, "page_size": 20}
		}},
	}
	// 权重 5:3:2，展开成长度 10 的表随机取
	wl := []int{0, 0, 0, 0, 0, 1, 1, 1, 2, 2}

	client := newClient()
	deadline := time.Now().Add(time.Duration(*dur) * time.Second)

	var mu sync.Mutex
	stats := make([]*stat, len(eps))
	for i := range eps {
		stats[i] = &stat{name: eps[i].name}
	}
	var transportErrs []string
	var httpErrSamples []string

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for time.Now().Before(deadline) {
				e := eps[wl[rng.Intn(len(wl))]]
				var body any
				if e.body != nil {
					body = e.body()
				}
				t0 := time.Now()
				hc, _, err := doJSON(client, e.method, *base+e.path, *token, body)
				lat := float64(time.Since(t0).Microseconds()) / 1000
				isErr := err != nil || hc != 200
				mu.Lock()
				stats[findEp(stats, e.name)].add(lat, isErr)
				if isErr && len(httpErrSamples) < 10 {
					httpErrSamples = append(httpErrSamples,
						fmt.Sprintf("%s HTTP=%d err=%v", e.name, hc, err))
				}
				if err != nil && len(transportErrs) < 5 {
					transportErrs = append(transportErrs, e.name+": "+err.Error())
				}
				mu.Unlock()
			}
		}(time.Now().UnixNano() + int64(w))
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 汇总
	all := &stat{name: "ALL"}
	for _, s := range stats {
		all.total += s.total
		all.errN += s.errN
		all.lat = append(all.lat, s.lat...)
	}
	sort.Float64s(all.lat)
	errRate := 0.0
	if all.total > 0 {
		errRate = float64(all.errN) / float64(all.total) * 100
	}
	fmt.Printf("=== 混合读压测结果 ===\n")
	fmt.Printf("时长: %.1fs  并发: %d\n", elapsed.Seconds(), *conc)
	fmt.Printf("总请求: %d  RPS: %.1f\n", all.total, float64(all.total)/elapsed.Seconds())
	fmt.Printf("错误: %d (%.3f%%)\n", all.errN, errRate)
	fmt.Printf("延迟(ms): p50=%s p95=%s p99=%s max=%s\n",
		fmtMs(pct(all.lat, 50)), fmtMs(pct(all.lat, 95)), fmtMs(pct(all.lat, 99)), fmtMs(all.lat[len(all.lat)-1]))
	fmt.Printf("%-18s %8s %6s %10s %10s\n", "端点", "请求", "错误", "p50(ms)", "p99(ms)")
	for _, s := range stats {
		sort.Float64s(s.lat)
		fmt.Printf("%-18s %8d %6d %10s %10s\n", s.name, s.total, s.errN,
			fmtMs(pct(s.lat, 50)), fmtMs(pct(s.lat, 99)))
	}
	for _, e := range transportErrs {
		fmt.Printf("传输错误样例: %s\n", e)
	}
	for _, e := range httpErrSamples {
		fmt.Printf("错误样例: %s\n", e)
	}
	return nil
}

func findEp(stats []*stat, name string) int {
	for i, s := range stats {
		if s.name == name {
			return i
		}
	}
	return 0
}

// ---------------------------------------------------------------- loginburst 子命令

func cmdLoginBurst(fs *flag.FlagSet) error {
	base := fs.String("base", "http://127.0.0.1:8080", "API 基地址")
	n := fs.Int("n", 12, "连续登录次数")
	user := fs.String("user", "admin", "用户名")
	pass := fs.String("pass", "", "密码（正确密码；成功登录会重置失败计数，不触发锁号）")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *pass == "" {
		return fmt.Errorf("必须传 -pass（在 shell 内 source 取得，绝不回显）")
	}
	client := newClient()
	ok200, got429, other := 0, 0, 0
	for i := 1; i <= *n; i++ {
		hc, _, err := doJSON(client, "POST", *base+"/api/login", "",
			map[string]string{"username": *user, "password": *pass})
		switch {
		case err == nil && hc == 200:
			ok200++
			fmt.Printf("#%02d HTTP 200 登录成功\n", i)
		case err == nil && hc == 429:
			got429++
			fmt.Printf("#%02d HTTP 429 触发限流\n", i)
		default:
			other++
			fmt.Printf("#%02d HTTP %d err=%v\n", i, hc, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Printf("=== 登录限流结果 === 成功=%d 限流429=%d 其他=%d\n", ok200, got429, other)
	return nil
}

// ---------------------------------------------------------------- storm 子命令

func cmdStorm(fs *flag.FlagSet) error {
	base := fs.String("base", "http://127.0.0.1:8080", "API 基地址")
	n := fs.Int("n", 100, "任务数")
	c := fs.Int("c", 10, "创建并发数")
	prefix := fs.String("prefix", "压测", "任务名前缀（自动加【】与序号）")
	startT := fs.String("start", "", "统一 start_time（RFC3339）")
	endT := fs.String("end", "", "统一 end_time（RFC3339）")
	token := fs.String("token", "", "Bearer token")
	stream := fs.String("stream", "rtmp://recmaestro-srs-e2e:1935/live/e2e", "拉流地址")
	seg := fs.Int("seg", 30, "分段秒数")
	out := fs.String("out", "/tmp/storm_ids.txt", "任务 ID 落盘路径")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *startT == "" || *endT == "" {
		return fmt.Errorf("必须传 -start / -end（RFC3339）")
	}

	client := newClient()
	type result struct {
		idx  int
		id   string
		hc   int
		err  string
		name string
	}
	results := make([]result, *n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, *c)
	t0 := time.Now()
	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			name := fmt.Sprintf("【%s-%d】", *prefix, idx+1)
			hc, env, err := doJSON(client, "POST", *base+"/api/tasks", *token, map[string]any{
				"name":            name,
				"stream_url":      *stream,
				"start_time":      *startT,
				"end_time":        *endT,
				"segment_seconds": *seg,
			})
			r := result{idx: idx, hc: hc, name: name}
			if err != nil {
				r.err = err.Error()
			}
			if env != nil && env.Code == 0 {
				var d struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(env.Data, &d) == nil && d.ID != "" {
					r.id = d.ID
				}
			}
			results[idx] = r
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(t0)

	okN, failN := 0, 0
	f, ferr := os.Create(*out)
	if ferr != nil {
		return fmt.Errorf("创建落盘文件失败: %w", ferr)
	}
	defer f.Close()
	for _, r := range results {
		if r.hc == 201 && r.err == "" {
			okN++
			fmt.Fprintf(f, "%s\t%s\n", r.id, r.name)
		} else {
			failN++
			fmt.Printf("创建失败: %s HTTP=%d err=%s\n", r.name, r.hc, r.err)
		}
	}
	fmt.Printf("=== 任务风暴创建结果 === 总数=%d 成功=%d 失败=%d 耗时=%.1fs\n",
		*n, okN, failN, elapsed.Seconds())
	fmt.Printf("任务 ID+名称已落盘: %s（格式: id<TAB>名称，断言阶段查 MySQL 对账）\n", *out)
	return nil
}

// ---------------------------------------------------------------- main

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: loadgen <mixed|loginburst|storm> [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	var err error
	switch os.Args[1] {
	case "mixed":
		err = cmdMixed(fs)
	case "loginburst":
		err = cmdLoginBurst(fs)
	case "storm":
		err = cmdStorm(fs)
	default:
		err = fmt.Errorf("未知子命令 %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
