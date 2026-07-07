# 上传按钮悬停展开 UI 改进计划

## Context

用户反馈当前两个并列的上传按钮（"+ 选择文件"和"+ 上传文件夹"）占位较多，建议改为：鼠标移到上传组件时，再展开显示"选择文件"和"上传文件夹"两个选项。这样更简洁美观。

### 当前 UI 结构

[web/index.html:52-79](file:///d:/STUDY/GO/StudyGolang/firstGolang/filesync/web/index.html#L52-L79) 的 `.upload-controls` 容器（flex 布局）中，两个 `.pick-btn` 按钮并列显示：

```html
<label class="pick-btn" id="pick-btn" title="选择文件上传">+ 选择文件</label>
<input type="file" id="file-input" multiple hidden>
<label class="pick-btn" id="folder-pick-btn" title="选择文件夹上传（保留目录结构）">+ 上传文件夹</label>
<input type="file" id="folder-input" webkitdirectory multiple hidden>
```

[web/style.css:237-253](file:///d:/STUDY/GO/StudyGolang/firstGolang/filesync/web/style.css#L237-L253) 的 `.pick-btn` 样式为普通边框按钮，hover 时变 accent 色。

## 改进方案：悬停展开下拉菜单

将两个按钮合并为一个下拉菜单组件，默认只显示"+ 上传"主按钮，悬停时展开两个选项。

### 改动 1：index.html 重构上传按钮区域

[web/index.html:75-78](file:///d:/STUDY/GO/StudyGolang/firstGolang/filesync/web/index.html#L75-L78) 替换为：

```html
<div class="upload-dropdown" id="upload-dropdown">
    <label class="pick-btn upload-trigger" title="上传文件或文件夹">+ 上传 ▾</label>
    <div class="upload-menu">
        <label class="upload-menu-item" id="pick-btn" title="选择文件上传">
            <svg class="upload-menu-icon" width="14" height="14"><use href="#icon-file"/></svg>
            选择文件
        </label>
        <label class="upload-menu-item" id="folder-pick-btn" title="选择文件夹上传（保留目录结构）">
            <svg class="upload-menu-icon" width="14" height="14"><use href="#icon-folder"/></svg>
            上传文件夹
        </label>
    </div>
    <input type="file" id="file-input" multiple hidden>
    <input type="file" id="folder-input" webkitdirectory multiple hidden>
</div>
```

### 改动 2：style.css 添加下拉菜单样式

在 [web/style.css:253](file:///d:/STUDY/GO/StudyGolang/firstGolang/filesync/web/style.css#L253) `.pick-btn:focus-visible` 之后追加：

```css
/* === 上传下拉菜单（悬停展开） === */
.upload-dropdown {
    position: relative;
    display: inline-block;
}
.upload-trigger {
    /* 复用 .pick-btn 样式，额外加箭头 */
    white-space: nowrap;
}
.upload-menu {
    position: absolute;
    top: 100%;
    left: 0;
    margin-top: 4px;
    background: var(--bg-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 4px;
    min-width: 160px;
    z-index: 100;
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
    opacity: 0;
    visibility: hidden;
    transform: translateY(-4px);
    transition: opacity 0.15s, transform 0.15s, visibility 0.15s;
}
.upload-dropdown:hover .upload-menu,
.upload-dropdown:focus-within .upload-menu {
    opacity: 1;
    visibility: visible;
    transform: translateY(0);
}
.upload-menu-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    cursor: pointer;
    border-radius: var(--radius-sm);
    font-family: var(--mono);
    font-size: 11px;
    color: var(--fg-1);
    transition: background 0.15s, color 0.15s;
    white-space: nowrap;
}
.upload-menu-item:hover {
    background: var(--bg-3);
    color: var(--accent);
}
.upload-menu-icon {
    flex-shrink: 0;
    opacity: 0.8;
}
.upload-menu-item:hover .upload-menu-icon {
    opacity: 1;
}
```

### 改动 3：app.js 事件绑定（无需改动）

[web/app.js:2711-2749](file:///d:/STUDY/GO/StudyGolang/firstGolang/filesync/web/app.js#L2711-L2749) 中 `pick-btn` 和 `folder-pick-btn` 的 click 事件绑定逻辑不变，因为：
- 两个 label 元素的 id 保持不变（`pick-btn` 和 `folder-pick-btn`）
- 点击 label 仍会触发对应 input 的 click
- CSS `:hover` 处理菜单展开，不需要 JS

### 关键设计决策

1. **纯 CSS 悬停**：用 `:hover` 和 `:focus-within` 实现展开，无需 JS 事件
2. **保留 id**：`pick-btn` 和 `folder-pick-btn` 的 id 不变，事件绑定代码零改动
3. **复用图标**：使用已有的 `#icon-file` 和 `#icon-folder` SVG symbol
4. **动画过渡**：opacity + transform 实现淡入上滑效果
5. **箭头指示**：主按钮文案"+ 上传 ▾"用下三角箭头暗示可展开

## 验证步骤

1. 本地启动 server，访问 http://localhost:8080/web/
2. 验证默认只显示"+ 上传 ▾"一个按钮
3. 鼠标移到按钮上，验证下拉菜单展开显示"选择文件"和"上传文件夹"
4. 点击"选择文件"，验证触发文件选择对话框
5. 点击"上传文件夹"，验证触发文件夹选择对话框
6. 移开鼠标，验证菜单自动收起
7. 用 Tab 键聚焦到下拉菜单，验证 `:focus-within` 也能展开菜单（键盘可访问性）
8. 运行 `node tools/minify/minify.js` 同步 dist
9. 部署到生产服务器
10. 生产环境验证

## 关键文件清单

| 文件 | 改动类型 | 说明 |
|---|---|---|
| `web/index.html` | 修改 | 将两个并列按钮重构为下拉菜单结构 |
| `web/style.css` | 添加 | 新增 .upload-dropdown / .upload-menu / .upload-menu-item 样式 |
| `web/app.js` | 零改动 | 事件绑定逻辑不变（id 保持一致） |
| `web/dist/*` | minify 同步 | 运行 minify 工具生成 |
