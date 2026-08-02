package biz

import "time"

// ============================================================
// 业务模型（V1.0 必交付：RBAC / 订单 / 工单 / 资源 / 服务）
// 对应需求文档第 17 章（RBAC）、13 章（订单/工单）、15 章（资源）、16 章（服务）
// ============================================================

// ---------- RBAC（第 17 章 / AUTH-001）----------

// Role 角色
type Role struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`   // 唯一标识：super_admin / admin / operator / staff / customer / guest
	Name        string `json:"name"`   // 显示名：超级管理员 / 后台管理员 / 中台运营 / 服务人员 / 客户 / 访客
	Description string `json:"description"`
}

// Permission 权限点
type Permission struct {
	ID     int64  `json:"id"`
	Code   string `json:"code"`   // 如 order:read / order:write
	Name   string `json:"name"`   // 如 订单查看
	Module string `json:"module"` // 所属模块：order / ticket / resource / service / workbench / admin
}

// UserRole 用户-角色绑定
type UserRole struct {
	UserID string `json:"user_id"`
	RoleID int64  `json:"role_id"`
}

// RolePermission 角色-权限绑定
type RolePermission struct {
	RoleID       int64 `json:"role_id"`
	PermissionID int64 `json:"permission_id"`
}

// ---------- 订单（第 13 章 / ORD）----------

// 订单状态机：待支付 → 已支付 → 待交付 → 已交付 → 已完成；支持 取消/关闭/退款
const (
	OrderStatusPending   = "pending"   // 待支付
	OrderStatusPaid      = "paid"      // 已支付
	OrderStatusDelivering = "delivering" // 待交付/交付中
	OrderStatusDelivered = "delivered" // 已交付
	OrderStatusCompleted = "completed" // 已完成
	OrderStatusCancelled = "cancelled" // 已取消
	OrderStatusClosed    = "closed"    // 已关闭
	OrderStatusRefunding = "refunding" // 退款中
	OrderStatusRefunded  = "refunded"  // 已退款
)

// Order 订单
type Order struct {
	ID        int64     `json:"id"`
	OrderNo   string    `json:"order_no"`   // 订单号
	UserID    string    `json:"user_id"`    // 下单用户
	Type      string    `json:"type"`       // 商品/服务/课程/会员
	Title     string    `json:"title"`      // 商品标题
	Amount    int64     `json:"amount"`     // 金额（分）
	PayAmount int64     `json:"pay_amount"` // 实付（分）
	Status    string    `json:"status"`
	PayChannel string   `json:"pay_channel"` // 支付渠道（统一支付接口预留）
	PayNo     string    `json:"pay_no"`      // 支付流水号（回调预留）
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	PaidAt    *time.Time `json:"paid_at,omitempty"`
}

// OrderStatusLog 订单状态流转记录（第 22 章 业务日志）
type OrderStatusLog struct {
	ID        int64     `json:"id"`
	OrderID   int64     `json:"order_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Operator  string    `json:"operator"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// Refund 退款（ORD-106 / 第 18 章）
type Refund struct {
	ID         int64     `json:"id"`
	RefundNo   string    `json:"refund_no"`
	OrderID    int64     `json:"order_id"`
	UserID     string    `json:"user_id"`
	Amount     int64     `json:"amount"`
	Reason     string    `json:"reason"`
	Type       string    `json:"type"` // partial / full
	Status     string    `json:"status"` // pending / approved / rejected / completed
	AuditNote  string    `json:"audit_note"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ---------- 工单（第 13 章 / WO）----------

// 工单状态：待受理 → 处理中 → 待用户确认 → 已解决 → 已关闭；支持重开
const (
	TicketStatusPending   = "pending"   // 待受理
	TicketStatusHandling  = "handling"  // 处理中
	TicketStatusWaiting   = "waiting"   // 待用户确认
	TicketStatusResolved  = "resolved"  // 已解决
	TicketStatusClosed    = "closed"    // 已关闭
)

// Ticket 工单
type Ticket struct {
	ID         int64     `json:"id"`
	TicketNo   string    `json:"ticket_no"`
	UserID     string    `json:"user_id"`     // 提交人
	Title      string    `json:"title"`
	Category   string    `json:"category"`   // 分类
	Priority   string    `json:"priority"`   // high/medium/low
	Status     string    `json:"status"`
	AssigneeID string    `json:"assignee_id"` // 处理人（中台）
	OrderID    int64     `json:"order_id"`    // 关联订单（ORD-106 联动）
	SLADueAt   time.Time `json:"sla_due_at"`  // SLA 响应时限（启用 SLA）
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TicketReply 工单回复（WO-105：客户可见回复 + 内部备注）
type TicketReply struct {
	ID         int64     `json:"id"`
	TicketID   int64     `json:"ticket_id"`
	UserID     string    `json:"user_id"`
	Content    string    `json:"content"`
	IsInternal bool      `json:"is_internal"` // 内部备注客户不可见
	CreatedAt  time.Time `json:"created_at"`
}

// TicketEvaluation 工单满意度评价（WO-109 / 第 16 章统一评价）
type TicketEvaluation struct {
	ID            int64     `json:"id"`
	TicketID      int64     `json:"ticket_id"`
	Score         int       `json:"score"`   // 1-5
	Speed         int       `json:"speed"`   // 1-5
	Quality       int       `json:"quality"` // 1-5
	Communication int       `json:"communication"` // 1-5
	Comment       string    `json:"comment"`
	CreatedAt     time.Time `json:"created_at"`
}

// ---------- 资源（第 15 章 / RES）----------

// 开放策略（RES-201）：公开/登录可见/白名单/会员/付费/条件解锁
const (
	ResourcePolicyPublic     = "public"     // 公开
	ResourcePolicyLogin      = "login"      // 登录可见
	ResourcePolicyWhitelist  = "whitelist"  // 白名单
	ResourcePolicyMember     = "member"     // 会员
	ResourcePolicyPaid       = "paid"       // 付费
)

// 资源状态：草稿 → 待发布 → 已发布 → 下架（RES-209）
const (
	ResourceStatusDraft   = "draft"   // 草稿
	ResourceStatusPending = "pending" // 待审核
	ResourceStatusPublished = "published" // 已发布
	ResourceStatusOffline = "offline" // 下架
)

// Resource 资源
type Resource struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`   // 文档/图片/视频/音频/压缩包
	Description string    `json:"description"`
	Cover       string    `json:"cover"`
	FileID      string    `json:"file_id"`  // 关联 FileSvc 文件
	OwnerID     string    `json:"owner_id"` // 上传人
	Status      string    `json:"status"`
	Policy      string    `json:"policy"`   // 开放策略
	Price       int64     `json:"price"`    // 付费价格（分），付费策略时有效
	Downloads   int64     `json:"downloads"` // 下载次数
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ResourceWhitelist 白名单（RES-202）
type ResourceWhitelist struct {
	ID         int64  `json:"id"`
	ResourceID int64  `json:"resource_id"`
	UserID     string `json:"user_id"`
}

// ResourceDownloadLog 下载留痕（RES-109 / 第 22 章审计）
type ResourceDownloadLog struct {
	ID         int64     `json:"id"`
	ResourceID int64     `json:"resource_id"`
	UserID     string    `json:"user_id"`
	IP         string    `json:"ip"`
	DownloadedAt time.Time `json:"downloaded_at"`
}

// ---------- 服务（第 16 章 / SRV）----------

// 服务单状态机：待确认 → 待分配 → 交付中 → 待验收 → 完成 → 取消
const (
	SRVStatusWaitConfirm = "WAIT_CONFIRM" // 待需求确认
	SRVStatusWaitAssign  = "WAIT_ASSIGN"  // 待人员分配
	SRVStatusDelivering  = "DELIVERING"   // 交付中
	SRVStatusWaitAccept  = "WAIT_ACCEPT"  // 待验收
	SRVStatusCompleted   = "COMPLETED"    // 完成
	SRVStatusCancelled   = "CANCELLED"    // 取消
)

// ServiceCatalog 服务目录（SRV-101）
type ServiceCatalog struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"` // 6 类：咨询/开发定制/技术支持/课程培训/会员/定制解决方案
	Description string `json:"description"`
	Price       int64  `json:"price"` // 基础价（分）
	Cycle       string `json:"cycle"` // 服务周期
	Deliverable string `json:"deliverable"` // 交付内容
	SLA         string `json:"sla"`   // SLA 说明
	Status      string `json:"status"` // active / inactive
}

// ServiceSKU 服务商品化（SRV-102）
type ServiceSKU struct {
	ID         int64  `json:"id"`
	ServiceID  int64  `json:"service_id"`
	Name       string `json:"name"`  // 如 专业版
	Price      int64  `json:"price"` // 分
	Desc       string `json:"desc"`
}

// ServiceOrder 服务单（SRV-103）
type ServiceOrder struct {
	ID          int64     `json:"id"`
	SRVNo       string    `json:"srv_no"` // 如 SRV20260801
	OrderID     int64     `json:"order_id"` // 关联订单
	ServiceID   int64     `json:"service_id"`
	SKUID       int64     `json:"sku_id"`
	UserID      string    `json:"user_id"`
	OwnerID     string    `json:"owner_id"` // 负责人（服务人员）
	Status      string    `json:"status"`
	Requirement string    `json:"requirement"` // 需求描述
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ServiceMilestone 交付里程碑（SRV-104 / 16.2）
type ServiceMilestone struct {
	ID          int64     `json:"id"`
	ServiceOrderID int64   `json:"service_order_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"` // pending / doing / done
	DueAt       time.Time `json:"due_at"`
}

// ServiceAcceptance 验收记录（SRV-105）
type ServiceAcceptance struct {
	ID             int64     `json:"id"`
	ServiceOrderID int64     `json:"service_order_id"`
	Result         string    `json:"result"` // pass / reject
	Note           string    `json:"note"`
	AcceptedAt     time.Time `json:"accepted_at"`
}
