package eu.tjena.tjena_bridge

import java.math.BigInteger
import java.nio.ByteBuffer
import java.security.AlgorithmParameters
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.SecureRandom
import java.security.interfaces.ECPublicKey
import java.security.spec.ECFieldFp
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPrivateKeySpec
import java.security.spec.ECPublicKeySpec
import java.util.Base64
import javax.crypto.KeyAgreement
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/**
 * Moving without a camera: the two devices agree on the transfer key with
 * ECDH (P-256) through the gateway, and the user compares a 6-digit number on
 * both screens. Same as docker/manager/kex.go (protocol described there);
 * MoveKexTest checks both give the same bytes.
 */
object MoveKex {
    const val PROOF_HELLO = "tjena-bridge move hello"
    const val PROOF_IMPORTED = "tjena-bridge move imported"

    class Keys(val priv: PrivateKey, val pub: ByteArray)

    class Result(val key: ByteArray, val code: String)

    private val b64enc = Base64.getUrlEncoder().withoutPadding()
    private val b64dec = Base64.getUrlDecoder()

    fun b64(b: ByteArray): String = b64enc.encodeToString(b)
    fun unb64(s: String): ByteArray = b64dec.decode(s.trimEnd('='))

    private val params: ECParameterSpec by lazy {
        AlgorithmParameters.getInstance("EC").apply { init(ECGenParameterSpec("secp256r1")) }
            .getParameterSpec(ECParameterSpec::class.java)
    }

    fun generate(): Keys {
        val g = KeyPairGenerator.getInstance("EC")
        g.initialize(ECGenParameterSpec("secp256r1"), SecureRandom())
        val kp = g.generateKeyPair()
        return Keys(kp.private, encode(kp.public as ECPublicKey))
    }

    /** For tests: keys from a fixed scalar. */
    fun fromScalar(d: ByteArray, pub: ByteArray): Keys =
        Keys(KeyFactory.getInstance("EC").generatePrivate(ECPrivateKeySpec(BigInteger(1, d), params)), pub)

    fun commit(pub: ByteArray): String = b64(sha256(pub))

    fun proof(key: ByteArray, label: String): String {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(key, "HmacSHA256"))
        return b64(mac.doFinal(label.toByteArray()))
    }

    fun checkProof(key: ByteArray, label: String, got: String): Boolean =
        MessageDigest.isEqual(proof(key, label).toByteArray(), got.toByteArray())

    /** Key and number from our keys and the peer's public key; [oldSide]: we send the bridge. */
    fun derive(ours: Keys, peer: ByteArray, oldSide: Boolean): Result {
        val ka = KeyAgreement.getInstance("ECDH")
        ka.init(ours.priv)
        ka.doPhase(decode(peer), true)
        val shared = ka.generateSecret()
        val (oldPub, newPub) = if (oldSide) ours.pub to peer else peer to ours.pub
        val salt = sha256(oldPub + newPub)
        val key = hkdf(shared, salt, "tjena-bridge move key", 32)
        val n = ByteBuffer.wrap(hkdf(shared, salt, "tjena-bridge move sas", 4)).int.toLong() and 0xffffffffL
        return Result(key, "%06d".format(n % 1_000_000))
    }

    private fun encode(pub: ECPublicKey): ByteArray =
        byteArrayOf(4) + fixed32(pub.w.affineX) + fixed32(pub.w.affineY)

    private fun fixed32(v: BigInteger): ByteArray {
        val b = v.toByteArray()
        return when {
            b.size == 32 -> b
            b.size > 32 -> b.copyOfRange(b.size - 32, b.size)
            else -> ByteArray(32 - b.size) + b
        }
    }

    private fun decode(raw: ByteArray): java.security.PublicKey {
        require(raw.size == 65 && raw[0] == 4.toByte()) { "bad key from the other device" }
        val x = BigInteger(1, raw.copyOfRange(1, 33))
        val y = BigInteger(1, raw.copyOfRange(33, 65))
        // On the curve? y² = x³ + ax + b (mod p)
        val p = (params.curve.field as ECFieldFp).p
        require(x < p && y < p) { "bad key from the other device" }
        val rhs = x.pow(3).add(params.curve.a.multiply(x)).add(params.curve.b).mod(p)
        require(y.modPow(BigInteger.valueOf(2), p) == rhs) { "bad key from the other device" }
        return KeyFactory.getInstance("EC").generatePublic(ECPublicKeySpec(ECPoint(x, y), params))
    }

    private fun sha256(b: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(b)

    private fun hkdf(ikm: ByteArray, salt: ByteArray, info: String, len: Int): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(salt, "HmacSHA256"))
        val prk = mac.doFinal(ikm)
        mac.init(SecretKeySpec(prk, "HmacSHA256"))
        val out = java.io.ByteArrayOutputStream()
        var t = ByteArray(0)
        var i = 1
        while (out.size() < len) {
            mac.update(t)
            mac.update(info.toByteArray())
            mac.update(i.toByte())
            t = mac.doFinal()
            out.write(t)
            i++
        }
        return out.toByteArray().copyOf(len)
    }
}
