// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "SiriGlassComponent",
    // Keep the host launchable on the public Glass API floor so the adapter
    // can show a readable refusal outside its exact private ABI gate.
    platforms: [.macOS(.v26)],
    products: [
        .library(name: "SiriGlassBridge", type: .dynamic, targets: ["SiriGlassBridge"])
    ],
    targets: [
        .target(name: "SiriGlassModel"),
        .target(
            name: "CGlassPrivateABI",
            path: "Sources/CGlassPrivateABI",
            publicHeadersPath: "include",
            cSettings: [.unsafeFlags(["-fobjc-arc"])],
            linkerSettings: [.linkedFramework("Foundation")]
        ),
        .target(
            name: "CGlassCompatibility",
            publicHeadersPath: "include",
            linkerSettings: [.linkedFramework("Foundation")]
        ),
        .target(
            name: "SiriGlassNativeABI",
            dependencies: ["SiriGlassModel", "CGlassPrivateABI", "CGlassCompatibility"]
        ),
        .target(
            name: "SiriGlassRenderer",
            dependencies: ["SiriGlassModel"]
        ),
        .target(
            name: "SiriGlassBridge",
            dependencies: ["SiriGlassModel", "SiriGlassNativeABI", "SiriGlassRenderer"]
        ),
        .testTarget(name: "SiriGlassModelTests", dependencies: ["SiriGlassModel"])
    ]
)
