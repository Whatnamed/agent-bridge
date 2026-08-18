# AGY OAuth / CloudCode POC

这是 `agent-bridge` monorepo 下完全隔离的 Google Antigravity/CloudCode 研究性 POC。

它只负责验证：

1. 独立的 Google OAuth authorization-code + PKCE 登录链路；
2. 直接调用 CloudCode `loadCodeAssist`、`fetchAvailableModels` 和原生 SSE `streamGenerateContent`；
3. `compat` 与 `minimal` 两种请求形态；
4. 一个无文件系统、无 shell、无网络工具的 `get_test_value(name)` function-call 往返。

它不是 monitor/controller 的替代品，也不是服务、托盘程序、代理网关、账号池或生产集成。

## 严格边界

- 本目录是独立 Go module，只修改 `agy\`；不修改 `monitor\`、`controller\`、根仓库运行逻辑或现有 `127.0.0.1:18080`。
- 不调用、读取、复制或解析已安装 AGY CLI 的 Credential Manager 凭据。
- 不读取 Codex/ZCode 配置或 `auth.json`，不修改 PATH、系统环境、防火墙或监听配置。
- `antigravity` profile 使用当前直接 OAuth 实现采用的 desktop-client 配置和 CloudCode scopes；它仍然通过本 POC 自己的 browser OAuth + PKCE 流程创建一套新 token。不会复用已安装 AGY CLI 的 token。
- `custom` profile 才读取 `AGY_POC_CLIENT_ID` / `AGY_POC_CLIENT_SECRET`；client secret 不写入文件、不出现在输出中。
- 访问令牌和刷新令牌保存于仓库之外：
  `%LOCALAPPDATA%\AgentBridge\agy-poc\oauth_creds.json`
  （代码使用 `LOCALAPPDATA`/`os.UserConfigDir` 解析，不硬编码当前用户名）。文件通过临时文件原子替换写入，并请求 `0600`；Windows 上仍应依赖并检查用户 profile 的 ACL。`logout` 只删除这个文件。
- POC 不写持久日志；终端输出只包含脱敏状态、模型/延迟/usage/响应文本。不会输出 token、Authorization header 或完整请求头。probe 的 prompt 不写入任何日志。
- `auth` / `models` / `probe` 的真实调用必须由用户明确执行；`go test ./...` 和 `go vet ./...` 不触发真实 OAuth 或生成请求。

## OAuth profiles

CLI 默认使用：

```text
--oauth-profile antigravity
```

这是当前直接 Antigravity OAuth 实现使用的 desktop-client profile，包含固定的 OAuth endpoints、loopback callback 和以下 scopes：

```text
https://www.googleapis.com/auth/cloud-platform
https://www.googleapis.com/auth/userinfo.email
https://www.googleapis.com/auth/userinfo.profile
https://www.googleapis.com/auth/cclog
https://www.googleapis.com/auth/experimentsandconfigs
```

需要自有 OAuth client 时显式选择：

```powershell
$env:AGY_POC_CLIENT_ID = 'your-client-id.apps.googleusercontent.com'
$env:AGY_POC_CLIENT_SECRET = 'your-client-secret'
go run .\cmd\agy-oauth-poc auth --oauth-profile custom
```

两个 profile 都使用新的 browser OAuth + PKCE，不读取现有 AGY CLI credential manager。默认回调 URI 为：

```text
http://localhost:51121/oauth-callback
```

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

`models` 只调用 `fetchAvailableModels`。`probe` 先调用 `loadCodeAssist` 获取 Cloud AI Companion project，再读取模型目录；只有模型目录确认后才发送生成请求。

## 模型解析：fail closed

probe 不会把用户请求静默替换成 `defaultAgentModelId`、tiered model 或其它猜测的 fallback：

- catalog 中 exact match：`model_resolution=catalog_exact`，`actual_upstream_model` 就是 requested model；
- 只有代码中存在、且已针对当前 CloudCode 实现验证过的显式 alias 才允许 `alias_verified` 映射；输出同时包含 `requested_model` 和 `actual_upstream_model`；
- catalog 中不存在 requested model：`status=model_not_found`、`model_resolution=requested_unverified`，不发送 generation request。

因此 `resolved_model`（为兼容保留）和 `actual_upstream_model` 为空时，不能把这次结果当成指定模型的 benchmark。

## Prompt 与 tool-test

普通 probe 的默认 prompt 是：

```text
只回复：OAUTH_OK
```

`--tool-test` 且用户没有显式传 `--prompt` 时，CLI 使用确定性的：

```text
必须调用 get_test_value，参数 name="smoke"。获得工具结果后，只回复工具返回的 value。
```

如果用户显式传 `--prompt`，始终尊重该 prompt。`--tool-test` 只声明并执行 `get_test_value(name)`；客户端仅返回固定的 `AGY_POC_OK`，任何其它函数名都会 fail closed。

## Benchmark timing

输出把 preflight 与 generation 分开：

- preflight：`load_code_assist_ms`、`fetch_models_ms`；
- generation：`request_to_headers_ms`、`first_sse_event_ms`、`first_reasoning_ms`、`first_text_ms`/`ttft_ms`、`completion_ms`、`generation_total_ms`；
- 全流程：`whole_probe_total_ms`。

与 AGY CLI baseline 比较时只使用 generation 指标；`whole_probe_total_ms` 包含 OAuth token 读取/刷新之外的本次 probe preflight 和 generation 阶段，不能当作 TTFT。

## SSE 结束条件

- `[DONE]` 是正常完成；
- clean EOF 在已经观察到可信终止证据（`finishReason`、terminal candidate 或 usage）时也视为正常完成；
- clean EOF 没有任何可信终止证据时返回 `truncated CloudCode SSE stream`，不会伪装成成功。

## compat 与 minimal

`compat` 保留当前公开参考实现所需的协议骨架：

- OAuth bearer header；
- `daily-cloudcode-pa.googleapis.com/v1internal:*` endpoint；
- `userAgent=antigravity`、`requestType=agent`；
- `requestId`、`request.sessionId`；
- `contents`、CloudCode function schema、`thoughtSignature`/function id；
- compatibility generation config（`includeThoughts=true`、`thinkingBudget=10001`）和一个短的 POC compatibility system instruction。

POC 没有复制公开参考仓库中的完整 Antigravity system prompt，因为其中包含宿主环境/用户元数据，不应被本项目带入或提交；这里验证的是 CloudCode 协议兼容性，而不是复刻 AGY CLI 的完整行为策略。

`minimal` 只移除本地 POC 的 compatibility system instruction 和默认 thinking config，仍保留 CloudCode 外层协议字段。没有随机删除未知字段；如果 minimal 失败，应以 compat 的请求/响应证据为基准判断。

## 离线验证

```powershell
go test -count=1 ./...
go vet ./...
```

测试使用 `httptest` 覆盖 OAuth profile/URL/state/PKCE、脱敏错误、凭据原子保存/删除、401 refresh、跨 UTF-8 chunk 的 SSE、`[DONE]`、trusted clean EOF、truncated stream、模型 fail-closed、分离 timing、CLI tool-test 默认 prompt 和安全工具往返。测试不会读取真实凭据，不会调用 `daily-cloudcode-pa.googleapis.com`，也不会发送 `/v1/responses` 或其它计费请求。

## 参考与当前限制

请求骨架参考当前公开的 Antigravity direct-OAuth/CloudCode 实现；OAuth 的 `state`、offline refresh 和 loopback redirect 语义按 Google OAuth 文档实现。profile 只提供 OAuth 配置，不执行 GCP-managed project onboarding、账号轮换、自动 fallback、OpenAI/Responses 代理、托盘控制器或 monitor 集成。

只有在离线测试通过、用户明确选择并完成独立 OAuth、并且 compat/minimal/tool 的 live 证据都通过后，才有理由讨论后续集成；本轮不会自行开始真实 OAuth 或 live generation。
