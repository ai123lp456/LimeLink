package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"encoding/json"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const codeChars = "0123456789"

// newPairCode 生成 n 位数字配对码。
func newPairCode(n int) string {
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeChars))))
		if err != nil {
			b[i] = '0'
			continue
		}
		b[i] = codeChars[v.Int64()]
	}
	return string(b)
}

const tokenChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// newToken 生成长度 n 的随机 token。
func newToken(n int) string {
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(tokenChars))))
		if err != nil {
			b[i] = '0'
			continue
		}
		b[i] = tokenChars[v.Int64()]
	}
	return string(b)
}

// newDeviceID 生成设备 id（16 位十六进制）。
func newDeviceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "dev" + newToken(12)
	}
	return hex.EncodeToString(b)
}

// sanitizeDeviceID 只接受安全字符，空则新建。
func sanitizeDeviceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return newDeviceID()
	}
	var b strings.Builder
	for _, r := range id {
		if r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return newDeviceID()
	}
	return b.String()
}

// attempt 记录某个 IP 的配对码失败次数（防暴力枚举）。
type attempt struct {
	fails int
	reset time.Time
}

const (
	pairMaxFails   = 5
	pairLockWindow = 5 * time.Minute
	maxPending     = 8
)

// pairBlocked 判断该 IP 是否因失败过多被临时锁定。
func (s *Server) pairBlocked(ip string) (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.fails[ip]
	if a == nil || time.Now().After(a.reset) {
		return false, 0
	}
	if a.fails >= pairMaxFails {
		return true, int(time.Until(a.reset).Seconds())
	}
	return false, 0
}

// notePairFail 记一次失败；达到上限后锁定窗口。
func (s *Server) notePairFail(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.fails[ip]
	if a == nil || time.Now().After(a.reset) {
		a = &attempt{reset: time.Now().Add(pairLockWindow)}
		s.fails[ip] = a
	}
	a.fails++
}

// clearPairFails 配对成功后清零。
func (s *Server) clearPairFails(ip string) {
	s.mu.Lock()
	delete(s.fails, ip)
	s.mu.Unlock()
}

// handlePair 是 v1.2.x 兼容的配对码配对：码对即建/取设备并回 token。
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	ip := remoteIP(r)
	if blocked, secs := s.pairBlocked(ip); blocked {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("配对码错误次数过多，请 %d 秒后再试", secs),
		})
		return
	}
	var body struct {
		Code     string `json:"code"`
		Name     string `json:"name"`
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的请求体"})
		return
	}
	if body.Code != s.PairCode() {
		s.notePairFail(ip)
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "配对码错误，请核对电脑端显示的配对码"})
		return
	}
	s.clearPairFails(ip)
	d := s.upsertDevice(sanitizeDeviceID(body.DeviceID), sanitizeName(body.Name), ip)
	if d == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "设备登记失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"token":     d.Token,
		"name":      s.Name(),
		"device_id": d.ID,
		"version":   ProtocolVersion,
	})
}

// upsertDevice 建立或更新一台设备（同一设备 id 复用旧 token，保证重启后不用重新配对）。
func (s *Server) upsertDevice(id, name, ip string) *Device {
	if id == "" {
		id = newDeviceID()
	}
	if name == "" {
		name = "LimeLink-Phone"
	}
	s.mu.Lock()
	d, ok := s.devices[id]
	if !ok {
		d = &Device{ID: id, Name: name, Type: "phone", Token: newToken(32), PairedAt: time.Now()}
		s.devices[id] = d
		s.order = append(s.order, id)
	} else {
		d.Name = name
		if d.Token == "" {
			d.Token = newToken(32)
		}
	}
	d.IP = ip
	d.LastSeen = time.Now()
	s.mu.Unlock()
	s.save()
	s.broadcast(Event{Type: "devices"})
	return d
}

// handlePairRequest 是免码配对：手机发起「请求连接」，电脑端界面点「允许」后发 token。
//
// 已配对过的设备（device_id 命中）直接回 token，实现「配对过一次，以后自动重连」。
func (s *Server) handlePairRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var body struct {
		Name     string `json:"name"`
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的请求体"})
		return
	}
	id := sanitizeDeviceID(body.DeviceID)
	name := sanitizeName(body.Name)
	ip := remoteIP(r)

	// 老设备：直接发 token（免审批）
	s.mu.Lock()
	if d, ok := s.devices[id]; ok && d.Token != "" {
		if name != "" {
			d.Name = name
		}
		d.LastSeen = time.Now()
		d.IP = ip
		token := d.Token
		s.mu.Unlock()
		s.save()
		s.broadcast(Event{Type: "devices"})
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"status":    "approved",
			"token":     token,
			"name":      s.Name(),
			"device_id": id,
		})
		return
	}
	// 已有同设备待确认请求：复用
	for _, p := range s.pending {
		if p.DeviceID == id && p.Status == "" && time.Since(p.Created) < pendingTTL {
			reqID := p.ID
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":        true,
				"status":    "pending",
				"req_id":    reqID,
				"device_id": id,
			})
			return
		}
	}
	// 待确认请求过多：防止有人刷屏
	if len(s.pending) >= maxPending {
		s.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok":    false,
			"error": "待确认的连接请求过多，请先在电脑端处理",
		})
		return
	}
	p := &PendingRequest{
		ID:       newToken(10),
		DeviceID: id,
		Name:     name,
		IP:       ip,
		Created:  time.Now(),
	}
	s.pending[p.ID] = p
	reqID := p.ID
	s.mu.Unlock()

	log.Printf("[pair] 新的连接请求 设备=%s 名称=%q 来源=%s\n", id, name, ip)
	s.broadcast(Event{Type: "pair-request", Text: name})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"status":    "pending",
		"req_id":    reqID,
		"device_id": id,
		"name":      s.Name(),
	})
}

// handlePairStatus 让手机轮询免码配对结果。
func (s *Server) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	reqID := r.URL.Query().Get("id")
	if reqID == "" {
		reqID = r.URL.Query().Get("req_id")
	}
	s.prunePending()
	s.mu.Lock()
	p, ok := s.pending[reqID]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "expired"})
		return
	}
	status := p.Status
	token := p.Token
	deviceID := p.DeviceID
	s.mu.Unlock()

	resp := map[string]any{"ok": true, "device_id": deviceID}
	if status == "" {
		resp["status"] = "pending"
	} else {
		resp["status"] = status
	}
	if status == "approved" {
		resp["token"] = token
		resp["name"] = s.Name()
	}
	writeJSON(w, http.StatusOK, resp)
}

// prunePending 清理过期请求。
func (s *Server) prunePending() {
	s.mu.Lock()
	for id, p := range s.pending {
		age := time.Since(p.Created)
		if p.Status == "" && age > pendingTTL {
			delete(s.pending, id)
			continue
		}
		if p.Status != "" && age > 3*pendingTTL {
			delete(s.pending, id)
		}
	}
	s.mu.Unlock()
}

// ApprovePairRequest 允许一条配对请求，返回设备（不存在返回 nil）。
func (s *Server) ApprovePairRequest(reqID string) *Device {
	s.mu.Lock()
	p, ok := s.pending[reqID]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	id, name, ip := p.DeviceID, p.Name, p.IP
	s.mu.Unlock()

	d := s.upsertDevice(id, name, ip)
	if d == nil {
		return nil
	}
	s.mu.Lock()
	if p2, ok := s.pending[reqID]; ok {
		p2.Status = "approved"
		p2.Token = d.Token
	}
	s.mu.Unlock()
	log.Printf("[pair] 已允许设备 %s(%s)\n", d.Name, d.IP)
	s.broadcast(Event{Type: "devices"})
	return d
}

// DenyPairRequest 拒绝一条配对请求。
func (s *Server) DenyPairRequest(reqID string) bool {
	s.mu.Lock()
	p, ok := s.pending[reqID]
	if ok {
		p.Status = "denied"
	}
	s.mu.Unlock()
	if ok {
		log.Printf("[pair] 已拒绝请求 %s\n", reqID)
		s.broadcast(Event{Type: "pair-request"})
	}
	return ok
}

// KickDevice 解绑一台设备（手机端 token 立即失效）。
func (s *Server) KickDevice(deviceID string) bool {
	s.mu.Lock()
	if _, ok := s.devices[deviceID]; !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.devices, deviceID)
	order := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if id != deviceID {
			order = append(order, id)
		}
	}
	s.order = order
	delete(s.outbox, deviceID)
	s.mu.Unlock()
	s.save()
	s.broadcast(Event{Type: "devices"})
	return true
}

// QueueText 给指定设备排一条文字消息，等它下次心跳取走。
func (s *Server) QueueText(deviceID, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if len([]rune(text)) > 500 {
		text = string([]rune(text)[:500])
	}
	s.mu.Lock()
	if _, ok := s.devices[deviceID]; !ok {
		s.mu.Unlock()
		return false
	}
	box := s.outbox[deviceID]
	if len(box) >= maxOutbox {
		box = box[len(box)-maxOutbox+1:]
	}
	box = append(box, Message{ID: newToken(8), Text: text, TS: time.Now().UnixMilli()})
	s.outbox[deviceID] = box
	s.mu.Unlock()
	return true
}

// takeOutbox 取走并清空某设备的待发消息。
func (s *Server) takeOutbox(deviceID string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.outbox[deviceID]
	if len(msgs) == 0 {
		return []Message{}
	}
	delete(s.outbox, deviceID)
	return msgs
}

// ---- 持久化 ----

type storedData struct {
	Devices []*Device `json:"devices"`
	Records []Record  `json:"records"`
	Code    string    `json:"pair_code"`
}

func (s *Server) dataPath() string {
	if s.storeDir == "" {
		return ""
	}
	return filepath.Join(s.storeDir, "state.json")
}

// load 从磁盘恢复设备、配对码与记录（失败则当首次运行）。
func (s *Server) load() {
	p := s.dataPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var d storedData
	if err := json.Unmarshal(b, &d); err != nil {
		log.Printf("[store] 状态文件解析失败，按首次运行处理: %v\n", err)
		return
	}
	s.mu.Lock()
	for _, dev := range d.Devices {
		if dev == nil || dev.ID == "" || dev.Token == "" {
			continue
		}
		if dev.Type == "" {
			dev.Type = "phone"
		}
		s.devices[dev.ID] = dev
		s.order = append(s.order, dev.ID)
	}
	if d.Code != "" {
		s.pairCode = d.Code
	}
	if len(d.Records) > 0 {
		start := 0
		if len(d.Records) > maxRecords {
			start = len(d.Records) - maxRecords
		}
		s.records = append(s.records, d.Records[start:]...)
	}
	n := len(s.records)
	m := len(s.devices)
	s.mu.Unlock()
	if n > 0 || m > 0 {
		log.Printf("[store] 已恢复 %d 台设备、%d 条记录（%s）\n", m, n, p)
	}
}

// save 原子写盘（先写临时文件再改名），避免半截文件。
func (s *Server) save() {
	p := s.dataPath()
	if p == "" {
		return
	}
	s.mu.Lock()
	d := storedData{Code: s.pairCode}
	d.Records = append(d.Records, s.records...)
	for _, id := range s.order {
		if dev, ok := s.devices[id]; ok {
			d.Devices = append(d.Devices, dev)
		}
	}
	s.mu.Unlock()

	if err := os.MkdirAll(s.storeDir, 0o755); err != nil {
		log.Printf("[store] 建目录失败: %v\n", err)
		return
	}
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("[store] 写临时文件失败: %v\n", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		log.Printf("[store] 替换状态文件失败: %v\n", err)
	}
}
