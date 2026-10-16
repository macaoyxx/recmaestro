<template>
  <div>
    <div class="toolbar">
      <h3>用户管理</h3>
      <el-button size="small" type="primary" plain @click="openCreate">创建用户</el-button>
      <el-button size="small" @click="load">刷新</el-button>
    </div>

    <el-table :data="users" border>
      <el-table-column label="ID" width="80">
        <template #default="{ row }"><span class="mono">{{ row.id }}</span></template>
      </el-table-column>
      <el-table-column prop="username" label="用户名" min-width="140" />
      <el-table-column label="角色" width="100">
        <template #default="{ row }">
          <span class="st">
            <span class="rm-dot" :style="{ background: row.role === 'admin' ? 'var(--rm-signal)' : 'var(--rm-ink-dim)' }"></span>
            {{ row.role === 'admin' ? '管理员' : '用户' }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }">
          <span class="st">
            <span class="rm-dot" :class="{ 'rm-dot-live': row.status === 'enabled' }"
                  :style="{ background: row.status === 'enabled' ? 'var(--rm-ok)' : 'var(--rm-ink-dim)' }"></span>
            {{ row.status === 'enabled' ? '启用' : '禁用' }}
          </span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="300">
        <template #default="{ row }">
          <el-button v-if="row.status === 'enabled' && !isProtected(row)" size="small" type="warning" plain @click="toggle(row, 'disabled')">禁用</el-button>
          <el-button v-else-if="row.status !== 'enabled'" size="small" type="success" plain @click="toggle(row, 'enabled')">启用</el-button>
          <el-button size="small" @click="openReset(row)">重置密码</el-button>
          <el-button v-if="!isProtected(row)" size="small" type="danger" plain @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 创建 / 重置密码 共用对话框 -->
    <el-dialog v-model="dialogVisible" :title="dialogMode === 'create' ? '创建用户' : `重置 ${dialogUser ? dialogUser.username : ''} 的密码`" width="420px">
      <el-form label-width="78px" @submit.prevent="save">
        <el-form-item v-if="dialogMode === 'create'" label="用户名">
          <el-input v-model="form.username" placeholder="登录用户名" />
        </el-form-item>
        <el-form-item :label="dialogMode === 'create' ? '密码' : '新密码'">
          <el-input v-model="form.password" type="password" show-password placeholder="至少 6 位" />
        </el-form-item>
        <el-form-item v-if="dialogMode === 'create'" label="角色">
          <el-radio-group v-model="form.role">
            <el-radio value="user">用户</el-radio>
            <el-radio value="admin">管理员</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">{{ dialogMode === 'create' ? '创建' : '重置' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'
import { rmConfirm } from '../confirm'

const users = ref([])
const currentUsername = localStorage.getItem('username') || ''

// 受保护账号：内置 admin 与当前登录用户——不可删除、不可禁用（后端同样拦截，前端只藏按钮）
function isProtected(row) {
  return row.username === 'admin' || row.username === currentUsername
}
const dialogVisible = ref(false)
const dialogMode = ref('create')
const dialogUser = ref(null)
const saving = ref(false)
const form = ref({ username: '', password: '', role: 'user' })

async function load() {
  try {
    const resp = await api.listUsers()
    users.value = resp.data || []
  } catch (e) {
    ElMessage.error(e.message)
  }
}

function openCreate() {
  dialogMode.value = 'create'
  dialogUser.value = null
  form.value = { username: '', password: '', role: 'user' }
  dialogVisible.value = true
}

function openReset(row) {
  dialogMode.value = 'reset'
  dialogUser.value = row
  form.value = { username: '', password: '', role: 'user' }
  dialogVisible.value = true
}

async function save() {
  if (dialogMode.value === 'create') {
    if (!form.value.username || !form.value.password) {
      ElMessage.warning('请填写用户名与密码')
      return
    }
    try {
      await api.createUser(form.value)
      ElMessage.success('用户已创建')
      dialogVisible.value = false
      load()
    } catch (e) {
      ElMessage.error(e.message)
    }
    return
  }
  if (!form.value.password) {
    ElMessage.warning('请填写新密码')
    return
  }
  try {
    await api.resetPassword(dialogUser.value.id, form.value.password)
    ElMessage.success('密码已重置')
    dialogVisible.value = false
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function remove(row) {
  const ok = await rmConfirm({
    title: '删除用户',
    tone: 'danger',
    confirmText: '删除用户',
    message: `确认删除用户 ${row.username}？账号将被物理移除、无法恢复；历史审计保留其名称快照。`
  })
  if (!ok) return
  try {
    await api.deleteUser(row.id)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function toggle(row, status) {
  const enabling = status === 'enabled'
  const ok = await rmConfirm({
    title: enabling ? '启用用户' : '禁用用户',
    tone: enabling ? 'signal' : 'warn',
    confirmText: enabling ? '启用' : '禁用',
    message: enabling
      ? `确认启用用户 ${row.username}？启用会同时清零登录失败计数（解除锁定）。`
      : `确认禁用用户 ${row.username}？禁用后该账号无法登录。`
  })
  if (!ok) return
  try {
    await api.updateUserStatus(row.id, status)
    ElMessage.success(enabling ? '已启用' : '已禁用')
    load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 14px; margin-bottom: 16px; }
.toolbar h3 { margin: 0; font-size: 15px; margin-right: auto; }
.st { display: inline-flex; align-items: center; gap: 7px; }
</style>
