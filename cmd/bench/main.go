package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const baseURL = "http://localhost:8080"

type BenchResult struct {
	Label    string
	Total    int64
	Duration time.Duration
	QPS      float64
	Errors   int64
	P50      time.Duration
	P90      time.Duration
	P99      time.Duration
}

func benchmark(label string, concurrency, total int, fn func(int) error) BenchResult {
	var errCount int64
	start := time.Now()
	var wg sync.WaitGroup
	ch := make(chan int, total)

	go func() {
		for i := 0; i < total; i++ {
			ch <- i
		}
		close(ch)
	}()

	latencies := make([]time.Duration, 0, total)
	var latMu sync.Mutex

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for jobID := range ch {
				t0 := time.Now()
				if err := fn(jobID); err != nil {
					atomic.AddInt64(&errCount, 1)
				}
				latMu.Lock()
				latencies = append(latencies, time.Since(t0))
				latMu.Unlock()
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	sortDurations(latencies)
	p50 := latencies[len(latencies)*50/100]
	p90 := latencies[len(latencies)*90/100]
	p99 := latencies[len(latencies)*99/100]

	qps := float64(total) / elapsed.Seconds()
	fmt.Printf("  %s: %d req / %.2fs = %.0f QPS (err=%d, p50=%s, p90=%s, p99=%s)\n",
		label, total, elapsed.Seconds(), qps, errCount, p50, p90, p99)

	return BenchResult{
		Label: label, Total: int64(total), Duration: elapsed,
		QPS: qps, Errors: errCount, P50: p50, P90: p90, P99: p99,
	}
}

func sortDurations(d []time.Duration) {
	for i := 0; i < len(d); i++ {
		for j := i + 1; j < len(d); j++ {
			if d[j] < d[i] {
				d[i], d[j] = d[j], d[i]
			}
		}
	}
}

func main() {
	client := &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        5000,
			MaxIdleConnsPerHost: 5000,
			MaxConnsPerHost:     5000,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	// Create sessions for read/chunk benchmarks
	// 失败直接退出，避免后续测试用空 sid 跑出全错误的结果（C3 修复）
	sidStatus := createSession(client, 1048576, 524288)
	if sidStatus == "" {
		log.Fatalf("FATAL: failed to create session for GetUploadStatus (server unreachable or 'bench.dat' conflict)")
	}
	sidChunk := createSession(client, 100*1048576, 524288)
	if sidChunk == "" {
		log.Fatalf("FATAL: failed to create session for UploadChunk (server unreachable or 'bench.dat' conflict)")
	}
	chunkData := make([]byte, 4096)

	type BenchCase struct {
		Label       string
		Concurrency int
		Total       int
		Fn          func(idx int) error
	}

	cases := []BenchCase{}

	// ============================================================
	// InitUpload — 高并发重量级测试
	// ============================================================
	for _, c := range []int{100, 500, 1000} {
		total := c * 20
		if total > 10000 {
			total = 10000
		}
		cases = append(cases, BenchCase{
			Label: fmt.Sprintf("InitUpload(c=%d)", c), Concurrency: c, Total: total,
			Fn: func(idx int) error {
				body := fmt.Sprintf(`{"filename":"init_%d.dat","file_size":1024,"chunk_size":1024}`, idx)
				resp, err := client.Post(baseURL+"/api/upload/init", "application/json", bytes.NewReader([]byte(body)))
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					return fmt.Errorf("status %d", resp.StatusCode)
				}
				return nil
			},
		})
	}

	// ============================================================
	// UploadChunk — 核心性能测试，目标 10000+ QPS
	// ============================================================
	// C1 修复：
	//   1. 每个 case 用独立 chunkIdx（原代码跨 case 累积，32500 远超 200）
	//   2. idx mod totalChunks 循环复用，确保不越界，避免污染 chunks 表
	const uploadChunkTotal = 200 // 100MB / 512KB = 200 chunks
	for _, c := range []int{50, 200, 500, 1000} {
		var chunkIdx int64 // 每个 case 独立计数器，闭包捕获本次迭代的变量
		total := c * 50
		if total > 10000 {
			total = 10000
		}
		cases = append(cases, BenchCase{
			Label: fmt.Sprintf("UploadChunk(c=%d)", c), Concurrency: c, Total: total,
			Fn: func(int) error {
				// mod 循环复用 chunk_index，确保 idx < totalChunks
				idx := int(atomic.AddInt64(&chunkIdx, 1)-1) % uploadChunkTotal
				var buf bytes.Buffer
				w := multipart.NewWriter(&buf)
				w.WriteField("session_id", sidChunk)
				w.WriteField("chunk_index", fmt.Sprintf("%d", idx))
				fw, _ := w.CreateFormFile("chunk_data", "chunk.bin")
				fw.Write(chunkData)
				w.Close()
				resp, err := client.Post(baseURL+"/api/upload/chunk", w.FormDataContentType(), &buf)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					return fmt.Errorf("status %d", resp.StatusCode)
				}
				return nil
			},
		})
	}

	// ============================================================
	// GetUploadStatus — Pipeline 优化测试
	// ============================================================
	for _, c := range []int{100, 500, 1000} {
		sid := sidStatus
		cases = append(cases, BenchCase{
			Label: fmt.Sprintf("GetUploadStatus(c=%d)", c), Concurrency: c, Total: c * 10,
			Fn: func(int) error {
				resp, err := client.Get(baseURL + "/api/upload/status?session_id=" + sid)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					return fmt.Errorf("status %d", resp.StatusCode)
				}
				return nil
			},
		})
	}

	// ============================================================
	// CompleteUpload
	// ============================================================
	// C2 修复：用全局唯一计数器生成 filename，避免跨 case 重复触发 409 Conflict
	var cmplCounter int64
	for _, c := range []int{5, 10, 30} {
		total := c * 10
		if total > 200 {
			total = 200
		}
		cases = append(cases, BenchCase{
			Label: fmt.Sprintf("CompleteUpload(c=%d)", c), Concurrency: c, Total: total,
			Fn: func(int) error {
				// 全局自增，确保 filename 全局唯一，避免重复 conflict 污染错误率
				n := atomic.AddInt64(&cmplCounter, 1) - 1
				body := fmt.Sprintf(`{"filename":"cmpl_%d.dat","file_size":4096,"chunk_size":4096}`, n)
				resp, err := client.Post(baseURL+"/api/upload/init", "application/json", bytes.NewReader([]byte(body)))
				if err != nil {
					return err
				}
				var initRes struct{ SessionID string `json:"session_id"` }
				json.NewDecoder(resp.Body).Decode(&initRes)
				resp.Body.Close()
				if initRes.SessionID == "" {
					return fmt.Errorf("no session")
				}
				var buf bytes.Buffer
				w := multipart.NewWriter(&buf)
				w.WriteField("session_id", initRes.SessionID)
				w.WriteField("chunk_index", "0")
				fw, _ := w.CreateFormFile("chunk_data", "chunk.bin")
				fw.Write(chunkData)
				w.Close()
				resp, err = client.Post(baseURL+"/api/upload/chunk", w.FormDataContentType(), &buf)
				if err != nil {
					return err
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()

				body2 := fmt.Sprintf(`{"session_id":"%s"}`, initRes.SessionID)
				resp, err = client.Post(baseURL+"/api/upload/complete", "application/json", bytes.NewReader([]byte(body2)))
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					return fmt.Errorf("status %d", resp.StatusCode)
				}
				return nil
			},
		})
	}

	fmt.Println("\n========== HIGH CONCURRENCY BENCHMARK ==========")
	fmt.Println("(并发: 100~1000, 目标 QPS: InitUpload 5000+, UploadChunk 10000+, GetUploadStatus 15000+)")
	fmt.Println()

	for _, bc := range cases {
		fmt.Printf("\n--- %s ---\n", bc.Label)
		benchmark(bc.Label, bc.Concurrency, bc.Total, bc.Fn)
	}

	fmt.Println("\n=== ALL DONE ===")
}

func createSession(client *http.Client, fileSize, chunkSize int64) string {
	body := fmt.Sprintf(`{"filename":"bench.dat","file_size":%d,"chunk_size":%d}`, fileSize, chunkSize)
	resp, err := client.Post(baseURL+"/api/upload/init", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var result struct {
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.SessionID
}
