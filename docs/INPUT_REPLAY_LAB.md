# YunPin 输入回放实验室（MVP）

## 当前边界

输入回放实验室用于记录“输入法内部发生了什么”，然后用可重复的规则分析候选排序和删改轨迹。它不是系统键盘记录器，不读取其他输入法，也不会抓取应用内已经存在的文字。实验室根目录和导出目标如果位于任何 Git 工作树内会被直接拒绝。

当前 MVP 已实现：

- 严格版本化的 `yunpin.replay.event.v1` JSONL 事件协议；
- 本地、仓库外、追加写入的会话存储，每个事件写入后 `fsync`，会话元数据原子替换；
- `init/start/pause/resume/status/ingest/report/export/clear` CLI；
- 对退格重打、候选位次、正确路径被纠错候选抢首位、同拼音分词替换的确定性分析；
- 默认关闭的固定大小 C++ native event、64 槽单生产者/单消费者 ring、8 KiB JSON 边界和 `drop_count`；
- native frame 到 EventV1/报告器的严格解析与合成端到端测试；
- `librime-yunpin` 已接实际候选页、选择、提交和组合区退格回调；
- Squirrel/Weasel 已启动默认休眠的后台 watcher，只在显式实验会话为
  `running` 时启用 producer；
- 所有真实轨迹的 Git 忽略与 CI 私人数据扫描。

代码和安装包管线已经接通原生 producer 与后台落盘，并用同一条合成轨迹验证到 Go `report`。但这**不等于当前机器上已经安装的旧版本也具备该功能**；真实 Squirrel/Weasel 宿主采集仍须安装新包后人工验收。它不扫描系统按键、不注册全局键盘钩子，也不包含网络、云同步或模型调用。

输入热路径只做固定大小内存复制和非阻塞 ring push。`active.json` 轮询、文件追加和 flush 都在后台线程；每个 native 会话文件上限 64 MiB。watcher 每 50 ms 复核一次状态，`pause`、无效状态或宿主退出会关闭 producer 并丢弃边界队列，避免文字跨实验会话。被 Rime 标记为密码、隐私、一次性输入以及宿主未明确允许学习的上下文均为零记录；原生宿主对真实安全输入框的标记传递仍须在实机门禁中验证。

## 生命周期

默认目录为：

- macOS：`~/Library/Application Support/YunPin/ReplayLab`
- Windows：`%LOCALAPPDATA%\YunPin\ReplayLab`
- Linux：`$XDG_STATE_HOME/yunpin/replaylab`，未设置时使用 `~/.local/state/yunpin/replaylab`

初始化只创建一个关闭状态的实验室：

```sh
yunpin-replay-lab init
yunpin-replay-lab start
yunpin-replay-lab status
yunpin-replay-lab pause
yunpin-replay-lab resume
```

开发预览安装后的 CLI 位置为：

- macOS：`/Library/Input Methods/YunPin.app/Contents/MacOS/yunpin-replay-lab`
- Windows：`%LOCALAPPDATA%\Programs\YunPinIME\Preview\support\sync-agent\yunpin-replay-lab.exe`

`init` 只需执行一次；之后用 `start` 开始第一段会话，用 `pause` 停止采集、`resume` 继续。仅启动输入法不会自动创建实验会话，也不会记录任何文字。

原生 sidecar 或专用实验宿主以一行一个事件的方式接入。MVP 采用单写入者模型：一个持久 sidecar 保持一个 ingest 进程，不允许多个进程并发追加同一会话：

```sh
yunpin-replay-lab ingest --input /path/outside/repository/events.jsonl
```

生成本地统计报告或导出原始追加日志：

```sh
yunpin-replay-lab report
yunpin-replay-lab export --output /path/outside/repository/session.yunpinreplay
```

清空是破坏性操作，只接受带有效 `lab.json` 标识且与清单中绝对路径完全一致的实验室根目录。文件系统根目录、用户主目录、宽路径、符号链接或没有标识的目录都会被拒绝：

```sh
yunpin-replay-lab clear --confirm
```

## EventV1 约束

每条事件不超过 8 KiB，未知 JSON 字段会被拒绝。`session_id` 和 `episode_id` 是 128 位随机 ID 的 32 位小写十六进制表示；`seq` 必须从 1 连续递增；`monotonic_us` 和规范 UTC 时间不能倒退。一个 episode 只有在 `commit`、`abort` 或 `pause` 后才能切换，已经关闭的 ID 不得复用；单会话最多 100 万个 episode。

事件类型包括：

- `composition_snapshot`：当前输入、规范拼音、光标、是否存在精确路径、最多 8 个候选；
- `select`：一基候选位次和候选文字；
- `commit`：提交文字、来源，以及带 `scope/confidence` 的最终文字；
- `backspace`、`delete`、`abort`：输入法组合区内的删改或放弃；
- `pause`、`resume`：显式实验状态切换；
- `drop_count`：未来原生有界队列丢弃事件时的可观测计数。

候选包含 `text`、`is_correction` 和 `highlighted`。同一快照最多一个高亮候选。字符串分别有 256–2048 字节的上限，必须是有效 UTF-8 且不能包含 NUL。

`final_text` 的 `scope` 可为 `composition`、`clause` 或 `episode`，`confidence` 可为 `observed`、`user_confirmed` 或 `inferred`。这两个字段防止分析器把“输入法看见的提交”误称为“整段最终文章”。

## 报告如何形成建议

报告器以流式方式读取原始事件并输出计数和建议，不把全部原始文字加载到内存，不修改词库，也不调用大模型：

- 精确路径存在但首候选标记为 correction：建议把精确候选放回纠错候选之前，并生成排名黄金测试；
- 用户从第二位以后选择非纠错候选：归类为同拼音词组/分词重排，不归类为键位拼错；
- 退格或删除后以相同规范拼音提交不同文字：归类为可复盘的重打修正；
- 平均选择位次偏后：优先考虑有界个人重排，不盲目增加生成候选；
- `drop_count` 非零：先修复采集完整性，再使用频次结论。

原始轨迹始终是事实层；迭代建议是派生层。后续即使加入本地模型，也只能离线读取导出的实验片段，不能在按键热路径中阻塞输入或改写事实记录。

## 原生接入状态与剩余人工门禁

代码层已经完成：

1. `librime` 插件在实际候选页形成、选择、提交和组合区退格时创建固定大小的 native event；
2. 按键热路径只尝试写入有界 ring buffer，不等待文件、网络或模型；队列满时累计 `drop_count`；
3. macOS Squirrel 与 Windows Weasel 启动后台 watcher，把 native event 写入仓库外固定本地目录；
4. `pause` 会关闭 producer，而不只是停止落盘；
5. Rime 已标记为密码、安全输入、隐私模式和一次性输入的上下文继续强制不采集；
6. 合成宿主已验证 producer → ring → native 文件 → Go report，能识别“正确候选被纠错候选抢首位”。

剩余门禁是安装新开发预览后，在 macOS 与 Windows 各做一次明确授权的真实输入实验，核对：启动/暂停延迟、跨应用会话隔离、报告内容、队列丢帧计数，以及输入延迟没有可感知回退。在这项人工验收完成前，只能称“代码与自动化链路已接通”，不能称“当前已安装版本持续监控已验收”。

原生接口固定为：

```text
NativeEventRing::TryPush(BoundedNativeEvent) -> accepted | dropped
ReplayMessageSink::Drain() -> EventV1 JSONL
ReplayIngestor::Accept(EventV1) -> accepted | validation_error
```

这使采集、持久化和分析可以分别测试，也保证默认关闭、显式启用和实机验收三个状态不会被混为一谈。
