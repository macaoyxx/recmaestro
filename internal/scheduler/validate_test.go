package scheduler

import (
	"testing"
	"time"
)

func TestValidateTaskTimes(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	later := future.Add(time.Hour)

	cases := []struct {
		name    string
		start   *time.Time
		end     *time.Time
		wantMsg string
	}{
		{"end 为 nil", nil, nil, "截止时间（end_time）必填"},
		{"end 早于当前时间", nil, &past, "截止时间不能早于当前时间"},
		{"end 等于当前时间", nil, &now, "截止时间不能早于当前时间"},
		{"end 晚于当前时间且无 start", nil, &future, ""},
		{"end 等于 start（均为未来）", &future, &future, "截止时间必须晚于开始时间"},
		{"end 晚于当前时间但早于 start", &later, &future, "截止时间必须晚于开始时间"},
		{"start 已过且 end 合法", &past, &future, ""},
		{"start 未来且 end 更晚", &future, &later, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validateTaskTimes(c.start, c.end, now); got != c.wantMsg {
				t.Errorf("validateTaskTimes() = %q, want %q", got, c.wantMsg)
			}
		})
	}
}

func TestValidateSegmentSeconds(t *testing.T) {
	cases := []struct {
		seg     int
		wantMsg string
	}{
		{0, ""}, // 不改/用默认值
		{4, "分段时长须在 5-3600 秒之间"},
		{5, ""}, // 下边界
		{300, ""},
		{3600, ""}, // 上边界
		{3601, "分段时长须在 5-3600 秒之间"},
		{-1, "分段时长须在 5-3600 秒之间"},
	}

	for _, c := range cases {
		if got := validateSegmentSeconds(c.seg); got != c.wantMsg {
			t.Errorf("validateSegmentSeconds(%d) = %q, want %q", c.seg, got, c.wantMsg)
		}
	}
}
