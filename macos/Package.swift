// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "RedlineMac",
    platforms: [.macOS(.v13)],
    products: [
        .library(name: "RedlineKit", targets: ["RedlineKit"]),
        .executable(name: "RedlineMenuBar", targets: ["RedlineMenuBar"]),
    ],
    dependencies: [
        .package(url: "https://github.com/sparkle-project/Sparkle", exact: "2.9.2"),
    ],
    targets: [
        .target(name: "RedlineKit"),
        .executableTarget(
            name: "RedlineMenuBar",
            dependencies: [
                "RedlineKit",
                .product(name: "Sparkle", package: "Sparkle"),
            ],
            exclude: ["Resources"],
            linkerSettings: [
                .unsafeFlags(["-Xlinker", "-rpath", "-Xlinker", "@executable_path/../Frameworks"]),
            ]
        ),
        .testTarget(name: "RedlineKitTests", dependencies: ["RedlineKit"]),
    ]
)
