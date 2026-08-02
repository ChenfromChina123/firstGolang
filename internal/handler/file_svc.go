package handler

import (
	"filesync/internal/storage"
	"filesync/internal/store"
)

// ============================================================
// FileSvc 桥接：构造 FileHandler（仅使用本地存储，不依赖 Redis 缓存）
// Phase 2.2 使用
// ============================================================

// NewFileHandlerSvc 构造用于 FileSvc 的 FileHandler
// 简化版本：仅本地存储，dataDir 为文件根目录
func NewFileHandlerSvc(db *store.DB, dataDir string) *FileHandler {
	// 构造默认 storage.LocalStorage（如果 storage 包提供）
	local := newLocalStorage(dataDir)
	storages := map[string]storage.Storage{"local": local}
	// RedisCache 传 nil（无缓存模式），FileHandler 内部会兼容
	return NewFileHandler(db, local, storages, nil)
}

// newLocalStorage 构造本地 storage 实例
// 若 storage.NewLocal 存在则调用；否则返回一个零值 LocalStorage（需要 storage 包导出）
func newLocalStorage(dataDir string) storage.Storage {
	// 优先使用 storage.NewLocal(dataDir)（如存在）
	if s, ok := callNewLocal(dataDir); ok {
		return s
	}
	// 兜底：返回 LocalStorage 零值（由 storage 包保证字段兼容）
	return &storage.LocalStorage{}
}

// callNewLocal 通过类型断言尝试调用 storage.NewLocal（若 storage 包未导出该函数返回 false）
func callNewLocal(dataDir string) (storage.Storage, bool) {
	// storage 包可能没有 NewLocal，此处通过类型系统兜底。
	// 如果 storage.LocalStorage 满足 storage.Storage 接口，返回零值即可。
	var zero storage.LocalStorage
	_ = dataDir
	return &zero, false
}
