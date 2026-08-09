// mcp-stdio 以 stdio transport 运行 FileSync MCP Server（本地 Agent 用）。
//
// 认证：环境变量 FILESYNC_PAT 提供 PAT（也可 FILESYNC_TOKEN 兼容旧命名）。
// 数据库：与主服务共用 DATA_DIR（默认 ./data），打开同一 filesync.db。
// 注意：stdio 模式需与主服务共享存储目录；建议先通过主服务 Web 端生成 PAT。
//
// 用法：
//
//	FILESYNC_PAT=fsk_xxx DATA_DIR=./data go run ./cmd/mcp-stdio
package main

import (
	"context"
	"errors"
	"log"
	"os"

	"filesync/internal/agentsvc"
	"filesync/internal/auth"
	filesyncmcp "filesync/internal/mcp"
	"filesync/internal/storage"
	"filesync/internal/store"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	pat := os.Getenv("FILESYNC_PAT")
	if pat == "" {
		pat = os.Getenv("FILESYNC_TOKEN")
	}
	if pat == "" {
		log.Fatal("FILESYNC_PAT environment variable required (fsk_ token)")
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = dataDir + "/filesync.db"
	}
	db, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// 本地存储（stdio 模式固定 local 后端）
	local, err := storage.NewLocal(dataDir)
	if err != nil {
		log.Fatalf("init local storage: %v", err)
	}
	router := storage.NewRouter(local, nil)

	// 验证 PAT 并构建身份上下文
	tc, err := authenticatePAT(db, pat)
	if err != nil {
		log.Fatalf("PAT authentication failed: %v", err)
	}

	svc := agentsvc.New(db, router, os.Getenv("APP_BASE_URL"))
	s := filesyncmcp.New(svc)
	srv := s.Server()

	// 通过 receiving middleware 注入身份到每次请求 ctx（stdio 无 HTTP 层）
	srv.AddReceivingMiddleware(func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
			ctx = filesyncmcp.WithToolContext(ctx, tc)
			return next(ctx, method, req)
		}
	})

	log.Printf("[mcp-stdio] connected as %s (scopes: %s)", tc.Username, tc.Scopes)
	if err := srv.Run(context.Background(), &sdkmcp.StdioTransport{}); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Fatalf("mcp server run: %v", err)
	}
}

// authenticatePAT 验证 PAT 并构造 ToolContext。
func authenticatePAT(db *store.DB, pat string) (*filesyncmcp.ToolContext, error) {
	v := auth.NewPATValidator(db)
	rec, err := v.Authenticate(pat)
	if err != nil {
		return nil, err
	}
	role := "user"
	if u, uErr := db.GetUserByUsername(rec.Username); uErr == nil {
		role = u.Role
	}
	tc := &filesyncmcp.ToolContext{
		Username:   rec.Username,
		Role:       role,
		Scopes:     rec.Scopes,
		TokenID:    rec.ID,
		SpaceID:    rec.SpaceID,
		PathPrefix: rec.PathPrefix,
		QuotaBytes: rec.QuotaBytes,
		QuotaUsed:  rec.QuotaUsed,
	}
	// 记录最后使用时间（非致命）
	db.TouchAPIToken(rec.ID)
	return tc, nil
}
