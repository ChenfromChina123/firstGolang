package agentsvc

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"filesync/internal/model"
	"filesync/internal/store"

	"golang.org/x/crypto/bcrypt"
)

// ============ 分享操作（供 MCP share_* 工具调用） ============

// ShareCreateOptions 创建分享参数。
type ShareCreateOptions struct {
	FileID    string // file 类型必填
	DirPrefix string // dir 类型必填（目录前缀，如 "docs/"）
	ShareType string // "file" | "dir"
	Password  string // 可选，1-64 字符
	ExpiresIn int64  // 秒；0=永久
	Username  string // 创建者
	Role      string // 角色
	SpaceID   string // 目标空间（dir 类型可选）
}

// ShareView 分享视图（URL 为绝对地址）。
type ShareView struct {
	ID         string `json:"id"`
	ShareType  string `json:"share_type"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	HasPassword bool  `json:"has_password"`
	DownloadCount int `json:"download_count"`
}

// CreateShare 创建分享，返回绝对 URL。
// 修复点：现有 REST createShareResponse.URL 是相对路径，MCP 必须返回绝对 URL。
func (s *AgentSvc) CreateShare(ctx context.Context, opt ShareCreateOptions) (*ShareView, error) {
	if opt.ShareType != "file" && opt.ShareType != "dir" {
		return nil, fmt.Errorf("share_type must be file or dir")
	}
	var name string
	var shareSpaceID string
	if opt.ShareType == "file" {
		if opt.FileID == "" {
			return nil, fmt.Errorf("file_id required for file share")
		}
		f, err := s.db.GetFile(opt.FileID)
		if err != nil {
			if ErrIsNotFound(err) {
				return nil, ErrNotFound
			}
			return nil, fmtErr(err)
		}
		if f.Owner != opt.Username && opt.Role != "admin" {
			return nil, ErrForbidden
		}
		shareSpaceID = f.SpaceID
		name = f.Filename
	} else {
		if opt.DirPrefix == "" {
			return nil, fmt.Errorf("dir_prefix required for dir share")
		}
		opt.DirPrefix = normalizeDirPrefix(opt.DirPrefix)
		// 空间归一化（与 REST resolveSpaceID 语义一致）：
		//   - admin 空 → SpaceAll("*")，匹配所有空间（分享全局目录）
		//   - 普通用户空 → "default-<username>"（我的空间）
		//   - 指定空间 → 直接使用
		shareSpaceID = opt.SpaceID
		querySpaceID := opt.SpaceID
		switch {
		case shareSpaceID == "" && opt.Role == "admin":
			querySpaceID = store.SpaceAll
		case shareSpaceID == "":
			shareSpaceID = "default-" + opt.Username
			querySpaceID = shareSpaceID
		}
		owner := opt.Username
		if opt.Role == "admin" {
			owner = ""
		}
		files, err := s.db.ListFiles(querySpaceID, opt.DirPrefix, owner)
		if err != nil {
			return nil, fmtErr(err)
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("directory empty or not found")
		}
		name = strings.TrimSuffix(opt.DirPrefix, "/")
	}

	// 密码
	var passwordHash string
	if opt.Password != "" {
		if len(opt.Password) > 64 {
			return nil, fmt.Errorf("password length cannot exceed 64 chars")
		}
		h, err := bcrypt.GenerateFromPassword([]byte(opt.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmtErr(err)
		}
		passwordHash = string(h)
	}

	// 过期时间
	var expiresAt *time.Time
	if opt.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(opt.ExpiresIn) * time.Second)
		expiresAt = &t
	}

	shareID, err := generateShareID()
	if err != nil {
		return nil, fmtErr(err)
	}
	sr := &model.Share{
		ID:           shareID,
		FileID:       opt.FileID,
		DirPrefix:    opt.DirPrefix,
		ShareType:    opt.ShareType,
		CreatedBy:    opt.Username,
		SpaceID:      shareSpaceID,
		CreatedAt:    time.Now(),
		ExpiresAt:    expiresAt,
		DownloadCount: 0,
		IsActive:     true,
		PasswordHash: passwordHash,
	}
	if err := s.db.CreateShare(sr); err != nil {
		return nil, fmtErr(err)
	}
	rel := fmt.Sprintf("/web/share.html?id=%s", shareID)
	return &ShareView{
		ID:          shareID,
		ShareType:   sr.ShareType,
		Name:        name,
		URL:         s.AbsURL(rel),
		HasPassword: passwordHash != "",
	}, nil
}

// ListShares 列出用户的分享。
func (s *AgentSvc) ListShares(ctx context.Context, username string) ([]ShareView, error) {
	shares, err := s.db.ListSharesByCreator(username)
	if err != nil {
		return nil, fmtErr(err)
	}
	views := make([]ShareView, 0, len(shares))
	for _, sr := range shares {
		var name string
		if sr.ShareType == "file" && sr.FileID != "" {
			if f, err := s.db.GetFile(sr.FileID); err == nil {
				name = f.Filename
			} else {
				name = "(file deleted)"
			}
		} else if sr.ShareType == "dir" {
			name = strings.TrimSuffix(sr.DirPrefix, "/")
		}
		v := ShareView{
			ID:            sr.ID,
			ShareType:     sr.ShareType,
			Name:          name,
			URL:           s.AbsURL(fmt.Sprintf("/web/share.html?id=%s", sr.ID)),
			DownloadCount: sr.DownloadCount,
			HasPassword:   sr.PasswordHash != "",
		}
		if sr.ExpiresAt != nil {
			v.ExpiresAt = sr.ExpiresAt.Format(time.RFC3339)
		}
		views = append(views, v)
	}
	return views, nil
}

// DeleteShare 删除分享（校验归属）。
func (s *AgentSvc) DeleteShare(ctx context.Context, shareID, username, role string) error {
	sr, err := s.db.GetShare(shareID)
	if err != nil {
		if ErrIsNotFound(err) {
			return ErrNotFound
		}
		return fmtErr(err)
	}
	if sr.CreatedBy != username && role != "admin" {
		return ErrForbidden
	}
	if err := s.db.DeleteShare(shareID); err != nil {
		return fmtErr(err)
	}
	return nil
}

// generateShareID 生成 8 字符短分享 ID（字母数字，避免易混淆字符）。
func generateShareID() (string, error) {
	const chars = "23456789abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}
