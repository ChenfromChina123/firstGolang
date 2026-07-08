package storage

import (
	"context"
	"log"
	"time"
)

// 预转码 worker 配置
const (
	// preemptScanInterval 预转码扫描频率（每小时扫描一次）。
	preemptScanInterval = 1 * time.Hour
	// preemptRecentDays 预转码近 N 天的视频（与 DB 查询参数保持一致，仅用于日志展示）。
	preemptRecentDays = 30
	// preemptStartupDelay 启动后延迟首次扫描的时间（避免启动时与用户请求竞争资源）。
	preemptStartupDelay = 5 * time.Minute
)

// preemptQualities 预转码目标画质列表。
// high 无需转码（直接走原文件），medium/low 需要提前转码以便用户请求时命中缓存。
var preemptQualities = []string{"medium", "low"}

// VideoFile 描述待预转码的视频文件信息（由 main.go 从 DB 查询后注入）。
// SrcPath 为 local 视频的绝对路径（已去 "local:" 前缀），供 ffmpeg 直接读取。
type VideoFile struct {
	FileID   string
	SrcPath  string
	Filename string
}

// StartPreemptWorker 启动预转码 worker（每小时扫描一次）。
//
// 工作流程：
//  1. 启动后等待 5 分钟（避免启动时与用户请求竞争）
//  2. 每小时检查转码队列是否为空（worker 空闲）
//  3. 队列为空时，查询近30天 local 视频列表
//  4. 对每个视频的 medium/low 画质检查 OSS 缓存是否已存在
//  5. 缓存未命中的入队 low 优先级转码任务（饥饿保护 60s 后自动升 high）
//
// 参数：
//   - cacheStore: OSS 缓存后端（nil 时直接返回，本地 MP4 模式不预转码）
//   - basePath: 存储根目录（LocalStorage.BasePath()，用于转码产物临时目录）
//   - listRecentVideos: 回调函数，返回近30天 local 视频列表（避免 storage 包依赖 store 包）
func StartPreemptWorker(cacheStore TranscodeCacheStore, basePath string, listRecentVideos func() ([]VideoFile, error)) {
	if cacheStore == nil {
		log.Printf("[Preempt] disabled: cacheStore is nil (local MP4 mode)")
		return
	}

	go func() {
		time.Sleep(preemptStartupDelay)
		preemptScanLoop(cacheStore, basePath, listRecentVideos)
	}()
	log.Printf("[Preempt] worker started: scan_interval=%v recent_days=%d", preemptScanInterval, preemptRecentDays)
}

// preemptScanLoop 预转码扫描主循环。
// 每隔 preemptScanInterval 执行一次扫描，队列非空时跳过（不抢占用户任务）。
func preemptScanLoop(cacheStore TranscodeCacheStore, basePath string, listRecentVideos func() ([]VideoFile, error)) {
	ticker := time.NewTicker(preemptScanInterval)
	defer ticker.Stop()

	for range ticker.C {
		// 队列非空时跳过（worker 忙，不抢占用户任务）
		if GetQueueSize() > 0 {
			continue
		}
		preemptScanOnce(cacheStore, basePath, listRecentVideos)
	}
}

// preemptScanOnce 执行一次预转码扫描。
// 查询近30天 local 视频，对每个视频的 medium/low 画质检查 OSS 缓存，未命中的入队 low 优先级任务。
func preemptScanOnce(cacheStore TranscodeCacheStore, basePath string, listRecentVideos func() ([]VideoFile, error)) {
	videos, err := listRecentVideos()
	if err != nil {
		log.Printf("[Preempt] list recent videos failed: %v", err)
		return
	}
	if len(videos) == 0 {
		return
	}

	enqueued := 0
	for _, v := range videos {
		for _, q := range preemptQualities {
			// 队列非空时停止入队（用户请求优先，避免预转码任务堆积）
			if GetQueueSize() > 0 {
				if enqueued > 0 {
					log.Printf("[Preempt] paused: queue not empty, enqueued %d tasks so far", enqueued)
				}
				return
			}

			// 检查 OSS 缓存是否已存在
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			exists, err := cacheStore.TranscodeExists(ctx, v.FileID, q)
			cancel()
			if err != nil {
				log.Printf("[Preempt] check cache failed id=%s q=%s err=%v", v.FileID, q, err)
				continue
			}
			if exists {
				continue // 已有缓存，跳过
			}

			// 入队 low 优先级转码任务（饥饿保护 60s 后自动升 high）
			EnqueueTranscode(transcodeRequest{
				FileID:      v.FileID,
				Quality:     q,
				SrcPath:     v.SrcPath,
				BasePath:    basePath,
				Priority:    priorityLow,
				EnqueueTime: time.Now(),
				CacheStore:  cacheStore,
				CleanupSrc:  false, // local 视频不清理源文件
			})
			enqueued++
		}
	}
	if enqueued > 0 {
		log.Printf("[Preempt] enqueued %d transcode tasks (low priority)", enqueued)
	}
}
