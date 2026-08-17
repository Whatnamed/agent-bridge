# Codex Bridge Tray Controller

这是 `openai-api-server-via-codex` 的轻量 Windows 托盘控制器。它只负责启动、停止、重启和观察本机 bridge，不读取或修改 Codex、ZCode、OAuth、uv/npm 全局环境、防火墙或网络监听配置。

## 文件

- `CodexBridge.ps1`：PowerShell 7 + WinForms 托盘控制器。
- `lib\BridgeModel.ps1`：纯 ownership、fingerprint、状态和菜单模型；不做进程、CIM、网络或文件 I/O。
- `Launch.vbs`：隐藏启动器。
- `InstallShortcut.ps1`：为当前用户创建桌面快捷方式。
- `tests\CodexBridge.Tests.ps1`：Pester 3.4 纯模型测试。
- `.github\workflows\ci.yml`：Windows GitHub Actions 解析检查和 Pester 验证。
- `README.md`：使用和安全边界说明。

## 配置

`CodexBridge.ps1` 顶部集中定义运行配置：

- uvx：`E:\Dev\uv\uvx.exe`
- bridge：`openai-api-server-via-codex==0.2.0`
- host：`127.0.0.1`
- port：`18080`
- health：`http://127.0.0.1:18080/healthz`
- run directory：`$env:USERPROFILE\.config\openai-api-server-via-codex\run\`
- PID file：`$env:USERPROFILE\.config\openai-api-server-via-codex\run\server-127.0.0.1-18080.pid`
- log file：`$env:USERPROFILE\.config\openai-api-server-via-codex\run\server-127.0.0.1-18080.log`
- auth path：`$env:USERPROFILE\.codex\auth.json`

当前固定使用已验证的 `0.2.0`。将来升级只修改顶部的 `$BridgeVersion`，启动和停止逻辑会自动使用同一个版本配置。

控制器通过统一的 runtime argument builder 为 `start`、`stop` 和 `status` 显式传入 host、port、state directory、PID file 和 log file；`start` 另外显式传入 auth path。外部 `config.toml`、环境变量或 bridge 默认值不会改变控制器认知的目标 instance。

## 安装和启动

在 PowerShell 7 中执行一次：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File E:\Codex\CodexBridge\InstallShortcut.ps1
```

桌面路径使用 `[Environment]::GetFolderPath('Desktop')` 动态取得。已有同名快捷方式只有在确认目标和参数都属于本控制器时才会更新；否则安装会拒绝覆盖。不创建管理员任务或开机启动。

`Launch.vbs` 依次尝试：

1. `%LOCALAPPDATA%\Microsoft\WindowsApps\pwsh.exe`
2. `%ProgramFiles%\PowerShell\7\pwsh.exe`
3. PATH 中的 `pwsh.exe`

每次都使用 `-NoProfile -NonInteractive -STA -WindowStyle Hidden`，不修改 PATH 或执行策略。启动器会确认托盘控制器进程已出现，不把仍在运行但未成功初始化的 PowerShell 进程误判为成功。

## 状态探测

状态由后台 worker 综合判断：health、PID 文件、PID 对应进程的名称/可执行文件/命令行/父 PID/CreationDate、从 PID-file supervisor 向下的子孙关系，以及 `18080` listener。WinForms Timer 只调度和回收异步任务，不在 UI 线程执行 HTTP、CLI、CIM、端口或进程树查询。

- 普通状态使用约 4 秒一次的 light probe，只查询 PID 文件、listener owner 和必要 fingerprint。
- 进程树变化、操作期间、异常或约 45 秒周期到达时执行 deep probe。
- worker 使用持久 `HttpClient`，任务串行且 single-flight，不会堆积重复 probe。
- 每个操作有序列号；旧结果不能覆盖新状态。
- 目标 instance affinity 会区分 `Bound`、`HistoricalBound`、`Unbound` 和 `Conflict`。PID 文件只证明 supervisor 身份，不自动证明它就是 18080 instance；只有目标 listener owner 与已验证 supervisor tree 一致时才会进入 `Bound`。
- 如果 bridge 自身创建了 Windows Terminal 控制台窗口，控制器只在 bridge 已验证为 `Bound` 时，按精确 executable path 隐藏对应窗口；不关闭 `WindowsTerminal.exe`、`conhost.exe`，也不影响其他终端窗口。

内部 operation state（`Starting`、`Stopping`、`Restarting`）与 observed state（`Running`、`Stopped`、`Degraded`、`Conflict`、`Unknown`）分离。启动中、停止中和重启中会禁用启动/停止/重启；Running 禁用启动；Stopped 只启用启动；未知端口占用时不提供强制停止。

## 停止和安全 fallback

停止总是先执行固定版本的官方命令：

```text
uvx --from openai-api-server-via-codex==0.2.0 openai-api-server-via-codex stop --host 127.0.0.1 --port 18080 --state-dir <RunDirectory> --pid-file <PidFilePath> --log-file <LogFilePath>
```

官方 stop 返回后，控制器会确认它自己的 CLI invocation 已退出，并捕获有上限的 stdout/stderr；超时时只回收该控制器创建的 CLI invocation process tree，无法确认退出时不会进入 bridge fallback。

进入 fallback 前必须保留停止前 ownership snapshot。fallback 的 root 只能是 PID 文件对应且命令行包含 `daemon-run` 的已验证 supervisor；ownership 只向下遍历该 root 的 descendants，不向上扩展共同祖先，也不根据端口拥有者直接杀进程。

每个可终止进程都必须同时满足：

- 位于已验证 supervisor 的 ownership tree，或是 supervisor 消失前在其 descendants 中捕获的 child；
- PID、父 PID、CreationDate、Name、ExecutablePath、CommandLine fingerprint 与预期一致；
- `18080` listener 属于同一已验证 tree；
- 进程身份和观察结果完整，没有 foreign/unknown listener。

fallback 的候选 PID 先由纯 `Get-FallbackActionPlan` 根据 snapshot、当前进程记录和 listener 记录计算，再由后台 worker 逐项重新 fingerprint；计划不通过时不会调用终止命令。

若 supervisor 会重启 child，先终止已验证 supervisor，再重新扫描。仍然存活的 child 只有在此前或 supervisor 仍被验证期间捕获过完整 fingerprint 才能处理；出现未纳入 ownership 的 bridge process、foreign listener、PID reuse 或任何查询不完整时，停止 fallback 并提示，不杀未知进程。bridge fallback 不使用未经筛选的 `Kill(entireProcessTree: true)`；仅 controller-owned 的 CLI 超时回收可以使用 process-tree kill。

成功停止必须同时确认 supervisor、bridge child/server、18080 listener、health endpoint 和 PID 文件对应进程均已消失，之后才会安全清理 stale PID 文件。

## 日志和运行目录

- 日志：`$env:USERPROFILE\.config\openai-api-server-via-codex\run\server-127.0.0.1-18080.log`
- 运行目录可从托盘菜单打开。
- 控制器只检查 `auth.json` 是否存在，绝不读取、打印、复制或保存其内容。

## 测试

纯模型测试不启动 bridge、不访问 `/v1/responses`，只覆盖 ownership、共同祖先隔离、PID reuse、fingerprint、respawn、foreign listener、其他端口实例、stale PID 和菜单状态：

```powershell
pwsh -NoProfile -NonInteractive -Command "Import-Module Pester -RequiredVersion 3.4.0; Invoke-Pester -Path E:\Codex\CodexBridge\tests\CodexBridge.Tests.ps1"
```

真实集成验证只使用 `/healthz`、listener、PID/进程观察和官方 start/stop，不发送计费的 `/v1/responses` 请求。
