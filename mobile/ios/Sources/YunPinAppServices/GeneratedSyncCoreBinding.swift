// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(YunPinMobileCore)
import YunPinMobileCore
#endif

/// App composition selects the reviewed generated framework when present and
/// otherwise keeps the existing explicit fail-closed gate.
public enum SyncCoreBindingFactory {
    public static func production() -> any SyncCoreBinding {
        #if canImport(Mobilecore)
        GeneratedSyncCoreBinding()
        #else
        UnavailableSyncCoreBinding()
        #endif
    }
}

enum GeneratedBindingPayloadKind: Sendable {
    case sync
    case status
    case snapshot
    case learn

    fileprivate var resultKeys: Set<String> {
        switch self {
        case .sync:
            ["Rounds", "Uploaded", "Downloaded", "Cursor", "Pending", "SnapshotRows", "SnapshotChanged"]
        case .status:
            ["Cursor", "Pending", "Prepared", "SnapshotPresent", "RollbackPresent", "ControlPlaneGate"]
        case .snapshot:
            ["Generation", "Rows", "Changed", "RollbackAvailable"]
        case .learn:
            ["Recorded", "UseCount", "SyncEligible"]
        }
    }
}

/// Validates the generated facade's small JSON envelope before Codable sees
/// it. Foundation decoders accept duplicate object members, so relying on
/// first/last-wins behavior could make `ok`, an error code, or a result field
/// ambiguous. This boundary rejects duplicate keys at every object depth,
/// trailing tokens, unknown fields, and success/failure shape mixing.
enum GeneratedBindingJSONBoundary {
    static func validate(_ json: String, kind: GeneratedBindingPayloadKind) throws {
        let data = Data(json.utf8)
        guard data.count >= 2, data.count <= 16 * 1024 else {
            throw SyncCoreBindingError.localState
        }
        do {
            var scanner = StrictJSONMemberScanner(data: data)
            try scanner.validateSingleValue()
            guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
                  let okNumber = root["ok"] as? NSNumber,
                  CFGetTypeID(okNumber) == CFBooleanGetTypeID() else {
                throw SyncCoreBindingError.localState
            }
            if okNumber.boolValue {
                guard Set(root.keys) == ["ok", "result"],
                      let result = root["result"] as? [String: Any],
                      Set(result.keys) == kind.resultKeys else {
                    throw SyncCoreBindingError.localState
                }
            } else {
                guard Set(root.keys) == ["ok", "error_code"],
                      root["error_code"] is String else {
                    throw SyncCoreBindingError.localState
                }
            }
        } catch let error as SyncCoreBindingError {
            throw error
        } catch {
            throw SyncCoreBindingError.localState
        }
    }
}

private struct StrictJSONMemberScanner {
    private enum ScanError: Error { case invalid }

    private let bytes: [UInt8]
    private var index = 0

    init(data: Data) {
        self.bytes = Array(data)
    }

    mutating func validateSingleValue() throws {
        skipWhitespace()
        try parseValue()
        skipWhitespace()
        guard index == bytes.count else { throw ScanError.invalid }
    }

    private mutating func parseValue() throws {
        guard let byte = current else { throw ScanError.invalid }
        switch byte {
        case 0x7B: try parseObject()       // {
        case 0x5B: try parseArray()        // [
        case 0x22: _ = try parseString()   // "
        case 0x74: try consume("true")
        case 0x66: try consume("false")
        case 0x6E: try consume("null")
        case 0x2D, 0x30...0x39: try parseNumber()
        default: throw ScanError.invalid
        }
    }

    private mutating func parseObject() throws {
        try consumeByte(0x7B)
        skipWhitespace()
        if consumeIfPresent(0x7D) { return }
        var keys = Set<String>()
        while true {
            guard current == 0x22 else { throw ScanError.invalid }
            let key = try parseString()
            guard keys.insert(key).inserted else { throw ScanError.invalid }
            skipWhitespace()
            try consumeByte(0x3A)
            skipWhitespace()
            try parseValue()
            skipWhitespace()
            if consumeIfPresent(0x7D) { return }
            try consumeByte(0x2C)
            skipWhitespace()
        }
    }

    private mutating func parseArray() throws {
        try consumeByte(0x5B)
        skipWhitespace()
        if consumeIfPresent(0x5D) { return }
        while true {
            try parseValue()
            skipWhitespace()
            if consumeIfPresent(0x5D) { return }
            try consumeByte(0x2C)
            skipWhitespace()
        }
    }

    private mutating func parseString() throws -> String {
        let start = index
        try consumeByte(0x22)
        while let byte = current {
            switch byte {
            case 0x22:
                index += 1
                let encoded = Data(bytes[start..<index])
                guard let value = try? JSONDecoder().decode(String.self, from: encoded) else {
                    throw ScanError.invalid
                }
                return value
            case 0x00...0x1F:
                throw ScanError.invalid
            case 0x5C:
                index += 1
                guard let escaped = current else { throw ScanError.invalid }
                if escaped == 0x75 {
                    index += 1
                    for _ in 0..<4 {
                        guard let scalar = current,
                              (scalar >= 0x30 && scalar <= 0x39)
                                || (scalar >= 0x41 && scalar <= 0x46)
                                || (scalar >= 0x61 && scalar <= 0x66) else {
                            throw ScanError.invalid
                        }
                        index += 1
                    }
                } else {
                    guard [0x22, 0x5C, 0x2F, 0x62, 0x66, 0x6E, 0x72, 0x74].contains(escaped) else {
                        throw ScanError.invalid
                    }
                    index += 1
                }
            default:
                index += 1
            }
        }
        throw ScanError.invalid
    }

    private mutating func parseNumber() throws {
        _ = consumeIfPresent(0x2D)
        guard let first = current else { throw ScanError.invalid }
        if first == 0x30 {
            index += 1
            if let next = current, next >= 0x30, next <= 0x39 {
                throw ScanError.invalid
            }
        } else {
            guard first >= 0x31, first <= 0x39 else { throw ScanError.invalid }
            index += 1
            consumeDigits()
        }
        if consumeIfPresent(0x2E) {
            guard consumeDigits() else { throw ScanError.invalid }
        }
        if current == 0x65 || current == 0x45 {
            index += 1
            if current == 0x2B || current == 0x2D { index += 1 }
            guard consumeDigits() else { throw ScanError.invalid }
        }
    }

    @discardableResult
    private mutating func consumeDigits() -> Bool {
        let start = index
        while let byte = current, byte >= 0x30, byte <= 0x39 { index += 1 }
        return index != start
    }

    private mutating func consume(_ literal: StaticString) throws {
        for byte in literal.withUTF8Buffer({ Array($0) }) {
            try consumeByte(byte)
        }
    }

    private mutating func consumeByte(_ expected: UInt8) throws {
        guard current == expected else { throw ScanError.invalid }
        index += 1
    }

    private mutating func consumeIfPresent(_ expected: UInt8) -> Bool {
        guard current == expected else { return false }
        index += 1
        return true
    }

    private mutating func skipWhitespace() {
        while let byte = current, byte == 0x20 || byte == 0x09 || byte == 0x0A || byte == 0x0D {
            index += 1
        }
    }

    private var current: UInt8? {
        index < bytes.count ? bytes[index] : nil
    }
}

#if canImport(Mobilecore)
import Mobilecore

/// Adapter for the gomobile surface generated by running
/// `gomobile bind -target=ios -o <external-cache>/Mobilecore.xcframework .`
/// from `mobile/synccore` (the repository root has no `go.mod`). Every call
/// owns one short-lived facade; decoded
/// results contain fixed counters/booleans only and raw native errors are never
/// logged, returned to UI, or persisted.
public struct GeneratedSyncCoreBinding: SyncCoreBinding {
    private let cancellationRegistry: ActiveFacadeCancellationRegistry

    public init() {
        self.cancellationRegistry = ActiveFacadeCancellationRegistry()
    }

    public func synchronize(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        timeoutMilliseconds: Int64
    ) async throws -> SyncCoreReport {
        try await perform(paths: paths, endpoint: endpoint, credentialBlob: credentialBlob) { facade in
            let envelope: WireEnvelope<WireSyncReport> = try decode(
                facade.sync(timeoutMilliseconds),
                kind: .sync
            )
            let result = try requireResult(envelope)
            try GeneratedBindingResultValidator.validateSync(
                rounds: result.rounds,
                uploaded: result.uploaded,
                downloaded: result.downloaded,
                cursor: result.cursor,
                snapshotRows: result.snapshotRows
            )
            return SyncCoreReport(
                rounds: result.rounds,
                uploaded: result.uploaded,
                downloaded: result.downloaded,
                cursor: result.cursor,
                pending: result.pending,
                snapshotRows: result.snapshotRows,
                snapshotChanged: result.snapshotChanged
            )
        }
    }

    public func status(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        timeoutMilliseconds: Int64
    ) async throws -> SyncCoreStatus {
        try await perform(paths: paths, endpoint: endpoint, credentialBlob: credentialBlob) { facade in
            let envelope: WireEnvelope<WireStatus> = try decode(
                facade.status(timeoutMilliseconds),
                kind: .status
            )
            let result = try requireResult(envelope)
            try GeneratedBindingResultValidator.validateStatus(
                cursor: result.cursor,
                controlPlaneGate: result.controlPlaneGate
            )
            return SyncCoreStatus(
                cursor: result.cursor,
                pending: result.pending,
                prepared: result.prepared,
                snapshotPresent: result.snapshotPresent,
                rollbackPresent: result.rollbackPresent,
                controlPlaneGate: result.controlPlaneGate
            )
        }
    }

    public func publishSnapshot(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        timeoutMilliseconds: Int64
    ) async throws -> SnapshotPublishReport {
        try await perform(paths: paths, endpoint: endpoint, credentialBlob: credentialBlob) { facade in
            let envelope: WireEnvelope<WireSnapshotReport> = try decode(
                facade.publishSnapshot(timeoutMilliseconds),
                kind: .snapshot
            )
            let result = try requireResult(envelope)
            try GeneratedBindingResultValidator.validateSnapshot(rows: result.rows)
            return SnapshotPublishReport(
                generation: result.generation,
                rows: result.rows,
                changed: result.changed,
                rollbackAvailable: result.rollbackAvailable
            )
        }
    }

    public func rollbackSnapshot(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data
    ) async throws {
        try await perform(paths: paths, endpoint: endpoint, credentialBlob: credentialBlob) { facade in
            try facade.rollbackSnapshot()
        }
    }

    public func recordSelection(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        text: String,
        pinyin: String,
        privacy: SelectionPrivacyContext,
        timeoutMilliseconds: Int64
    ) async throws -> RecordSelectionReport {
        try await perform(paths: paths, endpoint: endpoint, credentialBlob: credentialBlob) { facade in
            let json = try facade.recordSelection(
                text,
                pinyin,
                privacy.passwordField,
                privacy.privateMode,
                privacy.oneTimeInput,
                privacy.noPersonalizedLearning,
                timeoutMilliseconds
            )
            let envelope: WireEnvelope<WireLearnResult> = try decode(json, kind: .learn)
            let result = try requireResult(envelope)
            return RecordSelectionReport(
                recorded: result.recorded,
                useCount: result.useCount,
                syncEligible: result.syncEligible
            )
        }
    }

    public func saveExplicit(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        text: String,
        pinyin: String,
        useCount: UInt64,
        pinned: Bool,
        timeoutMilliseconds: Int64
    ) async throws {
        guard useCount > 0, useCount <= UInt64(Int64.max) else {
            throw SyncCoreBindingError.invalidConfiguration
        }
        try await perform(paths: paths, endpoint: endpoint, credentialBlob: credentialBlob) { facade in
            try facade.saveExplicit(text, pinyin, Int64(useCount), pinned, timeoutMilliseconds)
        }
    }

    public func delete(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        text: String,
        pinyin: String,
        timeoutMilliseconds: Int64
    ) async throws {
        try await perform(paths: paths, endpoint: endpoint, credentialBlob: credentialBlob) { facade in
            try facade.delete(text, pinyin, timeoutMilliseconds)
        }
    }

    public func cancelCurrentOperation() async {
        cancellationRegistry.cancelCurrentOperation()
    }

    private func perform<Result: Sendable>(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        operation: @escaping @Sendable (MobilecoreFacade) throws -> Result
    ) async throws -> Result {
        guard let lease = cancellationRegistry.beginOperation() else {
            throw SyncCoreBindingError.localState
        }
        let registry = cancellationRegistry
        return try await Task.detached(priority: .utility) {
            defer { registry.finishOperation(lease) }
            return try Self.withFacade(
                paths: paths,
                endpoint: endpoint,
                credentialBlob: credentialBlob,
                registry: registry,
                lease: lease,
                operation: operation
            )
        }.value
    }

    private static func withFacade<Result: Sendable>(
        paths: SyncCorePaths,
        endpoint: SyncEndpointProfile,
        credentialBlob: Data,
        registry: ActiveFacadeCancellationRegistry,
        lease: ActiveFacadeCancellationRegistry.Lease,
        operation: (MobilecoreFacade) throws -> Result
    ) throws -> Result {
        do {
            let facade = try MobilecoreOpenFacade(
                paths.encryptedDatabaseURL.path,
                paths.privateSnapshotURL.path,
                endpoint.endpoint.absoluteString,
                endpoint.allowsPrivateHTTP,
                credentialBlob
            )
            defer { try? facade.close() }
            let cancellationHandle = MobilecoreFacadeCancellationHandle(facade: facade)
            guard registry.registerFacade(
                for: lease,
                cancellationAction: { cancellationHandle.cancel() }
            ) else {
                throw SyncCoreBindingError.localState
            }
            return try operation(facade)
        } catch {
            throw normalizeGeneratedBindingError(error)
        }
    }
}

private final class MobilecoreFacadeCancellationHandle: @unchecked Sendable {
    private let facade: MobilecoreFacade

    init(facade: MobilecoreFacade) {
        self.facade = facade
    }

    func cancel() {
        facade.cancelCurrentOperation()
    }
}

private struct WireEnvelope<Result: Decodable & Sendable>: Decodable, Sendable {
    let ok: Bool
    let errorCode: String?
    let result: Result?

    enum CodingKeys: String, CodingKey {
        case ok
        case errorCode = "error_code"
        case result
    }
}

private struct WireSyncReport: Decodable, Sendable {
    let rounds: Int
    let uploaded: Int
    let downloaded: Int
    let cursor: Int64
    let pending: UInt64
    let snapshotRows: Int
    let snapshotChanged: Bool

    enum CodingKeys: String, CodingKey {
        case rounds = "Rounds"
        case uploaded = "Uploaded"
        case downloaded = "Downloaded"
        case cursor = "Cursor"
        case pending = "Pending"
        case snapshotRows = "SnapshotRows"
        case snapshotChanged = "SnapshotChanged"
    }
}

private struct WireStatus: Decodable, Sendable {
    let cursor: Int64
    let pending: UInt64
    let prepared: Bool
    let snapshotPresent: Bool
    let rollbackPresent: Bool
    let controlPlaneGate: String

    enum CodingKeys: String, CodingKey {
        case cursor = "Cursor"
        case pending = "Pending"
        case prepared = "Prepared"
        case snapshotPresent = "SnapshotPresent"
        case rollbackPresent = "RollbackPresent"
        case controlPlaneGate = "ControlPlaneGate"
    }
}

private struct WireSnapshotReport: Decodable, Sendable {
    let generation: UInt64
    let rows: Int
    let changed: Bool
    let rollbackAvailable: Bool

    enum CodingKeys: String, CodingKey {
        case generation = "Generation"
        case rows = "Rows"
        case changed = "Changed"
        case rollbackAvailable = "RollbackAvailable"
    }
}

private struct WireLearnResult: Decodable, Sendable {
    let recorded: Bool
    let useCount: UInt64
    let syncEligible: Bool

    enum CodingKeys: String, CodingKey {
        case recorded = "Recorded"
        case useCount = "UseCount"
        case syncEligible = "SyncEligible"
    }
}

private func decode<Result: Decodable & Sendable>(
    _ json: String,
    kind: GeneratedBindingPayloadKind
) throws -> WireEnvelope<Result> {
    do {
        try GeneratedBindingJSONBoundary.validate(json, kind: kind)
        return try JSONDecoder().decode(WireEnvelope<Result>.self, from: Data(json.utf8))
    } catch {
        throw SyncCoreBindingError.localState
    }
}

private func requireResult<Result: Decodable & Sendable>(_ envelope: WireEnvelope<Result>) throws -> Result {
    guard envelope.ok, let result = envelope.result else {
        throw syncCoreBindingError(forRedactedCode: envelope.errorCode)
    }
    return result
}
#endif
