import { defineStore } from 'pinia'

/**
 * 同步状态类型定义
 */
export interface SyncFile {
  id: string
  name: string
  path: string
  size: number
  status: 'pending' | 'syncing' | 'synced' | 'error' | 'conflict'
  progress: number
}

export interface SyncLog {
  time: string
  level: 'info' | 'warn' | 'error'
  message: string
}

/**
 * 同步状态 Store
 * 管理文件列表、同步进度、日志
 */
export const useSyncStore = defineStore('sync', {
  state: () => ({
    isSyncing: false,
    lastSyncTime: '',
    syncProgress: 0,
    files: [] as SyncFile[],
    logs: [] as SyncLog[],
  }),
  actions: {
    /**
     * 添加日志条目
     * @param level 日志级别
     * @param message 日志内容
     */
    addLog(level: 'info' | 'warn' | 'error', message: string) {
      this.logs.push({
        time: new Date().toLocaleTimeString(),
        level,
        message,
      })
      // 限制日志数量，避免内存溢出
      if (this.logs.length > 500) {
        this.logs = this.logs.slice(-500)
      }
    },
    /**
     * 清空日志
     */
    clearLogs() {
      this.logs = []
    },
    /**
     * 更新同步状态
     */
    setSyncing(syncing: boolean) {
      this.isSyncing = syncing
      if (syncing) {
        this.syncProgress = 0
      } else {
        this.lastSyncTime = new Date().toISOString()
      }
    },
  },
})
