package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const chunkSize = 512 * 1024 // 512KB

var serverURL string

func main() {
	flag.StringVar(&serverURL, "server", "http://localhost:8080", "FileSync server URL")
	flag.Parse()

	if flag.NArg() < 1 {
		printUsage()
		return
	}

	cmd := flag.Arg(0)

	switch cmd {
	case "upload":
		cmdUpload(flag.Args()[1:])
	case "download":
		cmdDownload(flag.Args()[1:])
	case "list":
		cmdList()
	case "info":
		cmdInfo(flag.Args()[1:])
	default:
		fmt.Printf("unknown command: %s\n", cmd)
		printUsage()
	}
}

func printUsage() {
	fmt.Print(`FileSync CLI Client
Usage:
  filesync-client [options] <command> [args]

Commands:
  upload <file>          Upload a file with chunking and resume
  download <file-id>     Download a file with resume support
  list                   List all files on server
  info <file-id>         Show file details

Options:
  -server <url>          Server URL (default: http://localhost:8080)
`)
}

// ---------- UPLOAD ----------

func cmdUpload(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: filesync-client upload <filepath>")
		return
	}

	filePath := args[0]
	fi, err := os.Stat(filePath)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}

	filename := filepath.Base(filePath)
	fileSize := fi.Size()

	fmt.Printf("Uploading: %s (%d bytes)\n", filename, fileSize)
	fmt.Printf("Chunk size: %d bytes, total chunks: %d\n", chunkSize, (fileSize+chunkSize-1)/chunkSize)

	// Try upload without conflict
	sessionID, err := initUpload(filename, fileSize, "local", false)
	if err != nil {
		// Check if conflict
		if conflictResp, ok := maybeConflict(err); ok {
			fmt.Printf("Conflict: %s\n", conflictResp["message"])
			fmt.Printf("Strategies: skip, overwrite, rename\n")
			fmt.Print("Choose strategy: ")
			var strategy string
			fmt.Scanln(&strategy)

			switch strategy {
			case "skip":
				fmt.Println("Upload cancelled.")
				return
			case "overwrite":
				sessionID, err = initUpload(filename, fileSize, "local", true)
				if err != nil {
					fmt.Printf("init upload error: %v\n", err)
					return
				}
			case "rename":
				sessionID, err = initUpload(filename, fileSize, "local", false)
				if err != nil {
					fmt.Printf("init upload error: %v\n", err)
					return
				}
			default:
				fmt.Println("invalid strategy")
				return
			}
		} else {
			fmt.Printf("init upload error: %v\n", err)
			return
		}
	}

	fmt.Printf("Session ID: %s\n", sessionID)

	// Check existing progress (for resume)
	status := getUploadStatus(sessionID)
	if status != nil {
		fmt.Printf("Resuming upload at %s\n", status.Progress)
	}

	// Upload chunks
	err = uploadChunks(sessionID, filePath, fileSize)
	if err != nil {
		fmt.Printf("Upload failed: %v\n", err)
		fmt.Println("You can resume later with the same session ID.")
		return
	}

	// Complete
	result := completeUpload(sessionID)
	if result != nil {
		fmt.Printf("\nUpload complete!\n")
		fmt.Printf("File ID: %s\n", result.FileID)
		fmt.Printf("Filename: %s\n", result.Filename)
		fmt.Printf("Size: %d bytes\n", result.Size)
		fmt.Printf("Hash: %s\n", result.Hash)
	}
}

func initUpload(filename string, fileSize int64, storageType string, force bool) (string, error) {
	body := map[string]interface{}{
		"filename":   filename,
		"file_size":  fileSize,
		"chunk_size": chunkSize,
		"storage":    storageType,
	}
	data, _ := json.Marshal(body)

	url := serverURL + "/api/upload/init"
	if force {
		url += "?force=true"
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		// Read conflict details
		var conflict map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&conflict)
		return "", &conflictError{response: conflict}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var initResp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	return initResp.SessionID, nil
}

type conflictError struct {
	response map[string]interface{}
}

func (e *conflictError) Error() string {
	return fmt.Sprintf("conflict: %v", e.response["message"])
}

func maybeConflict(err error) (map[string]interface{}, bool) {
	if ce, ok := err.(*conflictError); ok {
		return ce.response, true
	}
	return nil, false
}

func getUploadStatus(sessionID string) *struct {
	Progress string
} {
	resp, err := http.Get(fmt.Sprintf("%s/api/upload/status?session_id=%s", serverURL, sessionID))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	var status struct {
		Progress string
	}
	var respData map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&respData)
	if p, ok := respData["progress"].(string); ok {
		status.Progress = p
	}
	return &status
}

func uploadChunks(sessionID, filePath string, fileSize int64) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	totalChunks := int((fileSize + chunkSize - 1) / chunkSize)

	// Get received chunks
	statusResp := getUploadStatusFull(sessionID)
	received := make(map[int]bool)
	if statusResp != nil {
		for _, idx := range statusResp.ReceivedChunks {
			received[idx] = true
		}
	}

	buf := make([]byte, chunkSize)
	for i := 0; i < totalChunks; i++ {
		if received[i] {
			fmt.Printf("  Chunk %d/%d - already uploaded, skipping\n", i+1, totalChunks)
			continue
		}

		n, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return fmt.Errorf("read chunk %d: %w", i, err)
		}
		if n == 0 {
			break
		}

		// Upload chunk
		chunkData := buf[:n]
		if err := uploadSingleChunk(sessionID, i, chunkData); err != nil {
			return fmt.Errorf("upload chunk %d: %w", i, err)
		}

		fmt.Printf("  Chunk %d/%d - uploaded (%d bytes)\n", i+1, totalChunks, n)
	}

	return nil
}

type uploadStatusFull struct {
	ReceivedChunks []int   `json:"received_chunks"`
	Progress       string  `json:"progress"`
	TotalChunks    int     `json:"total_chunks"`
}

func getUploadStatusFull(sessionID string) *uploadStatusFull {
	resp, err := http.Get(fmt.Sprintf("%s/api/upload/status?session_id=%s", serverURL, sessionID))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	var status uploadStatusFull
	json.NewDecoder(resp.Body).Decode(&status)
	return &status
}

func uploadSingleChunk(sessionID string, chunkIndex int, data []byte) error {
	// Build multipart form
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("--boundary\r\nContent-Disposition: form-data; name=\"session_id\"\r\n\r\n%s\r\n", sessionID))
	buf.WriteString(fmt.Sprintf("--boundary\r\nContent-Disposition: form-data; name=\"chunk_index\"\r\n\r\n%d\r\n", chunkIndex))
	buf.WriteString(fmt.Sprintf("--boundary\r\nContent-Disposition: form-data; name=\"chunk_data\"; filename=\"chunk\"\r\nContent-Type: application/octet-stream\r\n\r\n"))
	buf.Write(data)
	buf.WriteString("\r\n--boundary--\r\n")

	resp, err := http.Post(serverURL+"/api/upload/chunk", "multipart/form-data; boundary=boundary", &buf)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server: %s", string(body))
	}

	return nil
}

func completeUpload(sessionID string) *struct {
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash"`
} {
	body := map[string]string{"session_id": sessionID}
	data, _ := json.Marshal(body)

	resp, err := http.Post(serverURL+"/api/upload/complete", "application/json", bytes.NewReader(data))
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	var result struct {
		FileID   string `json:"file_id"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		Hash     string `json:"hash"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return &result
}

// ---------- DOWNLOAD ----------

func cmdDownload(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: filesync-client download <file-id> [output-path]")
		return
	}

	fileID := args[0]
	outputPath := fileID + ".downloaded"
	if len(args) >= 2 {
		outputPath = args[1]
	}

	fmt.Printf("Downloading file %s to %s\n", fileID, outputPath)

	// Resume: check if partial file exists
	var downloaded int64
	if fi, err := os.Stat(outputPath); err == nil {
		downloaded = fi.Size()
		fmt.Printf("Partial file found: %d bytes downloaded, resuming...\n", downloaded)
	}

	// Open file for append
	flags := os.O_CREATE | os.O_WRONLY
	if downloaded > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(outputPath, flags, 0644)
	if err != nil {
		fmt.Printf("error creating output file: %v\n", err)
		return
	}
	defer f.Close()

	// Request with Range header
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/download/%s", serverURL, fileID), nil)
	if err != nil {
		fmt.Printf("error creating request: %v\n", err)
		return
	}

	if downloaded > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", downloaded))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("download error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusOK {
		// Get total size from Content-Range header
		var totalSize int64
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			fmt.Sscanf(cr, "bytes %*d-%*d/%d", &totalSize)
		} else {
			totalSize = resp.ContentLength + downloaded
		}

		written, err := io.Copy(f, resp.Body)
		if err != nil {
			fmt.Printf("download interrupted: %v\n", err)
			fmt.Printf("Downloaded %d bytes so far (resumable)\n", downloaded+written)
			return
		}

		fmt.Printf("Download complete! %d bytes\n", downloaded+written)

		// Verify hash if we have it
		if fi, err := os.Stat(outputPath); err == nil {
			hash := computeFileHash(outputPath)
			fmt.Printf("SHA256: %s\n", hash)
			_ = fi
		}
	} else {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("download failed (HTTP %d): %s\n", resp.StatusCode, string(body))
	}
}

// ---------- LIST ----------

func cmdList() {
	resp, err := http.Get(serverURL + "/api/files")
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	var files []struct {
		ID        string `json:"id"`
		Filename  string `json:"filename"`
		Size      int64  `json:"size"`
		Hash      string `json:"hash"`
		CreatedAt string `json:"created_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		fmt.Printf("parse error: %v\n", err)
		return
	}

	if len(files) == 0 {
		fmt.Println("No files on server.")
		return
	}

	fmt.Printf("%-36s %-30s %-12s %-20s\n", "FILE ID", "FILENAME", "SIZE", "CREATED")
	fmt.Println(strings.Repeat("-", 100))
	for _, f := range files {
		fmt.Printf("%-36s %-30s %-12s %-20s\n",
			f.ID, f.Filename, formatSize(f.Size), f.CreatedAt)
	}
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return strconv.FormatInt(bytes, 10) + " B"
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	} else {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
}

// ---------- INFO ----------

func cmdInfo(args []string) {
	if len(args) < 1 {
		fmt.Println("usage: filesync-client info <file-id>")
		return
	}

	resp, err := http.Get(fmt.Sprintf("%s/api/files/%s", serverURL, args[0]))
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("error (HTTP %d): %s\n", resp.StatusCode, string(body))
		return
	}

	var info struct {
		ID          string `json:"id"`
		Filename    string `json:"filename"`
		Size        int64  `json:"size"`
		Hash        string `json:"hash"`
		StoragePath string `json:"storage_path"`
		StorageType string `json:"storage_type"`
		CreatedAt   string `json:"created_at"`
	}
	json.NewDecoder(resp.Body).Decode(&info)

	fmt.Printf("File ID:     %s\n", info.ID)
	fmt.Printf("Filename:    %s\n", info.Filename)
	fmt.Printf("Size:        %s\n", formatSize(info.Size))
	fmt.Printf("SHA256:      %s\n", info.Hash)
	fmt.Printf("Storage:     %s\n", info.StorageType)
	fmt.Printf("Path:        %s\n", info.StoragePath)
	fmt.Printf("Created:     %s\n", info.CreatedAt)
}

// ---------- UTILS ----------

func computeFileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := sha256.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// Ensure time is used
var _ = time.Now
