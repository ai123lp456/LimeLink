package server

import "os/exec"

// openFolder 用资源管理器打开目录（仅 Windows 目标）。
func openFolder(dir string) error {
	return exec.Command("explorer", dir).Start()
}
