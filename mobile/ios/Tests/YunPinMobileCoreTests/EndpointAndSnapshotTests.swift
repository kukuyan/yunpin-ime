// SPDX-License-Identifier: Apache-2.0

import Foundation
import Testing
@testable import YunPinMobileCore

@Test func endpointPolicyMatchesDesktopBoundary() throws {
    let https = try SyncEndpointProfile(
        displayName: "Synthetic relay",
        endpoint: "https://sync.example.test",
        allowsPrivateHTTP: false
    )
    #expect(https.endpoint.absoluteString == "https://sync.example.test")

    let ipv6Loopback = try SyncEndpointProfile(
        displayName: "Synthetic IPv6 loopback",
        endpoint: "http://[::1]:8787",
        allowsPrivateHTTP: true
    )
    #expect(ipv6Loopback.endpoint.host == "::1")

    _ = try SyncEndpointProfile(
        displayName: "Synthetic IPv6 private",
        endpoint: "http://[fd00::1]:8787",
        allowsPrivateHTTP: true
    )

    #expect(throws: EndpointProfileError.privateHTTPRequiresOptIn) {
        try SyncEndpointProfile(
            displayName: "Synthetic LAN",
            endpoint: "http://192.168.50.10:8787",
            allowsPrivateHTTP: false
        )
    }
    #expect(throws: EndpointProfileError.publicHTTPForbidden) {
        try SyncEndpointProfile(
            displayName: "Synthetic public HTTP",
            endpoint: "http://203.0.113.10:8787",
            allowsPrivateHTTP: true
        )
    }
    #expect(throws: EndpointProfileError.publicHTTPForbidden) {
        try SyncEndpointProfile(
            displayName: "Synthetic public hostname",
            endpoint: "http://public.example.invalid:8787",
            allowsPrivateHTTP: true
        )
    }
    #expect(throws: EndpointProfileError.credentialsOrComponentsForbidden) {
        try SyncEndpointProfile(
            displayName: "Synthetic unsafe URL",
            endpoint: "https://user:credential@example.test/path",
            allowsPrivateHTTP: false
        )
    }
}

@Test func readOnlySnapshotFallsBackWithoutRewritingSharedCoreData() throws {
    let root = temporaryDirectory()
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let snapshotURL = root.appendingPathComponent("private.tsv")
    let fallback = syntheticSnapshot(text: "合成回滚候选", pinyin: "he cheng hui gun hou xuan")
    try fallback.write(to: URL(fileURLWithPath: snapshotURL.path + ".rollback"), options: .atomic)
    try Data("synthetic corruption".utf8).write(to: snapshotURL, options: .atomic)

    let resolved = try ReadOnlySnapshotResolver(snapshotURL: snapshotURL).resolve()
    #expect(resolved.usedFallback)
    #expect(resolved.data == fallback)
    #expect(try Data(contentsOf: snapshotURL) == Data("synthetic corruption".utf8))
    #expect(throws: PrivateSnapshotError.self) {
        try ExactSnapshotInspector.inspect(at: snapshotURL)
    }
    let rollbackFingerprint = try ExactSnapshotInspector.inspect(
        at: URL(fileURLWithPath: snapshotURL.path + ".rollback")
    )
    #expect(rollbackFingerprint.sha256Hex.count == 64)
}

@Test func snapshotValidatorRejectsLegacyOrMalformedRows() {
    let malformed = Data("phrase\tpinyin\tsource\tuse_count\tpinned\n合成\the cheng\tpersonal_import\t1\tfalse\n".utf8)
    #expect(throws: PrivateSnapshotError.invalidRow) {
        try PrivateSnapshotValidator.validate(malformed)
    }

    let zeroDay = Data((PrivateSnapshotValidator.header
        + "合成\the cheng\tsynced_learning@0\t1\tfalse\n").utf8)
    #expect(throws: PrivateSnapshotError.invalidRow) {
        try PrivateSnapshotValidator.validate(zeroDay)
    }
}

@Test func snapshotValidatorRejectsDuplicatePhraseIdentity() {
    let row = "合成重复\the cheng chong fu\tsynced_learning\t1\tfalse\n"
    let duplicate = Data((PrivateSnapshotValidator.header + row + row).utf8)
    #expect(throws: PrivateSnapshotError.invalidRow) {
        try PrivateSnapshotValidator.validate(duplicate)
    }
}

@Test func snapshotValidatorRequiresTerminalLFAndRejectsC1Controls() {
    let validRow = "合成换行\the cheng huan hang\tsynced_learning\t1\tfalse"
    #expect(throws: PrivateSnapshotError.invalidRow) {
        try PrivateSnapshotValidator.validate(
            Data((PrivateSnapshotValidator.header + validRow).utf8)
        )
    }
    #expect(throws: PrivateSnapshotError.invalidRow) {
        try PrivateSnapshotValidator.validate(Data())
    }

    let c1 = Data((PrivateSnapshotValidator.header
        + "合成\u{0085}控制\the cheng kong zhi\tsynced_learning\t1\tfalse\n").utf8)
    #expect(throws: PrivateSnapshotError.invalidRow) {
        try PrivateSnapshotValidator.validate(c1)
    }
}

private func temporaryDirectory() -> URL {
    FileManager.default.temporaryDirectory
        .appendingPathComponent("yunpin-ios-tests")
        .appendingPathComponent(UUID().uuidString)
}

private func syntheticSnapshot(text: String, pinyin: String) -> Data {
    Data((PrivateSnapshotValidator.header + "\(text)\t\(pinyin)\tsynced_learning@20000\t3\tfalse\n").utf8)
}
