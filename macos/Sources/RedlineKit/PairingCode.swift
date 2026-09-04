import CoreImage
import CoreImage.CIFilterBuiltins
import Foundation

#if canImport(AppKit)
import AppKit
#endif

/// Builds the pairing URL and renders it as a QR code.
///
/// The format is the one `redline pair --qr` already produces and the phone
/// already parses, so this is a second way to reach the same flow rather than a
/// second flow. Anything else would mean two pairing formats to keep in step.
public enum PairingCode {

    /// The URL encoded into the QR.
    ///
    /// The token travels in the fragment because browsers never send a fragment
    /// to a server; that matters little here, but keeping the format identical
    /// to the CLI's means one thing to maintain.
    public static func url(host: String, port: Int, token: String) -> String {
        var components = URLComponents()
        components.scheme = "https"
        components.host = host
        // 443 is implied. Carrying it would make the phone display and store a
        // redundant port.
        if port != 443 {
            components.port = port
        }
        components.path = "/pair"

        // Escaped the way Go's url.Values.Encode does for every character a
        // pairing token can contain. Tokens are base64url (A-Za-z0-9-_), and
        // both encoders leave that alphabet literal, so the two surfaces
        // produce identical codes for every token either one can issue.
        //
        // They are not identical for arbitrary text: Go writes a space as '+',
        // this writes '%20'. Both decode to the same value, and a token cannot
        // contain a space, so the difference is unreachable -- but the claim is
        // "agrees on the token alphabet", not "byte-identical for all input".
        //
        // Not URLComponents' own query encoding: it leaves '+' and '/' literal,
        // and a literal '+' decodes back as a space, so the phone would redeem
        // a different token than the desktop issued. Assigning to the
        // already-encoded `fragment` property would then escape the percent
        // signs a second time.
        components.percentEncodedFragment = "pairing_token=" + escapeQueryValue(token)

        return components.string ?? ""
    }

    /// Percent-encodes a query value the way Go's `url.QueryEscape` does.
    ///
    /// Everything outside the unreserved set is escaped, so no character in a
    /// token can change the meaning of the fragment it sits in.
    private static func escapeQueryValue(_ value: String) -> String {
        var unreserved = CharacterSet.alphanumerics
        unreserved.insert(charactersIn: "-_.~")
        return value.addingPercentEncoding(withAllowedCharacters: unreserved) ?? value
    }

    /// The host a phone should be pointed at, read from the service's own
    /// configuration.
    ///
    /// Read rather than detected: `redline pair --qr` cannot auto-detect it
    /// from inside the app bundle, because the bundled binary has no Tailscale
    /// CLI on its PATH and fails with a confusing JSON parse error. The trusted
    /// host list is the authoritative answer and is already required for the
    /// phone to be allowed to connect at all.
    ///
    /// Returns nil when none is configured, which the caller must report rather
    /// than paper over: a QR aimed at localhost looks correct on screen and is
    /// useless on a phone.
    ///
    /// A host may carry an explicit port (`name.ts.net:8443`), which is how a
    /// Tailscale Serve front end that is not on 443 is expressed.
    public static func trustedHost(inConfiguration yaml: String) -> String? {
        guard let entry = firstTrustedHostEntry(inConfiguration: yaml) else { return nil }
        // Strip any port; callers ask for the port separately.
        return entry.split(separator: ":").first.map(String.init)
    }

    /// The port a phone should connect on.
    ///
    /// Taken from the trusted host entry when it names one. Tailscale Serve
    /// commonly fronts on 8443 rather than 443, and a QR built for the wrong
    /// port produces a phone that cannot connect with nothing on screen to
    /// explain why, so this is not a good place to assume.
    public static func trustedPort(inConfiguration yaml: String, default fallback: Int = 8443) -> Int {
        guard
            let entry = firstTrustedHostEntry(inConfiguration: yaml),
            let portText = entry.split(separator: ":").dropFirst().first,
            let port = Int(portText)
        else {
            return fallback
        }
        return port
    }

    /// Finds the first entry of `api.trusted_hosts`.
    ///
    /// Deliberately narrow rather than a general YAML parser: it tracks the
    /// `api:` section and its indentation, so another section with a key of the
    /// same name cannot decide where a phone connects. That is not theoretical
    /// now that a `relay:` section exists.
    ///
    /// Line endings are split on \n only, then a trailing \r is trimmed.
    /// Splitting on `.newlines` treats CRLF as two breaks, which made a
    /// perfectly valid Windows-saved config report no host at all.
    private static func firstTrustedHostEntry(inConfiguration yaml: String) -> String? {
        var inAPISection = false
        var apiIndent = 0
        var inTrustedHosts = false
        var listIndent = 0

        for rawLine in yaml.components(separatedBy: "\n") {
            let line = stripComment(from: rawLine.replacingOccurrences(of: "\r", with: ""))
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty { continue }

            let indent = line.prefix { $0 == " " || $0 == "\t" }.count

            if inTrustedHosts {
                if trimmed.hasPrefix("- ") || trimmed == "-" {
                    let value = trimmed.dropFirst(1)
                        .trimmingCharacters(in: .whitespaces)
                        .trimmingCharacters(in: CharacterSet(charactersIn: "\"'"))
                    return value.isEmpty ? nil : value
                }
                // Any other key at or above the list's indentation ends it.
                if indent <= listIndent { inTrustedHosts = false } else { continue }
            }

            if inAPISection && indent <= apiIndent && !trimmed.hasPrefix("-") {
                // Dedented back out of api:, so anything further is a different
                // section and its trusted_hosts is not ours.
                inAPISection = false
            }

            if trimmed.hasPrefix("api:") {
                inAPISection = true
                apiIndent = indent
                continue
            }

            guard inAPISection, trimmed.hasPrefix("trusted_hosts:") else { continue }

            let remainder = trimmed
                .dropFirst("trusted_hosts:".count)
                .trimmingCharacters(in: .whitespaces)
            if remainder.hasPrefix("[") {
                // Inline flow list: take the first element, or nothing if empty.
                let inner = remainder.dropFirst().prefix { $0 != "]" }
                let first = inner
                    .split(separator: ",")
                    .first?
                    .trimmingCharacters(in: .whitespaces)
                    .trimmingCharacters(in: CharacterSet(charactersIn: "\"'"))
                return (first?.isEmpty ?? true) ? nil : first
            }
            if !remainder.isEmpty { return nil }
            inTrustedHosts = true
            listIndent = indent
        }
        return nil
    }

    /// Removes a trailing `#` comment.
    ///
    /// Only outside quotes, so a host that legitimately contains a hash inside
    /// a quoted value survives.
    private static func stripComment(from line: String) -> String {
        var inQuotes = false
        var quote: Character = " "
        for (offset, character) in line.enumerated() {
            if character == "\"" || character == "'" {
                if !inQuotes {
                    inQuotes = true
                    quote = character
                } else if character == quote {
                    inQuotes = false
                }
            }
            if character == "#" && !inQuotes {
                return String(line.prefix(offset))
            }
        }
        return line
    }

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
