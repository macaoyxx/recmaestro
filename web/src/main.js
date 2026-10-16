import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './theme.css'
import App from './App.vue'

// 播控台暗色主题：EP 组件按 html.dark 变量渲染（theme.css 内覆写为墨蓝令牌）
document.documentElement.classList.add('dark')

// 路由懒加载：各页面拆为独立 chunk，首屏只加载当前页
const routes = [
  { path: '/login', component: () => import('./views/Login.vue') },
  { path: '/', component: () => import('./views/TaskList.vue') },
  { path: '/tasks/:id', component: () => import('./views/TaskDetail.vue') },
  { path: '/nodes', component: () => import('./views/NodeList.vue') },
  { path: '/stats', component: () => import('./views/Stats.vue') },
  { path: '/users', component: () => import('./views/UserList.vue') },
  { path: '/audit-logs', component: () => import('./views/AuditLog.vue') }
]

const router = createRouter({
  history: createWebHashHistory(),
  routes
})

router.beforeEach((to) => {
  const token = localStorage.getItem('token')
  if (!token && to.path !== '/login') return '/login'
  if (token && to.path === '/login') return '/'
})

const app = createApp(App)
app.use(router)
app.mount('#app')
