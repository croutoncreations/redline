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
        let url = PairingCode.url(
            host: "macbook-pro.tail2e5d9.ts.net",
            port: 8443,
            token: "8G-y7DyZw3yxAbCdEfGhIjKlMnOpQrStUvWxYz01234"
        )
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
        let url = PairingCode.url(
            host: "mac.example.ts.net",
            port: 443,
            token: "aa--bb__cc-_dd"
        )
        let image = try #require(PairingCode.image(for: url, size: 260))
        let cgImage = try #require(
            image.cgImage(forProposedRect: nil, context: nil, hints: nil)
        )
        let decoded = try #require(decode(cgImage))
        #expect(decoded == url)
        #expect(decoded.contains("aa--bb__cc-_dd"))
    }
}
