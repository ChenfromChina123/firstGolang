package storage

import (
	"context"
	"log"
	"strings"
	"time"
)

// 缓存清理 worker 配置
const (
	// cleanerScanInterval 清理 worker 扫描频率（每6小时扫描一次）。
	cleanerScanInterval = 6 * time.Hour
	// cleanerMaxAge 转码产物最大保留时间（7天未重新生成则清理）。
	// 由于没有访问日志，用"生成时间"近似"最后访问时间"。
	// 7天后清理，用户再次访问会触发重新转码（预转码 worker 会定期补充）。
	cleanerMaxAge = 7 * 24 * time.Hour
	// cleanerStartupDelay 启动后延迟首次扫描的时间（避免启动时负载峰值）。
	cleanerStartupDelay = 10 * time.Minute
	// cleanerScanTimeout 单次扫描的超时时间（列举 + 删除）。
	cleanerScanTimeout = 5 * time.Minute
)

// StartCacheCleaner 启动 OSS 转码缓存清理 worker。
//
// 工作流程：
//  1. 启动后等待 10 分钟（避免启动时负载峰值）
//  2. 每6小时扫描一次 OSS transcoded/ 前缀
//  3. 按 fileID+quality 分组，取 m3u8 的 LastModified 作为"最后生成时间"
//  4. 超过7天的组删除全部产物（m3u8 + 所有 ts 切片）
//
// 参数：
//   - cacheStore: OSS 缓存后端（nil 时直接返回）
func StartCacheCleaner(cacheStore TranscodeCacheStore) {
	if cacheStore == nil {
		log.Printf("[Cleaner] disabled: cacheStore is nil")
		return
	}

	go func() {
		time.Sleep(cleanerStartupDelay)
		cleanerScanLoop(cacheStore)
	}()
	log.Printf("[Cleaner] worker started: scan_interval=%v max_age=%v", cleanerScanInterval, cleanerMaxAge)
}

// cleanerScanLoop 清理 worker 主循环。
// 每隔 cleanerScanInterval 执行一次扫描。
func cleanerScanLoop(cacheStore TranscodeCacheStore) {
	ticker := time.NewTicker(cleanerScanInterval)
	defer ticker.Stop()

	for range ticker.C {
		cleanerScanOnce(cacheStore)
	}
}

// cleanerScanOnce 执行一次清理扫描。
// 列举 OSS transcoded/ 前缀所有对象，按 fileID+quality 分组，
// m3u8 的 LastModified 超过7天则删除该组的全部产物。
func cleanerScanOnce(cacheStore TranscodeCacheStore) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanerScanTimeout)
	defer cancel()

	// 列举所有转码产物
	objects, err := cacheStore.ListTranscodeByPrefix(ctx, HLSTranscodeRootPrefix())
	if err != nil {
		log.Printf("[Cleaner] list transcode objects failed: %v", err)
		return
	}

	// 按 fileID:quality 分组，记录每组的 m3u8 LastModified
	type groupInfo struct {
		fileID       string
		quality      string
		lastModified time.Time
	}
	groups := make(map[string]*groupInfo)

	for _, obj := range objects {
		if obj.FileID == "" || obj.Quality == "" {
			continue
		}
		// 只关注 m3u8 文件的 LastModified（标志转码完成时间）
		if !strings.HasSuffix(obj.Key, "playlist.m3u8") {
			continue
		}
		key := obj.FileID + ":" + obj.Quality
		g, ok := groups[key]
		if !ok || obj.LastModified.After(g.lastModified) {
			groups[key] = &groupInfo{
				fileID:       obj.FileID,
				quality:      obj.Quality,
				lastModified: obj.LastModified,
			}
		}
	}

	if len(groups) == 0 {
		return
	}

	// 清理超过7天的组
	now := time.Now()
	cleaned := 0
	for _, g := range groups {
		if now.Sub(g.lastModified) <= cleanerMaxAge {
			continue
		}
		log.Printf("[Cleaner] deleting stale transcode: fileID=%s quality=%s age=%v",
			g.fileID, g.quality, now.Sub(g.lastModified))
		if err := cacheStore.DeleteTranscode(ctx, g.fileID, g.quality); err != nil {
			log.Printf("[Cleaner] delete failed: fileID=%s quality=%s err=%v",
				g.fileID, g.quality, err)
			continue
		}
		cleaned++
	}

	if cleaned > 0 {
		log.Printf("[Cleaner] cleaned %d stale transcode groups", cleaned)
	}
}
