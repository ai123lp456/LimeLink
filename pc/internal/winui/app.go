package winui

import (
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gen2brain/dlgs"
	"limelink/pc/internal/server"
)

// Controller 是界面需要向宿主（main）请求的动作集合。
type Controller interface {
	Rename(name string) error
	ResetPair() string
	Approve(reqID string) bool
	Deny(reqID string) bool
	Kick(deviceID string) bool
	SendText(deviceID, text string) bool
	ClearRecords()
	DeleteRecord(id string) bool
	Notify(title, text string)
	MarkCloseHinted() bool
	StoreDir() string
	OpenStore() error
	SetTheme(name string) error
	// ToggleMergeRepeat / ToggleMuteRepeat 切换重复通知的两个开关，返回切换后的值（v1.3.2）
	ToggleMergeRepeat() bool
	ToggleMuteRepeat() bool
	// FirewallHelp 返回给用户看的放行命令文本
	FirewallHelp() string
}

// Page 是四个主页面。
type Page int

const (
	PageHome Page = iota
	PageDevices
	PagePair
	PageSettings
)

var pageTitles = [...]string{"通知流", "设备", "配对", "设置"}
var pageSubs = [...]string{
	"手机通知实时镜像到这里",
	"已配对的设备与待确认请求",
	"手机用这 6 位数字即可连接",
	"设备、数据与排查",
}

type hit struct {
	r  rect
	id string
}

// App 是一个窗口实例。
type App struct {
	hwnd    syscall.Handle
	srv     *server.Server
	ctrl    Controller
	version string

	mu       sync.Mutex
	visible  bool
	created  bool
	AutoShow bool

	// 绘制资源（只在尺寸变化时重建）
	surf    *surface
	paint   *painter
	scale   float64
	w, h    int32
	state   server.State
	fp      string
	page    Page
	filter  string
	hover   string
	press   string
	hits    []hit
	scroll  int32
	maxScr  int32
	toasts   []toastItem
	tooltip  string
	theme   theme

	// 键盘焦点（Tab 遍历）与滚动条拖拽
	focusables []hit
	focusIdx   int
	mouseX     int32
	mouseY     int32
	dragging   bool
	dragStartY int32
	dragStartS int32

	// 滚动条几何（拖拽时按比例换算）与可见列表高度
	sbTop, sbH    int32
	workW, workH  int32
	feedContent   int32
	feedH         int32

	// 行级交互：右键菜单需要知道点的是哪条记录
	rowByID map[string]server.Record
	ctxAt   time.Time // 右键菜单刚关闭时忽略随后的鼠标抬起

	// 新通知提示：滚到下面时来了新消息，顶部给个可点的提示
	newCount  int
	lastTopID string

	// 待确认请求的可见性：新请求要主动提醒，消失时区分「拒绝」和「过期」
	knownPending map[string]bool
	pendingReady bool
	userActed    bool
}

var current *App

// New 创建一个界面实例（窗口在 Run 中创建）。
func New(srv *server.Server, ctrl Controller, version, themeName string) *App {
	a := &App{srv: srv, ctrl: ctrl, version: version, scale: 1, theme: themeByName(themeName)}
	current = a
	return a
}

func themeByName(name string) theme {
	if name == "dark" {
		return darkTheme
	}
	return lightTheme
}

// Show 显示窗口（可从任意 goroutine 调用）。
func (a *App) Show() {
	if a.hwnd != 0 {
		pPostMessageW.Call(uintptr(a.hwnd), wmAppShow, 0, 0)
	}
}

// Close 退出界面线程。
func (a *App) Close() {
	if a.hwnd != 0 {
		pPostMessageW.Call(uintptr(a.hwnd), wmAppQuit, 0, 0)
	}
}

// Run 创建窗口并进入消息循环（阻塞，需在新 goroutine 中调用）。
func (a *App) Run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// 优先用 Per-Monitor V2：这样系统不会把窗口位图缩放，文字才是清晰的原始分辨率。
	// 老系统（Win10 1703 之前）退回 System-DPI aware。
	if r, _, _ := pSetDpiAwareContext.Call(^uintptr(3)); r == 0 {
		pSetProcessDPIAware.Call()
	}
	initGdiplus()
	if dc, _, _ := pGetDC.Call(0); dc != 0 {
		gpScreenDC = dc
	}

	// 先取系统 DPI：窗口创建前 GetDpiForWindow 还不可靠，会拿到 96 导致整个界面偏小。
	sysScale := 1.0
	if dc, _, _ := pGetDC.Call(0); dc != 0 {
		if v, _, _ := pGetDeviceCaps.Call(dc, 88); v > 0 { // LOGPIXELSX
			sysScale = float64(v) / 96.0
		}
		pReleaseDC.Call(0, dc)
	}
	a.scale = sysScale

	hInst, _, _ := pGetModuleHandleW.Call(0)
	className := u16("LimeLinkSwissUI")
	cursor, _, _ := pLoadCursorW.Call(0, idcArrow)
	icon, _, _ := pLoadIconW.Call(hInst, 1)
	if icon == 0 {
		icon, _, _ = pLoadIconW.Call(0, idiApplication)
	}
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		Style:         csHRedraw | csVRedraw | csDblClks,
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     syscall.Handle(hInst),
		HIcon:         syscall.Handle(icon),
		HCursor:       syscall.Handle(cursor),
		HbrBackground: 0,
		LpszClassName: className,
		HIconSm:       syscall.Handle(icon),
	}
	if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		log.Printf("[ui] RegisterClassEx 失败: %v\n", err)
		return
	}

	// 默认客户区 1280x920（逻辑像素），但要先裁到「工作区」内：
	// 1366x768 这类小屏上，不裁的话窗口会比屏幕还高，底部被任务栏压住。
	var wa rect
	pSystemParamsInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&wa)), 0)
	if wa.W() <= 0 || wa.H() <= 0 {
		sw, _, _ := pGetSystemMetrics.Call(0)
		sh, _, _ := pGetSystemMetrics.Call(1)
		wa = rect{0, 0, int32(sw), int32(sh)}
	}
	a.workW, a.workH = wa.W(), wa.H()

	cr := rect{0, 0, int32(1280 * sysScale), int32(920 * sysScale)}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&cr)), wsOverlappedWindow, 0, wsExAppWindow)
	ww, wh := cr.W(), cr.H()
	if ww > wa.W() {
		ww = wa.W()
	}
	if wh > wa.H() {
		wh = wa.H()
	}
	x := wa.Left + (wa.W()-ww)/2
	y := wa.Top + (wa.H()-wh)/2
	if x < wa.Left {
		x = wa.Left
	}
	if y < wa.Top {
		y = wa.Top
	}

	hwnd, _, err := pCreateWindowExW.Call(
		wsExAppWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(u16("小青柠 LimeLink "+a.version))),
		wsOverlappedWindow|wsClipChildren,
		uintptr(x), uintptr(y), uintptr(ww), uintptr(wh),
		0, 0, hInst, 0)
	if hwnd == 0 {
		log.Printf("[ui] CreateWindowEx 失败: %v\n", err)
		return
	}
	a.hwnd = syscall.Handle(hwnd)
	a.created = true

	if a.scale < 1 {
		a.scale = 1
	}

	a.refreshState()
	a.visible = a.AutoShow
	if a.AutoShow {
		pShowWindow.Call(hwnd, swShow)
	}

	// 窗口显示后再校正 DPI：创建时 GetDpiForWindow 可能还拿不到显示器真实缩放，
	// 不校正的话整个界面会按 100% 画在 150% 的屏幕上，字和框都会偏小。
	if dpi, _, _ := pGetDpiForWindow.Call(hwnd); dpi > 0 {
		if sc := float64(dpi) / 96.0; sc > 0 && sc != a.scale {
			a.scale = sc
			cr2 := rect{0, 0, int32(1280 * sc), int32(920 * sc)}
			pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&cr2)), wsOverlappedWindow, 0, wsExAppWindow)
			w2, h2 := cr2.W(), cr2.H()
			if a.workW > 0 && w2 > a.workW {
				w2 = a.workW
			}
			if a.workH > 0 && h2 > a.workH {
				h2 = a.workH
			}
			nx := (a.workW - w2) / 2
			ny := (a.workH - h2) / 2
			if nx < 0 {
				nx = 0
			}
			if ny < 0 {
				ny = 0
			}
			pSetWindowPos.Call(hwnd, 0, uintptr(nx), uintptr(ny),
				uintptr(w2), uintptr(h2), swpNoZOrder)
		}
	}
	pInvalidateRect.Call(hwnd, 0, 0)
	pUpdateWindow.Call(hwnd)

	// 700ms 刷新：状态变化才重绘，避免空转
	pSetTimer.Call(hwnd, 1, 700, 0)

	var msg msgStruct
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	a.teardown()
}

func (a *App) teardown() {
	a.surf.free()
	a.surf = nil
	if a.paint != nil {
		a.paint.free()
		a.paint = nil
	}
}

// refreshState 拉一份快照，返回是否有变化。
func (a *App) refreshState() bool {
	st := a.srv.Snapshot(a.version)
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%d|%d|%d|%s|%s|%d|%t|%t", st.PairedCount, st.OnlineCount, len(st.Records),
		len(st.Pending), st.Code, st.Name, st.StartedAt/1000, st.MergeRepeat, st.MuteRepeat)
	// 记录指纹要含 UpdatedAt：实时记录被「原地更新」时 ID 不变，
	// 只看 ID 会导致界面不刷新，计时类通知的数字就永远是第一次的值。
	for _, r := range st.Records {
		fmt.Fprintf(&b, "|%s:%d", r.ID, r.UpdatedAt.UnixMilli())
	}
	for _, d := range st.Devices {
		fmt.Fprintf(&b, "|%s%d", d.ID, b2i(d.Online))
	}
	for _, p := range st.Pending {
		b.WriteString("|")
		b.WriteString(p.ID)
	}
	fp := b.String()
	a.state = st

	// 新通知计数：滚到下面时来了新消息，顶部给一个可点的提示
	if len(st.Records) > 0 {
		topID := st.Records[0].ID
		if a.lastTopID != "" && topID != a.lastTopID {
			if a.scroll > 0 {
				a.newCount++
			} else {
				a.newCount = 0
			}
		}
		a.lastTopID = topID
	}

	// 记录索引：右键菜单与行命中都按 id 查原始记录
	a.rowByID = make(map[string]server.Record, len(st.Records))
	for _, r := range st.Records {
		a.rowByID[r.ID] = r
	}
	a.trackPending(st)

	if fp == a.fp {
		return false
	}
	a.fp = fp
	return true
}

// trackPending 让「手机请求连接」这个动作主动找到用户：
// 新请求 → 系统通知 + 窗口收起时自动唤起；请求消失（非用户操作）→ 过期提示。
func (a *App) trackPending(st server.State) {
	seen := make(map[string]bool, len(st.Pending))
	var added []server.PendingView
	for _, p := range st.Pending {
		seen[p.ID] = true
		if a.pendingReady && !a.knownPending[p.ID] {
			added = append(added, p)
		}
	}

	// 用户没点拒绝却消失了 → 判定过期
	if a.pendingReady && !a.userActed {
		for id := range a.knownPending {
			if !seen[id] {
				go a.ctrl.Notify("连接请求已过期", "手机端没有等到确认，请重新发起，或改用配对码连接")
				break
			}
		}
	}
	a.userActed = false
	a.knownPending = seen

	if len(added) > 0 {
		p := added[0]
		name := p.Name
		if name == "" {
			name = "一台手机"
		}
		go a.ctrl.Notify("手机请求连接", name+"（"+p.IP+"）想连上这台电脑，点「允许连接」即可")
		// 免码配对是产品主路径：窗口收在托盘时不能让请求静默过期，直接唤起
		if !a.visible {
			a.Show()
		}
		a.page = PageDevices
		a.scroll = 0
		a.invalidate()
	}
	a.pendingReady = true
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// onOffWord 把开关状态转成提示语用词（开启 / 关闭）。
func onOffWord(v bool) string {
	if v {
		return "开启"
	}
	return "关闭"
}

// onOffMark 把开关状态转成设置页右栏的小字（开 / 关）。
func onOffMark(v bool) string {
	if v {
		return "开"
	}
	return "关"
}

// toggleAction 返回开关按钮上的动作词。
func toggleAction(v bool) string {
	if v {
		return "关闭"
	}
	return "打开"
}

func (a *App) invalidate() {
	if a.hwnd != 0 {
		pInvalidateRect.Call(uintptr(a.hwnd), 0, 0)
	}
}

// ---------------- 窗口过程 ----------------

func wndProc(hwnd, msg, wparam, lparam uintptr) (ret uintptr) {
	// 界面线程兜底：绘制/交互 panic 不应把整个程序带走
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ui] wndProc panic msg=%d: %v\n%s\n", msg, r, debug.Stack())
			ret = 0
		}
	}()

	a := current
	if a == nil {
		r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	}
	switch msg {
	case wmEraseBkgnd:
		return 1

	case wmPaint:
		var ps paintStruct
		hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		a.paintAll(syscall.Handle(hdc))
		pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case wmSize:
		var rc rect
		pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		// 最小尺寸保护：太窄会把排版挤坏，直接回弹到 1080x720 逻辑像素
		minW := int32(1080 * a.scale)
		minH := int32(720 * a.scale)
		// 小屏上别把最小尺寸设得比屏幕还大，否则窗口永远放不下
		if a.workW > 0 && minW > a.workW {
			minW = a.workW
		}
		if a.workH > 0 && minH > a.workH {
			minH = a.workH
		}
		if rc.W() < minW || rc.H() < minH {
			if rc.W() < minW {
				rc.Right = rc.Left + minW
			}
			if rc.H() < minH {
				rc.Bottom = rc.Top + minH
			}
			cr := rect{0, 0, rc.W(), rc.H()}
			pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&cr)), wsOverlappedWindow, 0, wsExAppWindow)
			pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(cr.W()), uintptr(cr.H()), swpNoZOrder|swpNoMove)
			return 0
		}
		if rc.W() != a.w || rc.H() != a.h {
			a.w, a.h = rc.W(), rc.H()
			a.surf.free()
			a.surf = nil
			if a.paint != nil {
				a.paint.free()
				a.paint = nil
			}
			a.invalidate()
		}
		return 0

	case wmMouseMove:
		x, y := a.mouseXY(lparam)
		a.mouseX, a.mouseY = x, y
		if a.dragging {
			a.dragScrollTo(y)
			return 0
		}
		id := a.hitAt(x, y)
		if id != a.hover {
			a.hover = id
			a.invalidate()
		}
		tme := trackMouseEvent{CbSize: uint32(unsafe.Sizeof(trackMouseEvent{})), DwFlags: tmeLeave, HwndTrack: syscall.Handle(hwnd)}
		pTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
		return 0

	case wmMouseLeave:
		if a.hover != "" {
			a.hover = ""
			a.invalidate()
		}
		return 0

	case wmLButtonDown:
		x, y := a.mouseXY(lparam)
		a.mouseX, a.mouseY = x, y
		id := a.hitAt(x, y)
		if id == idScrollbar {
			// 按住滚动条：捕获鼠标并直接跳到位，之后跟随拖动
			a.dragging = true
			pSetCapture.Call(hwnd)
			a.dragScrollTo(y)
			return 0
		}
		a.press = id
		a.invalidate()
		return 0

	case wmLButtonUp:
		if a.dragging {
			a.dragging = false
			pReleaseCapture.Call()
			a.invalidate()
			return 0
		}
		x, y := a.mouseXY(lparam)
		a.mouseX, a.mouseY = x, y
		id := a.hitAt(x, y)
		press := a.press
		a.press = ""
		a.invalidate()
		if id != "" && id == press {
			a.onClick(id)
		}
		return 0

	case wmRButtonUp:
		x, y := a.mouseXY(lparam)
		a.mouseX, a.mouseY = x, y
		id := a.hitAt(x, y)
		if strings.HasPrefix(id, "row:") {
			if rec, ok := a.rowByID[strings.TrimPrefix(id, "row:")]; ok {
				a.showRowMenu(hwnd, rec)
			}
		}
		return 0

	case wmMouseWheel:
		delta := hiWord(wparam)
		a.scroll -= int32(delta) / 120 * int32(48*a.scale)
		a.clampScroll()
		a.invalidate()
		return 0

	case wmKeyDown:
		switch wparam {
		case '1', '2', '3', '4':
			a.page = Page(wparam - '1')
			a.scroll = 0
			a.focusIdx = -1
			a.invalidate()
		case vkTab:
			shift, _, _ := pGetKeyState.Call(vkShift)
			a.moveFocus(int16(shift) < 0)
			a.invalidate()
		case vkEnter, vkSpace:
			if a.activateFocused() {
				a.invalidate()
			}
		case vkUp:
			a.scrollBy(-int32(44 * a.scale))
		case vkDown:
			a.scrollBy(int32(44 * a.scale))
		case vkPrior:
			a.scrollBy(-a.feedH + int32(60*a.scale))
		case vkNext:
			a.scrollBy(a.feedH - int32(60*a.scale))
		case vkHome:
			a.scroll = 0
			a.clampScroll()
			a.invalidate()
		case vkEnd:
			a.scroll = a.maxScr
			a.clampScroll()
			a.invalidate()
		case vkEsc:
			pShowWindow.Call(hwnd, swHide)
			a.visible = false
		}
		return 0

	case wmDpiChanged:
		newDpi := int32((wparam >> 16) & 0xffff)
		if newDpi > 0 {
			a.scale = float64(newDpi) / 96.0
		}
		// 不读 lParam 的建议矩形（避免 uintptr→unsafe.Pointer），自己按逻辑尺寸换算
		if a.scale > 0 {
			cr := rect{0, 0, int32(1280 * a.scale), int32(920 * a.scale)}
			pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&cr)), wsOverlappedWindow, 0, wsExAppWindow)
			pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(cr.W()), uintptr(cr.H()), swpNoZOrder|swpNoMove)
		}
		a.surf.free()
		a.surf = nil
		if a.paint != nil {
			a.paint.free()
			a.paint = nil
		}
		a.invalidate()
		return 0

	case wmSetCursor:
		if a.hover != "" {
			c, _, _ := pLoadCursorW.Call(0, idcHand)
			pSetCursor.Call(c)
			return 1
		}
		return 0

	case wmTimer:
		changed := a.refreshState()
		if a.expireToasts() {
			changed = true
		}
		if changed {
			a.invalidate()
		}
		return 0

	case wmAppShow:
		pShowWindow.Call(hwnd, swShow)
		pSetForegroundWindow.Call(hwnd)
		a.visible = true
		a.invalidate()
		return 0

	case wmAppQuit:
		pKillTimer.Call(hwnd, 1)
		pDestroyWindow.Call(hwnd)
		pPostQuitMessage.Call(0)
		return 0

	case wmClose:
		// 关窗只是隐藏，程序继续在托盘常驻；首次关闭时说明一次，避免以为退出了
		pShowWindow.Call(hwnd, swHide)
		a.visible = false
		if a.ctrl.MarkCloseHinted() {
			go a.ctrl.Notify("小青柠仍在托盘运行", "关闭窗口不会退出程序；右键托盘图标可以再打开，或在托盘菜单里选「退出」。")
		}
		return 0

	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// mouseXY 取鼠标位置（客户区物理像素）。
//
// 注意：命中框（a.hits）全部是物理像素，所以这里**不能再除以缩放系数**。
// 1.3.1 早期版本把鼠标坐标除成了逻辑像素，150% 缩放下命中区整体偏下偏右，
// 表现就是「鼠标要放到选项下面几行才点得到」。
func (a *App) mouseXY(lparam uintptr) (int32, int32) {
	x := int32(int16(lparam & 0xffff))
	y := int32(int16((lparam >> 16) & 0xffff))
	return x, y
}

func (a *App) hitAt(x, y int32) string {
	for i := len(a.hits) - 1; i >= 0; i-- {
		r := a.hits[i].r
		if x >= r.Left && x < r.Right && y >= r.Top && y < r.Bottom {
			return a.hits[i].id
		}
	}
	return ""
}

// ---------------- 滚动 / 焦点 / 右键菜单 ----------------

const (
	idScrollbar = "scrollbar"
	rowPrefix   = "row:"
)

// dragScrollTo 把滚动条拖到 y 位置对应的比例。
func (a *App) dragScrollTo(y int32) {
	if a.sbH <= 0 || a.feedContent <= a.feedH {
		return
	}
	rel := float64(y-a.sbTop) / float64(a.sbH)
	if rel < 0 {
		rel = 0
	}
	if rel > 1 {
		rel = 1
	}
	a.scroll = int32(rel * float64(a.feedContent-a.feedH))
	a.clampScroll()
	a.invalidate()
}

func (a *App) scrollBy(d int32) {
	a.scroll += d
	a.clampScroll()
	a.invalidate()
}

// moveFocus 在可聚焦控件之间移动（Tab / Shift+Tab）。
func (a *App) moveFocus(back bool) {
	n := len(a.focusables)
	if n == 0 {
		a.focusIdx = -1
		return
	}
	if a.focusIdx < 0 || a.focusIdx >= n {
		if back {
			a.focusIdx = n - 1
		} else {
			a.focusIdx = 0
		}
		return
	}
	if back {
		a.focusIdx = (a.focusIdx - 1 + n) % n
	} else {
		a.focusIdx = (a.focusIdx + 1) % n
	}
}

// activateFocused 用 Enter / Space 触发当前焦点控件。
func (a *App) activateFocused() bool {
	if a.focusIdx < 0 || a.focusIdx >= len(a.focusables) {
		return false
	}
	id := a.focusables[a.focusIdx].id
	if id == "" {
		return false
	}
	a.onClick(id)
	return true
}

// showRowMenu 弹系统原生右键菜单：复制正文 / 复制标题 / 删除这条。
func (a *App) showRowMenu(hwnd uintptr, rec server.Record) {
	h, _, _ := pCreatePopupMenu.Call()
	if h == 0 {
		return
	}
	defer pDestroyMenu.Call(h)
	pAppendMenuW.Call(h, mfString, 1, uintptr(unsafe.Pointer(u16("复制正文"))))
	pAppendMenuW.Call(h, mfString, 2, uintptr(unsafe.Pointer(u16("复制标题"))))
	pAppendMenuW.Call(h, mfString, 3, uintptr(unsafe.Pointer(u16("删除这条"))))

	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(h, tpmReturnCmd|tpmRightButton,
		uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)

	switch int32(cmd) {
	case 1:
		body := rec.Text
		if body == "" {
			body = rec.Title
		}
		if setClipboard(body) {
			a.showToast("已复制正文")
		}
	case 2:
		if setClipboard(rec.Title) {
			a.showToast("已复制标题")
		}
	case 3:
		if a.ctrl.DeleteRecord(rec.ID) {
			a.showToast("已删除这条")
		}
	}
	a.refreshState()
	a.invalidate()
}

func (a *App) clampScroll() {
	if a.maxScr < 0 {
		a.maxScr = 0
	}
	if a.scroll < 0 {
		a.scroll = 0
	}
	if a.scroll > a.maxScr {
		a.scroll = a.maxScr
	}
}

// ---------------- 交互 ----------------

func (a *App) onClick(id string) {
	parts := strings.SplitN(id, ":", 3)
	defer func() { a.refreshState(); a.invalidate() }()

	switch parts[0] {
	case "nav":
		if len(parts) > 1 {
			a.page = Page(int(parts[1][0] - '0'))
			a.scroll = 0
		}
	case "filter":
		a.filter = ""
		if len(parts) > 1 {
			a.filter = parts[1]
		}
		a.scroll = 0
	case "allow":
		a.userActed = true
		if a.ctrl.Approve(parts[1]) {
			a.showToast("已允许该设备连接")
		}
	case "deny":
		a.userActed = true
		a.ctrl.Deny(parts[1])
		a.showToast("已拒绝该请求")
	case "kick":
		if ok, err := dlgs.Question("解绑设备", "这台手机需要重新配对才能同步，确定解绑？", false); err == nil && ok {
			a.ctrl.Kick(parts[1])
			a.showToast("已解绑")
		}
	case "send":
		text, ok, err := dlgs.Entry("文字快送", "要发到手机的文字（手机会弹出通知）：", "")
		if err == nil && ok && strings.TrimSpace(text) != "" {
			if a.ctrl.SendText(parts[1], text) {
				a.showToast("已排队，手机 5 秒内收到")
			}
		}
	case "sendtext":
		var target string
		for _, d := range a.state.Devices {
			if d.Online {
				target = d.ID
				break
			}
		}
		if target == "" && len(a.state.Devices) > 0 {
			target = a.state.Devices[0].ID
		}
		if target == "" {
			dlgs.Error("没有设备", "先在设备页允许一台手机连接。")
			return
		}
		text, ok, err := dlgs.Entry("文字快送", "要发到手机的文字（手机会弹出通知）：", "")
		if err == nil && ok && strings.TrimSpace(text) != "" {
			if a.ctrl.SendText(target, text) {
				a.showToast("已排队，手机 5 秒内收到")
			}
		}
	case "theme":
		if len(parts) > 1 && parts[1] != "" {
			name := parts[1]
			a.theme = themeByName(name)
			_ = a.ctrl.SetTheme(name)
			a.showToast("主题已切换为" + map[string]string{"dark": "深色", "light": "浅色"}[name])
		}
	case "togglemerge":
		a.showToast("合并重复通知已" + onOffWord(a.ctrl.ToggleMergeRepeat()))
	case "togglemute":
		a.showToast("重复通知静音已" + onOffWord(a.ctrl.ToggleMuteRepeat()))
	case "newpill":
		a.scroll = 0
		a.newCount = 0
		a.clampScroll()
	case "copystore":
		if setClipboard(a.ctrl.StoreDir()) {
			a.showToast("数据目录已复制：" + a.ctrl.StoreDir())
		}
	case "copycode":
		if setClipboard(a.state.Code) {
			a.showToast("配对码已复制")
		}
	case "reset":
		if ok, err := dlgs.Question("重置配对", "会生成新的配对码，并解绑所有已配对手机。确定？", false); err == nil && ok {
			a.ctrl.ResetPair()
			a.showToast("已重置配对码")
		}
	case "rename":
		name, ok, err := dlgs.Entry("设备改名", "电脑端名称（手机端可见，最多 32 字）：", a.state.Name)
		if err == nil && ok && strings.TrimSpace(name) != "" {
			if err := a.ctrl.Rename(name); err != nil {
				dlgs.Error("保存失败", err.Error())
			} else {
				a.showToast("已改名为「" + name + "」")
			}
		}
	case "clear":
		if ok, err := dlgs.Question("清空通知记录", "只删除本机记录，不影响同步。确定？", false); err == nil && ok {
			a.ctrl.ClearRecords()
			a.showToast("记录已清空")
		}
	case "store":
		if err := a.ctrl.OpenStore(); err != nil {
			dlgs.Error("打开失败", err.Error())
		}
	case "firewall":
		text := a.ctrl.FirewallHelp()
		if ok, err := dlgs.Question("放行防火墙", "需要放行 TCP 8787 与 UDP 8788。\n要把命令复制到剪贴板吗？", true); err == nil && ok {
			setClipboard(text)
			a.showToast("命令已复制，请在管理员终端中粘贴执行")
		}
	case "diag":
		setClipboard(a.diagText())
		a.showToast("诊断信息已复制")
	case "copyip":
		if len(parts) > 2 {
			if setClipboard(parts[1] + ":" + parts[2]) {
				a.showToast("地址已复制")
			}
		}
	case "about":
		dlgs.Info("关于", fmt.Sprintf("小青柠 LimeLink %s\n手机通知 → Windows 桌面镜像\n本机端口 %d / 发现端口 8788\n数据目录 %s\n\nSwiss 排版 · 原生 Win32 渲染，无浏览器内核",
			a.version, a.state.Port, a.ctrl.StoreDir()))
	}
}

// toastItem 是一条界面内提示（右下角堆叠，最多 3 条）。
type toastItem struct {
	text string
	at   time.Time
}

func (a *App) showToast(text string) {
	a.toasts = append(a.toasts, toastItem{text: text, at: time.Now()})
	if len(a.toasts) > 3 {
		a.toasts = a.toasts[len(a.toasts)-3:]
	}
	a.invalidate()
}

// expireToasts 清掉超过 2.6 秒的提示，返回是否有变化。
func (a *App) expireToasts() bool {
	if len(a.toasts) == 0 {
		return false
	}
	out := a.toasts[:0]
	for _, t := range a.toasts {
		if time.Since(t.at) < 2600*time.Millisecond {
			out = append(out, t)
		}
	}
	changed := len(out) != len(a.toasts)
	a.toasts = out
	return changed
}

func (a *App) diagText() string {
	st := a.state
	var b strings.Builder
	fmt.Fprintf(&b, "version: %s\nname: %s\nport: %d\npaired: %d online: %d\nuptime_sec: %d\nnotify: %d rejects: %d\nstore: %s\n",
		st.Version, st.Name, st.Port, st.PairedCount, st.OnlineCount, st.UptimeSec, st.NotifyCount, st.NotifyRejects, st.StoreDir)
	for _, d := range st.Devices {
		fmt.Fprintf(&b, "device: %s ip=%s online=%v last=%s\n", d.Name, d.IP, d.Online, d.LastSeenText)
	}
	for _, ip := range st.IPs {
		fmt.Fprintf(&b, "addr: %s (%s)\n", ip.IP, ip.Iface)
	}
	return b.String()
}
