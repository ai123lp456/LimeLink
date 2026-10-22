package com.limelink.app

import android.util.Log
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import org.json.JSONObject

/**
 * UDP 广播发现客户端：向局域网广播探测包（8788 端口），
 * 电脑端回一个单播应答，携带电脑端 IP + 设备名 + HTTP 端口 + 版本号。
 *
 * 用于断线后自动重新发现电脑端 IP，不依赖具体网段假设。
 */
object Discovery {

    private const val TAG = "Discovery"
    private const val PROBE_MAGIC = "LIMELINK-DISCOVER"
    private const val DISCOVERY_PORT = 8788

    /**
     * 发送 UDP 广播并等待电脑端应答。
     * @param timeoutMs 等待时长（毫秒）
     * @return 电脑端 HTTP 服务 IP（未发现返回 null）
     */
    suspend fun discover(timeoutMs: Long): String? = withContext(Dispatchers.IO) {
        withTimeoutOrNull(timeoutMs) {
            var socket: DatagramSocket? = null
            try {
                socket = DatagramSocket()
                socket.broadcast = true
                socket.soTimeout = 1000 // 每次收包最多等 1s

                val data = PROBE_MAGIC.toByteArray(Charsets.UTF_8)
                // 定向广播到 255.255.255.255:8788
                val packet = DatagramPacket(
                    data, data.size,
                    InetAddress.getByName("255.255.255.255"), DISCOVERY_PORT
                )
                socket.send(packet)

                // 收应答（最多循环直到整体超时）
                val buf = ByteArray(1024)
                val deadline = System.currentTimeMillis() + timeoutMs
                while (System.currentTimeMillis() < deadline) {
                    val recv = DatagramPacket(buf, buf.size)
                    try {
                        socket.receive(recv)
                    } catch (e: java.net.SocketTimeoutException) {
                        continue
                    }
                    val msg = String(recv.data, 0, recv.length, Charsets.UTF_8)
                    val obj = try { JSONObject(msg) } catch (e: Exception) { continue }
                    if (obj.optString("magic") != PROBE_MAGIC) continue
                    // 应答里的 ip 来自来源地址；port 是 HTTP 端口
                    val host = recv.address?.hostAddress
                    if (host != null) {
                        Log.i(TAG, "发现电脑端: $host 名称=${obj.optString("name")}")
                        return@withTimeoutOrNull host
                    }
                }
                null
            } catch (e: Exception) {
                Log.w(TAG, "发现异常: ${e.message}")
                null
            } finally {
                try { socket?.close() } catch (_: Exception) {}
            }
        }
    }
}