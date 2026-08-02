package biz

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store 业务数据存储（SQLite 持久化，满足 DATA-001）
// 承载：RBAC / 订单 / 工单 / 资源 / 服务
type Store struct {
	conn *sql.DB
}

// NewStore 打开（或创建）业务数据库并确保表结构
func NewStore(dbPath string) (*Store, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open biz db: %w", err)
	}
	conn.SetMaxOpenConns(1)
	s := &Store{conn: conn}
	if err := s.ensureTables(); err != nil {
		return nil, err
	}
	if err := s.seedRBAC(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库
func (s *Store) Close() error { return s.conn.Close() }

func (s *Store) ensureTables() error {
	stmts := []string{
		// ---------- RBAC ----------
		`CREATE TABLE IF NOT EXISTS biz_roles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS biz_permissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			module TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS biz_user_roles (
			user_id TEXT NOT NULL,
			role_id INTEGER NOT NULL,
			PRIMARY KEY (user_id, role_id)
		)`,
		`CREATE TABLE IF NOT EXISTS biz_role_permissions (
			role_id INTEGER NOT NULL,
			permission_id INTEGER NOT NULL,
			PRIMARY KEY (role_id, permission_id)
		)`,
		// ---------- 订单 ----------
		`CREATE TABLE IF NOT EXISTS biz_orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_no TEXT NOT NULL UNIQUE,
			user_id TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT '商品',
			title TEXT NOT NULL,
			amount INTEGER NOT NULL DEFAULT 0,
			pay_amount INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			pay_channel TEXT NOT NULL DEFAULT '',
			pay_no TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			paid_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS biz_order_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			order_id INTEGER NOT NULL,
			from_status TEXT NOT NULL DEFAULT '',
			to_status TEXT NOT NULL,
			operator TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS biz_refunds (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			refund_no TEXT NOT NULL UNIQUE,
			order_id INTEGER NOT NULL,
			user_id TEXT NOT NULL,
			amount INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'full',
			status TEXT NOT NULL DEFAULT 'pending',
			audit_note TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		// ---------- 工单 ----------
		`CREATE TABLE IF NOT EXISTS biz_tickets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ticket_no TEXT NOT NULL UNIQUE,
			user_id TEXT NOT NULL,
			title TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT '咨询',
			priority TEXT NOT NULL DEFAULT 'medium',
			status TEXT NOT NULL DEFAULT 'pending',
			assignee_id TEXT NOT NULL DEFAULT '',
			order_id INTEGER NOT NULL DEFAULT 0,
			sla_due_at TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS biz_ticket_replies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ticket_id INTEGER NOT NULL,
			user_id TEXT NOT NULL,
			content TEXT NOT NULL,
			is_internal INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS biz_ticket_evaluations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ticket_id INTEGER NOT NULL UNIQUE,
			score INTEGER NOT NULL DEFAULT 5,
			speed INTEGER NOT NULL DEFAULT 5,
			quality INTEGER NOT NULL DEFAULT 5,
			communication INTEGER NOT NULL DEFAULT 5,
			comment TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		// ---------- 资源 ----------
		`CREATE TABLE IF NOT EXISTS biz_resources (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT '文档',
			description TEXT NOT NULL DEFAULT '',
			cover TEXT NOT NULL DEFAULT '',
			file_id TEXT NOT NULL DEFAULT '',
			owner_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'draft',
			policy TEXT NOT NULL DEFAULT 'public',
			price INTEGER NOT NULL DEFAULT 0,
			downloads INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS biz_resource_whitelist (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			resource_id INTEGER NOT NULL,
			user_id TEXT NOT NULL,
			UNIQUE (resource_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS biz_resource_download_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			resource_id INTEGER NOT NULL,
			user_id TEXT NOT NULL,
			ip TEXT NOT NULL DEFAULT '',
			downloaded_at TEXT NOT NULL
		)`,
		// ---------- 服务 ----------
		`CREATE TABLE IF NOT EXISTS biz_service_catalogs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT '咨询服务',
			description TEXT NOT NULL DEFAULT '',
			price INTEGER NOT NULL DEFAULT 0,
			cycle TEXT NOT NULL DEFAULT '',
			deliverable TEXT NOT NULL DEFAULT '',
			sla TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'active'
		)`,
		`CREATE TABLE IF NOT EXISTS biz_service_skus (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			price INTEGER NOT NULL DEFAULT 0,
			desc TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS biz_service_orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			srv_no TEXT NOT NULL UNIQUE,
			order_id INTEGER NOT NULL DEFAULT 0,
			service_id INTEGER NOT NULL,
			sku_id INTEGER NOT NULL DEFAULT 0,
			user_id TEXT NOT NULL,
			owner_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'WAIT_CONFIRM',
			requirement TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS biz_service_milestones (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_order_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			due_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS biz_service_acceptances (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			service_order_id INTEGER NOT NULL,
			result TEXT NOT NULL,
			note TEXT NOT NULL DEFAULT '',
			accepted_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.conn.Exec(stmt); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}
	log.Println("[BizStore] 业务数据表就绪")
	return nil
}

// seedRBAC 初始化角色 + 权限 + 角色权限（幂等）
// 角色层级（28.4）：超级管理员 → 后台管理员 → 中台运营 → 服务人员 → 客户 → 访客
func (s *Store) seedRBAC() error {
	roles := []Role{
		{Code: "super_admin", Name: "超级管理员", Description: "系统最高权限"},
		{Code: "admin", Name: "后台管理员", Description: "管理前台与中台"},
		{Code: "operator", Name: "中台运营", Description: "处理需求/订单/工单/审核"},
		{Code: "staff", Name: "服务人员", Description: "服务交付"},
		{Code: "customer", Name: "客户", Description: "前台用户"},
		{Code: "guest", Name: "访客", Description: "未登录访客"},
	}
	for _, r := range roles {
		_, err := s.conn.Exec(`INSERT OR IGNORE INTO biz_roles(code,name,description) VALUES(?,?,?)`,
			r.Code, r.Name, r.Description)
		if err != nil {
			return err
		}
	}

	perms := []Permission{
		// workbench
		{Code: "workbench:read", Name: "工作台查看", Module: "workbench"},
		{Code: "workbench:write", Name: "工作台编辑", Module: "workbench"},
		// order
		{Code: "order:read", Name: "订单查看", Module: "order"},
		{Code: "order:write", Name: "订单处理", Module: "order"},
		{Code: "order:refund", Name: "订单退款审核", Module: "order"},
		// ticket
		{Code: "ticket:read", Name: "工单查看", Module: "ticket"},
		{Code: "ticket:write", Name: "工单处理", Module: "ticket"},
		{Code: "ticket:assign", Name: "工单派单", Module: "ticket"},
		{Code: "ticket:evaluate", Name: "工单评价", Module: "ticket"},
		// resource
		{Code: "resource:read", Name: "资源查看", Module: "resource"},
		{Code: "resource:write", Name: "资源管理", Module: "resource"},
		{Code: "resource:review", Name: "资源审核", Module: "resource"},
		{Code: "resource:download", Name: "资源下载", Module: "resource"},
		// service
		{Code: "service:read", Name: "服务查看", Module: "service"},
		{Code: "service:write", Name: "服务管理", Module: "service"},
		{Code: "service:deliver", Name: "服务交付", Module: "service"},
		// admin
		{Code: "admin:user", Name: "用户管理", Module: "admin"},
		{Code: "admin:role", Name: "角色权限", Module: "admin"},
		{Code: "admin:audit", Name: "审计日志", Module: "admin"},
	}
	for _, p := range perms {
		_, err := s.conn.Exec(`INSERT OR IGNORE INTO biz_permissions(code,name,module) VALUES(?,?,?)`,
			p.Code, p.Name, p.Module)
		if err != nil {
			return err
		}
	}

	// 角色权限矩阵
	rolePerms := map[string][]string{
		"super_admin": {"workbench:read", "workbench:write", "order:read", "order:write", "order:refund",
			"ticket:read", "ticket:write", "ticket:assign", "ticket:evaluate",
			"resource:read", "resource:write", "resource:review", "resource:download",
			"service:read", "service:write", "service:deliver",
			"admin:user", "admin:role", "admin:audit"},
		"admin": {"workbench:read", "workbench:write", "order:read", "order:write", "order:refund",
			"ticket:read", "ticket:write", "ticket:assign", "ticket:evaluate",
			"resource:read", "resource:write", "resource:review", "resource:download",
			"service:read", "service:write", "service:deliver",
			"admin:user", "admin:role", "admin:audit"},
		"operator": {"workbench:read", "order:read", "order:write",
			"ticket:read", "ticket:write", "ticket:assign",
			"resource:read", "resource:review", "resource:download",
			"service:read", "service:deliver"},
		"staff": {"workbench:read", "ticket:read", "ticket:write",
			"resource:read", "resource:download",
			"service:read", "service:deliver"},
		"customer": {"workbench:read", "order:read", "ticket:read", "ticket:evaluate",
			"resource:read", "resource:download", "service:read"},
		"guest": {"resource:read", "service:read"},
	}
	for roleCode, permCodes := range rolePerms {
		var roleID int64
		if err := s.conn.QueryRow(`SELECT id FROM biz_roles WHERE code=?`, roleCode).Scan(&roleID); err != nil {
			return err
		}
		for _, pc := range permCodes {
			var permID int64
			if err := s.conn.QueryRow(`SELECT id FROM biz_permissions WHERE code=?`, pc).Scan(&permID); err != nil {
				continue // 权限不存在则跳过
			}
			_, err := s.conn.Exec(`INSERT OR IGNORE INTO biz_role_permissions(role_id,permission_id) VALUES(?,?)`,
				roleID, permID)
			if err != nil {
				return err
			}
		}
	}

	// 默认角色绑定：admin 用户 → super_admin
	var superAdminID int64
	if err := s.conn.QueryRow(`SELECT id FROM biz_roles WHERE code='super_admin'`).Scan(&superAdminID); err != nil {
		return err
	}
	_, _ = s.conn.Exec(`INSERT OR IGNORE INTO biz_user_roles(user_id,role_id) VALUES('admin',?)`, superAdminID)
	// 支持通过 BIZ_ADMIN_USER_ID 绑定 AuthSvc 真实管理员（user_id 为 UUID）
	if adminUID := strings.TrimSpace(os.Getenv("BIZ_ADMIN_USER_ID")); adminUID != "" {
		_, _ = s.conn.Exec(`INSERT OR IGNORE INTO biz_user_roles(user_id,role_id) VALUES(?,?)`, adminUID, superAdminID)
		log.Printf("[BizStore] 已绑定超级管理员 user_id=%s", adminUID)
	}

	log.Println("[BizStore] RBAC 角色/权限初始化完成")
	return nil
}

// now 返回当前时间（RFC3339 存储）
func now() string { return time.Now().Format(time.RFC3339) }

// ---------- 通用工具 ----------

// genNo 生成业务单号：前缀 + 时间戳 + 随机后缀（crypto/rand，避免同微秒撞号）
func genNo(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%s%d%s", prefix, time.Now().UnixMilli(), hex.EncodeToString(buf))
}
