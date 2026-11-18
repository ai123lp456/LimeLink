// Command limehost 是 LimeLink 电脑端常驻程序（v1.3.1）。
//
// 启动后做四件事：
//  1. 起 HTTP 服务（默认 8787），接收手机推送的通知；
//  2. 起 UDP 发现服务（8788），定时广播自己 + 应答手机探测；
//  3. 托盘图标常驻（打开界面 / 地址 / 配对码 / 改名 / 记录 / 退出）；
//  4. 打开原生界面窗口（Win32 + GDI+ 自绘，瑞士极简排版，无浏览器内核）。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gen2brain/dlgs"
	"github.com/getlantern/systray"
	"limelink/pc/internal/config"
	"limelink/pc/internal/discovery"
	"limelink/pc/internal/netinfo"
	"limelink/pc/internal/notifier"
	"limelink/pc/internal/server"
	"limelink/pc/internal/winui"
)

// appID 是 Toast 的 AppUserModelID（含版本，便于系统通知设置里区分）。
const appID = "LimeLink.Notification.1.3.2"

// appVersion 是电脑端版本号（同步体现在 exe 资源、界面、README）。
const appVersion = "1.3.2"

func main() {
	setupLog()

	cfg := config.Load()

	// 已有实例在跑：把它已经开着的窗口提到前台，然后退出，不再抢端口
	if alreadyRunning(cfg.Port) {
		if !winui.BringToFront() {
			log.Println("[boot] 已有实例在运行，但没找到它的窗口（可能被关闭到托盘）")
		}
		return
	}

	srv := server.NewWithName(newPairCode6(), cfg.Name(), config.ConfigDir())
	srv.SetPort(cfg.Port)
	// v1.3.2：把「合并重复通知 / 重复通知静音」两个开关落到服务里（缺省都开）
	srv.SetRepeatOptions(cfg.MergeRepeatEnabled(), cfg.MuteRepeatEnabled())
	ntf := notifier.New(appID)
	httpSrv := srv.NewHTTPServer(ntf, nil)

	ui := winui.New(srv, &controller{cfg: &cfg, srv: srv}, appVersion, cfg.ThemeName())
	ui.AutoShow = !cfg.NoWindow
	go ui.Run()
	// 给窗口线程一点时间建窗，避免托盘首批点击时 hwnd 还是 0
	time.Sleep(120 * time.Millisecond)

	ln, port, err := listenWithFallback(cfg.Port)
	if err != nil {
		log.Printf("[fatal] 端口 %d~%d 均被占用: %v\n", cfg.Port, cfg.Port+4, err)
		_, _ = dlgs.Error("小青柠 LimeLink",
			fmt.Sprintf("端口 %d~%d 都被占用，无法启动。\n请关闭占用端口的程序后重试。", cfg.Port, cfg.Port+4))
		return
	}
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

	systray.Run(onReady(srv, ntf, cfg, httpSrv, disc, ui), onExit)
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
		Ok bool `json:"ok"`
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
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
		if err == nil {
			return ln, p, nil
		}
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
		_ = os.Remove(p)
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

// controller 把界面动作落到真实的配置与服务上。
type controller struct {
	cfg *config.Config
	srv *server.Server
}

func (c *controller) Rename(name string) error {
	c.cfg.DevName = name
	if err := c.cfg.Save(); err != nil {
		return err
	}
	c.srv.SetName(name)
	return nil
}

func (c *controller) ResetPair() string   { return c.srv.ResetPairCode() }
func (c *controller) Approve(id string) bool { return c.srv.ApprovePairRequest(id) != nil }
func (c *controller) Deny(id string) bool    { return c.srv.DenyPairRequest(id) }
func (c *controller) Kick(id string) bool    { return c.srv.KickDevice(id) }
func (c *controller) SendText(id, text string) bool { return c.srv.QueueText(id, text) }
func (c *controller) ClearRecords()              { c.srv.ClearRecords() }
func (c *controller) DeleteRecord(id string) bool { return c.srv.DeleteRecord(id) }

// Notify 发一条系统通知（配对请求、过期提醒等），不阻塞界面线程。
func (c *controller) Notify(title, text string) {
	_ = notifier.Show(server.Notification{Title: title, Text: text, AppName: "LimeLink", IconType: "system"})
}
func (c *controller) StoreDir() string       { return config.ConfigDir() }

// MarkCloseHinted 首次关闭窗口时给一次说明，之后不再打扰。
func (c *controller) MarkCloseHinted() bool {
	if c.cfg.CloseHinted {
		return false
	}
	c.cfg.CloseHinted = true
	_ = c.cfg.Save()
	return true
}

func (c *controller) SetTheme(name string) error {
	if name != "dark" {
		name = "light"
	}
	c.cfg.Theme = name
	return c.cfg.Save()
}

// ToggleMergeRepeat 切换「合并重复通知」，返回切换后的值（v1.3.2）。
func (c *controller) ToggleMergeRepeat() bool {
	c.cfg.SetMergeRepeat(!c.cfg.MergeRepeatEnabled())
	_ = c.cfg.Save()
	c.srv.SetRepeatOptions(c.cfg.MergeRepeatEnabled(), c.cfg.MuteRepeatEnabled())
	return c.cfg.MergeRepeatEnabled()
}

// ToggleMuteRepeat 切换「重复通知静音」，返回切换后的值（v1.3.2）。
func (c *controller) ToggleMuteRepeat() bool {
	c.cfg.SetMuteRepeat(!c.cfg.MuteRepeatEnabled())
	_ = c.cfg.Save()
	c.srv.SetRepeatOptions(c.cfg.MergeRepeatEnabled(), c.cfg.MuteRepeatEnabled())
	return c.cfg.MuteRepeatEnabled()
}

func (c *controller) OpenStore() error {
	dir := config.ConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return exec.Command("explorer", dir).Start()
}

func (c *controller) FirewallHelp() string {
	return `netsh advfirewall firewall add rule name="LimeLink TCP 8787" dir=in action=allow protocol=TCP localport=8787
netsh advfirewall firewall add rule name="LimeLink UDP 8788" dir=in action=allow protocol=UDP localport=8788`
}

func onReady(srv *server.Server, ntf *notifier.Notifier, cfg config.Config, httpSrv *http.Server, disc *discovery.Service, ui *winui.App) func() {
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
					ui.Show()
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
					ui.Close()
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

// shutdown 优雅关闭 HTTP 与 UDP 发现服务。
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
