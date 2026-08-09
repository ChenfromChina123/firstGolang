package auth

import "context"

// context key 类型，避免冲突
type contextKey string

const (
	// CtxKeyUserID 用户 ID
	CtxKeyUserID contextKey = "user_id"
	// CtxKeyUsername 用户名
	CtxKeyUsername contextKey = "username"
	// CtxKeyRole 角色
	CtxKeyRole contextKey = "role"
)

// UserIDFromContext 从 context 中提取用户 ID
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(CtxKeyUserID).(string); ok {
		return v
	}
	return ""
}

// UsernameFromContext 从 context 中提取用户名
func UsernameFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(CtxKeyUsername).(string); ok {
		return v
	}
	return ""
}

// RoleFromContext 从 context 中提取角色
func RoleFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(CtxKeyRole).(string); ok {
		return v
	}
	return ""
}
