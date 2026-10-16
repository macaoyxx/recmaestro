<template>
  <div class="login-wrap">
    <div class="login-panel">
      <div class="login-brand">
        <span class="rm-dot rm-dot-live login-dot"></span>
        <span class="login-name">RECMAESTRO</span>
      </div>
      <div class="login-sub rm-eyebrow">直播录制调度台</div>

      <form @submit.prevent="handleLogin">
        <div class="field">
          <label class="rm-eyebrow" for="login-user">用户名</label>
          <input id="login-user" v-model="username" type="text" autocomplete="username" placeholder="admin" />
        </div>
        <div class="field">
          <label class="rm-eyebrow" for="login-pass">密码</label>
          <input id="login-pass" v-model="password" type="password" autocomplete="current-password" placeholder="••••••••" />
        </div>
        <button class="login-btn" type="submit" :disabled="loading">{{ loading ? '连接中…' : '进入控制台' }}</button>
      </form>

      <div v-if="error" class="login-error">{{ error }}</div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'

const router = useRouter()
const username = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')

async function handleLogin() {
  error.value = ''
  loading.value = true
  try {
    const resp = await api.login(username.value, password.value)
    localStorage.setItem('token', resp.data.access_token)
    localStorage.setItem('refresh_token', resp.data.refresh_token)
    localStorage.setItem('role', (resp.data.user && resp.data.user.role) || '')
    localStorage.setItem('username', (resp.data.user && resp.data.user.username) || '')
    router.push('/')
  } catch (e) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-wrap {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  /* 播控室网格底纹：极淡的静态网格，克制不抢戏 */
  background:
    linear-gradient(rgba(91, 140, 255, 0.035) 1px, transparent 1px),
    linear-gradient(90deg, rgba(91, 140, 255, 0.035) 1px, transparent 1px),
    var(--rm-bg);
  background-size: 36px 36px, 36px 36px, auto;
}

.login-panel {
  width: 360px;
  padding: 36px 32px 28px;
  background: var(--rm-panel);
  border: 1px solid var(--rm-line);
  border-radius: 6px;
}

.login-brand { display: flex; align-items: center; gap: 10px; }
.login-dot { width: 10px; height: 10px; background: var(--rm-live); }
.login-name { font-size: 15px; font-weight: 700; letter-spacing: 0.22em; }
.login-sub { margin-top: 6px; }

form { margin-top: 28px; }
.field { margin-bottom: 18px; display: flex; flex-direction: column; gap: 6px; }
.field label { font-size: 10px; }
.field input {
  background: var(--rm-deep);
  border: 1px solid var(--rm-line);
  border-radius: 4px;
  color: var(--rm-ink);
  font-size: 14px;
  font-family: var(--rm-sans);
  padding: 10px 12px;
  outline: none;
  transition: border-color 0.15s;
}
.field input:focus { border-color: var(--rm-signal); }
.field input::placeholder { color: #5a657d; }

.login-btn {
  width: 100%;
  margin-top: 6px;
  padding: 11px 0;
  background: var(--rm-signal);
  border: none;
  border-radius: 4px;
  color: #0c1424;
  font-size: 14px;
  font-weight: 700;
  font-family: var(--rm-sans);
  cursor: pointer;
  transition: filter 0.15s;
}
.login-btn:hover { filter: brightness(1.1); }
.login-btn:disabled { opacity: 0.55; cursor: wait; }

.login-error {
  margin-top: 16px;
  padding: 9px 12px;
  border-left: 2px solid var(--rm-live);
  border-radius: 0 4px 4px 0;
  background: rgba(255, 77, 94, 0.08);
  color: #ff9aa3;
  font-size: 13px;
}
</style>
