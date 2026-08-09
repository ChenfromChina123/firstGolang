// Package ws 提供 WebSocket 实时事件推送（供前端传输中心展示 MCP/外部上传）。
//
// 设计：
//   - 全局默认 Hub（包级单例），MCP 工具上传成功后调用 PublishUpload 广播
//   - 端点 /api/ws/events：客户端带 Bearer PAT（fsk_*）升级连接，订阅实时事件
//   - 事件 JSON：{type:"upload", source:"mcp", username, filename, size, space_id, tool, time}
package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Event 实时事件载荷。
type Event struct {
	Type     string `json:"type"`     // "upload" | ...
	Source   string `json:"source"`   // "mcp" | "web" | ...
	Username string `json:"username"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SpaceID  string `json:"space_id,omitempty"`
	Tool     string `json:"tool,omitempty"` // fs_write / fs_upload / ...
	Time     string `json:"time"`
}

// Client 单个 WebSocket 连接。
type Client struct {
	conn *websocket.Conn
	send chan []byte
}

// Hub 管理连接并广播事件。
type Hub struct {
	mu      sync.Mutex
	clients map[*Client]bool
}

// Default 全局默认 Hub（单例）。
var Default = NewHub()

// NewHub 创建 Hub。
func NewHub() *Hub {
	return &Hub{clients: make(map[*Client]bool)}
}

// Broadcast 向所有连接广播一条原始消息（非阻塞，队列满则丢弃慢消费者）。
func (h *Hub) Broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			// 慢消费者：丢弃该条，避免阻塞广播
		}
	}
}

// Publish 序列化事件并广播。
func (h *Hub) Publish(ev Event) {
	if ev.Time == "" {
		ev.Time = time.Now().Format(time.RFC3339)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.Broadcast(b)
}

// PublishUpload 发布一次上传事件（MCP 工具 / 外部写入调用）。
// 同时广播给 WebSocket 客户端，并触发可选的落库回调（由 main 注入，
// 用于写入 upload_events 历史表）。
func PublishUpload(username, filename, spaceID, tool string, size int64) {
	Default.Publish(Event{
		Type:     "upload",
		Source:   "mcp",
		Username: username,
		Filename: filename,
		Size:     size,
		SpaceID:  spaceID,
		Tool:     tool,
	})
	if uploadRecorder != nil {
		uploadRecorder(Event{
			Type:     "upload",
			Source:   "mcp",
			Username: username,
			Filename: filename,
			Size:     size,
			SpaceID:  spaceID,
			Tool:     tool,
		})
	}
}

// uploadRecorder 可选的历史落库回调（避免 ws 包依赖 store）。
var uploadRecorder func(ev Event)

// SetUploadRecorder 注册上传事件历史落库回调（main 启动时注入）。
func SetUploadRecorder(fn func(ev Event)) { uploadRecorder = fn }

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// 同源页面连接，允许跨域（前端 wss 同 host）
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler 返回 WebSocket 升级端点（认证由外层中间件完成，成功后注入 username）。
func (h *Hub) Handler(username string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &Client{conn: conn, send: make(chan []byte, 32)}
		h.mu.Lock()
		h.clients[c] = true
		h.mu.Unlock()
		log.Printf("[ws] client connected: user=%s", username)

		// 读循环：丢弃客户端消息（仅推送），检测断开
		go func() {
			defer func() {
				h.mu.Lock()
				delete(h.clients, c)
				h.mu.Unlock()
				conn.Close()
			}()
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		// 写循环
		go func() {
			ticker := time.NewTicker(30 * time.Second) // ping 保活
			defer ticker.Stop()
			for {
				select {
				case msg, ok := <-c.send:
					if !ok {
						conn.WriteMessage(websocket.CloseMessage, []byte{})
						return
					}
					if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
						return
					}
				case <-ticker.C:
					if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
						return
					}
				}
			}
		}()
	})
}
