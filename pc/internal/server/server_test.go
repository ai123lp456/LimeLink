package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeHook struct{ got []Notification }

func (f *fakeHook) OnNotification(n Notification) { f.got = append(f.got, n) }

func newTestServer(t *testing.T, code string) (*Server, *httptest.Server) {
	t.Helper()
	s := New(code, t.TempDir())
	ts := httptest.NewServer(s.Handler(&fakeHook{}, nil))
	t.Cleanup(ts.Close)
	return s, ts
}

func TestPairAndNotifyFlow(t *testing.T) {
	s, ts := newTestServer(t, "123456")
	h := &fakeHook{}
	_ = h

	body, _ := json.Marshal(Notification{Title: "hi", Text: "x"})

	// 未配对时 notify 应返回 401
	res, _ := http.Post(ts.URL+"/notify", "application/json", bytes.NewReader(body))
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("期望 401，得到 %d", res.StatusCode)
	}
	res.Body.Close()

	// 错误配对码
	res, _ = http.Post(ts.URL+"/pair", "application/json", bytes.NewReader([]byte(`{"code":"000000"}`)))
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("期待 403，得到 %d", res.StatusCode)
	}
	res.Body.Close()

	// 正确配对
	res, _ = http.Post(ts.URL+"/pair", "application/json", bytes.NewReader([]byte(`{"code":"123456","name":"MyPhone","device_id":"dev-1"}`)))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("期待 200，得到 %d", res.StatusCode)
	}
	var pr struct {
		OK       bool   `json:"ok"`
		Token    string `json:"token"`
		DeviceID string `json:"device_id"`
	}
	json.NewDecoder(res.Body).Decode(&pr)
	res.Body.Close()
	if !pr.OK || pr.Token == "" {
		t.Fatalf("配对失败: %+v", pr)
	}
	if pr.DeviceID != "dev-1" {
		t.Fatalf("设备 id 应回传 dev-1，得到 %q", pr.DeviceID)
	}

	// 带 token 推送
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/notify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LimeLink-Token", pr.Token)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("推送失败：%d", res.StatusCode)
	}
	res.Body.Close()

	if got := len(s.Records()); got != 1 {
		t.Fatalf("期望 1 条记录，得到 %d", got)
	}
	st := s.Snapshot("test")
	if st.TodayCount != 1 || st.PairedCount != 1 {
		t.Fatalf("快照统计不对: %+v", st)
	}
}

// 免码配对：请求 → 电脑端允许 → 手机轮询拿到 token。
func TestPairRequestApprovalFlow(t *testing.T) {
	s, ts := newTestServer(t, "123456")

	res, err := http.Post(ts.URL+"/pair/request", "application/json",
		bytes.NewReader([]byte(`{"name":"手机A","device_id":"phone-a"}`)))
	if err != nil {
		t.Fatal(err)
	}
	var pr struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
		ReqID  string `json:"req_id"`
	}
	json.NewDecoder(res.Body).Decode(&pr)
	res.Body.Close()
	if pr.Status != "pending" || pr.ReqID == "" {
		t.Fatalf("期望 pending，得到 %+v", pr)
	}

	// 未允许前不应拿到 token
	res, _ = http.Get(ts.URL + "/pair/status?id=" + pr.ReqID)
	var st1 struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	json.NewDecoder(res.Body).Decode(&st1)
	res.Body.Close()
	if st1.Status != "pending" || st1.Token != "" {
		t.Fatalf("未允许不应给 token: %+v", st1)
	}

	if d := s.ApprovePairRequest(pr.ReqID); d == nil {
		t.Fatal("允许失败")
	}

	res, _ = http.Get(ts.URL + "/pair/status?id=" + pr.ReqID)
	var st2 struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	json.NewDecoder(res.Body).Decode(&st2)
	res.Body.Close()
	if st2.Status != "approved" || st2.Token == "" {
		t.Fatalf("允许后应拿到 token: %+v", st2)
	}

	// 同一设备再次请求：直接 approved（免重复审批，实现自动重连）
	res, _ = http.Post(ts.URL+"/pair/request", "application/json",
		bytes.NewReader([]byte(`{"name":"手机A","device_id":"phone-a"}`)))
	var again struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	json.NewDecoder(res.Body).Decode(&again)
	res.Body.Close()
	if again.Status != "approved" || again.Token != st2.Token {
		t.Fatalf("老设备应直接拿回同一 token: %+v", again)
	}
}

// 心跳应带回电脑端待发文字，并且只发一次。
func TestHeartbeatDeliversPendingText(t *testing.T) {
	s, ts := newTestServer(t, "123456")
	d := s.upsertDevice("phone-x", "手机X", "192.168.1.9")
	if !s.QueueText(d.ID, "记得买柠檬") {
		t.Fatal("排消息失败")
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/heartbeat", nil)
	req.Header.Set("X-LimeLink-Token", d.Token)
	res, _ := http.DefaultClient.Do(req)
	var hb struct {
		OK      bool      `json:"ok"`
		Name    string    `json:"name"`
		Pending []Message `json:"pending"`
	}
	json.NewDecoder(res.Body).Decode(&hb)
	res.Body.Close()
	if !hb.OK || len(hb.Pending) != 1 || hb.Pending[0].Text != "记得买柠檬" {
		t.Fatalf("心跳未带回消息: %+v", hb)
	}

	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/heartbeat", nil)
	req2.Header.Set("X-LimeLink-Token", d.Token)
	res2, _ := http.DefaultClient.Do(req2)
	var hb2 struct {
		Pending []Message `json:"pending"`
	}
	json.NewDecoder(res2.Body).Decode(&hb2)
	res2.Body.Close()
	if len(hb2.Pending) != 0 {
		t.Fatalf("消息应只发一次，第二次仍有 %d 条", len(hb2.Pending))
	}
}

// 重启后已配对设备不应丢失（token 持久化）。
func TestDevicePersistence(t *testing.T) {
	dir := t.TempDir()
	s1 := New("123456", dir)
	d := s1.upsertDevice("phone-p", "手机P", "10.0.0.2")

	s2 := New("123456", dir)
	if d2 := s2.deviceByToken(d.Token); d2 == nil || d2.ID != "phone-p" {
		t.Fatal("重启后设备 token 丢失")
	}
	if s2.PairCode() != "123456" {
		t.Fatalf("配对码应持久化，得到 %q", s2.PairCode())
	}
}

// /ping 是未鉴权端点，不能泄露配对码（公开版安全要求）。
func TestPingDoesNotLeakPairCode(t *testing.T) {
	_, ts := newTestServer(t, "987654")
	res, err := http.Get(ts.URL + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw["code"]; has {
		t.Fatalf("/ping 不应返回配对码: %+v", raw)
	}
	if raw["ok"] != true {
		t.Fatalf("/ping 应返回 ok: %+v", raw)
	}
}

// 配对码连续输错应被限速（防暴力枚举）。
func TestPairRateLimit(t *testing.T) {
	_, ts := newTestServer(t, "111111")
	statuses := make([]int, 0, pairMaxFails+1)
	for i := 0; i < pairMaxFails; i++ {
		res, err := http.Post(ts.URL+"/pair", "application/json",
			bytes.NewReader([]byte(`{"code":"000000","device_id":"x"}`)))
		if err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, res.StatusCode)
		res.Body.Close()
	}
	res, err := http.Post(ts.URL+"/pair", "application/json",
		bytes.NewReader([]byte(`{"code":"111111","device_id":"x"}`)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("失败 %d 次后应返回 429，得到 %d（历史状态 %v）", pairMaxFails, res.StatusCode, statuses)
	}
}

// 单条记录删除（界面右键菜单用）。
func TestDeleteRecord(t *testing.T) {
	s, _ := newTestServer(t, "123456")
	d := s.upsertDevice("phone-d", "手机D", "10.0.0.3")
	s.addRecord(Notification{ID: "keep", Title: "保留"}, d)
	s.addRecord(Notification{ID: "drop", Title: "删除"}, d)

	if !s.DeleteRecord("drop") {
		t.Fatal("删除失败")
	}
	recs := s.Records()
	if len(recs) != 1 || recs[0].ID != "keep" {
		t.Fatalf("删除后应只剩 keep，实际 %+v", recs)
	}
	if s.DeleteRecord("drop") {
		t.Fatal("重复删除应返回 false")
	}
}