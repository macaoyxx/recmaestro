<template>
  <div v-loading="loading">
    <div class="hero">
      <div class="hero-main">
        <div class="rm-eyebrow">正在录制</div>
        <div class="rec-line">
          <span class="rm-dot rec-dot" :class="{ 'rm-dot-live': recording > 0 }"
                :style="{ background: recording > 0 ? 'var(--rm-live)' : 'var(--rm-ink-dim)' }"></span>
          <span class="rec-num mono" :class="{ dim: recording === 0 }">{{ recording }}</span>
        </div>
        <div class="rec-hint">{{ recording > 0 ? '路任务推流录制中' : '空闲——没有正在录制的任务' }}</div>
      </div>

      <div class="hero-metrics">
        <div class="metric rm-panel">
          <div class="metric-num mono">{{ queueSize }}</div>
          <div class="metric-label">执行队列积压</div>
        </div>
        <div class="metric rm-panel">
          <div class="metric-num mono">{{ scheduledSize }}</div>
          <div class="metric-label">定时待触发</div>
        </div>
        <div class="metric rm-panel">
          <div class="metric-num mono">{{ nodesOnline }}<span class="metric-sub">/{{ nodesTotal }}</span></div>
          <div class="metric-label">worker 在线</div>
        </div>
      </div>
    </div>

    <!-- 状态分布：横向分段信号条（一段一状态色，宽度按占比） -->
    <div class="dist rm-panel">
      <div class="dist-head">
        <span class="rm-eyebrow">任务状态分布</span>
        <span class="updated mono" v-if="updatedAt">{{ updatedAt }}</span>
        <el-button size="small" text @click="load(true)">刷新</el-button>
      </div>

      <template v-if="total > 0">
        <div class="signal-bar">
          <div v-for="seg in segments" :key="seg.status" class="seg"
               :style="{ width: seg.pct + '%', background: seg.color }"
               :title="`${seg.label} ${seg.count}`"></div>
        </div>
        <div class="legend">
          <div v-for="seg in segments" :key="seg.status" class="legend-item">
            <span class="rm-dot" :style="{ background: seg.color }"></span>
            <span class="legend-label">{{ seg.label }}</span>
            <span class="mono legend-count">{{ seg.count }}</span>
            <span class="legend-pct mono">{{ seg.pct }}%</span>
          </div>
        </div>
      </template>
      <div v-else class="empty">
        还没有任务。去<a href="#/">任务页</a>创建第一个录制，状态分布会在这里亮起来。
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'
import { statusText, statusColor } from '../format'

const loading = ref(false)
const counts = ref({})
const queueSize = ref(0)
const scheduledSize = ref(0)
const nodesOnline = ref(0)
const nodesTotal = ref(0)
const updatedAt = ref('')
let timer = null

const recording = computed(() => counts.value.running || 0)
const total = computed(() => Object.values(counts.value).reduce((a, b) => a + b, 0))

// 信号条分段：只显示非零状态，按视觉重要度排序（在录在前，异常其次，收尾在后）
const SEG_ORDER = ['running', 'queued', 'pending', 'recovering', 'completed', 'aborted', 'cancelled', 'interrupted', 'record_failed', 'merge_failed']
const segments = computed(() => {
  const t = total.value
  if (!t) return []
  return SEG_ORDER
    .filter((st) => counts.value[st])
    .map((st) => ({
      status: st,
      count: counts.value[st],
      pct: Math.round((counts.value[st] / t) * 1000) / 10,
      color: statusColor(st),
      label: statusText[st] || st
    }))
})

async function load(manual = false) {
  loading.value = true
  try {
    const resp = await api.stats()
    const data = resp.data || {}
    counts.value = data.tasks_by_status || {}
    queueSize.value = data.queue_size || 0
    scheduledSize.value = data.scheduled_size || 0
    nodesOnline.value = data.nodes_online || 0
    nodesTotal.value = data.nodes_total || 0
    updatedAt.value = new Date().toLocaleTimeString()
  } catch (e) {
    // 自动轮询错误静默（避免后端抖动时每 10s 弹窗），手动刷新才提示
    if (manual) ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

function startTimer() {
  if (!timer) timer = setInterval(() => load(), 10000)
}

function stopTimer() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

function onVisibilityChange() {
  if (document.hidden) {
    stopTimer()
  } else {
    load()
    startTimer()
  }
}

onMounted(() => {
  load(true)
  startTimer()
  document.addEventListener('visibilitychange', onVisibilityChange)
})

onUnmounted(() => {
  stopTimer()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>

<style scoped>
/* 主位：录制中大数字 */
.hero { display: flex; gap: 24px; align-items: stretch; margin-bottom: 24px; }
.hero-main { flex: 1; }
.rec-line { display: flex; align-items: center; gap: 14px; margin-top: 8px; }
.rec-dot { width: 12px; height: 12px; }
.rec-num { font-size: 56px; font-weight: 700; line-height: 1; color: var(--rm-ink); }
.rec-num.dim { color: var(--rm-ink-dim); }
.rec-hint { color: var(--rm-ink-dim); font-size: 13px; margin-top: 10px; }

.hero-metrics { display: flex; gap: 14px; align-items: stretch; }
.metric { padding: 16px 20px; min-width: 128px; display: flex; flex-direction: column; justify-content: center; }
.metric-num { font-size: 28px; font-weight: 700; color: var(--rm-ink); }
.metric-sub { font-size: 14px; color: var(--rm-ink-dim); font-weight: 400; }
.metric-label { font-size: 12px; color: var(--rm-ink-dim); margin-top: 6px; }

/* 状态分布面板 */
.dist { padding: 18px 20px; }
.dist-head { display: flex; align-items: center; gap: 14px; margin-bottom: 16px; }
.updated { font-size: 12px; color: var(--rm-ink-dim); }
.dist-head :last-child { margin-left: auto; }

.signal-bar {
  display: flex;
  height: 14px;
  border-radius: 3px;
  overflow: hidden;
  background: var(--rm-deep);
}
.seg { min-width: 6px; transition: width 0.4s; }

.legend { display: flex; flex-wrap: wrap; gap: 10px 26px; margin-top: 14px; }
.legend-item { display: flex; align-items: center; gap: 7px; font-size: 12.5px; }
.legend-label { color: var(--rm-ink); }
.legend-count { color: var(--rm-ink); font-weight: 700; }
.legend-pct { color: var(--rm-ink-dim); }

.empty { color: var(--rm-ink-dim); font-size: 13px; padding: 22px 0 8px; }
.empty a { color: var(--rm-signal); }

@media (max-width: 900px) {
  .hero { flex-direction: column; }
}
</style>
