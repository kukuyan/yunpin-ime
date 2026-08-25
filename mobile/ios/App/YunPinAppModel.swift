// SPDX-License-Identifier: Apache-2.0

import Foundation
import SwiftUI
#if canImport(YunPinAppServices)
import YunPinAppServices
#endif
#if canImport(YunPinMobileCore)
import YunPinMobileCore
#endif

@MainActor
final class YunPinAppModel: ObservableObject {
    static let shared = YunPinAppModel()

    @Published var displayName = ""
    @Published var endpointText = ""
    @Published var allowsPrivateHTTP = false
    @Published private(set) var profiles: [SyncEndpointProfile] = []
    @Published private(set) var selectedProfileID: UUID?
    @Published private(set) var statusText = "等待配置"
    @Published private(set) var pendingCount: UInt64 = 0
    @Published private(set) var cursor: Int64 = 0
    @Published var showsEndpointError = false

    private var endpointStore: EndpointProfileStore?
    private var coordinator: MobileSyncCoordinator?
    private var pathResolver: SyncCorePathResolver?
    private var backgroundRetryController: BackgroundSyncRetryController?
    private var upgradeController: UpgradeRollbackController?
    private var preparedProfileID: UUID?
    private var pendingHealthBuild: String?
    private var upgradeRecoveryGated = false
    private let upgradePreparation = UpgradePreparationSingleFlight()

    private init() {
        do {
            let fileManager = FileManager.default
            guard let appGroup = fileManager.containerURL(
                forSecurityApplicationGroupIdentifier: YunPinAppConfiguration.appGroupIdentifier
            ) else {
                throw SyncCoreBindingError.invalidConfiguration
            }
            let appState = try Self.applicationStateDirectory(fileManager: fileManager)
            let paths = try SyncCorePathResolver(
                databaseRoot: appState.appendingPathComponent("Profiles", isDirectory: true),
                snapshotRoot: appGroup
                    .appendingPathComponent("PrivateSnapshots", isDirectory: true)
                    .appendingPathComponent("Profiles", isDirectory: true)
            )

            let endpoints = try EndpointProfileStore(directory: appState)
            let statuses = try RedactedSyncStatusStore(directory: appState)
            let credentials = KeychainCredentialStore(
                service: Bundle.main.bundleIdentifier ?? "io.github.kukuyan.yunpin.ios"
            )
            self.endpointStore = endpoints
            self.pathResolver = paths
            self.backgroundRetryController = try BackgroundSyncRetryController(directory: appState)
            self.coordinator = MobileSyncCoordinator(
                endpoints: endpoints,
                credentials: credentials,
                binding: SyncCoreBindingFactory.production(),
                pathResolver: paths,
                statuses: statuses
            )
            Task { await reloadProfiles() }
        } catch {
            statusText = "本地安全存储不可用"
        }
    }

    func saveEndpoint() {
        guard let endpointStore else { return }
        do {
            let profile = try SyncEndpointProfile(
                displayName: displayName,
                endpoint: endpointText,
                allowsPrivateHTTP: allowsPrivateHTTP
            )
            Task {
                do {
                    try await endpointStore.saveAndSelect(profile)
                    await reloadProfiles()
                } catch {
                    showsEndpointError = true
                }
            }
        } catch {
            showsEndpointError = true
        }
    }

    func select(_ profile: SyncEndpointProfile) {
        guard let endpointStore else { return }
        Task {
            do {
                try await endpointStore.select(profile.id)
                await reloadProfiles()
            } catch {
                showsEndpointError = true
            }
        }
    }

    func syncNow() {
        Task { await runLifecycleSync(trigger: .manual) }
    }

    func applicationBecameActive() async {
        await runLifecycleSync(trigger: .appActivation)
    }

    func backgroundSync() async -> Bool {
        let status = await runLifecycleSync(trigger: .backgroundTask)
        return status.phase == .waiting || (status.phase == .idle && pendingHealthBuild == nil)
    }

    func cancelCurrentSync() async {
        guard let coordinator else { return }
        await coordinator.cancelCurrentOperation()
    }

    func nextBackgroundDelay() async -> TimeInterval {
        guard let backgroundRetryController else { return 15 * 60 }
        return await backgroundRetryController.currentDelay()
    }

    func recordBackgroundResult(success: Bool) async -> TimeInterval {
        guard let backgroundRetryController else { return 15 * 60 }
        return (try? await backgroundRetryController.recordResult(success: success)) ?? 15 * 60
    }

    @discardableResult
    private func runLifecycleSync(trigger: SyncTrigger) async -> RedactedSyncStatus {
        let preparationAllowsDataPlane = await FailClosedUpgradePreparation.runIfPrepared(
            preparation: { try await self.prepareUpgradeRecoveryOnce() },
            dataPlane: {
                guard !self.upgradeRecoveryGated else { return false }
                guard let selected = await self.endpointStore?.selected() else {
                    return self.preparedProfileID == nil
                }
                return self.preparedProfileID == selected.id
            }
        )
        guard preparationAllowsDataPlane == true else {
            return RedactedSyncStatus(
                phase: .blocked,
                code: .protocolViolation,
                pendingEnvelopeCount: pendingCount,
                cursor: cursor
            )
        }
        await markUpgradeHealthyFromLocalState()
        let expectedProfileID = preparedProfileID
        if let expectedProfileID,
           await endpointStore?.selected()?.id != expectedProfileID {
            return RedactedSyncStatus(
                phase: .blocked,
                code: .protocolViolation,
                pendingEnvelopeCount: pendingCount,
                cursor: cursor
            )
        }
        let status: RedactedSyncStatus
        if let coordinator {
            status = await coordinator.synchronize(
                trigger: trigger,
                expectedProfileID: expectedProfileID
            )
            apply(status)
        } else {
            status = RedactedSyncStatus(
                phase: .failed,
                code: .storage,
                pendingEnvelopeCount: pendingCount,
                cursor: cursor
            )
            apply(status)
        }
        // A first sync may create the initial snapshot. Re-check locally after
        // success, but never use relay success itself as the health marker.
        if status.phase == .idle, pendingHealthBuild != nil {
            await markUpgradeHealthyFromLocalState()
        }
        return status
    }

    private func reloadProfiles() async {
        guard let endpointStore else { return }
        profiles = await endpointStore.profiles()
        let selected = await endpointStore.selected()
        selectedProfileID = selected?.id
        if let selected {
            displayName = selected.displayName
            endpointText = selected.endpoint.absoluteString
            allowsPrivateHTTP = selected.allowsPrivateHTTP
        }
    }

    private func prepareUpgradeRecoveryOnce() async throws {
        guard let endpointStore,
              let selected = await endpointStore.selected() else { return }
        try await upgradePreparation.prepare(profileID: selected.id) { @MainActor [weak self] in
            guard let self else { throw SyncCoreBindingError.localState }
            try await self.performUpgradePreparation(for: selected)
        }
        guard preparedProfileID == selected.id,
              await endpointStore.selected()?.id == selected.id else {
            throw MobileSyncError.profileChanged
        }
    }

    private func performUpgradePreparation(for selected: SyncEndpointProfile) async throws {
        guard let endpointStore,
              let pathResolver,
              let coordinator else {
            throw SyncCoreBindingError.invalidConfiguration
        }
        // In-progress is deliberately not published as prepared. Every
        // concurrent lifecycle callback awaits UpgradePreparationSingleFlight.
        preparedProfileID = nil
        pendingHealthBuild = nil
        upgradeRecoveryGated = true
        upgradeController = nil
        let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "unknown"
        do {
            let resolvedPaths = try pathResolver.paths(for: selected.id)
            let snapshotURL = resolvedPaths.privateSnapshotURL
            let upgradeController = try UpgradeRollbackController(
                directory: try pathResolver.privateStateDirectory(for: selected.id),
                profileID: selected.id
            )
            self.upgradeController = upgradeController
            let rollbackURL = URL(fileURLWithPath: snapshotURL.path + ".rollback")
            let rollbackFingerprint = await Self.exactSnapshotFingerprint(at: rollbackURL)
            guard await endpointStore.selected()?.id == selected.id else {
                throw MobileSyncError.profileChanged
            }
            let plan = try await upgradeController.beginLaunch(
                expectedProfileID: selected.id,
                build: build,
                validatedRollback: rollbackFingerprint
            )
            guard await endpointStore.selected()?.id == selected.id else {
                throw MobileSyncError.profileChanged
            }
            if plan.gate != nil {
                preparedProfileID = selected.id
                upgradeRecoveryGated = true
                statusText = "升级恢复需要匹配的有效快照"
                return
            }
            if plan.shouldRestoreSnapshot {
                guard let rollbackFingerprint else {
                    upgradeRecoveryGated = true
                    statusText = "升级恢复需要匹配的有效快照"
                    return
                }
                try await coordinator.rollbackSnapshot(expectedProfileID: selected.id)
                let restored = await Self.exactSnapshotFingerprint(at: snapshotURL)
                guard await endpointStore.selected()?.id == selected.id,
                      restored == rollbackFingerprint else {
                    upgradeRecoveryGated = true
                    statusText = "升级恢复快照校验失败"
                    return
                }
            }
            guard await endpointStore.selected()?.id == selected.id else {
                throw MobileSyncError.profileChanged
            }
            // Keep the build pending until a network-free shared-core status
            // call and exact current snapshot validation both succeed.
            preparedProfileID = selected.id
            pendingHealthBuild = build
            upgradeRecoveryGated = false
        } catch {
            // Never retain an "already prepared" identity after journal/path
            // failure. The current lifecycle remains gated, while a later
            // explicit activation may retry the complete local preparation.
            preparedProfileID = nil
            pendingHealthBuild = nil
            upgradeController = nil
            upgradeRecoveryGated = true
            statusText = "升级恢复等待共享核心"
            throw error
        }
    }

    private func markUpgradeHealthyFromLocalState() async {
        guard let build = pendingHealthBuild,
              let upgradeController,
              let coordinator,
              let expectedProfileID = preparedProfileID else { return }
        // Facade.Status, exact current-file validation, and the atomic journal
        // commit share one coordinator single-flight transaction.
        if await ProfileBoundUpgradeHealthVerifier.markHealthy(
            expectedProfileID: expectedProfileID,
            build: build,
            coordinator: coordinator,
            controller: upgradeController
        ) {
            pendingHealthBuild = nil
        }
    }

    private static func exactSnapshotFingerprint(at url: URL) async -> ValidatedSnapshotFingerprint? {
        await Task.detached(priority: .utility) {
            try? ExactSnapshotInspector.inspect(at: url)
        }.value
    }

    private func apply(_ status: RedactedSyncStatus) {
        pendingCount = status.pendingEnvelopeCount
        cursor = status.cursor
        switch (status.phase, status.code) {
        case (.idle, _): statusText = "已同步"
        case (.syncing, _): statusText = "同步中"
        case (.waiting, .endpointMissing): statusText = "等待服务器配置"
        case (.waiting, .credentialMissing): statusText = "等待已配对凭据"
        case (.blocked, .syncCoreUnavailable): statusText = "等待共享核心绑定"
        case (.blocked, .authenticationRejected): statusText = "需要重新授权"
        case (.blocked, .sequenceConflict): statusText = "序列冲突，已停止"
        case (.blocked, .operationBusy): statusText = "同步操作进行中"
        case (_, .connectivity): statusText = "网络暂不可用"
        case (_, .cancelled): statusText = "同步已取消"
        default: statusText = "同步失败（已脱敏）"
        }
    }

    private static func applicationStateDirectory(fileManager: FileManager) throws -> URL {
        guard let base = fileManager.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else {
            throw SyncCoreBindingError.invalidConfiguration
        }
        let directory = base.appendingPathComponent("YunPin", isDirectory: true)
        try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
        var mutableDirectory = directory
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try mutableDirectory.setResourceValues(values)
        let readback = try directory.resourceValues(forKeys: [.isExcludedFromBackupKey])
        guard readback.isExcludedFromBackup == true else {
            throw SyncCoreBindingError.invalidConfiguration
        }
        return directory
    }
}
