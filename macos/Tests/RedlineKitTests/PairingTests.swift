import Foundation
import Testing
@testable import RedlineKit

/// The pairing URL and the host it names.
///
/// Worth testing on its own because a QR that encodes the wrong host fails
/// silently: the phone scans it, stores a base URL nothing answers, and the
/// failure surfaces much later as "unreachable" with no clue why.
@Suite("Pairing code")
struct PairingTests {

    @Test("Builds the pairing URL the phone already understands")
    func buildsThePairingURL() {
        let url = PairingCode.url(
            host: "macbook-pro.tail2e5d9.ts.net",
            port: 8443,
            token: "one-time-token"
        )
        #expect(url == "https://macbook-pro.tail2e5d9.ts.net:8443/pair#pairing_token=one-time-token")
    }

    /// 443 is implied, and carrying it would make the phone display and store a
    /// redundant port.
    @Test("Omits the default HTTPS port")
    func omitsTheDefaultPort() {
        let url = PairingCode.url(host: "macbook.example.ts.net", port: 443, token: "abc")
        #expect(url == "https://macbook.example.ts.net/pair#pairing_token=abc")
    }

    /// A token with URL-significant characters must survive intact, or the
    /// phone redeems a different token than the desktop issued.
    @Test("Escapes a token containing URL punctuation")
    func escapesTheToken() {
        let url = PairingCode.url(host: "mac.example.ts.net", port: 443, token: "a+b/c=d&e")
        // Byte-identical to what `redline pair --qr` emits for the same token,
        // so the two surfaces cannot drift into producing different codes.
        #expect(url == "https://mac.example.ts.net/pair#pairing_token=a%2Bb%2Fc%3Dd%26e")
    }

    /// The host has to come from configuration rather than being guessed. A
    /// pairing code aimed at localhost looks fine on screen and is useless on a
    /// phone, which is the failure this prevents.
    @Test("Reads the trusted host from configuration")
    func readsTheTrustedHost() {
        let yaml = """
        database: redline.db
        active_policy: standard
        api:
          trusted_hosts:
            - macbook-pro.tail2e5d9.ts.net
        """
        #expect(PairingCode.trustedHost(inConfiguration: yaml) == "macbook-pro.tail2e5d9.ts.net")
    }

    @Test("Takes the first host when several are listed")
    func takesTheFirstHost() {
        let yaml = """
        api:
          trusted_hosts:
            - first.example.ts.net
            - second.example.ts.net
        """
        #expect(PairingCode.trustedHost(inConfiguration: yaml) == "first.example.ts.net")
    }

    /// With no trusted host there is nothing to put in a QR, and saying so is
    /// better than emitting a code for an address no phone can reach.
    @Test("Reports no host rather than guessing one")
    func reportsNoHost() {
        #expect(PairingCode.trustedHost(inConfiguration: "database: redline.db") == nil)
        #expect(PairingCode.trustedHost(inConfiguration: "api:\n  trusted_hosts: []") == nil)
        #expect(PairingCode.trustedHost(inConfiguration: "") == nil)
    }

    /// Commented-out entries are not configuration.
    @Test("Ignores commented hosts")
    func ignoresCommentedHosts() {
        let yaml = """
        api:
          trusted_hosts:
            # - old.example.ts.net
            - real.example.ts.net
        """
        #expect(PairingCode.trustedHost(inConfiguration: yaml) == "real.example.ts.net")
    }

    /// A later section must not be read as more hosts.
    @Test("Stops at the end of the list")
    func stopsAtTheEndOfTheList() {
        let yaml = """
        api:
          trusted_hosts:
            - real.example.ts.net
        scheduler:
          enabled: true
        """
        #expect(PairingCode.trustedHost(inConfiguration: yaml) == "real.example.ts.net")
    }

    /// Quoted values are valid YAML and appear in hand-edited files.
    @Test("Accepts quoted hosts")
    func acceptsQuotedHosts() {
        let yaml = """
        api:
          trusted_hosts:
            - "quoted.example.ts.net"
        """
        #expect(PairingCode.trustedHost(inConfiguration: yaml) == "quoted.example.ts.net")
    }

    /// Tailscale Serve commonly fronts on 8443 rather than 443. A QR built for
    /// the wrong port produces a phone that cannot connect, with nothing on
    /// screen to explain why, so the port is read rather than assumed.
    @Test("Reads an explicit port from the trusted host")
    func readsAnExplicitPort() {
        let yaml = """
        api:
          trusted_hosts:
            - macbook.example.ts.net:8443
        """
        #expect(PairingCode.trustedHost(inConfiguration: yaml) == "macbook.example.ts.net")
        #expect(PairingCode.trustedPort(inConfiguration: yaml) == 8443)
    }

    @Test("Falls back to the Serve default when no port is given")
    func fallsBackToTheServeDefault() {
        let yaml = """
        api:
          trusted_hosts:
            - macbook.example.ts.net
        """
        #expect(PairingCode.trustedPort(inConfiguration: yaml) == 8443)
        #expect(PairingCode.trustedPort(inConfiguration: yaml, default: 443) == 443)
    }

    /// The QR has to encode without throwing for a realistic URL, and produce
    /// an image with actual pixels rather than an empty placeholder.
    @Test("Renders a QR image at the requested size")
    func rendersAnImage() throws {
        let image = try #require(
            PairingCode.image(
                for: "https://macbook-pro.tail2e5d9.ts.net:8443/pair#pairing_token=abc123",
                size: 240
            ),
            "a valid URL must produce a QR image"
        )
        #expect(image.size.width == 240)
        #expect(image.size.height == 240)
    }
}
