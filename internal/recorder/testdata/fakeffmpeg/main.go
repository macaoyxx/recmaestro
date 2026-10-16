// fake ffmpeg 测试替身：把收到的命令行参数写档后挂起，等停止文件出现再退出。
// 由 recorder_test.go 在测试运行期编译为可执行文件，用于捕获 Record/Merge 构造的
// ffmpeg 参数与验证回调接线，避免测试依赖真实拉流。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fake ffmpeg 错误:", err)
		os.Exit(1)
	}
}

func run() error {
	// 参数写档路径（JSON 数组），由测试通过环境变量注入
	argsFile := os.Getenv("FAKE_ARGS_FILE")
	if argsFile == "" {
		return fmt.Errorf("缺少 FAKE_ARGS_FILE 环境变量")
	}
	data, err := json.Marshal(os.Args[1:])
	if err != nil {
		return fmt.Errorf("序列化参数失败: %w", err)
	}
	if err := os.WriteFile(argsFile, data, 0o644); err != nil {
		return fmt.Errorf("写参数文件失败: %w", err)
	}

	// 工作目录写档（可选），供测试验证进程工作目录
	if cwdFile := os.Getenv("FAKE_CWD_FILE"); cwdFile != "" {
		wd, _ := os.Getwd()
		_ = os.WriteFile(cwdFile, []byte(wd), 0o644)
	}

	// 输出一行 stderr，验证 OnLog 逐行采集
	fmt.Fprintln(os.Stderr, "fake-ffmpeg started")

	// 轮询停止文件，出现即退出；30 秒兜底，避免测试挂死
	stopFile := os.Getenv("FAKE_STOP_FILE")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stopFile); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}
