package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword 用 bcrypt 哈希密码（cost=12）
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(bytes), err
}

// CheckPassword 校验明文密码与 bcrypt 哈希是否匹配
func CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// GenerateSecret 生成 32 字节随机密钥（用于 JWT_SECRET 环境变量未设置时）
func GenerateSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("generate secret: %v", err))
	}
	return hex.EncodeToString(b)
}

// ValidatePasswordStrength 校验密码强度
// 规则：至少 8 位，包含字母和数字
func ValidatePasswordStrength(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hasLetter := false
	hasDigit := false
	for _, c := range password {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			hasLetter = true
		case c >= '0' && c <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("password must contain at least one letter and one digit")
	}
	return nil
}

// GenerateActivationToken 生成 32 字节随机 hex 字符串作为激活令牌
// 用于注册后通过邮件发送的激活链接 ?token=xxx
// rand.Read 失败时返回 error，调用方必须处理（不使用可预测的弱兜底值）
func GenerateActivationToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate activation token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GenerateResetCode 生成 6 位数字字符串作为密码重置验证码
// 范围 000000-999999，前导零补齐
// rand.Read 失败时返回 error，调用方必须处理（不使用固定值兜底，避免可预测验证码）
func GenerateResetCode() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate reset code: %w", err)
	}
	// 把 4 字节转成无符号 32 位整数再对 1000000 取模
	var num uint32
	for _, c := range b {
		num = num<<8 | uint32(c)
	}
	return fmt.Sprintf("%06d", num%1000000), nil
}

// GenerateUsernameFromEmail 从邮箱生成 username
// 规则：取 @ 前部分 + 4 位随机 hex 后缀（避免与现有用户名冲突）
// 例如：alice@example.com → alice3a7f
func GenerateUsernameFromEmail(email string) string {
	prefix := email
	if idx := strings.Index(email, "@"); idx > 0 {
		prefix = email[:idx]
	}
	// 仅保留字母数字下划线，避免特殊字符
	var sb strings.Builder
	for _, c := range prefix {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			sb.WriteRune(c)
		}
	}
	name := sb.String()
	if name == "" {
		name = "user"
	}
	// 转小写 + 4 位随机 hex 后缀
	name = strings.ToLower(name)
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return name + "0000"
	}
	return name + hex.EncodeToString(b)
}

// ValidateEmail 简单邮箱格式校验
// 规则：包含 @，@ 前后都有内容，域名包含 .
func ValidateEmail(email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return errors.New("email is required")
	}
	atIdx := strings.Index(email, "@")
	if atIdx <= 0 || atIdx == len(email)-1 {
		return errors.New("invalid email format")
	}
	domain := email[atIdx+1:]
	if !strings.Contains(domain, ".") {
		return errors.New("invalid email domain")
	}
	if len(email) > 255 {
		return errors.New("email too long")
	}
	return nil
}

// NormalizeEmail 邮箱标准化：去空格 + 转小写
// 注册和查询前都应调用此函数，确保大小写一致
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
