package workbench

import (
	"database/sql"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

// Store 工作台业务数据持久化（SQLite）
// 职责：项目 / 待办 / 内容 / 通知 / 概览卡片 / 收入 / 流量 的 CRUD
// 按 owner（user_id）隔离数据，满足 DATA-001（业务数据入库、重启不丢）
type Store struct {
	conn *sql.DB
}

// NewStore 打开（或创建）工作台 SQLite 数据库并确保表结构
func NewStore(dbPath string) (*Store, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open workbench db: %w", err)
	}
	// SQLite 单写者限制：连接池上限 1，避免并发写锁
	conn.SetMaxOpenConns(1)

	s := &Store{conn: conn}
	if err := s.ensureTables(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库连接
func (s *Store) Close() error { return s.conn.Close() }

func (s *Store) ensureTables() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS wb_overview (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			label TEXT NOT NULL,
			value TEXT NOT NULL,
			trend TEXT NOT NULL DEFAULT '',
			trend_up INTEGER NOT NULL DEFAULT 1,
			icon TEXT NOT NULL DEFAULT 'eye',
			pos INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS wb_projects (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT '规划中',
			progress INTEGER NOT NULL DEFAULT 0,
			priority TEXT NOT NULL DEFAULT 'medium',
			deadline TEXT NOT NULL DEFAULT '',
			pos INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS wb_content (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			title TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT '博客',
			status TEXT NOT NULL DEFAULT '草稿',
			views TEXT NOT NULL DEFAULT '0',
			date TEXT NOT NULL DEFAULT '',
			pos INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS wb_todos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			text TEXT NOT NULL,
			done INTEGER NOT NULL DEFAULT 0,
			priority TEXT NOT NULL DEFAULT 'medium',
			due TEXT NOT NULL DEFAULT '',
			pos INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS wb_notifications (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			icon TEXT NOT NULL DEFAULT 'bell',
			title TEXT NOT NULL,
			desc TEXT NOT NULL DEFAULT '',
			time TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT 'info',
			unread INTEGER NOT NULL DEFAULT 1,
			pos INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS wb_income (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			label TEXT NOT NULL,
			value INTEGER NOT NULL DEFAULT 0,
			color TEXT NOT NULL DEFAULT '#00d9ff',
			pos INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS wb_traffic (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			source TEXT NOT NULL,
			visits INTEGER NOT NULL DEFAULT 0,
			percentage INTEGER NOT NULL DEFAULT 0,
			pos INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS wb_chart (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT 'visits',
			label TEXT NOT NULL,
			value INTEGER NOT NULL DEFAULT 0,
			pos INTEGER NOT NULL DEFAULT 0
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.conn.Exec(stmt); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}
	log.Println("[WorkbenchStore] 数据表就绪")
	return nil
}

// SeedIfEmpty 当 owner 无任何数据时写入默认演示数据（与旧 config.js 工作台数据对齐）
func (s *Store) SeedIfEmpty(owner string) error {
	var cnt int
	if err := s.conn.QueryRow(`SELECT COUNT(*) FROM wb_overview WHERE owner=?`, owner).Scan(&cnt); err != nil {
		return err
	}
	if cnt > 0 {
		return nil // 已有数据，不重复 seed
	}
	log.Printf("[WorkbenchStore] owner=%s 首次使用，写入默认工作台数据", owner)
	tx, err := s.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	overview := []struct{ label, value, trend string; up bool; icon string }{
		{"今日访问", "1,284", "+12.5%", true, "eye"},
		{"本周新增订阅", "86", "+23.1%", true, "mail"},
		{"本月收入", "¥18,560", "+8.3%", true, "dollar"},
		{"待办事项", "7", "-2", false, "clipboard"},
	}
	for i, o := range overview {
		up := 0
		if o.up {
			up = 1
		}
		if _, err := tx.Exec(`INSERT INTO wb_overview(owner,label,value,trend,trend_up,icon,pos) VALUES(?,?,?,?,?,?,?)`,
			owner, o.label, o.value, o.trend, up, o.icon, i); err != nil {
			return err
		}
	}

	projects := []struct{ name, status string; progress int; priority, deadline string }{
		{"AIWriter v2.0 迭代", "进行中", 65, "high", "2026-08-15"},
		{"AgentForge 文档更新", "进行中", 40, "medium", "2026-08-08"},
		{"训练营第5期筹备", "规划中", 15, "high", "2026-08-20"},
		{"墨远周报第48期", "已发布", 100, "low", "2026-07-28"},
	}
	for i, p := range projects {
		if _, err := tx.Exec(`INSERT INTO wb_projects(owner,name,status,progress,priority,deadline,pos) VALUES(?,?,?,?,?,?,?)`,
			owner, p.name, p.status, p.progress, p.priority, p.deadline, i); err != nil {
			return err
		}
	}

	content := []struct{ title, typ, status, views, date string }{
		{"2026年AI Agent开发：从概念到生产的完整路径", "博客", "已发布", "3,241", "2026-07-25"},
		{"AI应用开发训练营 - 第4期课程录像", "视频", "已发布", "5,678", "2026-07-22"},
		{"独立开发者第一年：我用3个产品赚到第一个10万", "博客", "已发布", "8,920", "2026-07-18"},
		{"Prompt工程不是玄学：一套可复用的提示词设计框架", "博客", "已发布", "6,543", "2026-07-10"},
		{"大模型选型指南：什么场景该用什么模型", "博客", "草稿", "—", "2026-07-02"},
	}
	for i, c := range content {
		if _, err := tx.Exec(`INSERT INTO wb_content(owner,title,type,status,views,date,pos) VALUES(?,?,?,?,?,?,?)`,
			owner, c.title, c.typ, c.status, c.views, c.date, i); err != nil {
			return err
		}
	}

	todos := []struct{ text string; done bool; priority, due string }{
		{"完成AIWriter v2.0技术方案文档", false, "high", "今天"},
		{"录制训练营第5期第3课视频", false, "high", "明天"},
		{"回复读者邮件咨询（5封）", false, "medium", "今天"},
		{"更新AgentForge文档", true, "low", "已完成"},
		{"撰写墨远周报第49期", false, "medium", "周五"},
	}
	for i, t := range todos {
		d := 0
		if t.done {
			d = 1
		}
		if _, err := tx.Exec(`INSERT INTO wb_todos(owner,text,done,priority,due,pos) VALUES(?,?,?,?,?,?)`,
			owner, t.text, d, t.priority, t.due, i); err != nil {
			return err
		}
	}

	notifs := []struct{ icon, title, desc, time, typ string; unread bool }{
		{"message", "新评论提醒", "张三评论了你的文章《AI Agent开发》", "5分钟前", "comment", true},
		{"bell", "项目截止提醒", "AIWriter v2.0 距截止还有15天", "1小时前", "deadline", true},
		{"dollar", "收入到账", "训练营第4期学费¥2,800已到账", "3小时前", "income", true},
		{"email", "新订阅", "5位新用户订阅了墨远周报", "昨天", "subscribe", false},
		{"handshake", "合作邀约", "某科技公司邀请技术咨询服务", "昨天", "collaboration", false},
	}
	for i, n := range notifs {
		u := 0
		if n.unread {
			u = 1
		}
		if _, err := tx.Exec(`INSERT INTO wb_notifications(owner,icon,title,desc,time,type,unread,pos) VALUES(?,?,?,?,?,?,?,?)`,
			owner, n.icon, n.title, n.desc, n.time, n.typ, u, i); err != nil {
			return err
		}
	}

	income := []struct{ label string; value int; color string }{
		{"训练营", 9800, "#00d9ff"},
		{"1对1咨询", 4200, "#ffb547"},
		{"资源包", 2560, "#a855f7"},
		{"其他", 2000, "#22c55e"},
	}
	for i, v := range income {
		if _, err := tx.Exec(`INSERT INTO wb_income(owner,label,value,color,pos) VALUES(?,?,?,?,?)`,
			owner, v.label, v.value, v.color, i); err != nil {
			return err
		}
	}

	traffic := []struct{ source string; visits, pct int }{
		{"搜索引擎", 4520, 35},
		{"社交媒体", 3280, 26},
		{"直接访问", 2100, 16},
		{"推荐链接", 1850, 14},
		{"邮件", 980, 9},
	}
	for i, v := range traffic {
		if _, err := tx.Exec(`INSERT INTO wb_traffic(owner,source,visits,percentage,pos) VALUES(?,?,?,?,?)`,
			owner, v.source, v.visits, v.pct, i); err != nil {
			return err
		}
	}

	chart := []struct{ kind, label string; value int }{
		{"visits", "周一", 820}, {"visits", "周二", 932}, {"visits", "周三", 901},
		{"visits", "周四", 1290}, {"visits", "周五", 1330}, {"visits", "周六", 1520},
		{"visits", "周日", 1284},
		{"subscribes", "周一", 12}, {"subscribes", "周二", 15}, {"subscribes", "周三", 18},
		{"subscribes", "周四", 22}, {"subscribes", "周五", 19}, {"subscribes", "周六", 28},
		{"subscribes", "周日", 25},
	}
	for i, c := range chart {
		if _, err := tx.Exec(`INSERT INTO wb_chart(owner,kind,label,value,pos) VALUES(?,?,?,?,?)`,
			owner, c.kind, c.label, c.value, i); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ============ 概览卡片 ============

// OverviewItem 概览卡片
type OverviewItem struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Trend  string `json:"trend"`
	TrendUp bool  `json:"trendUp"`
	Icon   string `json:"icon"`
}

// GetOverview 读取概览卡片（按 pos 排序）
func (s *Store) GetOverview(owner string) ([]OverviewItem, error) {
	rows, err := s.conn.Query(`SELECT label,value,trend,trend_up,icon FROM wb_overview WHERE owner=? ORDER BY pos`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OverviewItem
	for rows.Next() {
		var it OverviewItem
		var up int
		if err := rows.Scan(&it.Label, &it.Value, &it.Trend, &up, &it.Icon); err != nil {
			return nil, err
		}
		it.TrendUp = up == 1
		items = append(items, it)
	}
	return items, rows.Err()
}

// ============ 项目 ============

// Project 项目
type Project struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Priority string `json:"priority"`
	Deadline string `json:"deadline"`
}

// GetProjects 读取项目列表
func (s *Store) GetProjects(owner string) ([]Project, error) {
	rows, err := s.conn.Query(`SELECT id,name,status,progress,priority,deadline FROM wb_projects WHERE owner=? ORDER BY pos`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Status, &p.Progress, &p.Priority, &p.Deadline); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

// CreateProject 新增项目
func (s *Store) CreateProject(owner string, p Project) (int64, error) {
	res, err := s.conn.Exec(`INSERT INTO wb_projects(owner,name,status,progress,priority,deadline,pos)
		VALUES(?,?,?,?,?,?,(SELECT COALESCE(MAX(pos)+1,0) FROM wb_projects WHERE owner=?))`,
		owner, p.Name, p.Status, p.Progress, p.Priority, p.Deadline, owner)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateProject 更新项目（按 owner 隔离）
func (s *Store) UpdateProject(owner string, id int64, p Project) error {
	res, err := s.conn.Exec(`UPDATE wb_projects SET name=?,status=?,progress=?,priority=?,deadline=? WHERE id=? AND owner=?`,
		p.Name, p.Status, p.Progress, p.Priority, p.Deadline, id, owner)
	if err != nil {
		return err
	}
	return checkAffected(res, id)
}

// DeleteProject 删除项目
func (s *Store) DeleteProject(owner string, id int64) error {
	res, err := s.conn.Exec(`DELETE FROM wb_projects WHERE id=? AND owner=?`, id, owner)
	if err != nil {
		return err
	}
	return checkAffected(res, id)
}

// ============ 内容 ============

// ContentItem 内容条目
type ContentItem struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Views  string `json:"views"`
	Date   string `json:"date"`
}

// GetContent 读取内容列表
func (s *Store) GetContent(owner string) ([]ContentItem, error) {
	rows, err := s.conn.Query(`SELECT id,title,type,status,views,date FROM wb_content WHERE owner=? ORDER BY pos`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ContentItem
	for rows.Next() {
		var c ContentItem
		if err := rows.Scan(&c.ID, &c.Title, &c.Type, &c.Status, &c.Views, &c.Date); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

// ============ 待办 ============

// Todo 待办
type Todo struct {
	ID       int64  `json:"id"`
	Text     string `json:"text"`
	Done     bool   `json:"done"`
	Priority string `json:"priority"`
	Due      string `json:"due"`
}

// GetTodos 读取待办列表
func (s *Store) GetTodos(owner string) ([]Todo, error) {
	rows, err := s.conn.Query(`SELECT id,text,done,priority,due FROM wb_todos WHERE owner=? ORDER BY pos`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Todo
	for rows.Next() {
		var t Todo
		var d int
		if err := rows.Scan(&t.ID, &t.Text, &d, &t.Priority, &t.Due); err != nil {
			return nil, err
		}
		t.Done = d == 1
		items = append(items, t)
	}
	return items, rows.Err()
}

// ToggleTodo 切换待办完成状态
func (s *Store) ToggleTodo(owner string, id int64) (bool, error) {
	var done int
	if err := s.conn.QueryRow(`SELECT done FROM wb_todos WHERE id=? AND owner=?`, id, owner).Scan(&done); err != nil {
		return false, err
	}
	next := 0
	if done == 0 {
		next = 1
	}
	if _, err := s.conn.Exec(`UPDATE wb_todos SET done=? WHERE id=? AND owner=?`, next, id, owner); err != nil {
		return false, err
	}
	return next == 1, nil
}

// CreateTodo 新增待办
func (s *Store) CreateTodo(owner string, t Todo) (int64, error) {
	d := 0
	if t.Done {
		d = 1
	}
	res, err := s.conn.Exec(`INSERT INTO wb_todos(owner,text,done,priority,due,pos)
		VALUES(?,?,?,?,?,(SELECT COALESCE(MAX(pos)+1,0) FROM wb_todos WHERE owner=?))`,
		owner, t.Text, d, t.Priority, t.Due, owner)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ============ 通知 ============

// Notification 通知
type Notification struct {
	ID     int64  `json:"id"`
	Icon   string `json:"icon"`
	Title  string `json:"title"`
	Desc   string `json:"desc"`
	Time   string `json:"time"`
	Type   string `json:"type"`
	Unread bool   `json:"unread"`
}

// GetNotifications 读取通知列表
func (s *Store) GetNotifications(owner string) ([]Notification, error) {
	rows, err := s.conn.Query(`SELECT id,icon,title,desc,time,type,unread FROM wb_notifications WHERE owner=? ORDER BY pos`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Notification
	for rows.Next() {
		var n Notification
		var u int
		if err := rows.Scan(&n.ID, &n.Icon, &n.Title, &n.Desc, &n.Time, &n.Type, &u); err != nil {
			return nil, err
		}
		n.Unread = u == 1
		items = append(items, n)
	}
	return items, rows.Err()
}

// ToggleNotificationRead 切换通知已读/未读
func (s *Store) ToggleNotificationRead(owner string, id int64) (bool, error) {
	var unread int
	if err := s.conn.QueryRow(`SELECT unread FROM wb_notifications WHERE id=? AND owner=?`, id, owner).Scan(&unread); err != nil {
		return false, err
	}
	next := 0
	if unread == 0 {
		next = 1
	}
	if _, err := s.conn.Exec(`UPDATE wb_notifications SET unread=? WHERE id=? AND owner=?`, next, id, owner); err != nil {
		return false, err
	}
	return next == 1, nil
}

// ============ 收入 ============

// IncomeItem 收入项
type IncomeItem struct {
	Label string `json:"label"`
	Value int    `json:"value"`
	Color string `json:"color"`
}

// GetIncome 读取收入统计
func (s *Store) GetIncome(owner string) ([]IncomeItem, error) {
	rows, err := s.conn.Query(`SELECT label,value,color FROM wb_income WHERE owner=? ORDER BY pos`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []IncomeItem
	for rows.Next() {
		var it IncomeItem
		if err := rows.Scan(&it.Label, &it.Value, &it.Color); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// ============ 流量 ============

// TrafficItem 流量来源
type TrafficItem struct {
	Source     string `json:"source"`
	Visits     int    `json:"visits"`
	Percentage int    `json:"percentage"`
}

// GetTraffic 读取流量来源
func (s *Store) GetTraffic(owner string) ([]TrafficItem, error) {
	rows, err := s.conn.Query(`SELECT source,visits,percentage FROM wb_traffic WHERE owner=? ORDER BY pos`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TrafficItem
	for rows.Next() {
		var it TrafficItem
		if err := rows.Scan(&it.Source, &it.Visits, &it.Percentage); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// ============ 图表 ============

// ChartData 访问趋势 / 订阅趋势
type ChartData struct {
	Labels []string `json:"labels"`
	Values []int    `json:"values"`
}

// GetChart 读取图表数据（kind: visits / subscribes）
func (s *Store) GetChart(owner, kind string) (ChartData, error) {
	rows, err := s.conn.Query(`SELECT label,value FROM wb_chart WHERE owner=? AND kind=? ORDER BY pos`, owner, kind)
	if err != nil {
		return ChartData{}, err
	}
	defer rows.Close()
	var cd ChartData
	for rows.Next() {
		var label string
		var value int
		if err := rows.Scan(&label, &value); err != nil {
			return ChartData{}, err
		}
		cd.Labels = append(cd.Labels, label)
		cd.Values = append(cd.Values, value)
	}
	return cd, rows.Err()
}

// checkAffected 校验受影响行数（无匹配行时报 not found）
func checkAffected(res sql.Result, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("record %d not found", id)
	}
	return nil
}
