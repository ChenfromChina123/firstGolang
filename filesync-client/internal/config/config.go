// Package config 负责桌面端客户端配置的加载与持久化。
// 配置文件位置：~/.filesync-client/config.json
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// 配置目录名与文件名
const (
	configDirName  = ".filesync-client"
	configFileName = "config.json"
)

// Config 桌面端客户端配置结构体
type Config struct {
	ServerURL    string `json:"server_url"`     // 服务器地址（如 https://aistudy.icu）
	Username     string `json:"username"`       // 登录用户名
	SyncDir      string `json:"sync_dir"`       // 本地同步目录绝对路径
	AutoStart    bool   `json:"auto_start"`     // 是否开机自启
	SyncStrategy string `json:"sync_strategy"`  // 冲突默认策略：ask|always_upload|always_download
	LastSyncTime string `json:"last_sync_time"` // 上次同步时间（ISO 8601）
	AutoSync     bool   `json:"auto_sync"`      // 是否启用自动同步
	SyncInterval int    `json:"sync_interval"`  // 同步间隔（秒），默认 30
}

// DefaultConfig 返回默认配置实例。
// 默认服务器地址指向本地，同步间隔 30 秒，冲突策略为询问用户。
func DefaultConfig() *Config {
	return &Config{
		ServerURL:    "http://localhost:8080",
		Username:     "",
		SyncDir:      "",
		AutoStart:    false,
		SyncStrategy: "ask",
		LastSyncTime: "",
		AutoSync:     true,
		SyncInterval: 30,
	}
}

// configDir 返回配置目录绝对路径（~/.filesync-client）。
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDirName), nil
}

// ConfigPath 返回配置文件完整路径。
// 用于外部读取或调试。
func ConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

// Exists 检查配置文件是否存在。
// 用于判断是否首次启动：文件不存在则进入首次启动向导。
func Exists() bool {
	path, err := ConfigPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Load 从用户目录加载配置。
// 若配置文件不存在，返回默认配置且不报错（首次启动场景）。
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 首次启动，返回默认配置
			return DefaultConfig(), nil
		}
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save 将配置写入用户目录。
// 若目录不存在会自动创建（权限 0700，仅当前用户可访问）。
func Save(c *Config) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	// 确保目录存在
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	path, err := ConfigPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 文件权限 0600，仅当前用户可读写，保护账号信息
	return os.WriteFile(path, data, 0600)
}
