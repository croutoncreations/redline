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
    let log = try await client.runLogs(runID: "run/one", stream: .stderr, tailBytes: 4096)
    try await client.markRunRead("run/one")
    #expect(log.content == "hello\n")
    #expect(requests.count == 3)
    #expect(requests[0].0 == "POST")
    #expect(requests[0].1 == "http://127.0.0.1:7436/v1/providers/codex%20main/pause")
    #expect(requests[1].0 == "GET")
    #expect(requests[1].1 == "http://127.0.0.1:7436/v1/runs/run%2Fone/logs?stream=stderr&tail_bytes=4096")
    #expect(requests[2].0 == "POST")
    #expect(requests[2].1 == "http://127.0.0.1:7436/v1/runs/run%2Fone/read")
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
