// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "RedlineMac",
    platforms: [.macOS(.v13)],
    products: [
        .library(name: "RedlineKit", targets: ["RedlineKit"]),
        .executable(name: "RedlineMenuBar", targets: ["RedlineMenuBar"]),
    ],
    targets: [
        .target(name: "RedlineKit"),
        .executableTarget(name: "RedlineMenuBar", dependencies: ["RedlineKit"], exclude: ["Resources"]),
        .testTarget(name: "RedlineKitTests", dependencies: ["RedlineKit"]),
    ]
)
