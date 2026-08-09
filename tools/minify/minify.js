/**
 * FileSync 前端压缩 + 混淆工具
 *
 * 将 web/ 中的 JS/CSS/HTML 处理到 web/dist/，保留原文件用于开发。
 * 处理策略：
 *   - JS（项目自有代码）：javascript-obfuscator 混淆（字符串 base64 编码、局部变量十六进制重命名、代码紧凑化）
 *   - CSS：移除注释/空白，合并相邻规则，压缩颜色值（clean-css）
 *   - HTML：移除注释/空白，保留 <pre>/<textarea> 内容，保留 meta 标签（html-minifier-terser）
 *   - 静态资源（lib/、img/、js/lib/）：原样复制，使 dist 可独立部署
 *
 * 混淆注意：
 *   - crypto.js 是跨文件共享的全局 API（login/register/admin 等页面都引用其全局函数），
 *     必须 renameGlobals:false，否则其它页面引用会断裂。
 *   - 其余页面脚本是自包含的（只引用第三方库的全局），可安全 renameGlobals:true。
 *
 * 用法：node minify.js
 * 输出：../../web/dist/（与 web/ 同结构，文件名相同）
 */

const fs = require('fs');
const path = require('path');
const JavaScriptObfuscator = require('javascript-obfuscator');
const CleanCSS = require('clean-css');
const { minify: htmlMinify } = require('html-minifier-terser');

// 路径配置
const WEB_SRC = path.resolve(__dirname, '../../web');
const WEB_DIST = path.resolve(__dirname, '../../web/dist');

// 需要混淆的 JS 文件（项目自有代码）
//   [文件名, 是否重命名顶层全局变量]
//   renameGlobals:true  = 页面自包含脚本（仅引用第三方库全局，如 CodeMirror/marked/Hls/Prism）
//   renameGlobals:false = 跨文件共享全局的脚本（如 crypto.js 被多个页面引用）
const JS_FILES = [
    ['app.js', true],
    ['admin.js', true],
    ['share.js', true],
    ['sha256.worker.js', true],
    ['js/crypto.js', false],
    ['js/login.js', true],
    ['js/register.js', true],
    ['js/activate.js', true],
    ['js/forgot-password.js', true],
    ['js/reset-password.js', true]
];

// 需要压缩的 CSS 文件
const CSS_FILES = ['style.css'];

// 需要压缩的 HTML 文件
const HTML_FILES = [
    'index.html', 'share.html', 'login.html',
    'register.html', 'forgot-password.html',
    'reset-password.html', 'activate.html',
    'admin.html', 'intro.html'
];

// 原样复制的静态资源目录（第三方库/图片，保持 dist 可独立部署）
const COPY_DIRS = ['lib', 'img', 'js/lib'];

/**
 * 混淆单个 JS 文件（javascript-obfuscator）
 */
function obfuscateJS(srcPath, distPath, renameGlobals) {
    const code = fs.readFileSync(srcPath, 'utf8');
    const result = JavaScriptObfuscator.obfuscate(code, {
        compact: true,                    // 输出紧凑代码（等价于 minify）
        controlFlowFlattening: false,     // 关闭控制流平坦化（保持性能，避免极端情况破坏逻辑）
        deadCodeInjection: false,         // 不注入死代码（控制体积）
        disableConsoleOutput: false,      // 保留 console（生产调试）
        identifierNamesGenerator: 'hexadecimal', // 局部变量/函数用十六进制短名
        renameGlobals: renameGlobals,     // 是否重命名顶层全局（见文件头注释）
        selfDefending: false,             // 关闭自防御（避免格式化即报错的误伤）
        stringArray: true,                // 字符串抽离到数组
        stringArrayEncoding: ['base64'],  // base64 编码字符串
        stringArrayThreshold: 0.75,       // 75% 字符串进数组
        rotateStringArray: true,          // 随机旋转数组顺序
        shuffleStringArray: true,         // 打乱数组顺序
        splitStrings: false,              // 不拆分字符串（控制体积）
        transformObjectKeys: false,       // 不转换对象键（保持 API 字段可读、避免动态访问断裂）
        unicodeEscapeSequence: false,     // 中文不转义 unicode（控制体积）
        simplify: true,                   // AST 简化
        target: 'browser'
    });
    fs.writeFileSync(distPath, result.getObfuscatedCode(), 'utf8');
    return { src: Buffer.byteLength(code), dist: Buffer.byteLength(result.getObfuscatedCode()) };
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
 * 原样复制目录（保留相对结构）
 */
function copyDir(srcDir, distDir) {
    if (!fs.existsSync(srcDir)) return 0;
    let count = 0;
    const walk = (s, d) => {
        if (!fs.existsSync(d)) fs.mkdirSync(d, { recursive: true });
        for (const entry of fs.readdirSync(s, { withFileTypes: true })) {
            const sp = path.join(s, entry.name);
            const dp = path.join(d, entry.name);
            if (entry.isDirectory()) walk(sp, dp);
            else { fs.copyFileSync(sp, dp); count++; }
        }
    };
    walk(srcDir, distDir);
    return count;
}

/**
 * 格式化文件大小
 */
function fmtSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    return (bytes / 1024).toFixed(1) + ' KB';
}

/**
 * 主函数：创建 dist 目录，混淆/压缩/复制所有文件，输出统计
 */
async function main() {
    console.log('=== FileSync 前端压缩 + 混淆工具 ===\n');

    // 确保 dist 目录存在
    if (!fs.existsSync(WEB_DIST)) {
        fs.mkdirSync(WEB_DIST, { recursive: true });
    }

    let totalSrc = 0, totalDist = 0;
    const stats = [];

    // 混淆 JS
    console.log('--- JS 混淆 ---');
    for (const [file, renameGlobals] of JS_FILES) {
        const srcPath = path.join(WEB_SRC, file);
        const distPath = path.join(WEB_DIST, file);
        if (!fs.existsSync(srcPath)) {
            console.log(`  跳过（不存在）: ${file}`);
            continue;
        }
        fs.mkdirSync(path.dirname(distPath), { recursive: true });
        const { src, dist } = obfuscateJS(srcPath, distPath, renameGlobals);
        const ratio = ((1 - dist / src) * 100).toFixed(1);
        console.log(`  ${file}: ${fmtSize(src)} → ${fmtSize(dist)} (-${ratio}%)`);
        stats.push({ file, src, dist });
        totalSrc += src;
        totalDist += dist;
    }

    // 压缩 CSS
    console.log('\n--- CSS 压缩 ---');
    for (const file of CSS_FILES) {
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
    for (const file of HTML_FILES) {
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

    // 复制静态资源
    console.log('\n--- 静态资源复制 ---');
    for (const dir of COPY_DIRS) {
        const n = copyDir(path.join(WEB_SRC, dir), path.join(WEB_DIST, dir));
        console.log(`  ${dir}/: ${n} 个文件`);
    }

    // 汇总
    const totalRatio = ((1 - totalDist / totalSrc) * 100).toFixed(1);
    console.log('\n=== 汇总 ===');
    console.log(`  源文件总大小: ${fmtSize(totalSrc)}`);
    console.log(`  处理后总大小: ${fmtSize(totalDist)}`);
    console.log(`  总压缩率:     -${totalRatio}%`);
    console.log(`  输出目录:     ${WEB_DIST}`);
    console.log('\n=== 处理完成 ===');
}

main().catch(err => {
    console.error('处理失败:', err);
    process.exit(1);
});
