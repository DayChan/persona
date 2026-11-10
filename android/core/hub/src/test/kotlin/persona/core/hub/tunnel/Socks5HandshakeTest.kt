package persona.core.hub.tunnel

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.fail
import org.junit.Test
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelProblem

class Socks5HandshakeTest {
    private fun bytes(vararg values: Int) = ByteArray(values.size) { values[it].toByte() }

    private fun Socks5Handshake.Step.written(): ByteArray = (this as Socks5Handshake.Step.Write).bytes

    private fun problem(block: () -> Unit): TunnelProblem {
        try { block() } catch (error: TunnelException) { return error.problem }
        fail("No TunnelException")
        throw AssertionError()
    }

    @Test fun withoutLoginOnlyNoLoginIsOfferedThenTheConnectFollows() {
        val handshake = Socks5Handshake("192.0.2.10", 18660, null)
        assertArrayEquals(bytes(5, 1, 0), handshake.start().written())
        assertArrayEquals(bytes(5, 1, 0, 1, 192, 0, 2, 10, 0x48, 0xe4), handshake.read(bytes(5, 0)).written())
        assertEquals(Socks5Handshake.Step.Read(4 + 2), handshake.read(bytes(5, 0, 0, 1)))
        assertEquals(Socks5Handshake.Step.Done, handshake.read(bytes(0, 0, 0, 0, 0, 0)))
    }

    @Test fun aLoginIsSentWhenTheProxyPicksIt() {
        val handshake = Socks5Handshake("hub.example.com", 80, "me" to "pw")
        assertArrayEquals(bytes(5, 2, 0, 2), handshake.start().written())
        assertArrayEquals(bytes(1, 2, 'm'.code, 'e'.code, 2, 'p'.code, 'w'.code), handshake.read(bytes(5, 2)).written())
        val connect = handshake.read(bytes(1, 0)).written()
        assertArrayEquals(bytes(5, 1, 0, 3, 15) + "hub.example.com".toByteArray() + bytes(0, 80), connect)
    }

    @Test fun aNameAndAnIpv6BoundAddressAreReadToTheirEnd() {
        val named = Socks5Handshake("hub", 80, null).apply { start(); read(bytes(5, 0)) }
        assertEquals(Socks5Handshake.Step.Read(1), named.read(bytes(5, 0, 0, 3)))
        assertEquals(Socks5Handshake.Step.Read(9 + 2), named.read(bytes(9)))
        assertEquals(Socks5Handshake.Step.Done, named.read(ByteArray(11)))
        val v6 = Socks5Handshake("[::1]", 80, null).apply { start() }
        assertArrayEquals(bytes(5, 1, 0, 4) + ByteArray(15) + bytes(1, 0, 80), v6.read(bytes(5, 0)).written())
        assertEquals(Socks5Handshake.Step.Read(16 + 2), v6.read(bytes(5, 0, 0, 4)))
    }

    @Test fun onlyRealLiteralsGoAsAddressesAndNothingIsLookedUp() {
        val outOfRange = Socks5Handshake.connectRequest("300.1.1.1", 80)
        assertArrayEquals(bytes(5, 1, 0, 3, 9) + "300.1.1.1".toByteArray() + bytes(0, 80), outOfRange)
        val mapped = Socks5Handshake.connectRequest("::ffff:192.0.2.10", 80)
        assertArrayEquals(bytes(5, 1, 0, 4) + ByteArray(10) + bytes(0xff, 0xff, 192, 0, 2, 10, 0, 80), mapped)
        assertEquals(TunnelProblem.BadHost, problem { Socks5Handshake.connectRequest("hub.example.com:80", 80) })
    }

    @Test fun refusalsSayWhy() {
        assertEquals(TunnelProblem.NotSocks5, problem { Socks5Handshake("hub", 80, null).apply { start() }.read("HT".toByteArray()) })
        assertEquals(TunnelProblem.NoAcceptableMethod(loginOffered = false),
            problem { Socks5Handshake("hub", 80, null).apply { start() }.read(bytes(5, 0xff)) })
        assertEquals(TunnelProblem.NoAcceptableMethod(loginOffered = true),
            problem { Socks5Handshake("hub", 80, "me" to "pw").apply { start() }.read(bytes(5, 0xff)) })
        assertEquals(TunnelProblem.NoAcceptableMethod(loginOffered = false),
            problem { Socks5Handshake("hub", 80, null).apply { start() }.read(bytes(5, 2)) })
        assertEquals(TunnelProblem.LoginRejected,
            problem { Socks5Handshake("hub", 80, "me" to "pw").apply { start(); read(bytes(5, 2)) }.read(bytes(1, 1)) })
        assertEquals(TunnelProblem.ConnectFailed(5),
            problem { Socks5Handshake("hub", 80, null).apply { start(); read(bytes(5, 0)) }.read(bytes(5, 5, 0, 1)) })
        assertEquals(TunnelProblem.Malformed,
            problem { Socks5Handshake("hub", 80, null).apply { start(); read(bytes(5, 0)) }.read(bytes(5, 0, 0, 9)) })
        assertEquals(TunnelProblem.Malformed, problem { Socks5Handshake("hub", 80, null).apply { start() }.read(bytes(5)) })
    }

    @Test fun whatCannotBeSentIsRefusedBeforeDialling() {
        assertEquals(TunnelProblem.LoginTooLong, problem { Socks5Handshake("hub", 80, "x".repeat(256) to "") })
        assertEquals(TunnelProblem.BadHost, problem { Socks5Handshake("h".repeat(256), 80, null) })
        assertEquals(TunnelProblem.BadHost, problem { Socks5Handshake("", 80, null) })
        assertEquals(TunnelProblem.BadHost, problem { Socks5Handshake("hub", 0, null) })
    }

    @Test fun replyCodesHaveTheirRfcNames() {
        assertEquals("connection refused", Socks5Handshake.reply(5))
        assertEquals("code 42", Socks5Handshake.reply(42))
    }
}
