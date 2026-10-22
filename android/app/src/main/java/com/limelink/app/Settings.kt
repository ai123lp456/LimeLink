package com.limelink.app

import android.content.Context
import android.content.SharedPreferences
import kotlin.random.Random

/**
 * 持久化设置：电脑端地址、配对 token、本机设备名、各类型开关。
 */
class Settings(context: Context) {

    private val prefs: SharedPreferences =
        context.getSharedPreferences("limelink", Context.MODE_PRIVATE)

    var host: String?
        get() = prefs.getString("host", null)
        set(v) = prefs.edit().putString("host", v).apply()

    var port: Int
        get() = prefs.getInt("port", 8787)
        set(v) = prefs.edit().putInt("port", v).apply()

    var token: String?
        get() = prefs.getString("token", null)
        set(v) = prefs.edit().putString("token", v).apply()

    /**
     * 本机（手机端）设备名。首次访问自动生成默认名「LimeLink-Phone-XXXX」。
     * 空或超长（>32）做兜底。
     */
    var deviceName: String
        get() {
            val saved = prefs.getString("device_name", null)
            if (saved.isNullOrBlank()) {
                val generated = "LimeLink-Phone-" + randomHex4()
                prefs.edit().putString("device_name", generated).apply()
                return generated
            }
            return if (saved.length > 32) saved.substring(0, 32) else saved
        }
        set(v) {
            val name = if (v.isBlank()) "LimeLink-Phone-" + randomHex4() else
                (if (v.length > 32) v.substring(0, 32) else v)
            prefs.edit().putString("device_name", name).apply()
        }

    fun isSyncEnabled(): Boolean = prefs.getBoolean("sync_enabled", true)

    fun setSyncEnabled(v: Boolean) = prefs.edit().putBoolean("sync_enabled", v).apply()

    fun isCategoryEnabled(c: Category): Boolean =
        prefs.getBoolean("cat_${c.name}", true)

    fun setCategoryEnabled(c: Category, v: Boolean) =
        prefs.edit().putBoolean("cat_${c.name}", v).apply()

    private fun randomHex4(): String {
        val chars = "0123456789ABCDEF"
        return buildString {
            repeat(4) { append(chars[Random.nextInt(16)]) }
        }
    }
}