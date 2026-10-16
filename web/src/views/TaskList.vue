<template>
  <div>
    <div class="toolbar">
      <el-input v-model="keyword" placeholder="搜索名称 / 直播地址" clearable style="width: 240px" @keyup.enter="search" @clear="search" />
      <el-select v-model="statusFilter" placeholder="全部状态" clearable style="width: 150px" @change="search">
        <el-option v-for="(text, code) in statusText" :key="code" :label="text" :value="code" />
      </el-select>
      <el-button type="primary" @click="search">搜索</el-button>
      <el-button type="primary" plain @click="openCreate">创建任务</el-button>
    </div>

    <el-table :data="tasks" border>
      <el-table-column prop="id" label="ID" width="150" show-overflow-tooltip>
        <template #default="{ row }"><span class="mono">{{ row.id }}</span></template>
      </el-table-column>
      <el-table-column prop="name" label="名称" min-width="90" show-overflow-tooltip />
      <el-table-column prop="stream_url" label="直播地址" min-width="280" show-overflow-tooltip />
      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <span class="st">
            <span class="rm-dot" :class="{ 'rm-dot-live': row.status === 'running' }"
                  :style="{ background: statusColor(row.status) }"></span>
            {{ statusText[row.status] || row.status }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="创建时间" width="150">
        <template #default="{ row }"><span class="mono dim">{{ fmtTime(row.created_at) }}</span></template>
      </el-table-column>
      <el-table-column label="开始时间" width="150">
        <template #default="{ row }"><span class="mono dim">{{ fmtTime(row.start_time) }}</span></template>
      </el-table-column>
      <el-table-column label="截止时间" width="150">
        <template #default="{ row }"><span class="mono dim">{{ fmtTime(row.end_time) }}</span></template>
      </el-table-column>
      <el-table-column label="操作" width="250">
        <template #default="{ row }">
          <el-button size="small" @click="$router.push(`/tasks/${row.id}`)">详情</el-button>
          <el-button v-if="editable(row.status)" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button v-if="isRunning(row.status)" size="small" type="warning" @click="abort(row)">中止</el-button>
          <el-button v-if="editable(row.status)" size="small" type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <div class="pagination">
      <el-pagination
        v-model:current-page="page"
        v-model:page-size="pageSize"
        :total="total"
        layout="total, prev, pager, next, sizes"
        :page-sizes="[10, 20, 50, 100]"
        @current-change="load"
        @size-change="onSizeChange"
      />
    </div>

    <el-dialog v-model="dialogVisible" :title="editingId ? '编辑任务' : '创建任务'" width="540px">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-form-item v-if="!editingId" label="任务 ID">
          <el-input v-model="form.id" placeholder="留空自动生成（雪花 ID / UUID）" />
        </el-form-item>
        <el-form-item label="名称"><el-input v-model="form.name" placeholder="可选" /></el-form-item>
        <el-form-item label="直播地址" required><el-input v-model="form.stream_url" placeholder="rtmp://... 或 http://..." /></el-form-item>
        <el-form-item label="开始时间">
          <el-date-picker v-model="form.start_time" type="datetime" placeholder="留空立即开始" style="width: 100%" @change="onStartTimeChange" />
        </el-form-item>
        <el-form-item label="结束时间" prop="end_time">
          <el-date-picker v-model="form.end_time" type="datetime" placeholder="必填，到点自动停止" style="width: 100%" :disabled-date="disablePastDays" />
        </el-form-item>
        <el-form-item label="分段时长">
          <el-input-number v-model="form.segment_seconds" :min="5" :max="3600" /> 秒
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="save">{{ editingId ? '保存' : '创建' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'
import { rmConfirm } from '../confirm'
import { statusText, formatTs as fmtTime, statusColor } from '../format'

const tasks = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(10)
const statusFilter = ref('')
const keyword = ref('')
const dialogVisible = ref(false)
const editingId = ref(null)
const formRef = ref(null)
const form = ref({ id: '', name: '', stream_url: '', start_time: null, end_time: null, segment_seconds: 300 })

const rules = {
  end_time: [
    { required: true, message: '请选择结束时间', trigger: 'change' },
    {
      validator: (rule, value, callback) => {
        if (!value) return callback()
        if (value.getTime() <= Date.now()) return callback(new Error('结束时间不能早于当前时间'))
        if (form.value.start_time && value.getTime() <= form.value.start_time.getTime()) {
          return callback(new Error('结束时间必须晚于开始时间'))
        }
        callback()
      },
      trigger: 'change'
    }
  ]
}

// 结束时间选择器禁选今天之前的日期（当天可选，精确到时分秒由 validator 兜底）
function disablePastDays(d) {
  return d.getTime() < new Date().setHours(0, 0, 0, 0)
}

// 开始时间变化后重新校验已填的结束时间，保证先后关系即时生效
function onStartTimeChange() {
  if (form.value.end_time) {
    formRef.value?.validateField('end_time').catch(() => {})
  }
}

async function load() {
  const params = { page: page.value, page_size: pageSize.value }
  if (statusFilter.value) params.status = statusFilter.value
  if (keyword.value) params.keyword = keyword.value
  const resp = await api.listTasks(params)
  tasks.value = resp.data || []
  total.value = resp.total || 0
}

function search() {
  page.value = 1
  load()
}

function onSizeChange() {
  page.value = 1
  load()
}

function editable(status) {
  return status !== 'running' && status !== 'recovering'
}

function isRunning(status) {
  return status === 'running' || status === 'recovering'
}

function openCreate() {
  editingId.value = null
  form.value = { id: '', name: '', stream_url: '', start_time: null, end_time: null, segment_seconds: 300 }
  dialogVisible.value = true
}

function openEdit(row) {
  editingId.value = row.id
  form.value = {
    id: '',
    name: row.name || '',
    stream_url: row.stream_url || '',
    start_time: row.start_time ? new Date(row.start_time) : null,
    end_time: row.end_time ? new Date(row.end_time) : null,
    segment_seconds: row.segment_seconds || 300
  }
  dialogVisible.value = true
}

async function save() {
  if (!form.value.stream_url) {
    ElMessage.warning('请填写直播地址')
    return
  }
  try {
    await formRef.value.validate()
  } catch (e) {
    return
  }
  // 时间字段为覆盖式提交：始终显式携带（null 表示清空开始时间，后端以此为准）
  const payload = {
    name: form.value.name,
    stream_url: form.value.stream_url,
    segment_seconds: form.value.segment_seconds,
    start_time: form.value.start_time ? formatTime(form.value.start_time) : null,
    end_time: form.value.end_time ? formatTime(form.value.end_time) : null
  }
  if (form.value.id) payload.id = form.value.id
  try {
    if (editingId.value) {
      await api.updateTask(editingId.value, payload)
      ElMessage.success('已更新')
    } else {
      await api.createTask(payload)
      ElMessage.success('任务已创建')
    }
    dialogVisible.value = false
    load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function abort(row) {
  const ok = await rmConfirm({
    title: '中止任务',
    tone: 'warn',
    confirmText: '中止任务',
    message: `确认中止任务 #${row.id}？中止后可在详情页查看，或通过重试继续处理。`
  })
  if (!ok) return
  try {
    await api.abortTask(row.id)
    ElMessage.success('已中止')
    load()
  } catch (e) {
    ElMessage.error(e.message || '中止失败')
  }
}

async function remove(row) {
  const ok = await rmConfirm({
    title: '删除任务',
    tone: 'danger',
    confirmText: '删除任务',
    message: `确认删除任务 #${row.id}？任务的段文件与产物记录将一并移除。`
  })
  if (!ok) return
  try {
    await api.deleteTask(row.id)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    ElMessage.error(e.message || '删除失败')
  }
}

function formatTime(d) {
  // 生成 RFC3339 格式（含时区偏移），后端 Go 的 time.Time 才能解析
  const pad = (n) => String(n).padStart(2, '0')
  const offset = -d.getTimezoneOffset()
  const sign = offset >= 0 ? '+' : '-'
  const offH = pad(Math.floor(Math.abs(offset) / 60))
  const offM = pad(Math.abs(offset) % 60)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}${sign}${offH}:${offM}`
}

onMounted(load)
</script>

<style scoped>
.toolbar { margin-bottom: 14px; display: flex; gap: 12px; }
.pagination { margin-top: 14px; display: flex; justify-content: flex-end; }
.st { display: inline-flex; align-items: center; gap: 7px; }
.dim { color: var(--rm-ink-dim); }
</style>
