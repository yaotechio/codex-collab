# codex-collab

> 一个极薄的 Go MCP Server，让 **Claude Code** 与本机 **Codex CLI** 协作：Claude 作架构师/审查者，Codex 作底层实现者。

中文 / [English](./README.en.md)

Claude 拿到需求后，多轮拉起 Codex **只读讨论**方案，定稿并经你确认后，再派 Codex **写模式实现**，最后由 Claude 对照方案验收。讨论循环由 Claude 的 agent loop 驱动；本 Server 只负责「单轮执行 Codex + 会话续接 + 安全护栏」。

## 特性

- **单一 `codex` 工具**，参数直映射 `codex exec` CLI。
- **会话续接**：讨论复用同一 `session_id`（Codex 记得上下文），实现时另起干净会话。
- **三层安全护栏**：写操作需显式确认 + PreToolUse hook 强制用户批准 + 默认只读。
- **轮次硬熔断**：单会话讨论轮次上限，防 Claude↔Codex 无限互掐烧 token。
- **单二进制、零外部依赖**，进程随用随起、超时或取消即终止 codex 子进程。

跨平台：纯 Go 实现，支持 **macOS / Linux / Windows**（amd64 与 arm64）。

## 依赖

- [Claude Code](https://claude.com/claude-code)
- [Codex CLI](https://github.com/openai/codex)（`codex` 在 PATH 中，已 `codex login`；Windows 上 `codex.cmd`/`codex.exe` 同样由 PATH 解析）
- **插件安装（方式 A）**：[Node.js](https://nodejs.org/) ≥ 16（提供 `npx`）——无需 Go
- **源码编译（方式 B）**：[Go](https://go.dev/) ≥ 1.23

## 安装

### 方式 A — 插件市场（推荐，免编译、免 Go）

在 Claude Code 里执行：

```
/plugin marketplace add yaotechio/codex-collab
/plugin install codex-collab@yaotechio
```

一步装齐 `codex` MCP server、`/codex-collab` 命令与写操作确认 hook（需 [Node.js](https://nodejs.org/) ≥ 16；首次运行需几秒初始化）。

装完**重启 Claude Code**（或运行 `/reload-plugins`），让 MCP server 连上即可使用。

更新：`/plugin update codex-collab@yaotechio`（或随 Claude Code 启动自动更新）。自定义参数见[配置](#配置)。

> 命令在 `/plugin` 选择器里可能显示为带命名空间的 `/codex-collab:codex-collab`。

### 方式 B — 源码编译（手动）

```bash
git clone git@github.com:yaotechio/codex-collab.git && cd codex-collab
make build      # 自动识别当前系统/架构，Windows 自动产出 codex-collab.exe
```

得到二进制 `./codex-collab`（Windows 为 `codex-collab.exe`）。记下其**绝对路径**，下面用 `<BIN>` 代指。

需要为别的平台/架构一次性出全套包：

```bash
make dist        # 在 ./dist 生成 linux/darwin/windows × amd64/arm64
```

> 没装 make 时可直接 `go build -o codex-collab .`，或用 `GOOS=linux GOARCH=arm64 go build ...` 交叉编译。

注册到 Claude Code：

```bash
claude mcp add codex-collab -s user \
  -e CODEX_MCP_MAX_ROUNDS=6 \
  -e CODEX_MCP_TIMEOUT=300 \
  -- <BIN>
```

验证：

```bash
claude mcp list      # 显示 codex-collab: connected
```

源码安装还需手动开两层护栏（插件安装已自动捆绑，无需手动）：

**写确认 hook**：在 `~/.claude/settings.json` 加，写模式调用会弹确认、只读放行：

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "mcp__codex-collab__codex",
        "hooks": [ { "type": "command", "command": "<BIN> hook" } ] }
    ]
  }
}
```

**协作命令**：把 `.claude/commands/codex-collab.md` 复制到 `~/.claude/commands/`（Windows：`%USERPROFILE%\.claude\commands\`），即可用 `/codex-collab`。

## 配置

环境变量（插件已带默认值；要改在启动 Claude Code 前导出对应变量即可）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `CODEX_MCP_MAX_ROUNDS` | 6 | 单会话最大讨论轮次（硬熔断） |
| `CODEX_MCP_TIMEOUT` | 300 | 单次 codex 调用超时（秒） |
| `CODEX_MCP_SESSION_TTL` | 24 | 会话轮次计数保留时长（小时） |

## 使用

最常用——一条命令走完全流程：

```
/codex-collab 优化 ./src/sort.go 的排序性能
```

Claude 会：只读多轮与 Codex 讨论 → 给出《最终方案》并问你确认 → 你确认后派 Codex 写文件（hook 再弹一次批准）→ 对照方案验收并汇报。

也可让 Claude 直接调 `codex` 工具，参数见下。

## `codex` 工具参考

普通使用走 `/codex-collab` 即可；如需直接调用 `codex` 工具（高级），展开查看参数与返回：

<details>
<summary>参数与返回</summary>

### 入参

| 参数 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `PROMPT` | string | 是 | — | 发给 Codex 的指令 |
| `cd` | string | 否 | `.` | 工作目录（仅新会话生效） |
| `sandbox` | string | 否 | `read-only` | `read-only` / `workspace-write` / `danger-full-access`（仅新会话生效） |
| `session_id` | string | 否 | — | 传入续接会话；不传新建 |
| `skip_git_repo_check` | bool | 否 | false | 允许非 git 仓库 |
| `model` | string | 否 | — | 指定 Codex 模型 |
| `return_all_messages` | bool | 否 | false | 返回完整推理/工具调用事件 |
| `confirmed` | bool | 否 | false | 写模式必须为 true，否则拒绝 |

> 注：`session_id` 续接时 `sandbox`/`cd` 由原会话锁定，传了也不生效——这正好保证讨论会话始终只读。

### 返回（JSON）

| 字段 | 说明 |
|---|---|
| `success` | 本轮是否成功 |
| `session_id` | 会话 id（新建时回传，供后续续接） |
| `output` | Codex 最终回复 / patch；`return_all_messages=true` 时为完整事件流 |
| `round` | 本会话已进行轮次 |
| `rounds_remaining` | 剩余可用轮次 |
| `error` | 失败时的结构化错误 |

</details>

## 开发

```bash
go test ./...      # 单元测试
go build -o codex-collab .
```

## 致谢

- [GuDaStudio/codexmcp](https://github.com/GuDaStudio/codexmcp)（MIT）—— 用 MCP 桥接 Claude Code 与 Codex 的整体构想受其启发；本项目为独立的 Go 重写实现。
- [multica-ai/andrej-karpathy-skills](https://github.com/multica-ai/andrej-karpathy-skills)（MIT）—— 协作流程中的编码原则（简化优先、外科手术式改动、目标驱动、先想后写）借鉴自该项目（源自 Andrej Karpathy 的观察）。

## License

[MIT](./LICENSE)
