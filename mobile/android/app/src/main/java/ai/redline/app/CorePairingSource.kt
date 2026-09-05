package ai.redline.app

import android.util.Log
import core.Core
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** The pairing details carried by a scanned QR code. */
@Serializable
data class PairingRequest(
    @SerialName("base_url") val baseUrl: String,
    @SerialName("pairing_token") val pairingToken: String,
    // Absent from an older desktop's QR, which is why both default to empty:
    // pairing must still work, it just leaves the relay unavailable.
    @SerialName("relay_url") val relayUrl: String = "",
    @SerialName("desktop_key") val desktopKey: String = "",
    @SerialName("relay_session") val relaySession: String = "",
    @SerialName("entitlement_token") val entitlementToken: String = "",
)

/**
 * [PairingSource] backed by the Go core.
 *
 * Parsing and redeeming live in Go so the rules about what counts as a valid
 * pairing code — and the refusal to send a credential over plain HTTP to
 * anything but loopback — are written and tested once for both platforms.
 */
class CorePairingSource : PairingSource {

    override fun parsePairingUrl(raw: String): String = Core.parsePairingURL(raw)

    /**
     * Redeems directly when the code names an endpoint, and over the relay
     * otherwise -- or when direct fails and the code carried a relay.
     *
     * The relay session is built from the scanned code, not from settings:
     * nothing has been stored yet, and this is the request that earns the
     * right to store anything. It is dialled lazily and closed afterwards,
     * so a code with a direct endpoint that answers never opens a relay leg
     * it will not use.
     */
    override fun redeem(request: PairingRequest): String {
        Log.i(
            TAG,
            "redeem: base=${request.baseUrl.ifEmpty { "(none)" }} relay=${request.hasRelay} " +
                "token=${request.pairingToken.take(6)}…",
        )
        if (!request.hasRelay) {
            return Core.redeemPairing(request.baseUrl, request.pairingToken)
        }
        var relay: core.RelayClient? = null
        try {
            val fallback = object : core.RelayFallbackFull {
                private fun session(): core.RelayClient =
                    relay ?: Core.dialRelay(
                        request.relayUrl,
                        request.relaySession,
                        request.desktopKey,
                        request.entitlementToken,
                    ).also {
                        relay = it
                        Log.i(TAG, "redeem: relay session established")
                    }

                override fun do_(method: String, path: String, body: String): String =
                    session().answer(method, path, body)

                // Pairing's credential arrives as a Set-Cookie header, which
                // the compact answer discards; this is the one caller that
                // needs the full reply.
                override fun doFull(method: String, path: String, body: String): String =
                    session().answerFull(method, path, body).also {
                        // Status and size only. The envelope carries the
                        // Set-Cookie that IS the credential, and a prefix of
                        // it stayed out of the log only because another
                        // header happened to sort first.
                        Log.i(TAG, "redeem: relay carried $method $path -> ${statusOf(it)} (${it.length} chars)")
                    }
            }
            return Core.redeemPairingVia(request.baseUrl, request.pairingToken, fallback).also {
                Log.i(TAG, "redeem: credential received (${it.length} chars)")
            }
        } catch (error: Exception) {
            Log.w(TAG, "redeem failed: ${error.message}")
            throw error
        } finally {
            runCatching { relay?.close() }
        }
    }

    private companion object {
        const val TAG = "RedlineRelay"

        /** The status from a full relay envelope, without parsing the rest of it. */
        fun statusOf(envelope: String): String =
            Regex("\"status\":(\\d{3})").find(envelope)?.groupValues?.get(1) ?: "?"
    }
}

/** Whether the code carried enough to reach the desktop through a relay. */
val PairingRequest.hasRelay: Boolean
    get() = relayUrl.isNotBlank() && desktopKey.isNotBlank() && relaySession.isNotBlank()
