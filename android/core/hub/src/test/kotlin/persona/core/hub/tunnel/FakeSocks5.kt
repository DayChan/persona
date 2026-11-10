package persona.core.hub.tunnel

import java.io.DataInputStream
import java.io.IOException
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.atomic.AtomicInteger
import kotlin.concurrent.thread

/** A SOCKS5 proxy on loopback (RFC 1928, with RFC 1929's login when [login] is set) that records what it is asked for. */
internal class FakeSocks5(
    private val login: Pair<String, String>? = null,
    /** Where a CONNECT really goes; null: where it asked. */
    private val target: InetSocketAddress? = null,
    /** Answers like a web server instead. */
    private val http: Boolean = false,
) : AutoCloseable {
    /** A refusal code every CONNECT is answered with; 0 connects. */
    @Volatile var reply = 0
    private val server = ServerSocket(0, 50, InetAddress.getLoopbackAddress())
    val port: Int = server.localPort
    /** Every CONNECT's host and port, in order. */
    val asked = ConcurrentLinkedQueue<Pair<String, Int>>()
    val carried = AtomicInteger()

    init {
        thread(isDaemon = true, name = "Fake SOCKS5") {
            while (!server.isClosed) {
                val client = try { server.accept() } catch (_: IOException) { break }
                thread(isDaemon = true) { runCatching { serve(client) }; runCatching { client.close() } }
            }
        }
    }

    private fun serve(client: Socket) {
        val input = DataInputStream(client.getInputStream())
        val output = client.getOutputStream()
        if (http) {
            output.write("HTTP/1.1 400 Bad Request\r\n\r\n".toByteArray())
            output.flush()
            return
        }
        input.readUnsignedByte()
        val methods = ByteArray(input.readUnsignedByte()).also(input::readFully)
        if (login != null) {
            if (2.toByte() !in methods) { output.write(byteArrayOf(5, -1)); return }
            output.write(byteArrayOf(5, 2))
            input.readUnsignedByte()
            val user = String(ByteArray(input.readUnsignedByte()).also(input::readFully))
            val password = String(ByteArray(input.readUnsignedByte()).also(input::readFully))
            if (user to password != login) { output.write(byteArrayOf(1, 1)); return }
            output.write(byteArrayOf(1, 0))
        } else {
            output.write(byteArrayOf(5, 0))
        }
        repeat(3) { input.readUnsignedByte() }
        val host = when (input.readUnsignedByte()) {
            1 -> InetAddress.getByAddress(ByteArray(4).also(input::readFully)).hostAddress!!
            4 -> InetAddress.getByAddress(ByteArray(16).also(input::readFully)).hostAddress!!
            3 -> String(ByteArray(input.readUnsignedByte()).also(input::readFully))
            else -> return
        }
        val port = input.readUnsignedShort()
        asked += host to port
        val refused = byteArrayOf(5, reply.takeIf { it != 0 }?.toByte() ?: 5, 0, 1, 0, 0, 0, 0, 0, 0)
        if (reply != 0) { output.write(refused); return }
        val upstream = try {
            Socket().apply { connect(target ?: InetSocketAddress(host, port), 2000) }
        } catch (_: IOException) {
            output.write(refused)
            return
        }
        carried.incrementAndGet()
        // A name as the bound address: the client has to read it to its end.
        output.write(byteArrayOf(5, 0, 0, 3, 9) + "localhost".toByteArray() + byteArrayOf(0, 80))
        output.flush()
        upstream.use {
            val up = thread(isDaemon = true) { runCatching { input.copyTo(upstream.getOutputStream()); upstream.shutdownOutput() } }
            runCatching { upstream.getInputStream().copyTo(output) }
            up.join(1000)
        }
    }

    override fun close() = server.close()
}
