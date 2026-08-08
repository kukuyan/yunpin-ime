# YunPin IME 交接文档（评审后回交 Codex）

本文是 `docs/HANDOVER_CLAUDE_REVIEW.md` 的回程。评审已完成，评审中发现的阻断级与高危问题已修复并提交。本文说明改了什么、为什么改、验证到什么程度，以及还剩什么没做。

## 一、评审回交时的分支与提交状态

- 仓库路径：本地仓库根目录
- 分支：`codex/desktop-native-preview`
- 本地 HEAD：`c0a4f75`
- 评审回交时，远端 `origin/codex/desktop-native-preview` 仍在 `3bbeb74`；以下四个提交由 Codex 整理后发布
- `main` 仍在 `cb1ef55`，未受影响
- 子模块 `third_party/squirrel` `876adeb` 与 `third_party/weasel` `9cc96e2` 未改动

```
c0a4f75  test: make candidate ordering testable without a librime tree
cf3e2d2  test: close the overlay and patch-series coverage gaps
6756722  fix: keep expression actions off the head of the candidate page
21c13ce  fix: harden expression favorite persistence on both desktop frontends
3bbeb74  (原 HEAD，评审起点)
```

合计 30 个文件，+938 / −104。

## 二、评审结论摘要

原交接文档第三节列的五项评审范围里，第 1 项（8 候选）机制上是成立的，第 3、4 项基本可用，**第 2 项（表情搜索与收藏）存在一个使输入法不可用的阻断级缺陷**，第 5 项的前提不成立（见 §六）。

需要特别修正原文档的一处自评：第四节第 2 条称表情搜索/收藏「已接入但仍为预览级」。实际状态是已经接入且**默认会触发**，且接入方式会劫持最高频按键。这不是预览级不完善，是不可用。

## 三、`21c13ce` — 收藏动作的持久化加固

改动范围：`platform/patches/squirrel/0005-*.patch`、`platform/patches/weasel/0003-*.patch`、`platform/windows/dependencies.lock.json`。

### macOS（Squirrel 0005）

1. **整表覆盖的数据丢失路径**。原 `persistFavorite()` 在文件已存在但 `FileHandle(forWritingTo:)` 返回 nil 时，会穿透到 `data.write(to:options:.atomic)`，用单条记录原子覆盖整个收藏文件。改为纯追加：文件不存在时显式创建，随后一律通过句柄追加，已有内容永不截断。
2. **非法 JSONL**。原来用 `String(format:)` 手写转义，只处理 `\` 和 `"`。查询串含控制字符时产出无法解析的行。改用 `JSONSerialization`（`.sortedKeys` 保证顺序确定）。
3. **文件名不确定**。`appendingPathComponent("favorites.jsonl", conformingTo: .json)` 里名字已带 `.jsonl` 而 UTType 是 `.json`，实际落盘名不可靠。改为 `isDirectory: false`。
4. **权限**。目录 0700、文件 0600，与 `inject-rime-config.sh` 对齐。收藏内容是用户输入，此前无任何权限限制。
5. **一个既有编译错误**。原代码 `handle.seekToEnd()` 缺 `try`，而该 API 是 throwing 的。这行本来就编不过，说明 0005 补丁从未被实际构建验证过——建议排查一下该补丁是怎么进入补丁链的。
6. 失败路径改走 `NSLog`（不打印 query 本身）。原来 `guard persistFavorite(...) else { return true }` 两个分支都返回 `true`，返回值是死逻辑。

### Windows（Weasel 0003）

1. **在全新机器上必然失败**。`BuildFavoritePath()` 用 `CreateDirectoryW` 创建 `%APPDATA%\YunPin\emoji_favorites`，而该 API 不创建中间层级。父目录 `YunPin` 不存在时返回 `ERROR_PATH_NOT_FOUND`（不是 `ERROR_ALREADY_EXISTS`），直接失败。新增递归的 `EnsureDirectory()`，逐级向上创建，遇到盘符根停止。
2. **转义**。`EscapeJson` 补齐剩余 C0 控制字符为 `\uXXXX`。
3. **并发追加**。`_wfopen(path, L"ab")` 换成 `CreateFileW(..., FILE_APPEND_DATA, ...)`。TSF 模块加载进每个客户端进程，多进程并发写同一文件需要原子追加句柄；CRT 的 `"a"` 模式不保证跨进程原子性。

补丁内容变化后已同步更新 `platform/windows/dependencies.lock.json` 里 weasel 0003 的 sha256。

### 未改动：联网搜索

`LaunchImageSearch` 仍然把用户输入明文拼进 `https://www.bing.com/images/search?q=...`，无用户确认、无开关、搜索引擎硬编码。**这是仓库所有者明确决定保留的**，不是遗漏。

需要注意它与 `README.md` 第 12 行「候选热路径只读内存快照，不访问网络或磁盘」以及项目「隐私优先」定位之间的矛盾。若最终保留该行为，`docs/THREAT_MODEL.md` 需要相应补充。

## 四、`6756722` — 阻断级缺陷：动作候选劫持候选页头部

这是挡住稳定版的那一条。

### 原行为

`librime-yunpin/src/rime_yunpin_filter.cpp` 的 `Apply()` 中，两个动作候选被**无条件前置**，排在私词与 upstream 之前：

```cpp
if (!trimmed_input.empty()) {
  injected.push_back(New<YunPinSearchCandidate>(...));   // 第 1 位
  injected.push_back(New<YunPinFavoriteCandidate>(...)); // 第 2 位
}
```

`YunPinMergedTranslation` 把注入项全部排在 upstream 之前，filter 又挂在 `@before 0`。后果：

- **空格键提交第一候选**，即打开浏览器搜索，而不是上屏。
- **数字键 1 / 2** 同理。中文输入法最高频的三个按键全部失效。
- `matches.empty()` 分支显式保留了注入，所以**零私词命中时也会出现**。
- 8 候选被吃掉 2 格，加上私词最多 2 格，`README.md` 第 11 行「首页八项中个人候选最多两项」的口径不再成立。

触发条件：`yunpin/enabled: true` 且用户数据目录下存在非空 `yunpin/private.tsv`。macOS overlay 默认 `enabled: true`，因此**任何 macOS 预览用户一旦导入私人词库即触发**。Windows 因 `enabled: false` 暂时幸免。

此外，动作功能的可用性此前被意外耦合在私词快照上：构造函数里 `enabled_ = LoadSnapshot(...)`，快照加载失败关闭整个 filter，加载成功则顺带把联网动作打开。这两个特性不共享任何数据。

### 修复

1. **新增 `yunpin/expression_search`，默认 `false`**。动作候选不再默认存在。主开关 `yunpin/enabled: false` 仍然强制压掉它，因此 `Install-Preview.ps1` 里那条 Windows 不变量检查不受影响。
2. **拆分快照就绪与模块激活**。新增 `private_ready_`，快照缺失只影响私词，不再影响动作；快照存在也不再自动开启动作。新增 `Active()` = `enabled_ && (private_ready_ || expression_search_)`。
3. **`YunPinMergedTranslation` 改为两段注入**：`front`（私词，占头部）+ upstream + `deferred`（动作）。`deferred_offset` 取 `max(front.size(), page_size - deferred.size())`，即把动作停在首页最后两格。page_size 通过 `engine_->schema()->page_size()` 读取，缺省回退 5。
4. **upstream 提前枯竭时动作仍会发出**，不会被丢弃。
5. `Peek()` 与 `Next()` 收敛到单一 `SelectSource()` 决策点。原来两处各写一套游标判断，是容易长歪的地方。

page_size 8、私词 2 条时的实际顺序：

```
0-1  私词          ← 空格键落在这里
2-5  Rime 候选
6-7  搜索 / 收藏    ← 需要按 7 或 8
```

配置项已在两个 `rime_ice.custom.yaml` 中显式写出 `false`，`librime-yunpin/README.md` 有说明，`test_platform_configs.py` 断言任何发行 overlay 都不得启用。

## 五、`cf3e2d2` / `c0a4f75` — 测试与供应链

### 测试覆盖缺口（`cf3e2d2`）

原 `test_platform_configs.py` 手写文件列表，断言了 `platform/rime/common/default.custom.yaml`，但**macOS 实际安装的是 `platform/rime/squirrel/default.custom.yaml`**（见 `inject-rime-config.sh:33`、`build-preview.sh:22`、`prepare-source.sh:26`）。也就是说决定 Mac 上显示几个候选的那个文件，此前没有任何测试覆盖；`weasel.custom.yaml` 同样漏掉。

改为对 `platform/rime` 与 `platform/windows/rime` 下所有 `*.custom.yaml` 做扫描，每个都必须把 `menu/page_size` 钉成 8 并带 1-8 选字键，**文件清单本身也被钉死**，新增 overlay 无法绕过测试。另加一条断言：两份 `default.custom.yaml` 的 menu 设置必须逐行一致。

### `platform/rime/common/` 更名（`cf3e2d2`）

该目录只被 `Package-Preview.ps1` 读取，「common」名不副实（macOS 走 `squirrel/`）。移到 `platform/rime/weasel/default.custom.yaml`，与 `weasel.custom.yaml` 并列，与 `squirrel/` 目录对称。`README.md`、`README.en.md` 的手动恢复命令已同步。

### Squirrel 补丁摘要锁（`cf3e2d2`）

Windows 侧 `dependencies.lock.json` 一直对 weasel 补丁记 sha256，macOS 侧没有——`test_macos_integration.py` 只断言「数量 == 5 且含 Base-Commit」。Base-Commit 只说明补丁是对着哪个上游写的，检测不出补丁被改过。

新增 `platform/macos/dependencies.lock.json` 的 `squirrel_patches` 字段（5 条 path + sha256），并在 `prepare-source.sh` 中**于克隆之前**校验：既比对摘要，也比对锁记录的补丁清单与磁盘实际文件是否一致。

### 无 librime 环境下的行为测试（`c0a4f75`）

`rime_yunpin_filter.cpp` 只能在 librime 树内编译，需要 Boost、glog 和完整前端工具链。这意味着候选顺序规则此前只能靠出包验证。

新增 `librime-yunpin/tests/rime_stubs/`：11 个桩头文件，只声明 filter 用到的那部分 librime API，签名逐个对着真实头文件抄写。新 CMake target `yunpin_filter_behaviour_tests` 用**生产源码**（filter + snapshot store + phrase engine）配桩头编译并跑 7 个场景：

| 场景 | 断言 |
|---|---|
| 发行默认 overlay | 完全没有动作候选 |
| 开启 + page_size 8 | 动作在第 7、8 位 |
| 开启 + 快照缺失 | 动作照常，私词消失 |
| `yunpin/enabled: false` + 开启 | filter 整体不激活 |
| password_mode | filter 整体不激活 |
| page_size 5 | 动作在第 4、5 位，仍不占头部 |
| `max_candidates: 5` | 仍被钳到 2 |

桩最大的风险是 librime 升级后签名悄悄失效。每个桩头文件里写死了它对齐的 librime commit，`test_platform_configs.py` 把它钉到 `platform/upstream-lock.json`。**升级 librime 时这条测试会红，届时需要逐个重新核对签名，不要直接改数字放行。**

运行方式（任何 Linux/macOS 机器，无需 librime/Boost/glog/Xcode）：

```bash
cmake -S librime-yunpin -B build/librime-yunpin
cmake --build build/librime-yunpin --parallel
ctest --test-dir build/librime-yunpin --output-on-failure
```

## 六、关于原文档第五个评审问题

原文档问：「同步服务是否可改指向局域网 NAS 后保持客户端行为不变」。

答案是可以，且零风险——**因为目前没有任何客户端集成**。全仓库不存在客户端侧的 sync endpoint 配置：`localstore/`、`librime-yunpin/`、两个平台补丁里都没有 HTTP 客户端，唯一调用 sync HTTP 接口的是 `integration/protocol_sync_test.go` 里的测试客户端。`sync/cmd/yunpin-sync/main.go` 只认 `YUNPIN_LISTEN` 与 `YUNPIN_DATABASE`，纯 HTTP，TLS 明确交给反向代理。改指向 NAS 纯属部署决策。

因此原文档第八节「明确同步服务端点与表达式收藏是否要接入 sync 端密文通道」低估了距离：现在连客户端通道入口都还不存在。

服务端本身评审下来是这几个模块里质量最高的：opaque relay 设计清晰，`sync/README.md` 把限流器只信 `RemoteAddr`、忽略 `X-Forwarded-For` 这个关键 caveat 写得很明白，openapi.yaml 是合法 OpenAPI 3.1 且 11 个 path 与 README 接口表一一对应。

## 七、验证到什么程度

**已验证**

- Python 全套：`make test-tools` 5 项、`tools/public_pack` 5 项、`platform/windows/tests` 7 项（含补丁 sha256 对账）、`platform/macos/tests` 18 项、`test_platform_configs` 6 项。
- C++ filter：真实源码在 `-Wall -Wextra -Wpedantic -Werror` 下编译零告警，7 个行为用例全过。
- 变异验证（确认测试不是摆设）：两份 `default.custom.yaml` 任一 page_size 改回 5 → 红；overlay 里开启 `expression_search` → 红；锁定的 squirrel 补丁追加一字节 → `prepare-source.sh` 拒绝且不创建 checkout；`upstream-lock.json` 里 librime commit 改动 → 桩漂移断言红；移除 `expression_search` 门 → 行为测试首个用例 abort。
- Weasel 的 `EscapeJson` 抽出后单独编译，用全部 C0 控制字符 + 引号 + 反斜杠 + 中文 + emoji 跑过，输出交给 JSON 解析器验证精确 round-trip。

**未验证**

- **Go 测试完全没跑过**。`sync/`、`protocol/`、`localstore/`、`integration/` 四个 module 一次都没执行。评审环境无 go 且无法安装。这是当前唯一没有任何验证覆盖的部分。
- **完整 C++ 构建没跑过**。行为测试用的是桩 librime。真实的 `platform/macos/scripts/build-preview.sh` / `platform/windows/scripts/Build-Preview.ps1` 从未执行，链接期与真实 librime 的集成未验证。
- 两个平台的补丁改动**没有在真实 Weasel / Squirrel 构建中编译过**。macOS 那处 `try` 缺失说明这条链路此前也没被真实构建覆盖过，风险偏高。

## 八、交给 Codex 的待办

**必须先做（否则无法判断能否推进稳定版）**

1. 跑 Go 全量测试：`for m in sync protocol localstore integration; do (cd $m && go test ./...); done`
2. 跑完整 macOS 与 Windows 预览构建，确认两个补丁与 filter 改动在真实工具链下能编译链接。

**功能层面的开放问题**

3. 动作候选的 `SimpleCandidate` 的 `text` 就是提交串，因此候选框里会**字面显示 `yunpin-search:nihaoshijie`**，comment 才是「点击联网搜索」。这在开启时仍然很难看。若要保留该特性，建议改为独立 tag / recognizer 触发（如输入 `/gif` 才出），而不是混在普通候选里。
4. 提交串会进入 Rime 的 commit history 与用户词典学习路径，存在把 `yunpin-search:xxx` 学成词条的风险，未验证。
5. 联网搜索的隐私问题（见 §三末尾）需要产品层面定论，并同步 `docs/THREAT_MODEL.md`。
6. 收藏目录两端不一致：macOS 是 `yunpin-emoji-favorites`，Windows 是 `%APPDATA%\YunPin\emoji_favorites`。将来接入同步或合并时需要先统一。
7. 空查询时行为不一致：Windows 会把 `yunpin-search:` 字面插入文本，macOS 直接吞掉。

**工程债**

8. `librime-yunpin/tests/snapshot_store_test.cpp` 没有 `#undef NDEBUG`，Release 配置下断言会被优化掉，测试静默通过。新增的 `filter_behaviour_test.cpp` 已加，建议补齐。
9. 四个 Go module 的语言版本不齐：`sync` 是 `go 1.24.0`，其余三个是 `1.25.0`。
10. `platform/windows/rime/yunpin-private.tsv.example` 只有注释占位，没有示范数据行。实测发现 `pinyin` 列**必须是音节分隔的**（`ni hao shi jie`，`SplitPinyin` 按空格 / `'` / `-` 切分），连写整行会被拒收。写用户导入文档时务必给出真实样例，否则很容易踩坑。
11. macOS 输入法不显示时手改 `com.apple.HIToolbox.plist` 的应急流程仍未产品化。另外原文档记录写入的是 `YunPin.Hans` / `YunPin.Hant`，而 `test_macos_integration.py:108` 断言的模式是 `io.github.kukuyan.inputmethod.YunPin.Hans`，短标识符可能本身就不对，值得复核。
12. Windows / macOS 安装包仍未签名，仅供本地测试。

## 九、修改补丁时的注意事项

两个前端补丁不是手改 hunk 行号生成的，流程是：应用到对应 submodule 的原始源码 → 修改源码 → `git diff` 重新生成 → 保留原有的 SPDX / Base-Commit / Subject 三行头。**手改 `@@` 行号极易出错**，尤其 weasel 0003 有两个 hunk，第一个 hunk 的行数变化会连带影响第二个的起始行。

改完 weasel 补丁后必须更新 `platform/windows/dependencies.lock.json` 的 sha256；改完 squirrel 补丁后必须更新 `platform/macos/dependencies.lock.json` 的 `squirrel_patches`。两处都有测试对账，漏了会红。
