package storage

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArchiveEntry 表示压缩包内单个文件条目（用于列表返回）。
type ArchiveEntry struct {
	Path     string `json:"path"`     // 包内相对路径（已清洗，保证不含 .. 和绝对路径）
	Size     int64  `json:"size"`     // 未压缩字节数
	IsDir    bool   `json:"is_dir"`   // 是否为目录条目
	Modified int64  `json:"modified"` // Unix 秒（0 表示未知）
}

// archiveReadCloser 包装提取出的文件流，在 Close 时清理临时文件与底层 reader。
// zip 模式下 tmpFile 非 nil；tar/gzip/bzip2 模式下 tmpFile 为 nil，仅透传 reader 与 source。
type archiveReadCloser struct {
	reader  io.Reader
	tmpFile *os.File  // zip 临时文件（可选）
	source  io.Closer // 底层 reader 的 Closer（可选，如 gzip.Reader）
}

// Read 透传到底层 reader。
func (a *archiveReadCloser) Read(p []byte) (int, error) {
	return a.reader.Read(p)
}

// Close 关闭底层 reader 并清理临时文件。
func (a *archiveReadCloser) Close() error {
	var firstErr error
	// 关闭 reader（如果是 Closer 且不等于 tmpFile，避免重复关闭）
	if rc, ok := a.reader.(io.Closer); ok && rc != a.tmpFile {
		if err := rc.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// 关闭 source（如 gzip.Reader，可能与 reader 相同，需避免重复关闭）
	if a.source != nil {
		// 判断 source 是否已作为 reader 关闭过
		alreadyClosed := false
		if rc, ok := a.reader.(io.Closer); ok && rc == a.source {
			alreadyClosed = true
		}
		if !alreadyClosed && a.source != a.tmpFile {
			if err := a.source.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	// 清理临时文件
	if a.tmpFile != nil {
		name := a.tmpFile.Name()
		if err := a.tmpFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := os.Remove(name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SupportedArchive 判断文件名是否为受支持的压缩包格式。
// 支持 .zip / .tar / .tar.gz / .tgz / .tar.bz2 / .tbz2 / .gz（单文件 gzip）/ .bz2（单文件 bzip2）。
func SupportedArchive(filename string) bool {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".zip"),
		strings.HasSuffix(lower, ".tar"),
		strings.HasSuffix(lower, ".gz"),
		strings.HasSuffix(lower, ".bz2"),
		strings.HasSuffix(lower, ".tgz"),
		strings.HasSuffix(lower, ".tbz2"),
		strings.HasSuffix(lower, ".tar.gz"),
		strings.HasSuffix(lower, ".tar.bz2"):
		return true
	}
	return false
}

// ListArchive 流式列出压缩包内容（不读取文件体）。
// 支持格式按 filename 扩展名分发：zip / tar / tar.gz / tar.bz2 / gz / bz2。
// 安全：拒绝包内路径含 .. 或绝对路径的条目（直接跳过，不返回）。
// maxEntries ≤ 0 时默认上限 10000；超过上限时返回已收集的条目并设置 truncated=true。
func ListArchive(reader io.Reader, filename string, maxEntries int) (entries []ArchiveEntry, truncated bool, err error) {
	if maxEntries <= 0 {
		maxEntries = 10000
	}
	lower := strings.ToLower(filename)

	switch {
	case strings.HasSuffix(lower, ".zip"):
		return listZip(reader, maxEntries)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return listTarStream(reader, maxEntries, "gzip")
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return listTarStream(reader, maxEntries, "bzip2")
	case strings.HasSuffix(lower, ".tar"):
		return listTarStream(reader, maxEntries, "none")
	case strings.HasSuffix(lower, ".gz"):
		return listSingleStream("gzip")
	case strings.HasSuffix(lower, ".bz2"):
		return listSingleStream("bzip2")
	default:
		return nil, false, fmt.Errorf("unsupported archive format: %s", filename)
	}
}

// ExtractArchiveFile 从压缩包中提取单个文件（按 path 精确匹配），流式返回 io.ReadCloser。
// 返回的 ReadCloser 在 Close 时会清理临时文件（zip 模式）。
// 未找到返回 os.ErrNotExist；加密或损坏返回对应错误。
// 调用方负责关闭返回的 ReadCloser；原始 reader 由本函数内部消费（zip 模式会读完）或
// 由返回的 ReadCloser 透传关闭（tar/gzip/bzip2 模式）。
func ExtractArchiveFile(reader io.Reader, filename string, targetPath string) (io.ReadCloser, int64, error) {
	if targetPath == "" {
		return nil, 0, fmt.Errorf("target path required")
	}
	targetPath = cleanArchivePath(targetPath)
	if targetPath == "" {
		return nil, 0, fmt.Errorf("invalid target path")
	}

	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(reader, targetPath)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarStream(reader, targetPath, "gzip")
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return extractTarStream(reader, targetPath, "bzip2")
	case strings.HasSuffix(lower, ".tar"):
		return extractTarStream(reader, targetPath, "none")
	case strings.HasSuffix(lower, ".gz"):
		return extractSingleStream(reader, "gzip")
	case strings.HasSuffix(lower, ".bz2"):
		return extractSingleStream(reader, "bzip2")
	default:
		return nil, 0, fmt.Errorf("unsupported archive format: %s", filename)
	}
}

// cleanArchivePath 清洗压缩包内路径：拒绝 .. 和绝对路径，统一分隔符。
// 返回空字符串表示路径非法。
func cleanArchivePath(p string) string {
	// 统一反斜杠为正斜杠
	p = strings.ReplaceAll(p, "\\", "/")
	// 拒绝绝对路径（Unix 风格）
	if strings.HasPrefix(p, "/") {
		return ""
	}
	// 拒绝 Windows 盘符绝对路径（如 C:）
	if len(p) >= 2 && p[1] == ':' {
		return ""
	}
	// filepath.Clean 后再校验 ..
	cleaned := filepath.Clean(p)
	if cleaned == "." || cleaned == ".." {
		return ""
	}
	if strings.HasPrefix(cleaned, "..") {
		return ""
	}
	// 拒绝包含 .. 段的路径（防 Zip Slip）
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if seg == ".." {
			return ""
		}
	}
	return filepath.ToSlash(cleaned)
}

// listZip 列出 zip 压缩包内容。
// zip 标准库要求 ReaderAt，需将流式 reader 拷贝到临时文件后读取。
func listZip(reader io.Reader, maxEntries int) (entries []ArchiveEntry, truncated bool, err error) {
	tmpFile, size, err := copyToTemp(reader)
	if err != nil {
		return nil, false, fmt.Errorf("copy zip to temp: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpName)
	}()

	zr, err := zip.NewReader(tmpFile, size)
	if err != nil {
		return nil, false, fmt.Errorf("open zip: %w", err)
	}

	entries = make([]ArchiveEntry, 0, len(zr.File))
	for _, f := range zr.File {
		if len(entries) >= maxEntries {
			truncated = true
			break
		}
		cleaned := cleanArchivePath(f.Name)
		if cleaned == "" {
			continue // 跳过非法路径（Zip Slip 防护）
		}
		entries = append(entries, ArchiveEntry{
			Path:     cleaned,
			Size:     int64(f.UncompressedSize64),
			IsDir:    f.FileInfo().IsDir(),
			Modified: f.Modified.Unix(),
		})
	}
	return entries, truncated, nil
}

// listTarStream 列出 tar 流内容（支持 gzip/bzip2/无压缩 三种外层压缩）。
// algo: "gzip" | "bzip2" | "none"。
func listTarStream(reader io.Reader, maxEntries int, algo string) (entries []ArchiveEntry, truncated bool, err error) {
	sr, closer, err := wrapDecompressor(reader, algo)
	if err != nil {
		return nil, false, err
	}
	if closer != nil {
		defer closer.Close()
	}

	tr := tar.NewReader(sr)
	entries = make([]ArchiveEntry, 0, 64)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, fmt.Errorf("read tar entry: %w", err)
		}
		if len(entries) >= maxEntries {
			truncated = true
			break
		}
		cleaned := cleanArchivePath(hdr.Name)
		if cleaned == "" {
			continue
		}
		modified := int64(0)
		if !hdr.ModTime.IsZero() {
			modified = hdr.ModTime.Unix()
		}
		entries = append(entries, ArchiveEntry{
			Path:     cleaned,
			Size:     hdr.Size,
			IsDir:    hdr.Typeflag == tar.TypeDir,
			Modified: modified,
		})
	}
	return entries, truncated, nil
}

// listSingleStream 列出单文件压缩流（.gz / .bz2 单文件模式）。
// 无法知道解压后文件名，返回单条目 "<algo>-stream"。
// size 未知（流式无法预知），设为 0。
func listSingleStream(algo string) (entries []ArchiveEntry, truncated bool, err error) {
	entries = []ArchiveEntry{{
		Path:     algo + "-stream",
		Size:     0,
		IsDir:    false,
		Modified: time.Now().Unix(),
	}}
	return entries, false, nil
}

// extractZip 提取 zip 内单个文件。
// 临时文件生命周期延续到返回的 ReadCloser.Close。
func extractZip(reader io.Reader, targetPath string) (io.ReadCloser, int64, error) {
	tmpFile, size, err := copyToTemp(reader)
	if err != nil {
		return nil, 0, fmt.Errorf("copy zip to temp: %w", err)
	}

	zr, err := zip.NewReader(tmpFile, size)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, 0, fmt.Errorf("open zip: %w", err)
	}

	for _, f := range zr.File {
		cleaned := cleanArchivePath(f.Name)
		if cleaned != targetPath {
			continue
		}
		if f.FileInfo().IsDir() {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return nil, 0, fmt.Errorf("target is a directory: %s", targetPath)
		}
		// 加密检查（位 0 = 加密标志）
		if f.Flags&0x01 != 0 {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return nil, 0, fmt.Errorf("encrypted archive not supported")
		}
		rc, err := f.Open()
		if err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
			return nil, 0, fmt.Errorf("open zip entry %s: %w", targetPath, err)
		}
		return &archiveReadCloser{
			reader:  rc,
			tmpFile: tmpFile,
		}, int64(f.UncompressedSize64), nil
	}

	tmpFile.Close()
	os.Remove(tmpFile.Name())
	return nil, 0, os.ErrNotExist
}

// extractTarStream 提取 tar 流内单个文件（流式扫描直到匹配）。
// algo: "gzip" | "bzip2" | "none"。
// 注意：tar 流式，target 在末尾时需顺序读完前面所有内容才能命中。
func extractTarStream(reader io.Reader, targetPath string, algo string) (io.ReadCloser, int64, error) {
	sr, closer, err := wrapDecompressor(reader, algo)
	if err != nil {
		return nil, 0, err
	}

	tr := tar.NewReader(sr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if closer != nil {
				closer.Close()
			}
			return nil, 0, fmt.Errorf("read tar entry: %w", err)
		}
		cleaned := cleanArchivePath(hdr.Name)
		if cleaned != targetPath {
			continue
		}
		if hdr.Typeflag == tar.TypeDir {
			if closer != nil {
				closer.Close()
			}
			return nil, 0, fmt.Errorf("target is a directory: %s", targetPath)
		}
		// tar.Reader 实现了 io.Reader，读取当前 entry 内容直到 EOF。
		// 返回包装的 ReadCloser，Close 时关闭 decompressor（如 gzip.Reader）。
		return &archiveReadCloser{
			reader: tr,
			source: closer,
		}, hdr.Size, nil
	}
	if closer != nil {
		closer.Close()
	}
	return nil, 0, os.ErrNotExist
}

// extractSingleStream 提取 .gz / .bz2 单文件流（直接返回解压流，忽略 targetPath）。
func extractSingleStream(reader io.Reader, algo string) (io.ReadCloser, int64, error) {
	switch algo {
	case "gzip":
		gr, err := gzip.NewReader(reader)
		if err != nil {
			return nil, 0, fmt.Errorf("open gzip: %w", err)
		}
		return &archiveReadCloser{reader: gr, source: gr}, -1, nil
	case "bzip2":
		sr := bzip2.NewReader(reader)
		return &archiveReadCloser{reader: sr}, -1, nil
	default:
		return nil, 0, fmt.Errorf("unsupported algo: %s", algo)
	}
}

// wrapDecompressor 根据算法包装解压器。
// 返回的 closer 为 nil 时表示无需关闭（bzip2/none 模式）。
func wrapDecompressor(reader io.Reader, algo string) (io.Reader, io.Closer, error) {
	switch algo {
	case "gzip":
		gr, err := gzip.NewReader(reader)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip: %w", err)
		}
		return gr, gr, nil
	case "bzip2":
		return bzip2.NewReader(reader), nil, nil
	case "none":
		return reader, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported algo: %s", algo)
	}
}

// copyToTemp 将 reader 内容拷贝到临时文件，返回 *os.File 和大小。
// 调用方负责关闭和删除临时文件。
func copyToTemp(reader io.Reader) (*os.File, int64, error) {
	tmpFile, err := os.CreateTemp("", "filesync_archive_*.tmp")
	if err != nil {
		return nil, 0, fmt.Errorf("create temp file: %w", err)
	}
	n, err := io.Copy(tmpFile, reader)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, 0, fmt.Errorf("copy to temp: %w", err)
	}
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, 0, fmt.Errorf("seek temp: %w", err)
	}
	return tmpFile, n, nil
}
