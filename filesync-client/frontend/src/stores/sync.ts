import { defineStore } from 'pinia'
import type { main } from '../../wailsjs/go/models'
import { ScanAndUpload, UploadFile, GetUploadProgress } from '../../wailsjs/go/main/App'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'

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
 * 管理文件列表、同步进度、日志、上传进度、扫描结果
 * 通过 Wails 事件接收后端推送的进度更新
 */
export const useSyncStore = defineStore('sync', {
  state: () => ({
    isSyncing: false,
    lastSyncTime: '',
    syncProgress: 0,
    files: [] as SyncFile[],
    logs: [] as SyncLog[],
    uploadProgress: {} as Record<string, main.UploadProgress>,
    scanResult: [] as Array<{ rel_path: string; abs_path: string; size: number }>,
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
    /**
     * 更新单个文件的上传进度（upload:progress 事件触发）
     */
    updateProgress(p: main.UploadProgress) {
      this.uploadProgress[p.filename] = p
    },
    /**
     * 处理单个文件上传完成（upload:complete 事件触发）
     */
    handleComplete(p: main.UploadProgress) {
      this.uploadProgress[p.filename] = p
      this.addLog('info', `文件 ${p.filename} 上传完成（${p.status}）`)
    },
    /**
     * 处理单个文件上传失败（upload:error 事件触发）
     */
    handleError(data: { filename: string; error: string }) {
      this.addLog('error', `文件 ${data.filename} 上传失败: ${data.error}`)
    },
    /**
     * 处理扫描结果（upload:scan 事件触发）
     */
    handleScan(files: Array<{ rel_path: string; abs_path: string; size: number }>) {
      this.scanResult = files
      this.addLog('info', `扫描完成，待上传 ${files.length} 个文件`)
    },
    /**
     * 启动扫描并上传全流程
     */
    async startScanAndUpload() {
      this.setSyncing(true)
      try {
        await ScanAndUpload()
        this.addLog('info', '同步完成')
      } catch (e) {
        this.addLog('error', `同步失败: ${String(e)}`)
      } finally {
        this.setSyncing(false)
      }
    },
    /**
     * 上传单个文件
     */
    async startUploadFile(absPath: string) {
      try {
        await UploadFile(absPath)
      } catch (e) {
        this.addLog('error', `上传失败: ${String(e)}`)
      }
    },
    /**
     * 主动刷新进度（事件丢失时兜底）
     */
    async refreshProgress() {
      try {
        this.uploadProgress = await GetUploadProgress()
      } catch (e) {
        console.error('刷新进度失败:', e)
      }
    },
    /**
     * 注册 Wails 事件监听（SyncView onMounted 调用）
     */
    registerEvents() {
      EventsOn('upload:progress', (p: main.UploadProgress) => this.updateProgress(p))
      EventsOn('upload:complete', (p: main.UploadProgress) => this.handleComplete(p))
      EventsOn('upload:error', (data: { filename: string; error: string }) => this.handleError(data))
      EventsOn('upload:scan', (files: Array<{ rel_path: string; abs_path: string; size: number }>) => this.handleScan(files))
    },
    /**
     * 取消 Wails 事件监听（SyncView onUnmounted 调用）
     */
    unregisterEvents() {
      EventsOff('upload:progress')
      EventsOff('upload:complete')
      EventsOff('upload:error')
      EventsOff('upload:scan')
    },
  },
})
