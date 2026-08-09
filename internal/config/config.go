// Package config 集中管理服务间默认地址与通用配置项，
// 消除各 cmd/intprimitive 重复硬编码导致的默认值漂移。
package config

import (
	"os"
	"strings"
)

// 服务默认地址（内网服务间调用）
const (
	DefaultAuthSvcURL = "http://localhost:8081" // auth-svc 认证服务
	DefaultFileSvcURL = "http://localhost:8082" // file-svc 文件服务
)

// getEnv 读取环境变量，为空时返回 fallback
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// AuthSvcURL 返回认证服务地址（环境变量 AUTHSVC_URL 优先）
func AuthSvcURL() string {
	return strings.TrimSuffix(getEnv("AUTHSVC_URL", DefaultAuthSvcURL), "/")
}

// FileSvcURL 返回文件服务地址（环境变量 FILE_SVC_URL 优先）
func FileSvcURL() string {
	return strings.TrimSuffix(getEnv("FILE_SVC_URL", DefaultFileSvcURL), "/")
}