// Package winui 用 Win32 + GDI+ 自绘 LimeLink 电脑端界面。
//
// 为什么不用浏览器：用户的诉求是「原生渲染、不臃肿、不依赖 Edge」。
// 这里只用 Windows 自带组件（user32 / gdi32 / gdiplus），不引入 WebView2、CGO 或第三方 UI 框架，
// 产物仍是单个 exe，界面像素完全由代码控制（瑞士极简风格）。
package winui

import (
	"syscall"
)

// ---------------- DLL / 过程地址 ----------------

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modGdi32    = syscall.NewLazyDLL("gdi32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modGdiplus  = syscall.NewLazyDLL("gdiplus.dll")
	modShell32  = syscall.NewLazyDLL("shell32.dll")
	modDwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	pRegisterClassExW     = modUser32.NewProc("RegisterClassExW")
	pCreateWindowExW      = modUser32.NewProc("CreateWindowExW")
	pDefWindowProcW       = modUser32.NewProc("DefWindowProcW")
	pDestroyWindow        = modUser32.NewProc("DestroyWindow")
	pGetMessageW          = modUser32.NewProc("GetMessageW")
	pTranslateMessage     = modUser32.NewProc("TranslateMessage")
	pDispatchMessageW     = modUser32.NewProc("DispatchMessageW")
	pPostMessageW         = modUser32.NewProc("PostMessageW")
	pPostQuitMessage      = modUser32.NewProc("PostQuitMessage")
	pSendMessageW         = modUser32.NewProc("SendMessageW")
	pBeginPaint           = modUser32.NewProc("BeginPaint")
	pEndPaint             = modUser32.NewProc("EndPaint")
	pInvalidateRect       = modUser32.NewProc("InvalidateRect")
	pUpdateWindow         = modUser32.NewProc("UpdateWindow")
	pShowWindow           = modUser32.NewProc("ShowWindow")
	pIsWindowVisible      = modUser32.NewProc("IsWindowVisible")
	pSetForegroundWindow  = modUser32.NewProc("SetForegroundWindow")
	pSetWindowPos         = modUser32.NewProc("SetWindowPos")
	pGetClientRect        = modUser32.NewProc("GetClientRect")
	pGetWindowRect        = modUser32.NewProc("GetWindowRect")
	pAdjustWindowRectEx   = modUser32.NewProc("AdjustWindowRectEx")
	pLoadCursorW          = modUser32.NewProc("LoadCursorW")
	pSetCursor            = modUser32.NewProc("SetCursor")
	pTrackMouseEvent      = modUser32.NewProc("TrackMouseEvent")
	pGetSystemMetrics     = modUser32.NewProc("GetSystemMetrics")
	pOpenClipboard        = modUser32.NewProc("OpenClipboard")
	pCloseClipboard       = modUser32.NewProc("CloseClipboard")
	pEmptyClipboard       = modUser32.NewProc("EmptyClipboard")
	pSetClipboardData     = modUser32.NewProc("SetClipboardData")
	pIsClipboardFormatAv  = modUser32.NewProc("IsClipboardFormatAvailable")
	pGetClipboardData     = modUser32.NewProc("GetClipboardData")
	pSetProcessDPIAware   = modUser32.NewProc("SetProcessDPIAware")
	pSetDpiAwareContext   = modUser32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForWindow      = modUser32.NewProc("GetDpiForWindow")
	pGetDC                = modUser32.NewProc("GetDC")
	pReleaseDC            = modUser32.NewProc("ReleaseDC")
	pLoadIconW            = modUser32.NewProc("LoadIconW")
	pScreenToClient       = modUser32.NewProc("ScreenToClient")
	pClientToScreen       = modUser32.NewProc("ClientToScreen")
	pSetTimer             = modUser32.NewProc("SetTimer")
	pKillTimer            = modUser32.NewProc("KillTimer")
	pMessageBoxW          = modUser32.NewProc("MessageBoxW")
	pCreateSolidBrush     = modGdi32.NewProc("CreateSolidBrush")
	pFindWindowW          = modUser32.NewProc("FindWindowW")
	pDeleteObject         = modGdi32.NewProc("DeleteObject")
	pCreateCompatibleDC   = modGdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection     = modGdi32.NewProc("CreateDIBSection")
	pSelectObject         = modGdi32.NewProc("SelectObject")
	pDeleteDC             = modGdi32.NewProc("DeleteDC")
	pBitBlt               = modGdi32.NewProc("BitBlt")
	pCreatePen            = modGdi32.NewProc("CreatePen")
	pSetTextColor         = modGdi32.NewProc("SetTextColor")
	pSetBkMode            = modGdi32.NewProc("SetBkMode")
	pGetStockObject       = modGdi32.NewProc("GetStockObject")
	pGetModuleHandleW     = modKernel32.NewProc("GetModuleHandleW")
	pGetDeviceCaps        = modGdi32.NewProc("GetDeviceCaps")
	pSystemParamsInfo     = modUser32.NewProc("SystemParametersInfoW")
	pCreatePopupMenu      = modUser32.NewProc("CreatePopupMenu")
	pAppendMenuW          = modUser32.NewProc("AppendMenuW")
	pTrackPopupMenu       = modUser32.NewProc("TrackPopupMenu")
	pDestroyMenu          = modUser32.NewProc("DestroyMenu")
	pGetCursorPos         = modUser32.NewProc("GetCursorPos")
	pSetCapture           = modUser32.NewProc("SetCapture")
	pReleaseCapture       = modUser32.NewProc("ReleaseCapture")
	pGetKeyState          = modUser32.NewProc("GetKeyState")
	pGlobalAlloc          = modKernel32.NewProc("GlobalAlloc")
	pGlobalLock           = modKernel32.NewProc("GlobalLock")
	pGlobalUnlock         = modKernel32.NewProc("GlobalUnlock")
	pGlobalFree           = modKernel32.NewProc("GlobalFree")
	pRtlMoveMemory        = modKernel32.NewProc("RtlMoveMemory")
	pShellExecuteW        = modShell32.NewProc("ShellExecuteW")
	pDwmSetWindowAttr     = modDwmapi.NewProc("DwmSetWindowAttribute")
	pGdiplusStartup       = modGdiplus.NewProc("GdiplusStartup")
	pGdiplusShutdown      = modGdiplus.NewProc("GdiplusShutdown")
	pGdipCreateFromHDC    = modGdiplus.NewProc("GdipCreateFromHDC")
	pGdipDeleteGraphics   = modGdiplus.NewProc("GdipDeleteGraphics")
	pGdipSetSmoothingMode = modGdiplus.NewProc("GdipSetSmoothingMode")
	pGdipSetTextRendering = modGdiplus.NewProc("GdipSetTextRenderingHint")
	pGdipSetPageUnit      = modGdiplus.NewProc("GdipSetPageUnit")
	pGdipCreateSolidFill  = modGdiplus.NewProc("GdipCreateSolidFill")
	pGdipDeleteBrush      = modGdiplus.NewProc("GdipDeleteBrush")
	pGdipFillRectI        = modGdiplus.NewProc("GdipFillRectangleI")
	pGdipCreatePen1       = modGdiplus.NewProc("GdipCreatePen1")
	pGdipDeletePen        = modGdiplus.NewProc("GdipDeletePen")
	pGdipDrawLineI        = modGdiplus.NewProc("GdipDrawLineI")
	pGdipCreateFontFamily = modGdiplus.NewProc("GdipCreateFontFamilyFromName")
	pGdipDeleteFontFamily = modGdiplus.NewProc("GdipDeleteFontFamily")
	pGdipCreateFontFromLogfont = modGdiplus.NewProc("GdipCreateFontFromLogfontW")
	pGdipDeleteFont       = modGdiplus.NewProc("GdipDeleteFont")
	pGdipGetFontSize      = modGdiplus.NewProc("GdipGetFontSize")
	pGdipGetFontHeight    = modGdiplus.NewProc("GdipGetFontHeight")
	pGdipCreateStringFmt  = modGdiplus.NewProc("GdipCreateStringFormat")
	pGdipDeleteStringFmt  = modGdiplus.NewProc("GdipDeleteStringFormat")
	pGdipSetFmtAlign      = modGdiplus.NewProc("GdipSetStringFormatAlign")
	pGdipSetFmtLineAlign  = modGdiplus.NewProc("GdipSetStringFormatLineAlign")
	pGdipSetFmtTrimming   = modGdiplus.NewProc("GdipSetStringFormatTrimming")
	pGdipSetFmtFlags      = modGdiplus.NewProc("GdipSetStringFormatFlags")
	pGdipDrawString       = modGdiplus.NewProc("GdipDrawString")
	pGdipMeasureString    = modGdiplus.NewProc("GdipMeasureString")
	pGdipSetClipRectI     = modGdiplus.NewProc("GdipSetClipRectI")
	pGdipResetClip        = modGdiplus.NewProc("GdipResetClip")
	pGdipGraphicsClear    = modGdiplus.NewProc("GdipGraphicsClear")
)

// ---------------- 常量 ----------------

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmPaint       = 0x000F
	wmEraseBkgnd  = 0x0014
	wmSize        = 0x0005
	wmMouseMove   = 0x0200
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmMouseLeave  = 0x02A3
	wmMouseWheel  = 0x020A
	wmRButtonUp   = 0x0205
	wmKeyDown     = 0x0100
	wmSetCursor   = 0x0020
	wmTimer       = 0x0113
	wmDpiChanged  = 0x02E0
	wmNCHitTest   = 0x0084
	wmAppShow     = 0x8001 // WM_APP + 1
	wmAppQuit     = 0x8002
	wmAppTick     = 0x8003

	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wsClipChildren     = 0x02000000
	wsExAppWindow      = 0x00040000

	swShow     = 5
	swRestore  = 9
	swHide     = 0
	swpNoZOrder = 0x0004
	swpNoSize   = 0x0001
	swpNoMove   = 0x0002

	csHRedraw = 0x0002
	csVRedraw = 0x0001
	csDblClks = 0x0008

	idiApplication = 32512
	idcArrow       = 32512
	idcHand        = 32649

	tmeLeave = 0x0002

	srcCopy = 0x00CC0020

	mbOK       = 0x00000000
	mbIconInfo = 0x00000040

	gmCompat = 0x0040

	dtmSetText = 0x0001

	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// GDI+ 枚举值
const (
	gpSmoothingAntiAlias = 4
	gpTextClearType      = 4
	gpUnitPixel          = 2
	gpFontStyleRegular   = 0
	gpFontStyleBold      = 1
	gpAlignNear          = 0
	gpAlignCenter        = 1
	gpAlignFar           = 2
	gpTrimNone           = 0
	gpTrimEllipsisChar   = 3
	gpFlagNoWrap         = 0x1000
	gpCombineIntersect   = 1
)

// ---------------- 结构体 ----------------

type point struct{ X, Y int32 }

type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) W() int32 { return r.Right - r.Left }
func (r rect) H() int32 { return r.Bottom - r.Top }

type rectF struct{ X, Y, W, H float32 }

type msgStruct struct {
	Hwnd     syscall.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type paintStruct struct {
	Hdc         syscall.Handle
	FErase      int32
	RcPaint     rect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type trackMouseEvent struct {
	CbSize      uint32
	DwFlags     uint32
	HwndTrack   syscall.Handle
	DwHoverTime uint32
}

type gdiplusStartupInput struct {
	GdiplusVersion           uint32
	DebugEventCallback       uintptr
	SuppressBackgroundThread int32
	SuppressExternalCodecs   int32
}

// ---------------- 小工具 ----------------

func u16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}

func loWord(v uintptr) int32 { return int32(int16(v & 0xffff)) }
func hiWord(v uintptr) int32 { return int32(int16((v >> 16) & 0xffff)) }

// ---------------- 键盘 / 菜单常量 ----------------

const (
	vkTab   = 0x09
	vkEnter = 0x0D
	vkShift = 0x10
	vkEsc   = 0x1B
	vkSpace = 0x20
	vkEnd   = 0x23
	vkHome  = 0x24
	vkLeft  = 0x25
	vkUp    = 0x26
	vkRight = 0x27
	vkDown  = 0x28
	vkPrior = 0x21 // PageUp
	vkNext  = 0x22 // PageDown

	mfString = 0x0000
	tpmLeftButton  = 0x0000
	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100

	spiGetWorkArea = 0x0030
)