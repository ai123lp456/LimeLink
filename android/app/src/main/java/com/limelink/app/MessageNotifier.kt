package com.limelink.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.os.Build

/**
 * 电脑端 → 手机 的文字消息通知（v1.3.0）。
 *
 * 消息走心跳通道（心跳应答里带 pending 数组），手机收到后作为本地通知弹出。
 * 不占用状态通知的渠道，避免把「运行状态」和「消息」混在一起。
 */
object MessageNotifier {

    private const val CHANNEL_ID = "limelink_msg"
    private const val BASE_ID = 2000
    private var counter = 0

    fun show(context: Context, text: String) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        ensureChannel(nm)
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(context, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(context)
        }
        val notif = builder
            .setSmallIcon(android.R.drawable.ic_dialog_email)
            .setContentTitle("来自电脑 · 小青柠")
            .setContentText(text)
            .setStyle(Notification.BigTextStyle().bigText(text))
            .setAutoCancel(true)
            .build()
        try {
            nm.notify(BASE_ID + (counter++ % 5), notif)
        } catch (_: SecurityException) {
            // Android 13+ 未授予通知权限时静默跳过
        }
    }

    private fun ensureChannel(nm: NotificationManager) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        if (nm.getNotificationChannel(CHANNEL_ID) != null) return
        val ch = NotificationChannel(CHANNEL_ID, "电脑消息", NotificationManager.IMPORTANCE_DEFAULT)
        ch.description = "电脑端发到手机的文字"
        ch.setShowBadge(true)
        nm.createNotificationChannel(ch)
    }
}
