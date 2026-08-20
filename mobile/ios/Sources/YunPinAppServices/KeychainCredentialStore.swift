// SPDX-License-Identifier: Apache-2.0

import Foundation
import Security

public enum CredentialStoreError: Error, Equatable, Sendable {
    case invalidBlob
    case unavailable(OSStatus)
}

public protocol OpaqueCredentialStore: Sendable {
    func load(for profileID: UUID) async throws -> Data?
    func save(_ credentialBlob: Data, for profileID: UUID) async throws
}

/// App-only credential storage. The blob's fields are interpreted only by the
/// shared sync core; this type never creates account/recovery material.
/// Each endpoint profile has a distinct Keychain account, preventing a blob
/// from being reused after another server profile is selected. No Keychain
/// access group is set, so the keyboard extension cannot read it.
public actor KeychainCredentialStore: OpaqueCredentialStore {
    private let service: String

    public init(service: String) {
        self.service = service
    }

    public func load(for profileID: UUID) throws -> Data? {
        var query = baseQuery(for: profileID)
        query[kSecReturnData] = kCFBooleanTrue
        query[kSecMatchLimit] = kSecMatchLimitOne
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = item as? Data else {
            throw CredentialStoreError.unavailable(status)
        }
        return data
    }

    public func save(_ credentialBlob: Data, for profileID: UUID) throws {
        guard !credentialBlob.isEmpty, credentialBlob.count <= 64 << 10 else {
            throw CredentialStoreError.invalidBlob
        }
        let attributes: [CFString: Any] = [
            kSecValueData: credentialBlob,
            kSecAttrAccessible: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
            kSecAttrSynchronizable: kCFBooleanFalse as Any,
        ]
        let query = baseQuery(for: profileID)
        let updateStatus = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if updateStatus == errSecSuccess { return }
        guard updateStatus == errSecItemNotFound else {
            throw CredentialStoreError.unavailable(updateStatus)
        }
        var insert = query
        for (key, value) in attributes { insert[key] = value }
        let insertStatus = SecItemAdd(insert as CFDictionary, nil)
        guard insertStatus == errSecSuccess else {
            throw CredentialStoreError.unavailable(insertStatus)
        }
    }

    private func baseQuery(for profileID: UUID) -> [CFString: Any] {
        [
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrAccount: "endpoint-profile-v1:\(profileID.uuidString.lowercased())",
            kSecAttrSynchronizable: kCFBooleanFalse as Any,
        ]
    }
}
