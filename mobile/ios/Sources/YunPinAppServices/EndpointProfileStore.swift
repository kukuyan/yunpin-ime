// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(YunPinMobileCore)
import YunPinMobileCore
#endif

public enum EndpointProfileStoreError: Error, Equatable, Sendable {
    case endpointBindingImmutable
}

public actor EndpointProfileStore {
    private struct State: Codable, Sendable {
        let schema: Int
        var profiles: [SyncEndpointProfile]
        var selectedProfileID: UUID?
    }

    private let store: AtomicVersionedFileStore<State>
    private var state: State

    public init(directory: URL) throws {
        let store = try AtomicVersionedFileStore<State>(directory: directory, name: "endpoints")
        self.store = store
        let loaded = try store.load()
        self.state = loaded?.schema == 1 ? loaded! : State(schema: 1, profiles: [], selectedProfileID: nil)
    }

    public func profiles() -> [SyncEndpointProfile] { state.profiles }

    public func selected() -> SyncEndpointProfile? {
        guard let selectedProfileID = state.selectedProfileID else { return nil }
        return state.profiles.first { $0.id == selectedProfileID }
    }

    public func saveAndSelect(_ profile: SyncEndpointProfile) throws {
        if let index = state.profiles.firstIndex(where: { $0.id == profile.id }) {
            let existing = state.profiles[index]
            guard existing.endpoint == profile.endpoint,
                  existing.allowsPrivateHTTP == profile.allowsPrivateHTTP else {
                throw EndpointProfileStoreError.endpointBindingImmutable
            }
            // A display-only rename is safe. Changing a server or its transport
            // policy requires a new profile ID and separately paired credential.
            state.profiles[index] = profile
        } else {
            state.profiles.append(profile)
        }
        state.selectedProfileID = profile.id
        try store.save(state)
    }

    public func select(_ profileID: UUID) throws {
        guard state.profiles.contains(where: { $0.id == profileID }) else {
            throw EndpointProfileError.invalidURL
        }
        state.selectedProfileID = profileID
        try store.save(state)
    }
}
