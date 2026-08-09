# Landing 落地页设计（intro.html）

> 覆盖 MASTER.md。模式：**Feature-Rich Showcase + Demo**（Hero > Features > CTA）
> 追加：信任区块（Real-Time/Ops 风格带实时指标，呼应"安全云存储"）

## 布局

```
Hero（全宽，浅蓝描边 + 大字标题 + 主 CTA + 副 CTA）
│
├ 信任条：技术徽章（断点续传/SHA256/JWT/OAuth2）
├ Features 3 列网格：核心功能
├ 安全防护段落（左右分栏：文字 + 图示）
├ 技术架构段落（栈徽章条）
├ 实时指标条（当前用户/文件/传输状态 — 可做静态）
└ CTA 收尾（蓝实底大按钮）
Footer（产品名 + 链接 + 版权）
```

## 关键规则

1. **Hero**：主标题 38px 600，副标题 18px muted；CTA 放置首屏（above fold）+ 导航栏 sticky
2. **主 CTA**：`.btn-primary`（蓝实底 `#2563EB`），副 CTA = `.btn-secondary`（描边蓝）
3. **Features**：3 卡片网格，白底 `--shadow-md` hover 上浮 2px；卡内图标 = 实色圆角底（`#2563EB18` + 蓝图标）
4. **安全区块**：左右两栏（>768px），左文字 + checklist（✓ 图标 + 文本），右图示
5. **技术徽章**：芯片式（Float 边框 1px + muted bg 胶囊状，字体 mono 13px）
6. **信任信号**：状态灯圆点（绿=在线/已签名）+ 实时更新条
7. 间距：区块 `--space-3xl` (64px) 垂直；移动端压缩至 32-40px
8. 禁用滚动触发重动画（word：简洁简洁，纯 CSS 淡入即可）

## 禁止项

- ❌ 过度动画/滚动展示（scroll-triggered storytelling 不适用工具类落地页）
- ❌ 复杂阴影/3D
- ❌ 虚假实时数据（如非真实统计则禁用闪烁数字）
- ❌ emoji 图标（全 SVG）