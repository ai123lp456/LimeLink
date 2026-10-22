package com.limelink.app

import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.provider.Settings as AndroidSettings
import android.widget.Button
import android.widget.EditText
import android.widget.Switch
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

class MainActivity : AppCompatActivity() {

    private lateinit var settings: Settings
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    private lateinit var etHost: EditText
    private lateinit var etPort: EditText
    private lateinit var etPair: EditText
    private lateinit var btnPair: Button
    private lateinit var tvStatus: TextView
    private lateinit var swSync: Switch

    private lateinit var swWechat: Switch
    private lateinit var swSms: Switch
    private lateinit var swApp: Switch
    private lateinit var swSystem: Switch

    // 设备命名（v1.2.0）
    private lateinit var tvDeviceCard: TextView
    private lateinit var etDeviceName: EditText
    private lateinit var btnRename: Button

    private val stateListener: (ConnectionState) -> Unit = { st ->
        runOnUiThread {
            tvStatus.text = statusText(st)
            renderDeviceCard()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        settings = Settings(this)
        bindViews()

        loadState()
        requestPostNotificationsIfNeeded()
        showVersion()

        btnPair.setOnClickListener { doPair() }
        btnRename.setOnClickListener { doRename() }
    }

    override fun onResume() {
        super.onResume()
        // 订阅唯一状态机
        ConnectionManager.addListener(stateListener)
        // 回到前台时用真实状态刷新常驻状态通知（覆盖「权限被关」场景）
        StatusNotification.refresh(this)
        renderDeviceCard()
        ConnectionManager.start(settings)
        if (!StatusNotification.hasListenerPermission(this) && settings.isSyncEnabled()) {
            tvStatus.text = "通知同步未开启：请在系统设置里开启小青柠的通知读取权限"
        }
    }

    override fun onPause() {
        super.onPause()
        ConnectionManager.removeListener(stateListener)
    }

    /** 「关于」位置显示版本号（读自构建配置，不会与 versionName 脱节）。 */
    private fun showVersion() {
        val v = try {
            packageManager.getPackageInfo(packageName, 0).versionName
        } catch (e: Exception) {
            ""
        }
        findViewById<TextView>(R.id.tvVersion).text = "小青柠 LimeLink v$v"
    }

    /**
     * Android 13+ 需要运行时通知权限，常驻状态通知才能显示。
     * 该权限只影响状态通知的可见性，与「通知读取权限」无关。
     */
    private fun requestPostNotificationsIfNeeded() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(android.Manifest.permission.POST_NOTIFICATIONS), 100)
        }
    }

    private fun bindViews() {
        etHost = findViewById(R.id.etHost)
        etPort = findViewById(R.id.etPort)
        etPair = findViewById(R.id.etPair)
        btnPair = findViewById(R.id.btnPair)
        tvStatus = findViewById(R.id.tvStatus)
        swSync = findViewById(R.id.swSync)
        swWechat = findViewById(R.id.swWechat)
        swSms = findViewById(R.id.swSms)
        swApp = findViewById(R.id.swApp)
        swSystem = findViewById(R.id.swSystem)
        tvDeviceCard = findViewById(R.id.tvDeviceCard)
        etDeviceName = findViewById(R.id.etDeviceName)
        btnRename = findViewById(R.id.btnRename)
    }

    private fun loadState() {
        etHost.setText(settings.host ?: "")
        etPort.setText(settings.port.toString())
        etDeviceName.setText(settings.deviceName)
        swSync.isChecked = settings.isSyncEnabled()
        swWechat.isChecked = settings.isCategoryEnabled(Category.WECHAT)
        swSms.isChecked = settings.isCategoryEnabled(Category.SMS)
        swApp.isChecked = settings.isCategoryEnabled(Category.APP)
        swSystem.isChecked = settings.isCategoryEnabled(Category.SYSTEM)

        swSync.setOnCheckedChangeListener { _, v ->
            settings.setSyncEnabled(v)
            if (v) {
                ConnectionManager.resume(settings)
            } else {
                ConnectionManager.pause()
            }
            tvStatus.text = if (v) "通知同步已开启" else "通知同步未开启"
            StatusNotification.refresh(this)
        }
        swWechat.setOnCheckedChangeListener { _, v -> settings.setCategoryEnabled(Category.WECHAT, v) }
        swSms.setOnCheckedChangeListener { _, v -> settings.setCategoryEnabled(Category.SMS, v) }
        swApp.setOnCheckedChangeListener { _, v -> settings.setCategoryEnabled(Category.APP, v) }
        swSystem.setOnCheckedChangeListener { _, v -> settings.setCategoryEnabled(Category.SYSTEM, v) }
    }

    /** 渲染设备卡片：显示对方（电脑端）名字 + 当前连接状态点。 */
    private fun renderDeviceCard() {
        val st = ConnectionManager.state
        val pcName = ConnectionManager.pcName.ifEmpty { "电脑端" }
        val dot = when (st) {
            ConnectionState.CONNECTED -> "● 已连接"
            ConnectionState.CONNECTING -> "◌ 连接中"
            ConnectionState.RECONNECTING -> "◌ 重连中"
            ConnectionState.DISCONNECTED -> "○ 未连接"
            ConnectionState.PAUSED -> "‖ 已暂停"
        }
        val own = settings.deviceName
        tvDeviceCard.text = "本机：$own\n对方：$pcName  $dot"
    }

    private fun doRename() {
        val name = etDeviceName.text.toString().trim()
        settings.deviceName = name
        renderDeviceCard()
        tvStatus.text = "已改名为「${settings.deviceName}」，下次连接时对方刷新可见"
    }

    /** 由状态机驱动的状态文案。 */
    private fun statusText(st: ConnectionState): String = when (st) {
        ConnectionState.DISCONNECTED -> ConnectionManager.diagnose.ifEmpty { "未连接" }
        ConnectionState.CONNECTING -> "正在连接电脑端…"
        ConnectionState.CONNECTED -> "已连接，开始同步"
        ConnectionState.RECONNECTING -> ConnectionManager.diagnose.ifEmpty { "正在重连…" }
        ConnectionState.PAUSED -> "已暂停同步"
    }

    private fun doPair() {
        val host = etHost.text.toString().trim()
        val port = etPort.text.toString().trim().toIntOrNull() ?: 8787
        val code = etPair.text.toString().trim()

        if (host.isEmpty()) {
            tvStatus.text = "请填写电脑端 IP 地址"
            return
        }
        if (code.isEmpty()) {
            tvStatus.text = "请填写电脑端显示的配对码"
            return
        }

        tvStatus.text = "正在连接电脑端…"
        scope.launch {
            ConnectionManager.pair(settings, host, port, code)
            runOnUiThread {
                // 状态机已通过 listener 更新 tvStatus；这里补一个权限引导
                requestNotificationAccess()
                StatusNotification.refresh(this@MainActivity)
                renderDeviceCard()
            }
        }
    }

    /**
     * 引导用户开启通知读取权限；已开启则不跳转。
     */
    private fun requestNotificationAccess() {
        if (!StatusNotification.hasListenerPermission(this)) {
            startActivity(Intent(AndroidSettings.ACTION_NOTIFICATION_LISTENER_SETTINGS))
        }
    }
}