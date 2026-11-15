package winui

import (
	"fmt"
	"math"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"limelink/pc/internal/server"
)

// ---------------- 排版标尺 ----------------
//
// 第二轮调字号：正文字号统一到 20，标题 22–32、统计数字 44、配对码 54，
// 最小号（分区标签/快捷键提示）不低于 16。行高与版式同步放大，避免字变大后互相挤压。

const (
	// 字号（96dpi 逻辑像素）
	tsBrand     = 24
	tsBrandTag  = 16
	tsVersion   = 16
	tsNav       = 22
	tsNavKey    = 16
	tsNavStatus = 18
	tsNavName   = 17
	tsPageTitle = 32
	tsPageSub   = 19
	tsLabel     = 16
	tsStatValue = 44
	tsStatUnit  = 18
	tsFilter    = 19
	tsFeedTag   = 16
	tsFeedTitle = 22
	tsFeedTime  = 18
	tsFeedText  = 20
	tsRailCode  = 28
	tsHint      = 18
	tsBtn       = 19
	tsAddr      = 20
	tsDevName   = 23
	tsDevMeta   = 18
	tsDevState  = 18
	tsPairCode  = 54
	tsSetLabel  = 21
	tsSetValue  = 20
	tsAboutKey  = 16
	tsAboutVal  = 20
	tsToast     = 19

	// 版式尺寸
	topH   = 72
	navW   = 224
	padX   = 28
	padTop = 22

	navStartY = 118
	navStep   = 48

	statBlockH = 114
	feedRowH   = 118
	settingRow = 70
	railW      = 320
	btnH       = 38
)

// theme 是一套颜色令牌：浅色=白底黑字，深色=黑底白字，线色比 1.3.0 深。
type theme struct {
	paper  uint32
	ink    uint32
	ink2   uint32
	muted  uint32
	subtle uint32
	rule   uint32
	rule2  uint32
	accent uint32
	green  uint32
	red    uint32
	hover  uint32
	press  uint32
	dark   bool
}

var lightTheme = theme{
	paper:  0xFFFAFAF7,
	ink:    0xFF0E0E10,
	ink2:   0xFF26262A,
	muted:  0xFF5E5E58,
	subtle: 0xFF6B6B64,
	rule:   0xFFC3C3B9,
	rule2:  0xFFE1E1D9,
	accent: 0xFFC8FF2E,
	green:  0xFF12703A,
	red:    0xFFB01616,
	hover:  0xFFF0F0EA,
	press:  0xFFE4E4DC,
}

var darkTheme = theme{
	paper:  0xFF0C0C0E,
	ink:    0xFFF3F3EE,
	ink2:   0xFFD6D6CF,
	muted:  0xFFA3A39B,
	subtle: 0xFF94948D,
	rule:   0xFF3D3D43,
	rule2:  0xFF28282D,
	accent: 0xFFC8FF2E,
	green:  0xFF4ADE80,
	red:    0xFFF87171,
	hover:  0xFF1A1A1E,
	press:  0xFF232329,
	dark:   true,
}

type ui struct {
	a *App
	p *painter
	s float64
	c theme
}

func (u *ui) i(v float64) int32  { return int32(v*u.s + 0.5) }
func (u *ui) f(v float64) float64 { return v * u.s }

// W / H 返回客户区尺寸（物理像素）。布局坐标统一走 u.i()（逻辑→物理），
// 所以这里也必须是物理像素，否则视口会被算小。
func (u *ui) W() int32 { return u.a.w }
func (u *ui) H() int32 { return u.a.h }

func (a *App) paintAll(hdc syscall.Handle) {
	if a.surf == nil || a.surf.w != a.w || a.surf.h != a.h {
		a.surf.free()
		a.surf = newSurface(hdc, a.w, a.h)
		if a.paint != nil {
			a.paint.free()
			a.paint = nil
		}
	}
	if a.surf == nil {
		return
	}
	if a.paint == nil {
		a.paint = newPainter(a.surf.hdc)
		if a.paint == nil {
			return
		}
	}
	u := &ui{a: a, p: a.paint, s: a.scale, c: a.theme}
	u.draw()
	a.surf.blit(hdc)
}

func (u *ui) draw() {
	a := u.a
	a.hits = a.hits[:0]
	a.focusables = a.focusables[:0]
	a.tooltip = ""
	u.p.clear(u.c.paper)

	// 顶栏
	u.p.rect(rect{u.i(28), u.i(28), u.i(42), u.i(42)}, u.c.accent)
	u.brandMark(u.i(28), u.i(28), u.i(14))
	u.text("小青柠", u.i(48), u.i(18), tsBrand, true, u.c.ink)
	u.text("LIMELINK", u.i(48)+u.w("小青柠", tsBrand, true)+u.i(12), u.i(26), tsBrandTag, false, u.c.muted)
	u.textR("v"+a.version, u.W()-u.i(padX), u.i(22), tsVersion, false, u.c.subtle)
	u.p.hline(0, u.i(topH-1), u.W(), u.c.rule)

	u.drawNav()
	u.p.vline(u.i(navW), u.i(topH), u.H()-u.i(topH), u.c.rule)

	switch a.page {
	case PageHome:
		u.drawHome()
	case PageDevices:
		u.drawDevices()
	case PagePair:
		u.drawPair()
	case PageSettings:
		u.drawSettings()
	}

	u.drawToast()
	u.drawTooltip()
	u.drawFocusRing()
}

// drawTooltip 悬停在被截断的通知行上时，显示完整标题（原生自绘没有系统 tooltip）。
func (u *ui) drawTooltip() {
	a := u.a
	if a.tooltip == "" {
		return
	}
	pad := u.i(12)
	maxW := u.i(620)
	w := u.w(a.tooltip, tsFeedTitle, true) + pad*2
	if w > maxW {
		w = maxW
	}
	h := u.i(46)
	x := a.mouseX + u.i(18)
	y := a.mouseY + u.i(22)
	if x+w > u.W()-u.i(12) {
		x = u.W() - u.i(12) - w
	}
	if y+h > u.H()-u.i(12) {
		y = a.mouseY - h - u.i(10)
	}
	if x < u.i(8) {
		x = u.i(8)
	}
	if y < u.i(8) {
		y = u.i(8)
	}
	u.p.rect(rect{x, y, x + w, y + h}, u.c.ink)
	u.textW(a.tooltip, x+pad, y+u.i(11), w-pad*2, tsFeedTitle, true, u.c.paper)
}

// drawFocusRing 键盘焦点环：2px 墨色外框，始终可见（自绘界面没有系统焦点指示）。
func (u *ui) drawFocusRing() {
	a := u.a
	if a.focusIdx < 0 || a.focusIdx >= len(a.focusables) {
		return
	}
	r := a.focusables[a.focusIdx].r
	outer := rect{r.Left - u.i(3), r.Top - u.i(3), r.Right + u.i(3), r.Bottom + u.i(3)}
	u.p.outline(outer, u.c.ink)
	inner := rect{outer.Left + u.i(1), outer.Top + u.i(1), outer.Right - u.i(1), outer.Bottom - u.i(1)}
	u.p.outline(inner, u.c.ink)
}

// brandMark 在青柠色块里画一个「青柠切面」：圆环 + 十字辐条，全部落在色块内部。
func (u *ui) brandMark(x, y, size int32) {
	cx, cy := x+size/2, y+size/2
	r := size * 26 / 100
	if r < 3 {
		r = 3
	}
	th := u.i(2)
	if th < 2 {
		th = 2
	}
	seg := 12
	for i := 0; i < seg; i++ {
		ang := 2 * math.Pi * float64(i) / float64(seg)
		px := cx + int32(float64(r)*math.Cos(ang)) - th/2
		py := cy + int32(float64(r)*math.Sin(ang)) - th/2
		u.p.rect(rect{px, py, px + th, py + th}, u.c.ink)
	}
	u.p.rect(rect{cx - r, cy - th/2, cx + r, cy + th/2}, u.c.ink)
	u.p.rect(rect{cx - th/2, cy - r, cx + th/2, cy + r}, u.c.ink)
}
// stampText 给非今天的通知带上日期，避免昨天的记录看起来像今天。
func stampText(t time.Time) string {
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() && now.YearDay()-t.YearDay() == 1 {
		return "昨天 " + t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}
// ---------------- 文本（全部带宽度约束，避免长文本压到右侧元素） ----------------

func (u *ui) text(s string, x, top int32, size float64, bold bool, col uint32) {
	u.textW(s, x, top, u.i(700), size, bold, col)
}

func (u *ui) textW(s string, x, top, width int32, size float64, bold bool, col uint32) {
	if s == "" || width <= 0 {
		return
	}
	sz := u.f(size)
	r := rectF{X: float32(x), Y: float32(top), W: float32(width), H: float32(sz * 1.45)}
	u.p.text(s, r, sz, bold, col, gpAlignNear)
}

func (u *ui) textR(s string, right, top int32, size float64, bold bool, col uint32) {
	if s == "" {
		return
	}
	sz := u.f(size)
	w := u.i(700)
	r := rectF{X: float32(right) - float32(w), Y: float32(top), W: float32(w), H: float32(sz * 1.45)}
	u.p.text(s, r, sz, bold, col, gpAlignFar)
}

func (u *ui) textC(s string, cx, top int32, size float64, bold bool, col uint32) {
	sz := u.f(size)
	r := rectF{X: float32(cx) - 140, Y: float32(top), W: 280, H: float32(sz * 1.45)}
	u.p.text(s, r, sz, bold, col, gpAlignCenter)
}

func (u *ui) textWrap(s string, x, top, width int32, size float64, col uint32, lines int) {
	if s == "" || width <= 0 {
		return
	}
	sz := u.f(size)
	h := int32(float64(lines)*float64(sz)*1.42 + 2)
	r := rectF{X: float32(x), Y: float32(top), W: float32(width), H: float32(h)}
	u.p.textWrap(s, r, sz, false, col)
}

func (u *ui) w(s string, size float64, bold bool) int32 {
	w, _ := u.p.measure(s, u.f(size), bold)
	return w
}

func (u *ui) label(s string, x, top int32) {
	u.text(strings.ToUpper(s), x, top, tsLabel, true, u.c.muted)
}

func (u *ui) addHit(r rect, id string) {
	u.a.hits = append(u.a.hits, hit{r: r, id: id})
}

// addFocus 登记一个可 Tab 聚焦的控件（列表行不进焦点序列，避免 Tab 走 N 次）。
func (u *ui) addFocus(r rect, id string) {
	u.a.focusables = append(u.a.focusables, hit{r: r, id: id})
}

func (u *ui) hit(r rect, id string) {
	u.addHit(r, id)
	u.addFocus(r, id)
}

// textBtn 文本按钮：默认墨色，悬停铺底 + 下划线。
func (u *ui) textBtn(id, label string, x, top int32, col uint32) int32 {
	w := u.w(label, tsBtn, true)
	r := rect{x - u.i(8), top - u.i(9), x + w + u.i(8), top + u.i(26)}
	if u.a.hover == id {
		u.p.rect(r, u.c.hover)
		u.p.hline(r.Left+u.i(8), r.Bottom-u.i(7), w, col)
	}
	u.text(label, x, top, tsBtn, true, col)
	u.hit(r, id)
	return w
}

// solidBtn 主按钮：墨底纸字（深色主题下自然反相）。
func (u *ui) solidBtn(id, label string, x, top, height int32) int32 {
	w := u.w(label, tsBtn, true) + u.i(36)
	r := rect{x, top, x + w, top + height}
	bg := u.c.ink
	if u.a.hover == id {
		bg = u.c.ink2
	}
	if u.a.press == id {
		bg = u.c.subtle
	}
	u.p.rect(r, bg)
	u.text(label, x+u.i(18), top+(height-u.i(25))/2, tsBtn, true, u.c.paper)
	u.hit(r, id)
	return w
}

func (u *ui) outlineBtn(id, label string, x, top, height int32, col uint32) int32 {
	w := u.w(label, tsBtn, true) + u.i(34)
	r := rect{x, top, x + w, top + height}
	if u.a.hover == id {
		u.p.rect(r, u.c.hover)
	}
	u.p.outline(r, u.c.rule)
	u.text(label, x+u.i(17), top+(height-u.i(25))/2, tsBtn, true, col)
	u.hit(r, id)
	return w
}

// ---------------- 导航 ----------------

func (u *ui) drawNav() {
	a := u.a
	items := []struct {
		id    string
		label string
		key   string
	}{
		{"nav:0", "通知流", "1"},
		{"nav:1", "设备", "2"},
		{"nav:2", "配对", "3"},
		{"nav:3", "设置", "4"},
	}
	y := u.i(navStartY)
	step := u.i(navStep)
	for i, it := range items {
		active := int(a.page) == i
		row := rect{u.i(0), y - u.i(12), u.i(navW), y + u.i(28)}
		if a.hover == it.id {
			u.p.rect(rect{u.i(1), row.Top, u.i(navW), row.Bottom}, u.c.hover)
		}
		if active {
			u.p.rect(rect{u.i(0), y - u.i(2), u.i(4), y + u.i(22)}, u.c.ink)
			u.text(it.label, u.i(38), y, tsNav, true, u.c.ink)
		} else {
			u.text(it.label, u.i(38), y, tsNav, false, u.c.muted)
		}
		if it.id == "nav:1" && len(a.state.Pending) > 0 {
			// 待确认数量：窗口开着时也能一眼看到有人在等
			label := fmt.Sprintf("%d", len(a.state.Pending))
			bw := u.w(label, tsNavKey, true) + u.i(16)
			bx := u.i(navW) - u.i(44) - bw
			u.p.rect(rect{bx, y + u.i(2), bx + bw, y + u.i(26)}, u.c.ink)
			u.textW(label, bx+u.i(8), y+u.i(6), bw-u.i(12), tsNavKey, true, u.c.paper)
		}
		u.textR(it.key, u.i(navW-26), y+u.i(4), tsNavKey, false, u.c.subtle)
		u.hit(row, it.id)
		y += step
	}

	// 底部状态
	st := a.state
	u.p.hline(u.i(26), u.H()-u.i(92), u.i(navW-52), u.c.rule2)
	dot := u.c.rule
	if st.OnlineCount > 0 {
		dot = u.c.accent
	}
	dy := u.H() - u.i(72)
	u.p.rect(rect{u.i(38), dy + u.i(10), u.i(38) + u.i(9), dy + u.i(19)}, dot)
	u.p.outline(rect{u.i(38), dy + u.i(10), u.i(38) + u.i(9), dy + u.i(19)}, u.c.rule)
	u.text(fmt.Sprintf("%d / %d 在线", st.OnlineCount, st.PairedCount), u.i(56), dy, tsNavStatus, false, u.c.muted)
	u.textW(a.state.Name, u.i(38), u.H()-u.i(44), u.i(navW-56), tsNavName, false, u.c.subtle)
}

// ---------------- 页面骨架 ----------------

func (u *ui) pageHead() int32 {
	a := u.a
	x0 := u.i(navW + padX)
	top := u.i(topH + padTop)
	u.text(pageTitles[a.page], x0, top-u.i(8), tsPageTitle, true, u.c.ink)
	u.text(pageSubs[a.page], x0, top+u.i(42), tsPageSub, false, u.c.muted)
	return top + u.i(78)
}

func (u *ui) contentW() int32 { return u.W() - u.i(navW+padX*2) }

// ---------------- 通知流 ----------------

func (u *ui) drawHome() {
	a := u.a
	st := a.state
	x0 := u.i(navW + padX)
	y := u.pageHead()
	cw := u.contentW()

	// 三栏统计，竖细线切分
	colW := cw / 3
	stats := []struct {
		label string
		value string
		unit  string
	}{
		{"今日同步", fmt.Sprintf("%d", st.TodayCount), "条通知"},
		{"在线设备", fmt.Sprintf("%d", st.OnlineCount), fmt.Sprintf("共 %d 台", st.PairedCount)},
		{"运行时长", uptimeShort(st.UptimeSec), "本次启动"},
	}
	for i, s := range stats {
		x := x0 + int32(i)*colW
		u.label(s.label, x, y)
		u.text(s.value, x, y+u.i(22), tsStatValue, false, u.c.ink)
		u.text(s.unit, x, y+u.i(88), tsStatUnit, false, u.c.muted)
		if i > 0 {
			u.p.vline(x-u.i(22), y, u.i(statBlockH-10), u.c.rule)
		}
	}
	u.p.hline(x0, y+u.i(statBlockH), cw, u.c.rule)

	// 筛选
	fy := y + u.i(statBlockH+24)
	filters := []struct{ id, label string }{
		{"filter:", "全部"},
		{"filter:wechat", "微信"},
		{"filter:sms", "短信"},
		{"filter:app", "应用"},
		{"filter:system", "系统"},
	}
	fx := x0
	for _, f := range filters {
		active := a.filter == strings.TrimPrefix(f.id, "filter:")
		col := u.c.muted
		if active {
			col = u.c.ink
		}
		w := u.w(f.label, tsFilter, active)
		r := rect{fx - u.i(8), fy - u.i(8), fx + w + u.i(8), fy + u.i(28)}
		if a.hover == f.id {
			u.p.rect(r, u.c.hover)
		}
		u.text(f.label, fx, fy, tsFilter, active, col)
		if active {
			u.p.hline(fx, fy+u.i(26), w, u.c.ink)
		}
		u.hit(r, f.id)
		fx += w + u.i(26)
	}

	// 右栏
	rail := u.i(railW)
	feedW := cw - rail - u.i(34)
	feedX := x0
	feedTop := fy + u.i(46)
	feedBottom := u.H() - u.i(24)
	u.drawFeed(feedX, feedTop, feedW, feedBottom)
	u.drawRail(x0+feedW+u.i(34), feedTop, rail, feedBottom)
}

func (u *ui) drawFeed(x, top, width, bottom int32) {
	a := u.a
	rows := u.filteredRecords()
	viewport := bottom - top
	rowH := u.i(feedRowH)
	contentH := int32(len(rows)) * rowH
	a.maxScr = contentH - viewport
	if len(rows) == 0 {
		a.maxScr = 0
	}
	a.feedH = viewport
	a.feedContent = contentH
	a.sbTop = top
	a.sbH = viewport
	a.clampScroll()

	if len(rows) == 0 {
		// 区分「一条都没有」和「筛选后没有匹配」：后者说「还没有通知」是在撒谎
		if len(a.state.Records) == 0 {
			u.text("还没有通知", x, top+u.i(18), tsFeedTitle, true, u.c.ink)
			u.textWrap("手机配对并开启通知读取后，微信、短信、应用与系统通知会实时出现在这里。",
				x, top+u.i(54), u.i(460), tsFeedText, u.c.muted, 2)
		} else {
			u.text("没有匹配的通知", x, top+u.i(18), tsFeedTitle, true, u.c.ink)
			u.textWrap("当前筛选条件下没有记录。点上面的「全部」可以看回全部通知。",
				x, top+u.i(54), u.i(460), tsFeedText, u.c.muted, 2)
			u.textBtn("filter:", "查看全部", x, top+u.i(110), u.c.ink)
		}
		return
	}

	// 只画完整可见的行（不依赖 GDI+ 裁剪，裁剪在本机会让后续绘制失效）
	first := int(a.scroll / rowH)
	if first < 0 {
		first = 0
	}
	for i := first; i < len(rows); i++ {
		ry := top + int32(i)*rowH - a.scroll
		if ry+rowH > bottom {
			break
		}
		u.drawFeedRow(rows[i], x, ry, width, rowH)
	}

	// 有新通知但用户正滚在下面：顶部浮一个可点的提示
	if a.newCount > 0 && a.scroll > 0 {
		label := fmt.Sprintf("↑ %d 条新通知", a.newCount)
		w := u.w(label, tsHint, true) + u.i(30)
		px := x + (width-w)/2
		py := top + u.i(6)
		u.p.rect(rect{px, py, px + w, py + u.i(36)}, u.c.ink)
		u.textW(label, px+u.i(16), py+u.i(8), w-u.i(22), tsHint, true, u.c.paper)
		u.hit(rect{px, py, px + w, py + u.i(36)}, "newpill")
	}

	if a.maxScr > 0 {
		trackH := viewport
		thumbH := trackH * viewport / contentH
		if thumbH < u.i(30) {
			thumbH = u.i(30)
		}
		pos := trackH * a.scroll / contentH
		sx := x + width + u.i(10)
		hovered := a.hover == idScrollbar || a.dragging
		col := u.c.muted
		if hovered {
			col = u.c.ink
		}
		u.p.rect(rect{sx, top, sx + u.i(3), top + trackH}, u.c.rule2)
		u.p.rect(rect{sx, top + pos, sx + u.i(3), top + pos + thumbH}, col)
		// 可点击/可拖拽：热区比 3px 视觉宽得多，避免"看得见点不着"
		a.hits = append(a.hits, hit{r: rect{sx - u.i(6), top, sx + u.i(12), top + trackH}, id: idScrollbar})
	}
}

func (u *ui) drawFeedRow(r server.Record, x, y, width, height int32) {
	a := u.a
	cat := strings.ToLower(r.IconType)
	if cat == "" {
		cat = strings.ToLower(r.AppLabel)
	}
	label := map[string]string{"wechat": "微信", "sms": "短信", "system": "系统"}[cat]
	if label == "" {
		label = truncRunes(r.AppName, 3)
		if label == "" {
			label = "应用"
		}
	}
	tagW := u.i(80)
	u.textW(label, x, y+u.i(24), tagW-u.i(6), tsFeedTag, true, u.c.muted)

	title := r.Title
	if title == "" {
		title = r.AppName
	}
	// 时间固定占右侧 86px，标题宽度扣掉它，长标题自动省略，不会压到时间
	timeW := u.i(124)
	textX := x + u.i(86)
	textW := width - u.i(86) - u.i(14)
	titleW := textW - timeW
	u.textW(title, textX, y+u.i(12), titleW, tsFeedTitle, true, u.c.ink)
	u.textR(stampText(r.ReceivedAt), x+width, y+u.i(24), tsFeedTime, false, u.c.subtle)

	// 整行可右键：复制正文 / 复制标题 / 删除这条
	rowID := rowPrefix + r.ID
	a.hits = append(a.hits, hit{r: rect{x, y, x + width, y + height - u.i(1)}, id: rowID})
	if a.hover == rowID && u.w(title, tsFeedTitle, true) > titleW {
		a.tooltip = title // 被截断时给出完整标题
	}

	text := r.Text
	if text == "" && r.Hidden {
		text = "（内容已隐藏）"
	}
	u.textWrap(text, textX, y+u.i(50), textW, tsFeedText, u.c.muted, 2)

	u.p.hline(x, y+height-u.i(1), width, u.c.rule2)
}

func (u *ui) filteredRecords() []server.Record {
	a := u.a
	out := make([]server.Record, 0, len(a.state.Records))
	for _, r := range a.state.Records {
		if a.filter == "" {
			out = append(out, r)
			continue
		}
		cat := strings.ToLower(r.IconType)
		if cat == "" {
			cat = strings.ToLower(r.AppLabel)
		}
		if cat == a.filter {
			out = append(out, r)
		}
	}
	return out
}

func (u *ui) drawRail(x, top, width, bottom int32) {
	a := u.a
	st := a.state

	u.label("配对码 · PAIR", x, top)
	code := st.Code
	if code == "" {
		code = "——————"
	}
	cell := u.i(42)
	for i, ch := range code {
		if i >= 6 {
			break
		}
		u.textC(string(ch), x+cell/2+int32(i)*cell, top+u.i(24), tsRailCode, false, u.c.ink)
	}
	u.hit(rect{x - u.i(4), top + u.i(16), x + cell*6, top + u.i(62)}, "copycode")
	u.textWrap("手机打开小青柠 → 选中这台电脑 → 连接，或直接输入这 6 位数字。",
		x, top+u.i(64), width-u.i(10), tsHint, u.c.muted, 3)
	by := top + u.i(132)
	w1 := u.textBtn("copycode", "复制配对码", x, by, u.c.ink)
	u.textBtn("reset", "重置配对", x+w1+u.i(24), by, u.c.muted)

	u.p.hline(x, top+u.i(172), width, u.c.rule)

	sy := top + u.i(192)
	u.label("文字快送 · SEND", x, sy)
	u.textWrap("写一段文字，手机会在 5 秒内弹出通知。", x, sy+u.i(24), width-u.i(10), tsHint, u.c.muted, 2)
	u.outlineBtn("sendtext", "写一段文字…", x, sy+u.i(64), u.i(btnH), u.c.ink)

	u.p.hline(x, sy+u.i(124), width, u.c.rule)

	ly := sy + u.i(144)
	u.label("本机地址 · ADDRESS", x, ly)
	iy := ly + u.i(26)
	for i, ip := range st.IPs {
		if i >= 2 || iy > bottom-u.i(26) {
			break
		}
		line := fmt.Sprintf("%s :%d", ip.IP, st.Port)
		id := "copyip:" + ip.IP + ":" + fmt.Sprint(st.Port)
		if a.hover == id {
			u.p.rect(rect{x - u.i(6), iy - u.i(6), x + width, iy + u.i(22)}, u.c.hover)
			u.p.hline(x, iy+u.i(24), u.w(line, tsAddr, false), u.c.muted)
		}
		u.text(line, x, iy, tsAddr, false, u.c.ink)
		u.textR(ifaceShort(ip.Iface), x+width, iy, tsDevMeta, false, u.c.subtle)
		u.hit(rect{x - u.i(6), iy - u.i(8), x + width, iy + u.i(26)}, id)
		iy += u.i(32)
	}
	if len(st.IPs) > 2 && iy < bottom {
		u.text(fmt.Sprintf("还有 %d 个地址", len(st.IPs)-2), x, iy, tsHint, false, u.c.subtle)
	}
}

// ---------------- 设备 ----------------

func (u *ui) drawDevices() {
	a := u.a
	st := a.state
	x0 := u.i(navW + padX)
	y := u.pageHead()
	cw := u.contentW()

	if len(st.Pending) > 0 {
		u.label(fmt.Sprintf("待确认 · %d", len(st.Pending)), x0, y)
		y += u.i(32)
		for _, p := range st.Pending {
			u.textW(p.Name, x0, y+u.i(4), cw-u.i(340), tsDevName, true, u.c.ink)
			u.textW(fmt.Sprintf("%s · %s发起", p.IP, p.WaitText), x0, y+u.i(36), cw-u.i(340), tsDevMeta, false, u.c.muted)
			bx := x0 + cw
			wDeny := u.w("拒绝", tsBtn, true) + u.i(16)
			u.textBtn("deny:"+p.ID, "拒绝", bx-wDeny, y+u.i(18), u.c.muted)
			u.solidBtn("allow:"+p.ID, "允许连接", bx-wDeny-u.i(14)-u.i(130), y+u.i(10), u.i(btnH))
			u.p.hline(x0, y+u.i(72), cw, u.c.rule2)
			y += u.i(86)
		}
		y += u.i(20)
	}

	u.label(fmt.Sprintf("已配对 · %d", len(st.Devices)), x0, y)
	y += u.i(32)

	if len(st.Devices) == 0 {
		u.text("还没有设备", x0, y+u.i(10), tsFeedTitle, true, u.c.ink)
		u.textWrap("手机端打开小青柠后，会在「附近的电脑」里出现这台电脑；点连接，再回到这里点「允许」。",
			x0, y+u.i(46), u.i(620), tsFeedText, u.c.muted, 3)
		return
	}

	for _, d := range st.Devices {
		u.textW(d.Name, x0, y+u.i(4), cw-u.i(380), tsDevName, true, u.c.ink)
		meta := d.IP
		if d.LastSeenText != "" {
			meta += " · " + d.LastSeenText
		}
		u.textW(meta, x0, y+u.i(38), cw-u.i(380), tsDevMeta, false, u.c.muted)

		dot := u.c.rule
		stateText := "离线"
		if d.Online {
			dot = u.c.accent
			stateText = "在线"
		}
		dcy := y + u.i(14)
		u.p.rect(rect{x0 + cw - u.i(196), dcy, x0 + cw - u.i(187), dcy + u.i(9)}, dot)
		u.p.outline(rect{x0 + cw - u.i(196), dcy, x0 + cw - u.i(187), dcy + u.i(9)}, u.c.rule)
		u.textW(stateText, x0+cw-u.i(176), y+u.i(8), u.i(46), tsDevState, false, u.c.muted)

		bx := x0 + cw
		wKick := u.w("解绑", tsBtn, true)
		u.textBtn("kick:"+d.ID, "解绑", bx-wKick, y+u.i(20), u.c.muted)
		u.textBtn("send:"+d.ID, "发文字", bx-wKick-u.i(26)-u.w("发文字", tsBtn, true), y+u.i(20), u.c.ink)

		u.p.hline(x0, y+u.i(78), cw, u.c.rule2)
		y += u.i(92)
	}
}

// ---------------- 配对 ----------------

func (u *ui) drawPair() {
	a := u.a
	st := a.state
	x0 := u.i(navW + padX)
	y := u.pageHead()
	cw := u.contentW()

	u.label("六位配对码 · CODE", x0, y)
	cy := y + u.i(32)
	code := st.Code
	cellW := u.i(80)
	cellH := u.i(112)
	for i := 0; i < 6; i++ {
		ch := "—"
		if i < len(code) {
			ch = string(code[i])
		}
		cx := x0 + int32(i)*cellW
		u.textC(ch, cx+cellW/2, cy+u.i(10), tsPairCode, false, u.c.ink)
		if i > 0 {
			u.p.vline(cx, cy, cellH, u.c.rule)
		}
	}
	u.p.hline(x0, cy+cellH, cellW*6, u.c.rule)
	u.hit(rect{x0 - u.i(4), cy, x0 + cellW*6, cy + cellH}, "copycode")

	u.textWrap("手机与小青柠电脑端在同一 Wi-Fi 时，直接在手机「附近的电脑」里点这台电脑即可免码连接；输入这 6 位数字是备用方式。",
		x0, cy+cellH+u.i(24), u.i(680), tsFeedText, u.c.muted, 3)

	by := cy + cellH + u.i(94)
	w1 := u.solidBtn("copycode", "复制配对码", x0, by, u.i(btnH))
	u.outlineBtn("reset", "重置配对", x0+w1+u.i(16), by, u.i(btnH), u.c.red)

	ay := by + u.i(84)
	u.p.hline(x0, ay-u.i(20), cw, u.c.rule)
	u.label("本机地址 · 也可手动填写", x0, ay)
	iy := ay + u.i(32)
	if len(st.IPs) == 0 {
		u.text("未找到可用局域网地址", x0, iy, tsHint, false, u.c.muted)
	}
	for _, ip := range st.IPs {
		line := fmt.Sprintf("%s :%d", ip.IP, st.Port)
		id := "copyip:" + ip.IP + ":" + fmt.Sprint(st.Port)
		if a.hover == id {
			u.p.rect(rect{x0 - u.i(6), iy - u.i(8), x0 + cw, iy + u.i(24)}, u.c.hover)
		}
		u.text(line, x0, iy, tsAddr, true, u.c.ink)
		u.textW(ifaceShort(ip.Iface), x0+u.i(300), iy, u.i(260), tsDevMeta, false, u.c.subtle)
		u.textR("复制", x0+cw, iy, tsDevMeta, false, u.c.muted)
		u.hit(rect{x0 - u.i(6), iy - u.i(8), x0 + cw, iy + u.i(26)}, id)
		u.p.hline(x0, iy+u.i(30), cw, u.c.rule2)
		iy += u.i(40)
	}
}

// ---------------- 设置 ----------------

func (u *ui) drawSettings() {
	a := u.a
	st := a.state
	x0 := u.i(navW + padX)
	y := u.pageHead()
	cw := u.contentW()

	type row struct {
		label  string
		value  string
		btnID  string
		btnTxt string
		danger bool
	}
	rows := []row{
		{"设备名", st.Name, "rename", "改名", false},
		{"通知记录", fmt.Sprintf("%d 条（最多 200 条）", len(st.Records)), "clear", "清空", true},
		{"数据目录", shrinkPath(st.StoreDir), "store", "打开", false},
		{"防火墙", "放行 TCP 8787 / UDP 8788", "firewall", "查看", false},
		{"诊断信息", "版本、端口、设备与在线状态", "diag", "复制", false},
	}
	for _, r := range rows {
		col := u.c.ink
		if r.danger {
			col = u.c.red
		}
		btnW := u.w(r.btnTxt, tsSetValue, true)
		u.text(r.label, x0, y+u.i(16), tsSetLabel, true, u.c.ink)
		valW := cw - u.i(180) - btnW - u.i(30)
		if r.btnID == "store" {
			// 路径被截断时，点一下把完整路径复制走
			vid := "copystore"
			if a.hover == vid {
				u.p.rect(rect{x0 + u.i(174), y + u.i(10), x0 + u.i(174) + valW, y + u.i(52)}, u.c.hover)
			}
			u.textW(r.value, x0+u.i(180), y+u.i(19), valW, tsSetValue, false, u.c.muted)
			u.hit(rect{x0 + u.i(174), y + u.i(10), x0 + u.i(174) + valW, y + u.i(52)}, vid)
		} else {
			u.textW(r.value, x0+u.i(180), y+u.i(19), valW, tsSetValue, false, u.c.muted)
		}
		u.textBtn(r.btnID, r.btnTxt, x0+cw-btnW, y+u.i(16), col)
		u.p.hline(x0, y+u.i(settingRow-8), cw, u.c.rule2)
		y += u.i(settingRow)
	}

	// 主题切换
	y += u.i(18)
	u.label("主题 · THEME", x0, y)
	wLight := u.textBtn("theme:light", "浅色", x0, y+u.i(30), u.c.ink)
	u.textBtn("theme:dark", "深色", x0+wLight+u.i(26), y+u.i(30), u.c.ink)
	if a.theme.dark {
		u.p.hline(x0+wLight+u.i(26), y+u.i(55), u.w("深色", tsBtn, true), u.c.ink)
	} else {
		u.p.hline(x0, y+u.i(55), u.w("浅色", tsBtn, true), u.c.ink)
	}

	// 关于
	ay := y + u.i(94)
	u.p.hline(x0, ay-u.i(18), cw, u.c.rule)
	u.label("关于 · ABOUT", x0, ay)
	items := []struct{ k, v string }{
		{"版本", "v" + st.Version},
		{"协议", "LIMELINK3"},
		{"端口", fmt.Sprintf("%d", st.Port)},
		{"启动", startedAt(st.StartedAt)},
		{"累计通知", fmt.Sprintf("%d", st.NotifyCount)},
		{"被拦截推送", fmt.Sprintf("%d", st.NotifyRejects)},
	}
	iy := ay + u.i(30)
	colW := cw / 3
	for i, it := range items {
		cx := x0 + int32(i%3)*colW
		if i == 3 {
			iy += u.i(58)
		}
		u.text(it.k, cx, iy, tsAboutKey, false, u.c.subtle)
		u.textW(it.v, cx, iy+u.i(22), colW-u.i(24), tsAboutVal, false, u.c.ink)
	}

	by := iy + u.i(72)
	u.textBtn("about", "关于小青柠 LimeLink", x0, by, u.c.ink)
	u.textW("原生 Win32 + GDI+ 自绘，无浏览器内核。", x0+u.i(240), by+u.i(2), cw-u.i(240), tsHint, false, u.c.subtle)
}

// ---------------- 提示条 ----------------

func (u *ui) drawToast() {
	a := u.a
	if len(a.toasts) == 0 {
		return
	}
	// 从下往上堆叠，最多 3 条；越新的越靠下
	y := u.H() - u.i(24)
	for k := len(a.toasts) - 1; k >= 0; k-- {
		t := a.toasts[k]
		w := u.w(t.text, tsToast, false) + u.i(32)
		h := u.i(44)
		y -= h
		x := u.W() - u.i(26) - w
		u.p.rect(rect{x, y, x + w, y + h}, u.c.ink)
		u.text(t.text, x+u.i(16), y+u.i(10), tsToast, false, u.c.paper)
		y -= u.i(8)
	}
}
// ---------------- 文本工具 ----------------

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func shortTime(t time.Time) string {
	return t.Format("15:04")
}

func uptimeShort(sec int64) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%dh", sec/3600)
}

func startedAt(ms int64) string {
	return time.UnixMilli(ms).Format("01-02 15:04")
}

func ifaceShort(s string) string {
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) > 12 {
		return truncRunes(s, 12)
	}
	return s
}

func shrinkPath(p string) string {
	if p == "" {
		return ""
	}
	if len(p) > 30 {
		return "…" + p[len(p)-29:]
	}
	return p
}
