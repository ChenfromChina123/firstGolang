package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"filesync/internal/handler"
	"filesync/internal/store"
	"filesync/internal/storage"
)

func main() {
	// Configuration
	port := getEnv("PORT", "8080")
	dataDir := getEnv("DATA_DIR", "./data")
	storageType := getEnv("STORAGE_TYPE", "local")

	// Ensure data directory exists
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		log.Fatalf("invalid data dir: %v", err)
	}
	if err := os.MkdirAll(absDataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// Initialize database
	dbPath := filepath.Join(absDataDir, "filesync.db")
	db, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	// Initialize storage backend
	var st storage.Storage
	switch storageType {
	case "local":
		st, err = storage.NewLocal(absDataDir)
		if err != nil {
			log.Fatalf("init local storage: %v", err)
		}
		log.Printf("Storage: local -> %s", absDataDir)
	case "s3":
		// S3 configuration from environment
		s3Cfg := storage.S3Config{
			Endpoint:  getEnv("S3_ENDPOINT", "http://localhost:9000"),
			Region:    getEnv("S3_REGION", "us-east-1"),
			Bucket:    getEnv("S3_BUCKET", "filesync"),
			AccessKey: getEnv("S3_ACCESS_KEY", ""),
			SecretKey: getEnv("S3_SECRET_KEY", ""),
			UseSSL:    getEnv("S3_USE_SSL", "false") == "true",
		}
		st, err = storage.NewS3(absDataDir, s3Cfg)
		if err != nil {
			log.Fatalf("init s3 storage: %v", err)
		}
		log.Printf("Storage: s3 -> bucket=%s endpoint=%s", s3Cfg.Bucket, s3Cfg.Endpoint)
	default:
		log.Fatalf("unknown storage type: %s (use 'local' or 's3')", storageType)
	}

	// Register handlers
	uploadHandler := handler.NewUploadHandler(db, st)
	downloadHandler := handler.NewDownloadHandler(db, st)
	fileHandler := handler.NewFileHandler(db)

	mux := http.NewServeMux()
	mux.Handle("/api/upload/", uploadHandler)
	mux.Handle("/api/download/", downloadHandler)
	mux.Handle("/api/files/", fileHandler)
	mux.Handle("/api/files", fileHandler)

	// Health check
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"filesync"}`))
	})

	addr := fmt.Sprintf(":%s", port)
	log.Printf("FileSync Server starting on http://0.0.0.0%s", addr)
	log.Printf("API endpoints:")
	log.Printf("  POST   /api/upload/init     - Initialize upload")
	log.Printf("  POST   /api/upload/chunk    - Upload chunk")
	log.Printf("  GET    /api/upload/status   - Upload progress")
	log.Printf("  POST   /api/upload/complete - Complete upload")
	log.Printf("  GET    /api/download/{id}   - Download file (supports Range)")
	log.Printf("  GET    /api/files           - List files")
	log.Printf("  GET    /api/files/{id}      - File info")
	log.Printf("  GET    /api/health          - Health check")

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
