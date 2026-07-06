<script setup lang="ts">
/**
 * ConfigView 配置页面
 * Phase 2.2 完整实现：服务器地址、账号、同步目录、同步策略管理
 * 所有业务方法通过 Wails 绑定调用后端，前端只做展示（规则 #15）
 */
import { ref, reactive, onMounted } from 'vue'
import {
  NTabs,
  NTabPane,
  NForm,
  NFormItem,
  NInput,
  NButton,
  NCard,
  NSpace,
  NText,
  NAlert,
  NSwitch,
  NSelect,
  NInputNumber,
  NModal,
  useMessage,
} from 'naive-ui'
import {
  LoadConfig,
  SaveConfig,
  TestConnection,
  SelectDirectory,
  Login,
  Logout,
  GetCurrentUser,
  CheckAuth,
} from '../../wailsjs/go/main/App'
import type { main, auth } from '../../wailsjs/go/models'

const message = useMessage()

// 配置数据
const config = ref<main.Config | null>(null)

// 当前用户
const currentUser = ref<auth.UserInfo | null>(null)

// 服务器地址编辑
const serverUrlInput = ref('')
const testing = ref(false)
const testResult = ref<{ ok: boolean; msg: string } | null>(null)

// 同步目录编辑
const syncDirInput = ref('')

// 重新登录弹窗
const showLoginModal = ref(false)
const loginForm = reactive({
  username: '',
  password: '',
})
const loggingIn = ref(false)

// 同步策略选项
const strategyOptions = [
  { label: '每次询问', value: 'ask' },
  { label: '总是上传（本地优先）', value: 'always_upload' },
  { label: '总是下载（服务器优先）', value: 'always_download' },
]

// 保存中状态
const saving = ref(false)

/**
 * 加载配置和用户信息
 */
async function loadData() {
  try {
    config.value = await LoadConfig()
    serverUrlInput.value = config.value.serverUrl
    syncDirInput.value = config.value.syncDir
    currentUser.value = await GetCurrentUser()
  } catch (e) {
    message.error('加载配置失败: ' + String(e))
  }
}

/**
 * 测试服务器连接
 */
async function handleTestConnection() {
  if (!serverUrlInput.value) {
    message.warning('请输入服务器地址')
    return
  }
  testing.value = true
  testResult.value = null
  try {
    const result = await TestConnection(serverUrlInput.value)
    testResult.value = { ok: result.ok, msg: result.msg }
    if (result.ok) {
      message.success(result.msg)
    } else {
      message.error(result.msg)
    }
  } catch (e) {
    testResult.value = { ok: false, msg: String(e) }
    message.error('测试失败: ' + String(e))
  } finally {
    testing.value = false
  }
}

/**
 * 保存服务器地址
 */
async function saveServerUrl() {
  if (!config.value) return
  if (!serverUrlInput.value) {
    message.warning('服务器地址不能为空')
    return
  }
  saving.value = true
  try {
    const oldUrl = config.value.serverUrl
    config.value.serverUrl = serverUrlInput.value
    await SaveConfig(config.value)
    if (oldUrl !== serverUrlInput.value) {
      currentUser.value = null
      message.success('服务器地址已更新，请重新登录')
    } else {
      message.success('服务器地址已保存')
    }
  } catch (e) {
    message.error('保存失败: ' + String(e))
  } finally {
    saving.value = false
  }
}

/**
 * 选择同步目录
 */
async function handleSelectDir() {
  try {
    const dir = await SelectDirectory()
    if (dir) {
      syncDirInput.value = dir
    }
  } catch (e) {
    message.error('选择目录失败: ' + String(e))
  }
}

/**
 * 保存同步目录
 */
async function saveSyncDir() {
  if (!config.value) return
  if (!syncDirInput.value) {
    message.warning('请选择同步目录')
    return
  }
  saving.value = true
  try {
    config.value.syncDir = syncDirInput.value
    await SaveConfig(config.value)
    message.success('同步目录已保存')
  } catch (e) {
    message.error('保存失败: ' + String(e))
  } finally {
    saving.value = false
  }
}

/**
 * 保存同步策略
 */
async function saveSyncStrategy() {
  if (!config.value) return
  saving.value = true
  try {
    await SaveConfig(config.value)
    message.success('同步策略已保存')
  } catch (e) {
    message.error('保存失败: ' + String(e))
  } finally {
    saving.value = false
  }
}

/**
 * 打开重新登录弹窗
 */
function openLoginModal() {
  loginForm.username = config.value?.username || ''
  loginForm.password = ''
  showLoginModal.value = true
}

/**
 * 执行登录
 */
async function handleLogin() {
  if (!loginForm.username || !loginForm.password) {
    message.warning('请输入用户名和密码')
    return
  }
  loggingIn.value = true
  try {
    await Login(loginForm.username, loginForm.password)
    currentUser.value = await GetCurrentUser()
    if (config.value) {
      config.value.username = loginForm.username
      await SaveConfig(config.value)
    }
    message.success('登录成功')
    showLoginModal.value = false
  } catch (e) {
    const errStr = String(e)
    if (errStr.includes('用户名或密码错误')) {
      message.error('用户名或密码错误')
    } else if (errStr.includes('账号未激活')) {
      message.error('账号未激活，请查收激活邮件后激活账号')
    } else if (errStr.includes('无法连接服务器')) {
      message.error('无法连接服务器，请检查服务器地址')
    } else {
      message.error('登录失败: ' + errStr)
    }
  } finally {
    loggingIn.value = false
  }
}

/**
 * 登出
 */
async function handleLogout() {
  try {
    await Logout()
    currentUser.value = null
    message.success('已登出')
  } catch (e) {
    message.error('登出失败: ' + String(e))
  }
}

onMounted(() => {
  loadData()
})
</script>

<template>
  <div class="config-view">
    <n-card title="配置管理" :bordered="false">
      <n-tabs type="line" animated>
        <!-- 服务器标签页 -->
        <n-tab-pane name="server" tab="服务器">
          <n-form label-placement="top">
            <n-form-item label="服务器地址">
              <n-input
                v-model:value="serverUrlInput"
                placeholder="https://aistudy.icu 或 http://localhost:8080"
                clearable
              />
            </n-form-item>
            <n-form-item>
              <n-space>
                <n-button :loading="testing" @click="handleTestConnection">
                  测试连接
                </n-button>
                <n-button
                  type="primary"
                  :loading="saving"
                  @click="saveServerUrl"
                >
                  保存
                </n-button>
              </n-space>
            </n-form-item>
            <n-alert
              v-if="testResult"
              :type="testResult.ok ? 'success' : 'error'"
              :title="testResult.ok ? '连接成功' : '连接失败'"
              style="margin-top: 8px"
            >
              {{ testResult.msg }}
            </n-alert>
          </n-form>
        </n-tab-pane>

        <!-- 账号标签页 -->
        <n-tab-pane name="account" tab="账号">
          <n-space vertical :size="16">
            <div v-if="currentUser">
              <n-text>当前用户: </n-text>
              <n-text strong>{{ currentUser.username }}</n-text>
              <n-text depth="3"> ({{ currentUser.role }})</n-text>
            </div>
            <n-text v-else depth="3">未登录</n-text>
            <n-space>
              <n-button
                v-if="!currentUser"
                type="primary"
                @click="openLoginModal"
              >
                登录
              </n-button>
              <n-button v-else @click="openLoginModal">
                重新登录
              </n-button>
              <n-button
                v-if="currentUser"
                type="warning"
                @click="handleLogout"
              >
                登出
              </n-button>
            </n-space>
          </n-space>
        </n-tab-pane>

        <!-- 同步目录标签页 -->
        <n-tab-pane name="syncdir" tab="同步目录">
          <n-form label-placement="top">
            <n-form-item label="本地同步目录">
              <n-space align="center">
                <n-input
                  v-model:value="syncDirInput"
                  placeholder="点击右侧按钮选择目录"
                  readonly
                  style="width: 400px"
                />
                <n-button @click="handleSelectDir">
                  选择目录
                </n-button>
              </n-space>
            </n-form-item>
            <n-form-item>
              <n-button
                type="primary"
                :loading="saving"
                @click="saveSyncDir"
              >
                保存
              </n-button>
            </n-form-item>
            <n-text depth="3" style="font-size: 12px">
              该目录下的文件将与服务器保持同步。建议选择专用目录。
            </n-text>
          </n-form>
        </n-tab-pane>

        <!-- 同步策略标签页 -->
        <n-tab-pane name="strategy" tab="同步策略">
          <n-form label-placement="top" v-if="config">
            <n-form-item label="冲突解决策略">
              <n-select
                v-model:value="config.syncStrategy"
                :options="strategyOptions"
                style="width: 300px"
              />
            </n-form-item>
            <n-form-item label="自动同步">
              <n-switch v-model:value="config.autoSync" />
            </n-form-item>
            <n-form-item label="同步间隔（秒）">
              <n-input-number
                v-model:value="config.syncInterval"
                :min="10"
                :max="3600"
                style="width: 200px"
              />
            </n-form-item>
            <n-form-item>
              <n-button
                type="primary"
                :loading="saving"
                @click="saveSyncStrategy"
              >
                保存
              </n-button>
            </n-form-item>
          </n-form>
        </n-tab-pane>
      </n-tabs>
    </n-card>

    <!-- 重新登录弹窗 -->
    <n-modal
      v-model:show="showLoginModal"
      preset="dialog"
      title="登录服务器"
      :show-icon="false"
      style="width: 420px"
    >
      <n-form label-placement="top" style="margin-top: 16px">
        <n-form-item label="用户名">
          <n-input
            v-model:value="loginForm.username"
            placeholder="请输入用户名"
            clearable
          />
        </n-form-item>
        <n-form-item label="密码">
          <n-input
            v-model:value="loginForm.password"
            type="password"
            show-password-on="click"
            placeholder="请输入密码"
            @keyup.enter="handleLogin"
          />
        </n-form-item>
      </n-form>
      <template #action>
        <n-space>
          <n-button @click="showLoginModal = false">取消</n-button>
          <n-button
            type="primary"
            :loading="loggingIn"
            @click="handleLogin"
          >
            登录
          </n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<style scoped>
.config-view {
  padding: 24px;
}
</style>
