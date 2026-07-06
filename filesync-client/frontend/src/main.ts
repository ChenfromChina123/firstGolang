import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import './style.css'

/**
 * 应用入口
 * 注册 Vue + Pinia 状态管理
 * Naive UI 采用按需引入方式（在组件中直接 import），避免全量打包
 */
const app = createApp(App)
app.use(createPinia())
app.mount('#app')
