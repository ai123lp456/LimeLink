// Package server 实现 LimeLink 电脑端 HTTP 服务（v1.3.0）。
//
// 面向手机端的协议端点（保持 v1.2.x 兼容）：
//   - POST /pair          提交 6 位配对码 + 设备名 + 设备 id，换取该设备的长期 token
//   - POST /pair/request  免码配对：发起「请求连接」，等电脑端界面点「允许」
//   - GET  /pair/status   轮询免码配对结果
//   - POST /notify        推送一条通知（需带 X-LimeLink-Token）
//   - GET  /heartbeat     心跳，返回电脑端名字 + 待接收文字消息（需 token）
//   - GET  /ping          无鉴权在线探测
//   - GET  /records       最近同步记录（需 token）
//   - POST /reset         重置配对码（踢掉全部设备）
//
// 面向电脑端界面（仅本机 127.0.0.1 可访问）：
//   - GET  /                      内嵌 Web 界面
//   - GET  /api/state             全量状态快照
//   - GET  /api/events            SSE 实时事件流
//   - POST /api/name              改本机设备名
//   - POST /api/reset             重置配对码
//   - POST /api/approve|deny      允许 / 拒绝免码配对请求
//   - POST /api/kick              解绑某台已配对设备
//   - POST /api/send              给手机发一条文字（手机侧弹通知）
//   - POST /api/clear             清空通知记录
package server

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"limelink/pc/internal/netinfo"
)

// 超时与容量常量（需求 4.5 服务稳定性加固）。
const (
	readTimeout     = 10 * time.Second
	writeTimeout    = 10 * time.Second
	idleTimeout     = 60 * time.Second
	maxHeaderBytes  = 1 << 20 // 1MB，防超大请求头
	maxDeviceName   = 32      // 设备名长度上限（按 rune 截断）
	defaultDeviceNm = "LimeLink-PC"

	onlineWindow = 20 * time.Second  // 超过该时长无信号即视为离线
	pendingTTL   = 120 * time.Second // 免码配对请求有效期
	maxRecords   = 200               // 通知记录上限
	maxOutbox    = 20                // 每台手机的待发文字上限
)

// ProtocolVersion 是电脑端协议版本号（发现应答与界面展示用）。
const ProtocolVersion = "1.3.0"

// Notification 是一条从手机同步过来的通知。
type Notification struct {
	ID        string `json:"id"`        // 手机端生成的通知唯一 id
	Title     string `json:"title"`     // 通知标题
	Text      string `json:"text"`      // 通知正文
	AppName   string `json:"app_name"`  // 来源 App 名
	AppLabel  string `json:"app_label"` // 归一化来源标识：wechat/sms/app/system
	IconType  string `json:"icon_type"` // 小图标类型：wechat/sms/app/system
	Pkg       string `json:"pkg"`       // 来源包名（用于 Toast Tag）
	Hidden    bool   `json:"hidden"`    // 是否隐藏内容
	Timestamp int64  `json:"ts"`        // 毫秒时间戳
}

// Record 是本地保存的一条同步记录。
type Record struct {
	Notification
	ReceivedAt time.Time `json:"received_at"`
	DeviceID   string    `json:"device_id,omitempty"`
	DeviceName string    `json:"device_name,omitempty"`
}

// Device 是一台已配对的手机。
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Token    string    `json:"token"`
	IP       string    `json:"ip"`
	LastSeen time.Time `json:"last_seen"`
	PairedAt time.Time `json:"paired_at"`
}

// DeviceView 是给界面用的设备视图（不含 token）。
type DeviceView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	IP           string `json:"ip"`
	Online       bool   `json:"online"`
	LastSeen     int64  `json:"last_seen"`
	LastSeenText string `json:"last_seen_text"`
	PairedAt     int64  `json:"paired_at"`
}

// PendingRequest 是一条等待电脑端确认的免码配对请求。
type PendingRequest struct {
	ID       string    `json:"id"`
	DeviceID string    `json:"device_id"`
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	Created  time.Time `json:"created"`
	// Status 为空 = 待确认；approved / denied 为终态
	Status string `json:"status,omitempty"`
	// Token 在允许后回填，供手机端轮询取走
	Token string `json:"token,omitempty"`
}

// PendingView 是给界面用的配对请求视图。
type PendingView struct {
	ID        string `json:"id"`
	DeviceID  string `json:"device_id"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	CreatedAt int64  `json:"created_at"`
	WaitText  string `json:"wait_text"`
	RemainSec int    `json:"remain_sec"`
}

// Message 是电脑端发给手机的一条文字（手机侧弹通知）。
type Message struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	TS   int64  `json:"ts"`
}

// Event 是推给界面的 SSE 事件。
type Event struct {
	Type   string  `json:"type"`
	Record *Record `json:"record,omitempty"`
	Text   string  `json:"text,omitempty"`
}

// Hook 是弹窗回调接口，由 main 注入具体 Toast 实现。
type Hook interface {
	// OnNotification 在收到一条有效通知时被调用。
	OnNotification(n Notification)
}

// Server 持有服务状态。
type Server struct {
	mu       sync.Mutex
	pairCode string // 当前 6 位配对码
	name     string // 本机（电脑端）设备名
	port     int

	devices map[string]*Device // key = 设备 id
	order   []string           // 设备加入顺序

	pending map[string]*PendingRequest // key = 请求 id
	outbox  map[string][]Message       // key = 设备 id

	// 配对码暴力尝试限制（v1.3.0 公开版）：每个 IP 5 次失败后锁 5 分钟
	fails map[string]*attempt

	records []Record // 最近同步记录（旧在前）
	subs    map[int]chan Event
	nextSub int

	startTime     time.Time
	lastNotifyAt  time.Time
	notifyCount   int
	notifyRejects int
	todayDate     string
	todayCount    int

	storeDir string
}

// New 创建一个 Server，并尝试从 storeDir 恢复设备与记录。
func New(pairCode string, storeDir string) *Server {
	s := &Server{
		pairCode:  pairCode,
		name:      defaultDeviceNm,
		devices:   make(map[string]*Device),
		pending:   make(map[string]*PendingRequest),
		outbox:    make(map[string][]Message),
		fails:     make(map[string]*attempt),
		records:   make([]Record, 0, maxRecords),
		subs:      make(map[int]chan Event),
		startTime: time.Now(),
		todayDate: time.Now().Format("2006-01-02"),
		storeDir:  storeDir,
	}
	s.load()
	return s
}

// NewWithName 创建 Server 并指定本机设备名（空则回落默认名）。
func NewWithName(pairCode, name, storeDir string) *Server {
	s := New(pairCode, storeDir)
	s.SetName(name)
	return s
}

// SetPort 记录 HTTP 监听端口（用于界面展示与发现应答）。
func (s *Server) SetPort(p int) {
	s.mu.Lock()
	s.port = p
	s.mu.Unlock()
}

// Port 返回 HTTP 监听端口。
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// Name 返回本机设备名。
func (s *Server) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// SetName 设置本机设备名（空/超长做兜底）。
func (s *Server) SetName(name string) {
	name = sanitizeName(name)
	if name == "" {
		name = defaultDeviceNm
	}
	s.mu.Lock()
	s.name = name
	s.mu.Unlock()
}

// PairCode 返回当前配对码。
func (s *Server) PairCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pairCode
}

// IsPaired 返回是否已有设备配对。
func (s *Server) IsPaired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.devices) > 0
}

// ResetPairCode 重置配对码并清空全部配对设备。
func (s *Server) ResetPairCode() string {
	s.mu.Lock()
	s.pairCode = newPairCode(6)
	s.devices = make(map[string]*Device)
	s.order = nil
	s.pending = make(map[string]*PendingRequest)
	s.outbox = make(map[string][]Message)
	code := s.pairCode
	s.mu.Unlock()
	s.save()
	s.broadcast(Event{Type: "devices"})
	return code
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

// ClearRecords 清空通知记录。
func (s *Server) ClearRecords() {
	s.mu.Lock()
	s.records = s.records[:0]
	s.mu.Unlock()
	s.save()
	s.broadcast(Event{Type: "records"})
}

func (s *Server) addRecord(n Notification, dev *Device) Record {
	r := Record{Notification: n, ReceivedAt: time.Now()}
	if dev != nil {
		r.DeviceID = dev.ID
		r.DeviceName = dev.Name
	}
	s.mu.Lock()
	s.records = append(s.records, r)
	if len(s.records) > maxRecords {
		s.records = s.records[len(s.records)-maxRecords:]
	}
	s.lastNotifyAt = r.ReceivedAt
	s.notifyCount++
	today := r.ReceivedAt.Format("2006-01-02")
	if today != s.todayDate {
		s.todayDate = today
		s.todayCount = 0
	}
	s.todayCount++
	s.mu.Unlock()
	return r
}

// ---- 设备 ----

func (s *Server) deviceByToken(token string) *Device {
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.Token == token {
			return d
		}
	}
	return nil
}

// touch 更新设备在线信号。
func (s *Server) touch(d *Device, ip string) {
	if d == nil {
		return
	}
	s.mu.Lock()
	d.LastSeen = time.Now()
	if ip != "" {
		d.IP = ip
	}
	s.mu.Unlock()
}

// NotePeerSeen 记录一次 UDP 发现信号（手机在线但可能尚未配对）。
func (s *Server) NotePeerSeen(deviceID, name, ip string) {
	if deviceID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.devices[deviceID]; ok {
		d.LastSeen = time.Now()
		if ip != "" {
			d.IP = ip
		}
		if n := sanitizeName(name); n != "" {
			d.Name = n
		}
	}
}

func (s *Server) onlineLocked(d *Device) bool {
	if d == nil || d.LastSeen.IsZero() {
		return false
	}
	return time.Since(d.LastSeen) <= onlineWindow
}

// ---- 事件广播（SSE） ----

// Subscribe 注册一个事件订阅者。
func (s *Server) Subscribe() (int, <-chan Event) {
	ch := make(chan Event, 64)
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	s.mu.Unlock()
	return id, ch
}

// Unsubscribe 注销事件订阅者。
func (s *Server) Unsubscribe(id int) {
	s.mu.Lock()
	if ch, ok := s.subs[id]; ok {
		delete(s.subs, id)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *Server) broadcast(ev Event) {
	s.mu.Lock()
	subs := make([]chan Event, 0, len(s.subs))
	for _, ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // 订阅者太慢就丢事件，界面下次拉快照自愈
		}
	}
}

// ---- 快照 ----

// State 是给界面的全量状态。
type State struct {
	Version       string       `json:"version"`
	Name          string       `json:"name"`
	Port          int          `json:"port"`
	Code          string       `json:"code"`
	PairedCount   int          `json:"paired_count"`
	OnlineCount   int          `json:"online_count"`
	StartedAt     int64        `json:"started_at"`
	UptimeSec     int64        `json:"uptime_sec"`
	NotifyCount   int          `json:"notify_count"`
	TodayCount    int          `json:"today_count"`
	NotifyRejects int          `json:"notify_rejects"`
	LastNotifyAt  string       `json:"last_notify_at"`
	Devices       []DeviceView `json:"devices"`
	Records       []Record     `json:"records"`
	Pending       []PendingView `json:"pending"`
	IPs           []IPView     `json:"ips"`
	StoreDir      string       `json:"store_dir"`
}

// IPView 是一块网卡的地址。
type IPView struct {
	Iface string `json:"iface"`
	IP    string `json:"ip"`
	Kind  string `json:"kind"`
}

// Snapshot 返回界面所需的完整状态。
func (s *Server) Snapshot(version string) State {
	s.prunePending()
	s.mu.Lock()
	st := State{
		Version:       version,
		Name:          s.name,
		Port:          s.port,
		Code:          s.pairCode,
		StartedAt:     s.startTime.UnixMilli(),
		NotifyCount:   s.notifyCount,
		TodayCount:    s.todayCount,
		NotifyRejects: s.notifyRejects,
		StoreDir:      s.storeDir,
	}
	if !s.lastNotifyAt.IsZero() {
		st.LastNotifyAt = s.lastNotifyAt.Format("2006-01-02 15:04:05")
	}
	now := time.Now()
	for _, id := range s.order {
		d, ok := s.devices[id]
		if !ok {
			continue
		}
		v := DeviceView{
			ID:       d.ID,
			Name:     d.Name,
			Type:     d.Type,
			IP:       d.IP,
			Online:   s.onlineLocked(d),
			PairedAt: d.PairedAt.UnixMilli(),
		}
		if !d.LastSeen.IsZero() {
			v.LastSeen = d.LastSeen.UnixMilli()
			v.LastSeenText = humanAgo(now.Sub(d.LastSeen))
		}
		if v.Online {
			st.OnlineCount++
		}
		st.Devices = append(st.Devices, v)
	}
	st.PairedCount = len(st.Devices)
	for i := len(s.records) - 1; i >= 0; i-- {
		st.Records = append(st.Records, s.records[i])
	}
	if st.Records == nil {
		st.Records = []Record{}
	}
	if st.Devices == nil {
		st.Devices = []DeviceView{}
	}
	pending := make([]PendingView, 0, len(s.pending))
	for _, p := range s.pending {
		// 已处理（允许/拒绝）的请求不再出现在界面里
		if p.Status != "" {
			continue
		}
		remain := int(pendingTTL.Seconds() - now.Sub(p.Created).Seconds())
		if remain < 0 {
			remain = 0
		}
		pending = append(pending, PendingView{
			ID:        p.ID,
			DeviceID:  p.DeviceID,
			Name:      p.Name,
			IP:        p.IP,
			CreatedAt: p.Created.UnixMilli(),
			WaitText:  humanAgo(now.Sub(p.Created)),
			RemainSec: remain,
		})
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].CreatedAt > pending[j].CreatedAt })
	st.Pending = pending
	s.mu.Unlock()

	st.UptimeSec = int64(time.Since(s.startTime).Seconds())
	for _, e := range netinfo.ListLocalIPv4() {
		st.IPs = append(st.IPs, IPView{Iface: e.Iface, IP: e.IP, Kind: ipKind(e.IP)})
	}
	if st.IPs == nil {
		st.IPs = []IPView{}
	}
	return st
}

func ipKind(ip string) string {
	p := net.ParseIP(ip)
	if p == nil {
		return "lan"
	}
	switch {
	case strings.HasPrefix(ip, "192.168.137."):
		return "hotspot"
	case p.IsPrivate():
		return "lan"
	default:
		return "wan"
	}
}

func humanAgo(d time.Duration) string {
	if d < 5*time.Second {
		return "刚刚"
	}
	if d < time.Minute {
		return formatInt(int(d.Seconds())) + " 秒前"
	}
	if d < time.Hour {
		return formatInt(int(d.Minutes())) + " 分钟前"
	}
	if d < 24*time.Hour {
		return formatInt(int(d.Hours())) + " 小时前"
	}
	return formatInt(int(d.Hours()/24)) + " 天前"
}

func formatInt(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ---- HTTP 路由 ----

// Handler 返回电脑端 HTTP handler。ui 为界面静态资源处理器（可为 nil）。
func (s *Server) Handler(hook Hook, ui http.Handler) http.Handler {
	mux := http.NewServeMux()

	// 手机端协议
	mux.HandleFunc("/pair", s.handlePair)
	mux.HandleFunc("/pair/request", s.handlePairRequest)
	mux.HandleFunc("/pair/status", s.handlePairStatus)
	mux.HandleFunc("/notify", s.handleNotify(hook))
	mux.HandleFunc("/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("/ping", s.handlePing)
	mux.HandleFunc("/records", s.handleRecords)
	mux.HandleFunc("/reset", s.handleReset)
	mux.HandleFunc("/debug/state", s.localAPI(s.handleDebugState))

	// 电脑端界面 API（仅本机）
	mux.HandleFunc("/api/state", s.localAPI(s.handleAPIState))
	mux.HandleFunc("/api/events", s.localAPI(s.handleAPIEvents))
	mux.HandleFunc("/api/name", s.localAPI(s.handleAPIName))
	mux.HandleFunc("/api/reset", s.localAPI(s.handleAPIReset))
	mux.HandleFunc("/api/approve", s.localAPI(s.handleAPIApprove))
	mux.HandleFunc("/api/deny", s.localAPI(s.handleAPIDeny))
	mux.HandleFunc("/api/kick", s.localAPI(s.handleAPIKick))
	mux.HandleFunc("/api/send", s.localAPI(s.handleAPISend))
	mux.HandleFunc("/api/clear", s.localAPI(s.handleAPIClear))
	mux.HandleFunc("/api/reveal", s.localAPI(s.handleAPIReveal))

	if ui != nil {
		mux.Handle("/", s.localAPI(ui.ServeHTTP))
	}
	return recoverPanic(mux)
}

// localAPI 限制仅本机回环地址可访问界面与其 API。
func (s *Server) localAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"ok":    false,
				"error": "电脑端界面仅允许本机访问",
			})
			return
		}
		next(w, r)
	}
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- 手机端协议处理 ----

// handlePing 是无鉴权的在线探测：只回「是否已配对 / 设备名 / 端口 / 版本」。
//
// 注意：不返回配对码。v1.2.x 曾在这里回 code，等于把配对码广播给整个局域网，
// 公开版已移除（手机端配对走 /pair/request 与 /pair，不依赖该字段）。
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	paired := len(s.devices) > 0
	name := s.name
	port := s.port
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"paired":   paired,
		"name":     name,
		"port":     port,
		"version":  ProtocolVersion,
		"protocol": 3,
	})
}

// handleHeartbeat 是手机端心跳：带 token 探测，返回通道是否可用 + 待收文字。
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.Header.Get("X-LimeLink-Token")
	d := s.deviceByToken(token)
	if d == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "未配对或 token 无效"})
		return
	}
	s.touch(d, remoteIP(r))
	msgs := s.takeOutbox(d.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"name":     s.Name(),
		"version":  ProtocolVersion,
		"pending":  msgs,
		"interval": 5,
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

		token := r.Header.Get("X-LimeLink-Token")
		d := s.deviceByToken(token)
		if d == nil {
			s.mu.Lock()
			s.notifyRejects++
			s.mu.Unlock()
			log.Printf("[notify] 拒绝：token 无效（来源=%s 大小=%d）\n", remote, bodySize)
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
		s.touch(d, remoteIP(r))
		rec := s.addRecord(n, d)
		s.save()
		if hook != nil {
			hook.OnNotification(n)
		}
		s.broadcast(Event{Type: "notify", Record: &rec})
		log.Printf("[notify] 成功 app=%s title=%q（来源=%s）\n", n.AppName, n.Title, remote)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-LimeLink-Token")
	if s.deviceByToken(token) == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "records": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "records": s.Records()})
}

// handleReset 保留 v1.2.x 兼容；仅本机可调用。
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopback(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "仅允许本机调用"})
		return
	}
	code := s.ResetPairCode()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": code})
}

// handleDebugState 是排查用自检端点（轻量无副作用）。
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
	paired := len(s.devices)
	devs := make([]string, 0, paired)
	for _, id := range s.order {
		if d, ok := s.devices[id]; ok {
			devs = append(devs, d.Name+"("+d.IP+")")
		}
	}
	name := s.name
	s.mu.Unlock()

	lastNotifyStr := "（从未）"
	if !lastNotify.IsZero() {
		lastNotifyStr = lastNotify.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"version":        ProtocolVersion,
		"started_at":     start.Format(time.RFC3339),
		"last_notify_at": lastNotifyStr,
		"notify_count":   count,
		"notify_rejects": rejects,
		"record_count":   recCount,
		"record_storage": s.storeDir,
		"paired":         paired > 0,
		"paired_count":   paired,
		"devices":        devs,
		"name":           name,
	})
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	r := []rune(name)
	if len(r) > maxDeviceName {
		name = string(r[:maxDeviceName])
	}
	return name
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// 说明：v1.3.0 起不再返回 Access-Control-Allow-Origin: *。
// 手机端是原生 App、界面与接口同源，都不需要跨域；去掉后浏览器网页无法跨域调用局域网接口。

// recoverPanic 兜住 handler 内的 panic，避免单个异常连接拖垮整个服务。
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

// NewHTTPServer 构造带读写超时的 http.Server，避免慢连接堆积。
func (s *Server) NewHTTPServer(hook Hook, ui http.Handler) *http.Server {
	return &http.Server{
		Handler:           s.Handler(hook, ui),
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}
