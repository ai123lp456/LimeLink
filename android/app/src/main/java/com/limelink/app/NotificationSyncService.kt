package com.limelink.app

import android.content.pm.ServiceInfo
import android.os.Build
import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification

/**
 * 通知监听服务：前台服务承载常驻状态通知（v1.1.0），
 * 收到系统通知后经 NotificationPusher 推送到电脑端。
 */
class NotificationSyncService : NotificationListenerService() {

    private var pusher: NotificationPusher? = null

    override fun onCreate() {
        super.onCreate()
        pusher = NotificationPusher(Settings(this), this)
    }

    override fun onNotificationPosted(sbn: StatusBarNotification?) {
        if (sbn == null) return
        pusher?.handle(sbn)
    }

    override fun onNotificationRemoved(sbn: StatusBarNotification?) {
        // 移除通知无需同步（电脑端只做弹窗预览）
    }

    override fun onListenerConnected() {
        // 系统确认监听服务已绑定 + 权限已授予，此后才允许显示「正在读取」
        StatusNotification.listenerBound = true
        startForegroundWithStatus()
        // 启动连接状态机（若已配对则启动心跳循环）
        ConnectionManager.start(Settings(this))
    }

    override fun onListenerDisconnected() {
        StatusNotification.listenerBound = false
        StatusNotification.refresh(this)
    }

    override fun onDestroy() {
        StatusNotification.listenerBound = false
        super.onDestroy()
    }

    /** 用当前真实状态启动前台服务并常驻状态通知。 */
    private fun startForegroundWithStatus() {
        val state = StatusNotification.currentState(this)
        val notif = StatusNotification.build(this, state)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(
                StatusNotification.NOTIFICATION_ID, notif,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
            )
        } else {
            @Suppress("DEPRECATION")
            startForeground(StatusNotification.NOTIFICATION_ID, notif)
        }
    }
}
