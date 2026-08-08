# 云拼输入法（YunPin IME）

云拼是一个隐私优先、可自托管同步的中文拼音输入法项目。目标平台是 Windows 10/11 与 macOS 13+；iOS 17+ 为第二阶段。界面采用简洁的横向编号候选窗，交互接近主流中文输入法，但不复制任何商业输入法的品牌、皮肤或专有资源。

> 当前状态：开发预览。仓库已经包含可运行的候选排序核心、离线词库迁移工具、密文同步服务、Rime 平台配置和自动化测试；尚未发布经签名的 Windows/macOS 安装器。

## 核心特性

- 排序固定为：手动置顶个人词组 → 高频个人/迁移词组 → 公共高频词包 → Rime 基础候选。
- 置顶长词在两个完整音节或四个首字母后即可召回；完整拼音精确匹配排第一。
- 自动学习词使用两次后才进入同步词库；首页八项中个人候选最多两项。
- 候选热路径只读内存快照，不访问网络或磁盘。
- 个人词库在客户端加密；服务端只保存随机标识、令牌哈希和密文信封。
- 支持本地预览并导入 ChatGPT 导出、Codex 摘要、文本词库，以及通过独立 ImeWlConverter 迁移 `.scel`/`.bin`。

黄金测试包含长机构名“`中国石化销售股份有限公司河北石家庄石油分公司`”：`zhongguo...`、`zhongguoshihua...` 或 `zgsh...` 均应进入前三，完整拼音应为第一候选。该字符串是公开验收夹具，不是个人词库数据。

## 快速验证

```bash
make test-engine
make test-tools
make test-sync-docker
make privacy-check
```

启动本地同步服务：

```bash
docker compose up --build
curl http://127.0.0.1:8787/healthz
```

若部署到 NAS（例如 `192.168.1.127`），可改为：

```bash
cp .env.example .env
sed -i '' 's/^YUNPIN_HOST_BIND=.*/YUNPIN_HOST_BIND=192.168.1.127/' .env
docker compose up -d --build
curl http://192.168.1.127:8787/healthz
```

导入器默认只做预览；只有追加 `--confirm` 才会生成导入文件。输出必须放在仓库外：

```bash
cd tools/importer
python3 -m yunpin_importer import /path/to/conversations.json --kind chatgpt
python3 -m yunpin_importer import /path/to/conversations.json --kind chatgpt \
  --output "$HOME/Library/Application Support/YunPin/private/import.tsv" \
  --confirm IMPORT
```

### 如果你本机仍看到 5 个候选

通常是旧的用户配置文件未刷新。先不重编译的话，先按你实际使用端执行：

- macOS：

```bash
platform/macos/scripts/inject-rime-config.sh --force
killall Squirrel || true
```

- Windows（PowerShell）：

```powershell
Copy-Item -LiteralPath "$PWD\platform\rime\common\default.custom.yaml" -Destination "$env:APPDATA\YunPin\Rime\default.custom.yaml" -Force
Copy-Item -LiteralPath "$PWD\platform\rime\weasel\weasel.custom.yaml" -Destination "$env:APPDATA\YunPin\Rime\weasel.custom.yaml" -Force
Copy-Item -LiteralPath "$PWD\platform\windows\rime\rime_ice.custom.yaml" -Destination "$env:APPDATA\YunPin\Rime\rime_ice.custom.yaml" -Force
```

之后重新部署输入法（macOS 可在系统设置里重启输入法，Windows 退出并重新激活 YunPin），再试 `12345678` 是否生效。

## 数据与隐私边界

公共词包来自锁定版本的 Rime Ice、Rime Essay、THUOCL 与 phrase-pinyin-data；更新任务只提交待审核 PR。输入过程中绝不在线查询。历史对话只在本地提取短语、拼音和粗粒度频次，过滤原句、URL、IP、邮箱、路径、凭据、令牌、长数字与代码块。

真实个人词库、对话导出、搜狗原文件、服务数据库、密钥和签名材料均不得进入 Git、CI、日志、测试夹具或发行包。详见 [隐私模型](PRIVACY.md)、[安全政策](SECURITY.md) 与 [数据来源](docs/DATA_SOURCES.md)。

## 目录

- `engine/`：Apache-2.0 共享候选召回与排名核心。
- `localstore/`：记录级加密 SQLite、本地学习阈值与快照生成参考。
- `sync/`：Apache-2.0 Go 密文同步服务。
- `integration/`：真实 `protocol.Seal/ToWire` 到同步 HTTP 接口的跨模块验收。
- `tools/`：离线导入、脱敏和预览工具。
- `tools/public_pack/`：核验四个锁定上游并离线生成公共 Rime 词包。
- `platform/`：Weasel/Squirrel 配置与后续 GPL 补丁边界。
- `protocol/`：同步、加密和 CRDT 规范。
- `third_party/`：锁文件与固定提交的 Git 子模块；上游代码按需初始化。

## 开源与贡献

本仓库原创共享代码采用 Apache License 2.0。Weasel、Squirrel 衍生补丁与桌面发行物遵循 GPL-3.0；第三方词库各自保留原许可证。请先阅读 [许可证矩阵](docs/LICENSE_MATRIX.md) 和 [贡献指南](CONTRIBUTING.md)。

English documentation: [README.en.md](README.en.md)
