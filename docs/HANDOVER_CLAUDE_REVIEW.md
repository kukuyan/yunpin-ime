# YunPin IME 交接文档（供 Claude Code 评审）

> **SUPERSEDED / 已废弃：请勿把本文作为当前实现状态。** 这是早期送审快照；当前评审入口是 [`HANDOVER_CODEX_FROM_REVIEW.md`](HANDOVER_CODEX_FROM_REVIEW.md)。本文所述表情动作候选、Bing 打开和 JSONL 收藏写入均已从当前行为移除；表情搜索/收藏现为 fail-closed 延期功能，普通魔法前缀文本只按文本处理。

本文用于把当前工作转交给 Claude Code 做同行评审，聚焦“开发预览阶段当前能否继续推进到稳定版”。

## 一、送审时的分支与仓库状态

- 仓库路径：本地仓库根目录
- 当前分支：`codex/desktop-native-preview`
- 本地 HEAD：`3bbeb74`
- 远端跟踪：`origin/codex/desktop-native-preview`；送审时 `git status` 清洁（无未提交改动）。
- 目标分支 `main` 在本地仍对齐于 `cb1ef55`，此次工作全部在 `codex/desktop-native-preview`。

## 二、最近关键提交（按时间倒序）

- `3bbeb74 chore: harden 8-candidate schema overlay settings`
- `fc79d92 chore: ensure 8-key candidate settings are always applied by desktop overlays`
- `ffa46ca chore: align candidate/docs/tests with 8-key layout`
- `d80d457 feat: add expression search and favorite action hooks`
- `d4ce4be Increase default candidate page quota to eight`
- `03f3eb2 test: update macOS integration expectations for icon rename and patch count`
- `7de7021 fix: normalize Squirrel patch sequence and icon-name rollback`
- `39b2494 fix: include YunPin identity constants in Weasel utility`
- `659f522 fix: apply Weasel patches outside parent worktree`
- `1d905a3...`（及之前稳定基础修复）见 `git log --oneline` 全量上下文。

## 三、这版要评审的范围（已明确）

1. 候选词数量从 5 调整为 8（横向编号 1-8，含长词召回前置约束）
2. 表情搜索与收藏动作（草稿实现）：
   - Windows：`platform/patches/weasel/0003-yunpin-expression-search-and-favorite.patch`
   - macOS：`platform/patches/squirrel/0005-yunpin-expression-search-and-favorite.patch`
3. 输入法开发预览的配置覆盖链路与打包管线（Windows/macOS）
4. 交付文档与调试入口，防止“本机仍是 5 候选”与“输入法不显示/不可用”问题反复出现
5. 同步服务是否可改指向 NAS（如局域网 Relay）后保持客户端行为不变

## 四、已完成项目与验收依据（重点）

### 1) 8 候选布局（已实现）
- 统一写入 overlay：
  - `platform/rime/squirrel/rime_ice.custom.yaml`
  - `platform/windows/rime/rime_ice.custom.yaml`
  - `platform/rime/squirrel/squirrel.custom.yaml`
  - `platform/rime/weasel/weasel.custom.yaml`
  - `platform/rime/common/default.custom.yaml`
- 强制覆盖主配置与平台覆盖的测试断言：
  - `tools/importer/tests/test_platform_configs.py`
  - 测试命令：`python3 -m unittest tools.importer.tests.test_platform_configs`（通过）
- README 已补充故障恢复路径（无需重编译先改配置）：
  - `README.md`
  - `README.en.md`

### 2) 表情搜索/收藏（已接入但仍为预览级）
- 新增“放大镜/搜索动作 + 收藏动作”标记与本地收藏落盘路径；
- 关键词采用网络图片搜索参数触发（如 `gif 梗图 表情`）；
- 收藏动作生成本地 JSONL，可用于本地预览（未默认接入加密同步服务）。
- 变更位置见上条（platform/patches/*）。

### 3) Windows/macOS 预览管线
- Weasel / Squirrel 主要行为和补丁有序记录在：
  - `platform/patches/weasel/*.patch`
  - `platform/patches/squirrel/*.patch`
  - `platform/windows/dependencies.lock.json`
- 相关构建脚本：
  - `platform/windows/scripts/Build-Preview.ps1`
  - `platform/windows/package/Package-Preview.ps1`
  - `platform/macos/scripts/build-preview.sh`
  - `platform/macos/scripts/package-preview.sh`
  - `platform/macos/scripts/verify-app.sh`

## 五、当前已知边界（Reviewer 必须先确认）

- 同步服务与搜索/收藏的完整安全联动还在迭代：客户端当前为 development-preview，不代表端到端生产通道打通。
- 本机显示 5 个候选时，常因用户目录覆盖层未刷新；先用文档中 inject 命令修复，不必先重编译。
- Windows/ macOS 安装包目前是 unsigned 的开发预览（可用于本地测试，不能作为生产分发）。
- 私有词库学习与云端合并仍受开发门控：例如 Windows 安装脚本有明确提示“private snapshot 关闭”。
- 环境限制：当前会话工作机未发现 `go` 命令可用，`go test ./...` 无法本地执行，仅能验证 Python 层测试；需在有 `go` 的环境补齐 `sync` 的 Go 测试。

## 六、macOS 输入法缺失的应急修复（已做本机处理，未提交）

用户反馈“没有 YunPin 输入法”时，进行过一次系统侧修复：

1. 写入 `com.apple.HIToolbox.plist` 的 `AppleEnabledInputSources` 与 `AppleSelectedInputSources`，确保包含：
   - `YunPin.Hans`
   - `YunPin.Hant`
2. 当前该操作是系统偏好级别“应急修复”，不在 Git 仓库跟踪。
3. 恢复流程：
   - `platform/macos/scripts/inject-rime-config.sh --force`
   - `killall Squirrel || true`
   - 在系统设置里重新切换/勾选输入法
   - 必要时用 pkg 重装：`sudo installer -pkg .../YunPin-IME-development-preview.pkg -target /`

请 Claude Code 在复盘时确认：这类修复属于本地状态，不应等同于仓库提交。

## 七、建议的评审顺序（给 Claude Code）

1. 先看候选逻辑：
   - `tools/importer/tests/test_platform_configs.py`
   - `platform/rime/*/*.yaml`
2. 再看补丁链路是否按序应用且可回溯：
   - `platform/patches/weasel/0001..0003`
   - `platform/patches/squirrel/0001..0005`
   - `platform/patches` 与 `platform/windows/dependencies.lock.json`
3. 再看构建/打包/验证：
   - `platform/windows/README.md`
   - `platform/windows/scripts/Test-Package.ps1`
   - `platform/macos/README.md`
   - `platform/macos/scripts/verify-app.sh`
4. 最后看服务侧边界：
   - `sync/openapi.yaml`
   - `sync/internal/server/server.go`
   - `sync/README.md`

## 八、待处理建议（交接后）

- 明确“同步服务端点”与“表达式收藏”是否要接入 `sync` 端密文通道（当前尚未完成生产打通）
- 统一把“候选 8 项 + 私词不污染首页 >2” 的行为纳入端到端集成测试
- 补齐无 Go 环境机器上的完整 CI/本地验证脚本说明
- 对 macOS 输入法安装失败/不显示输入源做稳定化操作清单（不再依赖人工改 plist）
