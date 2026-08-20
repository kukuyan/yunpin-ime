// SPDX-License-Identifier: Apache-2.0

import Foundation

public enum VersionedStoreError: Error, Equatable, Sendable {
    case unsafePath
    case noValidGeneration
}

/// Keeps one last-known-good encoded value beside the current value. Writers
/// are expected to be serialized by their owning actor.
public struct AtomicVersionedFileStore<Value: Codable & Sendable>: Sendable {
    private let directory: URL
    private let currentURL: URL
    private let previousURL: URL
    private let encoder: JSONEncoder
    private let decoder: JSONDecoder

    public init(directory: URL, name: String) throws {
        guard directory.isFileURL,
              !name.isEmpty,
              name.utf8.allSatisfy({ byte in
                  (65...90).contains(byte)
                      || (97...122).contains(byte)
                      || (48...57).contains(byte)
                      || byte == 45
                      || byte == 95
              }) else {
            throw VersionedStoreError.unsafePath
        }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let directoryValues = try directory.resourceValues(forKeys: [.isDirectoryKey, .isSymbolicLinkKey])
        guard directoryValues.isDirectory == true,
              directoryValues.isSymbolicLink != true else {
            throw VersionedStoreError.unsafePath
        }
        self.directory = directory
        self.currentURL = directory.appendingPathComponent("\(name).json")
        self.previousURL = directory.appendingPathComponent("\(name).previous.json")
        self.encoder = JSONEncoder.yunPinStable
        self.decoder = JSONDecoder.yunPinStrictDates
    }

    public func load() throws -> Value? {
        if let current = try? decode(currentURL) { return current }
        if let previous = try? decode(previousURL) { return previous }
        if !FileManager.default.fileExists(atPath: currentURL.path),
           !FileManager.default.fileExists(atPath: previousURL.path) {
            return nil
        }
        throw VersionedStoreError.noValidGeneration
    }

    public func save(_ value: Value) throws {
        let data = try encoder.encode(value)
        _ = try decoder.decode(Value.self, from: data)
        if FileManager.default.fileExists(atPath: currentURL.path) {
            // Only a semantically decodable current generation may replace the
            // retained last-good value. Corrupt JSON must not poison rollback.
            if let current = try? validatedEncodedData(at: currentURL) {
                try current.write(to: previousURL, options: [.atomic])
                try applyProtection(to: previousURL)
            }
        }
        try data.write(to: currentURL, options: [.atomic])
        try applyProtection(to: currentURL)
    }

    private func decode(_ url: URL) throws -> Value {
        try decoder.decode(Value.self, from: validatedEncodedData(at: url))
    }

    private func validatedEncodedData(at url: URL) throws -> Data {
        let data = try validatedData(at: url, maximum: 8 << 20)
        _ = try decoder.decode(Value.self, from: data)
        return data
    }

    private func validatedData(at url: URL, maximum: Int) throws -> Data {
        let values = try url.resourceValues(forKeys: [.isRegularFileKey, .isSymbolicLinkKey, .fileSizeKey])
        guard values.isRegularFile == true,
              values.isSymbolicLink != true,
              (values.fileSize ?? maximum + 1) <= maximum else {
            throw VersionedStoreError.unsafePath
        }
        let data = try Data(contentsOf: url)
        guard data.count <= maximum else { throw VersionedStoreError.unsafePath }
        return data
    }

    private func applyProtection(to url: URL) throws {
        #if os(iOS)
        try FileManager.default.setAttributes(
            [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
            ofItemAtPath: url.path
        )
        #endif
    }
}

extension JSONEncoder {
    static var yunPinStable: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        return encoder
    }
}

extension JSONDecoder {
    static var yunPinStrictDates: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return decoder
    }
}
