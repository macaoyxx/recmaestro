<template>
  <Teleport to="body">
    <Transition name="rm-pop">
      <div v-if="state.open" class="rm-mask" @click.self="cancel">
        <div class="rm-box" :class="state.tone" role="alertdialog" :aria-label="state.title">
          <button class="rm-x" aria-label="关闭" @click="cancel">✕</button>

          <!-- 信号灯：确认按钮悬停时「上膛」增亮（播控台的武装感） -->
          <div class="beacon" :class="{ armed }" aria-hidden="true">
            <span class="beacon-core"></span>
          </div>

          <div class="rm-title">{{ state.title }}</div>
          <div class="rm-msg">{{ state.message }}</div>

          <div class="rm-actions">
            <button ref="cancelBtn" class="btn-ghost" @click="cancel">{{ state.cancelText }}</button>
            <button class="btn-solid"
                    @mouseenter="armed = true" @mouseleave="armed = false"
                    @focus="armed = true" @blur="armed = false"
                    @click="ok">{{ state.confirmText }}</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup>
// 全局唯一实例由 App.vue 挂载；状态与 Promise 结算在 confirm.js
import { ref, watch, onUnmounted, nextTick } from 'vue'
import { confirmState as state, settleConfirm } from '../confirm'

const armed = ref(false)
const cancelBtn = ref(null)

function cancel() {
  settleConfirm(false)
}

function ok() {
  settleConfirm(true)
}

// 键盘：ESC 取消、Enter 确认
function onKey(e) {
  if (!state.open) return
  if (e.key === 'Escape') cancel()
  else if (e.key === 'Enter') ok()
}
window.addEventListener('keydown', onKey)
onUnmounted(() => window.removeEventListener('keydown', onKey))

// 打开时聚焦取消按钮：危险操作的默认安全位（Enter 之外的误触不至直达确认）
watch(() => state.open, async (v) => {
  if (v) {
    armed.value = false
    await nextTick()
    if (cancelBtn.value) cancelBtn.value.focus()
  }
})
</script>

<style scoped>
.rm-mask {
  position: fixed;
  inset: 0;
  z-index: 3000;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(5, 9, 17, 0.62);
  backdrop-filter: blur(3px);
}

/* 弹窗盒：tone 决定信号灯与确认按钮的色系 */
.rm-box {
  position: relative;
  width: 360px;
  padding: 26px 26px 22px;
  text-align: center;
  background: var(--rm-panel);
  border: 1px solid var(--rm-line);
  border-radius: 8px;
  box-shadow: 0 18px 48px rgba(0, 0, 0, 0.45);
}
.rm-box.danger { --tone: var(--rm-live); --tone-glow: rgba(255, 77, 94, 0.55); }
.rm-box.warn { --tone: var(--rm-warn); --tone-glow: rgba(245, 166, 35, 0.55); }
.rm-box.signal { --tone: var(--rm-signal); --tone-glow: rgba(91, 140, 255, 0.55); }

/* 右上角关闭 */
.rm-x {
  position: absolute;
  top: 10px;
  right: 10px;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--rm-ink-dim);
  font-size: 13px;
  cursor: pointer;
  transition: color 0.15s, background 0.15s;
}
.rm-x:hover { color: var(--rm-ink); background: var(--rm-raised); }

/* 信号灯：呼吸圆核 + 外圈细环；悬停确认按钮时上膛（更亮 + 外圈扩散） */
.beacon {
  position: relative;
  width: 46px;
  height: 46px;
  margin: 0 auto 14px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.beacon::before {
  content: "";
  position: absolute;
  inset: 0;
  border-radius: 50%;
  border: 1px solid var(--tone);
  opacity: 0.35;
}
.beacon-core {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--tone);
  box-shadow: 0 0 14px 2px var(--tone-glow);
  animation: rm-beacon-breathe 2.2s ease-in-out infinite;
}
@keyframes rm-beacon-breathe {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.6; }
}
.beacon.armed::before { animation: rm-beacon-ring 0.9s ease-out infinite; }
.beacon.armed .beacon-core {
  animation: none;
  opacity: 1;
  transform: scale(1.15);
}
@keyframes rm-beacon-ring {
  0% { transform: scale(1); opacity: 0.5; }
  100% { transform: scale(1.45); opacity: 0; }
}

.rm-title { font-size: 15px; font-weight: 600; color: var(--rm-ink); }
.rm-msg {
  margin: 10px auto 0;
  max-width: 270px;
  font-size: 13px;
  line-height: 1.65;
  color: var(--rm-ink-dim);
  word-break: break-all;
}

.rm-actions {
  display: flex;
  justify-content: center;
  gap: 12px;
  margin-top: 22px;
}
.btn-ghost, .btn-solid {
  min-width: 104px;
  padding: 9px 18px;
  border-radius: 4px;
  font-size: 13px;
  font-family: var(--rm-sans);
  cursor: pointer;
  transition: filter 0.15s, color 0.15s, background 0.15s;
}
.btn-ghost {
  background: transparent;
  border: 1px solid var(--rm-line);
  color: var(--rm-ink-dim);
}
.btn-ghost:hover { color: var(--rm-ink); background: var(--rm-raised); }
.btn-solid {
  background: var(--tone);
  border: 1px solid var(--tone);
  color: #0c1424;
  font-weight: 600;
}
.btn-solid:hover { filter: brightness(1.12); }

/* 进出场：遮罩淡入 + 盒子轻微上浮放大 */
.rm-pop-enter-active { transition: opacity 0.18s ease; }
.rm-pop-leave-active { transition: opacity 0.14s ease; }
.rm-pop-enter-from, .rm-pop-leave-to { opacity: 0; }
.rm-pop-enter-active .rm-box { transition: transform 0.18s ease; }
.rm-pop-enter-from .rm-box { transform: scale(0.95) translateY(8px); }
</style>
