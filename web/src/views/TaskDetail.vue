<template>
  <div>
    <div class="header-row">
      <el-button @click="$router.back()">返回</el-button>
      <h3 class="mono">#{{ taskId }}</h3>
      <span v-if="task" class="st">
        <span class="rm-dot" :class="{ 'rm-dot-live': task.status === 'running' }"
              :style="{ background: statusColor(task.status) }"></span>
        {{ statusText[task.status] || task.status }}
      </span>
      <span v-if="task && task.retry_count > 0" class="retry-count">已重试 {{ task.retry_count }} 次</span>
      <el-button v-if="task && canAbort(task.status)" size="small" type="warning" @click="abort">中止</el-button>
      <el-button v-if="task && canRetry(task.status)" size="small" @click="retry">重试</el-button>
    </div>

    <el-tabs v-model="activeTab">
      <el-tab-pane label="日志" name="logs">
        <div class="toolbar">
          <el-button size="small" @click="loadLogs">刷新</el-button>
          <el-select v-model="logLevel" placeholder="全部级别" clearable size="small" style="width: 130px" @change="loadLogs">
            <el-option label="info" value="info" />
            <el-option label="warn" value="warn" />
            <el-option label="error" value="error" />
          </el-select>
          <el-button size="small" @click="downloadLogs">下载日志</el-button>
          <el-button size="small" @click="downloadFFmpegLog('record')">录制原始日志</el-button>
          <el-button size="small" @click="downloadFFmpegLog('merge')">合并原始日志</el-button>
        </div>
        <div class="log-list">
          <div v-for="l in logs" :key="l.id" class="log-item" :class="l.level">
            <span class="time">{{ formatTs(l.ts) }}</span>
            <span class="level">[{{ l.level }}]</span>
            <span class="stage">[{{ l.stage }}]</span>
            <span class="msg">{{ l.message }}</span>
          </div>
        </div>
      </el-tab-pane>

      <el-tab-pane label="文件" name="files">
        <div class="toolbar">
          <el-button size="small" @click="loadFiles">刷新</el-button>
          <el-button size="small" type="primary" :disabled="!selectedFiles.length" @click="mergeSelected">合并所选</el-button>
          <el-button size="small" type="danger" :disabled="!selectedFiles.length" @click="deleteSelected">删除所选</el-button>
          <el-button size="small" @click="downloadOutput">下载产物</el-button>
        </div>
        <el-table :data="files" @selection-change="(s) => (selectedFiles = s)">
          <el-table-column type="selection" width="50" />
          <el-table-column prop="filename" label="文件名" min-width="160" />
          <el-table-column prop="type" label="类型" width="100" />
          <el-table-column prop="seq" label="序号" width="80">
            <template #default="{ row }"><span class="mono">{{ row.seq }}</span></template>
          </el-table-column>
          <el-table-column label="大小" width="120">
            <template #default="{ row }"><span class="mono">{{ fmtBytes(row.size_bytes) }}</span></template>
          </el-table-column>
          <el-table-column label="可达" width="90">
            <template #default="{ row }">
              <el-tag :type="row.reachable ? 'success' : 'info'">{{ row.reachable ? '可达' : '不可达' }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { api } from '../api'
import { statusText, isTerminal, fmtBytes, formatTs, statusColor } from '../format'

const route = useRoute()
const taskId = route.params.id
const task = ref(null)
const activeTab = ref('logs')
const logs = ref([])
const logLevel = ref('')
const files = ref([])
const selectedFiles = ref([])
let timer = null

async function loadTask() {
  task.value = await api.getTask(taskId)
}

async function loadLogs() {
  const params = { limit: 100 }
  if (logLevel.value) params.level = logLevel.value
  const resp = await api.listLogs(taskId, params)
  logs.value = (resp.data || []).slice().reverse()
}

async function loadFiles() {
  const resp = await api.listFiles(taskId)
  files.value = resp.data || []
}

async function mergeSelected() {
  const ids = selectedFiles.value.filter((f) => f.type === 'segment').map((f) => f.seq)
  try {
    await api.mergeTask(taskId, ids)
    ElMessage.success('合并已下发')
    loadTask().catch(() => {})
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function deleteSelected() {
  const ids = selectedFiles.value.map((f) => f.id)
  try {
    await api.deleteFiles(taskId, ids)
    ElMessage.success('已删除')
    loadFiles().catch(() => {})
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function downloadOutput() {
  try { await api.downloadOutput(taskId) } catch (e) { ElMessage.error(e.message) }
}

async function downloadLogs() {
  try { await api.downloadLogs(taskId) } catch (e) { ElMessage.error(e.message) }
}

async function downloadFFmpegLog(name) {
  try { await api.downloadFFmpegLog(taskId, name) } catch (e) { ElMessage.error(e.message) }
}

async function retry() {
  try {
    const res = await api.retryTask(taskId)
    ElMessage.success(`第 ${res.data.retry_count} 次重试已入队`)
    refresh()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

function canAbort(status) {
  return status === 'running' || status === 'recovering'
}

async function abort() {
  try {
    await api.abortTask(taskId)
    ElMessage.success('已中止')
    loadTask().catch(() => {})
  } catch (e) {
    ElMessage.error(e.message)
  }
}

function canRetry(status) {
  return ['record_failed', 'interrupted'].includes(status)
}

// ---- 轮询治理：任务终态即停；页面不可见暂停、可见恢复；轮询错误不弹窗打扰 ----

function stopTimer() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

function startTimer() {
  if (timer) return
  timer = setInterval(refresh, 5000)
}

function refresh() {
  loadTask().then(() => {
    // 任务进入终态后不再有状态变化，停止轮询
    if (isTerminal(task.value && task.value.status)) stopTimer()
  }).catch(() => {})
  loadLogs().catch(() => {})
  loadFiles().catch(() => {})
}

function onVisibilityChange() {
  if (document.hidden) {
    stopTimer()
  } else if (!isTerminal(task.value && task.value.status)) {
    refresh()
    startTimer()
  }
}

onMounted(() => {
  refresh()
  startTimer()
  document.addEventListener('visibilitychange', onVisibilityChange)
})

onUnmounted(() => {
  stopTimer()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>

<style scoped>
.header-row { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.header-row h3 { margin: 0; font-size: 15px; }
.retry-count { color: var(--rm-warn); font-size: 13px; }
.toolbar { margin-bottom: 12px; display: flex; gap: 12px; }
.st { display: inline-flex; align-items: center; gap: 7px; }

/* 日志面板：终端化（更深底、等宽、按级别左侧色条） */
.log-list {
  max-height: 520px;
  overflow-y: auto;
  font-family: var(--rm-mono);
  font-size: 12px;
  line-height: 1.55;
  background: var(--rm-deep);
  border: 1px solid var(--rm-line);
  border-radius: 4px;
  padding: 6px 0;
}
.log-item { padding: 3px 14px; border-bottom: none; border-left: 2px solid transparent; }
.log-item.error { background: rgba(255, 77, 94, 0.05); border-left-color: var(--rm-live); }
.log-item.warn { background: rgba(245, 166, 35, 0.05); border-left-color: var(--rm-warn); }
.time { color: var(--rm-ink-dim); margin-right: 10px; }
.level { color: var(--rm-signal); margin-right: 10px; }
.stage { color: var(--rm-ok); margin-right: 10px; }
.msg { color: var(--rm-ink); word-break: break-all; }
</style>
