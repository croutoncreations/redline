import Foundation
import Testing
@testable import RedlineKit

private final class StubURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            let (response, data) = try Self.handler!(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }
    override func stopLoading() {}
}

private final class RetryStubURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            let (response, data) = try Self.handler!(request)
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }
    override func stopLoading() {}
}

@Test func apiClientControlsProvidersAndReadsBoundedRunLogs() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [StubURLProtocol.self]
    let session = URLSession(configuration: configuration)
    let client = RedlineAPIClient(baseURL: URL(string: "http://127.0.0.1:7436")!, token: "local-token", session: session)
    nonisolated(unsafe) var requests: [(String, String)] = []
    StubURLProtocol.handler = { request in
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer local-token")
        requests.append((request.httpMethod ?? "", request.url!.absoluteString))
        let body: Data
        if request.url!.path.hasSuffix("/logs") {
            body = Data(#"{"content":"hello\n","size_bytes":6,"truncated":false}"#.utf8)
        } else if request.url!.path.hasSuffix("/refresh") {
            body = Data(#"{"weekly":{"remaining":0.55},"allowances":[],"source":"openusage"}"#.utf8)
        } else if request.url!.path.hasSuffix("/read") {
            body = Data(#"{"read":true}"#.utf8)
        } else {
            body = Data(#"{"provider_account_id":"codex main","paused":true}"#.utf8)
        }
        return (HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!, body)
    }
    defer { StubURLProtocol.handler = nil }

    let control = try await client.pauseProvider("codex main")
    #expect(control.paused)
    let usage = try await client.refreshProvider("claude main")
    let log = try await client.runLogs(runID: "run/one", stream: .stderr, tailBytes: 4096)
    try await client.markRunRead("run/one")
    #expect(usage.weekly?.remaining == 0.55)
    #expect(log.content == "hello\n")
    #expect(requests.count == 4)
    #expect(requests[0].0 == "POST")
    #expect(requests[0].1 == "http://127.0.0.1:7436/v1/providers/codex%20main/pause")
    #expect(requests[1].0 == "POST")
    #expect(requests[1].1 == "http://127.0.0.1:7436/v1/providers/claude%20main/refresh")
    #expect(requests[2].0 == "GET")
    #expect(requests[2].1 == "http://127.0.0.1:7436/v1/runs/run%2Fone/logs?stream=stderr&tail_bytes=4096")
    #expect(requests[3].0 == "POST")
    #expect(requests[3].1 == "http://127.0.0.1:7436/v1/runs/run%2Fone/read")
}

@Test func apiClientResumesPausedProviderBeforeRetryingFailedTask() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [RetryStubURLProtocol.self]
    let session = URLSession(configuration: configuration)
    let client = RedlineAPIClient(baseURL: URL(string: "http://127.0.0.1:7436")!, token: "local-token", session: session)
    nonisolated(unsafe) var captured: [URLRequest] = []
    RetryStubURLProtocol.handler = { request in
        captured.append(request)
        let body = request.url!.path.hasSuffix("/resume")
            ? Data(#"{"provider_account_id":"claude-main","paused":false}"#.utf8)
            : Data(#"{"id":"failed/task","name":"Failed task","priority":50,"state":"queued","provider_account_id":"claude-main","dispatch_tier":"behind"}"#.utf8)
        return (HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!, body)
    }
    defer { RetryStubURLProtocol.handler = nil }

    let task = try await client.recoverFailedTask(
        "failed/task",
        providerID: "claude-main",
        providerPaused: true
    )

    #expect(task.state == "queued")
    #expect(captured.map(\.httpMethod) == ["POST", "POST"])
    #expect(captured.map { $0.url!.absoluteString } == [
        "http://127.0.0.1:7436/v1/providers/claude-main/resume",
        "http://127.0.0.1:7436/v1/tasks/failed%2Ftask/retry",
    ])
}

/// A stub that always answers 201 with a pairing token.
///
/// Each pairing test gets its own class rather than sharing one with a mutable
/// handler: a process-global handler is a race between concurrently running
/// tests, and marking them serialized narrows that window without closing it.
final class PairingCreatedStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let body = Data(
            #"{"pairing_token":"8G-y7DyZw3yx","expires_at":"2026-09-03T19:44:05.604714Z"}"#.utf8
        )
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 201, httpVersion: nil, headerFields: nil
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

/// What the service answers when a tailnet host and a relay are both set up.
final class PairingWithRelayStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let body = Data(
            #"""
            {"pairing_token":"8G-y7DyZw3yx","expires_at":"2026-09-03T19:44:05.604714Z",
             "pairing_url":"https://macbook.example.ts.net:8443/pair#pairing_token=8G-y7DyZw3yx&relay=https%3A%2F%2Frelay.example&key=ds%2Bl3Fu%2BI5pT&session=s",
             "routes":["direct","relay"],"endpoint":"macbook.example.ts.net:8443"}
            """#.utf8
        )
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 201, httpVersion: nil, headerFields: nil
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

/// What the service answers for a desktop with a relay and no tailnet.
final class PairingRelayOnlyStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let body = Data(
            #"""
            {"pairing_token":"8G-y7DyZw3yx","expires_at":"2026-09-03T19:44:05.604714Z",
             "pairing_url":"https://relay/pair#pairing_token=8G-y7DyZw3yx&relay=https%3A%2F%2Frelay.example&key=k&session=s",
             "routes":["relay"]}
            """#.utf8
        )
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 201, httpVersion: nil, headerFields: nil
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

/// Answers GET /v1/pairing/status with "redeemed" and records what it was asked.
final class PairingStatusStub: URLProtocol {
    nonisolated(unsafe) static var sawHeader: String?
    nonisolated(unsafe) static var sawPath: String?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        PairingStatusStub.sawHeader = request.value(forHTTPHeaderField: "X-Redline-Pairing-Token")
        PairingStatusStub.sawPath = request.url?.path
        let body = Data(#"{"status":"redeemed"}"#.utf8)
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

/// Answers with a status this build has never heard of.
final class PairingUnknownStatusStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let body = Data(#"{"status":"something-new"}"#.utf8)
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

/// A stub that always answers 401.
final class PairingUnauthorizedStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 401, httpVersion: nil, headerFields: nil
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{}".utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

/// The pairing endpoint answers 201 Created, not 200.
///
/// The client accepted only 200, so every attempt to open the pairing window
/// would have failed with "Redline returned HTTP 201". Nothing caught it
/// because the surrounding tests never exercised the real status code.
@Test func apiClientAcceptsTheCreatedStatusFromPairing() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [PairingCreatedStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )

    let pairing = try await client.createPairingToken()
    #expect(pairing.token == "8G-y7DyZw3yx")
    // The timestamp carries fractional seconds, which the default ISO 8601
    // options reject outright rather than ignoring.
    #expect(pairing.expiry != nil)
}

/// The service composes the pairing URL, and the client carries it through
/// untouched.
///
/// The menu bar used to build its own URL from the trusted host and the
/// token, and nothing else. When the QR grew relay fields the CLI got them
/// and the sheet did not, so every phone paired from the desktop app had no
/// relay and no way to know. The service is the one builder now; this client
/// must not have an opinion about the URL's shape.
@Test func apiClientCarriesTheServiceComposedPairingURL() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [PairingWithRelayStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )

    let pairing = try await client.createPairingToken()
    #expect(pairing.pairingURL == "https://macbook.example.ts.net:8443/pair#pairing_token=8G-y7DyZw3yx&relay=https%3A%2F%2Frelay.example&key=ds%2Bl3Fu%2BI5pT&session=s")
    #expect(pairing.routes == [.direct, .relay])
    #expect(pairing.endpoint == "macbook.example.ts.net:8443")
}

/// An older service answers without the new fields. The sheet must still work
/// against it, and must say plainly that it cannot build a code rather than
/// showing an empty QR.
@Test func apiClientToleratesAServiceWithoutAPairingURL() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [PairingCreatedStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )

    let pairing = try await client.createPairingToken()
    #expect(pairing.pairingURL == nil)
    #expect(pairing.routes.isEmpty)
}

/// A relay-only user has no trusted host, and the pairing URL says so.
@Test func apiClientReadsARelayOnlyPairing() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [PairingRelayOnlyStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )

    let pairing = try await client.createPairingToken()
    #expect(pairing.routes == [.relay])
    #expect(pairing.endpoint == nil)
    #expect(pairing.pairingURL?.hasPrefix("https://relay/pair#") == true)
}

/// The sheet asks whether its code has been scanned, sending the pairing token
/// in a header rather than the URL so it stays out of access logs.
@Test func apiClientAsksForPairingStatusWithTheTokenInAHeader() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [PairingStatusStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )

    let status = try await client.pairingStatus(of: "8G-y7DyZw3yx")
    #expect(status == .redeemed)
    #expect(PairingStatusStub.sawHeader == "8G-y7DyZw3yx")
    #expect(PairingStatusStub.sawPath == "/v1/pairing/status")
}

/// A status this client does not know is reported as expired, not as a crash:
/// the remedy -- offer a new code -- is the same.
@Test func apiClientTreatsAnUnknownPairingStatusAsExpired() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    // Its own stub: tests run in parallel, and a status shared through a
    // static on one stub class is a race between them.
    configuration.protocolClasses = [PairingUnknownStatusStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )
    #expect(try await client.pairingStatus(of: "x") == .expired)
}

/// A genuine failure must still be reported rather than swallowed by a wider
/// success range.
@Test func apiClientStillRejectsErrorStatuses() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [PairingUnauthorizedStub.self]
    let client = RedlineAPIClient(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: "local-token",
        session: URLSession(configuration: configuration)
    )

    await #expect(throws: RedlineAPIClient.Error.self) {
        _ = try await client.createPairingToken()
    }
}
