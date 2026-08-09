// theme.js — 跟随国内时间（UTC+8）自动切换浅色/暗色主题
// 晚上 18:00 ~ 次日 06:00 切暗色，其余时间浅色。
// 用法：在 <head> 中、style.css 之前同步引入，避免主题闪烁。
// 外部文件（满足 CSP script-src 'self'），通过 html[data-theme] 驱动 CSS。
(function () {
    'use strict';

    var NIGHT_START = 18; // 18:00 起进入夜间
    var NIGHT_END = 6;    // 次日 06:00 结束夜间
    var CHECK_INTERVAL = 60000; // 每分钟校准一次，覆盖跨时段切换

    var html = document.documentElement;

    // 北京时间 = UTC + 8（中国无夏令时，固定偏移即可，不依赖设备时区）
    function beijingHour() {
        return (new Date().getUTCHours() + 8) % 24;
    }

    function applyTheme() {
        var hour = beijingHour();
        var night = hour >= NIGHT_START || hour < NIGHT_END;
        var theme = night ? 'dark' : 'light';
        if (html.getAttribute('data-theme') !== theme) {
            html.setAttribute('data-theme', theme);
        }
        // 原生表单控件/滚动条与页面主题保持一致
        if (html.style.colorScheme !== theme) {
            html.style.colorScheme = theme;
        }
    }

    applyTheme();
    setInterval(applyTheme, CHECK_INTERVAL);
    // 切回前台立即校准（跨时段 + 长时间挂起场景）
    document.addEventListener('visibilitychange', function () {
        if (!document.hidden) applyTheme();
    });
})();
