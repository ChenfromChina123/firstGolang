package middleware

import (
	"log"
	"net/http"
	"net/url"
)

// RefererCheck 校验 HTTP Referer 头，只允许空 Referer 或白名单域名访问。
// 防止其他网站直接链接资源（如 <img src="https://aistudy.icu/...">）消耗带宽。
// 允许空 Referer 以兼容书签、直接访问、隐私模式和移动端 WebView。
// allowedDomains 为允许的精确 host 列表（如 ["aistudy.icu", "www.aistudy.icu"]）。
func RefererCheck(allowedDomains []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer := r.Referer()
		// 允许空 Referer（书签、直接访问、隐私模式、移动端 WebView）
		if referer == "" {
			next.ServeHTTP(w, r)
			return
		}
		// 解析 Referer，提取 host
		u, err := url.Parse(referer)
		if err != nil || u.Host == "" {
			// Referer 非法，当作空 Referer 处理（不阻断正常访问）
			next.ServeHTTP(w, r)
			return
		}
		// 校验 host 是否在白名单（精确匹配，避免子域名污染）
		for _, domain := range allowedDomains {
			if u.Host == domain {
				next.ServeHTTP(w, r)
				return
			}
		}
		// 拒绝其他域名
		log.Printf("[Referer] blocked: host=%s path=%s", u.Host, r.URL.Path)
		http.Error(w, "Forbidden: hotlink not allowed", http.StatusForbidden)
	})
}
