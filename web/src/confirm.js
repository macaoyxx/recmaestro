// rmConfirm —— 全局公共确认弹窗（播控台「信号灯」语言）。
// 用法：const ok = await rmConfirm({ title, message, tone, confirmText })
//   tone: 'danger'(录制红) | 'warn'(琥珀) | 'signal'(信号蓝)，默认 danger
//   返回 Promise<boolean>：true=确认，false=取消/ESC/点遮罩/点叉叉
// App.vue 挂载 <RmConfirm /> 一份，任意组件直接调用，替代 ElMessageBox.confirm。
import { reactive } from 'vue'

export const confirmState = reactive({
  open: false,
  title: '',
  message: '',
  tone: 'danger',
  confirmText: '确认',
  cancelText: '取消'
})

let resolver = null

export function rmConfirm(opts = {}) {
  return new Promise((resolve) => {
    resolver = resolve
    Object.assign(confirmState, {
      open: true,
      tone: 'danger',
      confirmText: '确认',
      cancelText: '取消',
      ...opts
    })
  })
}

export function settleConfirm(value) {
  confirmState.open = false
  if (resolver) {
    resolver(value)
    resolver = null
  }
}
