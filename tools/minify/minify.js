/**
 * FileSync 前端压缩工具
 *
 * 将 web/ 中的 JS/CSS/HTML 压缩到 web/dist/，保留原文件用于开发。
 * 压缩策略（仅 minify，不混淆）：
 *   - JS：移除注释/空白，缩短局部变量名（mangle），保留函数名和字符串
 *   - CSS：移除注释/空白，合并相邻规则，压缩颜色值
 *   - HTML：移除注释/空白，保留 <pre>/<textarea> 内容，保留 meta 标签
 *
 * 用法：node minify.js
 * 输出：../../web/dist/（与 web/ 同结构，文件名相同）
 */

const fs = require('fs');
const path = require('path');
const { minify: terserMinify } = require('terser');
const CleanCSS = require('clean-css');
const { minify: htmlMinify } = require('html-minifier-terser');

// 路径配置
const WEB_SRC = path.resolve(__dirname, '../../web');
const WEB_DIST = path.resolve(__dirname, '../../web/dist');

// 需要压缩的文件清单
const FILES = {
    js: ['app.js', 'share.js'],
    css: ['style.css'],
    html: [
        'index.html', 'share.html', 'login.html',
        'register.html', 'forgot-password.html',
        'reset-password.html', 'activate.html'
    ]
};

/**
 * 压缩单个 JS 文件
 */
async function minifyJS(srcPath, distPath) {
    const code = fs.readFileSync(srcPath, 'utf8');
    const result = await terserMinify(code, {
        compress: {
            drop_console: false,    // 保留 console.log（生产调试用）
            drop_debugger: true,    // 移除 debugger 语句
            passes: 2               // 两轮压缩优化
        },
        mangle: {
            toplevel: false,        // 不混淆顶层变量（保持 IIFE 结构清晰）
            keep_fnames: true       // 保留函数名（便于错误堆栈定位）
        },
        format: {
            comments: false         // 移除所有注释
        }
    });
    if (result.error) throw result.error;
    fs.writeFileSync(distPath, result.code, 'utf8');
    return { src: Buffer.byteLength(code), dist: Buffer.byteLength(result.code) };
}

/**
 * 压缩单个 CSS 文件
 */
function minifyCSS(srcPath, distPath) {
    const source = fs.readFileSync(srcPath, 'utf8');
    const result = new CleanCSS({
        level: {
            1: {
                all: true,                  // 基础优化（空白、注释、颜色）
                specialComments: 0          // 移除所有注释（包括 /*! */）
            },
            2: {
                mergeMedia: true,           // 合并 @media 规则
                removeDuplicateFontRules: true,
                removeDuplicateRules: true
            }
        },
        returnPromise: false
    }).minify(source);

    if (result.errors && result.errors.length > 0) {
        throw new Error(`CSS errors: ${result.errors.join(', ')}`);
    }
    const output = result.styles;
    fs.writeFileSync(distPath, output, 'utf8');
    return { src: Buffer.byteLength(source), dist: Buffer.byteLength(output) };
}

/**
 * 压缩单个 HTML 文件
 */
async function minifyHTML(srcPath, distPath) {
    const source = fs.readFileSync(srcPath, 'utf8');
    const output = await htmlMinify(source, {
        collapseWhitespace: true,           // 折叠空白
        removeComments: true,               // 移除 HTML 注释
        removeRedundantAttributes: false,   // 保留冗余属性（避免破坏样式钩子）
        removeScriptTypeAttributes: true,   // 移除 type="text/javascript"
        removeStyleLinkTypeAttributes: true,// 移除 type="text/css"
        minifyCSS: true,                    // 内联 CSS 也压缩
        minifyJS: true,                     // 内联 JS 也压缩
        useShortDoctype: true,              // <!DOCTYPE html>
        collapseBooleanAttributes: true,    // disabled="disabled" → disabled
        sortAttributes: false,              // 不重排属性顺序（保持可读性）
        sortClassName: false                // 不重排 class 顺序
    });
    fs.writeFileSync(distPath, output, 'utf8');
    return { src: Buffer.byteLength(source), dist: Buffer.byteLength(output) };
}

/**
 * 格式化文件大小
 */
function fmtSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    return (bytes / 1024).toFixed(1) + ' KB';
}

/**
 * 主函数：创建 dist 目录，压缩所有文件，输出统计
 */
async function main() {
    console.log('=== FileSync 前端压缩工具 ===\n');

    // 确保 dist 目录存在
    if (!fs.existsSync(WEB_DIST)) {
        fs.mkdirSync(WEB_DIST, { recursive: true });
    }

    let totalSrc = 0, totalDist = 0;
    const stats = [];

    // 压缩 JS
    console.log('--- JS 压缩 ---');
    for (const file of FILES.js) {
        const srcPath = path.join(WEB_SRC, file);
        const distPath = path.join(WEB_DIST, file);
        if (!fs.existsSync(srcPath)) {
            console.log(`  跳过（不存在）: ${file}`);
            continue;
        }
        const { src, dist } = await minifyJS(srcPath, distPath);
        const ratio = ((1 - dist / src) * 100).toFixed(1);
        console.log(`  ${file}: ${fmtSize(src)} → ${fmtSize(dist)} (-${ratio}%)`);
        stats.push({ file, src, dist });
        totalSrc += src;
        totalDist += dist;
    }

    // 压缩 CSS
    console.log('\n--- CSS 压缩 ---');
    for (const file of FILES.css) {
        const srcPath = path.join(WEB_SRC, file);
        const distPath = path.join(WEB_DIST, file);
        if (!fs.existsSync(srcPath)) continue;
        const { src, dist } = minifyCSS(srcPath, distPath);
        const ratio = ((1 - dist / src) * 100).toFixed(1);
        console.log(`  ${file}: ${fmtSize(src)} → ${fmtSize(dist)} (-${ratio}%)`);
        stats.push({ file, src, dist });
        totalSrc += src;
        totalDist += dist;
    }

    // 压缩 HTML
    console.log('\n--- HTML 压缩 ---');
    for (const file of FILES.html) {
        const srcPath = path.join(WEB_SRC, file);
        const distPath = path.join(WEB_DIST, file);
        if (!fs.existsSync(srcPath)) continue;
        const { src, dist } = await minifyHTML(srcPath, distPath);
        const ratio = ((1 - dist / src) * 100).toFixed(1);
        console.log(`  ${file}: ${fmtSize(src)} → ${fmtSize(dist)} (-${ratio}%)`);
        stats.push({ file, src, dist });
        totalSrc += src;
        totalDist += dist;
    }

    // 汇总
    const totalRatio = ((1 - totalDist / totalSrc) * 100).toFixed(1);
    console.log('\n=== 汇总 ===');
    console.log(`  源文件总大小: ${fmtSize(totalSrc)}`);
    console.log(`  压缩后总大小: ${fmtSize(totalDist)}`);
    console.log(`  总压缩率:     -${totalRatio}%`);
    console.log(`  输出目录:     ${WEB_DIST}`);
    console.log('\n=== 压缩完成 ===');
}

main().catch(err => {
    console.error('压缩失败:', err);
    process.exit(1);
});
