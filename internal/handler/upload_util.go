package handler

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// fastQueryParam extracts a single query param from RawQuery without map allocation.
// Returns empty string if key is not present or value is empty.
// 对提取出的值做 URL 解码（前端 encodeURIComponent 会把 / 编码为 %2F，
// 不解码会导致 prefix/path 类参数匹配不到文件，表现为子目录列表为空）。
func fastQueryParam(rawQuery, key string) string {
	if rawQuery == "" {
		return ""
	}
	prefix := key + "="
	idx := strings.Index(rawQuery, prefix)
	if idx < 0 {
		return ""
	}
	start := idx + len(prefix)
	end := strings.IndexByte(rawQuery[start:], '&')
	if end < 0 {
		end = len(rawQuery)
	} else {
		end = start + end
	}
	val := rawQuery[start:end]
	if dec, err := url.QueryUnescape(val); err == nil {
		return dec
	}
	return val
}

// progressStr formats a progress percentage without allocating via fmt.Sprintf.
func progressStr(progress float64) string {
	return strconv.FormatFloat(progress, 'f', 1, 64) + "%"
}

// sync.Pool for reusable []byte buffers (large chunk reads).
// Reduces GC pressure in UploadChunk handler.
var chunkBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 512*1024) // 512KB default chunk size
		return &b
	},
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// validateFilePath 校验文件名路径（路径枚举方案）。
// 允许 / 作为虚拟目录分隔符，但禁止危险路径，防止目录穿越和存储异常。
// 规则：非空、不以 / 开头、无连续 //、无 .. 段、无反斜杠、长度 1-1024。
func validateFilePath(filename string) error {
	if filename == "" {
		return fmt.Errorf("filename is empty")
	}
	if len(filename) > 1024 {
		return fmt.Errorf("filename too long (max 1024 bytes)")
	}
	if strings.HasPrefix(filename, "/") {
		return fmt.Errorf("filename must not start with '/'")
	}
	if strings.Contains(filename, "\\") {
		return fmt.Errorf("filename must not contain backslash")
	}
	if strings.Contains(filename, "//") {
		return fmt.Errorf("filename must not contain consecutive '/'")
	}
	segments := strings.Split(filename, "/")
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid path segment: '%s'", seg)
		}
	}
	return nil
}
