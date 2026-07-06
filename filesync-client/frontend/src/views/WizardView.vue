<script setup lang="ts">
/**
 * WizardView 首次启动向导
 * 5 步流程：欢迎 → 服务器配置 → 登录 → 选择同步目录 → 完成
 * 配置保存后进入主界面
 */
import { ref, reactive } from 'vue'
import {
  NSteps,
  NStep,
  NForm,
  NFormItem,
  NInput,
  NButton,
  NCard,
  NSpace,
  NIcon,
  NAlert,
  NText,
  useMessage,
} from 'naive-ui'
import {
  CloudOutline,
  ServerOutline,
  PersonOutline,
  FolderOpenOutline,
  CheckmarkCircleOutline,
} from '@vicons/ionicons5'
import { SelectDirectory, LoadConfig, SaveConfig, TestConnection } from '../../wailsjs/go/main/App'

const message = useMessage()

// 当前步骤（0-4）
const currentStep = ref(0)

// 表单数据
const form = reactive({
  serverUrl: 'http://localhost:8080',
  username: '',
  password: '',
  syncDir: '',
})

// 连接测试状态
const testing = ref(false)
const testResult = ref<{ ok: boolean; msg: string } | null>(null)

// 保存中状态
const saving = ref(false)

/**
 * 测试服务器连接
 */
async function handleTestConnection() {
  if (!form.serverUrl) {
    message.warning('请输入服务器地址')
    return
  }
  testing.value = true
  testResult.value = null
  try {
    const result = await TestConnection(form.serverUrl)
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
 * 选择同步目录
 */
async function handleSelectDir() {
  try {
    const dir = await SelectDirectory()
    if (dir) {
      form.syncDir = dir
    }
  } catch (e) {
    message.error('选择目录失败: ' + String(e))
  }
}

/**
 * 下一步
 */
function next() {
  if (currentStep.value === 1 && !form.serverUrl) {
    message.warning('请输入服务器地址')
    return
  }
  if (currentStep.value === 2 && (!form.username || !form.password)) {
    message.warning('请输入用户名和密码')
    return
  }
  if (currentStep.value === 3 && !form.syncDir) {
    message.warning('请选择同步目录')
    return
  }
  currentStep.value++
}

/**
 * 上一步
 */
function prev() {
  if (currentStep.value > 0) {
    currentStep.value--
  }
}

/**
 * 完成向导，保存配置
 */
async function finish() {
  saving.value = true
  try {
    const cfg = await LoadConfig()
    cfg.serverUrl = form.serverUrl
    cfg.username = form.username
    cfg.syncDir = form.syncDir
    await SaveConfig(cfg)
    message.success('配置已保存，即将进入主界面')
    // 通知父组件向导完成
    emit('finish')
  } catch (e) {
    message.error('保存配置失败: ' + String(e))
  } finally {
    saving.value = false
  }
}

const emit = defineEmits<{ (e: 'finish'): void }>()
</script>

<template>
  <div class="wizard-container">
    <n-card class="wizard-card" :bordered="false">
      <template #header>
        <div class="wizard-header">
          <n-icon size="32" color="#10b981">
            <CloudOutline />
          </n-icon>
          <n-text style="font-size: 20px; font-weight: 600">FileSync 客户端配置向导</n-text>
        </div>
      </template>

      <n-steps :current="currentStep + 1" class="wizard-steps">
        <n-step title="欢迎">
          <template #icon>
            <n-icon><CloudOutline /></n-icon>
          </template>
        </n-step>
        <n-step title="服务器">
          <template #icon>
            <n-icon><ServerOutline /></n-icon>
          </template>
        </n-step>
        <n-step title="登录">
          <template #icon>
            <n-icon><PersonOutline /></n-icon>
          </template>
        </n-step>
        <n-step title="同步目录">
          <template #icon>
            <n-icon><FolderOpenOutline /></n-icon>
          </template>
        </n-step>
        <n-step title="完成">
          <template #icon>
            <n-icon><CheckmarkCircleOutline /></n-icon>
          </template>
        </n-step>
      </n-steps>

      <div class="wizard-content">
        <!-- 步骤 1: 欢迎 -->
        <div v-if="currentStep === 0" class="step-pane">
          <n-space vertical :size="16">
            <h2 style="margin: 0">欢迎使用 FileSync 桌面客户端</h2>
            <p class="step-desc">
              本向导将引导你完成客户端的初始配置。配置完成后，客户端将自动在本地目录与服务器之间同步文件。
            </p>
            <n-space vertical :size="8">
              <n-text depth="3">配置流程：</n-text>
              <n-text depth="3">1. 设置服务器地址并测试连接</n-text>
              <n-text depth="3">2. 输入登录账号密码</n-text>
              <n-text depth="3">3. 选择本地同步目录</n-text>
              <n-text depth="3">4. 保存配置并进入主界面</n-text>
            </n-space>
          </n-space>
        </div>

        <!-- 步骤 2: 服务器配置 -->
        <div v-if="currentStep === 1" class="step-pane">
          <n-form label-placement="top">
            <n-form-item label="服务器地址">
              <n-input
                v-model:value="form.serverUrl"
                placeholder="https://aistudy.icu 或 http://localhost:8080"
                clearable
              />
            </n-form-item>
            <n-form-item>
              <n-button :loading="testing" @click="handleTestConnection">
                测试连接
              </n-button>
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
        </div>

        <!-- 步骤 3: 登录 -->
        <div v-if="currentStep === 2" class="step-pane">
          <n-form label-placement="top">
            <n-form-item label="用户名">
              <n-input
                v-model:value="form.username"
                placeholder="请输入用户名"
                clearable
              />
            </n-form-item>
            <n-form-item label="密码">
              <n-input
                v-model:value="form.password"
                type="password"
                show-password-on="click"
                placeholder="请输入密码"
              />
            </n-form-item>
            <n-text depth="3" style="font-size: 12px">
              登录功能将在 Phase 2.2 完整实现，此处仅记录账号信息。
            </n-text>
          </n-form>
        </div>

        <!-- 步骤 4: 选择同步目录 -->
        <div v-if="currentStep === 3" class="step-pane">
          <n-form label-placement="top">
            <n-form-item label="本地同步目录">
              <n-space align="center">
                <n-input
                  v-model:value="form.syncDir"
                  placeholder="点击右侧按钮选择目录"
                  readonly
                  style="width: 400px"
                />
                <n-button @click="handleSelectDir">
                  选择目录
                </n-button>
              </n-space>
            </n-form-item>
            <n-text depth="3" style="font-size: 12px">
              该目录下的文件将与服务器保持同步。建议选择专用空目录。
            </n-text>
          </n-form>
        </div>

        <!-- 步骤 5: 完成 -->
        <div v-if="currentStep === 4" class="step-pane">
          <n-space vertical :size="16">
            <n-icon size="48" color="#10b981">
              <CheckmarkCircleOutline />
            </n-icon>
            <h2 style="margin: 0">配置完成</h2>
            <n-space vertical :size="4">
              <n-text>服务器地址: {{ form.serverUrl }}</n-text>
              <n-text>用户名: {{ form.username }}</n-text>
              <n-text>同步目录: {{ form.syncDir }}</n-text>
            </n-space>
            <n-text depth="3">
              点击"完成"保存配置并进入主界面。
            </n-text>
          </n-space>
        </div>
      </div>

      <template #footer>
        <div class="wizard-footer">
          <n-button v-if="currentStep > 0" @click="prev">
            上一步
          </n-button>
          <n-button
            v-if="currentStep < 4"
            type="primary"
            @click="next"
          >
            下一步
          </n-button>
          <n-button
            v-if="currentStep === 4"
            type="primary"
            :loading="saving"
            @click="finish"
          >
            完成
          </n-button>
        </div>
      </template>
    </n-card>
  </div>
</template>

<style scoped>
.wizard-container {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: 24px;
  background: #f5f5f5;
}

.wizard-card {
  width: 100%;
  max-width: 720px;
  border-radius: 8px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.08);
}

.wizard-header {
  display: flex;
  align-items: center;
  gap: 12px;
}

.wizard-steps {
  margin-bottom: 32px;
}

.wizard-content {
  min-height: 240px;
  padding: 16px 0;
}

.step-pane {
  animation: fade-in 0.3s ease;
}

.step-desc {
  color: #666;
  line-height: 1.6;
  margin: 0;
}

.wizard-footer {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

@keyframes fade-in {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
