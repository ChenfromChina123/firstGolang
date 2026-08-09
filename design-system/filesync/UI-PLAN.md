# FileSync 全新 UI 方案总览

> 由 ui-ux-pro-max skill 生成，2026-08-09
> 本方案为**全新重设计**，与现有"终端深色风"（style.css v1）完全替换，不复用旧视觉。

---

## 1. 设计方向

| 维度 | 旧 UI（终端风） | 新 UI（瑞士极简 / Minimalism & Swiss Style） |
|------|-----------------|---------------------------------------------|
| 基调 | 深色炭灰 + 电光青 glow | **浅色底 #F8FAFC + 专业蓝 #2563EB** |
| 风格 | 终端/极客、glow 光晕、扫描线 | 干净、留白、几何网格、高对比、功能至上 |
| 字体 | JetBrains Mono + Outfit | **Plus Jakarta Sans**（友好现代 SaaS）+ JetBrains Mono 仅用于哈希/代码 |
| 图标 | 自绘绿青 SVG | **Lucide 风格**描边图标（stroke 1.75, 24px） |
| 卡片 | 4-6px 圆角暗色面板 | 8-16px 圆角白卡 + 柔和分层阴影 |
| 情感 | 技术极客 | **企业级信任感、专业清晰**（WCAG AAA） |

**理由**：FileSync 是"带支付级安全的文件同步服务"，瑞士极简被 skill 推荐给 Enterprise apps / SaaS / professional tools，浅色 + 高对比强化安全可信感，与旧终端风形成完全区隔。

---

## 2. 设计令牌（Design Tokens）

### 颜色

| 角色 | Hex | CSS 变量 |
|------|-----|----------|
| Primary | `#2563EB` | `--color-primary` |
| On Primary | `#FFFFFF` | `--color-on-primary` |
| Secondary | `#3B82F6` | `--color-secondary` |
| Accent | `#D97706` | `--color-accent` |
| Background | `#F8FAFC` | `--color-background` |
| Foreground | `#0F172A` | `--color-foreground` |
| Muted | `#F1F5FD` | `--color-muted` |
| Border | `#E4ECFC` | `--color-border` |
| Destructive | `#DC2626` | `--color-destructive` |
| Ring | `#2563EB` | `--color-ring` |
| Success | `#16A34A` | `--color-success` |
| Warning | `#D97706` | `--color-warning` |

语义：**文件夹蓝 + 文件琥珀**——蓝色用作主操作/导航，琥珀用作文件/高亮 CTA，绿色 = 成功/同步完成，红色 = 错误/删除。

### 字体

```css
@import url('https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@300;400;500;600;700&display=swap');
/* 仅代码/哈希保留 JetBrains Mono */
--font-heading: 'Plus Jakarta Sans', system-ui, sans-serif;
--font-body: 'Plus Jakarta Sans', system-ui, sans-serif;
--font-mono: 'JetBrains Mono', ui-monospace, monospace;
```

字号阶梯：`13 / 14 / 16 / 20 / 24 / 30 / 38px`，行高 1.5，正文基字号 16px。

### 间距 / 圆角 / 阴影

| 层级 | 值 |
|------|-----|
| `--space-xs/sm/md/lg/xl/2xl/3xl` | 4 / 8 / 16 / 24 / 32 / 48 / 64px |
| `--radius-sm/md/lg/xl` | 6 / 8 / 12 / 16px |
| `--shadow-sm/md/lg/xl` | 见 MASTER.md（卡片 hover 用 lg，modal 用 xl） |

### 动效

- 所有过渡 **150-250ms ease**；hover 用 `opacity / translateY(-1px)`，禁 scale 变形（防布局抖动）
- 点击按压：`transform: scale(0.97)` 立即反馈
- 尊重 `prefers-reduced-motion`

---

## 3. 核心组件规范

### 按钮
- `.btn-primary`：蓝底白字 12x24px 圆角 8 号 600 字重；hover 淡 90% + 上浮 1px
- `.btn-secondary`：透明底蓝描边 2px
- `.btn-ghost`：透明，hover 蒙层 #2563EB0F
- `.btn-danger`：`#DC2626` 底
- 所有可点元素 `cursor: pointer`

### 卡片
```
白底 → 实际用 var(--color-background) 卡片装 #FFFFFF 内容? 
统一：卡片 = #FFFFFF 底 + --shadow-md，hover → --shadow-lg + translateY(-2px)
```

### 输入框
16px 字号、12px 16px 内距、聚焦 = 蓝色描边 + `0 0 0 3px #2563EB20` 焦点环
密码框必须带显示/隐藏切换、autocomplete 属性正确（email/current-password）

### 模态框
`rgba(15,23,42,0.5)` 遮罩 + blur(4px)，白底 16px 圆角 xl 阴影

### 导航
- 桌面：左侧固定侧边栏（宽 248px，白底，底边框分隔），顶栏 sticky
- 顶栏 sticky 时 body 需补 padding-top 等高于防止遮挡
- 面包屑：深度 ≥3 必须显示
- 移动端：底部导航 ≤5 项 + 汉堡抽屉

### 表格（admin）
- 表头 muted 底 + 600 字重，行 hover `#F8FAFC`（即白底卡上浅蓝），圆角 8px 容器

---

## 4. 各页面设计规范

| 页面 | 布局模式 | 关键元素 |
|------|----------|----------|
| `login/register/forgot/reset/activate` | 居中单列认证卡（max-width 420px）+ 左侧品牌区 | 品牌渐变 → 纯蓝 logo、提醒卡片、验证码计数 |
| `index`（主控制台） | 侧边栏 + 顶栏 + 文件区 | 上传队列卡片、传输中心标签、状态徽章（同步中=蓝、完成=绿、失败=红） |
| `admin` | KPI 卡片行 + 数据表格 Tab | 4 个统计卡（总览/用户/文件/分享）、行内操作按钮 |
| `share` | 居中单卡（max-width 720px）+ 密码门 | 分享者信息头部、文件列表卡、预览区 |
| `intro`（落地页） | Hero + Features 3 列 + CTA | 首屏大标题 + 主 CTA，功能 3 卡网格，收尾 CTA |

## 5. 响应式断点

375 / 768 / 1024 / 1440px；移动端无横向滚动、禁双击缩放、safe-area。

## 6. 无障碍（WCAG AAA）

- 正文对比 ≥ 4.5:1（浅底深字达标：`#0F172A` on `#F8FAFC` = 15.9:1）
- 焦点环始终可见（蓝色 3px ring）
- 图标不单靠颜色/图标传达（配文字标签）
- `prefers-reduced-motion` 关闭装饰动画

## 7. 禁止项（Anti-Patterns）

❌ 不要渐变、3D、复杂阴影 丨 ❌ 不用 emoji 当图标 丨 ❌ 无 focus 环
❌ 0ms 状态突变 ❌ 占位符当 label ❌ 移动端横向滚动  ❌ 图标按钮无文字提示

## 8. 页面覆盖文件

本目录下 `pages/` 中每个页面文件存在时覆盖 MASTER：
- `pages/dashboard.md` — 主控制台（index.html）
- `pages/auth.md` — 五个认证页
- `pages/admin.md` — 管理后台
- `pages/share.md` — 分享页
- `pages/landing.md` — intro 落地页