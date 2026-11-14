package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// handleAPIState 返回界面全量快照。
func (s *Server) handleAPIState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.Snapshot(ProtocolVersion))
}

// handleAPIEvents 是给界面的 SSE 事件流：新通知、设备变化、配对请求、定时 tick。
func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	id, ch := s.Subscribe()
	defer s.Unsubscribe(id)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			_, _ = io.WriteString(w, "data: ")
			_ = enc.Encode(ev) // Encode 自带换行，事件需要一个空行分隔
			_, _ = io.WriteString(w, "\n")
			flusher.Flush()
		case <-ticker.C:
			// 定时 tick：界面借此刷新在线状态 / 倒计时
			_, _ = io.WriteString(w, "data: {\"type\":\"tick\"}\n\n")
			flusher.Flush()
		}
	}
}

// apiBody 解析请求体（容错：空体返回空 map）。
func apiBody(r *http.Request) map[string]any {
	defer r.Body.Close()
	m := map[string]any{}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(b) == 0 {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func strField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// handleAPIName 修改本机设备名。
func (s *Server) handleAPIName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body := apiBody(r)
	name := strField(body, "name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "设备名不能为空"})
		return
	}
	s.SetName(name)
	s.save()
	s.broadcast(Event{Type: "state"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": s.Name()})
}

// handleAPIReset 重置配对码并解绑全部设备。
func (s *Server) handleAPIReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code := s.ResetPairCode()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": code})
}

// handleAPIApprove 允许一条免码配对请求。
func (s *Server) handleAPIApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body := apiBody(r)
	reqID := strField(body, "req_id")
	if reqID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "缺少 req_id"})
		return
	}
	d := s.ApprovePairRequest(reqID)
	if d == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "请求已失效"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device_id": d.ID, "device_name": d.Name})
}

// handleAPIDeny 拒绝一条免码配对请求。
func (s *Server) handleAPIDeny(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body := apiBody(r)
	reqID := strField(body, "req_id")
	if !s.DenyPairRequest(reqID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "请求已失效"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAPIKick 解绑一台设备。
func (s *Server) handleAPIKick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body := apiBody(r)
	id := strField(body, "device_id")
	if !s.KickDevice(id) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "设备不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAPISend 给手机发一条文字（手机下次心跳取走并弹通知）。
func (s *Server) handleAPISend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body := apiBody(r)
	id := strField(body, "device_id")
	text := strField(body, "text")
	if id == "" {
		// 未指定设备时发给第一台在线设备
		s.mu.Lock()
		for _, did := range s.order {
			if d, ok := s.devices[did]; ok && s.onlineLocked(d) {
				id = did
				break
			}
		}
		s.mu.Unlock()
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "没有在线设备"})
		return
	}
	if !s.QueueText(id, text) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "文字为空或设备不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device_id": id})
}

// handleAPIClear 清空通知记录。
func (s *Server) handleAPIClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.ClearRecords()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAPIReveal 在资源管理器中打开数据目录（仅本机）。
func (s *Server) handleAPIReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := s.storeDir
	if dir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "未配置数据目录"})
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := openFolder(dir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": dir})
}
