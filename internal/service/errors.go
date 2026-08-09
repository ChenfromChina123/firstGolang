package service

import "errors"

// 认证服务导出哨兵错误（handler 层用 errors.Is 精确分支，勿做字符串匹配）
var (
	// ErrInvalidCredentials 用户名或密码错误
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrAccountNotActive 账号未激活
	ErrAccountNotActive = errors.New("account not activated")
	// ErrActivationExpired 激活链接过期
	ErrActivationExpired = errors.New("activation token expired")
)