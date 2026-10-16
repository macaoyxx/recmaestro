package scheduler

import (
	"fmt"
	"strings"
	"time"
)

// OptionalTime 兼容可空时间字段的 JSON 反序列化："" / null / 纯空白视为未设置（nil），
// 其余按 RFC3339 解析，非法格式仍报错（保持 400）。
// 背景：*time.Time 直接绑定时 JSON 空字符串会反序列化失败，导致 start_time 传 ""
// 的第三方调用被拒（文档语义"为空则立即执行"应包含空串）。
type OptionalTime struct{ *time.Time }

// UnmarshalJSON 实现 json.Unmarshaler。
func (t *OptionalTime) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(string(data)), `"`))
	if s == "" || s == "null" {
		t.Time = nil
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("时间格式须为 RFC3339: %w", err)
	}
	t.Time = &parsed
	return nil
}
