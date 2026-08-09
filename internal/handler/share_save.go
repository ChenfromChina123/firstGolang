package handler

import (
	"archive/zip"
	"encoding/json"
	"filesync/internal/model"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func (h *ShareHandler) batchDownloadShare(w http.ResponseWriter, r *http.Request, id string) {
	// 防盗链：校验 token + 频率限制
	token := r.URL.Query().Get("token")
	if !validateShareToken(token, id, h.secret) {
		http.Error(w, `{"error":"forbidden","message":"invalid or missing download token"}`, http.StatusForbidden)
		return
	}
	if !h.downloadLimiter.Allow(r) {
		http.Error(w, `{"error":"rate_limited","message":"too many downloads, try again later"}`, http.StatusTooManyRequests)
		return
	}

	s, err := h.db.GetShare(id)
	if err != nil {
		http.Error(w, `{"error":"share_not_found"}`, http.StatusNotFound)
		return
	}
	if !s.IsActive {
		http.Error(w, `{"error":"share_disabled"}`, http.StatusForbidden)
		return
	}
	if s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt) {
		http.Error(w, `{"error":"share_expired"}`, http.StatusForbidden)
		return
	}
	if s.ShareType != "dir" {
		http.Error(w, `{"error":"not_dir_share","message":"仅目录分享支持批量下载"}`, http.StatusBadRequest)
		return
	}
	// 密码检查：有密码且未认证则返回 401
	if !h.requireShareAuth(w, r, s) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 限制 body 64KB
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 {
		http.Error(w, `{"error":"paths_required"}`, http.StatusBadRequest)
		return
	}
	if len(req.Paths) > 500 {
		http.Error(w, `{"error":"too_many_files","message":"单次最多 500 个文件"}`, http.StatusBadRequest)
		return
	}

	// 记录下载（幂等，同 visitor 不重复计数）
	visitorID := getOrSetVisitorCookie(w, r)
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	if err := h.db.IncrementShareDownload(id, visitorID, ip, r.UserAgent()); err != nil {
		log.Printf("[Share] batch increment download error: id=%s err=%v", id, err)
	}

	// 流式打包 ZIP
	dirName := strings.TrimSuffix(s.DirPrefix, "/")
	if dirName == "" {
		dirName = "files"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-batch.zip"`, dirName))
	w.Header().Set("Content-Type", "application/zip")

	zw := zip.NewWriter(w)
	defer zw.Close()

	var successCount, failCount int
	for _, p := range req.Paths {
		fullPath, err := resolveSharePath(s.DirPrefix, p)
		if err != nil {
			log.Printf("[Share] batch skip invalid path %q: %v", p, err)
			failCount++
			continue
		}
		f, err := h.db.FindFileByName(shareSpaceID(s), fullPath, s.CreatedBy)
		if err != nil || f == nil {
			log.Printf("[Share] batch file not found: %s", fullPath)
			failCount++
			continue
		}
		if strings.HasSuffix(f.Filename, ".keep") {
			continue // 跳过目录占位文件
		}
		// ZIP 内路径用相对分享根的路径（去掉 s.DirPrefix）
		relPath := strings.TrimPrefix(f.Filename, s.DirPrefix)
		if relPath == "" {
			continue
		}
		zf, err := zw.Create(relPath)
		if err != nil {
			log.Printf("[Share] batch zip create %s error: %v", relPath, err)
			failCount++
			continue
		}
		reader, err := h.storage.ReadFile(f.StoragePath, 0)
		if err != nil {
			log.Printf("[Share] batch read %s error: %v", f.StoragePath, err)
			failCount++
			continue
		}
		if _, err := io.Copy(zf, reader); err != nil {
			log.Printf("[Share] batch zip copy %s error: %v", relPath, err)
			failCount++
		} else {
			successCount++
		}
		reader.Close()
	}
	log.Printf("[Share] batch downloaded: id=%s success=%d fail=%d", id, successCount, failCount)
}

// saveShareToMyFiles 转存分享文件到自己的文件中心（全局存储，共享 storage_path）。
// POST /api/share/save  Body: {"share_id":"xxx","file_ids":["id1","id2"],"target_dir":"docs/"}
// 权限：必须登录；每个文件校验属于该分享范围（isFileInShare）。
// 全局存储：新记录共享源文件 storage_path，不复制物理文件；CreateFile 失败仅返回错误（不删物理文件）。
// 同名冲突自动重命名（file.pdf → file_1.pdf）。
func (h *ShareHandler) saveShareToMyFiles(w http.ResponseWriter, r *http.Request, username string) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req struct {
		ShareID   string   `json:"share_id"`
		FileIDs   []string `json:"file_ids"`
		TargetDir string   `json:"target_dir"`
		SpaceID   string   `json:"space_id,omitempty"` // 转存目标空间 ID（空=默认空间）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_json"}`, http.StatusBadRequest)
		return
	}
	if req.ShareID == "" || len(req.FileIDs) == 0 {
		http.Error(w, `{"error":"share_id_and_file_ids_required"}`, http.StatusBadRequest)
		return
	}
	if len(req.FileIDs) > 500 {
		http.Error(w, `{"error":"too_many_files","message":"单次最多转存 500 个文件"}`, http.StatusBadRequest)
		return
	}

	// 校验分享有效性
	s, err := h.db.GetShare(req.ShareID)
	if err != nil {
		http.Error(w, `{"error":"share_not_found"}`, http.StatusNotFound)
		return
	}
	if !s.IsActive {
		http.Error(w, `{"error":"share_disabled"}`, http.StatusForbidden)
		return
	}
	if s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt) {
		http.Error(w, `{"error":"share_expired"}`, http.StatusForbidden)
		return
	}

	// 规范化目标目录（如 "docs/" 或 "" 表示根目录）
	targetDir := normalizeDirPath(req.TargetDir)
	if targetDir != "" {
		// 校验目标目录路径合法性
		if err := validateFilePath(targetDir + ".keep"); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"invalid_target_dir","message":"%s"}`, err.Error()), http.StatusBadRequest)
			return
		}
	}

	type result struct {
		FileID   string `json:"file_id"`
		Filename string `json:"filename"`
		NewName  string `json:"new_name"`
		Size     int64  `json:"size"`
		Success  bool   `json:"success"`
		Error    string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(req.FileIDs))
	var successCount, failCount int

	for _, fileID := range req.FileIDs {
		// 查询源文件
		f, err := h.db.GetFile(fileID)
		if err != nil {
			results = append(results, result{FileID: fileID, Success: false, Error: "file_not_found"})
			failCount++
			continue
		}
		// 校验文件属于该分享范围（防越权）
		if !isFileInShare(f, s) {
			results = append(results, result{FileID: fileID, Filename: f.Filename, Success: false, Error: "not_in_share"})
			failCount++
			continue
		}
		// 跳过目录占位文件
		if strings.HasSuffix(f.Filename, ".keep") {
			results = append(results, result{FileID: fileID, Filename: f.Filename, Success: false, Error: "is_directory_placeholder"})
			continue
		}

		// 构造新文件名：target_dir + 原文件 basename（去掉原路径前缀，只保留文件名）
		baseName := f.Filename
		if idx := strings.LastIndex(f.Filename, "/"); idx >= 0 {
			baseName = f.Filename[idx+1:]
		}
		newFilename := targetDir + baseName
		// 同 owner + 目标空间下避免重名
		newFilename = generateUniqueFilename(newFilename, req.SpaceID, username, h.db)

		// 全局存储：共享源文件 storage_path，不复制物理文件
		newFileID := generateID()

		// 创建新的 DB 记录（owner=当前用户，共享源文件 storage_path）
		now := time.Now()
		newRecord := &model.FileRecord{
			ID:          newFileID,
			Filename:    newFilename,
			Size:        f.Size,
			Hash:        f.Hash,
			StoragePath: f.StoragePath, // 共享源文件物理路径
			StorageType: f.StorageType,
			ChunkSize:   f.ChunkSize,
			TotalChunks: f.TotalChunks,
			Status:      "completed",
			Owner:       username,
			SpaceID:     req.SpaceID,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := h.db.CreateFile(newRecord); err != nil {
			log.Printf("[Share] save create file record error: %v", err)
			results = append(results, result{FileID: fileID, Filename: f.Filename, Success: false, Error: "db_create_failed"})
			failCount++
			continue
		}

		results = append(results, result{
			FileID:   fileID,
			Filename: f.Filename,
			NewName:  newFilename,
			Size:     f.Size,
			Success:  true,
		})
		successCount++
	}

	log.Printf("[Share] save to my files: user=%s share=%s success=%d fail=%d", username, req.ShareID, successCount, failCount)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"saved":   true,
		"success": successCount,
		"fail":    failCount,
		"results": results,
	})
}

// generateShareID 生成 8 字符短 ID（16 字节随机数 hex 编码后取前 8 字符）
// 碰撞概率：62^8 ≈ 218 万亿种组合，足够使用
