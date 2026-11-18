package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// ---------------- v1.3.2 去重 / 合并 ----------------

// 归一化只吃「时间 / 时长 / 百分比」，纯数字（验证码、金额、条数）必须原样保留。
func TestNormalizeContent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"已使用10:42,下次提醒19:17", "已使用#:##,下次提醒#:##"},
		{"已使用 10:42，下次提醒 19:17", "已使用 #:##，下次提醒 #:##"},
		{"验证码 1234", "验证码 1234"},
		{"还剩 5 分钟", "还剩 #分钟"},
		{"进度 45%", "进度 #%"},
		{"到账 128.00 元", "到账 128.00 元"},
		{"1:02:03 已录制", "#:## 已录制"},
	}
	for _, c := range cases {
		if got := normalizeContent(c.in); got != c.want {
			t.Errorf("normalizeContent(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 验收 1/5：时间侦探式实时通知连续 20 条只落 1 行，除首次外全部静音，内容实时刷新。
func TestLiveNotificationMergesAndMutes(t *testing.T) {
	s, _ := newTestServer(t, "123456")
	d := s.upsertDevice("phone-live", "手机L", "10.0.0.4")
	const key = "0|com.thx.apptimerchan|1|null"

	var lastSilent bool
	newRows, mergedCount, silentCount := 0, 0, 0
	for i := 0; i < 20; i++ {
		n := Notification{
			ID:      fmt.Sprintf("%s-%d", key, i),
			Title:   "QQ 计时中",
			Text:    fmt.Sprintf("已使用10:%02d,下次提醒19:%02d", 30+i, 17+i),
			AppName: "时间侦探",
			Pkg:     "com.thx.apptimerchan",
			Key:     key,
			Live:    true,
		}
		_, merged, dup, silent := s.admitNotification(n, d)
		if dup {
			t.Fatalf("实时通知不应走内容去重分支")
		}
		if merged {
			mergedCount++
		} else {
			newRows++
		}
		if silent {
			silentCount++
		}
		lastSilent = silent
	}
	if newRows != 1 {
		t.Fatalf("20 条实时推送只应新增 1 行，实际新增 %d 行", newRows)
	}
	if mergedCount != 19 {
		t.Fatalf("应有 19 条被合并，实际 %d", mergedCount)
	}
	if silentCount != 19 || lastSilent != true {
		t.Fatalf("除首次外都应静音，静音 %d 条", silentCount)
	}
	recs := s.Records()
	if len(recs) != 1 {
		t.Fatalf("记录应只有 1 条，实际 %d", len(recs))
	}
	if recs[0].Text != "已使用10:49,下次提醒19:36" {
		t.Fatalf("内容应原地刷新到最后一条，实际 %q", recs[0].Text)
	}
	if recs[0].UpdatedAt.IsZero() || !recs[0].Live {
		t.Fatalf("实时记录应带 updated_at 与 live 标记: %+v", recs[0])
	}
	if !recs[0].Silent {
		t.Fatalf("合并后的记录应是静音的")
	}
}

// 验收 3/6：不同内容（含验证码）必须各自新增并响铃。
func TestDifferentContentStillAdded(t *testing.T) {
	s, _ := newTestServer(t, "123456")
	d := s.upsertDevice("phone-chat", "手机C", "10.0.0.5")

	msgs := []Notification{
		{ID: "m1", Title: "张三", Text: "在吗", Pkg: "com.tencent.mm", IconType: "wechat"},
		{ID: "m2", Title: "张三", Text: "晚上吃饭不", Pkg: "com.tencent.mm", IconType: "wechat"},
		{ID: "m3", Title: "张三", Text: "我 6 点到", Pkg: "com.tencent.mm", IconType: "wechat"},
		{ID: "c1", Title: "验证码", Text: "验证码 1234", Pkg: "com.example.bank"},
		{ID: "c2", Title: "验证码", Text: "验证码 5678", Pkg: "com.example.bank"},
	}
	for _, n := range msgs {
		if _, merged, dup, silent := s.admitNotification(n, d); merged || dup || silent {
			t.Fatalf("不同内容不应合并/去重/静音: %+v", n)
		}
	}
	if got := len(s.Records()); got != len(msgs) {
		t.Fatalf("应落 %d 条记录，实际 %d", len(msgs), got)
	}
}

// 验收 4/7：老手机端（不发 key/live）同一条消息被重发 → 不产生第 2 条记录。
func TestLegacyClientDuplicateSuppressed(t *testing.T) {
	s, _ := newTestServer(t, "123456")
	d := s.upsertDevice("phone-old", "手机O", "10.0.0.6")

	first := Notification{ID: "a-1", Title: "李四", Text: "好的", Pkg: "com.tencent.mm", IconType: "wechat"}
	if _, _, dup, _ := s.admitNotification(first, d); dup {
		t.Fatal("第一条不应被判重")
	}
	second := first
	second.ID = "a-2" // 系统重发：id 变了，内容没变
	_, merged, dup, silent := s.admitNotification(second, d)
	if merged || !dup || !silent {
		t.Fatalf("重发应被判重且静音，实际 merged=%v dup=%v silent=%v", merged, dup, silent)
	}
	if got := len(s.Records()); got != 1 {
		t.Fatalf("重发不应新增记录，实际 %d 条", got)
	}
}

// 验收 5 + 7：老客户端的计时通知（只有数字在变）也要被兜底去重。
func TestLegacyTimerSpamSuppressed(t *testing.T) {
	s, _ := newTestServer(t, "123456")
	d := s.upsertDevice("phone-t", "手机T", "10.0.0.7")

	base := Notification{Title: "QQ 计时中", AppName: "时间侦探", Pkg: "com.thx.apptimerchan"}
	a := base
	a.ID, a.Text = "t-1", "已使用10:42,下次提醒19:17"
	b := base
	b.ID, b.Text = "t-2", "已使用10:47,下次提醒19:12" // 只变数字

	if _, _, dup, _ := s.admitNotification(a, d); dup {
		t.Fatal("第一条不应被判重")
	}
	if _, _, dup, silent := s.admitNotification(b, d); !dup || !silent {
		t.Fatalf("只变数字的计时通知应被兜底去重并静音，实际 dup=%v silent=%v", dup, silent)
	}
	if got := len(s.Records()); got != 1 {
		t.Fatalf("应只有 1 条记录，实际 %d", got)
	}
}

// 验收 9：两个开关都关掉后回到 1.3.1 的行为（每条都落库、都响）。
func TestRepeatSwitchesOffRestoreLegacy(t *testing.T) {
	s, _ := newTestServer(t, "123456")
	d := s.upsertDevice("phone-off", "手机F", "10.0.0.8")
	s.SetRepeatOptions(false, false)

	const key = "0|com.thx.apptimerchan|1|null"
	for i := 0; i < 5; i++ {
		n := Notification{
			ID: fmt.Sprintf("%s-%d", key, i), Title: "QQ 计时中",
			Text: fmt.Sprintf("已使用10:%02d,下次提醒19:17", 30+i),
			Pkg:  "com.thx.apptimerchan", Key: key, Live: true,
		}
		if _, merged, dup, silent := s.admitNotification(n, d); merged || dup || silent {
			t.Fatalf("开关全关时应完全回到旧行为: merged=%v dup=%v silent=%v", merged, dup, silent)
		}
	}
	if got := len(s.Records()); got != 5 {
		t.Fatalf("开关关闭时应落 5 条，实际 %d", got)
	}
}

// 只关「静音」：仍然合并成一行，但合并的刷新会响。
func TestMuteOffKeepsMergeButRings(t *testing.T) {
	s, _ := newTestServer(t, "123456")
	d := s.upsertDevice("phone-m", "手机M", "10.0.0.9")
	s.SetRepeatOptions(true, false)

	const key = "0|pkg|1|null"
	first := Notification{ID: "k1", Title: "T", Text: "1:01", Pkg: "pkg", Key: key, Live: true}
	second := Notification{ID: "k2", Title: "T", Text: "1:02", Pkg: "pkg", Key: key, Live: true}
	s.admitNotification(first, d)
	if _, merged, _, silent := s.admitNotification(second, d); !merged || silent {
		t.Fatalf("静音关闭时合并应保留但不静音: merged=%v silent=%v", merged, silent)
	}
	if got := len(s.Records()); got != 1 {
		t.Fatalf("合并后应仍是 1 行，实际 %d", got)
	}
}

// 太久以前的实时记录不再合并，避免「昨天的倒计时吃掉今天的记录」。
func TestStaleLiveRecordNotMerged(t *testing.T) {
	s, _ := newTestServer(t, "123456")
	d := s.upsertDevice("phone-stale", "手机S", "10.0.0.11")
	const key = "0|pkg|1|null"

	s.addRecord(Notification{ID: "old", Title: "QQ 计时中", Text: "已使用10:00", Pkg: "pkg", Key: key, Live: true}, d)
	// 把它推到 1 小时前
	s.mu.Lock()
	s.records[0].ReceivedAt = s.records[0].ReceivedAt.Add(-time.Hour)
	s.mu.Unlock()

	n := Notification{ID: "new", Title: "QQ 计时中", Text: "已使用10:05", Pkg: "pkg", Key: key, Live: true}
	if _, merged, _, _ := s.admitNotification(n, d); merged {
		t.Fatal("一小时前的实时记录不应被合并")
	}
	if got := len(s.Records()); got != 2 {
		t.Fatalf("应新增一条记录，实际 %d 条", got)
	}
}

// /notify 必须同时吃老协议（protocol 3，无新字段）与新协议（protocol 4）。
func TestNotifyProtocolCompatibility(t *testing.T) {
	s, ts := newTestServer(t, "123456")
	d := s.upsertDevice("phone-proto", "手机P", "10.0.0.10")

	post := func(body string) {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/notify", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-LimeLink-Token", d.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("推送应成功，得到 %d", res.StatusCode)
		}
	}

	// protocol 3：老手机端，没有新字段
	post(`{"id":"old-1","title":"微信","text":"老客户端消息","app_name":"微信","icon_type":"wechat","pkg":"com.tencent.mm","ts":1}`)
	// protocol 4：新字段齐全
	post(`{"id":"new-1","title":"QQ 计时中","text":"已使用10:42","app_name":"时间侦探","icon_type":"app","pkg":"com.thx.apptimerchan","ts":2,"key":"k1","live":true,"silent":true,"alert_once":true,"ongoing":true}`)

	recs := s.Records()
	if len(recs) != 2 {
		t.Fatalf("两种协议都应被接受，实际 %d 条", len(recs))
	}
	// Records() 最新在前
	if !recs[0].Live || recs[0].Key != "k1" || !recs[0].AlertOnce || !recs[0].Ongoing {
		t.Fatalf("新协议的字段应被解析: %+v", recs[0])
	}
	if recs[1].Live || recs[1].Key != "" || recs[1].Title != "微信" {
		t.Fatalf("老协议不应带新字段: %+v", recs[1])
	}

	st := s.Snapshot("test")
	if st.MergeRepeat != true || st.MuteRepeat != true {
		t.Fatalf("两个开关默认应为开: %+v", st)
	}
}