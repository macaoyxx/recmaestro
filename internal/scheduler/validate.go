package scheduler

import "time"

// validateTaskTimes 校验任务时间约束，返回错误信息（空串表示通过）：
// 截止时间必填、须晚于当前时间；开始时间非空时截止时间须晚于开始时间。
// 开始时间为空或已过 = 立即执行，属于既有设计，不做校验。
func validateTaskTimes(start, end *time.Time, now time.Time) string {
	if end == nil {
		return "截止时间（end_time）必填"
	}
	if !end.After(now) {
		return "截止时间不能早于当前时间"
	}
	if start != nil && !end.After(*start) {
		return "截止时间必须晚于开始时间"
	}
	return ""
}

// validateSegmentSeconds 校验分段时长：0 表示不改/用默认值，非 0 时须在 [5, 3600] 秒。
func validateSegmentSeconds(seg int) string {
	if seg != 0 && (seg < 5 || seg > 3600) {
		return "分段时长须在 5-3600 秒之间"
	}
	return ""
}
