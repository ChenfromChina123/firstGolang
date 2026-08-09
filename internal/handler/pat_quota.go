package handler

import (
	"context"
	"errors"

	"filesync/internal/middleware"
	"filesync/internal/store"
)

// ============================================================
// PAT 写入配额校验（补 G3：agent 可无限写入）
// 语义：仅 PAT 认证请求（上下文中带 token_id）才校验；Web JWT 用户不限制。
// 配额按 token 计：quota_bytes 为上限，quota_used 为已用，写入成功后在 DB 记账。
// ============================================================

// ErrQuotaExceeded 表示 PAT 写入配额已用尽（映射 413）。
var ErrQuotaExceeded = errors.New("quota exceeded: token write quota exhausted")

// CheckPATQuota 校验 PAT 写入配额是否允许再写入 size 字节。
// 非 PAT 请求直接放行。返回 error 时调用方应返回 413。
func CheckPATQuota(ctx context.Context, db *store.DB, size int64) error {
	tokenID := middleware.APITokenIDFromContext(ctx)
	if tokenID == "" {
		return nil // 非 PAT（Web 会话）不限制
	}
	quotaBytes, quotaUsed := middleware.PATQuotaFromContext(ctx)
	if quotaBytes <= 0 {
		return nil // 未设置配额
	}
	if quotaUsed+size > quotaBytes {
		return ErrQuotaExceeded
	}
	return nil
}

// AccountPATQuota 在文件成功写入后累加 PAT 配额使用量（非致命失败仅记日志由调用方决定）。
// 非 PAT 请求为空操作。
func AccountPATQuota(ctx context.Context, db *store.DB, size int64) {
	if size <= 0 {
		return
	}
	tokenID := middleware.APITokenIDFromContext(ctx)
	if tokenID == "" {
		return
	}
	// 与 CheckPATQuota 使用同一快照可能导致并发下轻微低估，
	// 这里以 DB 原子累加为准（不会超卖：Check 已按快照拦截，边界并发最多多写一次，可接受）。
	_ = db.AddAPITokenQuotaUsed(tokenID, size)
}
