import Foundation
import Testing
@testable import RedlineKit

@Test func launchConfigurationBuildsBundledServiceCommand() {
    let configuration = ServiceLaunchConfiguration(
        executableURL: URL(fileURLWithPath: "/Applications/Redline.app/Contents/Resources/bin/redline"),
        configURL: URL(fileURLWithPath: "/Users/test/Library/Application Support/Redline/redline.yaml"),
        apiURL: URL(string: "http://127.0.0.1:7436")!
    )

    #expect(configuration.arguments == [
        "--config", "/Users/test/Library/Application Support/Redline/redline.yaml",
        "serve", "--listen", "127.0.0.1:7436",
    ])
    #expect(configuration.workingDirectory.path == "/Users/test/Library/Application Support/Redline")
}

@Test func launchConfigurationRequiresLoopbackHTTPAPI() {
    #expect(throws: ServiceLaunchConfiguration.Error.unsupportedAPIURL) {
        try ServiceLaunchConfiguration.validated(
            executableURL: URL(fileURLWithPath: "/tmp/redline"),
            configURL: URL(fileURLWithPath: "/tmp/redline.yaml"),
            apiURL: URL(string: "https://example.com")!
        )
    }
}
