// 共享格式化工具：任务状态文案/标签色、终态判断、字节大小、时间展示。
// 由 TaskList / TaskDetail 共用，避免两处各自维护副本漂移。

export const statusText = {
  pending: '待开始',
  queued: '排队中',
  running: '录制中',
  recovering: '恢复中',
  completed: '已完成',
  record_failed: '录制失败',
  merge_failed: '合并失败',
  interrupted: '已中断',
  cancelled: '已取消',
  aborted: '已中止'
}

export function statusType(status) {
  const map = {
    completed: 'success',
    running: 'primary',
    pending: 'info',
    queued: 'info',
    recovering: 'warning',
    record_failed: 'danger',
    merge_failed: 'danger',
    interrupted: 'danger',
    cancelled: 'info',
    aborted: 'warning'
  }
  return map[status] || 'info'
}

// 任务终态集合：进入终态后详情页停止轮询
export function isTerminal(status) {
  return ['completed', 'record_failed', 'merge_failed', 'interrupted', 'cancelled', 'aborted'].includes(status)
}

// 状态指示点颜色（播控台主题令牌色值，供内联 style 使用；
// 与 theme.css 的 --rm-live/--rm-ok/--rm-warn/--rm-info 保持一致）
export function statusColor(status) {
  const map = {
    running: '#ff4d5e',
    record_failed: '#ff4d5e',
    merge_failed: '#ff4d5e',
    interrupted: '#ff4d5e',
    completed: '#34d399',
    recovering: '#f5a623',
    aborted: '#f5a623',
    pending: '#8a94a8',
    queued: '#8a94a8',
    cancelled: '#8a94a8'
  }
  return map[status] || '#8a94a8'
}

export function fmtBytes(n) {
  if (!n) return '0'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return n.toFixed(1) + ' ' + units[i]
}

// ISO 时间串转本地展示（yyyy-MM-dd HH:mm:ss）
export function formatTs(ts, placeholder = '-') {
  return ts ? ts.replace('T', ' ').slice(0, 19) : placeholder
}
