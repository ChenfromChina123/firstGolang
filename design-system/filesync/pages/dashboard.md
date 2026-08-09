# Dashboard — 主控制台页面设计（index.html）

> 覆盖 MASTER.md 的页面级规则。**模式：Flat Design**（2D、无阴影、粗线条、图标密集、排版为王）

## 差异点（相对 MASTER）

| 维度 | MASTER | Dashboard 页面 |
|------|--------|----------------|
| 整体风格 | Minimalism & Swiss | Flat Design（density 6/10） |
| 字体 | Plus Jakarta Sans | Fira Code（数据）+ Fira Sans（界面文本） |
| 效果 | 温和阴影 | 无渐变/无阴影，hover 仅颜色/透明度变化 |

## 布局

```
┌──────────────────────────────────────────────┐
│ Sidebar 248px │  Topbar sticky (health+user) │
│ ├ Upload      │ ┌───────────────────────────┐│
│ ├ New folder  │ │ 面包屑 空间名 > 目录      ││
│ ├ Transfers   │ │ 文件表格/网格区           ││
│ ├ Shares      │ │ （上传队列浮层）           ││
│ ├ Recycle bin │ └───────────────────────────┘│
│ └ Settings    │                              │
└──────────────────────────────────────────────┘
```

## 关键规则

1. **侧边栏**：宽 248px，白底，`--color-border` 右边框；激活项 = `--color-primary` 底 + 白字（Flat 风格用实色块不用阴影）
2. **顶栏 sticky**：`backdrop-filter: blur(8px)` + 半透明白 `rgba(255,255,255,0.85)`；body 补 padding-top = 顶栏高度（防遮挡）
3. **文件列表行**：hover `--color-muted`；状态徽章：同步中=蓝 `#2563EB`、完成=绿 `#16A34A`、失败=红 `#DC2626`、等待=灰
4. **上传队列**：右侧浮层卡片（Flat：白底 + 2px 实线边框 `#E4ECFC`），进度条 = 实色蓝块推进，无扫描线动画
5. **传输中心**：表格容器 8px 圆角，表头 `--color-muted` + 600 字重
6. **空间选择器**：顶部下拉卡片，显示用量进度条（蓝）
7. **树形文件库**：文件夹=蓝文件夹图标（stroke 图标），文件=琥珀文件图标；类型图标容器为实色圆角方块（`#2563EB1A` 底 + `#2563EB` 图标、琥珀同理）
8. **面包屑**：深度 ≥3 显示 首页 > 文件夹A > 文件夹B
9. 移动端：汉堡抽屉 + 底部导航 ≤5 项；安全区适配

## 组件特例

```css
--sidebar-width: 248px;
--topbar-height: 56px;
/* Flat 风格卡片：白底 #FFF + 1px 边框 #E4ECFC，无阴影 */
.flat-card { background:#fff; border:1px solid #E4ECFC; border-radius:8px; }
```

## 禁止项

- ❌ 扫描线/glow 动画（旧终端风残留）
- ❌ 依赖 hover 才可见的操作（移动端无 hover）
- ❌ 透明渐变 topbar 底色盖内容（半透明白 → 实白）
- ❌ emoji 图标（统一 Lucide 风格 SVG stroke 1.75）