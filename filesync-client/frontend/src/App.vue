<script setup lang="ts">
/**
 * App 根组件
 * 首次启动显示配置向导，否则显示主界面（n-tabs 切换同步/配置）
 */
import { ref, onMounted } from 'vue'
import { NConfigProvider, NMessageProvider, NDialogProvider, NTabs, NTabPane, zhCN, dateZhCN } from 'naive-ui'
import WizardView from './views/WizardView.vue'
import SyncView from './views/SyncView.vue'
import ConfigView from './views/ConfigView.vue'
import { IsFirstRun } from '../wailsjs/go/main/App'

// 是否首次启动
const isFirstRun = ref(true)
// 是否已加载
const loaded = ref(false)
// 当前激活的标签页
const activeTab = ref('sync')

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
        <n-tabs v-else v-model:value="activeTab" type="line" animated>
          <n-tab-pane name="sync" tab="同步">
            <SyncView />
          </n-tab-pane>
          <n-tab-pane name="config" tab="配置">
            <ConfigView />
          </n-tab-pane>
        </n-tabs>
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
