package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// baseURL 通过 -server flag 配置，默认 localhost:8080
var baseURL = "http://localhost:8080"

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

// benchmark 在给定并发度下运行 total 次请求 fn，统计 QPS / p50 / p90 / p99 / 错误数。
// 优化点：
//   - per-goroutine latency slice：消除全局锁争用（原 latMu 在 1000+ 并发下成为瓶颈）
//   - channel buffer = concurrency：避免 total=10000 时预分配大 buffer
//   - sort.Slice 替代 O(n²) 冒泡：10000 样本从 5000 万次比较降到 ~13 万次
//   - 百分位边界保护：len=0 时不再 panic
func benchmark(label string, concurrency, total int, fn func(int) error) BenchResult {
	var errCount int64
	start := time.Now()
	var wg sync.WaitGroup
	// buffer = concurrency 足够喂饱所有 worker，避免 total=10000 时浪费 80KB 内存
	ch := make(chan int, concurrency)

	go func() {
		for i := 0; i < total; i++ {
			ch <- i
		}
		close(ch)
	}()

	// per-goroutine latency 收集，无锁
	perGoroutine := make([][]time.Duration, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		perGoroutine[i] = make([]time.Duration, 0, total/concurrency+16)
		go func(idx int) {
			defer wg.Done()
			for jobID := range ch {
				t0 := time.Now()
				if err := fn(jobID); err != nil {
					atomic.AddInt64(&errCount, 1)
				}
				perGoroutine[idx] = append(perGoroutine[idx], time.Since(t0))
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 合并 per-goroutine latencies
	latencies := make([]time.Duration, 0, total)
	for _, l := range perGoroutine {
		latencies = append(latencies, l...)
	}

	// 边界保护：所有请求都失败时 latencies 为空
	if len(latencies) == 0 {
		fmt.Printf("  %s: no latency samples (total=%d, err=%d)\n", label, total, errCount)
		return BenchResult{Label: label, Total: int64(total), Duration: elapsed, Errors: errCount}
	}

	// O(n log n) 排序
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	p50 := latencies[percentileIndex(len(latencies), 50)]
	p90 := latencies[percentileIndex(len(latencies), 90)]
	p99 := latencies[percentileIndex(len(latencies), 99)]

	qps := float64(total) / elapsed.Seconds()
	fmt.Printf("  %s: %d req / %.2fs = %.0f QPS (err=%d, p50=%s, p90=%s, p99=%s)\n",
		label, total, elapsed.Seconds(), qps, errCount, p50, p90, p99)

	return BenchResult{
		Label: label, Total: int64(total), Duration: elapsed,
		QPS: qps, Errors: errCount, P50: p50, P90: p90, P99: p99,
	}
}

// percentileIndex 计算第 p 百分位的数组索引（0<=p<=100），越界时返回最后一个元素索引。
func percentileIndex(n, p int) int {
	if n == 0 {
		return 0
	}
	idx := n * p / 100
	if idx >= n {
		idx = n - 1
	}
	return idx
}

func main() {
	// 支持 -server flag 配置目标服务器
	flag.StringVar(&baseURL, "server", baseURL, "FileSync server URL")
	flag.Parse()

	client := &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			// Windows 有 10000 线程上限，不能设太高
			MaxIdleConns:        2000,
			MaxIdleConnsPerHost: 1000,
			MaxConnsPerHost:     2000,
			IdleConnTimeout:     30 * time.Second,
			DisableKeepAlives:   false,
		},
	}

	// warmup 预热连接池，避免冷启动影响第一个 case 的测试结果
	fmt.Println("Warming up connection pool...")
	for i := 0; i < 50; i++ {
		resp, err := client.Get(baseURL + "/api/health")
		if err != nil {
			log.Fatalf("FATAL: server unreachable at %s (err: %v)", baseURL, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	fmt.Println("Warmup done.")

	// Create sessions for read/chunk benchmarks
	// Use unique filenames to avoid Redis SISMEMBER conflict with itself
	sidStatus := createSession(client, "bench_status.dat", 1048576, 524288)
	if sidStatus == "" {
		log.Fatalf("FATAL: failed to create session for GetUploadStatus")
	}
	sidChunk := createSession(client, "bench_chunk.dat", 100*1048576, 524288)
	if sidChunk == "" {
		log.Fatalf("FATAL: failed to create session for UploadChunk")
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
	// 全局唯一计数器，避免跨 case filename 重复累积 upload_sessions 脏数据
	var initCounter int64
	for _, c := range []int{100, 500, 1000, 2000} {
		total := c * 20
		if total > 10000 {
			total = 10000
		}
		cases = append(cases, BenchCase{
			Label: fmt.Sprintf("InitUpload(c=%d)", c), Concurrency: c, Total: total,
			Fn: func(int) error {
				n := atomic.AddInt64(&initCounter, 1) - 1
				body := fmt.Sprintf(`{"filename":"init_%d.dat","file_size":1024,"chunk_size":1024}`, n)
				resp, err := client.Post(baseURL+"/api/upload/init", "application/json", bytes.NewReader([]byte(body)))
				if err != nil {
					return err
				}
				// 读取 body 以便连接复用，避免每次都新建 TCP 连接
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
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
	// 并发级别提升到 2000 以测试更高 QPS（Windows 线程上限 10000）
	for _, c := range []int{50, 200, 500, 1000, 2000} {
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
				// 读取 body 以便连接复用
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
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
	for _, c := range []int{100, 500, 1000, 2000} {
		sid := sidStatus
		cases = append(cases, BenchCase{
			Label: fmt.Sprintf("GetUploadStatus(c=%d)", c), Concurrency: c, Total: c * 10,
			Fn: func(int) error {
				resp, err := client.Get(baseURL + "/api/upload/status?session_id=" + sid)
				if err != nil {
					return err
				}
				// 读取 body 以便连接复用
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
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
	fmt.Printf("(server: %s, 并发: 100~2000, 目标: InitUpload 5000+, UploadChunk 10000+, GetUploadStatus 15000+)\n", baseURL)
	fmt.Println()

	// 收集所有结果用于 JSON 输出
	results := make([]BenchResult, 0, len(cases))
	for _, bc := range cases {
		fmt.Printf("\n--- %s ---\n", bc.Label)
		r := benchmark(bc.Label, bc.Concurrency, bc.Total, bc.Fn)
		results = append(results, r)
	}

	// 输出 JSON 结果到文件，便于跨次对比
	resultFile := fmt.Sprintf("bench_result_%s.json", time.Now().Format("20060102_150405"))
	f, err := os.Create(resultFile)
	if err != nil {
		log.Printf("warning: failed to write result file: %v", err)
	} else {
		json.NewEncoder(f).Encode(map[string]interface{}{
			"server":  baseURL,
			"results": results,
		})
		f.Close()
		fmt.Printf("\nResults saved to %s\n", resultFile)
	}

	fmt.Println("\n=== ALL DONE ===")
}

func createSession(client *http.Client, filename string, fileSize, chunkSize int64) string {
	body := fmt.Sprintf(`{"filename":"%s","file_size":%d,"chunk_size":%d}`, filename, fileSize, chunkSize)
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
