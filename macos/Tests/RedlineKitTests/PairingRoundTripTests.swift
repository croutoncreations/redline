import CoreImage
import Foundation
import Testing
@testable import RedlineKit

/// Renders a pairing QR and reads it back with a real decoder.
///
/// The generator and the phone's scanner are different implementations, so a
/// code that encodes without error can still be unreadable or carry the wrong
/// bytes. Decoding it here is the only check that covers the whole path.
@Suite("Pairing QR round trip")
struct PairingRoundTripTests {

    private func decode(_ image: CGImage) -> String? {
        let detector = CIDetector(
            ofType: CIDetectorTypeQRCode,
            context: CIContext(),
            options: [CIDetectorAccuracy: CIDetectorAccuracyHigh]
        )
        let features = detector?.features(in: CIImage(cgImage: image)) ?? []
        return (features.first as? CIQRCodeFeature)?.messageString
    }

    @Test("A rendered code decodes back to the exact pairing URL")
    func decodesBackToTheSameURL() throws {
        // A URL as the service composes it; this test is about the render.
        let url = "https://macbook-pro.tail2e5d9.ts.net:8443/pair#pairing_token=8G-y7DyZw3yxAbCdEfGhIjKlMnOpQrStUvWxYz01234"
        let image = try #require(PairingCode.image(for: url, size: 260))
        let cgImage = try #require(
            image.cgImage(forProposedRect: nil, context: nil, hints: nil)
        )
        #expect(decode(cgImage) == url)
    }

    /// A token is base64url, whose alphabet includes '-' and '_'. Those must
    /// survive the render, or the phone redeems something the desktop never
    /// issued.
    @Test("Base64url punctuation survives the render")
    func base64urlSurvives() throws {
        let url = "https://mac.example.ts.net/pair#pairing_token=aa--bb__cc-_dd"
        let image = try #require(PairingCode.image(for: url, size: 260))
        let cgImage = try #require(
            image.cgImage(forProposedRect: nil, context: nil, hints: nil)
        )
        let decoded = try #require(decode(cgImage))
        #expect(decoded == url)
        #expect(decoded.contains("aa--bb__cc-_dd"))
    }

    /// Relay fields are standard base64, percent-encoded by the service. The
    /// '+' in a key or an entitlement is the byte that was lost once already,
    /// on the phone's side; the render must not give it a second chance.
    @Test("Percent-encoded relay fields survive the render")
    func relayFieldsSurvive() throws {
        let url = "https://relay/pair#pairing_token=abc"
            + "&relay=https%3A%2F%2Frelay.example"
            + "&key=ds%2Bl3Fu%2BI5pTwmwTna7cMnK%2BP4LZulXpQz7f%2B9v5%2BE%3D"
            + "&session=YuUA-kBv0F4g4o_gYNTKYYmuY5QFzPEc9P3GXdwUBJ0"
            + "&entitlement=eyJleHAiOjF9.T1jh%2BdnP%2FigZ"
        let image = try #require(PairingCode.image(for: url, size: 320))
        let cgImage = try #require(
            image.cgImage(forProposedRect: nil, context: nil, hints: nil)
        )
        #expect(decode(cgImage) == url)
    }
}
