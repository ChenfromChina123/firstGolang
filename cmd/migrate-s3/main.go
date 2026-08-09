// migrate-s3 将 filesync 存量 s3 文件从 OSS 迁移到本地磁盘。
//
// 用法：
//
//	migrate-s3 --env=/opt/filesync/.env [--limit=N] [--dry-run] [--verify-hash] [--delete-s3] [--concurrency=N]
//
// 说明：
//   - 幂等：本地目标文件已存在且大小匹配时自动补写 DB 并跳过下载，可断点续跑。
//   - 原子：先下载到同目录临时文件，校验通过后 rename，失败不留半成品。
//   - 安全：对象键校验（拒绝绝对路径 / .. / 反斜杠），默认不删 OSS 对象，--delete-s3 显式开启。
//   - DB 更新带条件（WHERE storage_type='s3'），防止覆盖其他并发迁移结果。
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"filesync/internal/storage"
)

type fileRecord struct {
	ID          string
	Filename    string
	Size        int64
	Hash        string
	StoragePath string
}

type migrateResult struct {
	f      fileRecord
	status string // ok / skip / fail
	msg    string
}

// loadEnv 解析 .env 文件（KEY=VALUE，忽略注释与空行，支持引号包裹）。
func loadEnv(path string) map[string]string {
	env := map[string]string{}
	if path == "" {
		return env
	}
	f, err := os.Open(path)
	if err != nil {
		return env
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return env
}

func main() {
	envFile := flag.String("env", "", "path to .env file (default: process env)")
	limit := flag.Int("limit", 0, "max files to migrate (0 = all)")
	dryRun := flag.Bool("dry-run", false, "only print the migration plan, don't touch anything")
	verifyHash := flag.Bool("verify-hash", false, "verify sha256 against DB when hash is present")
	deleteS3 := flag.Bool("delete-s3", false, "delete the OSS object after successful migration")
	concurrency := flag.Int("concurrency", 2, "parallel download workers")
	flag.Parse()

	env := loadEnv(*envFile)
	getenv := func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return os.Getenv(k)
	}

	cfg := storage.S3Config{
		Endpoint:  getenv("S3_ENDPOINT"),
		Region:    getenv("S3_REGION"),
		Bucket:    getenv("S3_BUCKET"),
		AccessKey: getenv("S3_ACCESS_KEY"),
		SecretKey: getenv("S3_SECRET_KEY"),
		UseSSL:    strings.EqualFold(getenv("S3_USE_SSL"), "true"),
	}
	dataDir := getenv("DATA_DIR")
	dsn := getenv("MYSQL_DSN")

	if dsn == "" || dataDir == "" {
		log.Fatalf("missing MYSQL_DSN or DATA_DIR")
	}
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		log.Fatalf("missing S3_ENDPOINT or S3_BUCKET")
	}

	s3, err := storage.NewS3(cfg)
	if err != nil {
		log.Fatalf("init s3: %v", err)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping db: %v", err)
	}

	query := "SELECT id, filename, size, hash, storage_path FROM files WHERE storage_type='s3' AND deleted_at IS NULL AND status='completed' ORDER BY id"
	if *limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", *limit)
	}
	rows, err := db.Query(query)
	if err != nil {
		log.Fatalf("query s3 files: %v", err)
	}
	var files []fileRecord
	for rows.Next() {
		var f fileRecord
		if err := rows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath); err != nil {
			rows.Close()
			log.Fatalf("scan row: %v", err)
		}
		files = append(files, f)
	}
	rows.Close()

	log.Printf("found %d s3 file(s) | dataDir=%s bucket=%s endpoint=%s", len(files), dataDir, cfg.Bucket, cfg.Endpoint)

	if *dryRun {
		for _, f := range files {
			key := strings.TrimPrefix(f.StoragePath, storage.S3Prefix)
			log.Printf("[PLAN] id=%s name=%q size=%d key=%s -> %s", f.ID, f.Filename, f.Size, key,
				filepath.Join(dataDir, filepath.FromSlash(key)))
		}
		log.Printf("dry-run finished, nothing was changed")
		return
	}

	ctx := context.Background()
	jobs := make(chan fileRecord)
	results := make(chan migrateResult, len(files))
	var wg sync.WaitGroup
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				results <- migrateOne(ctx, db, s3, f, dataDir, *verifyHash, *deleteS3)
			}
		}()
	}
	go func() {
		for _, f := range files {
			jobs <- f
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var okN, skipN, failN int64
	for r := range results {
		switch r.status {
		case "ok":
			atomic.AddInt64(&okN, 1)
		case "skip":
			atomic.AddInt64(&skipN, 1)
		case "fail":
			atomic.AddInt64(&failN, 1)
		}
		log.Printf("[%s] id=%s name=%q size=%d | %s", strings.ToUpper(r.status), r.f.ID, r.f.Filename, r.f.Size, r.msg)
	}

	log.Printf("DONE: ok=%d skip=%d fail=%d", okN, skipN, failN)
	if failN > 0 {
		log.Printf("some files failed, re-run to retry (idempotent)")
		os.Exit(1)
	}
}

func migrateOne(ctx context.Context, db *sql.DB, s3 storage.Storage, f fileRecord, dataDir string, verifyHash, deleteS3 bool) migrateResult {
	key := strings.TrimPrefix(f.StoragePath, storage.S3Prefix)
	if key == "" {
		return migrateResult{f, "fail", "empty object key"}
	}
	if strings.HasPrefix(key, "/") || strings.Contains(key, "..") || strings.ContainsAny(key, "\\") {
		return migrateResult{f, "fail", fmt.Sprintf("unsafe object key %q", key)}
	}
	localPath := filepath.Join(dataDir, filepath.FromSlash(key))

	// 幂等：本地目标已存在
	if fi, err := os.Stat(localPath); err == nil {
		if fi.Size() != f.Size {
			return migrateResult{f, "fail", fmt.Sprintf("local file exists but size mismatch: local=%d db=%d", fi.Size(), f.Size)}
		}
		if err := updateDB(db, f, localPath); err != nil {
			return migrateResult{f, "fail", "update db (file already present): " + err.Error()}
		}
		return migrateResult{f, "skip", "file already present, DB updated"}
	}

	// 下载 S3 对象到同目录临时文件
	reader, err := s3.ReadFile(key, 0)
	if err != nil {
		return migrateResult{f, "fail", "s3 read: " + err.Error()}
	}
	defer reader.Close()

	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return migrateResult{f, "fail", "mkdir: " + err.Error()}
	}
	tmp, err := os.CreateTemp(dir, ".migrate-*")
	if err != nil {
		return migrateResult{f, "fail", "create temp: " + err.Error()}
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), reader)
	if err != nil {
		cleanup()
		return migrateResult{f, "fail", "download: " + err.Error()}
	}
	if n != f.Size {
		cleanup()
		return migrateResult{f, "fail", fmt.Sprintf("size mismatch: downloaded=%d db=%d", n, f.Size)}
	}
	if verifyHash && f.Hash != "" {
		if got := hex.EncodeToString(hasher.Sum(nil)); got != f.Hash {
			cleanup()
			return migrateResult{f, "fail", fmt.Sprintf("hash mismatch: local=%s db=%s", got, f.Hash)}
		}
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return migrateResult{f, "fail", "fsync: " + err.Error()}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return migrateResult{f, "fail", "close temp: " + err.Error()}
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		os.Remove(tmpName)
		return migrateResult{f, "fail", "rename: " + err.Error()}
	}

	if err := updateDB(db, f, localPath); err != nil {
		return migrateResult{f, "fail", "update db: " + err.Error() + " (local file is saved, re-run to resume)"}
	}
	if deleteS3 {
		if err := s3.DeleteFile(key); err != nil {
			return migrateResult{f, "fail", fmt.Sprintf("db updated but s3 delete failed: %v", err)}
		}
	}
	return migrateResult{f, "ok", "migrated to local"}
}

func updateDB(db *sql.DB, f fileRecord, localAbs string) error {
	ts := time.Now().Format("2006-01-02 15:04:05")
	res, err := db.Exec("UPDATE files SET storage_type='local', storage_path=?, updated_at=? WHERE id=? AND storage_type='s3'",
		storage.LocalPrefix+localAbs, ts, f.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no rows affected (id=%s may be migrated elsewhere)", f.ID)
	}
	return nil
}
