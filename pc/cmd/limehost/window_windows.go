package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"limelink/pc/internal/config"
)

// 界面窗口用 Edge / Chrome 的「应用模式」（--app）承载：
// 没有标题栏地址栏、独立任务栏图标，外观与原生窗口一致，
// 好处是不用引入 WebView2/CGO，单个 exe 仍然可以零依赖运行。

var (
	winMu  sync.Mutex
	winCmd *exec.Cmd
)

// uiURL 拼出界面地址。
func uiURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/", port)
}

// findBrowser 找可用的 Chromium 内核浏览器（Edge 优先）。
func findBrowser() string {
	candidates := []string{}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
		)
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("msedge.exe"); err == nil {
		return p
	}
	if p, err := exec.LookPath("chrome.exe"); err == nil {
		return p
	}
	return ""
}

// openWindow 打开（或再次唤起）界面窗口。
func openWindow(url string) error {
	exe := findBrowser()
	if exe == "" {
		// 没有 Chromium 浏览器：退回系统默认浏览器
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	profile := filepath.Join(config.ConfigDir(), "ui-profile")
	_ = os.MkdirAll(profile, 0o755)
	args := []string{
		"--app=" + url,
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,msEdgeSidebarV2",
		"--window-size=1240,860",
		"--disable-background-networking",
	}
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	winMu.Lock()
	winCmd = cmd
	winMu.Unlock()
	go func(c *exec.Cmd) {
		_ = c.Wait()
		winMu.Lock()
		if winCmd == c {
			winCmd = nil
		}
		winMu.Unlock()
	}(cmd)
	return nil
}

// closeWindow 退出时顺手关掉界面窗口（避免留下一个「连接中断」的白窗）。
func closeWindow() {
	winMu.Lock()
	cmd := winCmd
	winCmd = nil
	winMu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// messageBox 弹一个 Win32 消息框（windowsgui 模式下没有控制台可看）。
func messageBox(title, text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")
	t, err1 := syscall.UTF16PtrFromString(title)
	m, err2 := syscall.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		return
	}
	const mbIconInformation = 0x00000040
	_, _, _ = proc.Call(0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		mbIconInformation)
}
