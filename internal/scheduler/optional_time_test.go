package scheduler

import (
	"encoding/json"
	"testing"
	"time"
)

// TestOptionalTimeUnmarshal 验证可空时间字段兼容空串/null/空白（P-1），
// 非法格式仍报错保持 400。
func TestOptionalTimeUnmarshal(t *testing.T) {
	valid := "2026-09-23T15:00:00+08:00"
	want, err := time.Parse(time.RFC3339, valid)
	if err != nil {
		t.Fatalf("准备基准时间失败: %v", err)
	}

	cases := []struct {
		name    string
		data    string
		wantNil bool
		want    time.Time
		wantErr bool
	}{
		{"空串视为未设置", `""`, true, time.Time{}, false},
		{"null 视为未设置", `null`, true, time.Time{}, false},
		{"空白字符串视为未设置", `"   "`, true, time.Time{}, false},
		{"合法 RFC3339", `"` + valid + `"`, false, want, false},
		{"非法格式报错", `"2026-13-99 99:99"`, false, time.Time{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ot OptionalTime
			err := json.Unmarshal([]byte(c.data), &ot)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望解析报错, 实际 nil（Time=%v）", ot.Time)
				}
				return
			}
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if c.wantNil {
				if ot.Time != nil {
					t.Errorf("期望 nil, 实际 %v", ot.Time)
				}
				return
			}
			if ot.Time == nil || !ot.Time.Equal(c.want) {
				t.Errorf("期望 %v, 实际 %v", c.want, ot.Time)
			}
		})
	}
}
