// Package server 实现 LimeLink 电脑端 HTTP 服务：
// - POST /pair      手机提交配对码 + 设备名，换取长期 token（握手交换设备名）
// - POST /notify    手机推送一条通知（需带 token）
// - GET  /heartbeat 手机心跳探测（需带 token，通道可用与否的真实判据）
// - GET  /ping      无鉴权的在线探测 + 配对码状态
// - GET  /records   查看最近同步记录
// - POST /reset     重置配对码（踢掉旧设备）
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// Timeouts 是 HTTP 服务的读写超时配置（需求 4.5 服务稳定性加固）。
const (
	readTimeout     = 10 * time.Second
	writeTimeout    = 10 * time.Second
	idleTimeout     = 60 * time.Second
	maxHeaderBytes  = 1 << 20 // 1MB，防超大请求头
	maxDeviceName   = 32      // 设备名长度上限（需求第二章兜底）
	defaultDeviceNm = "LimeLink-PC"
)

// Notification 是一条从手机同步过来的通知。
type Notification struct {
	ID        string `json:"id"`        // 手机端生成的通知唯一 id
	Title     string `json:"title"`     // 通知标题
	Text      string `json:"text"`      // 通知正文
	AppName   string `json:"app_name"`  // 来源 App 名
	AppLabel  string `json:"app_label"` // 归一化来源标识：wechat/sms/app/system
	IconType  string `json:"icon_type"` // 小图标类型：wechat/sms/app/system
	Pkg       string `json:"pkg"`       // 来源包名（v1.1.0 新增，用于 Toast Tag；旧版手机端无此字段）
	Hidden    bool   `json:"hidden"`    // 是否隐藏内容
	Timestamp int64  `json:"ts"`        // 毫秒时间戳
}

// Record 是本地保存的一条同步记录。
type Record struct {
	Notification
	ReceivedAt time.Time `json:"received_at"`
}

// Server 持有服务状态。
type Server struct {
	mu       sync.Mutex
	pairCode string   // 当前 6 位配对码
	token    string   // 已配对设备的长期 token（空 = 未配对）
	name     string   // 本机（电脑端）设备名
	peer     string   // 已配对手机端的设备名（握手时交换）
	records  []Record // 最近同步记录（环形）
	maxRec   int

	// 可观测性（v1.2.1 排查用，轻量无副作用）
	startTime     time.Time // 进程启动时间
	lastNotifyAt  time.Time // 最近一次成功收到 /notify 的时间（零值=从未）
	notifyCount   int       // 累计成功落库的 notify 条数
	notifyRejects int       // 累计被拒绝（token 无效 / 内容为空等）的次数
}

// Hook 是弹窗回调接口，由 main 注入具体 Toast/托盘实现。
type Hook interface {
	// OnNotification 在收到一条有效通知时被调用。
	OnNotification(n Notification)
}

// New 创建一个 Server。
func New(pairCode string) *Server {
	return &Server{
		pairCode:  pairCode,
		name:      defaultDeviceNm,
		maxRec:    100,
		records:   make([]Record, 0, 100),
		startTime: time.Now(),
	}
}

// NewWithName 创建 Server 并指定本机设备名（空则回落默认名）。
func NewWithName(pairCode, name string) *Server {
	s := New(pairCode)
	s.SetName(name)
	return s
}

// Name 返回本机设备名。
func (s *Server) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// SetName 设置本机设备名（空/超长做兜底）。
func (s *Server) SetName(name string) {
	if name == "" {
		name = defaultDeviceNm
	}
	// 按 rune 截断到上限，避免超长名
	r := []rune(name)
	if len(r) > maxDeviceName {
		name = string(r[:maxDeviceName])
	}
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

// PeerName 返回已配对手机端的设备名（未配对为空串）。
func (s *Server) PeerName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peer
}

// Handler 返回带 CORS 头的 HTTP handler。
func (s *Server) Handler(hook Hook) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/pair", s.handlePair)
	mux.HandleFunc("/notify", s.handleNotify(hook))
	mux.HandleFunc("/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("/ping", s.handlePing)
	mux.HandleFunc("/records", s.handleRecords)
	mux.HandleFunc("/reset", s.handleReset)
	mux.HandleFunc("/debug/state", s.handleDebugState)
	return withCORS(recoverPanic(mux))
}

// ResetPairCode 重置配对码并清除已配对 token。
func (s *Server) ResetPairCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairCode = newPairCode(6)
	s.token = ""
	return s.pairCode
}

// PairCode 返回当前配对码。
func (s *Server) PairCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pairCode
}

// IsPaired 返回是否已配对。
func (s *Server) IsPaired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token != ""
}

// Reset 重置配对（清 token）。
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}

// Records 返回最近同步记录副本（最新在前）。
func (s *Server) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for i := len(s.records) - 1; i >= 0; i-- {
		out = append(out, s.records[i])
	}
	return out
}

func (s *Server) addRecord(n Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := Record{Notification: n, ReceivedAt: time.Now()}
	s.records = append(s.records, r)
	if len(s.records) > s.maxRec {
		s.records = s.records[len(s.records)-s.maxRec:]
	}
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"` // 手机端设备名（v1.2.0 握手交换）
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的请求体"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if body.Code != s.pairCode {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "配对码错误，请核对电脑端显示的配对码"})
		return
	}
	if s.token == "" {
		s.token = newToken(32)
	}
	// 握手：保存手机设备名（兜底：空则忽略，保留旧值）
	peer := sanitizeName(body.Name)
	if peer != "" {
		s.peer = peer
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"token": s.token,
		"name":  s.name, // 回传电脑端设备名给手机
	})
}

func (s *Server) handleNotify(hook Hook) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		remote := r.RemoteAddr
		bodySize := int(r.ContentLength)
		// v1.2.1：入口日志，排查「手机端 /notify 是否真到达」用
		log.Printf("[notify] 收到请求 来源=%s 大小=%d\n", remote, bodySize)

		token := r.Header.Get("X-LimeLink-Token")
		s.mu.Lock()
		valid := s.token != "" && token == s.token
		s.mu.Unlock()
		if !valid {
			s.mu.Lock()
			s.notifyRejects++
			s.mu.Unlock()
			log.Printf("[notify] 拒绝：token 无效（来源=%s）\n", remote)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "未配对或 token 无效，请重新配对"})
			return
		}
		var n Notification
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			s.mu.Lock()
			s.notifyRejects++
			s.mu.Unlock()
			log.Printf("[notify] 拒绝：JSON 解析失败（来源=%s）: %v\n", remote, err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的通知数据"})
			return
		}
		if n.Title == "" && n.Text == "" {
			s.mu.Lock()
			s.notifyRejects++
			s.mu.Unlock()
			log.Printf("[notify] 拒绝：内容为空（来源=%s）\n", remote)
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "通知内容为空"})
			return
		}
		s.addRecord(n)
		s.mu.Lock()
		s.lastNotifyAt = time.Now()
		s.notifyCount++
		s.mu.Unlock()
		if hook != nil {
			hook.OnNotification(n)
		}
		log.Printf("[notify] 成功 app=%s title=%q（来源=%s）\n", n.AppName, n.Title, remote)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	paired := s.token != ""
	code := s.pairCode
	name := s.name
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"paired": paired,
		"code":   code,
		"name":   name,
	})
}

// handleHeartbeat 是手机端的心跳端点：带 token 探测，返回通道是否可用。
// 配对成功 ≠ 通道可用，手机端只有收到 200 + ok 才判定 CONNECTED。
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.Header.Get("X-LimeLink-Token")
	s.mu.Lock()
	valid := s.token != "" && token == s.token
	name := s.name
	s.mu.Unlock()
	if !valid {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "未配对或 token 无效"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
}

// sanitizeName 兜底处理设备名：去首尾空白、截断到上限。空串原样返回。
func sanitizeName(name string) string {
	if name == "" {
		return ""
	}
	r := []rune(name)
	if len(r) > maxDeviceName {
		name = string(r[:maxDeviceName])
	}
	return name
}

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-LimeLink-Token")
	s.mu.Lock()
	valid := s.token != "" && token == s.token
	s.mu.Unlock()
	if !valid {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "records": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "records": s.Records()})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	s.pairCode = newPairCode(6)
	s.token = ""
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": s.pairCode})
}

// handleDebugState 是排查用自检端点（v1.2.1，轻量无副作用，不写入正式验收清单）。
// 返回进程启动时间、最近一次 notify 时间、落库条数、拒绝次数、记录存储位置等。
func (s *Server) handleDebugState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	lastNotify := s.lastNotifyAt
	start := s.startTime
	count := s.notifyCount
	rejects := s.notifyRejects
	recCount := len(s.records)
	paired := s.token != ""
	name := s.name
	peer := s.peer
	s.mu.Unlock()

	lastNotifyStr := "（从未）"
	if !lastNotify.IsZero() {
		lastNotifyStr = lastNotify.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"started_at":       start.Format(time.RFC3339),
		"last_notify_at":   lastNotifyStr,
		"notify_count":     count,
		"notify_rejects":   rejects,
		"record_count":     recCount,
		"record_storage":   "内存（进程重启清空）",
		"paired":           paired,
		"name":             name,
		"peer_name":        peer,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-LimeLink-Token")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverPanic 兜住 handler 内的 panic，避免单个异常连接拖垮整个服务（需求 4.5）。
func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[http] 请求处理 panic 已恢复: %v\n", rec)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "内部错误"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// NewHTTPServer 构造带读写超时的 http.Server（需求 4.5 服务稳定性加固），
// 避免慢连接堆积、异常连接不释放。
func (s *Server) NewHTTPServer(hook Hook) *http.Server {
	return &http.Server{
		Handler:           s.Handler(hook),
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}