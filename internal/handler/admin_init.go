package handler

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/store"
)

// EnsureInitialAdmin 确保 users 表有至少一个管理员
// 首次启动（users 表为空）时，从环境变量读取初始管理员账号密码
func EnsureInitialAdmin(db *store.DB, envUsername, envPassword string) error {
	count, err := db.CountUsers()
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	// users 表为空，需要创建初始管理员
	if envUsername == "" || envPassword == "" {
		log.Printf("[Auth] users table empty, using default admin: admin / changeme123")
		log.Printf("[Auth] IMPORTANT: change default password immediately!")
		envUsername = "admin"
		envPassword = "changeme123"
	} else {
		if err := auth.ValidatePasswordStrength(envPassword); err != nil {
			return fmt.Errorf("initial password too weak: %w", err)
		}
	}

	hash, err := auth.HashPassword(envPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	now := time.Now()
	user := &model.User{
		ID:           generateUserID(),
		Username:     envUsername,
		PasswordHash: hash,
		Role:         "admin",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.CreateUser(user); err != nil {
		return fmt.Errorf("create initial admin: %w", err)
	}
	log.Printf("[Auth] initial admin created: username=%s", envUsername)
	return nil
}

// generateUserID 生成 32 字符 hex 用户 ID
func generateUserID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405")))
	}
	return hex.EncodeToString(b)
}
