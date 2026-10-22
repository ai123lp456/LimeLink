# 更新记录

## 1.2.2（2026-10-17，电脑端）

- 电脑端图标与 Android 端统一，图标升级为 7 个尺寸的 ICO（16/24/32/48/64/128/256）。
- 将青柠图标与 PE 版本资源嵌入 `limehost.exe`（FileVersion / ProductVersion = 1.2.2）。
- `fix-firewall.ps1` 改为按端口放行（TCP 8787 / UDP 8788），不再绑定 exe 路径，目录改名或升级后无需重复运行。
- Android 端版本保持 1.2.0。

## 1.2.1（2026-10-15，电脑端）

- 修复 Windows 防火墙规则指向旧 exe 路径导致手机显示已连接但电脑收不到通知的问题。
- `/notify` 增加来源 IP、请求体大小与拒绝原因日志。
- 新增 `GET /debug/state` 排查端点。
- 同步记录仍为内存存储，进程重启后清空。

## 1.2.0（2026-10-14，主要更新）

### 连接稳定性

- 新增唯一连接状态机 `ConnectionManager`，统一 `DISCONNECTED / CONNECTING / CONNECTED / RECONNECTING / PAUSED` 状态。
- 配对与连接状态分离：配对只换取 token，心跳成功后才进入 `CONNECTED`。
- 新增 `GET /heartbeat` token 鉴权心跳端点。
- 心跳间隔 5 秒、超时 15 秒；断线后指数退避重连（1s → 2s → 4s，上限 30s），连续失败 3 次进入断开状态。
- 新增 UDP 8788 局域网发现服务，电脑 IP 变化后手机可自动重新发现。
- 电脑端 HTTP 服务增加读写超时、请求头大小限制、panic 恢复与优雅关闭。
- 连接失败时按配对码错误、心跳超时、IP 变化、端口无响应等情况给出具体提示。

### 设备命名

- `/pair` 与发现应答增加设备名交换。
- 电脑端默认名 `LimeLink-PC-XXXX`（MAC 后 4 位），Android 端默认名 `LimeLink-Phone-XXXX`（随机 4 位）。
- 两端均支持改名；空名回退默认名，超长按 32 个字符截断。

## 1.1.1（2026-10-12，Android 端）

- 修复应用自身状态通知被监听服务再次推送造成的循环。
- 推送结果未变化时不再重复刷新状态通知。

## 1.1.0（2026-10-12）

- Toast 按来源使用 Tag/Group，同一来源的新消息立即替换旧 Toast。
- 内嵌 `go-toast` 源码以支持 Tag/Group，保留原始 MIT 许可。
- Android 端新增常驻状态通知，显示未授权、等待连接、读取中、已连接、已暂停等状态，并提供暂停 / 恢复按钮。
- Android 13+ 增加 `POST_NOTIFICATIONS` 运行时权限请求。
