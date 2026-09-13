import Foundation
import Testing
@testable import RedlineKit

/// Fixtures below are literal JSON matching exactly what
/// `internal/config/relay_manager.go`'s `RelayStatus.MarshalJSON` emits for
/// each state: only the fields that state's case in the switch statement
/// sets, plus the always-present state/mode/connection trio and whichever
/// omitempty numeric/url fields are non-zero. No key is included the Go
/// encoder would omit for a zero value.
@Suite("RelayStatus JSON round trip")
struct RelayStatusRoundTripTests {

    @Test("active carries renews_at, seats, and connection=connected")
    func decodesActive() throws {
        let json = """
        {"state":"active","mode":"hosted","connection":"connected",
         "seats":5,"seats_used":3,"max_clients":10,
         "renews_at":"2026-10-01T00:00:00Z"}
        """
        let status = try JSONDecoder().decode(RelayStatus.self, from: Data(json.utf8))
        #expect(status.state == .active)
        #expect(status.mode == .hosted)
        #expect(status.connection == .connected)
        #expect(status.seats == 5)
        #expect(status.seatsUsed == 3)
        #expect(status.maxClients == 10)
        #expect(status.renewsAt == "2026-10-01T00:00:00Z")
        #expect(status.expiresAt == nil)
        #expect(status.since == nil)
        #expect(status.activations == nil)
        #expect(status.url == nil)
    }

    @Test("renew_pending carries expires_at, not renews_at")
    func decodesRenewPending() throws {
        let json = """
        {"state":"renew_pending","mode":"hosted","connection":"connecting",
         "expires_at":"2026-09-20T00:00:00Z"}
        """
        let status = try JSONDecoder().decode(RelayStatus.self, from: Data(json.utf8))
        #expect(status.state == .renewPending)
        #expect(status.connection == .connecting)
        #expect(status.expiresAt == "2026-09-20T00:00:00Z")
        #expect(status.renewsAt == nil)
        #expect(status.seats == nil)
    }

    /// The activations carried on a `no_seat` status are
    /// `RelayActivationSummary` on the wire -- label and first_seen only,
    /// never an id or current flag. Those two fields belong to
    /// `relay.Activation`, the shape `/v1/relay/devices` returns, which is a
    /// different Go type entirely.
    @Test("no_seat carries a bounded activations array with label and first_seen only")
    func decodesNoSeat() throws {
        let json = """
        {"state":"no_seat","mode":"hosted","connection":"disconnected",
         "activations":[
           {"label":"work-mac","first_seen":"2026-08-01T00:00:00Z"},
           {"label":"laptop","first_seen":"2026-08-15T00:00:00Z"}
         ]}
        """
        let status = try JSONDecoder().decode(RelayStatus.self, from: Data(json.utf8))
        #expect(status.state == .noSeat)
        #expect(status.connection == .disconnected)
        let activations = try #require(status.activations)
        #expect(activations.count == 2)
        #expect(activations[0].label == "work-mac")
        #expect(activations[0].firstSeen == "2026-08-01T00:00:00Z")
        // Absent on the wire for this shape; the model must default rather
        // than fail to decode.
        #expect(activations[0].id == "")
        #expect(activations[0].current == false)
        #expect(activations[1].label == "laptop")
    }

    @Test("off is minimal: no url, no seats, no per-state field")
    func decodesOff() throws {
        let json = """
        {"state":"off","mode":"off","connection":"disconnected"}
        """
        let status = try JSONDecoder().decode(RelayStatus.self, from: Data(json.utf8))
        #expect(status.state == .off)
        #expect(status.mode == .off)
        #expect(status.connection == .disconnected)
        #expect(status.url == nil)
        #expect(status.seats == nil)
        #expect(status.seatsUsed == nil)
        #expect(status.maxClients == nil)
        #expect(status.renewsAt == nil)
        #expect(status.expiresAt == nil)
        #expect(status.since == nil)
        #expect(status.activations == nil)
    }

    @Test("every RelayState raw value round trips")
    func allNineStatesDecode() throws {
        let names = [
            "off", "self_hosted", "needs_license", "active", "renew_pending",
            "unavailable", "lapsed", "no_seat", "invalid_key",
        ]
        for name in names {
            let json = #"{"state":"\#(name)","mode":"off","connection":"disconnected"}"#
            let status = try JSONDecoder().decode(RelayStatus.self, from: Data(json.utf8))
            #expect(status.state.rawValue == name)
        }
    }

    @Test("self_hosted carries a url when non-empty")
    func decodesSelfHostedWithURL() throws {
        let json = """
        {"state":"self_hosted","mode":"self_hosted","connection":"connected",
         "url":"https://relay.example.com"}
        """
        let status = try JSONDecoder().decode(RelayStatus.self, from: Data(json.utf8))
        #expect(status.mode == .selfHosted)
        #expect(status.url == "https://relay.example.com")
    }

    @Test("unavailable carries since")
    func decodesUnavailable() throws {
        let json = """
        {"state":"unavailable","mode":"hosted","connection":"disconnected",
         "since":"2026-09-10T12:00:00Z"}
        """
        let status = try JSONDecoder().decode(RelayStatus.self, from: Data(json.utf8))
        #expect(status.state == .unavailable)
        #expect(status.since == "2026-09-10T12:00:00Z")
    }
}

@Suite("RelayConfigureRequest encoding")
struct RelayConfigureRequestEncodingTests {

    private func encodedKeys(_ request: RelayConfigureRequest) throws -> Set<String> {
        let data = try JSONEncoder().encode(request)
        let object = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        return Set(object.keys)
    }

    @Test("nil optional fields are omitted, matching the Go struct's omitempty tags")
    func omitsNilFields() throws {
        let request = RelayConfigureRequest(mode: .off)
        #expect(try encodedKeys(request) == ["mode"])
    }

    @Test("empty-string optional fields are omitted too")
    func omitsEmptyStringFields() throws {
        let request = RelayConfigureRequest(mode: .selfHosted, url: "", licenseKey: "", label: "")
        #expect(try encodedKeys(request) == ["mode"])
    }

    @Test("present optional fields are encoded with their Go json tag names")
    func encodesPresentFields() throws {
        let request = RelayConfigureRequest(
            mode: .hosted, licenseKey: "rl_test_not_real", label: "Work Mac"
        )
        let data = try JSONEncoder().encode(request)
        let object = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(object["mode"] as? String == "hosted")
        #expect(object["license_key"] as? String == "rl_test_not_real")
        #expect(object["label"] as? String == "Work Mac")
        #expect(object["url"] == nil)
    }

    @Test("self_hosted's url is encoded when present")
    func encodesURL() throws {
        let request = RelayConfigureRequest(mode: .selfHosted, url: "https://relay.example.com")
        let data = try JSONEncoder().encode(request)
        let object = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(object["url"] as? String == "https://relay.example.com")
        #expect(object["license_key"] == nil)
    }
}

// MARK: - Client method tests

private final class RelayStatusStub: URLProtocol {
    nonisolated(unsafe) static var sawMethod: String?
    nonisolated(unsafe) static var sawPath: String?
    nonisolated(unsafe) static var body = Data(
        #"{"state":"active","mode":"hosted","connection":"connected","renews_at":"2026-10-01T00:00:00Z"}"#.utf8
    )

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        RelayStatusStub.sawMethod = request.httpMethod
        RelayStatusStub.sawPath = request.url?.path
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: RelayStatusStub.body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test("relayStatus() GETs /v1/relay/status and decodes the result")
func apiClientFetchesRelayStatus() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [RelayStatusStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )
    let status = try await client.relayStatus()
    #expect(RelayStatusStub.sawMethod == "GET")
    #expect(RelayStatusStub.sawPath == "/v1/relay/status")
    #expect(status.state == .active)
}

private final class RelayConfigureStub: URLProtocol {
    nonisolated(unsafe) static var sawMethod: String?
    nonisolated(unsafe) static var sawPath: String?
    nonisolated(unsafe) static var sawBody: Data?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        RelayConfigureStub.sawMethod = request.httpMethod
        RelayConfigureStub.sawPath = request.url?.path
        if let stream = request.httpBodyStream {
            stream.open()
            var collected = Data()
            let bufferSize = 4096
            var buffer = [UInt8](repeating: 0, count: bufferSize)
            while stream.hasBytesAvailable {
                let read = stream.read(&buffer, maxLength: bufferSize)
                if read <= 0 { break }
                collected.append(contentsOf: buffer[0..<read])
            }
            stream.close()
            RelayConfigureStub.sawBody = collected
        } else {
            RelayConfigureStub.sawBody = request.httpBody
        }
        let body = Data(#"{"state":"needs_license","mode":"hosted","connection":"disconnected"}"#.utf8)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test("configureRelay() POSTs the request body and decodes the returned status")
func apiClientConfiguresRelay() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [RelayConfigureStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )
    let status = try await client.configureRelay(
        RelayConfigureRequest(mode: .hosted, licenseKey: "rl_test_not_real", label: "Work Mac")
    )
    #expect(RelayConfigureStub.sawMethod == "POST")
    #expect(RelayConfigureStub.sawPath == "/v1/relay/configure")
    let sentBody = try #require(RelayConfigureStub.sawBody)
    let object = try #require(try JSONSerialization.jsonObject(with: sentBody) as? [String: Any])
    #expect(object["mode"] as? String == "hosted")
    #expect(object["license_key"] as? String == "rl_test_not_real")
    #expect(object["label"] as? String == "Work Mac")
    #expect(status.state == .needsLicense)
}

private final class RelayDevicesStub: URLProtocol {
    nonisolated(unsafe) static var sawMethod: String?
    nonisolated(unsafe) static var sawPath: String?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        RelayDevicesStub.sawMethod = request.httpMethod
        RelayDevicesStub.sawPath = request.url?.path
        let body = Data(
            #"""
            {"devices":[
              {"id":"dev-1","label":"Work Mac","first_seen":"2026-08-01T00:00:00Z","current":true},
              {"id":"dev-2","label":"Laptop","first_seen":"2026-08-15T00:00:00Z","current":false}
            ]}
            """#.utf8
        )
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test("relayDevices() GETs /v1/relay/devices and unwraps the devices array")
func apiClientListsRelayDevices() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [RelayDevicesStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )
    let devices = try await client.relayDevices()
    #expect(RelayDevicesStub.sawMethod == "GET")
    #expect(RelayDevicesStub.sawPath == "/v1/relay/devices")
    #expect(devices.count == 2)
    #expect(devices[0].id == "dev-1")
    #expect(devices[0].current == true)
    #expect(devices[1].id == "dev-2")
    #expect(devices[1].current == false)
}

private final class RelayDeactivateDeviceStub: URLProtocol {
    nonisolated(unsafe) static var sawMethod: String?
    nonisolated(unsafe) static var sawPath: String?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        RelayDeactivateDeviceStub.sawMethod = request.httpMethod
        RelayDeactivateDeviceStub.sawPath = request.url?.path
        let response = HTTPURLResponse(url: request.url!, statusCode: 204, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test("deactivateRelayDevice(id:) DELETEs the activation and does not try to decode a body")
func apiClientDeactivatesRelayDevice() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [RelayDeactivateDeviceStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )
    try await client.deactivateRelayDevice(id: "dev-1")
    #expect(RelayDeactivateDeviceStub.sawMethod == "DELETE")
    #expect(RelayDeactivateDeviceStub.sawPath == "/v1/relay/devices/dev-1")
}

private final class RelayPortalStub: URLProtocol {
    nonisolated(unsafe) static var sawMethod: String?
    nonisolated(unsafe) static var sawPath: String?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        RelayPortalStub.sawMethod = request.httpMethod
        RelayPortalStub.sawPath = request.url?.path
        let body = Data(#"{"url":"https://portal.example.com/session/abc"}"#.utf8)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test("relayPortalURL() POSTs /v1/relay/portal and unwraps the url")
func apiClientFetchesRelayPortalURL() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [RelayPortalStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )
    let url = try await client.relayPortalURL()
    #expect(RelayPortalStub.sawMethod == "POST")
    #expect(RelayPortalStub.sawPath == "/v1/relay/portal")
    #expect(url.absoluteString == "https://portal.example.com/session/abc")
}

private final class RelayDeactivateStub: URLProtocol {
    nonisolated(unsafe) static var sawMethod: String?
    nonisolated(unsafe) static var sawPath: String?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        RelayDeactivateStub.sawMethod = request.httpMethod
        RelayDeactivateStub.sawPath = request.url?.path
        let body = Data(#"{"state":"off","mode":"off","connection":"disconnected"}"#.utf8)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test("deactivateRelay() POSTs /v1/relay/deactivate and decodes the returned status")
func apiClientDeactivatesRelay() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [RelayDeactivateStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )
    let status = try await client.deactivateRelay()
    #expect(RelayDeactivateStub.sawMethod == "POST")
    #expect(RelayDeactivateStub.sawPath == "/v1/relay/deactivate")
    #expect(status.state == .off)
}

/// A non-2xx response from `configureRelay` must surface only a status code,
/// never the request body: the license key must never appear in a thrown
/// error's description.
private final class RelayConfigureRejectedStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let body = Data(#"{"code":"invalid_key","error":"the license key is invalid"}"#.utf8)
        let response = HTTPURLResponse(url: request.url!, statusCode: 422, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Test("a rejected configureRelay() throws a status-only error that never echoes the license key")
func apiClientConfigureRelayErrorNeverEchoesLicenseKey() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [RelayConfigureRejectedStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )
    do {
        _ = try await client.configureRelay(
            RelayConfigureRequest(mode: .hosted, licenseKey: "rl_secret_do_not_leak")
        )
        Issue.record("expected configureRelay to throw")
    } catch let error as RedlineAPIClient.Error {
        guard case .status(422) = error else {
            Issue.record("expected a status(422) error, got \(error)")
            return
        }
        #expect((error.errorDescription ?? "").contains("rl_secret_do_not_leak") == false)
    }
}
