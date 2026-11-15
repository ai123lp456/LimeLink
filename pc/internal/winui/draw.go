package winui

import (
	"fmt"
	"log"
	"math"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

var (
	dbgOnce sync.Once
	dbgFile *os.File
)

// dbgText 在设置了 LIME_TEXTLOG 时把每次文本绘制的坐标与实测宽度写进文件，
// 用于自动检测「文字互相压住」的排版问题（交付版本不受影响）。
func dbgText(kind string, x, y, w, h, measured, inkH float32, s string) {
	dbgOnce.Do(func() {
		p := os.Getenv("LIME_TEXTLOG")
		if p == "" {
			return
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			dbgFile = f
		}
	})
	if dbgFile == nil {
		return
	}
	fmt.Fprintf(dbgFile, "%s\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t%s\n", kind, x, y, w, h, measured, inkH, s)
}

// ---------------- 瑞士极简调色板 ----------------
//
// 只用「纸 / 墨 / 灰」三阶 + 一个品牌强调色（青柠）。
// 无圆角、无阴影、无渐变，靠字号、字重、间距和 1px 细线建立层级。
const (
	colPaper  uint32 = 0xFFFAFAF7
	colInk    uint32 = 0xFF111113
	colInk2   uint32 = 0xFF2A2A2C
	colMuted  uint32 = 0xFF6E6E68
	colSubtle uint32 = 0xFF9C9C95
	colRule   uint32 = 0xFFDCDCD5
	colRule2  uint32 = 0xFFEDEDE6
	colAccent uint32 = 0xFFC8FF2E
	colGreen  uint32 = 0xFF15803D
	colRed    uint32 = 0xFFB91C1C
	colHover  uint32 = 0xFFF1F1EB
	colPress  uint32 = 0xFFE8E8E1
)

var (
	gpToken    uintptr
	gpScreenDC uintptr
	fontWarned bool
)

// logFont 对应 Win32 LOGFONTW，用于让 GDI+ 创建带中文字形的字体。
type logFont struct {
	Height         int32
	Width          int32
	Escapement     int32
	Orientation    int32
	Weight         int32
	Italic         byte
	Underline      byte
	StrikeOut      byte
	CharSet        byte
	OutPrecision   byte
	ClipPrecision  byte
	Quality        byte
	PitchAndFamily byte
	FaceName       [32]uint16
}

// initGdiplus 启动 GDI+（字体在首次绘制时按需创建）。
func initGdiplus() {
	in := gdiplusStartupInput{GdiplusVersion: 1, SuppressBackgroundThread: 0, SuppressExternalCodecs: 1}
	if st, _, _ := pGdiplusStartup.Call(uintptr(unsafe.Pointer(&gpToken)), uintptr(unsafe.Pointer(&in)), 0); st != 0 {
		log.Printf("[ui] GDI+ 初始化失败: %d\n", st)
	}
}

// surface 是一块双缓冲位图：先画到内存 DC，再一次性 BitBlt 到窗口，避免闪烁。
type surface struct {
	hdc  syscall.Handle
	hbm  syscall.Handle
	old  syscall.Handle
	w, h int32
}

func newSurface(hdc syscall.Handle, w, h int32) *surface {
	if w <= 0 || h <= 0 {
		return nil
	}
	memDC, _, _ := pCreateCompatibleDC.Call(uintptr(hdc))
	if memDC == 0 {
		return nil
	}
	bmi := bitmapInfo{Header: bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       w,
		BiHeight:      -h, // 自上而下
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: 0, // BI_RGB
	}}
	var bits unsafe.Pointer
	hbm, _, _ := pCreateDIBSection.Call(uintptr(hdc), uintptr(unsafe.Pointer(&bmi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 {
		pDeleteDC.Call(memDC)
		return nil
	}
	old, _, _ := pSelectObject.Call(memDC, hbm)
	return &surface{hdc: syscall.Handle(memDC), hbm: syscall.Handle(hbm), old: syscall.Handle(old), w: w, h: h}
}

func (s *surface) free() {
	if s == nil {
		return
	}
	if s.old != 0 {
		pSelectObject.Call(uintptr(s.hdc), uintptr(s.old))
	}
	if s.hbm != 0 {
		pDeleteObject.Call(uintptr(s.hbm))
	}
	pDeleteDC.Call(uintptr(s.hdc))
}

// blit 把内存位图整块贴到窗口 DC。
func (s *surface) blit(dst syscall.Handle) {
	pBitBlt.Call(uintptr(dst), 0, 0, uintptr(s.w), uintptr(s.h), uintptr(s.hdc), 0, 0, srcCopy)
}

// painter 封装 GDI+ 的常用绘制，自带字体与文本格式缓存。
type painter struct {
	gfx     uintptr
	fonts   map[uint64]uintptr
	formats map[uint64]uintptr
}

func newPainter(hdc syscall.Handle) *painter {
	var g uintptr
	if st, _, _ := pGdipCreateFromHDC.Call(uintptr(hdc), uintptr(unsafe.Pointer(&g))); st != 0 || g == 0 {
		return nil
	}
	pGdipSetSmoothingMode.Call(g, gpSmoothingAntiAlias)
	pGdipSetTextRendering.Call(g, gpTextClearType)
	pGdipSetPageUnit.Call(g, gpUnitPixel)
	return &painter{gfx: g, fonts: map[uint64]uintptr{}, formats: map[uint64]uintptr{}}
}

func (p *painter) free() {
	for _, f := range p.fonts {
		pGdipDeleteFont.Call(f)
	}
	for _, f := range p.formats {
		pGdipDeleteStringFmt.Call(f)
	}
	pGdipDeleteGraphics.Call(p.gfx)
}

// font 按字号（像素 em）与粗细取字体；通过 LOGFONT 创建，ZH 字形走系统字体。
func (p *painter) font(size float64, bold bool) uintptr {
	px := int32(math.Round(size))
	key := uint64(uint32(px))<<1 | b2u(bold)
	if f, ok := p.fonts[key]; ok {
		return f
	}
	lf := logFont{
		Height:    -px,
		Weight:    400,
		CharSet:   1, // DEFAULT_CHARSET
		Quality:   5, // CLEARTYPE_QUALITY
		FaceName:  [32]uint16{},
	}
	if bold {
		lf.Weight = 700
	}
	face, _ := syscall.UTF16FromString("Microsoft YaHei UI")
	copy(lf.FaceName[:], face)

	var f uintptr
	pGdipCreateFontFromLogfont.Call(gpScreenDC, uintptr(unsafe.Pointer(&lf)), uintptr(unsafe.Pointer(&f)))
	if f == 0 {
		// 兜底：换 Segoe UI（纯英文环境）
		face2, _ := syscall.UTF16FromString("Segoe UI")
		lf.FaceName = [32]uint16{}
		copy(lf.FaceName[:], face2)
		pGdipCreateFontFromLogfont.Call(gpScreenDC, uintptr(unsafe.Pointer(&lf)), uintptr(unsafe.Pointer(&f)))
	}
	if f == 0 && !fontWarned {
		fontWarned = true
		log.Printf("[ui] 字体创建失败（size=%.1f bold=%v），界面将没有文字\n", size, bold)
	}
	// 标定：LOGFONT 的 lfHeight 在不同 DPI 下不等于「像素 em」（本机实测偏大约 1.36 倍），
	// 于是字比排版盒大、窄窗口下互相压住。这里用「永」的步进宽度闭环校正，
	// 最多迭代 4 次，保证 1 个 size 单位就是 1 个像素。
	if f != 0 {
		want := float64(px)
		lfH := want
		for i := 0; i < 4; i++ {
			adv, ok := p.glyphAdvance(f, "永")
			if !ok || adv <= 0.5 {
				break
			}
			if math.Abs(float64(adv)-want)/want <= 0.02 {
				if !fontWarned {
					fontWarned = true
					log.Printf("[ui] 字号标定完成：请求 %dpx → 实测 %.1fpx（迭代 %d 次）\n", px, adv, i+1)
				}
				break
			}
			lfH = lfH * want / float64(adv)
			pGdipDeleteFont.Call(f)
			f = 0
			lf.Height = -int32(math.Round(lfH))
			pGdipCreateFontFromLogfont.Call(gpScreenDC, uintptr(unsafe.Pointer(&lf)), uintptr(unsafe.Pointer(&f)))
			if f == 0 {
				break
			}
		}
	}
	if f == 0 {
		return 0
	}
	p.fonts[key] = f
	return f
}

// glyphAdvance 用当前 Graphics 量一个字符的步进宽度（含边界留白），用于字号标定。
func (p *painter) glyphAdvance(font uintptr, s string) (float32, bool) {
	if p.gfx == 0 || font == 0 {
		return 0, false
	}
	fm := p.format(gpAlignNear, gpTrimNone, true)
	layout := rectF{X: 0, Y: 0, W: 500, H: 200}
	var out rectF
	sp, err := syscall.UTF16PtrFromString(s)
	if err != nil || sp == nil {
		return 0, false
	}
	pGdipMeasureString.Call(p.gfx, uintptr(unsafe.Pointer(sp)), ^uintptr(0), font,
		uintptr(unsafe.Pointer(&layout)), fm, uintptr(unsafe.Pointer(&out)), 0, 0)
	return out.W, out.W > 0
}

func b2u(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func (p *painter) format(align int32, trim int32, nowrap bool) uintptr {
	key := uint64(uint32(align))<<8 | uint64(uint32(trim)) | b2u(nowrap)<<32
	if f, ok := p.formats[key]; ok {
		return f
	}
	var f uintptr
	pGdipCreateStringFmt.Call(0, 0, uintptr(unsafe.Pointer(&f)))
	if f == 0 {
		return 0
	}
	pGdipSetFmtAlign.Call(f, uintptr(align))
	pGdipSetFmtLineAlign.Call(f, uintptr(gpAlignNear))
	pGdipSetFmtTrimming.Call(f, uintptr(trim))
	if nowrap {
		pGdipSetFmtFlags.Call(f, gpFlagNoWrap)
	}
	p.formats[key] = f
	return f
}

func (p *painter) clear(argb uint32) {
	pGdipGraphicsClear.Call(p.gfx, uintptr(argb))
}

func (p *painter) rect(r rect, argb uint32) {
	if r.W() <= 0 || r.H() <= 0 {
		return
	}
	var br uintptr
	pGdipCreateSolidFill.Call(uintptr(argb), uintptr(unsafe.Pointer(&br)))
	if br == 0 {
		return
	}
	pGdipFillRectI.Call(p.gfx, br, uintptr(r.Left), uintptr(r.Top), uintptr(r.W()), uintptr(r.H()))
	pGdipDeleteBrush.Call(br)
}

// hline / vline 用 1px 填充代替画笔（GDI+ 画笔需要 float 参数，验证成本更高）。
func (p *painter) hline(x, y, w int32, argb uint32) {
	p.rect(rect{x, y, x + w, y + 1}, argb)
}

func (p *painter) vline(x, y, h int32, argb uint32) {
	p.rect(rect{x, y, x + 1, y + h}, argb)
}

func (p *painter) outline(r rect, argb uint32) {
	p.hline(r.Left, r.Top, r.W(), argb)
	p.hline(r.Left, r.Bottom-1, r.W(), argb)
	p.vline(r.Left, r.Top, r.H(), argb)
	p.vline(r.Right-1, r.Top, r.H(), argb)
}

func (p *painter) clip(r rect) {
	pGdipSetClipRectI.Call(p.gfx, uintptr(r.Left), uintptr(r.Top), uintptr(r.W()), uintptr(r.H()), gpCombineIntersect)
}

func (p *painter) unclip() { pGdipResetClip.Call(p.gfx) }

// text 在矩形内绘制单行文本。align: 0 左 / 1 中 / 2 右。
func (p *painter) text(s string, r rectF, size float64, bold bool, argb uint32, align int32) {
	if s == "" {
		return
	}
	f := p.font(size, bold)
	fm := p.format(align, gpTrimEllipsisChar, true)
	if f == 0 || fm == 0 {
		return
	}
	var br uintptr
	pGdipCreateSolidFill.Call(uintptr(argb), uintptr(unsafe.Pointer(&br)))
	sp, err := syscall.UTF16PtrFromString(s)
	if err != nil || sp == nil {
		return
	}
	pGdipDrawString.Call(p.gfx, uintptr(unsafe.Pointer(sp)), ^uintptr(0),
		f, uintptr(unsafe.Pointer(&r)), fm, br)
	pGdipDeleteBrush.Call(br)
	if dbgFile != nil || os.Getenv("LIME_TEXTLOG") != "" {
		mw, mh := p.measure(s, size, bold)
		// 记录「字形实际左边界」：右对齐/居中时盒子的左边界不等于字的位置，
		// 否则碰撞检测会把右边栏文字误判成压住了左边内容。
		left := r.X
		switch align {
		case gpAlignFar:
			left = r.X + r.W - float32(mw)
		case gpAlignCenter:
			left = r.X + (r.W-float32(mw))/2
		}
		dbgText("T", left, r.Y, float32(mw), r.H, float32(mw), float32(mh), s)
	}
}

// textWrap 绘制自动换行的多行文本。
func (p *painter) textWrap(s string, r rectF, size float64, bold bool, argb uint32) {
	if s == "" {
		return
	}
	f := p.font(size, bold)
	fm := p.format(gpAlignNear, gpTrimEllipsisChar, false)
	if f == 0 || fm == 0 {
		return
	}
	var br uintptr
	pGdipCreateSolidFill.Call(uintptr(argb), uintptr(unsafe.Pointer(&br)))
	sp, err := syscall.UTF16PtrFromString(s)
	if err != nil || sp == nil {
		return
	}
	pGdipDrawString.Call(p.gfx, uintptr(unsafe.Pointer(sp)), ^uintptr(0),
		f, uintptr(unsafe.Pointer(&r)), fm, br)
	pGdipDeleteBrush.Call(br)
}

func (p *painter) measure(s string, size float64, bold bool) (int32, int32) {
	f := p.font(size, bold)
	fm := p.format(gpAlignNear, gpTrimNone, true)
	if f == 0 || fm == 0 {
		return 0, 0
	}
	layout := rectF{X: 0, Y: 0, W: 4000, H: float32(size * 2)}
	var out rectF
	sp, _ := syscall.UTF16PtrFromString(s)
	pGdipMeasureString.Call(p.gfx, uintptr(unsafe.Pointer(sp)), ^uintptr(0), f,
		uintptr(unsafe.Pointer(&layout)), fm, uintptr(unsafe.Pointer(&out)), 0, 0)
	return int32(math.Ceil(float64(out.W))), int32(math.Ceil(float64(out.H)))
}

func (p *painter) measureWrap(s string, size float64, bold bool, width float32) int32 {
	f := p.font(size, bold)
	fm := p.format(gpAlignNear, gpTrimEllipsisChar, false)
	if f == 0 || fm == 0 {
		return 0
	}
	layout := rectF{X: 0, Y: 0, W: width, H: 2000}
	var out rectF
	sp, _ := syscall.UTF16PtrFromString(s)
	pGdipMeasureString.Call(p.gfx, uintptr(unsafe.Pointer(sp)), ^uintptr(0), f,
		uintptr(unsafe.Pointer(&layout)), fm, uintptr(unsafe.Pointer(&out)), 0, 0)
	return int32(math.Ceil(float64(out.H)))
}

// ---------------- 剪贴板 / 外部程序 ----------------

func setClipboard(text string) bool {
	ok, _, _ := pOpenClipboard.Call(0)
	if ok == 0 {
		return false
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	sp, err := syscall.UTF16FromString(text)
	if err != nil {
		return false
	}
	size := uintptr(len(sp) * 2)
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return false
	}
	ptr, _, _ := pGlobalLock.Call(h)
	if ptr == 0 {
		pGlobalFree.Call(h)
		return false
	}
	// 用 RtlMoveMemory 把字符串写进全局内存：避免 uintptr→unsafe.Pointer 的转换（go vet 会告警）
	pRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&sp[0])), size)
	pGlobalUnlock.Call(h)
	if r, _, _ := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		pGlobalFree.Call(h)
		return false
	}
	return true
}

// openExternal 用资源管理器/默认程序打开路径或网址。
func openExternal(path string) error {
	ret, _, err := pShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(u16("open"))),
		uintptr(unsafe.Pointer(u16(path))),
		0, 0, swShow)
	if ret <= 32 {
		return err
	}
	return nil
}
