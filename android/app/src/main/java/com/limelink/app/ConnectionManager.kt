package com.limelink.app

import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.random.Random

/**
 * 连接状态枚举：全 APP 唯一的连接状态源。
 * 主界面、常驻状态通知、内部逻辑都从 [ConnectionManager.state] 读取，保证一致。
 */
enum class ConnectionState {
    /** 未连接：无地址 / 无 token / 重连失败超限 */
    DISCONNECTED,

    /** 连接中：配对请求在途 */
    CONNECTING,

    /** 已连接：心跳成功，通道真实可用 */
    CONNECTED,

    /** 重连中：心跳超时 / 通道关闭，指数退避中 */
    RECONNECTING,

    /** 用户手动暂停 */
    PAUSED
}

/**
 * 连接诊断文案类型。
 */
enum class DiagnoseStep {
    FOUND_PC_BUT_WRONG_CODE,
    PAIRED_BUT_HEARTBEAT_FAIL,
    IP_CHANGED_REDISCOVERING,
    REDISCOVERED,
    PORT_NO_RESPONSE,
    DIFFERENT_SUBNET,
    RECONNECTING,
    RECONNECTED,
    GIVE_UP,
    PAUSED
}

/**
 * 唯一连接状态机（进程内单例）。
 *
 * 职责：
 * - 持有唯一 [state]，所有 UI 从这里读；
 * - 配对（仅换 token）与心跳（真实通道验证）分离，配对成功绝不直接标 CONNECTED；
 * - 独立心跳协程：5s 间隔、15s 超时；
 * - 断线后指数退避重连（1s→2s→4s…上限 30s），失败超 3 次转 DISCONNECTED；
 * - 断线时通过 UDP 广播重新发现电脑端 IP（8788），发现后自动更新 host。
 */
object ConnectionManager {

    private const val TAG = "ConnectionManager"

    // 心跳参数（见需求 4.3）
    private const val HEARTBEAT_INTERVAL_MS = 5_000L  // 5s 心跳
    private const val HEARTBEAT_TIMEOUT_MS = 15_000L  // 15s 超时
    private const val RECONNECT_MAX_FAIL = 3          // 重连失败上限
    private const val RECONNECT_BACKOFF_MAX_MS = 30_000L // 退避上限 30s
    private const val CATALOG_DISCOVER_TIMEOUT_MS = 2_000L // UDP 发现等待 2s

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(HEARTBEAT_TIMEOUT_MS, TimeUnit.MILLISECONDS)
        .writeTimeout(5, TimeUnit.SECONDS)
        .build()

    // 状态机
    @Volatile
    private var _state: ConnectionState = ConnectionState.DISCONNECTED
    val state: ConnectionState get() = _state

    // 状态监听者（用于刷新 UI / 状态通知）
    private val listeners = mutableListOf<(ConnectionState) -> Unit>()

    // 上次诊断文案
    @Volatile
    private var _diagnose: String = ""
    val diagnose: String get() = _diagnose

    // 心跳循环句柄
    private var heartbeatJob: Job? = null

    // 协程安全标记：是否有配对/连接动作在途，避免重入
    private val running = AtomicBoolean(false)

    // 最近一次成功发现/心跳时电脑端设备名
    @Volatile
    var pcName: String = ""
        private set

    /**
     * 注册状态监听。回调在调用线程（通常主线程）执行。
     */
    fun addListener(l: (ConnectionState) -> Unit) {
        synchronized(listeners) { listeners.add(l) }
        // 立即回吐一次当前状态，保证新订阅者拿到最新值
        l(_state)
    }

    fun removeListener(l: (ConnectionState) -> Unit) {
        synchronized(listeners) { listeners.remove(l) }
    }

    private fun setState(newState: ConnectionState) {
        if (_state != newState) {
            _state = newState
            Log.i(TAG, "状态迁移 -> $newState")
            synchronized(listeners) {
                listeners.forEach { it(newState) }
            }
        }
    }

    private fun setDiagnose(msg: String) {
        _diagnose = msg
    }

    /**
     * 启动/刷新连接引擎。根据用户总开关与配置决定是否运行。
     * PAUSED 由 sync_enabled=false 触发；恢复后重新走连接流程。
     */
    fun start(settings: Settings) {
        if (!settings.isSyncEnabled()) {
            setState(ConnectionState.PAUSED)
            setDiagnose(diagnoseText(DiagnoseStep.PAUSED))
            return
        }
        if (settings.host.isNullOrEmpty() || settings.token.isNullOrEmpty()) {
            setState(ConnectionState.DISCONNECTED)
            return
        }
        // 确保心跳循环在跑
        if (heartbeatJob == null || heartbeatJob?.isActive != true) {
            heartbeatJob = scope.launch { heartbeatLoop(settings) }
        }
    }

    /** 用户手动暂停。 */
    fun pause() {
        setState(ConnectionState.PAUSED)
        setDiagnose(diagnoseText(DiagnoseStep.PAUSED))
        stopHeartbeat()
    }

    /**
     * 请求立即重检连接（推送失败等辅助信号触发）。
     * 在后台做一次心跳，失败就把 CONNECTED 降级为 RECONNECTING，
     * 避免等满 5s 心跳周期。
     */
    fun requestRecheck(settings: Settings? = null) {
        scope.launch {
            val s = settings ?: return@launch
            if (_state != ConnectionState.CONNECTED) return@launch
            val ok = doHeartbeat(s)
            if (!ok) {
                setState(ConnectionState.RECONNECTING)
                setDiagnose(diagnoseText(DiagnoseStep.RECONNECTING))
            }
        }
    }

    /** 用户恢复：重新走连接流程。 */
    fun resume(settings: Settings) {
        start(settings)
    }

    private fun stopHeartbeat() {
        heartbeatJob?.cancel()
        heartbeatJob = null
    }

    /**
     * 配对：只换取 token，不改变连接状态。
     * 成功后立即发起一次心跳验证（若成功才标 CONNECTED），失败则报「配对成功但无法通信」。
     * @return true 表示配对成功（token 已存），通道是否可用由后续心跳决定。
     */
    suspend fun pair(settings: Settings, host: String, port: Int, code: String): Boolean {
        settings.host = host
        settings.port = port
        setState(ConnectionState.CONNECTING)
        setDiagnose("正在连接电脑端…")

        val myName = settings.deviceName
        return try {
            val json = JSONObject().put("code", code).put("name", myName).toString()
            val body = json.toRequestBody("application/json; charset=utf-8".toMediaType())
            val req = Request.Builder()
                .url("http://$host:$port/pair")
                .post(body)
                .build()
            client.newCall(req).execute().use { resp ->
                val text = resp.body?.string() ?: ""
                val obj = if (text.isNotEmpty()) JSONObject(text) else JSONObject()
                if (resp.isSuccessful && obj.optBoolean("ok", false)) {
                    val token = obj.optString("token")
                    settings.token = token
                    // 回传电脑设备名
                    pcName = obj.optString("name", "").ifEmpty { "LimeLink-PC" }
                    // 配对成功 → 立即心跳验证（仍 CONNECTING，不标 CONNECTED）
                    verifyHeartbeat(settings)
                    true
                } else {
                    val err: String = obj.optString("error", "")
                    if (resp.code == 403 || err.contains("配对码")) {
                        setDiagnose(diagnoseText(DiagnoseStep.FOUND_PC_BUT_WRONG_CODE))
                    } else {
                        setDiagnose(if (err.isNotEmpty()) err else diagnoseText(DiagnoseStep.PORT_NO_RESPONSE))
                    }
                    setState(ConnectionState.DISCONNECTED)
                    false
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "配对异常: ${e.message}")
            setDiagnose(diagnoseText(DiagnoseStep.PORT_NO_RESPONSE))
            setState(ConnectionState.DISCONNECTED)
            false
        }
    }

    /** 配对成功后的一次心跳验证。 */
    private suspend fun verifyHeartbeat(settings: Settings) {
        val ok = doHeartbeat(settings)
        if (ok) {
            setState(ConnectionState.CONNECTED)
            setDiagnose(diagnoseText(DiagnoseStep.RECONNECTED))
        } else {
            setDiagnose(diagnoseText(DiagnoseStep.PAIRED_BUT_HEARTBEAT_FAIL))
            setState(ConnectionState.RECONNECTING)
        }
    }

    /**
     * 心跳循环：5s 一次，超时 15s。连续失败进入退避重连。
     */
    private suspend fun heartbeatLoop(settings: Settings) {
        var consecutiveFail = 0
        var backoff = 1_000L
        while (true) {
            // 用户暂停则退出循环
            if (!settings.isSyncEnabled()) {
                setState(ConnectionState.PAUSED)
                return
            }
            if (settings.host.isNullOrEmpty() || settings.token.isNullOrEmpty()) {
                setState(ConnectionState.DISCONNECTED)
                return
            }
            val ok = doHeartbeat(settings)
            if (ok) {
                consecutiveFail = 0
                backoff = 1_000L
                // 重连中恢复
                if (_state == ConnectionState.RECONNECTING || _state == ConnectionState.CONNECTING) {
                    setState(ConnectionState.CONNECTED)
                    setDiagnose(diagnoseText(DiagnoseStep.RECONNECTED))
                } else if (_state != ConnectionState.CONNECTED) {
                    setState(ConnectionState.CONNECTED)
                }
                delay(HEARTBEAT_INTERVAL_MS)
                continue
            }
            // 心跳失败
            consecutiveFail++
            if (_state == ConnectionState.CONNECTED) {
                setState(ConnectionState.RECONNECTING)
                setDiagnose(diagnoseText(DiagnoseStep.RECONNECTING))
            }
            if (consecutiveFail >= RECONNECT_MAX_FAIL) {
                // 尝试 UDP 重新发现电脑端
                val rediscovered = tryRediscover(settings)
                if (rediscovered) {
                    consecutiveFail = 0
                    backoff = 1_000L
                    setState(ConnectionState.RECONNECTING)
                    setDiagnose(diagnoseText(DiagnoseStep.IP_CHANGED_REDISCOVERING))
                    continue
                }
                setState(ConnectionState.DISCONNECTED)
                setDiagnose(diagnoseText(DiagnoseStep.GIVE_UP))
                return
            }
            // 指数退避
            delay(backoff)
            backoff = (backoff * 2).coerceAtMost(RECONNECT_BACKOFF_MAX_MS)
        }
    }

    /** 单次心跳。 */
    private suspend fun doHeartbeat(settings: Settings): Boolean {
        val host = settings.host ?: return false
        val port = settings.port
        val token = settings.token ?: return false
        return try {
            val req = Request.Builder()
                .url("http://$host:$port/heartbeat")
                .header("X-LimeLink-Token", token)
                .get()
                .build()
            client.newCall(req).execute().use { resp ->
                if (resp.isSuccessful) {
                    val text = resp.body?.string() ?: ""
                    val obj = if (text.isNotEmpty()) JSONObject(text) else JSONObject()
                    val ok = obj.optBoolean("ok", false)
                    if (ok) {
                        val nm = obj.optString("name", "")
                        if (nm.isNotEmpty()) pcName = nm
                    }
                    ok
                } else false
            }
        } catch (e: Exception) {
            false
        }
    }

    /**
     * 尝试通过 UDP 广播重新发现电脑端。发现成功后更新 host 并返回 true。
     */
    private suspend fun tryRediscover(settings: Settings): Boolean {
        val found = Discovery.discover(CATALOG_DISCOVER_TIMEOUT_MS)
        if (found != null) {
            settings.host = found
            setDiagnose(diagnoseText(DiagnoseStep.REDISCOVERED))
            return true
        }
        return false
    }

    /** 诊断文案清单（需求 4.6）。 */
    fun diagnoseText(step: DiagnoseStep): String = when (step) {
        DiagnoseStep.FOUND_PC_BUT_WRONG_CODE -> "已找到电脑端，但配对码错误，请核对电脑端显示的 6 位配对码"
        DiagnoseStep.PAIRED_BUT_HEARTBEAT_FAIL -> "配对成功但无法通信，可能是 Windows 防火墙拦截了端口 8787/8788"
        DiagnoseStep.IP_CHANGED_REDISCOVERING -> "电脑端地址已变化，正在自动重新发现电脑端…"
        DiagnoseStep.REDISCOVERED -> "已重新找到电脑端，正在恢复连接"
        DiagnoseStep.PORT_NO_RESPONSE -> "找不到电脑端，端口无响应，请确认电脑端已启动、手机与电脑在同一网络"
        DiagnoseStep.DIFFERENT_SUBNET -> "当前网络与电脑不在同一网段，请连接到同一 Wi-Fi 或热点"
        DiagnoseStep.RECONNECTING -> "与电脑端连接中断，正在重连…"
        DiagnoseStep.RECONNECTED -> "已重新连接电脑端，恢复同步"
        DiagnoseStep.GIVE_UP -> "重连失败，已停止尝试。请检查电脑端是否启动、是否同一网络"
        DiagnoseStep.PAUSED -> "已暂停同步"
    }
}