package com.limelink.app

import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.provider.Settings as AndroidSettings
import android.view.LayoutInflater
import android.view.View
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch

/**
 * 主界面（v1.3.1 瑞士极简改版）。
 *
 * 排版原则：无卡片、无阴影、无圆角；用字号层级、1px 细线和 14dp 内边距建立结构；
 * 卡片压到 60dp 以内，正文 12–15sp（旧版是「大框小字」的块面风格）。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var settings: Settings
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    private lateinit var tvStateTitle: TextView
    private lateinit var tvStateSub: TextView
    private lateinit var tvStatusPill: TextView
    private lateinit var dotStatus: View
    private lateinit var tvScanHint: TextView
    private lateinit var tvDeviceCard: TextView
    private lateinit var deviceList: LinearLayout

    private lateinit var etPair: EditText
    private lateinit var etHost: EditText
    private lateinit var etPort: EditText
    private lateinit var etDeviceName: EditText

    private lateinit var btnScan: TextView
    private lateinit var btnPause: TextView
    private lateinit var btnPairCode: TextView
    private lateinit var btnPairManual: TextView
    private lateinit var btnRename: TextView

    private lateinit var swSync: Switch
    private lateinit var swWechat: Switch
    private lateinit var swSms: Switch
    private lateinit var swApp: Switch
    private lateinit var swSystem: Switch

    private var devices: List<PcDevice> = emptyList()
    private var scanning = false

    private val stateListener: (ConnectionState) -> Unit = { st ->
        runOnUiThread {
            renderState(st)
            renderDevices()
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
        renderState(ConnectionManager.state)
    }

    override fun onResume() {
        super.onResume()
        ConnectionManager.addListener(stateListener)
        StatusNotification.refresh(this)
        ConnectionManager.start(settings)
        renderDeviceCard()
        renderDevices()
        scanDevices(auto = true)
    }

    override fun onPause() {
        super.onPause()
        ConnectionManager.removeListener(stateListener)
    }

    override fun onDestroy() {
        super.onDestroy()
        scope.cancel()
    }

    private fun bindViews() {
        tvStateTitle = findViewById(R.id.tvStateTitle)
        tvStateSub = findViewById(R.id.tvStateSub)
        tvStatusPill = findViewById(R.id.tvStatusPill)
        dotStatus = findViewById(R.id.dotStatus)
        tvScanHint = findViewById(R.id.tvScanHint)
        tvDeviceCard = findViewById(R.id.tvDeviceCard)
        deviceList = findViewById(R.id.deviceList)
        etPair = findViewById(R.id.etPair)
        etHost = findViewById(R.id.etHost)
        etPort = findViewById(R.id.etPort)
        etDeviceName = findViewById(R.id.etDeviceName)
        btnScan = findViewById(R.id.btnScan)
        btnPause = findViewById(R.id.btnPause)
        btnPairCode = findViewById(R.id.btnPairCode)
        btnPairManual = findViewById(R.id.btnPairManual)
        btnRename = findViewById(R.id.btnRename)
        swSync = findViewById(R.id.swSync)
        swWechat = findViewById(R.id.swWechat)
        swSms = findViewById(R.id.swSms)
        swApp = findViewById(R.id.swApp)
        swSystem = findViewById(R.id.swSystem)

        btnScan.setOnClickListener { scanDevices(auto = false) }
        btnPause.setOnClickListener { togglePause() }
        btnPairCode.setOnClickListener { pairByCode() }
        btnPairManual.setOnClickListener { connectManually() }
        btnRename.setOnClickListener { doRename() }
    }

    private fun showVersion() {
        val v = try {
            packageManager.getPackageInfo(packageName, 0).versionName
        } catch (e: Exception) {
            ""
        }
        findViewById<TextView>(R.id.tvVersion).text = "小青柠 LimeLink v$v · 电脑端 1.3.1"
    }

    private fun requestPostNotificationsIfNeeded() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(android.Manifest.permission.POST_NOTIFICATIONS), 100)
        }
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
        updatePauseButton()

        swSync.setOnCheckedChangeListener { _, v ->
            settings.setSyncEnabled(v)
            if (v) ConnectionManager.resume(settings) else ConnectionManager.pause()
            updatePauseButton()
            renderState(ConnectionManager.state)
            StatusNotification.refresh(this)
        }
        swWechat.setOnCheckedChangeListener { _, v -> settings.setCategoryEnabled(Category.WECHAT, v) }
        swSms.setOnCheckedChangeListener { _, v -> settings.setCategoryEnabled(Category.SMS, v) }
        swApp.setOnCheckedChangeListener { _, v -> settings.setCategoryEnabled(Category.APP, v) }
        swSystem.setOnCheckedChangeListener { _, v -> settings.setCategoryEnabled(Category.SYSTEM, v) }
    }

    private fun updatePauseButton() {
        btnPause.text = if (settings.isSyncEnabled()) "暂停同步" else "恢复同步"
    }

    private fun togglePause() {
        val next = !settings.isSyncEnabled()
        settings.setSyncEnabled(next)
        swSync.isChecked = next
        if (next) ConnectionManager.resume(settings) else ConnectionManager.pause()
        updatePauseButton()
        renderState(ConnectionManager.state)
        StatusNotification.refresh(this)
    }

    // ------------------------------------------------------------ 渲染

    private fun renderState(st: ConnectionState) {
        val paired = !settings.token.isNullOrEmpty()
        val reading = StatusNotification.hasListenerPermission(this)
        val title = when (st) {
            ConnectionState.CONNECTED -> "已连接电脑"
            ConnectionState.CONNECTING -> "正在连接…"
            ConnectionState.RECONNECTING -> "正在重连…"
            ConnectionState.PAUSED -> "已暂停同步"
            ConnectionState.DISCONNECTED -> if (paired) "等待重连" else "还没连上电脑"
        }
        val sub = when {
            !reading -> "还没开启通知读取权限：去系统设置里给「小青柠」打开，才能同步通知。"
            st == ConnectionState.CONNECTED -> "通知正在镜像到「${ConnectionManager.pcName.ifEmpty { "电脑" }}」"
            st == ConnectionState.PAUSED -> "总开关已关闭，打开后自动恢复。"
            ConnectionManager.diagnose.isNotEmpty() -> ConnectionManager.diagnose
            else -> "手机和电脑连同一个 Wi-Fi，上面会自动出现这台电脑。"
        }
        tvStateTitle.text = title
        tvStateSub.text = sub
        tvStatusPill.text = when (st) {
            ConnectionState.CONNECTED -> "已连接"
            ConnectionState.CONNECTING -> "连接中"
            ConnectionState.RECONNECTING -> "重连中"
            ConnectionState.PAUSED -> "已暂停"
            ConnectionState.DISCONNECTED -> if (paired) "待重连" else "未连接"
        }
        val on = st == ConnectionState.CONNECTED
        dotStatus.setBackgroundColor(
            ContextCompat.getColor(this, if (on) R.color.accent else R.color.rule)
        )
        tvStatusPill.setTextColor(
            ContextCompat.getColor(this, if (on) R.color.ink else R.color.muted)
        )
        renderDeviceCard()
    }

    private fun renderDeviceCard() {
        tvDeviceCard.text = settings.deviceName
    }

    private fun renderDevices() {
        deviceList.removeAllViews()
        if (devices.isEmpty()) {
            val tv = TextView(this).apply {
                text = "没发现电脑。确认电脑端已启动、手机和电脑在同一 Wi-Fi，然后点右上角「刷新」。"
                setTextColor(ContextCompat.getColor(this@MainActivity, R.color.muted))
                textSize = 11.5f
                setLineSpacing(6f, 1f)
                setPadding(0, dp(6), 0, dp(10))
            }
            deviceList.addView(tv, LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            ))
            return
        }
        val inflater = LayoutInflater.from(this)
        devices.forEach { d ->
            val item = inflater.inflate(R.layout.item_device, deviceList, false)
            item.findViewById<TextView>(R.id.tvDevName).text = d.name
            val meta = StringBuilder("${d.host}:${d.port}")
            if (d.version.isNotEmpty()) meta.append("  ·  v").append(d.version)
            item.findViewById<TextView>(R.id.tvDevMeta).text = meta.toString()

            val stateTag = item.findViewById<TextView>(R.id.tvDevState)
            val connected = ConnectionManager.state == ConnectionState.CONNECTED && settings.host == d.host
            val sameHost = settings.host == d.host && !settings.token.isNullOrEmpty()
            when {
                connected -> {
                    stateTag.text = "已连接"
                    stateTag.setTextColor(ContextCompat.getColor(this, R.color.green))
                }
                sameHost -> {
                    stateTag.text = "已配对"
                    stateTag.setTextColor(ContextCompat.getColor(this, R.color.muted))
                }
                else -> {
                    stateTag.text = "可连接"
                    stateTag.setTextColor(ContextCompat.getColor(this, R.color.muted))
                }
            }

            val btn = item.findViewById<TextView>(R.id.btnDevConnect)
            btn.text = if (connected) "已连接" else "连接"
            btn.alpha = if (connected) 0.4f else 1f
            btn.isEnabled = !connected
            btn.setOnClickListener { connectTo(d) }

            val hint = item.findViewById<TextView>(R.id.tvDevHint)
            hint.text = when {
                d.id.startsWith("legacy-") -> "旧版电脑端，可能要配对码"
                sameHost -> "点一下直接恢复连接"
                else -> "电脑上会弹确认，点允许即可"
            }

            deviceList.addView(item, LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT
            ))

            val divider = View(this)
            divider.setBackgroundColor(ContextCompat.getColor(this, R.color.rule))
            deviceList.addView(divider, LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, dp(1)
            ))
        }
        tvScanHint.text = ""
    }

    // ------------------------------------------------------------ 扫描与连接

    private fun scanDevices(auto: Boolean) {
        if (scanning) return
        scanning = true
        if (!auto) tvScanHint.text = "正在扫描局域网…"
        scope.launch {
            val found = Discovery.scan(settings.deviceId)
            devices = found
            scanning = false
            renderDevices()
            if (found.isEmpty()) {
                tvScanHint.text = "没找到电脑：确认电脑端已启动（托盘有小青柠图标），并且和手机在同一网络。"
            } else {
                tvScanHint.text = "发现 ${found.size} 台电脑 · 点「连接」，电脑上再点「允许」"
            }
        }
    }

    private fun connectTo(d: PcDevice) {
        if (!settings.isSyncEnabled()) {
            toast("同步已暂停，先打开总开关")
            return
        }
        etHost.setText(d.host)
        etPort.setText(d.port.toString())
        tvStateTitle.text = "正在连接…"
        tvStateSub.text = "正在联系「${d.name}」"
        ConnectionManager.connectAsync(settings, d) { result ->
            if (result.ok) {
                toast("已连接「${d.name}」")
                requestNotificationAccess()
                StatusNotification.refresh(this)
            } else {
                toast(result.message)
            }
            renderState(ConnectionManager.state)
            renderDevices()
        }
    }

    private fun pairByCode() {
        val code = etPair.text.toString().trim()
        if (code.length != 6) {
            toast("配对码是电脑端显示的 6 位数字")
            return
        }
        val host = etHost.text.toString().trim().ifEmpty {
            devices.firstOrNull()?.host ?: ""
        }
        if (host.isEmpty()) {
            toast("先扫描到电脑，或手动填写 IP 地址")
            return
        }
        val port = etPort.text.toString().trim().toIntOrNull() ?: 8787
        tvStateSub.text = "正在用配对码连接…"
        scope.launch {
            val ok = ConnectionManager.pair(settings, host, port, code)
            if (ok) {
                toast("配对成功，已连接")
                requestNotificationAccess()
                StatusNotification.refresh(this@MainActivity)
            } else {
                toast(ConnectionManager.diagnose.ifEmpty { "连接失败" })
            }
            renderState(ConnectionManager.state)
            renderDevices()
        }
    }

    private fun connectManually() {
        val host = etHost.text.toString().trim()
        if (host.isEmpty()) {
            toast("先填写电脑 IP 地址")
            return
        }
        val port = etPort.text.toString().trim().toIntOrNull() ?: 8787
        val dev = PcDevice(id = "manual-$host", name = host, host = host, port = port, version = "")
        connectTo(dev)
    }

    private fun doRename() {
        val name = etDeviceName.text.toString().trim()
        settings.deviceName = name
        etDeviceName.setText(settings.deviceName)
        renderDeviceCard()
        toast("已改名为「${settings.deviceName}」，电脑端下次刷新可见")
    }

    private fun requestNotificationAccess() {
        if (!StatusNotification.hasListenerPermission(this)) {
            startActivity(Intent(AndroidSettings.ACTION_NOTIFICATION_LISTENER_SETTINGS))
        }
    }

    private fun toast(text: String) {
        Toast.makeText(this, text, Toast.LENGTH_SHORT).show()
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()
}
