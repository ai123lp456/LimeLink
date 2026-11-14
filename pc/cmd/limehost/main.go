// Command limehost 是 LimeLink 电脑端常驻程序（v1.3.0）。
//
// 启动后做四件事：
//  1. 起 HTTP 服务（默认 8787），接收手机推送的通知；界面 API 也在这里；
//  2. 起 UDP 发现服务（8788），定时广播自己 + 应答手机探测（LocalSend 那套思路）；
//  3. 托盘图标常驻，右键可打开主界面 / 看地址 / 改设备名 / 退出；
//  4. 打开内嵌 Web 界面窗口（Edge 应用模式），也就是电脑端 UI。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gen2brain/dlgs"
	"github.com/getlantern/systray"
	"limelink/pc/internal/config"
	"limelink/pc/internal/discovery"
	"limelink/pc/internal/netinfo"
	"limelink/pc/internal/notifier"
	"limelink/pc/internal/server"
	"limelink/pc/internal/webui"
)

// appID 是 Toast 的 AppUserModelID（含版本，便于系统通知设置里区分）。
const appID = "LimeLink.Notification.1.3.0"

// appVersion 是电脑端版本号（同步体现在 exe 资源、界面、README）。
const appVersion = "1.3.0"

func main() {
	setupLog()
	log.Println("[boot] step1 setupLog")

	cfg := config.Load()
	log.Printf("[boot] step2 config port=%d name=%q\n", cfg.Port, cfg.Name())

	// 已有实例在跑：把界面叫出来就行，不再抢端口
	if alreadyRunning(cfg.Port) {
		log.Println("[boot] step3 already running, opening window")
		_ = openWindow(uiURL(cfg.Port))
		return
	}
	log.Println("[boot] step3 no running instance")

	srv := server.NewWithName(newPairCode6(), cfg.Name(), config.ConfigDir())
	srv.SetPort(cfg.Port)
	ntf := notifier.New(appID)
	httpSrv := srv.NewHTTPServer(ntf, webui.Handler())
	log.Println("[boot] step4 server ready")

	// 先开窗口：首次启动 Windows 放行防火墙可能要十几秒，
	// 界面会显示「正在启动」并在服务就绪后自动接管。
	if !cfg.NoWindow {
		if err := openWindow(uiURL(cfg.Port)); err != nil {
			log.Printf("[ui] 打开界面失败: %v\n", err)
		}
	}

	ln, port, err := listenWithFallback(cfg.Port)
	if err != nil {
		log.Printf("[fatal] 端口 %d~%d 均被占用: %v\n", cfg.Port, cfg.Port+4, err)
		messageBox("小青柠 LimeLink",
			fmt.Sprintf("端口 %d~%d 都被占用，无法启动。\n请关闭占用端口的程序后重试。", cfg.Port, cfg.Port+4))
		return
	}
	log.Printf("[boot] step5 listening on %d\n", port)
	if port != cfg.Port {
		log.Printf("[http] 端口 %d 被占用，改用 %d\n", cfg.Port, port)
	}
	cfg.Port = port
	srv.SetPort(port)

	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[http] 服务退出: %v\n", err)
		}
	}()

	disc := discovery.New(discoveryProvider{srv: srv, port: port}, discovery.DefaultPort)
	go func() {
		if err := disc.Start(); err != nil {
			log.Printf("[discovery] 发现服务启动失败（界面仍可用）: %v\n", err)
		}
	}()

	log.Printf("[http] 小青柠 LimeLink v%s 已启动，端口 %d（发现 %d），本机 IP %s，设备名 %s\n",
		appVersion, port, discovery.DefaultPort, netinfo.LocalIPv4(), cfg.Name())

	systray.Run(onReady(srv, ntf, cfg, httpSrv, disc), onExit)
}

// alreadyRunning 探测同一端口上是否已经有小青柠在跑。
func alreadyRunning(port int) bool {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/ping", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var v struct {
		Ok   bool   `json:"ok"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return false
	}
	return v.Ok
}

// listenWithFallback 依次尝试端口，全部失败才报错。
// 显式绑 0.0.0.0（而不是空主机名），避免某些 Windows 环境下双栈探测拖慢启动。
func listenWithFallback(start int) (net.Listener, int, error) {
	var lastErr error
	for i := 0; i < 5; i++ {
		p := start + i
		t0 := time.Now()
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
		if err == nil {
			log.Printf("[boot] listen 0.0.0.0:%d 耗时 %v\n", p, time.Since(t0).Round(time.Millisecond))
			return ln, p, nil
		}
		log.Printf("[boot] listen 0.0.0.0:%d 失败(%v) 耗时 %v\n", p, err, time.Since(t0).Round(time.Millisecond))
		lastErr = err
	}
	return nil, start, lastErr
}

// setupLog 把日志写到用户目录，windowsgui 模式（无控制台）下仍可排查。
func setupLog() {
	dir := config.ConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	p := filepath.Join(dir, "limehost.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
		_ = os.Remove(p) // 超过 1MB 就重开，避免无限增长
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(f))
	log.SetFlags(log.LstdFlags)
}

// discoveryProvider 实现 discovery.InfoProvider，提供电脑端信息给 UDP 应答。
type discoveryProvider struct {
	srv  *server.Server
	port int
}

func (p discoveryProvider) Get() (name string, port int, version string) {
	return p.srv.Name(), p.port, appVersion
}

// OnPeer 收到手机广播：记录下来，界面里就能显示该手机在线。
func (p discoveryProvider) OnPeer(id, name, ip string) {
	p.srv.NotePeerSeen(id, name, ip)
}

func onReady(srv *server.Server, ntf *notifier.Notifier, cfg config.Config, httpSrv *http.Server, disc *discovery.Service) func() {
	return func() {
		systray.SetIcon(loadIcon())
		systray.SetTooltip(fmt.Sprintf("小青柠 LimeLink v%s", appVersion))

		mOpen := systray.AddMenuItem("打开主界面", "打开小青柠的电脑端界面")
		mStatus := systray.AddMenuItem("状态：启动中…", "当前连接概况")
		mStatus.Disable()
		mIP := systray.AddMenuItem(ipMenuTitle(cfg), "点击列出本机所有可用局域网地址")
		mPair := systray.AddMenuItem(fmt.Sprintf("配对码：%s", srv.PairCode()), "当前配对码")
		mRename := systray.AddMenuItem("设备改名", "修改电脑端设备名（手机端可见）")
		mRecords := systray.AddMenuItem("最近同步记录", "查看最近通知")
		systray.AddSeparator()
		mNoWin := systray.AddMenuItemCheckbox("启动时打开界面", "下次启动是否自动打开电脑端窗口", !cfg.NoWindow)
		mReset := systray.AddMenuItem("重置配对", "重新生成配对码并解绑全部手机")
		mAbout := systray.AddMenuItem("关于", "版本信息")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "退出小青柠")

		// 状态行定时刷新
		go func() {
			tick := time.NewTicker(3 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-tick.C:
					st := srv.Snapshot(appVersion)
					mStatus.SetTitle(fmt.Sprintf("状态：%d/%d 台在线 · 今日 %d 条",
						st.OnlineCount, st.PairedCount, st.TodayCount))
					mIP.SetTitle(ipMenuTitle(cfg))
				case <-mQuit.ClickedCh:
					return
				}
			}
		}()

		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					if err := openWindow(uiURL(cfg.Port)); err != nil {
						_ = notifier.ShowMsg("打开界面失败：" + err.Error())
					}
				case <-mIP.ClickedCh:
					mIP.SetTitle(ipMenuTitle(cfg))
					showAddresses(cfg)
				case <-mPair.ClickedCh:
					mPair.SetTitle(fmt.Sprintf("配对码：%s", srv.PairCode()))
				case <-mRename.ClickedCh:
					doRename(cfg, srv, ntf)
				case <-mRecords.ClickedCh:
					showRecords(srv)
				case <-mNoWin.ClickedCh:
					cfg.NoWindow = !cfg.NoWindow
					if cfg.NoWindow {
						mNoWin.Uncheck()
					} else {
						mNoWin.Check()
					}
					_ = cfg.Save()
				case <-mReset.ClickedCh:
					code := srv.ResetPairCode()
					mPair.SetTitle(fmt.Sprintf("配对码：%s", code))
					_ = ntf.Show(server.Notification{
						Title:    "配对已重置",
						Text:     "新配对码 " + code,
						AppName:  "LimeLink",
						IconType: "system",
					})
				case <-mAbout.ClickedCh:
					_ = notifier.ShowMsg(fmt.Sprintf(
						"小青柠 LimeLink 电脑端 v%s\n手机通知 → Windows 弹窗同步工具\n本机地址 %s:%d\n数据目录 %s",
						appVersion, netinfo.LocalIPv4(), cfg.Port, config.ConfigDir()))
				case <-mQuit.ClickedCh:
					shutdown(httpSrv, disc)
					systray.Quit()
					return
				}
			}
		}()
	}
}

func onExit() {
	log.Println("[exit] 小青柠已退出")
}

// shutdown 优雅关闭 HTTP 与 UDP 发现服务，并关掉界面窗口。
func shutdown(httpSrv *http.Server, disc *discovery.Service) {
	disc.Stop()
	done := make(chan struct{})
	go func() {
		_ = httpSrv.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		log.Println("[exit] HTTP 服务关闭超时")
	}
	closeWindow()
}

// ipMenuTitle 计算托盘「本机地址」菜单项标题。
func ipMenuTitle(cfg config.Config) string {
	ip := netinfo.LocalIPv4()
	if ip == "" {
		return "本机地址：未找到可用网络"
	}
	return fmt.Sprintf("本机地址：%s:%d", ip, cfg.Port)
}

// showAddresses 点击「本机地址」后，列出所有网卡地址并弹 Toast。
func showAddresses(cfg config.Config) {
	list := netinfo.ListLocalIPv4()
	if len(list) == 0 {
		_ = notifier.ShowMsg("未找到可用局域网地址。请检查网络连接，或开启移动热点后再试。")
		return
	}
	buf := fmt.Sprintf("手机端填以下地址之一（端口 %d）：\n", cfg.Port)
	for _, e := range list {
		buf += fmt.Sprintf("%s（%s）\n", e.IP, e.Iface)
	}
	_ = notifier.Show(server.Notification{
		Title:    "本机局域网地址",
		Text:     buf,
		AppName:  "LimeLink",
		IconType: "system",
	})
}

func showRecords(srv *server.Server) {
	recs := srv.Records()
	if len(recs) == 0 {
		_ = notifier.Show(server.Notification{
			Title:    "最近同步记录",
			Text:     "暂无记录。手机配对并开启通知读取后，这里会显示最近的通知。",
			AppName:  "LimeLink",
			IconType: "system",
		})
		return
	}
	buf := ""
	for i, r := range recs {
		if i >= 10 {
			break
		}
		title := r.Title
		if title == "" {
			title = r.AppName
		}
		buf += fmt.Sprintf("%s [%s] %s\n", r.ReceivedAt.Format("15:04:05"), r.AppName, title)
	}
	_ = notifier.ShowMsg(buf)
}

// doRename 弹输入框修改电脑端设备名，写回配置并同步内存。
func doRename(cfg config.Config, srv *server.Server, ntf *notifier.Notifier) {
	newName, ok, err := dlgs.Entry("设备改名", "请输入电脑端设备名（手机端可见）：", cfg.Name())
	if err != nil || !ok {
		return
	}
	newName = trimSpace(newName)
	if newName == "" {
		_ = notifier.ShowMsg("设备名不能为空，未修改。")
		return
	}
	cfg.DevName = newName
	if err := cfg.Save(); err != nil {
		_ = notifier.ShowMsg("保存设备名失败：" + err.Error())
		return
	}
	srv.SetName(newName)
	_ = ntf.Show(server.Notification{
		Title:    "设备已改名",
		Text:     "新名称：" + newName,
		AppName:  "LimeLink",
		IconType: "system",
	})
}
