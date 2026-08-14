// SPDX-License-Identifier: GPL-3.0-only

import Foundation

private var rimeOperationDepth = 0
private var deliveredMessages: [String] = []
private var deliveryOccurredInsideRime = false

// Model the production C callback boundary: transient C storage is copied
// synchronously, while all observable work crosses an asynchronous main-queue
// boundary.
private func notificationCallback(_ messageType: UnsafePointer<CChar>?) {
  let copiedMessageType = messageType.map { String(cString: $0) }
  DispatchQueue.main.async {
    if rimeOperationDepth != 0 {
      deliveryOccurredInsideRime = true
    }
    if let copiedMessageType {
      deliveredMessages.append(copiedMessageType)
    }
  }
}

private var transientBytes = Array("option\0".utf8).map { CChar(bitPattern: $0) }
rimeOperationDepth = 1
transientBytes.withUnsafeBufferPointer { buffer in
  notificationCallback(buffer.baseAddress)
}

precondition(deliveredMessages.isEmpty, "notification escaped synchronously from the Rime callback")

// Destroy the callback-owned bytes before the queued delivery. The copied
// Swift value must remain intact and no C pointer may be retained.
for index in transientBytes.indices {
  transientBytes[index] = CChar(bitPattern: 120)
}
rimeOperationDepth = 0

let deadline = Date().addingTimeInterval(2)
while deliveredMessages.isEmpty, Date() < deadline {
  RunLoop.main.run(mode: .default, before: Date().addingTimeInterval(0.01))
}

precondition(!deliveryOccurredInsideRime, "notification delivery re-entered an active Rime operation")
precondition(deliveredMessages == ["option"], "notification did not own its copied C-string value")
print("notification deferral harness: PASS")
