package com.limelink.app

import android.util.Log
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.NetworkInterface
import java.net.SocketTimeoutException
import java.util.Collections
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONObject

/** 一台在局域网里被发现的电脑。 */
data class PcDevice(
    val id: String,
    val name: String,
    val host: String,
    val port: Int,
    val version: String
)

/**
 * 局域网发现客户端（v1.3.0，参考 LocalSend 思路）。
 *
 * - 手机发一条 probe 广播 / 组播出去，电脑端单播回自己的名字 + 端口；
 * - 同时兼容 v1.2.x 电脑端的裸字符串探测包；
 * - 这样手机端不需要先知道电脑 IP，打开 App 就能看到「附近的电脑」。
 */
object Discovery {

    private const val TAG = "Discovery"
    const val MAGIC = "LIMELINK3"
    const val LEGACY_MAGIC = "LIMELINK-DISCOVER"
    const val PORT = 8788
    private const val MULTICAST_GROUP = "239.255.42.99"

    /**
     * 扫描局域网内的电脑。
     * @param deviceId 本机设备 id（随 probe 带上，便于电脑端识别）
     * @return 已发现的电脑（按发现顺序去重）
     */
    suspend fun scan(deviceId: String, timeoutMs: Long = 2500): List<PcDevice> =
        withContext(Dispatchers.IO) {
            val found = LinkedHashMap<String, PcDevice>()
            var socket: DatagramSocket? = null
            try {
                socket = DatagramSocket().apply {
                    broadcast = true
                    soTimeout = 400
                }
                val targets = broadcastAddrs()
                val probe = JSONObject().apply {
                    put("m", MAGIC)
                    put("t", "probe")
                    put("id", deviceId)
                    put("role", "phone")
                }.toString().toByteArray(Charsets.UTF_8)

                for (addr in targets) {
                    try {
                        socket.send(DatagramPacket(probe, probe.size, addr, PORT))
                    } catch (e: Exception) {
                        Log.d(TAG, "probe 发送失败 ${addr.hostAddress}: ${e.message}")
                    }
                }
                // v1.2.x 电脑端兼容：裸字符串探测包
                val legacy = LEGACY_MAGIC.toByteArray(Charsets.UTF_8)
                for (addr in targets) {
                    try {
                        socket.send(DatagramPacket(legacy, legacy.size, addr, PORT))
                    } catch (_: Exception) {
                    }
                }

                val buf = ByteArray(2048)
                val deadline = System.currentTimeMillis() + timeoutMs
                while (System.currentTimeMillis() < deadline) {
                    val recv = DatagramPacket(buf, buf.size)
                    try {
                        socket.receive(recv)
                    } catch (e: SocketTimeoutException) {
                        continue
                    } catch (e: Exception) {
                        Log.w(TAG, "接收失败: ${e.message}")
                        break
                    }
                    val host = recv.address?.hostAddress ?: continue
                    val text = String(recv.data, 0, recv.length, Charsets.UTF_8)
                    val dev = parseReply(text, host) ?: continue
                    found[dev.id] = dev
                }
            } catch (e: Exception) {
                Log.w(TAG, "发现异常: ${e.message}")
            } finally {
                try {
                    socket?.close()
                } catch (_: Exception) {
                }
            }
            found.values.toList()
        }

    /** 解析电脑端应答：新格式 announce，或旧格式 magic=LIMELINK-DISCOVER。 */
    private fun parseReply(text: String, host: String): PcDevice? {
        val obj = try {
            JSONObject(text)
        } catch (e: Exception) {
            return null
        }
        // v1.2.x 兼容格式
        if (obj.optString("magic") == LEGACY_MAGIC) {
            val port = obj.optInt("port", 8787)
            return PcDevice(
                id = "legacy-$host",
                name = obj.optString("name", "LimeLink-PC").ifEmpty { "LimeLink-PC" },
                host = host,
                port = port,
                version = obj.optString("version", "")
            )
        }
        if (obj.optString("m") != MAGIC) return null
        if (obj.optString("t") != "announce") return null
        if (obj.optString("role") != "pc") return null
        val port = obj.optInt("port", 8787)
        val id = obj.optString("id").ifEmpty { "pc-$host" }
        return PcDevice(
            id = id,
            name = obj.optString("name", "LimeLink-PC").ifEmpty { "LimeLink-PC" },
            host = host,
            port = port,
            version = obj.optString("ver", "")
        )
    }

    /** 广播目标：全网广播 + 组播 + 每块网卡的子网广播地址。 */
    private fun broadcastAddrs(): List<InetAddress> {
        val out = mutableListOf<InetAddress>()
        runCatching { out.add(InetAddress.getByName("255.255.255.255")) }
        runCatching { out.add(InetAddress.getByName(MULTICAST_GROUP)) }
        try {
            val nifs = Collections.list(NetworkInterface.getNetworkInterfaces())
            for (nif in nifs) {
                if (!nif.isUp || nif.isLoopback) continue
                for (ia in nif.interfaceAddresses) {
                    ia.broadcast?.let { out.add(it) }
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "枚举网卡失败: ${e.message}")
        }
        return out.distinct()
    }
}
