// SPDX-License-Identifier: Apache-2.0

import Foundation
import CryptoKit
import Darwin

public enum PrivateSnapshotError: Error, Equatable, Sendable {
    case invalidRow
    case tooManyRows
    case tooLarge
    case unavailable
}

public struct ResolvedPrivateSnapshot: Equatable, Sendable {
    public let data: Data
    public let usedFallback: Bool

    public init(data: Data, usedFallback: Bool) {
        self.data = data
        self.usedFallback = usedFallback
    }
}

/// A content fingerprint can identify an exact validated snapshot generation
/// without retaining any phrase, path, account, device, or credential value.
public struct ValidatedSnapshotFingerprint: Codable, Equatable, Sendable {
    public let sha256Hex: String

    fileprivate init(sha256Hex: String) {
        self.sha256Hex = sha256Hex
    }
}

public enum ExactSnapshotState: Equatable, Sendable {
    case absent
    case present(ValidatedSnapshotFingerprint)
}

/// Validator for the immutable file produced by `mobile/synccore`. Swift does
/// not build, merge, or rewrite this private data.
public enum PrivateSnapshotValidator {
    public static let header = "phrase\tpinyin\tsource\tuse_count\tpinned\n"
    public static let maximumBytes = 64 << 20
    public static let maximumRows = 100_000

    @discardableResult
    public static func validate(_ data: Data) throws -> Int {
        guard !data.isEmpty, data.last == 0x0a else {
            throw PrivateSnapshotError.invalidRow
        }
        guard data.count <= maximumBytes,
              let text = String(data: data, encoding: .utf8) else {
            throw PrivateSnapshotError.tooLarge
        }
        var lines = text.split(separator: "\n", omittingEmptySubsequences: false)
        guard !lines.isEmpty, String(lines.removeFirst()) + "\n" == header else {
            throw PrivateSnapshotError.invalidRow
        }
        var count = 0
        var identities = Set<String>()
        for line in lines where !line.isEmpty && !line.hasPrefix("#") {
            let fields = line.split(separator: "\t", omittingEmptySubsequences: false)
            guard fields.count == 5,
                  validPhrase(String(fields[0])),
                  validPinyin(String(fields[1])),
                  validLearningSource(String(fields[2])),
                  UInt64(fields[3]).map({ $0 > 0 }) == true,
                  fields[4] == "true" || fields[4] == "false" else {
                throw PrivateSnapshotError.invalidRow
            }
            let identity = String(fields[0]) + "\0" + String(fields[1])
            guard identities.insert(identity).inserted else {
                throw PrivateSnapshotError.invalidRow
            }
            count += 1
            guard count <= maximumRows else { throw PrivateSnapshotError.tooManyRows }
        }
        return count
    }

    private static func validPhrase(_ value: String) -> Bool {
        guard !value.isEmpty, value.utf8.count <= 512 else { return false }
        return !value.unicodeScalars.contains { scalar in
            scalar.value < 0x20
                || scalar.value == 0x7f
                || (0x80...0x9f).contains(scalar.value)
                || (0x202a...0x202e).contains(scalar.value)
                || (0x2066...0x2069).contains(scalar.value)
        }
    }

    private static func validPinyin(_ value: String) -> Bool {
        !value.isEmpty && value.utf8.count <= 256 && value.utf8.allSatisfy { byte in
            (97...122).contains(byte) || byte == 32 || byte == 39 || byte == 45
        }
    }

    private static func validLearningSource(_ value: String) -> Bool {
        if value == "synced_learning" { return true }
        let prefix = "synced_learning@"
        guard value.hasPrefix(prefix) else {
            return false
        }
        let suffix = value.dropFirst(prefix.count)
        guard !suffix.isEmpty,
              suffix.utf8.allSatisfy({ (48...57).contains($0) }),
              let day = Int64(suffix) else { return false }
        return day > 0
    }
}

/// Reads exactly one bounded regular file. Unlike the keyboard resolver, this
/// inspector never follows a `.rollback` fallback.
public enum ExactSnapshotInspector {
    public static func inspect(at url: URL) throws -> ValidatedSnapshotFingerprint {
        let data = try SnapshotFileReader.validatedData(at: url)
        let digest = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        return ValidatedSnapshotFingerprint(sha256Hex: digest)
    }

    /// Distinguishes a genuinely absent first-generation snapshot from an
    /// invalid, oversized, or symlinked file. Only ENOENT is a legal empty
    /// state; every other filesystem or validation failure remains closed.
    public static func inspectState(at url: URL) throws -> ExactSnapshotState {
        guard url.isFileURL, url.path.hasPrefix("/") else {
            throw PrivateSnapshotError.unavailable
        }
        var metadata = stat()
        let result = url.withUnsafeFileSystemRepresentation { representation -> Int32 in
            guard let representation else { return -1 }
            return lstat(representation, &metadata)
        }
        if result == -1 {
            guard errno == ENOENT else { throw PrivateSnapshotError.unavailable }
            return .absent
        }
        return .present(try inspect(at: url))
    }
}

/// Read-only keyboard resolver. It never promotes or deletes files. When the
/// current snapshot is invalid it may load the sync core's retained `.rollback`
/// file in memory; only the containing app may request a persistent rollback.
public struct ReadOnlySnapshotResolver: Sendable {
    private let snapshotURL: URL

    public init(snapshotURL: URL) {
        self.snapshotURL = snapshotURL
    }

    public func resolve() throws -> ResolvedPrivateSnapshot {
        if let current = try? SnapshotFileReader.validatedData(at: snapshotURL) {
            return ResolvedPrivateSnapshot(data: current, usedFallback: false)
        }
        let fallbackURL = URL(fileURLWithPath: snapshotURL.path + ".rollback")
        if let fallback = try? SnapshotFileReader.validatedData(at: fallbackURL) {
            return ResolvedPrivateSnapshot(data: fallback, usedFallback: true)
        }
        throw PrivateSnapshotError.unavailable
    }
}

private enum SnapshotFileReader {
    static func validatedData(at url: URL) throws -> Data {
        let values = try url.resourceValues(forKeys: [.isRegularFileKey, .isSymbolicLinkKey, .fileSizeKey])
        guard values.isRegularFile == true,
              values.isSymbolicLink != true,
              (values.fileSize ?? maximumSentinel) <= PrivateSnapshotValidator.maximumBytes else {
            throw PrivateSnapshotError.unavailable
        }
        let data = try Data(contentsOf: url, options: [.mappedIfSafe])
        _ = try PrivateSnapshotValidator.validate(data)
        return data
    }

    private static let maximumSentinel = PrivateSnapshotValidator.maximumBytes + 1
}
