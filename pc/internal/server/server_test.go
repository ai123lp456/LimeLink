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

func TestPairAndNotifyFlow(t *testing.T) {
	s := New("123456")
	h := &fakeHook{}
	ts := httptest.NewServer(s.Handler(h))
	defer ts.Close()

	// 未配对时 notify 应返回 401
	body, _ := json.Marshal(Notification{Title: "hi", Text: "x"})
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
	res, _ = http.Post(ts.URL+"/pair", "application/json", bytes.NewReader([]byte(`{"code":"123456"}`)))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("期待 200，得到 %d", res.StatusCode)
	}
	var pr struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	json.NewDecoder(res.Body).Decode(&pr)
	res.Body.Close()
	if !pr.OK || pr.Token == "" {
		t.Fatalf("配对失败: %+v", pr)
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

	if len(h.got) != 1 {
		t.Fatalf("期望收到 1 条 hook，得到 %d", len(h.got))
	}
}