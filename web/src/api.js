const BASE = '/api'

// refresh 单飞：并发请求同时 401 时只触发一次 token 刷新，其余等待结果后各自重放
let refreshing = null

async function doRefresh() {
  const refreshToken = localStorage.getItem('refresh_token')
  if (!refreshToken) return false
  try {
    const resp = await fetch(BASE + '/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refreshToken })
    })
    if (!resp.ok) return false
    const data = await resp.json().catch(() => null)
    if (!data || !data.access_token) return false
    localStorage.setItem('token', data.access_token)
    return true
  } catch (e) {
    return false
  }
}

function sessionExpired() {
  localStorage.removeItem('token')
  localStorage.removeItem('refresh_token')
  localStorage.removeItem('role')
  localStorage.removeItem('username')
  location.hash = '#/login'
}

async function request(path, options = {}, isRetry = false) {
  const token = localStorage.getItem('token')
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) }
  if (token) headers['Authorization'] = 'Bearer ' + token
  const resp = await fetch(BASE + path, { ...options, headers })

  if (resp.status === 401 && !path.includes('/login') && !path.includes('/refresh')) {
    // access token 过期：静默刷新一次并重放原请求；重放仍 401 或刷新失败才登出
    if (!isRetry) {
      if (!refreshing) {
        refreshing = doRefresh().finally(() => { refreshing = null })
      }
      if (await refreshing) return request(path, options, true)
    }
    sessionExpired()
    throw new Error('登录已过期')
  }
  const json = await resp.json().catch(() => null)
  if (!resp.ok) {
    throw new Error((json && json.msg) || '请求失败')
  }
  // 全局拦截：后端统一信封 {code, data, msg[, total]}；code!=0 视为业务失败
  if (json && typeof json === 'object' && 'code' in json) {
    if (json.code !== 0) throw new Error(json.msg || '请求失败')
    return json
  }
  return json
}

async function download(path, filename) {
  const token = localStorage.getItem('token')
  const resp = await fetch(path, { headers: { Authorization: 'Bearer ' + token } })
  if (!resp.ok) {
    // 失败响应是统一信封 JSON（流式接口仅成功才出流）
    const data = await resp.json().catch(() => null)
    throw new Error((data && data.msg) || '下载失败')
  }
  const blob = await resp.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

export const api = {
  login: (username, password) => request('/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
  refresh: (refreshToken) => request('/refresh', { method: 'POST', body: JSON.stringify({ refresh_token: refreshToken }) }),
  listTasks: (params = {}) => request('/tasks/list', { method: 'POST', body: JSON.stringify(params) }),
  createTask: (task) => request('/tasks', { method: 'POST', body: JSON.stringify(task) }),
  getTask: (id) => request(`/tasks/${id}`),
  cancelTask: (id) => request(`/tasks/${id}/cancel`, { method: 'POST' }),
  retryTask: (id) => request(`/tasks/${id}/retry`, { method: 'POST' }),
  updateTask: (id, task) => request(`/tasks/${id}`, { method: 'PUT', body: JSON.stringify(task) }),
  abortTask: (id) => request(`/tasks/${id}/abort`, { method: 'POST' }),
  deleteTask: (id) => request(`/tasks/${id}`, { method: 'DELETE' }),
  listLogs: (id, params = {}) => request(`/tasks/${id}/logs?` + new URLSearchParams(params)),
  listFiles: (id) => request(`/tasks/${id}/files`),
  mergeTask: (id, segmentIds) => request(`/tasks/${id}/merge`, { method: 'POST', body: JSON.stringify({ segment_ids: segmentIds }) }),
  deleteFiles: (id, fileIds) => request(`/tasks/${id}/files/delete`, { method: 'POST', body: JSON.stringify({ file_ids: fileIds }) }),
  me: () => request('/me'),
  listUsers: () => request('/users'),
  createUser: (user) => request('/users', { method: 'POST', body: JSON.stringify(user) }),
  updateUserStatus: (id, status) => request(`/users/${id}/status`, { method: 'PATCH', body: JSON.stringify({ status }) }),
  deleteUser: (id) => request(`/users/${id}`, { method: 'DELETE' }),
  resetPassword: (id, password) => request(`/users/${id}/password`, { method: 'PATCH', body: JSON.stringify({ password }) }),
  listAuditLogs: (params = {}) => request('/audit-logs/list', { method: 'POST', body: JSON.stringify(params) }),
  listNodes: () => request('/nodes'),
  stats: () => request('/stats'),
  progress: (id) => request(`/tasks/${id}/progress`),
  downloadOutput: (id) => download(`/api/tasks/${id}/download`, `task-${id}.mp4`),
  downloadLogs: (id) => download(`/api/tasks/${id}/logs/download`, `task-${id}.log`),
  downloadFFmpegLog: (id, name) => download(`/api/tasks/${id}/ffmpeg-log/${name}`, `task-${id}-ffmpeg-${name}.log`)
}
