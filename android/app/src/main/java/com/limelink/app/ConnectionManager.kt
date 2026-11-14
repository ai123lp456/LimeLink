package com.limelink.app

import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 连接状态枚举：全 APP 唯一的连接状态源。
 * 主界面、常驻状态通知、内部逻辑都从 [ConnectionManager.state] 读取，保证一致。
 */
enum class ConnectionState {
    /** 未连接：无地址 / 无 token / 重连失败超限 */
    DISCONNECTED,

    /** 连接中：配对请求在途 / 等待电脑端点「允许」 */
    CONNECTING,

    /** 已连接：心跳成功，通道真实可用 */
    CONNECTED,

    /** 重连中：心跳超时 / 通道关闭，指数退避中 */
    RECONNECTING,

    /** 用户手动暂停 */
    PAUSED
}

/** 连接诊断文案类型。 */
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
    PAUSED,
    WAITING_APPROVAL,
    DENIED,
    TOKEN_EXPIRED
}

/** 连接（配对）结果。 */
data class PairResult(val ok: Boolean, val message: String = "")

/**
 * 唯一连接状态机（进程内单例）。
 *
 * 职责：
 * - 持有唯一 [state]，所有 UI 从这里读；
 * - 两种配对方式：免码请求（电脑端界面点允许，v1.3.0）/ 6 位配对码（兼容）；
 * - 配对（换 token）与心跳（真实通道验证）分离，配对成功绝不直接标 CONNECTED；
 * - 独立心跳协程：5s 间隔、15s 超时，顺带取回电脑端发来的文字；
 * - 断线后指数退避重连（1s→2s→4s…上限 30s），失败超 3 次转 DISCONNECTED；
 * - 断线时通过 UDP 广播重新发现电脑端 IP，发现后自动更新 host。
 */
object ConnectionManager {

    private const val TAG = "ConnectionManager"

    // 心跳参数
    private const val HEARTBEAT_INTERVAL_MS = 5_000L
    private const val HEARTBEAT_TIMEOUT_MS = 15_000L
    private const val RECONNECT_MAX_FAIL = 3
    private const val RECONNECT_BACKOFF_MAX_MS = 30_000L
    private const val DISCOVER_TIMEOUT_MS = 2_000L
    private const val APPROVAL_WAIT_MS = 90_000L
    private const val APPROVAL_POLL_MS = 1_200L

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    private val jsonMedia = "application/json; charset=utf-8".toMediaType()

    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(HEARTBEAT_TIMEOUT_MS, TimeUnit.MILLISECONDS)
        .writeTimeout(5, TimeUnit.SECONDS)
        .build()

    @Volatile
    private var _state: ConnectionState = ConnectionState.DISCONNECTED
    val state: ConnectionState get() = _state

    private val listeners = mutableListOf<(ConnectionState) -> Unit>()

    @Volatile
    private var _diagnose: String = ""
    val diagnose: String get() = _diagnose

    private var heartbeatJob: Job? = null
    private var connectJob: Job? = null

    private val running = AtomicBoolean(false)

    /** 最近一次成功心跳时电脑端设备名。 */
    @Volatile
    var pcName: String = ""
        private set

    fun addListener(l: (ConnectionState) -> Unit) {
        synchronized(listeners) { listeners.add(l) }
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
                listeners.toList().forEach { it(newState) }
            }
        }
    }

    private fun setDiagnose(msg: String) {
        _diagnose = msg
    }

    /** 启动/刷新连接引擎。 */
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
        if (heartbeatJob == null || heartbeatJob?.isActive != true) {
            heartbeatJob = scope.launch { heartbeatLoop(settings) }
        }
    }

    fun pause() {
        setState(ConnectionState.PAUSED)
        setDiagnose(diagnoseText(DiagnoseStep.PAUSED))
        stopHeartbeat()
        connectJob?.cancel()
        connectJob = null
    }

    fun resume(settings: Settings) {
        start(settings)
    }

    /** 推送失败等辅助信号触发的一次立即重检。 */
    fun requestRecheck(settings: Settings? = null) {
        scope.launch {
            val s = settings ?: return@launch
            if (_state != ConnectionState.CONNECTED) return@launch
            if (doHeartbeat(s) != Hb.OK) {
                setState(ConnectionState.RECONNECTING)
                setDiagnose(diagnoseText(DiagnoseStep.RECONNECTING))
            }
        }
    }

    private fun stopHeartbeat() {
        heartbeatJob?.cancel()
        heartbeatJob = null
    }

    // ---------------------------------------------------------------- 配对

    /**
     * 免码连接（v1.3.0 主路径）：向电脑端发「请求连接」，
     * 电脑端界面弹卡片点「允许」后拿到 token；已经配对过则直接放行。
     */
    fun connectAsync(settings: Settings, dev: PcDevice, onResult: (PairResult) -> Unit) {
        connectJob?.cancel()
        connectJob = scope.launch {
            val result = connectBlocking(settings, dev)
            withContext(Dispatchers.Main) { onResult(result) }
        }
    }

    private suspend fun connectBlocking(settings: Settings, dev: PcDevice): PairResult {
        if (!settings.isSyncEnabled()) return PairResult(false, "同步已暂停，先打开总开关")
        settings.host = dev.host
        settings.port = dev.port
        setState(ConnectionState.CONNECTING)
        setDiagnose("正在联系「${dev.name}」…")

        val body = JSONObject().apply {
            put("name", settings.deviceName)
            put("device_id", settings.deviceId)
        }
        return try {
            val first = postJson("http://${dev.host}:${dev.port}/pair/request", body)
            when (first.optString("status")) {
                "approved" -> {
                    saveToken(settings, first)
                    verifyHeartbeat(settings)
                    if (_state == ConnectionState.CONNECTED) PairResult(true)
                    else PairResult(false, diagnoseText(DiagnoseStep.PAIRED_BUT_HEARTBEAT_FAIL))
                }
                "pending" -> waitApproval(settings, dev, first.optString("req_id"))
                else -> {
                    setState(ConnectionState.DISCONNECTED)
                    setDiagnose("电脑端没有正确响应，可能是旧版本")
                    PairResult(false, "电脑端没有正确响应，试试用配对码连接")
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "免码连接异常: ${e.message}")
            setState(ConnectionState.DISCONNECTED)
            setDiagnose(diagnoseText(DiagnoseStep.PORT_NO_RESPONSE))
            PairResult(false, "连不上电脑端：确认电脑端已启动、手机和电脑在同一网络")
        }
    }

    /** 等待电脑端在界面上点「允许」。 */
    private suspend fun waitApproval(settings: Settings, dev: PcDevice, reqId: String): PairResult {
        if (reqId.isEmpty()) {
            setState(ConnectionState.DISCONNECTED)
            return PairResult(false, "电脑端没有返回请求编号")
        }
        setState(ConnectionState.CONNECTING)
        setDiagnose(diagnoseText(DiagnoseStep.WAITING_APPROVAL))
        val deadline = System.currentTimeMillis() + APPROVAL_WAIT_MS
        while (System.currentTimeMillis() < deadline) {
            delay(APPROVAL_POLL_MS)
            val st = try {
                getJson("http://${dev.host}:${dev.port}/pair/status?id=$reqId")
            } catch (e: Exception) {
                continue
            }
            when (st.optString("status")) {
                "approved" -> {
                    saveToken(settings, st)
                    verifyHeartbeat(settings)
                    return if (_state == ConnectionState.CONNECTED) PairResult(true)
                    else PairResult(false, diagnoseText(DiagnoseStep.PAIRED_BUT_HEARTBEAT_FAIL))
                }
                "denied" -> {
                    setState(ConnectionState.DISCONNECTED)
                    setDiagnose(diagnoseText(DiagnoseStep.DENIED))
                    return PairResult(false, "电脑端拒绝了这次连接")
                }
                "expired" -> {
                    setState(ConnectionState.DISCONNECTED)
                    setDiagnose("连接请求已过期")
                    return PairResult(false, "请求已过期，请重试")
                }
            }
        }
        setState(ConnectionState.DISCONNECTED)
        setDiagnose("电脑端没有确认，请到电脑端界面点「允许」，或改用配对码")
        return PairResult(false, "等待超时：到电脑端界面点「允许」，或用配对码连接")
    }

    /**
     * 配对码配对（兼容路径）：只换 token，不改变连接状态。
     * 成功后立即心跳验证，通过才算 CONNECTED。
     */
    suspend fun pair(settings: Settings, host: String, port: Int, code: String): Boolean {
        settings.host = host
        settings.port = port
        setState(ConnectionState.CONNECTING)
        setDiagnose("正在连接电脑端…")

        return try {
            val body = JSONObject().apply {
                put("code", code)
                put("name", settings.deviceName)
                put("device_id", settings.deviceId)
            }
            val obj = postJson("http://$host:$port/pair", body)
            if (obj.optBoolean("ok", false) && obj.optString("token").isNotEmpty()) {
                saveToken(settings, obj)
                verifyHeartbeat(settings)
                _state == ConnectionState.CONNECTED
            } else {
                val err = obj.optString("error", "")
                if (err.contains("配对码")) {
                    setDiagnose(diagnoseText(DiagnoseStep.FOUND_PC_BUT_WRONG_CODE))
                } else {
                    setDiagnose(if (err.isNotEmpty()) err else diagnoseText(DiagnoseStep.PORT_NO_RESPONSE))
                }
                setState(ConnectionState.DISCONNECTED)
                false
            }
        } catch (e: Exception) {
            Log.w(TAG, "配对异常: ${e.message}")
            setDiagnose(diagnoseText(DiagnoseStep.PORT_NO_RESPONSE))
            setState(ConnectionState.DISCONNECTED)
            false
        }
    }

    private fun saveToken(settings: Settings, obj: JSONObject) {
        val token = obj.optString("token")
        if (token.isNotEmpty()) settings.token = token
        val nm = obj.optString("name")
        if (nm.isNotEmpty()) pcName = nm
    }

    private suspend fun verifyHeartbeat(settings: Settings) {
        when (doHeartbeat(settings)) {
            Hb.OK -> {
                setState(ConnectionState.CONNECTED)
                setDiagnose(diagnoseText(DiagnoseStep.RECONNECTED))
            }
            Hb.UNAUTHORIZED -> {
                settings.token = null
                setState(ConnectionState.DISCONNECTED)
                setDiagnose(diagnoseText(DiagnoseStep.TOKEN_EXPIRED))
            }
            Hb.FAIL -> {
                setDiagnose(diagnoseText(DiagnoseStep.PAIRED_BUT_HEARTBEAT_FAIL))
                setState(ConnectionState.RECONNECTING)
            }
        }
    }

    // ---------------------------------------------------------------- 心跳

    private suspend fun heartbeatLoop(settings: Settings) {
        var consecutiveFail = 0
        var backoff = 1_000L
        while (true) {
            if (!settings.isSyncEnabled()) {
                setState(ConnectionState.PAUSED)
                return
            }
            if (settings.host.isNullOrEmpty() || settings.token.isNullOrEmpty()) {
                setState(ConnectionState.DISCONNECTED)
                return
            }
            when (doHeartbeat(settings)) {
                Hb.OK -> {
                    consecutiveFail = 0
                    backoff = 1_000L
                    if (_state != ConnectionState.CONNECTED) {
                        setState(ConnectionState.CONNECTED)
                        setDiagnose(diagnoseText(DiagnoseStep.RECONNECTED))
                    }
                    delay(HEARTBEAT_INTERVAL_MS)
                    continue
                }
                Hb.UNAUTHORIZED -> {
                    settings.token = null
                    setState(ConnectionState.DISCONNECTED)
                    setDiagnose(diagnoseText(DiagnoseStep.TOKEN_EXPIRED))
                    return
                }
                Hb.FAIL -> Unit
            }
            consecutiveFail++
            if (_state == ConnectionState.CONNECTED) {
                setState(ConnectionState.RECONNECTING)
                setDiagnose(diagnoseText(DiagnoseStep.RECONNECTING))
            }
            if (consecutiveFail >= RECONNECT_MAX_FAIL) {
                if (tryRediscover(settings)) {
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
            delay(backoff)
            backoff = (backoff * 2).coerceAtMost(RECONNECT_BACKOFF_MAX_MS)
        }
    }

    private enum class Hb { OK, UNAUTHORIZED, FAIL }

    /** 单次心跳；顺带取回电脑端发来的文字并弹通知。 */
    private suspend fun doHeartbeat(settings: Settings): Hb {
        val host = settings.host ?: return Hb.FAIL
        val port = settings.port
        val token = settings.token ?: return Hb.FAIL
        return try {
            val req = Request.Builder()
                .url("http://$host:$port/heartbeat")
                .header("X-LimeLink-Token", token)
                .get()
                .build()
            client.newCall(req).execute().use { resp ->
                if (resp.code == 401) return Hb.UNAUTHORIZED
                if (!resp.isSuccessful) return Hb.FAIL
                val text = resp.body?.string() ?: ""
                val obj = if (text.isNotEmpty()) JSONObject(text) else JSONObject()
                if (!obj.optBoolean("ok", false)) return Hb.FAIL
                val nm = obj.optString("name", "")
                if (nm.isNotEmpty()) pcName = nm
                handleIncoming(obj)
                Hb.OK
            }
        } catch (e: Exception) {
            Hb.FAIL
        }
    }

    /** 处理心跳里夹带的「电脑端 → 手机」消息。 */
    private fun handleIncoming(obj: JSONObject) {
        val arr = obj.optJSONArray("pending") ?: return
        val ctx = AndroidApp.instance ?: return
        for (i in 0 until arr.length()) {
            val m = arr.optJSONObject(i) ?: continue
            val text = m.optString("text")
            if (text.isNotEmpty()) MessageNotifier.show(ctx, text)
        }
    }

    private suspend fun tryRediscover(settings: Settings): Boolean {
        val list = Discovery.scan(settings.deviceId, DISCOVER_TIMEOUT_MS)
        val found = list.firstOrNull()
        if (found != null) {
            settings.host = found.host
            settings.port = found.port
            setDiagnose(diagnoseText(DiagnoseStep.REDISCOVERED))
            return true
        }
        return false
    }

    // ---------------------------------------------------------------- 网络

    private fun postJson(url: String, body: JSONObject): JSONObject {
        val req = Request.Builder()
            .url(url)
            .post(body.toString().toRequestBody(jsonMedia))
            .build()
        client.newCall(req).execute().use { resp ->
            val text = resp.body?.string() ?: ""
            return if (text.isNotEmpty()) {
                JSONObject(text)
            } else {
                JSONObject().put("ok", resp.isSuccessful)
            }
        }
    }

    private fun getJson(url: String): JSONObject {
        val req = Request.Builder().url(url).get().build()
        client.newCall(req).execute().use { resp ->
            val text = resp.body?.string() ?: ""
            return if (text.isNotEmpty()) JSONObject(text) else JSONObject()
        }
    }

    /** 诊断文案清单。 */
    fun diagnoseText(step: DiagnoseStep): String = when (step) {
        DiagnoseStep.FOUND_PC_BUT_WRONG_CODE -> "已找到电脑端，但配对码错误，请核对电脑端显示的 6 位码"
        DiagnoseStep.PAIRED_BUT_HEARTBEAT_FAIL -> "配对成功但无法通信，可能是 Windows 防火墙拦了 8787/8788"
        DiagnoseStep.IP_CHANGED_REDISCOVERING -> "电脑端地址变了，正在自动重新发现…"
        DiagnoseStep.REDISCOVERED -> "已重新找到电脑端，正在恢复连接"
        DiagnoseStep.PORT_NO_RESPONSE -> "找不到电脑端：确认电脑端已启动、手机和电脑在同一网络"
        DiagnoseStep.DIFFERENT_SUBNET -> "当前网络与电脑不在同一网段，请连同一个 Wi-Fi 或热点"
        DiagnoseStep.RECONNECTING -> "与电脑端连接中断，正在重连…"
        DiagnoseStep.RECONNECTED -> "已连接电脑端"
        DiagnoseStep.GIVE_UP -> "重连失败，已停止尝试。检查电脑端是否启动、是否同一网络"
        DiagnoseStep.PAUSED -> "已暂停同步"
        DiagnoseStep.WAITING_APPROVAL -> "已发出请求，请在电脑端界面点「允许」"
        DiagnoseStep.DENIED -> "电脑端拒绝了这次连接"
        DiagnoseStep.TOKEN_EXPIRED -> "电脑端已重置配对，请重新连接"
    }
}
