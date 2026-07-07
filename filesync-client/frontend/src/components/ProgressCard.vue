<script setup lang="ts">
/**
 * ProgressCard 进度卡片组件
 * 展示总进度：文件数统计 + 字节进度条
 */
import type { main } from '../../wailsjs/go/models'
import { computed } from 'vue'
import { NProgress, NStatistic, NCard } from 'naive-ui'

const props = defineProps<{
  progress: Record<string, main.UploadProgress>
}>()

/**
 * 汇总统计：总文件数、已完成、失败、字节进度百分比
 */
const stats = computed(() => {
  const files = Object.values(props.progress)
  const total = files.length
  const completed = files.filter((f) => ['completed', 'skipped', 'conflict'].includes(f.status)).length
  const failed = files.filter((f) => f.status === 'error').length
  const totalBytes = files.reduce((s, f) => s + f.totalBytes, 0)
  const sentBytes = files.reduce((s, f) => s + f.sentBytes, 0)
  const percent = totalBytes > 0 ? Math.round((sentBytes / totalBytes) * 100) : 0
  return { total, completed, failed, percent }
})
</script>

<template>
  <n-card title="同步进度" size="small">
    <div class="stats-row">
      <n-statistic label="总文件数" :value="stats.total" />
      <n-statistic label="已完成" :value="stats.completed" />
      <n-statistic label="失败" :value="stats.failed" />
    </div>
    <n-progress
      type="line"
      :percentage="stats.percent"
      :status="stats.failed > 0 ? 'error' : 'success'"
    />
  </n-card>
</template>

<style scoped>
.stats-row {
  display: flex;
  gap: 24px;
  margin-bottom: 16px;
}
</style>
