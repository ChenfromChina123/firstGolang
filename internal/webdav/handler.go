// Package webdav 提供 WebDAV 协议支持。
// 所有操作经过 FileSync 服务端认证 + 文件 I/O，不绕过服务端代码。
package webdav

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/net/webdav"
)

const subDir = "webdav"

// AuthFunc 验证用户名密码，返回 true 表示验证通过。
type AuthFunc func(username, password string) bool

// Handler 返回 WebDAV http.Handler。
//   - dataDir: DATA_DIR，WebDAV 文件存储于 {dataDir}/webdav/
//   - auth: 验证 Basic Auth 凭据的函数（经过服务端用户数据库校验）
//
// 认证方式：HTTP Basic Auth → auth 函数验证（经过服务端代码）
// 文件操作：通过 webdav.Handler → os 文件调用（经过服务端进程）
func Handler(dataDir string, auth AuthFunc) http.Handler {
	baseDir := filepath.Join(dataDir, subDir)
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		log.Printf("[WebDAV] 创建目录失败: %v", err)
	}

	// webdav.Handler 实现完整 WebDAV 协议
	// 包括: PROPFIND/GET/PUT/DELETE/MKCOL/MOVE/COPY/LOCK/UNLOCK
	wd := &webdav.Handler{
		Prefix:     "",
		FileSystem: webdav.Dir(baseDir),
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			user, _, _ := r.BasicAuth()
			if err != nil {
				log.Printf("[WebDAV] %s %s -> %v (user: %s)", r.Method, r.URL.Path, err, user)
			} else {
				log.Printf("[WebDAV] %s %s -> OK (user: %s)", r.Method, r.URL.Path, user)
			}
		},
	}

	// 用 Basic Auth 中间件包装
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="FileSync WebDAV"`)
			http.Error(w, "Unauthorized: missing credentials", http.StatusUnauthorized)
			return
		}
		if !auth(user, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="FileSync WebDAV"`)
			http.Error(w, "Unauthorized: invalid credentials", http.StatusUnauthorized)
			return
		}
		// 所有文件操作经过 webdav.Handler（服务端 os 文件调用）
		wd.ServeHTTP(w, r)
	})
}
