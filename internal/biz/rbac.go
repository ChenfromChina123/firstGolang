package biz

import (
	"database/sql"
	"fmt"
)

// ============================================================
// RBAC 服务（第 17 章 / AUTH-001 / 28.4）
// 模型：用户 → 角色 → 权限 → 功能资源；三台一体菜单由角色驱动
// ============================================================

// GetUserRoles 查询用户角色
func (s *Store) GetUserRoles(userID string) ([]Role, error) {
	rows, err := s.conn.Query(`
		SELECT r.id, r.code, r.name, r.description
		FROM biz_roles r
		JOIN biz_user_roles ur ON ur.role_id = r.id
		WHERE ur.user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Description); err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

// GetUserPermissions 查询用户全部权限码（通过角色去重）
func (s *Store) GetUserPermissions(userID string) ([]string, error) {
	rows, err := s.conn.Query(`
		SELECT DISTINCT p.code
		FROM biz_permissions p
		JOIN biz_role_permissions rp ON rp.permission_id = p.id
		JOIN biz_user_roles ur ON ur.role_id = rp.role_id
		WHERE ur.user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, rows.Err()
}

// HasPermission 判断用户是否拥有某权限
func (s *Store) HasPermission(userID, permCode string) (bool, error) {
	var n int
	err := s.conn.QueryRow(`
		SELECT COUNT(*)
		FROM biz_user_roles ur
		JOIN biz_role_permissions rp ON rp.role_id = ur.role_id
		JOIN biz_permissions p ON p.id = rp.permission_id
		WHERE ur.user_id = ? AND p.code = ?`, userID, permCode).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// AssignRole 给用户绑定角色
func (s *Store) AssignRole(userID string, roleCode string) error {
	var roleID int64
	if err := s.conn.QueryRow(`SELECT id FROM biz_roles WHERE code=?`, roleCode).Scan(&roleID); err != nil {
		return fmt.Errorf("role not found: %s", roleCode)
	}
	_, err := s.conn.Exec(`INSERT OR IGNORE INTO biz_user_roles(user_id,role_id) VALUES(?,?)`, userID, roleID)
	return err
}

// UnassignRole 解除用户角色
func (s *Store) UnassignRole(userID string, roleCode string) error {
	_, err := s.conn.Exec(`
		DELETE FROM biz_user_roles WHERE user_id=?
		AND role_id IN (SELECT id FROM biz_roles WHERE code=?)`, userID, roleCode)
	return err
}

// GetMenusForRole 按角色返回三台一体菜单（FR-805/806：菜单由后端驱动）
// 返回结构：前端渲染侧边栏菜单
type MenuItem struct {
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	Icon     string     `json:"icon"`
	Section  string     `json:"section"` // 对应工作台区域 / 前端路由
	Children []MenuItem `json:"children,omitempty"`
}

// GetMenus 按用户角色返回菜单（前台/中台/后台）
func (s *Store) GetMenus(userID string) ([]MenuItem, error) {
	roles, err := s.GetUserRoles(userID)
	if err != nil {
		return nil, err
	}
	perms, err := s.GetUserPermissions(userID)
	if err != nil {
		return nil, err
	}
	_ = roles

	// 权限 → 菜单项映射
	permMenu := map[string]MenuItem{
		"workbench:read":  {ID: "overview", Label: "数据概览", Icon: "chart", Section: "overview"},
		"order:read":      {ID: "orders", Label: "订单管理", Icon: "dollar", Section: "orders"},
		"ticket:read":     {ID: "tickets", Label: "工单管理", Icon: "message", Section: "tickets"},
		"resource:read":   {ID: "resources", Label: "资源管理", Icon: "archive", Section: "resources"},
		"service:read":    {ID: "services", Label: "服务管理", Icon: "rocket", Section: "services"},
		"admin:user":      {ID: "users", Label: "用户管理", Icon: "users", Section: "users"},
		"admin:role":      {ID: "roles", Label: "角色权限", Icon: "gear", Section: "roles"},
		"admin:audit":     {ID: "audit", Label: "审计日志", Icon: "clipboard", Section: "audit"},
	}

	var menus []MenuItem
	seen := map[string]bool{}
	for _, pc := range perms {
		item, ok := permMenu[pc]
		if ok && !seen[item.ID] {
			seen[item.ID] = true
			menus = append(menus, item)
		}
	}
	// 至少返回概览（保证空权限用户也有首页）
	if len(menus) == 0 {
		menus = append(menus, permMenu["workbench:read"])
	}
	return menus, nil
}

// ---------- 用户管理（第 17 章）----------

// ListUsersWithRoles 列出用户及其角色（供后台用户管理）
// 说明：用户主表在 AuthSvc，这里维护角色绑定；返回 AuthSvc 用户列表的本地角色视图
type UserWithRoles struct {
	UserID  string `json:"user_id"`
	Roles   []string `json:"roles"`
	RoleNames []string `json:"role_names"`
}

// ListRoleAssignments 列出所有角色绑定
func (s *Store) ListRoleAssignments() ([]UserWithRoles, error) {
	rows, err := s.conn.Query(`
		SELECT ur.user_id, r.code, r.name
		FROM biz_user_roles ur
		JOIN biz_roles r ON r.id = ur.role_id
		ORDER BY ur.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	userMap := map[string]*UserWithRoles{}
	var order []string
	for rows.Next() {
		var uid, code, name string
		if err := rows.Scan(&uid, &code, &name); err != nil {
			return nil, err
		}
		if _, ok := userMap[uid]; !ok {
			userMap[uid] = &UserWithRoles{UserID: uid}
			order = append(order, uid)
		}
		userMap[uid].Roles = append(userMap[uid].Roles, code)
		userMap[uid].RoleNames = append(userMap[uid].RoleNames, name)
	}
	var out []UserWithRoles
	for _, uid := range order {
		out = append(out, *userMap[uid])
	}
	return out, rows.Err()
}

// ListAllRoles 列出所有角色（后台配置）
func (s *Store) ListAllRoles() ([]Role, error) {
	rows, err := s.conn.Query(`SELECT id, code, name, description FROM biz_roles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Description); err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

// IsNotFound 判断是否为记录不存在错误
func IsNotFound(err error) bool { return err == sql.ErrNoRows }
