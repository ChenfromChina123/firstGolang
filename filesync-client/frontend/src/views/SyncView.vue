<script setup lang="ts">
/**
 * SyncView 同步状态面板
 * 顶部信息栏 + 操作区 + 进度卡片 + 文件列表/日志双栏
 * onMounted 注册 Wails 事件，onUnmounted 取消
 */
import { onMounted, onUnmounted, ref } from 'vue'
import { NCard, NButton, NSpace, NDivider, useMessage } from 'naive-ui'
import { useSyncStore } from '../stores/sync'
import { LoadConfig } from '../../wailsjs/go/main/App'
import ProgressCard from '../components/ProgressCard.vue'
import FileList from '../components/FileList.vue'
import LogPanel from '../components/LogPanel.vue'

const syncStore = useSyncStore()
const message = useMessage()

const serverURL = ref('')
const syncDir = ref('')

onMounted(async () => {
  syncStore.registerEvents()
  try {
    const cfg = await LoadConfig()
    serverURL.value = cfg.serverUrl
    syncDir.value = cfg.syncDir
  } catch (e) {
    console.error('加载配置失败:', e)
  }
})

onUnmounted(() => {
  syncStore.unregisterEvents()
})

/**
 * 触发扫描并上传
 */
async function handleScanAndUpload() {
  if (!syncDir.value) {
    message.warning('请先配置同步目录')
    return
  }
  message.info('开始扫描并上传...')
  await syncStore.startScanAndUpload()
}

/**
 * 清空日志
 */
function handleClearLogs() {
  syncStore.clearLogs()
}
</script>

<template>
  <div class="sync-view">
    <n-card size="small">
      <n-space align="center">
        <span>服务器: {{ serverURL || '未配置' }}</span>
        <n-divider vertical />
        <span>同步目录: {{ syncDir || '未配置' }}</span>
        <n-divider vertical />
        <span>上次同步: {{ syncStore.lastSyncTime || '从未' }}</span>
      </n-space>
    </n-card>

    <n-card size="small" class="action-card">
      <n-space>
        <n-button type="primary" :loading="syncStore.isSyncing" @click="handleScanAndUpload">
          扫描并上传
        </n-button>
        <n-button @click="handleClearLogs">清空日志</n-button>
      </n-space>
    </n-card>

    <ProgressCard :progress="syncStore.uploadProgress" />

    <div class="content-row">
      <div class="left-panel">
        <n-card title="文件列表" size="small">
          <FileList :progress="syncStore.uploadProgress" />
        </n-card>
      </div>
      <div class="right-panel">
        <n-card title="日志" size="small">
          <LogPanel :logs="syncStore.logs" />
        </n-card>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sync-view {
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.action-card {
  margin: 0;
}
.content-row {
  display: flex;
  gap: 12px;
}
.left-panel {
  flex: 1;
}
.right-panel {
  flex: 1;
}
</style>
