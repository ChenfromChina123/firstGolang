<script setup lang="ts">
/**
 * LogPanel 日志面板组件
 * 实时展示同步日志，按级别着色，自动滚动到底部
 */
import { ref, watch, nextTick } from 'vue'
import { NEmpty, NTag } from 'naive-ui'
import type { SyncLog } from '../stores/sync'

const props = defineProps<{
  logs: SyncLog[]
}>()

const containerRef = ref<HTMLElement | null>(null)

/**
 * 日志条数变化时自动滚动到底部
 */
watch(
  () => props.logs.length,
  async () => {
    await nextTick()
    if (containerRef.value) {
      containerRef.value.scrollTop = containerRef.value.scrollHeight
    }
  },
)

/**
 * 根据日志级别返回标签类型
 */
function tagType(level: string): 'error' | 'warning' | 'info' {
  if (level === 'error') return 'error'
  if (level === 'warn') return 'warning'
  return 'info'
}
</script>

<template>
  <div class="log-panel">
    <div class="log-container" ref="containerRef">
      <n-empty v-if="logs.length === 0" description="暂无日志" size="small" />
      <div v-for="(log, i) in logs" :key="i" class="log-item">
        <span class="time">{{ log.time }}</span>
        <n-tag :type="tagType(log.level)" size="tiny">{{ log.level }}</n-tag>
        <span class="message">{{ log.message }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.log-panel {
  padding: 12px;
}
.log-container {
  max-height: 300px;
  overflow-y: auto;
  font-family: monospace;
}
.log-item {
  display: flex;
  gap: 8px;
  padding: 4px 0;
  font-size: 12px;
  align-items: center;
}
.time {
  color: #999;
}
.message {
  flex: 1;
  word-break: break-all;
}
</style>
