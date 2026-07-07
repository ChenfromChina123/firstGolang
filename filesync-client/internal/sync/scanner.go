// Package sync 实现客户端文件同步编排逻辑（上传/下载/冲突处理）。
// 所有业务方法在后端实现，前端只做展示（规则 #15）。
package sync

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"filesync-client/internal/api"
)

// LocalFile 本地文件信息（相对 SyncDir）
type LocalFile struct {
	RelPath string    `json:"rel_path"` // 相对路径，用 / 分隔（符合服务器路径规范）
	AbsPath string    `json:"abs_path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// ScanDir 遍历 syncDir，返回所有非空常规文件。
// 跳过：空文件（size==0，服务器拒绝）、符号链接（防循环）、目录。
// 路径转换：Windows 反斜杠 → 正斜杠（filepath.ToSlash），符合服务器 filename 规范。
func ScanDir(syncDir string) ([]LocalFile, error) {
	if syncDir == "" {
		return nil, fmt.Errorf("同步目录未配置")
	}

	info, err := os.Stat(syncDir)
	if err != nil {
		return nil, fmt.Errorf("访问同步目录失败: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("同步目录不是目录: %s", syncDir)
	}

	var files []LocalFile
	err = filepath.WalkDir(syncDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// 跳过目录本身
		if d.IsDir() {
			return nil
		}

		// 跳过符号链接（防止循环引用）
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("获取文件信息失败 %s: %w", path, err)
		}

		// 跳过空文件（服务器拒绝 size<=0）
		if info.Size() == 0 {
			return nil
		}

		relPath, err := filepath.Rel(syncDir, path)
		if err != nil {
			return fmt.Errorf("计算相对路径失败 %s: %w", path, err)
		}

		files = append(files, LocalFile{
			RelPath: filepath.ToSlash(relPath),
			AbsPath: path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("遍历同步目录失败: %w", err)
	}

	return files, nil
}

// DiffFiles 对比本地与远程文件列表，返回需要上传的本地文件。
// 判定规则：远程不存在（新增）、或远程 Size 不同（修改）→ 需上传。
// 不计算本地 hash 做 hash 比对（避免扫描阶段全盘哈希），由 uploader 在上传流程中计算。
func DiffFiles(local []LocalFile, remote []api.FileRecord) []LocalFile {
	// 以 Filename 为 key 建立远程文件索引
	remoteMap := make(map[string]api.FileRecord, len(remote))
	for _, r := range remote {
		remoteMap[r.Filename] = r
	}

	var toUpload []LocalFile
	for _, l := range local {
		r, exists := remoteMap[l.RelPath]
		if !exists {
			// 远程不存在 → 新增文件
			toUpload = append(toUpload, l)
			continue
		}
		// 远程存在但大小不同 → 修改文件
		if l.Size != r.Size {
			toUpload = append(toUpload, l)
		}
		// 大小相同 → 跳过（简化，不查 hash）
	}
	return toUpload
}
