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

@Test func apiClientControlsProvidersAndReadsBoundedRunLogs() async throws {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [StubURLProtocol.self]
    let session = URLSession(configuration: configuration)
    let client = RedlineAPIClient(baseURL: URL(string: "http://127.0.0.1:7436")!, session: session)
    nonisolated(unsafe) var requests: [(String, String)] = []
    StubURLProtocol.handler = { request in
        requests.append((request.httpMethod ?? "", request.url!.absoluteString))
        let body = request.url!.path.hasSuffix("/logs")
            ? Data(#"{"content":"hello\n","size_bytes":6,"truncated":false}"#.utf8)
            : Data(#"{"provider_account_id":"codex main","paused":true}"#.utf8)
        return (HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!, body)
    }
    defer { StubURLProtocol.handler = nil }

    let control = try await client.pauseProvider("codex main")
    #expect(control.paused)
    let log = try await client.runLogs(runID: "run/one", stream: .stderr, tailBytes: 4096)
    #expect(log.content == "hello\n")
    #expect(requests.count == 2)
    #expect(requests[0].0 == "POST")
    #expect(requests[0].1 == "http://127.0.0.1:7436/v1/providers/codex%20main/pause")
    #expect(requests[1].0 == "GET")
    #expect(requests[1].1 == "http://127.0.0.1:7436/v1/runs/run%2Fone/logs?stream=stderr&tail_bytes=4096")
}
