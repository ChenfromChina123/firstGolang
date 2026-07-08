package storage

import (
	"container/heap"
	"log"
	"sync"
	"time"
)

// transcodePriority 转码任务优先级，数值越小优先级越高。
type transcodePriority int

const (
	// priorityHigh 用户主动请求的转码任务，优先执行。
	priorityHigh transcodePriority = 0
	// priorityLow 空闲预转码任务，排队等待执行。
	priorityLow transcodePriority = 1
)

// transcodeStarvationTimeout 低优先级任务饥饿提升阈值。
// 超过此时间仍未执行的 low 任务自动提升为 high，防止预转码任务在用户请求持续时永久饿死。
const transcodeStarvationTimeout = 60 * time.Second

// transcodeRequest 转码任务请求，入队到优先级队列。
type transcodeRequest struct {
	FileID      string              // 文件 UUID
	Quality     string              // 画质：high|medium|low
	SrcPath     string              // 源视频绝对路径
	BasePath    string              // 存储根目录（LocalStorage.BasePath()）
	Priority    transcodePriority   // 优先级
	EnqueueTime time.Time           // 入队时间（用于排序和饥饿判断）
	CacheStore  TranscodeCacheStore // OSS 缓存后端（nil 时走 MP4 fallback）
}

// transcodePriorityQueue 实现 heap.Interface。
// 排序规则：Priority 升序（high 先出）→ EnqueueTime 升序（先入先出）。
type transcodePriorityQueue []*transcodeRequest

// Len 返回队列长度。
func (pq transcodePriorityQueue) Len() int { return len(pq) }

// Less 比较优先级：Priority 小的优先，相同时 EnqueueTime 早的优先。
func (pq transcodePriorityQueue) Less(i, j int) bool {
	if pq[i].Priority != pq[j].Priority {
		return pq[i].Priority < pq[j].Priority
	}
	return pq[i].EnqueueTime.Before(pq[j].EnqueueTime)
}

// Swap 交换元素。
func (pq transcodePriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

// Push 向队列追加元素。
func (pq *transcodePriorityQueue) Push(x interface{}) {
	*pq = append(*pq, x.(*transcodeRequest))
}

// Pop 弹出队列末尾元素。
func (pq *transcodePriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

// 队列和调度器全局状态
var (
	transcodeQueueMu sync.Mutex
	transcodeQueue   transcodePriorityQueue
	queueCond        = sync.NewCond(&transcodeQueueMu)
	schedulerStarted bool
)

// EnqueueTranscode 将转码任务入队，返回当前任务状态。
// 调用方应先通过 StartTranscodeJob 创建 job 占位，再调用此函数入队。
// 若调度器未启动，任务仍会入队但在调度器启动后才开始执行。
func EnqueueTranscode(req transcodeRequest) string {
	if req.EnqueueTime.IsZero() {
		req.EnqueueTime = time.Now()
	}

	transcodeQueueMu.Lock()
	heap.Push(&transcodeQueue, &req)
	transcodeQueueMu.Unlock()
	queueCond.Signal()

	return string(TranscodeStatusPending)
}

// StartTranscodeScheduler 启动转码调度器（单 worker goroutine）。
// 仅启动一次，重复调用无副作用。worker 循环：等待任务 → 饥饿提升 → 取任务 → 执行。
// 必须在 main.go 初始化时调用，否则入队的任务永远不会执行。
func StartTranscodeScheduler() {
	transcodeQueueMu.Lock()
	if schedulerStarted {
		transcodeQueueMu.Unlock()
		return
	}
	schedulerStarted = true
	transcodeQueueMu.Unlock()

	go transcodeWorkerLoop()
	log.Printf("[Transcode] Scheduler started: single worker, starvation timeout=%v", transcodeStarvationTimeout)
}

// transcodeWorkerLoop 转码 worker 主循环。
// 单 worker 模型：同一时刻只执行一个 ffmpeg 进程（与原 transcodeGlobalSem 容量=1 等效）。
// 每次取任务前执行饥饿提升，防止 low 任务永久等待。
func transcodeWorkerLoop() {
	for {
		transcodeQueueMu.Lock()
		for transcodeQueue.Len() == 0 {
			queueCond.Wait()
		}

		// 饥饿提升：low 等待 >60s 升 high
		promoteStarvedJobs()

		req := heap.Pop(&transcodeQueue).(*transcodeRequest)
		transcodeQueueMu.Unlock()

		executeTranscodeRequest(req)
	}
}

// promoteStarvedJobs 将队列中等待超过阈值的 low 优先级任务提升为 high。
// 在 worker 持锁取任务前调用，遍历队列原地修改 Priority。
func promoteStarvedJobs() {
	now := time.Now()
	for _, req := range transcodeQueue {
		if req.Priority == priorityLow && now.Sub(req.EnqueueTime) > transcodeStarvationTimeout {
			req.Priority = priorityHigh
			log.Printf("[Transcode] Starvation promotion: fileID=%s quality=%s waited %v",
				req.FileID, req.Quality, now.Sub(req.EnqueueTime))
		}
	}
	// 重建堆（修改了 Priority 后需要重新排序）
	heap.Init(&transcodeQueue)
}

// executeTranscodeRequest 执行单个转码任务。
// 根据 CacheStore 是否可用选择 HLS 或 MP4 转码路径，更新 job 状态并注册清理。
func executeTranscodeRequest(req *transcodeRequest) {
	key := req.FileID + ":" + req.Quality

	jobIface, ok := transcodeJobs.Load(key)
	if !ok {
		log.Printf("[Transcode] Job not found for key=%s, skip", key)
		return
	}
	job := jobIface.(*TranscodeJob)
	job.setStatus(TranscodeStatusRunning)

	// 选择转码函数：有 cacheStore 用 HLS（边转边播+OSS缓存），否则用 MP4 fallback（本地缓存）
	var err error
	if req.CacheStore != nil {
		err = GenerateHLSTranscode(req.BasePath, req.SrcPath, req.FileID, req.Quality, req.CacheStore)
	} else {
		_, err = GenerateTranscode(req.BasePath, req.SrcPath, req.FileID, req.Quality)
	}

	if err != nil {
		log.Printf("[Transcode] Failed: fileID=%s quality=%s error=%v", req.FileID, req.Quality, err)
		job.setStatusAndError(TranscodeStatusFailed, err.Error())
		scheduleJobCleanup(key, TranscodeStatusFailed)
		return
	}

	log.Printf("[Transcode] Done: fileID=%s quality=%s", req.FileID, req.Quality)
	job.setStatus(TranscodeStatusDone)
	scheduleJobCleanup(key, TranscodeStatusDone)
}

// GetQueueSize 返回当前队列中待执行的任务数（用于监控/调试）。
func GetQueueSize() int {
	transcodeQueueMu.Lock()
	defer transcodeQueueMu.Unlock()
	return transcodeQueue.Len()
}
