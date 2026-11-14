// Package webui 内嵌电脑端界面（HTML/CSS/JS 全部打进 exe，离线可用）。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets
var assets embed.FS

// Handler 返回界面静态资源处理器：/ 对应 index.html。
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 界面随 exe 更新，禁用强缓存，避免升级后看到旧页面
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		if strings.HasSuffix(r.URL.Path, ".html") || r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		fileServer.ServeHTTP(w, r)
	})
}
