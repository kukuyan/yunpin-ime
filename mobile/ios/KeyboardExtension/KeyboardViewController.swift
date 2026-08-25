// SPDX-License-Identifier: Apache-2.0

import UIKit
#if canImport(YunPinKeyboardCore)
import YunPinKeyboardCore
#endif

@objc(KeyboardViewController)
final class KeyboardViewController: UIInputViewController {
    private let statusLabel = UILabel()

    override func viewDidLoad() {
        super.viewDidLoad()
        configureView()
        refreshPrivateSnapshotAvailability()
    }

    private func configureView() {
        let nextKeyboard = UIButton(type: .system)
        nextKeyboard.setTitle("切换键盘", for: .normal)
        nextKeyboard.addTarget(self, action: #selector(handleInputModeList(from:with:)), for: .allTouchEvents)

        let space = UIButton(type: .system)
        space.setTitle("空格", for: .normal)
        space.addAction(UIAction { [weak self] _ in self?.textDocumentProxy.insertText(" ") }, for: .touchUpInside)

        statusLabel.textAlignment = .center
        statusLabel.numberOfLines = 2
        statusLabel.font = .preferredFont(forTextStyle: .caption1)

        let stack = UIStackView(arrangedSubviews: [statusLabel, space, nextKeyboard])
        stack.axis = .vertical
        stack.spacing = 8
        stack.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 12),
            stack.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -12),
            stack.topAnchor.constraint(equalTo: view.topAnchor, constant: 8),
            stack.bottomAnchor.constraint(equalTo: view.bottomAnchor, constant: -8),
        ])
    }

    private func refreshPrivateSnapshotAvailability() {
        // iOS replaces custom keyboards in secure text fields. RequestsOpenAccess
        // is false in this target, so hasFullAccess stays false and App Group
        // data is never read: the default build exposes zero private candidates.
        let context = KeyboardPrivacyContext(
            hasFullAccess: hasFullAccess,
            isSecureTextContext: false,
            isIncognitoContext: false
        )
        guard KeyboardPrivacyPolicy.permitsPrivateCandidates(in: context) else {
            statusLabel.text = "基础键盘模式 · 私人候选关闭"
            return
        }
        // This RequestsOpenAccess=false target has no selected-profile handoff
        // and never attempts App Group access. A future reviewed Full Access
        // build must supply an explicit profile UUID to the read-only helper.
        statusLabel.text = "基础键盘模式 · 无可用快照"
    }
}
