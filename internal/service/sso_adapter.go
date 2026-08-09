package service

import (
	"time"

	"filesync/internal/store"
)

// SSOStoreAdapter 适配 *store.DB 为 auth.SSOSessionStore。
// 同时提供 Save 与 SaveSession 方法名，兼容接口新旧命名。
type SSOStoreAdapter struct{ DB *store.DB }

func (a SSOStoreAdapter) Save(sessionID, userID, ip, ua string, expiresAt time.Time) error {
	return a.DB.SaveSession(sessionID, userID, ip, ua, expiresAt)
}
func (a SSOStoreAdapter) SaveSession(sessionID, userID, ip, ua string, expiresAt time.Time) error {
	return a.DB.SaveSession(sessionID, userID, ip, ua, expiresAt)
}
func (a SSOStoreAdapter) GetBySessionID(sessionID string) (string, error) {
	return a.DB.GetBySessionID(sessionID)
}
func (a SSOStoreAdapter) Delete(sessionID string) error { return a.DB.Delete(sessionID) }
func (a SSOStoreAdapter) DeleteByUser(userID string) error { return a.DB.DeleteByUser(userID) }
