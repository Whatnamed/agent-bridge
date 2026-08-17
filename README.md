# Codex Bridge Tray Controller

这是 `openai-api-server-via-codex` 的轻量 Windows 托盘控制器，不修改 bridge、Codex、ZCode 或 OAuth 配置。

## 文件

- `CodexBridge.ps1`：PowerShell 7 + WinForms 托盘控制器。
- `Launch.vbs`：隐藏启动器，不留下长期 PowerShell/CMD 黑窗口。
- `InstallShortcut.ps1`：一次性创建当前用户桌面的 `Codex Bridge.lnk`。
- `README.md`：使用与安全边界说明。

## 一次性安装桌面快捷方式

在 PowerShell 7 中执行：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File E:\Codex\CodexBridge\InstallShortcut.ps1
```

也可以直接双击 `Launch.vbs` 启动控制器。

桌面路径由 `[Environment]::GetFolderPath('Desktop')` 动态取得，不写死用户目录。

## 当前 bridge 配置

- uvx：`E:\Dev\uv\uvx.exe`
- package：`openai-api-server-via-codex==0.2.0`
- host：`127.0.0.1`
- port：`18080`
- health：`http://127.0.0.1:18080/healthz`
- run directory：`C:\Users\hasee\.config\openai-api-server-via-codex\run\`
- log：`server-127.0.0.1-18080.log`

将来升级 bridge 时只需修改 `CodexBridge.ps1` 顶部的 `$BridgeVersion`。

## 状态与操作

状态不依赖 CLI `status` 文本，而是后台综合检查 health、PID 文件、CIM 进程记录、父子进程关系和 18080 listener。

所有 HTTP、CLI、CIM、端口和进程树操作都在单独的持久 PowerShell `Runspace` 中串行执行。WinForms Timer 只轮询异步 invocation 状态和调度，不在 UI 线程同步执行这些操作；probe 具有 single-flight 保护，不会堆积重复任务。后台 invocation 失败只进入一次异常状态/通知，不会重复弹窗。

停止时先执行官方 stop。官方 stop 返回非零并不直接视为最终失败，控制器会重新检查状态；只有在 PID 文件身份、bridge 可执行文件、父子关系和 listener 归属均确认后，才会终止已验证的 bridge process tree。

实测拓扑（PID 会随每次启动变化；以下为一次稳定采样）为：

```text
PID file 8760: openai-api-server-via-codex.exe daemon-run (supervisor)
└─ 13712: openai-api-server-via-codex.exe serve (listener 127.0.0.1:18080)
   └─ conhost.exe (not a bridge identity; never force-killed)
```

不会根据端口直接杀未知进程，也不会使用未经筛选的 `Kill(entireProcessTree: true)`。最终只有在 supervisor、bridge child、listener 和 health endpoint 均消失后，才清理 stale PID 文件。

## 安全边界

- 不读取、打印、复制或修改 `C:\Users\hasee\.codex\auth.json` 内容。
- 不保存 OAuth token 或第三方 API key。
- 不监听公网，不修改防火墙。
- 不修改 ZCode、Codex、bridge package、npm/uv 全局环境或 PATH。
- 不申请管理员权限。
- 不创建 Windows 开机自启动。
