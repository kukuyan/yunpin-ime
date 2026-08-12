// SPDX-License-Identifier: GPL-3.0-only

// Synthetic model for the Squirrel operation lease.  The production class is
// private to the InputMethodKit target; integration tests additionally inspect
// that source for the same boundary and critical commit ordering.
private final class SessionLifetime {
  private(set) var session: UInt64 = 0
  private var operationDepth: UInt = 0
  private var destroyRequested = false
  private let destroy: (UInt64) -> Void

  init(destroy: @escaping (UInt64) -> Void) {
    self.destroy = destroy
  }

  func install(_ value: UInt64) {
    precondition(operationDepth == 0)
    if session != 0, session != value {
      destroy(session)
    }
    session = value
    destroyRequested = false
  }

  func withSessionOperation<Result>(
    _ operation: (UInt64) -> Result
  ) -> Result? {
    operationDepth += 1
    defer {
      operationDepth -= 1
      if operationDepth == 0, destroyRequested {
        destroyNow()
      }
    }
    guard session != 0 else { return nil }
    return operation(session)
  }

  func requestDestroy() {
    if operationDepth == 0 {
      destroyNow()
    } else {
      destroyRequested = true
    }
  }

  private func destroyNow() {
    let doomed = session
    session = 0
    destroyRequested = false
    if doomed != 0 {
      destroy(doomed)
    }
  }
}

for cycle in 0..<256 {
  var trace: [String] = []
  let lifetime = SessionLifetime { session in
    trace.append("destroy:\(session)")
  }
  lifetime.install(UInt64(cycle + 1))

  let result = lifetime.withSessionOperation { outerSession in
    trace.append("read:\(outerSession)")
    _ = lifetime.withSessionOperation { nestedSession in
      trace.append("insert:\(nestedSession)")
      // Model an IMK client callback that releases its controller and asks
      // deinit to destroy the session before commitComposition resumes.
      lifetime.requestDestroy()
      precondition(lifetime.session == nestedSession)
      trace.append("clear:\(nestedSession)")
    }
    precondition(lifetime.session == outerSession)
    trace.append("free:\(outerSession)")
    return true
  }

  precondition(result == true)
  precondition(lifetime.session == 0)
  precondition(trace == [
    "read:\(cycle + 1)",
    "insert:\(cycle + 1)",
    "clear:\(cycle + 1)",
    "free:\(cycle + 1)",
    "destroy:\(cycle + 1)",
  ])
}

// Rime session ids are pointer values.  CleanupAllSessions() can free every
// session and the allocator may immediately reuse one controller's old value
// for another controller's new session.  Maintenance must therefore clear all
// controller-owned ids before asking librime to perform that global cleanup;
// otherwise a later install() can destroy a different controller's live,
// address-reused session.
private var maintenanceTrace: [String] = []
private let first = SessionLifetime { session in
  maintenanceTrace.append("destroy:first:\(session)")
}
private let second = SessionLifetime { session in
  maintenanceTrace.append("destroy:second:\(session)")
}

first.install(101)
second.install(202)

// Model prepareForUserDataMaintenance() invalidating the complete controller
// snapshot on the main thread before RimeSyncUserData() calls CleanupAllSessions().
first.requestDestroy()
second.requestDestroy()
precondition(first.session == 0)
precondition(second.session == 0)
precondition(maintenanceTrace == [
  "destroy:first:101",
  "destroy:second:202",
])

// Reuse both pointer-shaped ids in the opposite controller order.  Because
// the owners were cleared first, install() must not destroy either live id.
first.install(202)
second.install(101)
precondition(first.session == 202)
precondition(second.session == 101)
precondition(maintenanceTrace.count == 2)
