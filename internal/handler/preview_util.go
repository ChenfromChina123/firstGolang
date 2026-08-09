package handler

import (
	"path/filepath"
	"strings"

	"filesync/internal/storage"
)

// 返回值：image|pdf|text|code|audio|video|office|archive|unsupported
// 压缩包类型优先用 storage.SupportedArchive 判断（覆盖 .tar.gz/.tar.bz2 双扩展名）。
func getFileType(filename string) string {
	// 压缩包优先判断（多扩展名，filepath.Ext 只取最后一段会漏判 .tar.gz）
	if storage.SupportedArchive(filename) {
		return "archive"
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "unsupported"
	}
	ext = strings.TrimPrefix(ext, ".")
	switch ext {
	case "jpg", "jpeg", "png", "gif", "webp", "bmp", "tiff", "tif":
		return "image"
	case "pdf":
		return "pdf"
	case "txt", "md", "log", "csv", "json", "xml", "yml", "yaml":
		return "text"
	case "js", "ts", "go", "py", "java", "c", "cpp", "rs", "rb", "php", "sh", "sql", "html", "css":
		return "code"
	case "mp3", "wav", "ogg", "flac", "aac", "m4a":
		return "audio"
	case "mp4", "webm", "mkv", "avi", "mov":
		return "video"
	case "doc", "docx", "xls", "xlsx", "ppt", "pptx":
		return "office"
	}
	return "unsupported"
}

// contentTypeFor 根据文件扩展名返回 HTTP Content-Type。
// 用于 serveContent 设置响应头，确保浏览器正确渲染。
func contentTypeFor(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".log", ".md":
		return "text/plain; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".yml", ".yaml":
		return "text/plain; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".ts":
		return "application/typescript; charset=utf-8"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".aac":
		return "audio/aac"
	case ".m4a":
		return "audio/mp4"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	}
	return "application/octet-stream"
}
