// SPDX-License-Identifier: Apache-2.0

import BackgroundTasks
import UIKit

final class YunPinAppDelegate: NSObject, UIApplicationDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        BGTaskScheduler.shared.register(
            forTaskWithIdentifier: YunPinAppConfiguration.backgroundTaskIdentifier,
            using: nil
        ) { task in
            guard let processingTask = task as? BGProcessingTask else {
                task.setTaskCompleted(success: false)
                Task { @MainActor in
                    let delay = await YunPinAppModel.shared.recordBackgroundResult(success: false)
                    Self.scheduleNextBackgroundSync(after: delay)
                }
                return
            }
            let work = Task { @MainActor in
                let status = await YunPinAppModel.shared.backgroundSync()
                let delay = await YunPinAppModel.shared.recordBackgroundResult(success: status)
                processingTask.setTaskCompleted(success: status)
                Self.scheduleNextBackgroundSync(after: delay)
            }
            processingTask.expirationHandler = {
                work.cancel()
                Task { @MainActor in
                    await YunPinAppModel.shared.cancelCurrentSync()
                }
            }
        }
        return true
    }

    func applicationDidEnterBackground(_ application: UIApplication) {
        Task { @MainActor in
            let delay = await YunPinAppModel.shared.nextBackgroundDelay()
            Self.scheduleNextBackgroundSync(after: delay)
        }
    }

    private static func scheduleNextBackgroundSync(after delay: TimeInterval) {
        let request = BGProcessingTaskRequest(identifier: YunPinAppConfiguration.backgroundTaskIdentifier)
        request.requiresNetworkConnectivity = true
        request.requiresExternalPower = false
        request.earliestBeginDate = Date(timeIntervalSinceNow: max(1, delay))
        try? BGTaskScheduler.shared.submit(request)
    }
}
