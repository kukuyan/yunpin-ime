# YunPin IME 同行评审交接（Codex → Claude Code）

> 本文按评审时可见的代码与合成测试证据描述当前状态，不固定最终提交 SHA、CI 结论或产物哈希。请从实际 HEAD 开始复核。能编译的 development preview 不等于桌面端同步已经接通，也不等于可签名发布。评审记录、夹具和日志不得包含真实个人词库、恢复密钥、设备令牌或服务器秘密。

## 1. 当前状态矩阵

| 范围 | 当前已有 | 当前不能宣称 |
|---|---|---|
| 八候选 | 发行 overlay 固定 `page_size: 8` 与 `12345678`，私有候选上限为 2 | 已安装用户目录不会自动迁移；仍需分别刷新配置并做 macOS/Windows 候选窗实机验收 |
| 短输入保护 | 一至两个规范化拼音字母会过滤 upstream 中“三个及以上 Unicode 标量且全部为 CJK”的候选；`he` 不再出现 `合并为`，但保留一至二字、英文、混合文本和无效 UTF-8 的原顺序 | 这不是通用语言模型，也不能替代真实宿主测试；输入达到三个字母后不启用该 upstream 过滤 |
| 自动拼写纠错 | Windows/macOS 发行 overlay 均默认 OFF；显式实验模式有整句 normal-exact 禁纠错、精确前缀/后缀单桥、最多一个纠错 offset、32 次 search 与长句候选 #2/#3 保护 | 不是当前默认能力；关闭时 `yunpin_corrector` 返回空，不回退 `NearSearchCorrector`；旧“双错首位”和默认 `you` → `yao` 不能作为现状证据 |
| 私有长词与容量 | 私有候选最多占首页 2 项；原生 snapshot 上限已提高到 100,000；100k 高碰撞合成基准 P95 1.672 ms、峰值增量 80.172 MiB | Windows development preview 私有快照仍关闭；当前已安装旧 binary 仍是 50,000 上限，100k 结果不是生产词库/宿主保证 |
| 纠错学习 | `CorrectionLearning` 与 `SessionLearning` 已接入 librime commit/update/unhandled-key/option/delete notifier；真实 Rime C API 已验证“日长 → 两次退格 → 日常 → 重新查询重排” | 仅在当前输入法进程会话内生效；宿主编辑器缓冲区删除证明、可靠安全上下文、加密持久化、桌面监控 UI/CLI 与跨重启保留尚未完成 |
| Replay Lab | 有本地 `EventV1`、append-only store、生命周期 CLI、导出/规则报告、native frame 解析器及默认关闭的固定 C++ ring 基础 | Squirrel/Weasel native producer 和后台 sink 未接；执行 `start` 只建立本地会话，不等于持续监控已经运行 |
| R0W 迁移 | 已转换并合并 94,382 条，低于新的 100k hard cap | 完整 TSV 仍处于 incoming/staging，尚未部署；旧 50k binary 不能完整加载该结果 |
| 表情搜索/收藏 | 当前安全默认是完全 fail-closed；不注入表情动作，普通 `yunpin-search:` / `yunpin-fav:` 文本只按普通文本提交 | 没有图片/GIF 搜索、结果展示、收藏落盘、表情对象模型或云同步；`expression_search` 即使被手工设为 true 也应保持惰性 |
| macOS 原生预览 | 固定 Squirrel、合并 `librime-yunpin`、Universal arm64/x86_64 与 unsigned 包装路径；真实 Rime C API 已覆盖短输入、配额、提交和会话纠错 | InputMethodKit 宿主 UI/安全输入/跨应用矩阵、持久学习与同步 E2E、签名、公证尚未完成 |
| Windows 原生预览 | 固定 Weasel，构建 x86/x64 TSF 与 x64 输入服务的开发管线；短输入 guard 独立启用 | 私有快照明确 `enabled: false` 且 `session_learning: false`；当前实机学习/同步、32/64 位宿主矩阵和 Authenticode 尚未完成 |
| Headless 同步 | `syncclient` + `localstore` 有持久 checkpoint、幂等重试和真实 TCP 两设备恢复/双向收敛 E2E | headless 数据交换不会自动进入两个原生输入法；可信设备 roster、native snapshot rebuild/reload 仍缺失 |
| 桌面后台骨架 | `desktopagent` 有 macOS Keychain、Windows 当前用户 DPAPI、`status` / `sync-once` / `run` 骨架和单实例锁 | 还不是已安装后台服务；`init-account` 生产路径故意 fail-closed，配对/恢复/密钥轮换与原生快照桥未完成 |
| NAS Relay | 对 `192.168.1.127:8787/healthz` 的只读健康检查曾确认 HTTP 200 | 未向 NAS 写入测试账户、keyring 或 envelope；没有“Mac → NAS → Windows”的验收，HTTP 也不是 TLS |

## 2. 八候选与短输入保护

发行 overlay 必须同时设置：

```yaml
"menu/page_size": 8
"menu/alternative_select_keys": "12345678"
```

仓库配置和扫描测试能证明默认文件是一致的，但不能证明用户目录已经刷新。看到五候选时，应先注入/刷新 overlay 并重新部署 Rime，再判断是否需要重编译。

`librime-yunpin` 的 `short_input_guard` 与私有快照开关分离。对一至两个规范化拼音字母，它只跳过 upstream 中长度至少三个 Unicode 标量的纯 CJK 候选。因此：

- 若 upstream 提供，`he` 的 guard 会保留 `和`、`合并`，过滤 `合并为`；
- 英文、混合文本、无效 UTF-8，以及输入长度达到三个字母后的候选流保持原顺序；
- guard 在热路径只查看内存中的候选文本，不访问网络或磁盘；
- Windows overlay 可以保持 `yunpin/enabled: false` 与 `yunpin/session_learning: false`，同时以 `yunpin/short_input_guard: true` 启用这项公共候选保护；这不会加载个人快照或顺带开启学习。

共享 phrase engine 另有更严格的长词召回门槛：置顶个人长词需要两个完整全拼音节或四个首字母；非置顶三音节以上词需要两个完整音节，且不能由短首字母注入。请重点确认 librime upstream guard 与 phrase index 门槛没有产生平台间语义分叉。

### 100k 私有 snapshot 与 R0W 状态

`kMaxPrivateSnapshotEntries` 与 importer 默认/硬上限现在一致为 100,000。
解析测试明确保留第 50,001 行；独立 snapshot benchmark 构造 100,000 条
高碰撞合成词、完整解析并建立不可变索引。本轮可复核结果为查询 P95
1.672 ms、峰值 RSS 增量 80.172 MiB，低于 20 ms / 256 MiB 门禁。该数字只
对应当前合成夹具和机器，不是安装端或真实个人词库的性能保证。

R0W 网络与只读恢复入口已经恢复，不再是“等待网络”的 deferred 状态。当前
转换结果为 94,382 条合并词，能放入新的 100k 上限；但完整 TSV 仍只在
incoming/staging，尚未部署进任何 YunPin 用户目录。当前已安装旧 binary 仍按
50,000 上限构建，因此“转换完成”不能写成“94,382 条已经在输入法生效”。部署
前必须用新 binary、核对完整行数并保留原始快照，真实词库继续留在 Git 之外。

## 3. 自动拼写纠错：默认关闭的实验路径

Windows 与 macOS 发行 overlay 均设置：

```yaml
"translator/enable_correction": false
"translator/corrector_component": yunpin_corrector
"yunpin/typo_correction": false
"yunpin/typo_reviewed_confusions": false
```

因此当前安装默认不执行自动拼写纠错。即使配置仍指向组件，只要
`yunpin/typo_correction` 为 false，component factory 就返回空指针，不会回退
到 librime 较宽泛的 `NearSearchCorrector`。这项 fail-closed 行为必须在两端
保持一致。

显式实验同时开启 translator 与 YunPin 开关后，版本锁定的 librime 1.16/1.17
补丁先对整段输入建立 normal-exact 可达图：

1. 若从头到尾存在任意完整 normal exact path，整段全局禁用纠错，而不是只
   把精确候选排在纠错之前。
2. 若不存在完整精确路径，只能从 forward exact-reachable 位置发起搜索，且
   纠错边结束位置必须 reverse exact-suffix-reachable；即“精确前缀 → 一个纠错
   bridge → 精确后缀”。
3. 整段最多允许一个成功加入纠错边的 input offset；搜索调用总预算为 32。
   每次搜索仍受 128 字节输入、768 原始变体及 Prism 去重后每 offset 最多
   16 条边约束。
4. 生成器只做单次邻键、漏键、多键或相邻转置。有效拼音间的审核混淆另有
   显式实验开关，默认关闭；`you` → `yao` 不能写成默认行为。

候选层还有第二道保护：对规范拼音长度至少 12 的长输入，最多保留一条自动
纠错候选；没有私有头部时只能位于总排名 #2，有一个私有候选时只能位于 #3，
两个私有候选、没有普通候选、或纠错落在首页窗口之后时都隐藏。其余纠错不
延后到下一页。

当前可复核证据包括 portable 生成器边界测试、两端补丁结构/默认关闭静态
测试，以及 filter 对 #2/#3、双私有头、correction-only 和首页外纠错的行为
测试。旧 merged-Rime 夹具中“两个不同 offset 的错误仍首位”以及默认
`you` → `yao` 的结果与当前策略不一致，不能继续作为验收证据；需要用显式
opt-in 配置重写并重跑“无完整 exact path + 单 bridge”真实 librime E2E。
在此之前，manifest 中历史的 typo-correction librime-E2E 标记也不能被解释为
新策略已经通过。

本地模型仍只是未来可选方向。若试验，必须默认关闭、无网络、严格 IPC 超时，
并在失败时回到普通精确路径；当前没有 NearSearch 或模型 fallback。

## 4. 表情功能已安全延期

早期草稿曾把特殊提交文本解释为“打开浏览器”或“写收藏文件”。这个边界可被普通词条、导入词或同步文本伪造，且短 upstream 会让动作候选升到前位，因此已从当前行为中移除。

当前约束是：

- `librime-yunpin` 不注入搜索或收藏候选；空、单项和短 upstream 都不会凭空产生动作；
- 两个平台不再根据提交文本前缀打开浏览器或写文件；
- 形如 `yunpin-search:...` 和 `yunpin-fav:...` 的普通词条必须原样上屏且无副作用；
- overlay 中保留的 `expression_search: false` 只是未来配置占位，手工改成 true 也不能重新接通动作。

未来只有在 native frontend 能提供不可由词典文本伪造的 typed/armed action channel 后，才应重新设计入口。重新启用前还必须定义图片/GIF 提供商、许可证、内容安全、用户确认、真实表情对象、缓存/删除语义和独立的端到端加密收藏 envelope。当前没有“类似微信”的表情搜索或收藏同步能力。

## 5. 词级纠错学习核心

`engine` 中的 `CorrectionLearning` 是离线、词级、内存内状态机。它只接受紧邻事件：先选中一个 entry，立即撤销或删除刚上屏内容，再选择另一个 entry。`librime-yunpin` 的 `SessionLearning` 已把它接到 commit、context-update、unhandled-key、option 和 delete notifier：仅当一个单词候选提交后，在 5 秒内收到与其 Unicode 标量数完全一致的无修饰退格，再由相同规范化拼音提交不同词时才完成纠错。成功后旧词 `-1`、替代词 `+1`，重新查询时在前 8 个 upstream 候选内稳定重排。任何额外/不足退格、修饰键、不同拼音、多非空 segment、`sentence`、未知候选类型、超时或敏感选项都会断链。

监控模型只包含日期桶、entry ID、词、规范化拼音和计数，不包含原句、周边文字、应用名或窗口信息。密码、隐私、一次性、宿主 opt-out、URL/邮箱/路径/凭据样式、控制字符和超长输入均应 fail-closed。`yunpin_habit_report` 只渲染显式传入的合计数据；TSV helper 是人工导出格式，不是生产存储。

已经有 portable 状态机测试、stub filter 行为测试和真实 merged Universal librime C API 测试，后者实际验证“日长 → 两次退格 → 日常 → 再次输入 `richang` 时日常排在日长之前”。必须保持的边界：

- librime notifier 能证明按键序列，但不能读取宿主编辑器缓冲区并证明字符确实被删除；
- TSF/InputMethodKit 宿主尚未可靠桥接密码框、隐私模式和一次性上下文；
- 尚无把 monitor aggregate 写入加密 `localstore` 的 producer；
- `QueryHabits()` 仍只是进程内开发 API，尚无安装端监控 UI/CLI，重启会丢失会话统计和纠错分数；
- 尚无同步合并后重建不可变候选快照并通知 Rime reload 的通道。

因此当前可以宣称“真实 librime 会话内纠错重排已接通”，不能宣称“跨重启自动学习、可查看长期习惯或跨设备同步已完成”。

### Replay Lab 边界

`replaylab/` 已有严格有界的本地 `EventV1`、顺序/episode 校验、逐事件 fsync
的 append-only store、崩溃窗口 metadata 修复，以及 `init`、`start`、`pause`、
`resume`、`status`、`ingest`、`report`、`export`、`clear` CLI 和规则报告。
`librime-yunpin` 侧已有默认关闭、
固定 64 槽的单生产者/单消费者 C++ ring、8 KiB JSON 上限、`drop_count` 与
native frame 序列化/解析合成测试。

但 Squirrel/Weasel callback producer 与把 ring 持续排空到本地 store 的后台
sink 均未连接。`start` 只写入本地 session/resume 元数据，不会自动捕获任何
输入；只有显式 `ingest` 或测试 harness 能产生记录。因此当前可称“Replay Lab
协议/存储/CLI/报告与 native ring 基础”，不能称“已经开始持续监控”。真实
trace 必须留在 Git 外，且 native bridge 接通前仍是 P0。

## 6. macOS 与 Windows 原生预览

### macOS

Xcode 选择顺序是：有效 `DEVELOPER_DIR`、有效 `YUNPIN_XCODE_APP_PATH`、有效的 `xcode-select -p`（排除 Command Line Tools）、外置盘和常规路径扫描。该顺序避免 CI 或用户已经选择较新 Xcode 时，被较旧的默认 `/Applications/Xcode.app` 覆盖。测试构造了“已选新版本 + 默认旧版本”的场景来固定该行为。

原生管线能构建合并 `librime-yunpin` 的 Universal Squirrel/InputMethodKit development preview；真实 Rime C API 已覆盖 `he` 短输入、`zgsh` 置顶长词、两项个人配额、去重/提交、私密模式抑制和会话纠错。发行自动拼写纠错已改为默认关闭；旧单错/双错/审核混淆性能夹具不能替代当前单桥策略的重写与复测。该证据仍不是 InputMethodKit 跨应用宿主或生产 Rime Ice 验收；manifest 应继续把 native host E2E、安全上下文、持久学习、encrypted cloud sync 和 production signing 标为未完成。正式结论必须来自最终 HEAD 的全新构建和真实应用矩阵，不能复用旧外置盘 artifact。

### Windows

Weasel 管线目标是 x86/x64 TSF 加 x64 服务的 unsigned development preview。发行 overlay 当前故意设置：

```yaml
"yunpin/enabled": false
"yunpin/short_input_guard": true
"yunpin/session_learning": false
"translator/enable_correction": false
"translator/corrector_component": yunpin_corrector
"yunpin/typo_correction": false
"yunpin/typo_reviewed_confusions": false
"yunpin/long_correction_guard": true
```

这意味着私有快照、会话学习和自动拼写纠错均未进入 Windows 默认输入过程；短输入公共候选过滤独立工作，长纠错候选 guard 只是为显式实验预留。Windows 构建会应用锁定的 librime 1.17 component-selector 补丁并打包组件，但关闭时 factory 返回空且没有 NearSearch fallback。仍需完成 195 上的显式 opt-in 单桥实验，以及 Notepad、Office、Chrome、Terminal、32/64 位宿主、密码框、崩溃重连、安装升级和卸载验收。正式发布还需要 Authenticode。

## 7. Headless 同步与真实 TCP E2E

`localstore` 保存记录级 XChaCha20-Poly1305 密文和加密 outbox；它不是整库 SQLCipher。持久化 `sync_state` 绑定设备并保存 cursor、下一序号、previous hash，以及请求前准备好的精确 ciphertext wire/hash。网络已接收但响应丢失时会重发同一个 `(device_id, device_seq)` envelope，而不是生成新序号。

真实 TCP 合成 E2E 使用临时 `httptest` 服务，覆盖：设备 A 创建账户、设备 B 使用恢复材料加入同一账户、两端获得共享 epoch/object-ID keys、A 在“服务端已提交但响应丢失”后从 checkpoint 精确重试、B 下载验签解密并回传计数、最终 CRDT 收敛。它证明 headless worker 和本地库能够交换不透明密文，不证明任何原生候选已经刷新。

高优先级缺口是可信设备信任图。现有合成流程/credential bundle 可以带本地 verification keys，但没有由账户根信任认证的 roster、添加证明、撤销状态、并发配对或轮换链。relay 不应被信任为公钥真相。

## 8. `desktopagent` 边界

`CredentialBundleV1` 是有界、规范化的设备本地二进制记录，包含 device token、Ed25519 seed、X25519 私钥、本地数据库 key、object-ID key、epoch keys 和本地信任的 Ed25519 keys。恢复根及其 recovery-authentication 值不进入 bundle；灾难恢复仍需另存随机 account ID，因为当前恢复文本不编码 account ID。

平台 secret-store 骨架为：

- macOS：不可同步的 data-protection Keychain generic-password，`AfterFirstUnlockThisDeviceOnly`；测试不访问用户真实 Keychain；
- Windows：当前用户 `CryptProtectData`，禁用 UI、不使用 machine-wide flag，以域分离 entropy 保护，并在当前用户 `%LOCALAPPDATA%` 下原子替换；尚未显式安装 DACL，因此打包必须坚持固定每用户路径及继承 ACL。

CLI 暴露 `status`、`sync-once`、`run` 和 `init-account` 骨架：

- `status` 只验证本地凭据、endpoint 配置和数据库常规文件，不联网，也不输出标识或 key；
- `sync-once` 是唯一显式网络操作，打开加密本地库并调用 headless worker；
- `run` 使用每用户文件锁、重试/退避和仅含稳定代码与数字摘要的事件；尚未注册成受签名的系统后台服务；
- `init-account` 即使得到恢复信息显示确认，导出的生产 API 与 CLI 仍 fail-closed，因为 relay 没有 account-delete rollback。非原子合成流程只存在于包内未导出的测试 helper，外部调用者没有可开启的绕过开关。

仍缺：账户恢复/配对 UI、可信设备 roster、密钥轮换、会话纠错的加密持久化与监控界面、加密内存候选 snapshot bridge/Rime reload、安装器注册与 TLS 终止。一次 `sync-once` 成功只证明本地加密库交换了 envelope。

## 9. NAS 只读边界

对 `http://192.168.1.127:8787/healthz` 的既有只读核验返回 HTTP 200，说明 Relay 当时可达。没有读取数据库、环境变量、秘密或日志正文，也没有创建测试账户、设备、keyring 或 sync envelope。因为 API 没有删除账户/回滚端点，评审不应向现有实例写入不可清理的合成账户。

`allow_private_http` 只是显式风险开关，不提供链路加密。Bearer token 或 recovery authentication 在 HTTP 局域网链路上仍是明文传输；真实个人数据接入前应部署 TLS/可信反向代理，再执行可清理的两台真实设备 E2E。

## 10. 建议 Claude Code 优先审查

### P0：设备信任与账户生命周期

- 设计由账户根信任认证的设备 roster，验证恶意公钥替换、撤销后重放、旧 epoch 回滚、并发配对和恢复后认证。
- 为账户创建提供 rollback-safe 事务或删除能力；在此之前确认所有 production `init-account` 入口都无法越过 fail-closed gate。
- 明确定义恢复根与 account ID 的备份、二维码、轮换和销毁流程，且任何测试/日志不得打印秘密。

### P0：持久 learning 与快照生效链

- 审查现有 librime notifier 状态机与 TSF/InputMethodKit 宿主删除语义如何建立更强证明，并以可信方式桥接 secure-context；普通提交文本不能伪造控制事件。
- 把词级 monitor 合计写入加密 localstore，后台重建完整候选快照，原子替换并触发 Rime reload；失败必须保留上一代。
- 证明按键处理不访问网络或磁盘；服务停机、数据库满或后台崩溃不得影响输入。

### P1：同步正确性与崩溃一致性

- 为远端设备持久化并验证下载侧 `device_seq` / `previous_hash` 连续性及 key epoch，检测重放、遗漏、回滚、分叉和 keyring downgrade。
- 对“prepare → HTTP → merge → upload commit → cursor advance”逐阶段故障注入，证明不丢 outbox、不跳 cursor、不重复序号、不复活 tombstone。
- 复核 `run` 单实例边界、锁文件替换/权限、取消与退避语义，以及多个用户会话并存时的隔离。

### P1：本地秘密与隐私

- 审查 SQLite 主文件、WAL/SHM、备份、文件权限、符号链接/替换、临时明文和 object-ID 元数据泄漏。
- `Store.Close()` 当前循环会把 `dataKey` 和 `idKey` 的每个字节各置零一次；不要再描述为漏清某个 key。仍需明确：Go 编译器/运行时不能提供可证明的 secure erase 保证。
- 复核 Keychain accessibility、DPAPI 当前用户路径与 ACL、credential 更新失败回滚、卸载后保留/删除策略。
- 复核纠错核心的邻接状态、UTF-8/敏感过滤、计数溢出、并发与 50,000 个词/日期 aggregate 和 50,000 个会话分数上限；不要让明文 TSV 被误当自动持久化。

### P1：候选正确性与未来表情入口

- 对短输入 guard 验证 `he`、一至二字保留、三字纯 CJK 过滤、英文/混合/无效 UTF-8 保留，以及 Windows 私有关闭时仍生效。
- 保持两端默认 OFF 且关闭时无 NearSearch fallback。显式实验要验证 whole-normal exact path 全局禁纠错、forward/reverse 单 bridge、整句最多一个 offset、32 次 search 与每 offset 16 边预算；长纠错最多一条且只能位于总排名 #2/#3。用 100,000 条合成个人词复跑污染与 P50/P95/P99，不能复用旧“双错首位”或默认 `you` → `yao` 证据。
- 复核 100k snapshot 的负载、峰值内存、原子替换和旧 50k binary 拒绝/升级路径；R0W 的 94,382 行完整 TSV 在明确部署验收前必须保持 incoming 且不进入 Git。
- 为 Replay Lab 接上经过审查的 Squirrel/Weasel producer 与后台 sink，证明按键热路径只尝试写有界 ring、队满只计 `drop_count`、暂停/保护上下文立即生效；`start` 命令本身不得被当作捕获证明。
- 保持表情功能 fail-closed，直到存在不可伪造的 typed/armed native channel。未来另审内容提供商、许可证、隐私确认、缓存、删除和端到端加密收藏模型。

### P2：发布与实机矩阵

- macOS：TextEdit、Safari、Office、Terminal、原生/Rosetta、候选定位、深浅色、密码框、登录/睡眠、升级/卸载；Developer ID、notarization、stapling。
- Windows：Notepad、Office、Chrome、Terminal、32/64 位宿主、低完整性/UWP、崩溃重连、升级/卸载；Authenticode。
- 所有正式 artifact 必须由最终清洁提交重新构建，验证源码与二进制对应；签名密钥只进入受保护发布环境。

## 11. 复核建议

优先使用合成数据和临时服务，不连接生产 NAS：

```bash
git status --short
git diff --check
make -C engine test
cmake -S librime-yunpin -B build/librime-yunpin-review
cmake --build build/librime-yunpin-review --parallel
ctest --test-dir build/librime-yunpin-review --output-on-failure
(cd replaylab && go test ./...)
make -C platform/macos test
(cd platform/windows && python3 tests/test_windows_client.py -v)
docker build --target test -f integration/Dockerfile -t yunpin-integration:review .
bash scripts/check_private_data.sh
python3 scripts/check_licenses.py
python3 scripts/check_supply_chain.py
python3 scripts/check_submodule_locks.py
```

另按各 Go module 的 `go.mod` 分别执行 `go mod verify`、`go vet ./...` 和 `go test -race ./...`，尤其是 `localstore`、`protocol`、`sync`、`syncclient`、`desktopagent` 与 `integration`。完整 Windows/macOS native 构建使用仓库锁定的工具链；不要把旧 artifact 当作当前 HEAD 的证明。

## 12. 稳定版之前的最低退出条件

1. 可信设备 roster、配对、恢复、撤销和 key rotation 具备端到端正负测试；账户创建可回滚或可清理。
2. 会话 learning、宿主删除/保护上下文证明、加密持久化、snapshot rebuild/atomic reload 在两端实机闭环；按键路径始终离线且内存读取。
3. 自动拼写纠错继续默认关闭且无 NearSearch fallback；显式实验在锁定生产词库与两端宿主验证完整 exact path 禁用、单 offset bridge、32-search 预算及 #2/#3 候选限制后，才可讨论开放。
4. 表情功能在 typed/armed 安全通道和独立加密数据模型完成前继续 fail-closed。
5. NAS 经 TLS 完成可清理的两设备密文同步、撤销与故障恢复测试。
6. 100k-capable binary 完成 R0W 94,382 行 incoming TSV 的可回滚部署验收；旧 50k binary 不得被当作完整迁移结果。
7. Replay Lab native producer/sink 接通并用合成/授权 trace 证明持续捕获、暂停、丢帧与本地清理边界；`start` 单独不算验收。
8. 当前最终 HEAD 的 Windows/macOS 包在干净环境重建并完成对应源码、签名、公证与跨应用安装矩阵。
