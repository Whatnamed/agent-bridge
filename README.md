# Agent Bridge

这是本机 Agent Bridge 产品的统一私有 monorepo。

## 目录

- `controller\`：PowerShell 7 + WinForms 托盘控制器，负责 bridge 的启动、停止、重启、状态探测和安全 fallback。
- `monitor\`：Codex Bridge monitor/server，提供本机 `127.0.0.1:18080` 服务、Dashboard、telemetry 和历史查询。
- `agy\`：未来需要时可加入的 Agent/AGY 集成目录，当前不创建运行代码。

外层目录 `E:\Projects\agent-bridge\` 用作主工作树和以后新增工作树的容器；本目录 `E:\Projects\agent-bridge\agent-bridge\` 是实际 Git 仓库。

## 本地运行路径

控制器通过自身目录计算 monitor binary 路径：

```text
E:\Projects\agent-bridge\agent-bridge\monitor\bin\openai-api-server-via-codex.exe
```

运行目录、PID、日志、health、Dashboard 和 `auth.json` 仍保持原有用户级位置与 `127.0.0.1:18080` 配置。控制器不读取或输出 `auth.json` 内容。

## 开发

Monitor：

```powershell
Set-Location E:\Projects\agent-bridge\agent-bridge\monitor
go test ./...
go vet ./...
```

Controller：

```powershell
Set-Location E:\Projects\agent-bridge\agent-bridge\controller
Import-Module Pester -RequiredVersion 3.4.0
Invoke-Pester -Path .\tests\CodexBridge.Tests.ps1
```

完整的 monitor 使用说明在 `monitor\README.md` 和 `monitor\docs\`；托盘安全边界在 `controller\README.md`。
