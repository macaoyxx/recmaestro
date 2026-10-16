<template>
  <div>
    <div class="toolbar">
      <h3>审计日志</h3>
      <span v-if="updatedAt" class="updated mono">{{ updatedAt }}</span>
      <el-button size="small" @click="load">刷新</el-button>
    </div>

    <el-table :data="logs" border>
      <el-table-column label="时间" width="160">
        <template #default="{ row }"><span class="mono dim">{{ fmtTime(row.created_at) }}</span></template>
      </el-table-column>
      <el-table-column label="用户" width="110">
        <template #default="{ row }">
          <span v-if="row.operator_name" class="uname hoverable"
                @mouseenter="showTip($event, row.user_id)" @mouseleave="scheduleHide">{{ row.operator_name }}</span>
          <span v-else class="mono dim">{{ row.user_id || '-' }}</span>
        </template>
      </el-table-column>
      <el-table-column label="动作" width="150">
        <template #default="{ row }">
          <span class="st">
            <span class="rm-dot" :style="{ background: actionColor(row.action) }"></span>
            {{ actionText[row.action] || row.action }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="对象" min-width="180">
        <template #default="{ row }">
          <span class="res-type">{{ resourceText[row.resource] || row.resource }}</span>
          <span v-if="row.resource_name" class="res-name hoverable"
                @mouseenter="showTip($event, row.resource_id)" @mouseleave="scheduleHide">【{{ row.resource_name }}】</span>
          <span v-else class="mono dim">【{{ row.resource_id }}】</span>
        </template>
      </el-table-column>
      <el-table-column prop="detail" label="详情" min-width="200" show-overflow-tooltip />
      <el-table-column label="来源 IP" width="130">
        <template #default="{ row }"><span class="mono">{{ row.ip || '-' }}</span></template>
      </el-table-column>
      <el-table-column label="User-Agent" min-width="180" show-overflow-tooltip>
        <template #default="{ row }"><span class="dim">{{ row.user_agent || '-' }}</span></template>
      </el-table-column>
    </el-table>

    <!-- ID 悬停气泡：内容可划选复制，鼠标移入气泡不消失 -->
    <div v-if="tip.visible" class="id-pop mono"
         :style="{ left: tip.x + 'px', top: tip.y + 'px' }"
         @mouseenter="cancelHide" @mouseleave="scheduleHide">ID: {{ tip.text }}</div>

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
  </div>
</template>

<script setup>
import { ref, reactive, onMounted, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'
import { formatTs as fmtTime } from '../format'

const logs = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(10)
const updatedAt = ref('')

// 动作中文名与指示点颜色（与后端 store/audit_log.go 的动作常量对应）
// 资源类型枚举：key 落库，前端取展示名（新增类型在此登记）
const resourceText = {
  user: '用户',
  task: '任务'
}

const actionText = {
  login_success: '登录成功',
  login_fail: '密码错误',
  login_locked: '账号锁定',
  user_create: '创建用户',
  user_enable: '启用用户',
  user_disable: '禁用用户',
  user_reset_password: '重置密码',
  user_delete: '删除用户',
  task_edit: '编辑任务',
  task_abort: '中止任务',
  task_delete: '删除任务',
  file_merge: '手动合并',
  file_delete: '删除文件'
}

function actionColor(action) {
  if (['login_locked', 'task_delete', 'file_delete'].includes(action)) return '#ff4d5e'
  if (['login_fail', 'user_disable', 'task_abort'].includes(action)) return '#f5a623'
  if (action === 'login_success' || action === 'user_enable') return '#34d399'
  return '#5b8cff'
}

// ID 悬停气泡：显示与隐藏之间留 150ms 缓冲，鼠标从名称移到气泡上不消失（内容可划选）
const tip = reactive({ visible: false, text: '', x: 0, y: 0 })
let hideTimer = null

function showTip(e, id) {
  cancelHide()
  const r = e.target.getBoundingClientRect()
  tip.text = String(id || '')
  tip.x = Math.min(r.left, window.innerWidth - 260)
  tip.y = r.bottom + 6
  tip.visible = true
}

function scheduleHide() {
  cancelHide()
  hideTimer = setTimeout(() => { tip.visible = false }, 150)
}

function cancelHide() {
  if (hideTimer) {
    clearTimeout(hideTimer)
    hideTimer = null
  }
}

onUnmounted(cancelHide)

async function load() {
  try {
    const resp = await api.listAuditLogs({ page: page.value, page_size: pageSize.value })
    logs.value = resp.data || []
    total.value = resp.total || 0
    updatedAt.value = new Date().toLocaleTimeString()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

function onSizeChange() {
  page.value = 1
  load()
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 14px; margin-bottom: 16px; }
.toolbar h3 { margin: 0; font-size: 15px; margin-right: auto; }
.updated { font-size: 12px; color: var(--rm-ink-dim); }
.st { display: inline-flex; align-items: center; gap: 7px; }
.dim { color: var(--rm-ink-dim); }
.uname { color: var(--rm-ink); }
.res-type { color: var(--rm-ink-dim); }
.res-name { color: var(--rm-ink); }
.hoverable:hover { text-decoration: underline dotted; cursor: default; }
.id-pop {
  position: fixed;
  z-index: 3000;
  padding: 6px 10px;
  background: var(--rm-deep);
  border: 1px solid var(--rm-signal);
  border-radius: 4px;
  color: var(--rm-ink);
  font-size: 12px;
  white-space: nowrap;
  user-select: text;
  cursor: text;
  box-shadow: 0 6px 18px rgba(0, 0, 0, 0.4);
}
.pagination { margin-top: 14px; display: flex; justify-content: flex-end; }
</style>
