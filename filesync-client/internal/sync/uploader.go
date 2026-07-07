package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"filesync-client/internal/api"
	"filesync-client/internal/config"
)

const (
	defaultChunkSize   = 512 * 1024 // 默认分片大小 512KB，与服务器一致
	defaultConcurrency = 3          // 默认并发上传分片数
	maxChunkRetries    = 3          // 单分片上传失败最大重试次数
)

// UploadProgress 单个文件的上传进度（通过 Wails 事件推送给前端）
type UploadProgress struct {
	Filename    string `json:"filename"`
	TotalBytes  int64  `json:"total_bytes"`
	SentBytes   int64  `json:"sent_bytes"`
	TotalChunks int    `json:"total_chunks"`
	SentChunks  int    `json:"sent_chunks"`
	Status      string `json:"status"` // pending/hashing/checking/uploading/completed/error/skipped/conflict
	Error       string `json:"error,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

// Uploader 上传编排器。
// 协调 API Client 完成单文件上传全流程，管理进度状态，通过 Wails 事件推送进度。
type Uploader struct {
	ctx         context.Context // Wails 上下文，用于 EventEmit
	apiClient   *api.Client
	cfg         *config.Config
	chunkSize   int64
	concurrency int

	mu       sync.RWMutex
	progress map[string]*UploadProgress // key: filename（相对路径）
}

// NewUploader 创建上传编排器实例。
// ctx 为 Wails 上下文（app.startup 注入），用于 EventEmit。
func NewUploader(ctx context.Context, apiClient *api.Client, cfg *config.Config) *Uploader {
	return &Uploader{
		ctx:         ctx,
		apiClient:   apiClient,
		cfg:         cfg,
		chunkSize:   defaultChunkSize,
		concurrency: defaultConcurrency,
		progress:    make(map[string]*UploadProgress),
	}
}

// UpdateConfig 更新配置（SaveConfig 后调用，同步 SyncDir/SyncStrategy 变更）
func (u *Uploader) UpdateConfig(cfg *config.Config) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cfg = cfg
}

// UploadFile 上传单个文件（绝对路径）。
// 完整流程：打开文件 → 计算 SHA256 → CheckUpload 秒传 → InitUpload → GetUploadStatus 断点续传 → 并发 UploadChunk → CompleteUpload
// 进度通过 upload:progress / upload:complete / upload:error 事件推送。
func (u *Uploader) UploadFile(absPath string) error {
	// 获取文件信息
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("访问文件失败: %w", err)
	}

	// 确定相对路径作为 filename
	filename := filepath.Base(absPath)
	if u.cfg != nil && u.cfg.SyncDir != "" {
		if rel, err := filepath.Rel(u.cfg.SyncDir, absPath); err == nil {
			filename = filepath.ToSlash(rel)
		}
	}

	// 初始化进度
	p := &UploadProgress{
		Filename:    filename,
		TotalBytes:  info.Size(),
		SentBytes:   0,
		TotalChunks: 0,
		SentChunks:  0,
		Status:      "pending",
	}
	u.setProgress(filename, p)
	u.emitProgress(p)

	// 空文件跳过（服务器拒绝 size<=0）
	if info.Size() == 0 {
		p.Status = "skipped"
		u.emitComplete(p)
		return nil
	}

	// 步骤 1：计算 SHA256
	p.Status = "hashing"
	u.emitProgress(p)

	hash, err := u.computeSHA256(absPath)
	if err != nil {
		p.Status = "error"
		p.Error = fmt.Sprintf("计算哈希失败: %v", err)
		u.emitError(p)
		return err
	}

	// 步骤 2：CheckUpload 秒传检查
	p.Status = "checking"
	u.emitProgress(p)

	checkResp, err := u.apiClient.CheckUpload(api.CheckUploadRequest{
		Filename: filename,
		FileSize: info.Size(),
		FileHash: hash,
	})
	if err == nil && checkResp != nil && checkResp.InstantUpload {
		// 秒传成功
		p.Status = "completed"
		p.SentBytes = info.Size()
		u.emitComplete(p)
		return nil
	}
	if err != nil {
		// 检查是否为冲突错误
		var ce *api.ConflictError
		if errors.As(err, &ce) {
			// 冲突处理
			if u.handleConflict(p, ce) {
				return nil
			}
			// handleConflict 返回 false 表示需要继续上传（always_upload 策略）
		}
		// 其他错误：继续尝试正常上传
	}

	// 步骤 3：InitUpload
	p.Status = "uploading"
	u.emitProgress(p)

	force := u.cfg != nil && u.cfg.SyncStrategy == "always_upload"
	initResp, err := u.apiClient.InitUpload(api.InitUploadRequest{
		Filename:  filename,
		FileSize:  info.Size(),
		ChunkSize: u.chunkSize,
		FileHash:  hash,
		Storage:   "local",
	}, force, false)
	if err != nil {
		// 检查是否为冲突错误
		var ce *api.ConflictError
		if errors.As(err, &ce) {
			if u.handleConflict(p, ce) {
				return nil
			}
		}
		p.Status = "error"
		p.Error = fmt.Sprintf("初始化上传失败: %v", err)
		u.emitError(p)
		return err
	}

	p.SessionID = initResp.SessionID
	p.TotalChunks = initResp.TotalChunks
	u.emitProgress(p)

	// 步骤 4：GetUploadStatus 获取已上传分片（断点续传）
	receivedSet := make(map[int]bool)
	statusResp, err := u.apiClient.GetUploadStatus(initResp.SessionID)
	if err == nil && statusResp != nil {
		for _, idx := range statusResp.ReceivedChunks {
			receivedSet[idx] = true
		}
		// 更新已上传字节数（近似：已传分片数 * chunkSize）
		p.SentChunks = len(receivedSet)
		p.SentBytes = int64(p.SentChunks) * u.chunkSize
		if p.SentBytes > p.TotalBytes {
			p.SentBytes = p.TotalBytes
		}
		u.emitProgress(p)
	}

	// 步骤 5：并发上传 missingChunks
	if err := u.uploadChunks(initResp.SessionID, absPath, p, receivedSet); err != nil {
		p.Status = "error"
		p.Error = fmt.Sprintf("分片上传失败: %v", err)
		u.emitError(p)
		return err
	}

	// 步骤 6：CompleteUpload
	_, err = u.apiClient.CompleteUpload(initResp.SessionID)
	if err != nil {
		p.Status = "error"
		p.Error = fmt.Sprintf("完成上传失败: %v", err)
		u.emitError(p)
		return err
	}

	p.Status = "completed"
	p.SentBytes = p.TotalBytes
	p.SentChunks = p.TotalChunks
	u.emitComplete(p)
	return nil
}

// ScanAndUpload 扫描 SyncDir，对比服务器文件列表，上传所有新增/修改文件。
// 扫描结果通过 upload:scan 事件推送待上传文件列表，然后逐个调用 UploadFile。
func (u *Uploader) ScanAndUpload() error {
	if u.cfg == nil || u.cfg.SyncDir == "" {
		return fmt.Errorf("同步目录未配置")
	}

	// 扫描本地文件
	localFiles, err := ScanDir(u.cfg.SyncDir)
	if err != nil {
		return fmt.Errorf("扫描本地目录失败: %w", err)
	}

	// 获取服务器文件列表
	remoteFiles, err := u.apiClient.ListFiles("")
	if err != nil {
		return fmt.Errorf("获取服务器文件列表失败: %w", err)
	}

	// Diff
	toUpload := DiffFiles(localFiles, remoteFiles)

	// 推送扫描结果
	u.emitScan(toUpload)

	if len(toUpload) == 0 {
		return nil
	}

	// 逐个上传
	var lastErr error
	for _, f := range toUpload {
		if err := u.UploadFile(f.AbsPath); err != nil {
			lastErr = err
			// 继续上传下一个文件，不中断
		}
	}
	return lastErr
}

// GetProgress 返回所有文件的上传进度快照（线程安全拷贝）。
// 供 app.go 的 GetUploadProgress 绑定方法调用。
func (u *Uploader) GetProgress() map[string]UploadProgress {
	u.mu.RLock()
	defer u.mu.RUnlock()
	snapshot := make(map[string]UploadProgress, len(u.progress))
	for k, v := range u.progress {
		snapshot[k] = *v
	}
	return snapshot
}

// computeSHA256 流式计算文件 SHA256（避免大文件 OOM）
func (u *Uploader) computeSHA256(absPath string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// uploadChunks 并发上传缺失的分片。
// 使用 buffered channel 作为信号量控制并发数；file.ReadAt 并发安全。
func (u *Uploader) uploadChunks(sessionID, absPath string, p *UploadProgress, receivedSet map[int]bool) error {
	file, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()

	sem := make(chan struct{}, u.concurrency)
	var wg sync.WaitGroup
	var firstErr atomic.Value
	var sentBytes int64
	var sentChunks int32

	for i := 0; i < p.TotalChunks; i++ {
		if receivedSet[i] {
			continue // 断点续传跳过已传分片
		}

		wg.Add(1)
		sem <- struct{}{} // 获取令牌
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			// 读取分片：用 ReadAt 精确定位
			offset := int64(idx) * u.chunkSize
			chunkData := make([]byte, u.chunkSize)
			n, err := file.ReadAt(chunkData, offset)
			if err != nil && err != io.EOF {
				firstErr.Store(fmt.Errorf("读取分片 %d 失败: %w", idx, err))
				return
			}
			if n == 0 {
				return
			}
			chunkData = chunkData[:n]

			// 重试上传
			for retry := 0; retry < maxChunkRetries; retry++ {
				if err := u.apiClient.UploadChunk(sessionID, idx, chunkData); err == nil {
					// 成功：原子更新进度
					atomic.AddInt64(&sentBytes, int64(n))
					newChunks := atomic.AddInt32(&sentChunks, 1)
					p.SentBytes = atomic.LoadInt64(&sentBytes)
					p.SentChunks = int(newChunks) + len(receivedSet)
					u.emitProgress(p)
					return
				}
				// 退避重试
				time.Sleep(time.Second * time.Duration(retry+1))
			}
			// 重试耗尽
			firstErr.Store(fmt.Errorf("分片 %d 上传失败（重试 %d 次）", idx, maxChunkRetries))
		}(i)
	}
	wg.Wait()

	if err := firstErr.Load(); err != nil {
		return err.(error)
	}
	return nil
}

// handleConflict 处理冲突，返回 true 表示已处理（跳过/秒传），false 表示需要继续上传。
// - ask/always_download：跳过，Status=conflict
// - always_upload：返回 false，让调用方用 force=true 重试
func (u *Uploader) handleConflict(p *UploadProgress, ce *api.ConflictError) bool {
	strategy := "ask"
	if u.cfg != nil {
		strategy = u.cfg.SyncStrategy
	}

	switch strategy {
	case "always_upload":
		// 返回 false，让调用方用 force=true 重试
		return false
	default:
		// ask / always_download：跳过
		p.Status = "conflict"
		p.Error = ce.Message
		u.emitComplete(p)
		return true
	}
}

// setProgress 线程安全设置进度
func (u *Uploader) setProgress(filename string, p *UploadProgress) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.progress[filename] = p
}

// emitProgress 推送进度事件
func (u *Uploader) emitProgress(p *UploadProgress) {
	if u.ctx != nil && u.ctx.Err() == nil {
		runtime.EventsEmit(u.ctx, "upload:progress", *p)
	}
}

// emitComplete 推送完成事件
func (u *Uploader) emitComplete(p *UploadProgress) {
	if u.ctx != nil && u.ctx.Err() == nil {
		runtime.EventsEmit(u.ctx, "upload:complete", *p)
	}
}

// emitError 推送错误事件
func (u *Uploader) emitError(p *UploadProgress) {
	if u.ctx != nil && u.ctx.Err() == nil {
		runtime.EventsEmit(u.ctx, "upload:error", map[string]string{
			"filename": p.Filename,
			"error":    p.Error,
		})
	}
}

// emitScan 推送扫描结果事件
func (u *Uploader) emitScan(files []LocalFile) {
	if u.ctx != nil && u.ctx.Err() == nil {
		runtime.EventsEmit(u.ctx, "upload:scan", files)
	}
}
