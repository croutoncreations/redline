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
