// SPDX-License-Identifier: Apache-2.0

import Foundation

public struct BackgroundRetryPolicy: Equatable, Sendable {
    public let baseDelaySeconds: TimeInterval
    public let maximumDelaySeconds: TimeInterval
    public let jitterFraction: Double

    public init(
        baseDelaySeconds: TimeInterval = 15 * 60,
        maximumDelaySeconds: TimeInterval = 6 * 60 * 60,
        jitterFraction: Double = 0.2
    ) {
        let base = max(1, baseDelaySeconds.isFinite ? baseDelaySeconds : 15 * 60)
        self.baseDelaySeconds = base
        self.maximumDelaySeconds = max(base, maximumDelaySeconds.isFinite ? maximumDelaySeconds : 6 * 60 * 60)
        self.jitterFraction = min(max(jitterFraction.isFinite ? jitterFraction : 0, 0), 0.5)
    }

    public func delay(failureCount: Int, jitterUnit: Double) -> TimeInterval {
        let exponent = min(max(failureCount - 1, 0), 30)
        let exponential = baseDelaySeconds * pow(2, Double(exponent))
        let capped = min(exponential.isFinite ? exponential : maximumDelaySeconds, maximumDelaySeconds)
        let boundedJitter = min(max(jitterUnit.isFinite ? jitterUnit : 0, -1), 1)
        return min(maximumDelaySeconds, max(1, capped * (1 + jitterFraction * boundedJitter)))
    }
}

/// Persists only retry counters and the already-jittered next delay. It stores
/// no endpoint, account, device, credential, phrase, or free-form error value.
public actor BackgroundSyncRetryController {
    private struct State: Codable, Sendable {
        let schema: Int
        var consecutiveFailures: Int
        var nextDelaySeconds: TimeInterval
    }

    private let policy: BackgroundRetryPolicy
    private let jitter: @Sendable (Int) -> Double
    private let store: AtomicVersionedFileStore<State>
    private var state: State

    public init(
        directory: URL,
        policy: BackgroundRetryPolicy = BackgroundRetryPolicy(),
        jitter: @escaping @Sendable (Int) -> Double = { _ in Double.random(in: -1...1) }
    ) throws {
        let store = try AtomicVersionedFileStore<State>(directory: directory, name: "background-retry")
        self.store = store
        self.policy = policy
        self.jitter = jitter
        if let loaded = try store.load(),
           loaded.schema == 1,
           (0...31).contains(loaded.consecutiveFailures),
           loaded.nextDelaySeconds.isFinite,
           (1...policy.maximumDelaySeconds).contains(loaded.nextDelaySeconds) {
            self.state = loaded
        } else {
            self.state = State(
                schema: 1,
                consecutiveFailures: 0,
                nextDelaySeconds: policy.delay(failureCount: 0, jitterUnit: 0)
            )
        }
    }

    public func currentDelay() -> TimeInterval { state.nextDelaySeconds }

    @discardableResult
    public func recordResult(success: Bool) throws -> TimeInterval {
        if success {
            state.consecutiveFailures = 0
            state.nextDelaySeconds = policy.delay(failureCount: 0, jitterUnit: 0)
        } else {
            state.consecutiveFailures = min(state.consecutiveFailures, 30) + 1
            state.nextDelaySeconds = policy.delay(
                failureCount: state.consecutiveFailures,
                jitterUnit: jitter(state.consecutiveFailures)
            )
        }
        try store.save(state)
        return state.nextDelaySeconds
    }
}
