package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"filesync/internal/model"
)

// ============== Personal Access Token (PAT) ==============
// PAT 用于 MCP / 外部 Agent 认证，与 JWT 双 Token 体系并行。
// 设计：
//   - 明文格式 fsk_<43 字符 base62>（约 256 bit 熵），前缀便于日志脱敏与秘密扫描
//   - 库内只存 SHA-256 哈希（高熵随机串无需 bcrypt 慢哈希，且每请求都要验证）
//   - 校验通过后将 token 元数据（scopes/space_id/path_prefix/quota）注入请求上下文

const (
	// PATPrefix PAT 明文前缀（fsk = FileSync Key）
	PATPrefix = "fsk_"
	// PATPlainLen PAT 明文总长度：fsk_ + 43
	PATPlainLen = 4 + 43
)

// Scope 常量（空格分隔的 scope 体系，MCP 工具门禁与 Web 共用）
const (
	// ScopeRead 只读：list/stat/read/download/trash_list/space_list
	ScopeRead = "filesync:read"
	// ScopeWrite 写入：mkdir/rename/move/write/upload/delete/trash_restore
	ScopeWrite = "filesync:write"
	// ScopeShare 分享：share_create/share_delete（含密码设置）
	ScopeShare = "filesync:share"
)

var base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GeneratePAT 生成 PAT 明文 + 其 SHA-256 哈希。
// 明文仅在创建时返回一次，之后无法恢复。
func GeneratePAT() (plaintext, hash string, err error) {
	// 32 字节随机数 → base62 编码 ≈ 43 字符
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	return encodeBase62Big(buf)
}

// encodeBase62Big 用大整数将 32 字节编码为 base62 字符串（43 字符）。
func encodeBase62Big(b []byte) (string, string, error) {
	// 标准实现：处理大端字节序
	const base uint64 = 62
	// 用 []uint8 模拟大整数
	digits := make([]uint8, 0, 44)
	// 初始化大数（大端）
	num := make([]byte, len(b))
	copy(num, b)
	// 重复除法
	for {
		var rem uint64
		allZero := true
		for i := 0; i < len(num); i++ {
			cur := rem*256 + uint64(num[i])
			num[i] = byte(cur / base)
			rem = cur % base
			if num[i] != 0 {
				allZero = false
			}
		}
		digits = append(digits, byte(rem))
		if allZero {
			break
		}
	}
	// 反转
	var sb strings.Builder
	for i := len(digits) - 1; i >= 0; i-- {
		sb.WriteByte(base62Chars[digits[i]])
	}
	s := sb.String()
	// 补足到 43 字符（前导 '0' 字符即 base62Chars[0]）
	for len(s) < 43 {
		s = string(base62Chars[0]) + s
	}
	plain := PATPrefix + s
	return plain, HashPAT(plain), nil
}

// HashPAT 计算 PAT 明文的 SHA-256 哈希（hex）。
func HashPAT(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// LooksLikePAT 判断 token 是否以 PAT 前缀开头（用于认证分支选择）。
func LooksLikePAT(tok string) bool {
	return strings.HasPrefix(tok, PATPrefix)
}

// ValidatePATPlain 校验 PAT 明文格式（长度 + 前缀 + base62 字符集）。
func ValidatePATPlain(plain string) error {
	if !strings.HasPrefix(plain, PATPrefix) {
		return errors.New("PAT must start with " + PATPrefix)
	}
	if len(plain) != PATPlainLen {
		return errors.New("invalid PAT length")
	}
	for _, c := range plain[len(PATPrefix):] {
		if !strings.ContainsRune(base62Chars, c) {
			return errors.New("PAT contains invalid character")
		}
	}
	return nil
}

// PATStore PAT 持久化存储接口（由 store.DB 实现）。
type PATStore interface {
	GetAPITokenByHash(hash string) (*model.APIToken, error)
}

// PATValidator 验证 PAT 明文，检查吊销/过期状态。
type PATValidator struct {
	store PATStore
}

// NewPATValidator 创建 PAT 验证器。
func NewPATValidator(s PATStore) *PATValidator {
	return &PATValidator{store: s}
}

// Authenticate 验证 PAT 明文：
//   - 格式不合法 → ErrNotPAT
//   - 哈希查不到 / 已吊销 / 已过期 → error
//
// 成功返回 token 记录（含 scopes/space_id/path_prefix/quota）。
func (v *PATValidator) Authenticate(plain string) (*model.APIToken, error) {
	if !LooksLikePAT(plain) {
		return nil, ErrNotPAT
	}
	tok, err := v.store.GetAPITokenByHash(HashPAT(plain))
	if err != nil {
		return nil, err
	}
	if tok.RevokedAt != nil {
		return nil, ErrPATRevoked
	}
	if tok.ExpiresAt != nil && time.Now().After(*tok.ExpiresAt) {
		return nil, ErrPATExpired
	}
	return tok, nil
}

// PAT 相关错误（供中间件与 handler 判定）
var (
	// ErrNotPAT token 不以 fsk_ 前缀开头
	ErrNotPAT = errors.New("not a PAT token")
	// ErrPATRevoked PAT 已被吊销
	ErrPATRevoked = errors.New("PAT revoked")
	// ErrPATExpired PAT 已过期
	ErrPATExpired = errors.New("PAT expired")
)

// ParseScopes 解析空格分隔的 scope 字符串为集合（去重）。
func ParseScopes(scopes string) map[string]bool {
	set := make(map[string]bool)
	for _, s := range strings.Fields(scopes) {
		if s != "" {
			set[s] = true
		}
	}
	return set
}

// HasScope 检查 scope 集合是否包含指定 scope。
func HasScope(scopes string, required string) bool {
	if required == "" {
		return true
	}
	return ParseScopes(scopes)[required]
}
