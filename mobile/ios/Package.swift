// swift-tools-version: 6.0
// SPDX-License-Identifier: Apache-2.0

import PackageDescription

let package = Package(
    name: "YunPinIOS",
    platforms: [
        .iOS(.v17),
        .macOS(.v13),
    ],
    products: [
        .library(name: "YunPinMobileCore", targets: ["YunPinMobileCore"]),
        .library(name: "YunPinAppServices", targets: ["YunPinAppServices"]),
        .library(name: "YunPinKeyboardCore", targets: ["YunPinKeyboardCore"]),
    ],
    targets: [
        .target(name: "YunPinMobileCore"),
        .target(
            name: "YunPinAppServices",
            dependencies: ["YunPinMobileCore"]
        ),
        .target(
            name: "YunPinKeyboardCore",
            dependencies: ["YunPinMobileCore"]
        ),
        .testTarget(
            name: "YunPinMobileCoreTests",
            dependencies: ["YunPinMobileCore"]
        ),
        .testTarget(
            name: "YunPinAppServicesTests",
            dependencies: ["YunPinAppServices", "YunPinKeyboardCore"]
        ),
        .testTarget(
            name: "YunPinKeyboardCoreTests",
            dependencies: ["YunPinKeyboardCore"]
        ),
    ]
)
