package ai.redline.app

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

    override fun redeem(baseUrl: String, pairingToken: String): String =
        Core.redeemPairing(baseUrl, pairingToken)
}
