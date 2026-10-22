package com.limelink.app

import android.app.Notification
import android.service.notification.StatusBarNotification
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/**
 * 负责把 Android 通知转换为统一结构并推送到电脑端 HTTP 服务。
 */
class NotificationPusher(
    private val settings: Settings,
    private val context: android.content.Context
) {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(8, TimeUnit.SECONDS)
        .writeTimeout(8, TimeUnit.SECONDS)
        .build()

    private val jsonMedia = "application/json; charset=utf-8".toMediaType()

    /**
     * 处理一条 StatusBarNotification：按开关过滤、映射、推送。
     */
    fun handle(sbn: StatusBarNotification) {
        // v1.1.1 修复：小青柠自己的常驻状态通知不同步。
        // 否则「状态通知 → 监听服务收到自己 → 推送成功 → 刷新状态通知 → 又收到自己」
        // 形成死循环，电脑端 Toast 会不停闪烁。
        if (sbn.packageName == context.packageName) return
        if (!settings.isSyncEnabled()) return
        val category = categorize(sbn)
        if (!settings.isCategoryEnabled(category)) return

        val n = toNotification(sbn, category)
        push(n)
    }

    private fun push(n: OutNotification) {
        val host = settings.host ?: return
        val port = settings.port
        val token = settings.token ?: return
        scope.launch {
            try {
                val json = JSONObject().apply {
                    put("id", n.id)
                    put("title", n.title)
                    put("text", n.text)
                    put("app_name", n.appName)
                    put("app_label", n.category.name)
                    put("icon_type", n.iconType)
                    put("pkg", n.pkg) // v1.1.0：包名，电脑端用作 Toast Tag
                    put("hidden", n.hidden)
                    put("ts", n.ts)
                }.toString()

                val body = json.toRequestBody(jsonMedia)
                val req = Request.Builder()
                    .url("http://$host:$port/notify")
                    .addHeader("X-LimeLink-Token", token)
                    .post(body)
                    .build()
                client.newCall(req).execute().use { resp ->
                    if (!resp.isSuccessful) {
                        Log.w(TAG, "推送失败 ${resp.code}: ${resp.body?.string()}")
                        // 推送失败说明通道可能已断，交由状态机心跳兜底判断
                        reportConnection(false)
                    } else {
                        reportConnection(true)
                    }
                }
            } catch (e: Exception) {
                Log.w(TAG, "推送异常: ${e.message}")
                reportConnection(false)
            }
        }
    }

    /**
     * 把真实推送结果汇入唯一状态机（v1.2.0）。
     * 推送成功/失败不再是独立连接判断源，仅作为心跳之外的辅助信号：
     * - 失败且当前 CONNECTED → 触发重连（心跳循环会进一步确认）
     * 连接状态的真伪仍由心跳（/heartbeat）决定，保证全 APP 一致。
     */
    private fun reportConnection(ok: Boolean) {
        // 推送失败且当前自认已连接：请求状态机立即重检，尽快降级重连
        if (!ok && ConnectionManager.state == ConnectionState.CONNECTED) {
            ConnectionManager.requestRecheck(settings)
        }
    }

    // ---- 映射 ----

    private fun categorize(sbn: StatusBarNotification): Category {
        val pkg = sbn.packageName
        return when {
            pkg == WECHAT_PKG -> Category.WECHAT
            pkg == SMS_PKG -> Category.SMS
            isSystemApp(sbn) -> Category.SYSTEM
            else -> Category.APP
        }
    }

    private fun toNotification(sbn: StatusBarNotification, cat: Category): OutNotification {
        val extras = sbn.notification.extras
        var title = extras.getCharSequence(Notification.EXTRA_TITLE)?.toString() ?: ""
        var text = extras.getCharSequence(Notification.EXTRA_TEXT)?.toString() ?: ""

        // 微信：如果标题为空但有大文本（bigText），尝试解析「发送者: 内容」
        if (cat == Category.WECHAT) {
            val bigText = extras.getCharSequence(Notification.EXTRA_BIG_TEXT)?.toString()
            if (!bigText.isNullOrEmpty() && (title.isEmpty() || text.isEmpty())) {
                // 微信常见格式 "发送者: 消息内容" 或 "发送者：消息内容"
                val idx = bigText.indexOfFirst { it == ':' || it == '：' }
                if (idx > 0) {
                    if (title.isEmpty()) title = bigText.substring(0, idx).trim()
                    if (text.isEmpty()) text = bigText.substring(idx + 1).trim()
                } else if (text.isEmpty()) {
                    text = bigText
                }
            }
        }

        val hidden = extras.getBoolean(Notification.EXTRA_SHOW_WHEN, true) && isContentHidden(title, text)

        val appName = appLabel(sbn)

        val iconType = when (cat) {
            Category.WECHAT -> "wechat"
            Category.SMS -> "sms"
            Category.SYSTEM -> "system"
            Category.APP -> "app"
        }

        return OutNotification(
            id = "${sbn.key}-${sbn.postTime}",
            title = title,
            text = text,
            appName = appName,
            category = cat,
            iconType = iconType,
            pkg = sbn.packageName,
            hidden = hidden,
            ts = System.currentTimeMillis()
        )
    }

    private fun appLabel(sbn: StatusBarNotification): String {
        return try {
            // 尽量使用 App 的显示名
            val pm = context.packageManager
            val appInfo = pm.getApplicationInfo(sbn.packageName, 0)
            pm.getApplicationLabel(appInfo).toString()
        } catch (e: Exception) {
            sbn.packageName
        }
    }

    private fun isSystemApp(sbn: StatusBarNotification): Boolean {
        return try {
            val pm = context.packageManager
            val appInfo = pm.getApplicationInfo(sbn.packageName, 0)
            (appInfo.flags and android.content.pm.ApplicationInfo.FLAG_SYSTEM) != 0
        } catch (e: Exception) {
            false
        }
    }

    private fun isContentHidden(title: String, text: String): Boolean {
        // 微信空壳/隐藏内容判断：标题或正文包含常见隐藏提示
        val hiddenHints = listOf("消息被隐藏", "内容已隐藏", "您收到一条", "你收到一条")
        return hiddenHints.any { title.contains(it) || text.contains(it) }
    }

    companion object {
        private const val TAG = "NotificationPusher"
        private const val WECHAT_PKG = "com.tencent.mm"
        private const val SMS_PKG = "com.android.mms"
    }
}

/**
 * 通知分类。
 */
enum class Category {
    WECHAT, SMS, SYSTEM, APP
}

/**
 * 输出到电脑端的通知结构。
 */
data class OutNotification(
    val id: String,
    val title: String,
    val text: String,
    val appName: String,
    val category: Category,
    val iconType: String,
    val pkg: String,
    val hidden: Boolean,
    val ts: Long
)