package service

import "filesync/internal/model"

// ListUsersExport 导出 ListUsers 方法给 handler 使用
// 避免 handler 直接依赖 *store.DB
func (s *AuthService) ListUsersExport() ([]*model.User, error) {
	return s.db.ListUsers()
}
