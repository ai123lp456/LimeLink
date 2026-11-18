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
 *
 * v1.3.2 新增「判重 / 分类」层（`decide`）：
 * `onNotificationPosted()` 对「同一通知被应用反复刷新」也会回调，而 sbn.key 不变、内容只变数字。
 * 时间侦探（com.thx.apptimerchan）这类计时类应用每 5 秒刷一次常驻通知，
 * 1.3.1 会把它当成新通知逐条推送 → 电脑端逐条弹窗逐条响铃。
 * 现在的处理顺序：
 *  1. 白名单（通话 / 闹钟 / 导航 / 录屏投屏）→ 永远推送、永远响；
 *  2. 内容指纹（普通通知用原文，实时状态类先用数字归一化再比对）；
 *  3. 频率闸（同一 pkg+key 最快 20 秒一条，聊天类豁免）；
 *  4. 实时状态类（ongoing + 进度/秒表/progress 分类，或应用声明 alert-once）首次响、之后静默。
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

    /** 判重状态：key → 上一次放行的指纹与时间。加锁，监听回调可能并发。 */
    private val gateLock = Any()

    private class Seen(var fp: String, var at: Long)

    private val seen = object : LinkedHashMap<String, Seen>(64, 0.75f, true) {
        // 常驻键数量有界，避免长期运行无限增长
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, Seen>): Boolean =
            size > MAX_SEEN
    }

    /**
     * 处理一条 StatusBarNotification：按开关过滤、判重、映射、推送。
     */
    fun handle(sbn: StatusBarNotification) {
        // v1.1.1 修复：小青柠自己的常驻状态通知不同步。
        // 否则「状态通知 → 监听服务收到自己 → 推送成功 → 刷新状态通知 → 又收到自己」
        // 形成死循环，电脑端 Toast 会不停闪烁。
        if (sbn.packageName == context.packageName) return
        if (!settings.isSyncEnabled()) return
        val category = categorize(sbn)
        if (!settings.isCategoryEnabled(category)) return

        val sig = extract(sbn, category)
        val decision = decide(sig, category)
        if (!decision.push) {
            Log.d(TAG, "跳过重复 ${sig.pkg} key=${sig.key} live=${decision.live}")
            return
        }

        val n = toNotification(sbn, category, sig, decision)
        push(n)
    }

    // ---- 判重 / 分类（v1.3.2） ----

    /** 从通知里抽出的判重信号。 */
    private class Signals(
        val pkg: String,
        val key: String,
        val title: String,
        val text: String,
        val ongoing: Boolean,
        val alertOnce: Boolean,
        val progress: Boolean,
        val chronometer: Boolean,
        val categoryProgress: Boolean,
        val whitelist: Boolean,
        /** 消息类通知（msg / email / social）：永远不按「实时状态」处理，避免把多条消息合并成一行。 */
        val messageLike: Boolean
    )

    /** 判重结论：推不推、是否实时状态类、是否静音。 */
    private class Decision(val push: Boolean, val live: Boolean, val silent: Boolean)

    private fun extract(sbn: StatusBarNotification, cat: Category): Signals {
        val n = sbn.notification
        val extras = n.extras
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

        val flags = n.flags
        val ongoing = (flags and Notification.FLAG_ONGOING_EVENT) != 0 || sbn.isOngoing
        val alertOnce = (flags and Notification.FLAG_ONLY_ALERT_ONCE) != 0

        val progressMax = extras.getInt(Notification.EXTRA_PROGRESS_MAX, 0)
        val progress = progressMax > 0 && extras.getInt(Notification.EXTRA_PROGRESS, -1) >= 0
        // 用字面量而不是 Notification.EXTRA_SHOW_CHRONOMETER：该常量在不同 SDK 版本可见性不一致
        val chronometer = safeBool(extras, "android.showChronometer") ||
            safeBool(extras, "android.chronometerCountDown")

        val categoryProgress = n.category == Notification.CATEGORY_PROGRESS

        val key = sbn.key ?: "${sbn.packageName}|${sbn.id}|${sbn.tag ?: ""}"

        return Signals(
            pkg = sbn.packageName,
            key = key,
            title = title,
            text = text,
            ongoing = ongoing,
            alertOnce = alertOnce,
            progress = progress,
            chronometer = chronometer,
            categoryProgress = categoryProgress,
            whitelist = isWhitelisted(n.category),
            messageLike = isMessagingCategory(n.category)
        )
    }

    /**
     * 消息类通知（聊天 / 邮件 / 社交）永远不按「实时状态类」处理。
     * 少数应用会给这类通知设 FLAG_ONLY_ALERT_ONCE，若据此判成实时状态，
     * 同一会话的多条不同消息会被合并成一行、除首条外全部静音——这是最不能接受的退化。
     */
    private fun isMessagingCategory(category: String?): Boolean = when (category) {
        Notification.CATEGORY_MESSAGE,
        Notification.CATEGORY_EMAIL,
        Notification.CATEGORY_SOCIAL -> true
        else -> false
    }

    private fun safeBool(extras: android.os.Bundle, key: String): Boolean = try {
        extras.getBoolean(key, false)
    } catch (_: Exception) {
        false
    }

    /**
     * 白名单：通话 / 闹钟 / 导航 / 录屏投屏这类必须每次都响，不参与判重。
     * 判断只按系统 category，不按包名硬编码（避免误伤真消息）。
     */
    private fun isWhitelisted(category: String?): Boolean = when (category) {
        Notification.CATEGORY_CALL,
        Notification.CATEGORY_ALARM,
        Notification.CATEGORY_NAVIGATION,
        Notification.CATEGORY_SERVICE -> true
        else -> false
    }

    /**
     * 判重主逻辑。返回是否推送、是否实时状态类、是否静音。
     *
     * 关掉「合并计时/进度类通知」开关后直接放行，回到 1.3.1 的行为。
     */
    private fun decide(sig: Signals, cat: Category): Decision {
        // 白名单永远推送并响铃
        if (sig.whitelist) return Decision(true, false, false)

        // 用户关掉合并开关：完全不判重、不静音（= 1.3.1）
        if (!settings.isMergeLiveEnabled()) return Decision(true, false, false)

        val live = !sig.messageLike &&
            (sig.alertOnce || (sig.ongoing && (sig.progress || sig.chronometer || sig.categoryProgress)))

        // 实时状态类先用「数字归一化」后的指纹，普通通知用原文指纹。
        // 只对实时类归一化，验证码 / 金额 / 计数这类纯数字消息永远走原文比对，不会被误伤。
        val fp = if (live) Fingerprint.live(sig.title, sig.text) else Fingerprint.raw(sig.title, sig.text)
        val now = System.currentTimeMillis()

        synchronized(gateLock) {
            val prev = seen[sig.key]

            // 频率闸：同一 key 最快 20 秒一条；聊天类（微信/短信）豁免，避免漏掉密集对话
            val gateExempt = cat == Category.WECHAT || cat == Category.SMS
            if (!gateExempt && prev != null && now - prev.at < GATE_MS) {
                return Decision(false, live, true)
            }

            if (prev != null && prev.fp == fp) {
                val window = if (live) LIVE_DEDUPE_MS else NORMAL_DEDUPE_MS
                if (now - prev.at < window) {
                    // 内容没变：丢弃。实时类在窗口外仍允许一次静默刷新（下面继续）
                    return Decision(false, live, true)
                }
            }

            seen[sig.key] = Seen(fp, now)
        }

        // 实时状态类：首次响，之后的变化只做静默刷新
        if (live) {
            val firstTime = synchronized(gateLock) { !seen.containsKey(sig.key + SILENT_MARK) }
            if (firstTime) {
                synchronized(gateLock) { seen[sig.key + SILENT_MARK] = Seen("1", now) }
                return Decision(true, true, false)
            }
            return Decision(true, true, true)
        }

        return Decision(true, false, false)
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
                    // v1.3.2：判重与合并用的可选字段（老电脑端会忽略）
                    put("key", n.key)
                    put("live", n.live)
                    put("silent", n.silent)
                    put("alert_once", n.alertOnce)
                    put("ongoing", n.ongoing)
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

    private fun toNotification(
        sbn: StatusBarNotification,
        cat: Category,
        sig: Signals,
        d: Decision
    ): OutNotification {
        val hidden = sbn.notification.extras.getBoolean(Notification.EXTRA_SHOW_WHEN, true) &&
            isContentHidden(sig.title, sig.text)

        val appName = appLabel(sbn)

        val iconType = when (cat) {
            Category.WECHAT -> "wechat"
            Category.SMS -> "sms"
            Category.SYSTEM -> "system"
            Category.APP -> "app"
        }

        return OutNotification(
            id = "${sbn.key}-${sbn.postTime}",
            title = sig.title,
            text = sig.text,
            appName = appName,
            category = cat,
            iconType = iconType,
            pkg = sbn.packageName,
            hidden = hidden,
            ts = System.currentTimeMillis(),
            key = sig.key,
            live = d.live,
            silent = d.silent,
            alertOnce = sig.alertOnce,
            ongoing = sig.ongoing
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

        // ---- v1.3.2 判重参数 ----
        /** 实时状态类（计时/进度）内容不变时的最小刷新间隔：60 秒。 */
        private const val LIVE_DEDUPE_MS = 60_000L
        /** 普通通知内容完全相同的去重窗口：10 秒（再长会误伤重复的聊天内容）。 */
        private const val NORMAL_DEDUPE_MS = 10_000L
        /** 同一 pkg+key 的最快推送间隔（兜底频率闸）。 */
        private const val GATE_MS = 20_000L
        /** 判重表上限。 */
        private const val MAX_SEEN = 240
        /** 内部标记：记录「该 key 已经响过一次」。 */
        private const val SILENT_MARK = "\u0000silent"
    }
}

/**
 * 内容指纹。
 *
 * `raw`：原文（去空白）——用于普通通知，验证码 / 金额 / 计数一律保持原样。
 * `live`：在原文基础上把「时间、时长、百分比」这类会自增的数字换成占位符，
 * 用于计时 / 进度 / 秒表类通知（如「已使用10:42,下次提醒19:17」）。
 */
internal object Fingerprint {

    private val timeRe = Regex("""\d{1,2}:\d{2}(?::\d{2})?""")
    private val durationRe = Regex("""\d+\s*(分钟|分|小时|时|秒|天)""")
    private val percentRe = Regex("""\d+\s*%""")

    fun raw(title: String, text: String): String = "R|" + squash(title) + "|" + squash(text)

    fun live(title: String, text: String): String {
        val t = normalize(squash(title))
        val b = normalize(squash(text))
        return "L|$t|$b"
    }

    /** 只替换明确形态的「时间 / 时长 / 百分比」，其余数字（验证码、金额、条数）原样保留。 */
    private fun normalize(s: String): String {
        var out = s
        out = timeRe.replace(out, "#:##")
        out = durationRe.replace(out) { m -> "#" + m.groupValues[1] }
        out = percentRe.replace(out, "#%")
        return out
    }

    private fun squash(s: String): String = s.replace(Regex("""\s+"""), " ").trim()
}

/**
 * 通知分类。
 */
enum class Category {
    WECHAT, SMS, SYSTEM, APP
}

/**
 * 输出到电脑端的通知结构。
 *
 * v1.3.2 新增 key / live / silent / alertOnce / ongoing：老电脑端会忽略这些字段，
 * 老手机端不发这些字段，两边都不报错。
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
    val ts: Long,
    val key: String = "",
    val live: Boolean = false,
    val silent: Boolean = false,
    val alertOnce: Boolean = false,
    val ongoing: Boolean = false
)
