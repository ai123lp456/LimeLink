package com.limelink.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.graphics.drawable.Icon
import android.os.Build
import android.provider.Settings

/**
 * 常驻运行状态通知。
 *
 * 状态来源分层，全部来自真实信号：
 * - 通知读取权限：系统设置 enabled_notification_listeners（真实授权状态）
 * - 监听服务绑定：NotificationSyncService.onListenerConnected/Disconnected（真实绑定状态）
 * - 电脑连接：唯一状态机 ConnectionManager.state（v1.2.0 统一状态源）
 *
 * 渠道独立（limelink_status）、IMPORTANCE_LOW，不打扰不响铃。
 */
object StatusNotification {

    const val CHANNEL_ID = "limelink_status"
    const val NOTIFICATION_ID = 1001

    // v1.0.1 使用过的旧渠道 id，升级后清理
    private const val LEGACY_CHANNEL_ID = "limelink_sync"

    enum class State { NOT_AUTHORIZED, PAUSED, READING, WAITING_PC, CONNECTING, RECONNECTING, CONNECTED }

    /** 进程内共享：监听服务是否已绑定（独立于连接状态的真实信号）。 */
    @Volatile var listenerBound = false

    /** 通知读取权限是否已授予（以系统设置为准）。 */
    fun hasListenerPermission(context: Context): Boolean {
        val cn = ComponentName(context, NotificationSyncService::class.java)
        val flat = Settings.Secure.getString(context.contentResolver, "enabled_notification_listeners")
        return flat?.contains(cn.flattenToString()) == true
    }

    /** 依据真实信号计算当前应显示的状态。连接维度统一来自状态机。 */
    fun currentState(context: Context): State {
        if (!hasListenerPermission(context)) return State.NOT_AUTHORIZED
        val s = Settings(context)
        if (!s.isSyncEnabled()) return State.PAUSED
        // 未配对（没有电脑地址/token）：就绪但等待连接电脑
        if (s.host.isNullOrEmpty() || s.token.isNullOrEmpty()) return State.WAITING_PC
        // 权限在但监听服务未绑定（刚启动的短暂窗口）：按读取中处理
        if (!listenerBound) return State.READING
        return when (ConnectionManager.state) {
            ConnectionState.CONNECTED -> State.CONNECTED
            ConnectionState.CONNECTING -> State.CONNECTING
            ConnectionState.RECONNECTING -> State.RECONNECTING
            ConnectionState.DISCONNECTED -> State.WAITING_PC
            ConnectionState.PAUSED -> State.PAUSED
        }
    }

    /** 重算并刷新状态通知。 */
    fun refresh(context: Context) = post(context, currentState(context))

    /** 构建状态通知。 */
    fun build(context: Context, state: State): Notification {
        ensureChannel(context)
        val contentPI = PendingIntent.getActivity(
            context, 0,
            Intent(context, MainActivity::class.java)
                .setFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        val b = Notification.Builder(context, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentIntent(contentPI)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
        when (state) {
            State.NOT_AUTHORIZED ->
                b.setContentTitle("小青柠未获得通知读取权限")
                    .setContentText("点击打开小青柠重新授权")
            State.PAUSED -> {
                b.setContentTitle("小青柠已暂停同步")
                    .setContentText("点击恢复同步")
                b.addAction(action(context, "恢复同步", StatusActionReceiver.ACTION_RESUME))
            }
            State.READING -> {
                b.setContentTitle("小青柠正在读取通知")
                    .setContentText("等待向电脑端同步")
                b.addAction(action(context, "暂停同步", StatusActionReceiver.ACTION_PAUSE))
            }
            State.WAITING_PC -> {
                b.setContentTitle("小青柠已就绪，等待连接电脑")
                    .setContentText("电脑端未连接，暂不同步")
                b.addAction(action(context, "暂停同步", StatusActionReceiver.ACTION_PAUSE))
            }
            State.CONNECTING -> {
                b.setContentTitle("小青柠正在连接电脑…")
                    .setContentText("配对中")
                b.addAction(action(context, "暂停同步", StatusActionReceiver.ACTION_PAUSE))
            }
            State.RECONNECTING -> {
                b.setContentTitle("小青柠正在重连电脑…")
                    .setContentText(ConnectionManager.diagnose.ifEmpty { "连接中断" })
                b.addAction(action(context, "暂停同步", StatusActionReceiver.ACTION_PAUSE))
            }
            State.CONNECTED -> {
                b.setContentTitle("小青柠已连接电脑，正在同步")
                    .setContentText("通知读取中")
                b.addAction(action(context, "暂停同步", StatusActionReceiver.ACTION_PAUSE))
            }
        }
        return b.build()
    }

    /** 发布状态通知：前台服务存活时原地更新，服务未运行时作为普通通知显示。 */
    fun post(context: Context, state: State) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        try {
            nm.notify(NOTIFICATION_ID, build(context, state))
        } catch (_: SecurityException) {
            // Android 13+ 未授予 POST_NOTIFICATIONS 时静默跳过
        }
    }

    /** 建独立低优先级渠道；顺带清掉 v1.0.1 的旧渠道。 */
    private fun ensureChannel(context: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        val ch = NotificationChannel(CHANNEL_ID, "运行状态", NotificationManager.IMPORTANCE_LOW)
        ch.setShowBadge(false)
        nm.createNotificationChannel(ch)
        nm.deleteNotificationChannel(LEGACY_CHANNEL_ID)
    }

    /** 构建通知上的快捷操作按钮。 */
    private fun action(context: Context, label: String, action: String): Notification.Action {
        val pi = PendingIntent.getBroadcast(
            context,
            if (action == StatusActionReceiver.ACTION_PAUSE) 1 else 2,
            Intent(context, StatusActionReceiver::class.java).setAction(action),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        val iconRes = if (action == StatusActionReceiver.ACTION_PAUSE) {
            android.R.drawable.ic_media_pause
        } else {
            android.R.drawable.ic_media_play
        }
        return Notification.Action.Builder(
            Icon.createWithResource(context, iconRes), label, pi
        ).build()
    }
}
