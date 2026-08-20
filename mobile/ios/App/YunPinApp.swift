// SPDX-License-Identifier: Apache-2.0

import SwiftUI

@main
struct YunPinApp: App {
    @UIApplicationDelegateAdaptor(YunPinAppDelegate.self) private var appDelegate
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var model = YunPinAppModel.shared

    var body: some Scene {
        WindowGroup {
            NavigationStack {
                Form {
                    Section("同步服务器") {
                        TextField("显示名称", text: $model.displayName)
                        TextField("HTTPS 地址", text: $model.endpointText)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .keyboardType(.URL)
                        Toggle("允许私网 IP 明文 HTTP", isOn: $model.allowsPrivateHTTP)
                        Button("保存并选用") { model.saveEndpoint() }
                    }

                    if !model.profiles.isEmpty {
                        Section("已保存服务器") {
                            ForEach(model.profiles, id: \.id) { profile in
                                Button {
                                    model.select(profile)
                                } label: {
                                    HStack {
                                        Text(profile.displayName)
                                        Spacer()
                                        if profile.id == model.selectedProfileID {
                                            Image(systemName: "checkmark")
                                        }
                                    }
                                }
                            }
                        }
                    }

                    Section("同步状态") {
                        LabeledContent("状态", value: model.statusText)
                        LabeledContent("待同步", value: String(model.pendingCount))
                        LabeledContent("游标", value: String(model.cursor))
                        Button("立即同步") { model.syncNow() }
                    }

                    Section("安全边界") {
                        Text("凭据仅存于主应用 Keychain；键盘扩展无法读取。账户配对和签名多设备名册仍是人工门禁，本客户端不会创建或显示恢复密钥。")
                        Text("iOS 不保证键盘扩展持续后台运行；同步只由主应用激活或 BGTask 触发。")
                    }
                }
                .navigationTitle("YunPin")
                .alert("无法保存", isPresented: $model.showsEndpointError) {
                    Button("好", role: .cancel) {}
                } message: {
                    Text("请检查服务器名称、地址与私网 HTTP 选项。")
                }
            }
            .task { await model.applicationBecameActive() }
        }
        .onChange(of: scenePhase) { _, newPhase in
            if newPhase == .active {
                Task { await model.applicationBecameActive() }
            }
        }
    }
}
