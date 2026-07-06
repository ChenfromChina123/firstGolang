/**
 * auth store 管理客户端认证状态。
 * 所有业务方法通过 Wails 绑定调用后端，前端只做状态展示（规则 #15）。
 */
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { Login, Logout, GetCurrentUser, CheckAuth } from '../../wailsjs/go/main/App'
import type { auth } from '../../wailsjs/go/models'

export const useAuthStore = defineStore('auth', () => {
  const isAuthenticated = ref(false)
  const user = ref<auth.UserInfo | null>(null)
  const loading = ref(false)

  /**
   * 登录服务器。
   * 成功后更新 isAuthenticated 和 user 状态。
   */
  async function login(username: string, password: string) {
    loading.value = true
    try {
      await Login(username, password)
      await fetchUser()
      isAuthenticated.value = true
    } finally {
      loading.value = false
    }
  }

  /**
   * 登出服务器。
   * 清除本地认证状态。
   */
  async function logout() {
    loading.value = true
    try {
      await Logout()
    } finally {
      isAuthenticated.value = false
      user.value = null
      loading.value = false
    }
  }

  /**
   * 获取当前用户信息（从后端缓存读取）。
   */
  async function fetchUser() {
    const u = await GetCurrentUser()
    user.value = u
    isAuthenticated.value = u !== null
  }

  /**
   * 检查认证状态（通过 /api/me 探测服务器）。
   */
  async function checkAuth() {
    loading.value = true
    try {
      isAuthenticated.value = await CheckAuth()
      if (isAuthenticated.value) {
        await fetchUser()
      }
      return isAuthenticated.value
    } finally {
      loading.value = false
    }
  }

  return {
    isAuthenticated,
    user,
    loading,
    login,
    logout,
    fetchUser,
    checkAuth,
  }
})
