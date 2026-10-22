package com.limelink.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * 状态通知上的「暂停同步 / 恢复同步」快捷操作（v1.1.0 新增，能简则简）。
 */
class StatusActionReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        val s = Settings(context)
        when (intent.action) {
            ACTION_PAUSE -> {
                s.setSyncEnabled(false)
                ConnectionManager.pause()
            }
            ACTION_RESUME -> {
                s.setSyncEnabled(true)
                ConnectionManager.resume(s)
            }
        }
        StatusNotification.refresh(context)
    }

    companion object {
        const val ACTION_PAUSE = "com.limelink.app.ACTION_PAUSE_SYNC"
        const val ACTION_RESUME = "com.limelink.app.ACTION_RESUME_SYNC"
    }
}
