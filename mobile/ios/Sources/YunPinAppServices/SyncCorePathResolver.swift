// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Derives isolated app-private and App Group paths from the selected profile.
/// No database or snapshot is ever reused across profile UUIDs.
public struct SyncCorePathResolver: Sendable {
    private let databaseRoot: URL
    private let snapshotRoot: URL
    private let directoryPreparer: @Sendable (URL) throws -> Void

    public init(databaseRoot: URL, snapshotRoot: URL) throws {
        try self.init(
            databaseRoot: databaseRoot,
            snapshotRoot: snapshotRoot,
            preparingDirectory: Self.prepareNonBackupDirectory
        )
    }

    #if os(macOS)
    /// Host-test seam for filesystems that do not implement Apple's iOS
    /// backup-exclusion resource key. It is not compiled into the iOS app.
    init(
        databaseRoot: URL,
        snapshotRoot: URL,
        directoryPreparer: @escaping @Sendable (URL) throws -> Void
    ) throws {
        try self.init(
            databaseRoot: databaseRoot,
            snapshotRoot: snapshotRoot,
            preparingDirectory: directoryPreparer
        )
    }
    #endif

    private init(
        databaseRoot: URL,
        snapshotRoot: URL,
        preparingDirectory directoryPreparer: @escaping @Sendable (URL) throws -> Void
    ) throws {
        guard databaseRoot.isFileURL,
              snapshotRoot.isFileURL,
              databaseRoot.path.hasPrefix("/"),
              snapshotRoot.path.hasPrefix("/"),
              databaseRoot.standardizedFileURL != snapshotRoot.standardizedFileURL else {
            throw SyncCoreBindingError.invalidConfiguration
        }
        try directoryPreparer(databaseRoot)
        try directoryPreparer(snapshotRoot)
        self.databaseRoot = databaseRoot
        self.snapshotRoot = snapshotRoot
        self.directoryPreparer = directoryPreparer
    }

    public func paths(for profileID: UUID) throws -> SyncCorePaths {
        let component = profileID.uuidString.lowercased()
        let databaseDirectory = try privateStateDirectory(for: profileID)
        let snapshotDirectory = snapshotRoot
            .appendingPathComponent(component, isDirectory: true)
        try directoryPreparer(snapshotDirectory)
        return try SyncCorePaths(
            encryptedDatabaseURL: databaseDirectory
                .appendingPathComponent("mobile.db", isDirectory: false),
            privateSnapshotURL: snapshotDirectory
                .appendingPathComponent("private.tsv", isDirectory: false)
        )
    }

    public func privateStateDirectory(for profileID: UUID) throws -> URL {
        let directory = databaseRoot
            .appendingPathComponent(profileID.uuidString.lowercased(), isDirectory: true)
        try directoryPreparer(directory)
        return directory
    }

    private static func prepareNonBackupDirectory(_ directory: URL) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let identity = try directory.resourceValues(forKeys: [.isDirectoryKey, .isSymbolicLinkKey])
        guard identity.isDirectory == true, identity.isSymbolicLink != true else {
            throw SyncCoreBindingError.invalidConfiguration
        }
        var mutableDirectory = directory
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try mutableDirectory.setResourceValues(values)
        let readback = try directory.resourceValues(forKeys: [.isExcludedFromBackupKey])
        guard readback.isExcludedFromBackup == true else {
            throw SyncCoreBindingError.invalidConfiguration
        }
    }
}
