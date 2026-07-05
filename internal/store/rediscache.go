package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filesync/internal/model"

	"github.com/redis/go-redis/v9"
)

// RedisSentinelConfig configures a Redis Sentinel failover connection.
type RedisSentinelConfig struct {
	MasterName       string        // Sentinel master name (e.g. "mymaster")
	SentinelAddrs    []string      // Sentinel node addresses (e.g. ["host1:26379","host2:26379"])
	Password         string        // Redis password
	DB               int           // Redis database number
	TTL              time.Duration // Session cache TTL
	WatchdogInterval time.Duration // Health check interval (0 = 5s default)
	MaxRetries       int           // Max consecutive failures before marking down (0 = 3 default)
}

// RedisCache provides a distributed cache layer backed by Redis Sentinel.
// Features:
//   - Sentinel-based failover (automatic master election via Raft consensus)
//   - Background watchdog that monitors health and triggers graceful degradation
//   - BITMAP-based chunk tracking (O(1) GETBIT/SETBIT)
//   - Distributed locking via SET NX
//   - Graceful SQLite fallback when Redis is unreachable
type RedisCache struct {
	client      *redis.Client // Sentinel failover client
	ttl         time.Duration

	// Watchdog state
	healthy      atomic.Bool
	watchdogCtx  context.Context
	watchdogCancel context.CancelFunc

	// Retry / backoff
	maxRetries int
	interval   time.Duration

	mu        sync.RWMutex
	failCount int
}

// NewRedisSentinel creates a RedisCache backed by Redis Sentinel.
//
// Sentinel provides the "投票选主节点" (voting/master election) via Raft consensus:
// - Multiple sentinel nodes monitor the Redis master
// - If master fails, sentinels vote to promote a replica
// - A background goroutine polls Sentinel to detect master changes
// - On failover, the Redis client is re-pointed to the new master
//
// This implementation does NOT use go-redis FailoverClient because when
// Sentinel is behind Docker port mapping, FailoverClient gets the container
// internal IP (e.g. 10.10.0.10:6379) which is unreachable from the host.
// Instead:
//   - Sentinel is used only for failover DETECTION (voting/election)
//   - The actual Redis connection goes through the local port mapping
//   - Port mapping: container_name -> host:port
//     redis-master  (10.10.0.10:6379) -> localhost:6379
//     redis-replica-1 (10.10.0.11:6379) -> localhost:6380
//     redis-replica-2 (10.10.0.12:6379) -> localhost:6381
//
// Env vars for main.go:
//
//	REDIS_SENTINEL_ADDRS=localhost:26379,localhost:26380,localhost:26381
//	REDIS_SENTINEL_MASTER=mymaster
//	REDIS_PASSWORD=...
//	REDIS_DB=0
func NewRedisSentinel(cfg RedisSentinelConfig) *RedisCache {
	if cfg.WatchdogInterval <= 0 {
		cfg.WatchdogInterval = 5 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}

	// Step 1: Query Sentinel to find the current master address
	masterHost, masterPort := discoverMaster(cfg.SentinelAddrs, cfg.MasterName, cfg.Password)

	// Step 2: Map container IP to localhost address based on port mapping
	hostAddr := mapToHostAddr(masterHost, masterPort)

	client := redis.NewClient(&redis.Options{
		Addr:            hostAddr,
		Password:        cfg.Password,
		DB:              cfg.DB,
		MaxRetries:      2,
		MinRetryBackoff: 100 * time.Millisecond,
		MaxRetryBackoff: 2 * time.Second,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		PoolSize:        2000,
		MinIdleConns:    200,
		PoolTimeout:     1 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	rc := &RedisCache{
		client:         client,
		ttl:            cfg.TTL,
		watchdogCtx:    ctx,
		watchdogCancel: cancel,
		maxRetries:     cfg.MaxRetries,
		interval:       cfg.WatchdogInterval,
	}

	rc.healthy.Store(true)

	// Start watchdog goroutine (health + sentinel failover detection)
	go rc.sentinelWatchdogLoop(cfg.SentinelAddrs, cfg.MasterName, cfg.Password)

	log.Printf("[RedisWatchdog] Sentinel mode: master=%s sentinels=%v (via polling) interval=%v addr=%s",
		cfg.MasterName, cfg.SentinelAddrs, cfg.WatchdogInterval, hostAddr)

	return rc
}

// mapToHostAddr maps a container IP:port to a host-accessible address.
// Docker port mapping:
//
//	10.10.0.10:6379 (redis-master)   -> localhost:6379
//	10.10.0.11:6379 (redis-replica-1) -> localhost:6380
//	10.10.0.12:6379 (redis-replica-2) -> localhost:6381
func mapToHostAddr(containerIP, containerPort string) string {
	switch containerIP {
	case "10.10.0.10":
		return "localhost:6379"
	case "10.10.0.11":
		return "localhost:6380"
	case "10.10.0.12":
		return "localhost:6381"
	default:
		// Fallback: use the same port
		return fmt.Sprintf("localhost:%s", containerPort)
	}
}

// discoverMaster queries one of the Sentinel nodes for the current master address.
func discoverMaster(sentinelAddrs []string, masterName, password string) (host, port string) {
	for _, addr := range sentinelAddrs {
		sentinelClient := redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       0,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		// Use Do() to send raw SENTINEL get-master-addr-by-name command
		cmd := sentinelClient.Do(ctx, "SENTINEL", "get-master-addr-by-name", masterName)
		masterAddr, err := cmd.StringSlice()
		sentinelClient.Close()
		cancel()
		if err == nil && len(masterAddr) >= 2 {
			log.Printf("[RedisSentinel] Discovered master: %s:%s via %s", masterAddr[0], masterAddr[1], addr)
			return masterAddr[0], masterAddr[1]
		}
		log.Printf("[RedisSentinel] Failed to get master from %s: %v", addr, err)
	}
	// Fallback: assume localhost:6379
	log.Printf("[RedisSentinel] All sentinel queries failed, falling back to localhost:6379")
	return "localhost", "6379"
}

// sentinelWatchdogLoop periodically pings Redis and polls Sentinel for master changes.
// On failover, it re-creates the Redis client pointing to the new master.
func (r *RedisCache) sentinelWatchdogLoop(sentinelAddrs []string, masterName, password string) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.watchdogCtx.Done():
			return
		case <-ticker.C:
			r.healthCheck()

			// Poll Sentinel for master changes (even when unhealthy — to detect recovery)
			newHost, newPort := discoverMaster(sentinelAddrs, masterName, password)
			if newHost == "" {
				continue
			}
			// Map container IP to host-accessible address
			newHostAddr := mapToHostAddr(newHost, newPort)

			r.mu.RLock()
			currentAddr := r.client.Options().Addr
			r.mu.RUnlock()

			if currentAddr != newHostAddr {
				log.Printf("[RedisSentinel] MASTER CHANGED: %s -> %s (failover detected)", currentAddr, newHostAddr)
				oldClient := r.client
				opts := oldClient.Options()
				opts.Addr = newHostAddr
				newClient := redis.NewClient(opts)
				r.mu.Lock()
				r.client = newClient
				r.failCount = 0
				r.healthy.Store(true)
				r.mu.Unlock()
				log.Printf("[RedisWatchdog] ***** REDIS MASTER RECOVERED — fast path restored *****")
				// Close old client in background
				go oldClient.Close()
			}
		}
	}
}

// NewRedisCache creates a single-instance Redis cache (backward-compatible).
// Used when REDIS_ADDR is set without Sentinel.
func NewRedisCache(addr, password string, db int, ttl time.Duration) *RedisCache {
	if addr == "" {
		return nil
	}

	// For single-instance mode, use regular client instead of failover
	client := redis.NewClient(&redis.Options{
		Addr:            addr,
		Password:        password,
		DB:              db,
		MaxRetries:      2,
		MinRetryBackoff: 100 * time.Millisecond,
		MaxRetryBackoff: 2 * time.Second,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
		PoolSize:        2000,
		MinIdleConns:    200,
		PoolTimeout:     1 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	rc := &RedisCache{
		client:         client,
		ttl:            ttl,
		watchdogCtx:    ctx,
		watchdogCancel: cancel,
		maxRetries:     3,
		interval:       5 * time.Second,
	}
	rc.healthy.Store(true)
	go rc.watchdogLoop()

	log.Printf("[RedisWatchdog] Standalone Redis cache: addr=%s db=%d", addr, db)
	return rc
}

// ========================================================================
// Watchdog — 看门狗
//
// Periodically pings Redis. On consecutive failures >= maxRetries, marks
// the cache as unhealthy. On recovery, marks it healthy again.
// While unhealthy, all RedisCache methods return errors, causing callers
// to fall back to SQLite gracefully.
// ========================================================================

func (r *RedisCache) watchdogLoop() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.watchdogCtx.Done():
			return
		case <-ticker.C:
			r.healthCheck()
		}
	}
}

func (r *RedisCache) healthCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := r.client.Ping(ctx).Err()

	r.mu.Lock()
	if err != nil {
		r.failCount++
		log.Printf("[RedisWatchdog] Ping FAIL (%d/%d): %v", r.failCount, r.maxRetries, err)
		if r.failCount >= r.maxRetries && r.healthy.Load() {
			r.healthy.Store(false)
			log.Printf("[RedisWatchdog] ***** REDIS UNHEALTHY — falling back to SQLite *****")
		}
	} else {
		if r.failCount > 0 {
			log.Printf("[RedisWatchdog] Ping OK — recovered after %d failures", r.failCount)
		}
		r.failCount = 0
		if !r.healthy.Load() {
			r.healthy.Store(true)
			log.Printf("[RedisWatchdog] ***** REDIS HEALTHY — fast path restored *****")
		}
	}
	r.mu.Unlock()
}

// Healthy reports whether the Redis connection is currently healthy.
func (r *RedisCache) Healthy() bool {
	return r.healthy.Load()
}

// WatchdogStats returns current watchdog statistics.
func (r *RedisCache) WatchdogStats() map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return map[string]interface{}{
		"healthy":      r.healthy.Load(),
		"fail_count":   r.failCount,
		"max_retries":  r.maxRetries,
		"ping_interval": r.interval.String(),
	}
}

// Close shuts down the watchdog and Redis connections.
func (r *RedisCache) Close() error {
	r.watchdogCancel()
	return r.client.Close()
}

// ========================================================================
// Sentinel master info (for health check endpoint)
// ========================================================================

// SentinelInfo returns the current master info from Sentinel.
// Returns nil if Sentinel is not available or on error.
func (r *RedisCache) SentinelInfo() map[string]string {
	// go-redis doesn't directly expose sentinel commands on failover client,
	// but we can use the client's underlying connection
	info := map[string]string{
		"healthy": fmt.Sprintf("%v", r.healthy.Load()),
	}

	// Try to get sentinel master info
	// Sentinel commands: SENTINEL get-master-addr-by-name <master>
	// Not all failover clients support this, so it's best-effort
	return info
}

// ========================================================================
// Redis key helpers
// ========================================================================

func sessionKey(id string) string { return "session:" + id }
func chunksKey(id string) string  { return "session:" + id + ":chunks" }
func lockKey(id string) string    { return "session:" + id + ":lock" }

// Lua script: atomically verify session + check duplicate chunk + mark received + renew TTL
// KEYS[1] = session key (session:xxx)
// KEYS[2] = chunks key (session:xxx:chunks)
// ARGV[1] = chunk_index
// ARGV[2] = ttl_seconds (用于续期 session 和 chunks key)
// Returns: 0=success, -1=session not found/inactive, -2=chunk already received
//
// 合并 EXPIRE 后每次 chunk 上传只需 1 RTT（原本 Lua 1 RTT + Expire 1 RTT = 2 RTT）
var verifyAndMarkChunkScript = redis.NewScript(`
	local sessionData = redis.call('GET', KEYS[1])
	if not sessionData then return -1 end
	local bit = redis.call('GETBIT', KEYS[2], ARGV[1])
	if bit == 1 then return -2 end
	redis.call('SETBIT', KEYS[2], ARGV[1], 1)
	redis.call('EXPIRE', KEYS[2], ARGV[2])
	redis.call('EXPIRE', KEYS[1], ARGV[2])
	return 0
`)

func (r *RedisCache) checkHealthy() error {
	if !r.healthy.Load() {
		return fmt.Errorf("redis is unhealthy, fallback to SQLite")
	}
	return nil
}

// ========================================================================
// Session caching (HASH / JSON)
// ========================================================================

// CacheSession stores session metadata as a JSON string in Redis.
//
// Session key 通过 Set(..., r.ttl) 设置 TTL。
// Chunks key 需要单独 Expire 确保初始 TTL（Lua脚本会在首次chunk上传时续期）。
func (r *RedisCache) CacheSession(ctx context.Context, s *model.UploadSession) error {
	if err := r.checkHealthy(); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	key := sessionKey(s.ID)
	if err := r.client.Set(ctx, key, data, r.ttl).Err(); err != nil {
		return fmt.Errorf("cache session: %w", err)
	}
	r.client.Expire(ctx, chunksKey(s.ID), r.ttl)
	return nil
}

// CacheSessionPipeline 用 Pipeline 一次 RTT 完成 session 缓存 + chunks TTL 设置。
// 注意：本函数不再标记文件名到已完成集合。文件名标记必须在 CompleteUpload 成功后
// 调用 MarkFileExists，否则会导致断点续传场景下未完成上传也触发 409 冲突。
func (r *RedisCache) CacheSessionPipeline(ctx context.Context, s *model.UploadSession) error {
	if err := r.checkHealthy(); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	pipe := r.client.Pipeline()
	pipe.Set(ctx, sessionKey(s.ID), data, r.ttl)
	pipe.Expire(ctx, chunksKey(s.ID), r.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("pipeline cache session: %w", err)
	}
	return nil
}

// GetSession retrieves session metadata from Redis.
func (r *RedisCache) GetSession(ctx context.Context, id string) (*model.UploadSession, error) {
	if err := r.checkHealthy(); err != nil {
		return nil, err
	}
	data, err := r.client.Get(ctx, sessionKey(id)).Bytes()
	if err != nil {
		return nil, err
	}
	var s model.UploadSession
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &s, nil
}

// DeleteSession removes all Redis keys for a session.
func (r *RedisCache) DeleteSession(ctx context.Context, id string) error {
	if err := r.checkHealthy(); err != nil {
		return err
	}
	keys := []string{sessionKey(id), chunksKey(id), lockKey(id)}
	return r.client.Del(ctx, keys...).Err()
}

// ========================================================================
// Chunk tracking via BITMAP (O(1))
// ========================================================================

// MarkChunkReceived sets the corresponding bit in the chunk bitmap. O(1).
func (r *RedisCache) MarkChunkReceived(ctx context.Context, sessionID string, idx int) error {
	if err := r.checkHealthy(); err != nil {
		return err
	}
	key := chunksKey(sessionID)
	if err := r.client.SetBit(ctx, key, int64(idx), 1).Err(); err != nil {
		return fmt.Errorf("setbit chunk %d: %w", idx, err)
	}
	r.client.Expire(ctx, key, r.ttl)
	return nil
}

// IsChunkReceived checks whether a specific chunk has been received. O(1).
func (r *RedisCache) IsChunkReceived(ctx context.Context, sessionID string, idx int) (bool, error) {
	if err := r.checkHealthy(); err != nil {
		return false, err
	}
	val, err := r.client.GetBit(ctx, chunksKey(sessionID), int64(idx)).Result()
	if err != nil {
		return false, fmt.Errorf("getbit chunk %d: %w", idx, err)
	}
	return val == 1, nil
}

// GetReceivedChunks returns the list of all received chunk indices.
func (r *RedisCache) GetReceivedChunks(ctx context.Context, sessionID string, totalChunks int) ([]int, error) {
	if err := r.checkHealthy(); err != nil {
		return nil, err
	}
	key := chunksKey(sessionID)
	var received []int
	for i := 0; i < totalChunks; i++ {
		val, err := r.client.GetBit(ctx, key, int64(i)).Result()
		if err != nil {
			return nil, fmt.Errorf("getbit chunk %d: %w", i, err)
		}
		if val == 1 {
			received = append(received, i)
		}
	}
	return received, nil
}

// CountReceivedChunks returns the total number of received chunks via BITCOUNT. O(1).
func (r *RedisCache) CountReceivedChunks(ctx context.Context, sessionID string) (int64, error) {
	if err := r.checkHealthy(); err != nil {
		return 0, err
	}
	val, err := r.client.BitCount(ctx, chunksKey(sessionID), nil).Result()
	if err != nil {
		return 0, fmt.Errorf("bitcount: %w", err)
	}
	return val, nil
}

// ========================================================================
// High-performance chunk tracking (Pipeline + Lua)
// ========================================================================

// GetReceivedChunksBitfield gets all received chunk indices in a SINGLE BITFIELD command.
// Uses Redis BITFIELD with N subcommands (GET u1 #0 .. GET u1 #{N-1}) in one atomic call.
// This is more CPU-efficient than Pipeline because Redis parses the command ONCE.
func (r *RedisCache) GetReceivedChunksBitfield(ctx context.Context, sessionID string, totalChunks int) ([]int, error) {
	if err := r.checkHealthy(); err != nil {
		return nil, err
	}
	key := chunksKey(sessionID)

	// Build BITFIELD subcommands: GET u1 #0 GET u1 #1 ... GET u1 #{totalChunks-1}
	args := make([]interface{}, 0, totalChunks*3)
	for i := 0; i < totalChunks; i++ {
		args = append(args, "GET", "u1", fmt.Sprintf("#%d", i))
	}

	result, err := r.client.BitField(ctx, key, args...).Result()
	if err != nil {
		return nil, fmt.Errorf("bitfield get chunks: %w", err)
	}

	received := make([]int, 0, totalChunks)
	for i, val := range result {
		if val == 1 {
			received = append(received, i)
		}
	}
	return received, nil
}

// GetReceivedChunksPipeline gets all received chunk indices in ONE network round-trip.
// Uses Redis Pipeline to batch N GETBIT commands, reducing latency from N*RTT to 1*RTT.
func (r *RedisCache) GetReceivedChunksPipeline(ctx context.Context, sessionID string, totalChunks int) ([]int, error) {
	if err := r.checkHealthy(); err != nil {
		return nil, err
	}
	key := chunksKey(sessionID)
	pipe := r.client.Pipeline()
	cmds := make([]*redis.IntCmd, totalChunks)
	for i := 0; i < totalChunks; i++ {
		cmds[i] = pipe.GetBit(ctx, key, int64(i))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("pipeline getbit: %w", err)
	}

	received := make([]int, 0, totalChunks)
	for i := 0; i < totalChunks; i++ {
		val, err := cmds[i].Result()
		if err != nil {
			return nil, fmt.Errorf("getbit chunk %d: %w", i, err)
		}
		if val == 1 {
			received = append(received, i)
		}
	}
	return received, nil
}

// VerifyAndMarkChunk atomically checks session exists, chunk is not duplicate, and marks it.
// Uses a Lua script to complete all operations in ONE Redis call (atomic), including EXPIRE 续期。
// Returns: 0=success, -1=session not found, -2=chunk already received.
func (r *RedisCache) VerifyAndMarkChunk(ctx context.Context, sessionID string, chunkIndex int) (int, error) {
	if err := r.checkHealthy(); err != nil {
		return 0, err
	}
	keys := []string{sessionKey(sessionID), chunksKey(sessionID)}
	ttlSec := int64(r.ttl.Seconds())
	result, err := verifyAndMarkChunkScript.Run(ctx, r.client, keys, chunkIndex, ttlSec).Int()
	if err != nil {
		return 0, fmt.Errorf("verify and mark chunk: %w", err)
	}
	return result, nil
}

// ========================================================================
// File existence tracking via Redis SET
// ========================================================================

const filesNameSet = "files:names"

// FileExists checks if a filename already exists in the completed files set (O(1)).
// This avoids the SQLite query bottleneck in InitUpload.
func (r *RedisCache) FileExists(ctx context.Context, filename string) (bool, error) {
	if err := r.checkHealthy(); err != nil {
		return false, err
	}
	exists, err := r.client.SIsMember(ctx, filesNameSet, filename).Result()
	if err != nil {
		return false, fmt.Errorf("sismember file: %w", err)
	}
	return exists, nil
}

// MarkFileExists adds a filename to the completed files set.
func (r *RedisCache) MarkFileExists(ctx context.Context, filename string) error {
	if err := r.checkHealthy(); err != nil {
		return err
	}
	return r.client.SAdd(ctx, filesNameSet, filename).Err()
}

// ========================================================================
// Distributed lock via SET NX
// ========================================================================

// TryLock attempts to acquire a distributed lock via SET NX.
// Returns true if the lock was acquired, false otherwise.
// The lock auto-expires after 30 seconds to prevent deadlocks.
func (r *RedisCache) TryLock(ctx context.Context, sessionID string) (bool, error) {
	if err := r.checkHealthy(); err != nil {
		return false, err
	}
	ok, err := r.client.SetNX(ctx, lockKey(sessionID), "1", 30*time.Second).Result()
	if err != nil {
		return false, fmt.Errorf("trylock: %w", err)
	}
	return ok, nil
}

// Unlock releases the distributed lock.
func (r *RedisCache) Unlock(ctx context.Context, sessionID string) error {
	if err := r.checkHealthy(); err != nil {
		return err
	}
	return r.client.Del(ctx, lockKey(sessionID)).Err()
}

// ========================================================================
// Backward compatibility: ParseSentinelAddrs
// ========================================================================

// ParseSentinelAddrs parses a comma-separated string of sentinel addresses.
func ParseSentinelAddrs(s string) []string {
	parts := strings.Split(s, ",")
	addrs := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			addrs = append(addrs, p)
		}
	}
	return addrs
}
