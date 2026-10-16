<template>
  <div>
    <div class="toolbar">
      <h3>节点机架</h3>
      <el-button size="small" @click="load">刷新</el-button>
    </div>

    <div v-if="nodes.length" class="rack">
      <div v-for="n in nodes" :key="n.node_id" class="node-card rm-panel">
        <div class="card-head">
          <span class="rm-dot" :class="{ 'rm-dot-live': n.status === 'online' }"
                :style="{ background: n.status === 'online' ? 'var(--rm-ok)' : 'var(--rm-ink-dim)' }"></span>
          <span class="node-id mono">{{ shortId(n.node_id) }}</span>
          <span class="node-st">{{ n.status === 'online' ? '在线' : '离线' }}</span>
        </div>
        <div class="node-host">{{ n.ip }}<span v-if="n.host"> · {{ n.host }}</span></div>

        <div class="meter">
          <div class="meter-row"><span class="meter-label">CPU</span><span class="mono meter-val">{{ (n.cpu || 0).toFixed(1) }}%</span></div>
          <div class="bar"><div class="bar-fill" :style="barStyle(n.cpu, 'cpu')"></div></div>
        </div>
        <div class="meter">
          <div class="meter-row"><span class="meter-label">内存</span><span class="mono meter-val">{{ (n.mem || 0).toFixed(1) }}%</span></div>
          <div class="bar"><div class="bar-fill" :style="barStyle(n.mem, 'mem')"></div></div>
        </div>
        <div class="meter">
          <div class="meter-row"><span class="meter-label">任务</span><span class="mono meter-val">{{ n.current_tasks }}/{{ n.capacity }}</span></div>
          <div class="bar"><div class="bar-fill" :style="barStyle(capPct(n), 'task')"></div></div>
        </div>

        <div class="card-foot">
          <span>磁盘可用 <span class="mono">{{ fmtBytes(n.disk_free) }}</span></span>
          <span class="ffmpeg" :title="n.ffmpeg_version">{{ shortFfmpeg(n.ffmpeg_version) }}</span>
        </div>
      </div>
    </div>

    <div v-else class="empty rm-panel">
      机架上是空的——启动 worker 后约 10 秒内会自动注册上线。
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { api } from '../api'
import { fmtBytes } from '../format'

const nodes = ref([])

async function load() {
  const resp = await api.listNodes()
  nodes.value = resp.data || []
}

// node_id 形如 recmaestro-worker-172-18-0-3-8080，卡片上只展示差异段
function shortId(id) {
  return id.replace(/^recmaestro-worker-/, '')
}

function shortFfmpeg(v) {
  if (!v) return ''
  const m = v.match(/version\s+(\S+)/)
  return m ? 'ffmpeg ' + m[1] : v.slice(0, 22)
}

function capPct(n) {
  if (!n.capacity) return 0
  return (n.current_tasks / n.capacity) * 100
}

// 进度条颜色：CPU/内存按占用升色（低=荧绿，高=琥珀，>85%=红）；任务占用用信号蓝
function barStyle(pct, kind) {
  const p = Math.min(100, Math.max(0, pct || 0))
  let color = 'var(--rm-signal)'
  if (kind === 'cpu' || kind === 'mem') {
    color = p > 85 ? 'var(--rm-live)' : p > 60 ? 'var(--rm-warn)' : 'var(--rm-ok)'
  }
  return { width: p.toFixed(1) + '%', background: color }
}

onMounted(load)
</script>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 14px; margin-bottom: 16px; }
.toolbar h3 { margin: 0; font-size: 15px; }

.rack { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px; }

.node-card { padding: 16px 18px; }
.card-head { display: flex; align-items: center; gap: 9px; }
.node-id { font-size: 13px; font-weight: 700; color: var(--rm-ink); word-break: break-all; }
.node-st { margin-left: auto; font-size: 12px; color: var(--rm-ink-dim); }
.node-host { font-size: 12px; color: var(--rm-ink-dim); margin: 4px 0 14px 17px; }

.meter { margin-bottom: 10px; }
.meter-row { display: flex; justify-content: space-between; margin-bottom: 4px; }
.meter-label { font-size: 12px; color: var(--rm-ink-dim); }
.meter-val { font-size: 12px; color: var(--rm-ink); }

.bar { height: 4px; border-radius: 2px; background: var(--rm-deep); overflow: hidden; }
.bar-fill { height: 100%; border-radius: 2px; transition: width 0.3s; }

.card-foot {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--rm-line);
  font-size: 12px;
  color: var(--rm-ink-dim);
}
.ffmpeg { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

.empty { padding: 40px 24px; text-align: center; color: var(--rm-ink-dim); font-size: 13px; }
</style>
