import Foundation
import Testing
@testable import RedlineKit

private func temporaryLog(_ contents: String) throws -> URL {
    let url = FileManager.default.temporaryDirectory
        .appending(path: "redline-supervisor-\(UUID().uuidString).log")
    try contents.write(to: url, atomically: true, encoding: .utf8)
    return url
}

@Test func lastStderrLineReturnsTheProblemLineOfAYAMLDecodeError() throws {
    // Exactly what `redline serve` prints when the config has a type error.
    let url = try temporaryLog("""
    decode config /Users/x/Library/Application Support/Redline/redline.yaml: yaml: unmarshal errors:
      line 7: cannot unmarshal !!str `definitely` into bool

    """)
    defer { try? FileManager.default.removeItem(at: url) }
    #expect(ServiceSupervisor.lastStderrLine(at: url, after: 0)
        == "line 7: cannot unmarshal !!str `definitely` into bool")
}

@Test func lastStderrLineOnlyReadsOutputFromThisLaunch() throws {
    let previous = "old launch: some earlier failure\n"
    let url = try temporaryLog(previous + "config redline.yaml: api trusted_hosts[0] must be a MagicDNS name\n")
    defer { try? FileManager.default.removeItem(at: url) }
    let offset = UInt64(previous.utf8.count)
    #expect(ServiceSupervisor.lastStderrLine(at: url, after: offset)
        == "config redline.yaml: api trusted_hosts[0] must be a MagicDNS name")
    #expect(ServiceSupervisor.lastStderrLine(at: url, after: UInt64(url.dataSize)) == nil)
}

@Test func lastStderrLineIsNilForMissingOrEmptyLog() throws {
    let missing = FileManager.default.temporaryDirectory.appending(path: "does-not-exist-\(UUID().uuidString).log")
    #expect(ServiceSupervisor.lastStderrLine(at: missing, after: 0) == nil)
    let empty = try temporaryLog("\n\n   \n")
    defer { try? FileManager.default.removeItem(at: empty) }
    #expect(ServiceSupervisor.lastStderrLine(at: empty, after: 0) == nil)
}

private extension URL {
    var dataSize: Int {
        (try? Data(contentsOf: self).count) ?? 0
    }
}
