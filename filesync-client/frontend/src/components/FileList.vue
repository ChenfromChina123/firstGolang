<script setup lang="ts">
/**
 * FileList 文件列表组件
 * 展示上传进度中的文件列表，含状态标签、文件名、大小、分片进度
 */
import type { main } from '../../wailsjs/go/models'
import { computed } from 'vue'
import { NEmpty, NTag, NText } from 'naive-ui'

const props = defineProps<{
  progress: Record<string, main.UploadProgress>
}>()

const files = computed(() => Object.values(props.progress))

/**
 * 根据状态返回标签类型和文案
 */
function statusTag(status: string): { type: 'default' | 'info' | 'success' | 'warning' | 'error'; label: string } {
  const map: Record<string, { type: 'default' | 'info' | 'success' | 'warning' | 'error'; label: string }> = {
    pending: { type: 'default', label: '等待' },
    hashing: { type: 'info', label: '计算哈希' },
    checking: { type: 'info', label: '秒传检查' },
    uploading: { type: 'info', label: '上传中' },
    completed: { type: 'success', label: '完成' },
    error: { type: 'error', label: '失败' },
    skipped: { type: 'warning', label: '跳过' },
    conflict: { type: 'warning', label: '冲突' },
  }
  return map[status] || { type: 'default', label: status }
}

/**
 * 格式化文件大小
 */
function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(2)} MB`
}
</script>

<template>
  <div class="file-list">
    <n-empty v-if="files.length === 0" description="暂无文件" />
    <div v-else class="file-items">
      <div v-for="f in files" :key="f.filename" class="file-item">
        <div class="file-info">
          <span class="filename">{{ f.filename }}</span>
          <span class="size">{{ formatSize(f.totalBytes) }}</span>
        </div>
        <n-tag :type="statusTag(f.status).type" size="small">
          {{ statusTag(f.status).label }}
        </n-tag>
        <div v-if="f.status === 'uploading'" class="progress-info">
          {{ f.sentChunks }}/{{ f.totalChunks }} 分片
        </div>
        <n-text v-if="f.error" type="error" depth="2" class="error-msg">
          {{ f.error }}
        </n-text>
      </div>
    </div>
  </div>
</template>

<style scoped>
.file-list {
  padding: 12px;
  max-height: 400px;
  overflow-y: auto;
}
.file-item {
  padding: 8px 0;
  border-bottom: 1px solid #f0f0f0;
}
.file-info {
  display: flex;
  justify-content: space-between;
  margin-bottom: 4px;
}
.filename {
  font-size: 13px;
}
.size {
  font-size: 12px;
  color: #999;
}
.progress-info {
  font-size: 12px;
  color: #666;
  margin-top: 4px;
}
.error-msg {
  font-size: 12px;
  display: block;
  margin-top: 4px;
}
</style>
