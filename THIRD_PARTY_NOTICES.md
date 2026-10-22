# 第三方组件与许可

LimeLink 以 MIT 许可证发布（见 [LICENSE](LICENSE)）。项目包含或依赖以下第三方组件，其版权与许可归各自作者所有。

## 内嵌源码

### go-toast（内嵌修改版）

- 位置：`pc/internal/toast/`
- 来源：`github.com/go-toast/toast`
- 许可证：MIT
- 版权：Copyright (c) 2016 Jacob Marshall
- 说明：项目内嵌该库的单文件源码并增加了 Toast Tag/Group 支持。原始许可全文保存在 [`pc/internal/toast/LICENSE`](pc/internal/toast/LICENSE)。

## Go 依赖

| 组件 | 用途 | 许可证 |
| --- | --- | --- |
| `github.com/getlantern/systray` | Windows 系统托盘 | Apache-2.0 |
| `github.com/gen2brain/dlgs` | 设备改名输入框 | BSD-2-Clause |
| `golang.org/x/sys` | Windows 系统调用 | BSD-3-Clause |

其余间接依赖（`github.com/getlantern/*` 等）遵循各自上游许可证，完整版本见 [pc/go.mod](pc/go.mod) 与 [pc/go.sum](pc/go.sum)。

## 许可证全文

- [Apache-2.0（systray，亦适用于 Android 侧 Apache-2.0 组件）](licenses/systray-LICENSE.txt)
- [BSD-2-Clause（dlgs）](licenses/dlgs-LICENSE.txt)
- [BSD-3-Clause（golang.org/x/sys）](licenses/golang-x-sys-LICENSE.txt)
- [MIT（内嵌 go-toast）](pc/internal/toast/LICENSE)

## Android 依赖

| 组件 | 用途 | 许可证 |
| --- | --- | --- |
| AndroidX Core、AppCompat | 基础 UI 与兼容层 | Apache-2.0 |
| Material Components for Android | 界面组件 | Apache-2.0 |
| OkHttp | HTTP 客户端 | Apache-2.0 |
| Kotlin Standard Library、kotlinx.coroutines | Kotlin 运行时与协程 | Apache-2.0 |

## 资源

`assets/icon/` 下的 LimeLink 图标为本项目资源。项目内使用的应用来源小图标（微信/短信/系统）仅用于通知来源展示，相关商标归各自权利人所有。

如你在再分发时修改了上述组件，请同时保留对应的版权声明与许可证文本。
