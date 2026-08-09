# Auth 页面设计（login / register / forgot-password / reset-password / activate）

> 覆盖 MASTER.md。模式：分栏认证卡。

## 布局

```
┌──────────────────────┬──────────────────────┐
│  左：品牌区 (48%)     │  右：表单区 (52%)     │
│  logo + 产品名        │  居中单卡 420px max    │
│  一句价值主张         │  账号/密码字段        │
│  3 条安全亮点（图标）  │  主 CTA（蓝实底）     │
│  底部法律小字          │  辅助链接（注册/忘记）│
└──────────────────────┴──────────────────────┘
```

移动端（<768px）：左品牌区隐藏，仅表单区 + 顶部 logo。

## 关键规则

1. **表单卡片**：白底圆角 12px `--shadow-md`，内距 32px；容器居中 `max-width:420px; margin:auto`
2. **卡片外底色**：`--color-muted`（`#F1F5FD`）或浅蓝渐变背景（品牌区分栏用 `--color-primary`，白字）
3. **输入框**：16px 字号防 iOS 缩放；`autocomplete` 正确（email / current-password / new-password）
4. **密码框**：必须带 👁 显示/隐藏切换按钮（SVG 图标，非 emoji）
5. **Label**：`<label for>` 显式关联，禁止 placeholder 当 label
6. **提交按钮**：`.btn-primary` 全宽 48px 高；提交中显示 loading 态（spinner+禁用）
7. **错误提示**：字段旁红色 `#DC2626` 文案 + 输入框红色边框，非仅顶部汇总
8. **登录提示按钮** 100% 可点击区（cursor:pointer）
9. 验证码输入：6 位分隔样式（`letter-spacing: 0.5em`）或 6 个单格
10. 忘记密码成功页、激活结果页：居中 emoji 无 —— 用 Lucide 状态图标（✓ / ✗）卡片

## 禁止项

- ❌ placeholder 代替 label
- ❌ 密码框无可见/隐藏切换
- ❌ 移动端无触控字号（<16px 输入框 → iOS 自动缩放）
- ❌ 状态反馈仅在 0ms 突变（过渡 150-250ms）