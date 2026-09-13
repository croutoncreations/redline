import CoreImage
import CoreImage.CIFilterBuiltins
import Foundation

#if canImport(AppKit)
import AppKit
#endif

/// Renders a pairing URL as a QR code.
///
/// Only renders. The URL itself comes from the service (`POST /v1/pairing`
/// answers with `pairing_url`), which composes it from its own config and
/// relay identity. This type used to build the URL too, from the trusted host
/// and the token and nothing else, and when the format grew relay fields the
/// CLI learned about them and this did not -- so every phone paired from the
/// menu bar had no relay and no way to know. One builder now; this draws what
/// it is handed.
public enum PairingCode {

    #if canImport(AppKit)
    /// Renders the QR at the requested point size.
    ///
    /// Scaled up before conversion because CoreImage emits roughly one pixel
    /// per module, and a phone camera cannot resolve that on screen.
    public static func image(for url: String, size: CGFloat) -> NSImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(url.utf8)
        // Medium correction, matching the CLI's output.
        filter.correctionLevel = "M"
        guard let output = filter.outputImage else { return nil }

        let scale = size / output.extent.width
        let scaled = output.transformed(by: CGAffineTransform(scaleX: scale, y: scale))

        let context = CIContext()
        guard let cgImage = context.createCGImage(scaled, from: scaled.extent) else { return nil }
        return NSImage(cgImage: cgImage, size: NSSize(width: size, height: size))
    }
    #endif
}
