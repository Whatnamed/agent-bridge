# AGY OAuth / CloudCode POC

这是 `agent-bridge` monorepo 下完全隔离的 Google Antigravity/CloudCode 研究性 POC。

它只负责验证：

1. 一套独立的 Google OAuth authorization-code + PKCE 登录链路；
2. 直接调用 CloudCode `loadCodeAssist`、`fetchAvailableModels` 和原生 SSE `streamGenerateContent`；
3. `compat` 与 `minimal` 两种请求形态的差异；
4. 一个无文件系统、无 shell、无网络工具的 `get_test_value(name)` function-call 往返。

它不是 monitor/controller 的替代品，也不是服务、托盘程序、代理网关、账号池或生产集成。

## 严格边界

- 本目录是独立 Go module，不修改 `monitor\`、`controller\`、根仓库运行逻辑或现有 `127.0.0.1:18080`。
- 不调用、读取、复制或解析已安装 AGY CLI 的 Credential Manager 凭据。
- 不读取 Codex/ZCode 配置或 `auth.json`，不修改 PATH、系统环境、防火墙或监听配置。
- OAuth client id/secret 只从 `AGY_POC_CLIENT_ID` / `AGY_POC_CLIENT_SECRET` 读取；secret 不写入文件、不出现在输出中。请使用为本 POC 新建的 Google OAuth desktop client，不要把现有 AGY CLI 的 token 或 credential manager 当作输入。
- 访问令牌和刷新令牌保存于仓库之外：
  `%LOCALAPPDATA%\AgentBridge\agy-poc\oauth_creds.json`
  （代码使用 `LOCALAPPDATA`/`os.UserConfigDir` 解析，不硬编码当前用户名）。文件通过临时文件原子替换写入，并请求 `0600`；Windows 上仍应依赖并检查用户 profile 的 ACL。`logout` 只删除这个文件。
- POC 不写持久日志；终端输出只包含脱敏状态、模型/延迟/usage/响应文本。不会输出 token、Authorization header 或完整请求头。probe 的 prompt 不写入任何日志。
- `auth` / `models` / `probe` 的真实调用必须由用户明确执行；`go test ./...` 和 `go vet ./...` 不触发真实 OAuth 或生成请求。

## 准备独立 OAuth client

在 Google Cloud Console 创建一个供本 POC 使用的 OAuth desktop client，并确保回调 URI 与下列默认值完全一致：

```text
http://localhost:51121/oauth-callback
```

在 PowerShell 7 中为当前终端设置环境变量。不要把值写入仓库文件：

```powershell
$env:AGY_POC_CLIENT_ID = 'your-client-id.apps.googleusercontent.com'
$env:AGY_POC_CLIENT_SECRET = 'your-client-secret'
```

如果 desktop client 不要求 secret，可以把 `AGY_POC_CLIENT_SECRET` 留空；程序不会主动把它加入请求。

## 使用

在 `agy\` 目录执行：

```powershell
go run .\cmd\agy-oauth-poc auth
go run .\cmd\agy-oauth-poc auth-status
go run .\cmd\agy-oauth-poc models
go run .\cmd\agy-oauth-poc probe --mode compat --model gemini-3.7-flash-high
go run .\cmd\agy-oauth-poc probe --mode minimal --model gemini-3.7-flash-high
go run .\cmd\agy-oauth-poc probe --mode minimal --tool-test
go run .\cmd\agy-oauth-poc logout
```

`auth` 会在 loopback 上等待一次 OAuth callback，并打开浏览器；它不会触碰现有 AGY CLI 登录状态。`auth --verify` 可额外调用一次非生成的 `loadCodeAssist`，但默认不验证，以便把动作拆开观察。

`models` 只调用 `fetchAvailableModels`。`probe` 先调用 `loadCodeAssist` 获取 Cloud AI Companion project，再读取模型目录；只有之后才发送一次或（工具测试时）两次生成请求。

默认第一条 probe prompt 是：

```text
只回复：OAUTH_OK
```

输出包含 requested/resolved model、model resolution、SSE 首事件/首文本/首 reasoning/完成/总耗时、文本、finish reason 和 CloudCode usage 字段。`resolved_model` 只有在模型目录中观察到时才标记为 `catalog_exact`；否则保留用户明确请求的 id 并标记 `requested_unverified`，不会猜测 `tiered` 或其它 fallback id。

## compat 与 minimal

`compat` 保留当前公开参考实现所需的协议骨架：

- OAuth bearer header；
- `daily-cloudcode-pa.googleapis.com/v1internal:*` endpoint；
- `userAgent=antigravity`、`requestType=agent`；
- `requestId`、`request.sessionId`；
- `contents`、CloudCode function schema、`thoughtSignature`/function id；
- compatibility generation config（`includeThoughts=true`、`thinkingBudget=10001`）和一个短的 POC compatibility system instruction。

POC 没有复制公开参考仓库中的完整 Antigravity system prompt，因为其中包含宿主环境/用户元数据，不应被本项目带入或提交；因此这里验证的是 CloudCode 协议兼容性，而不是复刻 AGY CLI 的完整行为策略。

`minimal` 只移除本地 POC 的 compatibility system instruction 和默认 thinking config，仍保留上面的 CloudCode 外层协议字段。没有随机删除未知字段；如果 minimal 失败，应以 compat 的请求/响应证据为基准判断。

## 工具回合

`--tool-test` 只声明并执行：

```text
get_test_value(name)
```

客户端仅返回固定的 `AGY_POC_OK` 结果，并验证 model functionCall → client functionResponse → continuation → final text。任何其它函数名都会 fail closed；没有文件系统、shell、网络、进程或动态代码执行工具。

## 离线验证

```powershell
go test ./...
go vet ./...
```

测试使用 `httptest` 覆盖 OAuth URL/state/PKCE、脱敏错误、凭据原子保存/删除、401 refresh、跨 UTF-8 chunk 的 SSE、usage/finish、取消和安全工具往返。测试不会读取真实凭据，不会调用 `daily-cloudcode-pa.googleapis.com`，也不会发送 `/v1/responses` 或其它计费请求。

## 参考与当前限制

请求骨架参考当前公开的 `dvcrn/antigravity-oauth-proxy` 实现；该项目展示了 Google OAuth 登录、`loadCodeAssist`、CloudCode `v1internal` 路径和原生 SSE 方向。OAuth 的 `state`、offline refresh 和 loopback redirect 语义按 Google OAuth 文档实现。

当前 POC 不实现 GCP-managed project onboarding、账号轮换、自动 fallback、OpenAI/Responses 代理、托盘控制器或 monitor 集成。只有在离线测试通过、用户明确完成独立 OAuth、并且 compat/minimal/tool 的 live 证据都通过后，才有理由讨论后续集成；在此之前结论只能是研究性/条件性可行。
