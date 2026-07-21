# 给 AI 的信

这是一份给后续 AI/维护者快速接手 CLIProxyAPI 的项目地图。仓库是 Go 1.26+ 的多协议代理服务，对外提供 OpenAI/Gemini/Claude/Codex 兼容 API，对内把请求翻译、路由到不同 provider 的 OAuth/API key 凭据，并负责额度、冷却、日志、管理面板和插件体系。

## 一句话架构

请求从 `cmd/server/main.go` 启动进入 `sdk/cliproxy.Service`，由 `internal/api.Server` 注册 HTTP/WebSocket 路由；各协议 handler 在 `sdk/api/handlers/**` 里解析请求、构造 `cliproxyexecutor.Request/Options`，交给 `sdk/cliproxy/auth.Manager` 做认证选择、重试、冷却和执行；真正访问上游的代码在 `internal/runtime/executor/**`；协议翻译在 `internal/translator/**`；模型能力和别名由 `internal/registry/**` 与配置/认证合成器共同维护。

## 启动和生命周期

- `cmd/server/main.go`
  - CLI 入口，解析 `--config`、`--tui`、OAuth login、store backend、Home/cloud、local model 等参数。
  - 加载 `.env`，选择 file/Postgres/git/object store，构造 `sdk/cliproxy.Service`。
  - 远程模型更新由 `internal/registry.StartModelsUpdater` 管；`--local-model` 会禁用远程更新。

- `sdk/cliproxy/service.go`
  - 这是 embeddable SDK 的核心服务。
  - 创建 `coreauth.Manager`、注册 provider executors、启动 API server、启动 watcher、注册插件。
  - 认证变更后会 `Register/Update` auth，再调用 `registerModelsForAuth*` 把该 auth 支持的模型注册到全局 registry，最后 `RefreshSchedulerEntry` 刷新调度器。

- `internal/watcher/**`
  - 监听配置文件和 auth-dir JSON 文件。
  - `clients.go` 负责全量/增量 reload。
  - `synthesizer/file.go` 把 OAuth JSON 合成为 `coreauth.Auth`。
  - `synthesizer/config.go` 把 config 里的 API key 段合成为 `coreauth.Auth`。

## 主要目录职责

- `internal/config/`
  - YAML/JSON 配置结构、默认值、sanitize 和 clone。
  - 关键字段：`routing.strategy`、`routing.session-affinity`、`request-retry`、`max-retry-credentials`、`codex-api-key`、`oauth-model-alias`、`oauth-excluded-models`。

- `internal/api/`
  - Gin server、middleware、管理 API、特殊直通 route。
  - `server.go` 注册主路由、管理路由、Codex alpha search、WebSocket route。
  - `handlers/management/` 是管理面板后端：配置读写、auth 文件上传/patch、额度刷新、日志、插件等。
- `handlers/management/client_key_usage.go` 会作为 usage 插件统计下游客户端 `api-keys` 的 token、请求数、最近活跃终端数，以及活跃终端最近使用的模型和上游账户/凭据标识，并通过 `/v0/management/client-key-usage` 提供给仪表盘。终端身份只使用稳定的 `X-Client-Instance-Id`、`X-Device-Id`、`X-Terminal-Id`，没有这些请求头时使用“客户端 IP + User-Agent”；不要再使用 `Session_id`、`X-Client-Request-Id`、`Thread-Id`、`Conversation-Id`，这些值会按请求或对话变化并把同一个人错误拆成多个终端。

### 用户使用信息统计

- 后端实现位于 `internal/api/handlers/management/client_key_usage.go`，路由在 `internal/api/server.go` 注册。
- `GET /v0/management/client-key-usage` 返回全部已配置客户端 Key 的汇总数据，并在每项中返回稳定的 `key_id`。前端使用 `key_id` 请求明细，不会把完整客户端 API Key 放进查询参数。
- `GET /v0/management/client-key-usage/details?key_id=...&offset=0&limit=50` 返回指定使用人的逐次请求明细。接口只接受当前配置中存在的 Key 对应哈希，默认每页 50 条，最大 200 条，并返回 `total`、`has_more`、`next_offset`。
- 用户统计统一放在 `user-usages/` 目录。未设置 `WRITABLE_PATH` 时，该目录位于配置文件同目录；设置后位于 `WRITABLE_PATH` 指向的目录。
- `user-usages/cpa-user-usage-events.jsonl` 是汇总文件，每个客户端 Key 占一行，保存使用人、脱敏 Key、历史/当天 Token、请求次数和最后使用时间。旧版 `cpa-key-usage-state.json` 会在启动时读取并转换到新汇总文件。
- 每位使用人在 `user-usages/<使用人>/` 下拥有独立子目录，每天写入一个 `YYYY-MM-DD.jsonl`。旧版根目录单一明细文件会自动拆分迁移，并保留 `legacy` 时间戳备份。
- 逐次明细保存请求时间、完成时间、模型、provider、脱敏账户、终端哈希显示值、token、延迟、首 token 时间、状态、service tier、原始提示词和最终客户端响应正文；不会保存请求头或完整客户端 API Key。
- 请求/响应正文由 `internal/api/middleware/client_usage_capture.go` 捕获，经 `sdk/cliproxy/usage/content_capture.go` 在请求结束后传给 usage 插件。普通 JSON、SSE 流式响应和 zstd 压缩请求都支持正文保存。
- 前端页面位于管理中心仓库的 `src/pages/UserUsagePage.tsx` 与 `src/pages/UserUsagePage.module.scss`，菜单路由为 `#/user-usage`。页面初始只加载汇总；用户点击姓名后才读取明细，再点击每条记录的正文图标才展开提示词和回复正文。
- 管理面板是定制构建，部署时必须同时更新 `E:\MyWebsite\CLIProxyAPI\Exe\cli-proxy-api.exe` 和 `E:\MyWebsite\CLIProxyAPI\Exe\static\management.html`。生产配置必须保持 `remote-management.disable-auto-update-panel: true`，否则官方面板自动更新会覆盖定制页面。
- `sdk/api/handlers/`
  - 对外协议 handler。
  - `openai/`：OpenAI chat/responses/images/videos 和 `/v1/responses` WebSocket。
  - `claude/`、`gemini/`：对应协议入口。
  - handler 的核心工作是解析模型、选择 provider、组装 `executor.Options`，再调用 `ExecuteWithAuthManager` / `ExecuteStreamWithAuthManager`。

- `sdk/cliproxy/auth/`
  - 调度和认证状态中心。
  - `conductor.go`：`Manager`，负责 auth 注册、选择、执行、重试、刷新、冷却、结果记录。
  - `scheduler.go`：快速调度器，按 provider/model/priority/websocket 子集做 round-robin 或 fill-first。
  - `selector.go`：传统 selector、下游 API Key 的 provider 级共享轮询、session affinity、session ID 提取。
  - `types.go`：`Auth`、`ModelState`、quota、recent requests。

- `internal/runtime/executor/`
  - provider executor 实现。
  - Codex 重点文件：
    - `codex_executor.go`：Codex HTTP/SSE Responses 执行。
    - `codex_websockets_executor.go`：Codex upstream WebSocket transport。
    - `codex_openai_images.go`：Codex image API 路径。
  - Antigravity、Claude、Gemini、Kimi、xAI 也在这里。
  - 按约定，辅助文件放 `internal/runtime/executor/helps/`。

- `internal/thinking/`
  - 思考配置统一管线。
  - 重要约束：保持“canonical representation -> provider applier”的架构。
  - `ApplyThinking()` 解析后缀、规范化、校验，再交给 provider-specific applier。

- `internal/translator/`
  - 协议翻译实现。一般不要单独改这里；如果任务只要求改 translator，需要按 AGENTS.md 的权限规则先检查 GitHub 权限。

- `internal/registry/`
  - 模型 registry、内置模型 JSON、远程模型更新。
  - 调度器判断 auth 是否支持模型时，会查 `registry.GetGlobalRegistry().GetModelsForClient(auth.ID)`。

- `internal/store/`
  - token/auth 存储后端：file、Postgres、git、object store。

- `internal/pluginhost/`、`sdk/pluginapi/`
  - 插件加载、调度、模型路由、拦截器、管理扩展。

- `internal/home/`、`homeplugins/`
  - Home/cloud 控制面相关逻辑。Home enabled 时，auth 选择会走 Home dispatcher，而不是本地 scheduler。

- `internal/tui/`
  - Bubbletea TUI。

- `test/`
  - 跨模块集成测试和兼容性哨兵。

## 请求执行主链路

1. HTTP/WebSocket 请求进入 `internal/api.Server`。
2. 对应 handler 读取 body、headers、query，解析模型名和 stream/alt。
3. handler 调用 `BaseAPIHandler` 的执行方法。
4. `BaseAPIHandler` 根据模型解析 provider，设置 metadata，例如：
   - `requested_model`
   - `request_path`
   - `pinned_auth_id`
   - `selected_auth_callback`
   - `execution_session_id`
   - `disallow_free_auth`
5. `coreauth.Manager` 根据 provider/model/metadata 选择 auth。
6. `Manager` 调用 provider executor 的 `Execute` / `ExecuteStream`。
7. executor 做 provider-specific 请求构造、thinking 应用、payload 配置、上游调用和 usage 记录。
8. `Manager.MarkResult` 根据成功/错误更新 auth 状态、模型冷却、quota、recent requests。
9. handler 把上游响应翻译/封装回客户端协议。

## 认证和模型注册

- OAuth 文件默认在 `auth-dir` 下，file synthesizer 会读取 JSON 的 `type` 作为 provider。
- config 里的 API key 段会由 config synthesizer 生成虚拟 auth。
- auth 支持哪些模型不是只看 auth 本身，而是看 registry 中这个 auth ID 注册了哪些模型。
- 注册顺序很重要：`Manager.Register` 先入 auth，随后 `registerModelsForAuth` 注册模型，再 `RefreshSchedulerEntry` 让 scheduler 看到模型集合。
- Codex OAuth auth 文件里的 `websockets` 可在 metadata 中；调度器和 handler 会同时兼容 attributes/metadata。

## 调度和负载均衡

- 配置入口：`routing.strategy`
  - `round-robin`：同 provider/model/priority 桶内轮询。
  - `fill-first`：稳定选第一个可用账号，直到冷却/不可用。
- 普通下游请求只要能识别出客户端 `api-key`，就会绕过按 model 分片的快速调度路径，并由 `selector.go` 使用 provider 级共享游标统一轮询：所有 Key、所有模型共同推进同一个账号队列，同一个 Key 的连续请求也会继续换账号，不做固定绑定。
- 安装 `codex-token-usage` 插件后必须额外检查插件 Scheduler：插件 Scheduler 的优先级高于核心 `RoundRobinSelector`，只要插件返回 `Handled: true`，核心轮询就不会执行。
- 本机 `codex-token-usage v0.1.17` 的原始问题是：只要 SQLite 中存在任意一个 active autoban/invalid auth，插件就过滤被封禁账号后固定选择 `available[0]`。例如 Team 账号额度满时，剩余 4 个正常账号会全部被绕过轮询，所有请求持续落到候选列表第一个账号。
- 已修复的插件源码固定保存在：`E:\MyWebsite\CLIProxyAPI\CodexTokenUsageCode`，本地分支为 `cpa-round-robin-fix`。修复点在 `main.go` 的 `store.pickNextAvailableAuth()`：继续保留 429/401/402/403 封禁过滤和 priority 语义，但对过滤后最高优先级的可用账号使用插件级共享游标轮询；游标不按下游 Key 或模型拆分。
- 插件运行数据库默认位于：`E:\root\.cli-proxy-api\plugins\codex-token-usage\usage.db`。排查账号集中使用时，先查询 `autoban_bans WHERE active=1` 和 `invalid_auths WHERE active=1`，再确认是否由插件 Scheduler 接管。
- `routing.session-affinity` 会显式开启跨客户端的 session 粘性；但部分 handler 也会为了协议状态临时 pin auth。
- priority 数值越大越优先；不同 priority 不混轮询。
- Codex/xAI 下游 WebSocket 请求会优先选择 `websockets=true` 的 auth 子集。
- 发生 429/401/5xx 等错误时，`Manager` 会按 retry 设置尝试其他 auth，并更新冷却状态。

## Codex WebSocket 特别注意

- `/v1/responses` 的下游 WebSocket handler 在 `sdk/api/handlers/openai/openai_responses_websocket.go`。
- 它会给每个下游 WebSocket 连接分配 `execution_session_id`，Codex/xAI upstream WebSocket executor 会用这个 ID 复用上游连接。
- 只有请求携带 `previous_response_id` 或 `response.append` 这类依赖上游响应状态的 turn，才应该 pin 到上一轮成功 auth。
- 不带上游状态依赖的 turn 应继续交给 scheduler，这样 `round-robin` 才能在长连接里生效。
- executor 复用上游连接时必须检查当前连接的 `authID/wsURL` 是否等于这次调度选中的 auth；如果不同，要关闭旧连接并重新拨号。

## 日志和排障入口

- 请求日志：`internal/logging/` 和 `internal/api/middleware/request_logging.go`。
- 管理面日志 API：`internal/api/handlers/management/logs.go`。
- 客户端 Key 用量：`internal/api/handlers/management/client_key_usage.go`。它按配置中的下游 `api-keys` 输出使用人名字、历史 token、当天 token、历史/当天请求数、最后使用时间、最近 10 分钟活跃终端数，以及这些活跃终端最近使用的模型、provider、上游账户/凭据标识和 auth index；状态文件默认写入 `cpa-key-usage-state.json`。
- 客户端 Key 使用人映射：顶层配置 `api-key-names`，格式是 `map[完整 api-key]使用人名字`；后端结构字段在 `internal/config/sdk_config.go` 的 `SDKConfig.APIKeyNames`，API Keys 管理接口会在修改/删除 Key 时同步迁移或清理名字。
- auth 状态和额度：`internal/api/handlers/management/auth_files.go`、`quota.go`。
- 最近请求计数在 `coreauth.Auth.recentRequests`，管理面会展示 auth 近段时间成功/失败桶。

## 管理中心前端

- 当前核心仓库只内置打包后的 `static/management.html`。
- 本机用于这次修改的前端源码目录是：`E:\MyWebsite\CLIProxyAPI\Cli-Proxy-API-Management-Center`。
- 仪表盘页面：`src/pages/DashboardPage.tsx`。
- 仪表盘样式：`src/pages/DashboardPage.module.scss`。
- 客户端 Key 用量接口封装：`src/services/api/clientKeyUsage.ts`。
- 可视化配置编辑器：`src/hooks/useVisualConfig.ts` 负责读写 YAML，`src/components/config/VisualConfigEditorBlocks.tsx` 的 `ApiKeysCardEditor` 负责编辑 Key 和使用人名字。
- 新增可见文案要同步改 `src/i18n/locales/zh-CN.json`、`en.json`、`zh-TW.json`、`ru.json`。
- 前端构建产物是 `dist/index.html`，打包时复制到 `E:\MyWebsite\CLIProxyAPI\Exe\static\management.html`。

## 修改守则

- Go 改动后必须 `gofmt -w`。
- 最少验证：`go build -o test-output ./cmd/server && rm test-output`。
- 优先跑相关包测试，再跑 `go test ./...`。
- 不要泄露 token/API key 到日志。
- HTTP handler 不要 panic；返回有意义的状态码。
- 上游连接建立后的网络行为不要随便加 timeout；AGENTS.md 里列出的例外除外。
- `internal/translator/` 不要孤立改动。
- `internal/runtime/executor/` 只放 executor 和测试，辅助逻辑放 `helps/`。

## 打包约定

- 用户指定的 Windows 打包目录固定为：`E:\MyWebsite\CLIProxyAPI\Exe`。
- 打包产物应包含 `cli-proxy-api.exe`、`config.example.yaml`、`config.yaml`、`LICENSE`、`README.md`、`README_CN.md`，并保留 `plugins/`、`static/` 目录。
- 修复后的 Token Usage 插件主 DLL 固定部署到：`E:\MyWebsite\CLIProxyAPI\Exe\plugins\windows\amd64\codex-token-usage-v0.1.17.1.dll`；`config.yaml` 中的 `plugins.configs.codex-token-usage.store.version` 必须同步为 `0.1.17.1`，这样服务可选择修复版并支持运行时热重载。
- 该插件官方使用 Go 1.23.x 和 Windows CGO/MinGW 构建；本机可复用 `E:\MyWebsite\CLIProxyAPI\.tools\go1.23.12` 与 `E:\MyWebsite\CLIProxyAPI\.tools\w64devkit-2.0.0`。不要使用主项目的 Go 1.26 直接生成此旧版插件 DLL。
- 若需要保留运行状态文件，目标目录中使用 `cpa-key-policy-state.json`。

## 常用命令

```bash
gofmt -w .
go test ./...
go test -v -run TestName ./path/to/pkg
go build -o cli-proxy-api ./cmd/server
go build -o test-output ./cmd/server && rm test-output
```
