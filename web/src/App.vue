<template>
  <el-config-provider :locale="zhCn">
    <router-view v-if="isLogin" />
    <div v-else class="shell">
    <aside class="side">
      <div class="brand">
        <span class="rm-dot rm-dot-live brand-dot"></span>
        <span class="brand-name">RECMAESTRO</span>
      </div>

      <!-- 签名元素：ON AIR 信号面板（录制路数 + 每秒跳动的时间码） -->
      <div class="onair">
        <div class="onair-row">
          <span class="rm-dot" :class="{ 'rm-dot-live': recording > 0 }"
                :style="{ background: recording > 0 ? 'var(--rm-live)' : 'var(--rm-ink-dim)' }"></span>
          <span class="onair-label" :class="{ live: recording > 0 }">{{ recording > 0 ? 'ON AIR' : 'STANDBY' }}</span>
        </div>
        <div class="onair-count mono">{{ recording }}<span class="onair-unit">路录制中</span></div>
        <div class="onair-clock mono">{{ clock }}</div>
      </div>

      <nav class="nav">
        <router-link v-for="m in menus" :key="m.path" :to="m.path" class="nav-item" :title="m.label">
          <el-icon :size="16"><component :is="m.icon" /></el-icon>
          <span class="nav-label">{{ m.label }}</span>
        </router-link>
      </nav>

      <div class="side-footer">
        <a class="doc-link" href="/swagger/index.html" target="_blank" title="API 文档">
          <el-icon :size="16"><Document /></el-icon>
          <span class="nav-label">API 文档</span>
        </a>
        <button class="nav-item logout" @click="logout" title="退出登录">
          <el-icon :size="16"><SwitchButton /></el-icon>
          <span class="nav-label">退出</span>
        </button>
      </div>
    </aside>

    <main class="main">
      <router-view />
    </main>
  </div>

  <!-- 全局公共确认弹窗（任意组件经 rmConfirm() 调用） -->
  <RmConfirm />
  </el-config-provider>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElConfigProvider } from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import { VideoCamera, DataLine, Monitor, Document, SwitchButton, User, Tickets } from '@element-plus/icons-vue'
import { api } from './api'
import RmConfirm from './components/RmConfirm.vue'

const route = useRoute()
const router = useRouter()
const isLogin = computed(() => route.path === '/login')

// 管理菜单（用户/审计）仅 admin 可见；角色在登录时落 localStorage，刷新后缺失则经 /api/me 兜底补齐
const role = ref(localStorage.getItem('role') || '')
const username = ref(localStorage.getItem('username') || '')
const allMenus = [
  { path: '/', label: '任务', icon: VideoCamera },
  { path: '/stats', label: '统计', icon: DataLine },
  { path: '/nodes', label: '节点', icon: Monitor },
  { path: '/users', label: '用户', icon: User, admin: true },
  { path: '/audit-logs', label: '审计', icon: Tickets, admin: true }
]
const menus = computed(() => allMenus.filter((m) => !m.admin || role.value === 'admin'))

// ---- ON AIR 信号面板：录制路数 15s 轮询（静默失败、页面隐藏暂停）+ 每秒时钟 ----
const recording = ref(0)
const clock = ref('')
let statsTimer = null
let clockTimer = null

async function loadRecording() {
  try {
    const resp = await api.stats()
    const data = resp.data
    recording.value = (data && data.tasks_by_status && data.tasks_by_status.running) || 0
  } catch (e) {
    // 静默：边栏信号灯不是关键路径，失败时保留上次值
  }
}

function tick() {
  const d = new Date()
  const pad = (n) => String(n).padStart(2, '0')
  clock.value = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

function onVisibilityChange() {
  if (document.hidden) {
    stopTimers()
  } else {
    loadRecording()
    tick()
    startTimers()
  }
}

function startTimers() {
  if (!statsTimer) statsTimer = setInterval(loadRecording, 15000)
  if (!clockTimer) clockTimer = setInterval(tick, 1000)
}

function stopTimers() {
  if (statsTimer) { clearInterval(statsTimer); statsTimer = null }
  if (clockTimer) { clearInterval(clockTimer); clockTimer = null }
}

// 登录成功（登录页 -> 应用）时 App 不会重新挂载：路由切换时机重读登录页落库的角色，
// 拿不到再走 /me 兜底；否则管理菜单要等手动刷新才出现
watch(isLogin, (onApp) => {
  if (!onApp) {
    role.value = localStorage.getItem('role') || ''
    username.value = localStorage.getItem('username') || ''
    if (!role.value) loadRole()
  }
})

async function loadRole() {
  if ((role.value && username.value) || !localStorage.getItem('token')) return
  try {
    const resp = await api.me()
    role.value = (resp.data && resp.data.role) || ''
    username.value = (resp.data && resp.data.username) || ''
    localStorage.setItem('role', role.value)
    localStorage.setItem('username', username.value)
  } catch (e) {
    // 静默：拿不到角色时管理菜单不显示，不影响基础页面
  }
}

onMounted(() => {
  loadRecording()
  loadRole()
  tick()
  startTimers()
  document.addEventListener('visibilitychange', onVisibilityChange)
})

onUnmounted(() => {
  stopTimers()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})

function logout() {
  localStorage.removeItem('token')
  localStorage.removeItem('refresh_token')
  localStorage.removeItem('role')
  localStorage.removeItem('username')
  role.value = ''
  username.value = ''
  router.push('/login')
}
</script>

<style scoped>
.shell { display: flex; height: 100vh; }

/* ---- 边栏：机架式，比内容区深一档 ---- */
.side {
  width: 200px;
  flex: none;
  display: flex;
  flex-direction: column;
  background: var(--rm-deep);
  border-right: 1px solid var(--rm-line);
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 20px 18px 14px;
}
.brand-dot { width: 10px; height: 10px; background: var(--rm-live); }
.brand-name {
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 0.22em;
  color: var(--rm-ink);
}

/* ON AIR 信号面板 */
.onair {
  margin: 2px 14px 14px;
  padding: 12px 14px;
  border: 1px solid var(--rm-line);
  border-radius: 4px;
  background: var(--rm-panel);
}
.onair-row { display: flex; align-items: center; gap: 8px; }
.onair-label {
  font-size: 11px;
  letter-spacing: 0.18em;
  font-weight: 700;
  color: var(--rm-ink-dim);
}
.onair-label.live { color: var(--rm-live); }
.onair-count { font-size: 26px; font-weight: 700; margin-top: 6px; color: var(--rm-ink); }
.onair-unit { font-size: 12px; font-weight: 400; color: var(--rm-ink-dim); margin-left: 6px; }
.onair-clock { font-size: 13px; color: var(--rm-ink-dim); margin-top: 2px; }

/* 导航 */
.nav { display: flex; flex-direction: column; gap: 2px; padding: 0 10px; flex: 1; }
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 10px;
  border: none;
  border-left: 2px solid transparent;
  border-radius: 0 4px 4px 0;
  background: transparent;
  color: var(--rm-ink-dim);
  font-size: 13px;
  font-family: var(--rm-sans);
  text-decoration: none;
  cursor: pointer;
  transition: color 0.15s, background 0.15s;
}
.nav-item:hover { color: var(--rm-ink); background: var(--rm-raised); }
.nav-item.router-link-active {
  color: var(--rm-signal);
  border-left-color: var(--rm-signal);
  background: var(--rm-raised);
}

.side-footer {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px;
  border-top: 1px solid var(--rm-line);
}
.doc-link {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px;
  border-radius: 4px;
  color: var(--rm-ink-dim);
  font-size: 13px;
  text-decoration: none;
}
.doc-link:hover { color: var(--rm-ink); background: var(--rm-raised); }
.logout { border-radius: 4px; }

/* 内容区 */
.main { flex: 1; overflow-y: auto; padding: 24px 28px; }

/* 窄屏：边栏收为纯图标 */
@media (max-width: 768px) {
  .side { width: 52px; }
  .brand { padding: 16px 0 12px; justify-content: center; }
  .brand-name, .onair, .nav-label { display: none; }
  .nav, .side-footer { padding: 0 6px; }
  .nav-item { justify-content: center; }
  .doc-link { justify-content: center; }
  .main { padding: 16px; }
}
</style>
