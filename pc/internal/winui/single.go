package winui

import "unsafe"

// windowClass 是主窗口的窗口类名，也用于「第二次启动时把已有窗口提到前台」。
const windowClass = "LimeLinkSwissUI"

// BringToFront 查找已经存在的界面窗口并激活它。
// 返回 false 表示没找到（可能被用户关到托盘了，或还没起来）。
func BringToFront() bool {
	h, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(u16(windowClass))), 0)
	if h == 0 {
		return false
	}
	pShowWindow.Call(h, swRestore)
	pSetForegroundWindow.Call(h)
	return true
}
