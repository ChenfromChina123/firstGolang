<script setup lang="ts">
/**
 * App 根组件
 * 根据是否首次启动，显示配置向导或主界面
 */
import { ref, onMounted } from 'vue'
import { NConfigProvider, NMessageProvider, NDialogProvider, zhCN, dateZhCN } from 'naive-ui'
import WizardView from './views/WizardView.vue'
import SyncView from './views/SyncView.vue'
import { IsFirstRun } from '../wailsjs/go/main/App'

// 是否首次启动
const isFirstRun = ref(true)
// 是否已加载
const loaded = ref(false)

onMounted(async () => {
  try {
    isFirstRun.value = await IsFirstRun()
  } catch (e) {
    console.error('检查首次启动失败:', e)
    isFirstRun.value = true
  } finally {
    loaded.value = true
  }
})

/**
 * 向导完成回调
 */
function handleWizardFinish() {
  isFirstRun.value = false
}
</script>

<template>
  <n-config-provider :locale="zhCN" :date-locale="dateZhCN">
    <n-message-provider>
      <n-dialog-provider>
        <div v-if="!loaded" class="loading">
          <p>加载中...</p>
        </div>
        <WizardView
          v-else-if="isFirstRun"
          @finish="handleWizardFinish"
        />
        <SyncView v-else />
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>

<style>
/* 全局样式 */
body {
  margin: 0;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC',
    'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
}

.loading {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  color: #666;
}
</style>
