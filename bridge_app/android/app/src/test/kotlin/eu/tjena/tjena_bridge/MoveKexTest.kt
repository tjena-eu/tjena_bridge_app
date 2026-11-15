package eu.tjena.tjena_bridge

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/** Same fixed values as TestKexVectors in docker/manager/move_test.go. */
class MoveKexTest {
    private val oldPub = MoveKex.unb64("BFFcPW6545a5BNP-yn9U_c0MwemXvzddylFa0KbDtANfRTa-OlDzGPv5pUdZAqIhUCvvDVfgjFOyzApW8X2fk1Q")
    private val newPub = MoveKex.unb64("BGjsfPCM1BBuQ7FN6JVCZSK9CkUVDAJ-RceVNDTXR-e64685qI677oZ5u2HnhFw6icubWjI3w_2wsFh9uvQVEY0")
    private val old = MoveKex.fromScalar(ByteArray(32) { (it + 1).toByte() }, oldPub)
    private val new = MoveKex.fromScalar(ByteArray(32) { (0x40 + it).toByte() }, newPub)

    @Test fun matchesDocker() {
        val a = MoveKex.derive(old, newPub, oldSide = true)
        val b = MoveKex.derive(new, oldPub, oldSide = false)
        assertEquals("Pa1PMMGxB6TQbx8ETL9hscqpIyeLmLz1uv5B7Bf4Bfw", MoveKex.b64(a.key))
        assertEquals("Pa1PMMGxB6TQbx8ETL9hscqpIyeLmLz1uv5B7Bf4Bfw", MoveKex.b64(b.key))
        assertEquals("197704", a.code)
        assertEquals("197704", b.code)
        assertEquals("TVsLZpfsmEbeDNdqSEwCvb6cujifVglj2cog1BY_Q7A", MoveKex.commit(newPub))
        assertEquals("8ouimbtf_3UMfTXLUaxEJVpbw4JLL7K3w8OXsPjkauQ", MoveKex.proof(a.key, MoveKex.PROOF_HELLO))
        assertEquals("-cxZvd-8SOf6zD8AC5qNJe5BivVnQKvPT1-4eTC-mHg", MoveKex.proof(a.key, MoveKex.PROOF_IMPORTED))
        assertTrue(MoveKex.checkProof(a.key, MoveKex.PROOF_IMPORTED, "-cxZvd-8SOf6zD8AC5qNJe5BivVnQKvPT1-4eTC-mHg"))
        assertFalse(MoveKex.checkProof(a.key, MoveKex.PROOF_HELLO, "-cxZvd-8SOf6zD8AC5qNJe5BivVnQKvPT1-4eTC-mHg"))
    }

    @Test fun freshKeysAgree() {
        val a = MoveKex.generate()
        val b = MoveKex.generate()
        assertEquals(65, a.pub.size)
        val x = MoveKex.derive(a, b.pub, true)
        val y = MoveKex.derive(b, a.pub, false)
        assertEquals(MoveKex.b64(x.key), MoveKex.b64(y.key))
        assertEquals(x.code, y.code)
    }

    @Test fun offCurveKeyRefused() {
        val bad = newPub.copyOf().also { it[40] = (it[40].toInt() xor 1).toByte() }
        try {
            MoveKex.derive(old, bad, true)
            fail("accepted")
        } catch (_: IllegalArgumentException) {
        }
    }
}
