#requires -Version 7.0

[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

if ([System.Threading.Thread]::CurrentThread.ApartmentState -ne [System.Threading.ApartmentState]::STA) {
    [Console]::Error.WriteLine('Codex Bridge controller requires PowerShell 7 in STA mode.')
    exit 2
}

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

# ---- Runtime configuration: change BridgeVersion only when upgrading the verified bridge. ----
$UvxPath = 'E:\Dev\uv\uvx.exe'
$BridgePackage = 'openai-api-server-via-codex'
$BridgeVersion = '0.2.0'
$BridgeCommand = $BridgePackage
$BridgePackageSpec = "$BridgePackage==$BridgeVersion"
$BridgeIdentityMarker = $BridgePackage
$BridgeExecutableName = "$BridgePackage.exe"
$BridgeHost = '127.0.0.1'
[int]$BridgePort = 18080
$RunDirectory = 'C:\Users\hasee\.config\openai-api-server-via-codex\run'
$PidFileName = 'server-127.0.0.1-18080.pid'
$HealthUri = "http://$BridgeHost`:$BridgePort/healthz"
$AuthJsonPath = 'C:\Users\hasee\.codex\auth.json'
$DesktopPath = [Environment]::GetFolderPath('Desktop')

[int]$HealthTimeoutMilliseconds = 1500
[int]$RefreshIntervalMilliseconds = 1000
[int]$StartTimeoutSeconds = 60
[int]$StopGraceMilliseconds = 2000
[int]$StopTimeoutSeconds = 12

$script:Config = [pscustomobject]@{
    UvxPath = $UvxPath
    BridgePackage = $BridgePackage
    BridgeVersion = $BridgeVersion
    BridgeCommand = $BridgeCommand
    BridgePackageSpec = $BridgePackageSpec
    BridgeIdentityMarker = $BridgeIdentityMarker
    BridgeExecutableName = $BridgeExecutableName
    BridgeHost = $BridgeHost
    BridgePort = $BridgePort
    RunDirectory = $RunDirectory
    PidFilePath = (Join-Path $RunDirectory $PidFileName)
    HealthUri = $HealthUri
    AuthJsonPath = $AuthJsonPath
    HealthTimeoutMilliseconds = $HealthTimeoutMilliseconds
    RefreshIntervalMilliseconds = $RefreshIntervalMilliseconds
    StartTimeoutSeconds = $StartTimeoutSeconds
    StopGraceMilliseconds = $StopGraceMilliseconds
    StopTimeoutSeconds = $StopTimeoutSeconds
}

function Get-HealthOk {
    try {
        $client = [System.Net.Http.HttpClient]::new()
        $cancellation = [System.Threading.CancellationTokenSource]::new($script:Config.HealthTimeoutMilliseconds)
        try {
            $response = $client.GetAsync($script:Config.HealthUri, $cancellation.Token).GetAwaiter().GetResult()
            return ([int]$response.StatusCode -ge 200 -and [int]$response.StatusCode -lt 300)
        } finally {
            $cancellation.Dispose()
            $client.Dispose()
        }
    } catch {
        return $false
    }
}

function Get-PidFileValue {
    try {
        if (-not (Test-Path -LiteralPath $script:Config.PidFilePath -PathType Leaf)) { return $null }
        $raw = (Get-Content -LiteralPath $script:Config.PidFilePath -Raw).Trim()
        $processId = 0
        if ([int]::TryParse($raw, [ref]$processId) -and $processId -gt 0) { return $processId }
    } catch { }
    return $null
}

function Get-ProcessRecords {
    try {
        return @(
            Get-CimInstance Win32_Process |
                Select-Object ProcessId,ParentProcessId,Name,ExecutablePath,CommandLine
        )
    } catch {
        return @()
    }
}

function Get-ListenerRecords {
    try {
        return @(
            Get-NetTCPConnection -LocalPort $script:Config.BridgePort -State Listen |
                Select-Object LocalAddress,LocalPort,OwningProcess,State
        )
    } catch {
        return @()
    }
}

function Get-RelatedRecords {
    param(
        [object[]] $Records = @(),
        [int[]] $AnchorPids = @()
    )

    $byPid = @{}
    foreach ($record in $Records) {
        $byPid[[int]$record.ProcessId] = $record
    }

    $ids = [System.Collections.Generic.HashSet[int]]::new()
    foreach ($anchorPid in $AnchorPids) {
        if ($anchorPid -gt 0) { [void]$ids.Add($anchorPid) }
    }

    foreach ($anchorPid in $AnchorPids) {
        $currentPid = $anchorPid
        while ($currentPid -gt 0 -and $byPid.ContainsKey($currentPid)) {
            $parentPid = [int]$byPid[$currentPid].ParentProcessId
            if ($parentPid -le 0 -or $parentPid -eq $currentPid -or -not $ids.Add($parentPid)) { break }
            $currentPid = $parentPid
        }
    }

    $changed = $true
    while ($changed) {
        $changed = $false
        foreach ($record in $Records) {
            $processId = [int]$record.ProcessId
            $parentProcessId = [int]$record.ParentProcessId
            if ($ids.Contains($parentProcessId) -and $ids.Add($processId)) {
                $changed = $true
            }
        }
    }

    return @($Records | Where-Object { $ids.Contains([int]$_.ProcessId) })
}

function Test-BridgeIdentity {
    param([Parameter(Mandatory)] $Record)

    $name = [string]$Record.Name
    $path = [string]$Record.ExecutablePath
    $commandLine = [string]$Record.CommandLine
    $identityPattern = '(?i)(^|[\s"/\\])' + [regex]::Escape($script:Config.BridgeIdentityMarker) + '(?:\.exe)?([\s"]|$)'

    return (
        $name -ieq $script:Config.BridgeExecutableName -or
        $name -ieq $script:Config.BridgePackage -or
        $path -match [regex]::Escape($script:Config.BridgeIdentityMarker) -or
        $commandLine -match $identityPattern
    )
}

function Get-BridgeTopology {
    param(
        [object[]] $Records = @(),
        [object[]] $Listeners = @(),
        [int[]] $AnchorPids = @()
    )

    $pidFilePid = Get-PidFileValue
    $allBridgeRecords = @($Records | Where-Object { Test-BridgeIdentity $_ })
    $allBridgePids = @($allBridgeRecords | ForEach-Object { [int]$_.ProcessId } | Sort-Object -Unique)

    $anchors = @($AnchorPids + @($pidFilePid) | Where-Object { $_ -and [int]$_ -gt 0 } | ForEach-Object { [int]$_ } | Sort-Object -Unique)
    $relatedRecords = if ($anchors.Count -gt 0) {
        @(Get-RelatedRecords -Records $Records -AnchorPids $anchors)
    } else {
        @()
    }
    $bridgeTreeRecords = @($relatedRecords | Where-Object { Test-BridgeIdentity $_ })
    $bridgeTreePids = @($bridgeTreeRecords | ForEach-Object { [int]$_.ProcessId } | Sort-Object -Unique)
    $listenerPids = @($Listeners | ForEach-Object { [int]$_.OwningProcess } | Sort-Object -Unique)
    $unknownListenerPids = @($listenerPids | Where-Object { $allBridgePids -notcontains $_ })
    $listenerOutsideTreePids = @($listenerPids | Where-Object { $bridgeTreePids -notcontains $_ })
    $pidFileRecord = if ($pidFilePid) {
        $Records | Where-Object { [int]$_.ProcessId -eq $pidFilePid } | Select-Object -First 1
    } else {
        $null
    }

    $pidFileIdentityVerified = $null -ne $pidFileRecord -and (Test-BridgeIdentity $pidFileRecord)
    $canForceStop = $pidFileIdentityVerified -and
        $bridgeTreePids.Count -gt 0 -and
        $unknownListenerPids.Count -eq 0 -and
        $listenerOutsideTreePids.Count -eq 0

    [pscustomobject]@{
        PidFilePid = $pidFilePid
        PidFileRecord = $pidFileRecord
        PidFileProcessExists = $null -ne $pidFileRecord
        PidFileIdentityVerified = $pidFileIdentityVerified
        AllBridgeRecords = $allBridgeRecords
        AllBridgePids = $allBridgePids
        RelatedRecords = $relatedRecords
        BridgeTreeRecords = $bridgeTreeRecords
        BridgeTreePids = $bridgeTreePids
        ListenerPids = $listenerPids
        UnknownListenerPids = $unknownListenerPids
        ListenerOutsideTreePids = $listenerOutsideTreePids
        CanForceStop = $canForceStop
    }
}

function Find-LogPath {
    $exactPath = Join-Path $script:Config.RunDirectory 'server-127.0.0.1-18080.log'
    try {
        if (Test-Path -LiteralPath $exactPath -PathType Leaf) { return $exactPath }
        if (-not (Test-Path -LiteralPath $script:Config.RunDirectory -PathType Container)) { return $null }
        $candidate = Get-ChildItem -LiteralPath $script:Config.RunDirectory -File -Filter 'server-127.0.0.1-18080*.log' |
            Sort-Object LastWriteTime -Descending |
            Select-Object -First 1
        if ($candidate) { return $candidate.FullName }
    } catch { }
    return $null
}

function Get-BridgeObservation {
    $records = @(Get-ProcessRecords)
    $listeners = @(Get-ListenerRecords)
    $topology = Get-BridgeTopology -Records @($records) -Listeners @($listeners)
    $healthOk = Get-HealthOk
    $logPath = Find-LogPath

    [pscustomobject]@{
        Timestamp = [DateTimeOffset]::Now
        HealthOk = $healthOk
        PidFilePresent = Test-Path -LiteralPath $script:Config.PidFilePath -PathType Leaf
        PidFilePid = $topology.PidFilePid
        PidFileProcessExists = $topology.PidFileProcessExists
        PidFileIdentityVerified = $topology.PidFileIdentityVerified
        ListenerPids = @($topology.ListenerPids)
        UnknownListenerPids = @($topology.UnknownListenerPids)
        ListenerOutsideTreePids = @($topology.ListenerOutsideTreePids)
        AllBridgePids = @($topology.AllBridgePids)
        BridgeTreePids = @($topology.BridgeTreePids)
        BridgeTreeRecords = @($topology.BridgeTreeRecords)
        CanForceStop = $topology.CanForceStop
        LogPath = $logPath
        RunDirectoryExists = Test-Path -LiteralPath $script:Config.RunDirectory -PathType Container
    }
}

function Test-RunningEvidence {
    param([Parameter(Mandatory)] $Observation)

    return (
        $Observation.HealthOk -and
        $Observation.PidFileIdentityVerified -and
        @($Observation.BridgeTreePids).Count -gt 0 -and
        @($Observation.ListenerPids).Count -gt 0 -and
        @($Observation.UnknownListenerPids).Count -eq 0 -and
        @($Observation.ListenerOutsideTreePids).Count -eq 0
    )
}

function Get-BridgeState {
    param(
        [Parameter(Mandatory)] $Observation,
        [string] $OperationState = 'Idle'
    )

    switch ($OperationState) {
        'Starting' { return '启动中' }
        'Stopping' { return '停止中' }
        'Restarting' { return '重启中' }
    }

    $hasUnknownListener = @($Observation.UnknownListenerPids).Count -gt 0
    $hasListenerOutsideTree = @($Observation.ListenerOutsideTreePids).Count -gt 0
    $hasBridgeProcess = @($Observation.AllBridgePids).Count -gt 0

    if (Test-RunningEvidence -Observation $Observation) { return '已运行' }
    if (-not $Observation.HealthOk -and
        @($Observation.ListenerPids).Count -eq 0 -and
        -not $hasBridgeProcess) {
        return '已停止'
    }
    if ($hasUnknownListener -or $hasListenerOutsideTree -or $hasBridgeProcess) { return '异常' }
    return '异常'
}

function Get-StatusTooltip {
    param([string]$State)

    switch ($State) {
        '已运行' { return 'Codex Bridge — Running' }
        '已停止' { return 'Codex Bridge — Stopped' }
        '启动中' { return 'Codex Bridge — Starting' }
        '停止中' { return 'Codex Bridge — Stopping' }
        '重启中' { return 'Codex Bridge — Restarting' }
        default { return 'Codex Bridge — Error' }
    }
}

function New-HiddenProcess {
    param([Parameter(Mandatory)] [string[]] $Arguments)

    if (-not (Test-Path -LiteralPath $script:Config.UvxPath -PathType Leaf)) {
        throw "找不到 uvx：$($script:Config.UvxPath)"
    }

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $script:Config.UvxPath
    $startInfo.WorkingDirectory = $script:Config.RunDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    foreach ($argument in $Arguments) {
        [void]$startInfo.ArgumentList.Add($argument)
    }
    return [System.Diagnostics.Process]::Start($startInfo)
}

function Invoke-UvxCommand {
    param(
        [Parameter(Mandatory)] [string[]] $Arguments,
        [int] $WaitMilliseconds = 15000
    )

    try {
        $process = New-HiddenProcess -Arguments $Arguments
        $process.WaitForExit($WaitMilliseconds)
        $exited = $process.HasExited
        $exitCode = if ($exited) { $process.ExitCode } else { $null }
        $process.Dispose()
        return [pscustomobject]@{
            Started = $true
            Exited = $exited
            ExitCode = $exitCode
            Error = $null
        }
    } catch {
        return [pscustomobject]@{
            Started = $false
            Exited = $true
            ExitCode = $null
            Error = $_.Exception.Message
        }
    }
}

function Wait-ForStarted {
    param([System.Diagnostics.Process]$StartProcess)

    $deadline = [DateTime]::UtcNow.AddSeconds($script:Config.StartTimeoutSeconds)
    $lastObservation = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        $lastObservation = Get-BridgeObservation
        if (Test-RunningEvidence -Observation $lastObservation) {
            return [pscustomobject]@{
                Success = $true
                Observation = $lastObservation
                Message = 'Codex Bridge 已启动'
                ExitCode = if ($StartProcess -and $StartProcess.HasExited) { $StartProcess.ExitCode } else { $null }
            }
        }
        Start-Sleep -Milliseconds 500
    }

    if (-not $lastObservation) { $lastObservation = Get-BridgeObservation }
    [pscustomobject]@{
        Success = $false
        Observation = $lastObservation
        Message = "Codex Bridge 启动超时。请打开日志：$($lastObservation.LogPath ?? (Join-Path $script:Config.RunDirectory 'server-127.0.0.1-18080.log'))"
        ExitCode = if ($StartProcess -and $StartProcess.HasExited) { $StartProcess.ExitCode } else { $null }
    }
}

function Wait-ForStopped {
    param([int]$TimeoutSeconds = $script:Config.StopTimeoutSeconds)

    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastObservation = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        $lastObservation = Get-BridgeObservation
        if (-not $lastObservation.HealthOk -and
            @($lastObservation.ListenerPids).Count -eq 0 -and
            @($lastObservation.AllBridgePids).Count -eq 0) {
            return [pscustomobject]@{ Success = $true; Observation = $lastObservation }
        }
        Start-Sleep -Milliseconds 500
    }
    if (-not $lastObservation) { $lastObservation = Get-BridgeObservation }
    [pscustomobject]@{ Success = $false; Observation = $lastObservation }
}

function Get-ProcessDepth {
    param(
        [Parameter(Mandatory)] $Record,
        [Parameter(Mandatory)] [hashtable] $ByPid
    )

    $depth = 0
    $currentPid = [int]$Record.ProcessId
    while ($ByPid.ContainsKey($currentPid)) {
        $parentPid = [int]$ByPid[$currentPid].ParentProcessId
        if ($parentPid -le 0 -or $parentPid -eq $currentPid -or -not $ByPid.ContainsKey($parentPid)) { break }
        $depth++
        $currentPid = $parentPid
    }
    return $depth
}

function Stop-VerifiedBridgeTree {
    param([Parameter(Mandatory)] $InitialObservation)

    $originalPid = $InitialObservation.PidFilePid
    if (-not $originalPid -or -not $InitialObservation.PidFileIdentityVerified) {
        return [pscustomobject]@{
            Success = $false
            FallbackUsed = $false
            Refused = $true
            Observation = $InitialObservation
            Message = '无法确认 PID 文件对应的 bridge supervisor 身份，未执行强制终止。'
        }
    }

    $anchorPids = [System.Collections.Generic.HashSet[int]]::new()
    [void]$anchorPids.Add([int]$originalPid)
    $killedPids = [System.Collections.Generic.List[int]]::new()

    for ($pass = 0; $pass -lt 6; $pass++) {
        $records = @(Get-ProcessRecords)
        $listeners = @(Get-ListenerRecords)
        $currentPid = Get-PidFileValue
        if ($currentPid) { [void]$anchorPids.Add([int]$currentPid) }
        $topology = Get-BridgeTopology -Records @($records) -Listeners @($listeners) -AnchorPids @($anchorPids)

        if (@($topology.UnknownListenerPids).Count -gt 0 -or
            @($topology.ListenerOutsideTreePids).Count -gt 0) {
            $observation = Get-BridgeObservation
            return [pscustomobject]@{
                Success = $false
                FallbackUsed = $true
                Refused = $true
                Observation = $observation
                Message = '18080 仍被未能验证身份或进程树关系的进程占用，未执行强制终止。'
            }
        }

        $targets = @($topology.BridgeTreeRecords)
        if ($targets.Count -eq 0) { break }

        $byPid = @{}
        foreach ($record in $topology.RelatedRecords) { $byPid[[int]$record.ProcessId] = $record }
        $orderedTargets = @($targets | Sort-Object @{Expression={ Get-ProcessDepth -Record $_ -ByPid $byPid }}, ProcessId)

        foreach ($record in $orderedTargets) {
            $processId = [int]$record.ProcessId
            try {
                $liveRecord = Get-CimInstance Win32_Process -Filter "ProcessId = $processId" -ErrorAction Stop
                if ($liveRecord -and (Test-BridgeIdentity $liveRecord)) {
                    Stop-Process -Id $processId -Force -ErrorAction Stop
                    $killedPids.Add($processId)
                }
            } catch {
                # A process may have exited between the verified snapshot and Kill.
            }
        }

        Start-Sleep -Milliseconds 500
        $afterPass = Get-BridgeObservation
        if (-not $afterPass.HealthOk -and
            @($afterPass.ListenerPids).Count -eq 0 -and
            @($afterPass.AllBridgePids).Count -eq 0) {
            return [pscustomobject]@{
                Success = $true
                FallbackUsed = $true
                Refused = $false
                Observation = $afterPass
                KilledPids = @($killedPids)
                Message = 'Codex Bridge 已停止'
            }
        }
    }

    $finalObservation = Get-BridgeObservation
    $success = -not $finalObservation.HealthOk -and
        @($finalObservation.ListenerPids).Count -eq 0 -and
        @($finalObservation.AllBridgePids).Count -eq 0
    [pscustomobject]@{
        Success = $success
        FallbackUsed = $true
        Refused = -not $success
        Observation = $finalObservation
        KilledPids = @($killedPids)
        Message = if ($success) { 'Codex Bridge 已停止' } else { '无法在安全验证进程树后完成停止，未继续终止未知进程。' }
    }
}

function Remove-StalePidFileIfSafe {
    param([Parameter(Mandatory)] $Observation)

    if (-not $Observation.HealthOk -and
        @($Observation.ListenerPids).Count -eq 0 -and
        @($Observation.AllBridgePids).Count -eq 0 -and
        -not $Observation.PidFileProcessExists -and
        (Test-Path -LiteralPath $script:Config.PidFilePath -PathType Leaf)) {
        try { Remove-Item -LiteralPath $script:Config.PidFilePath -Force } catch { }
    }
}

function Invoke-StartOperation {
    $before = Get-BridgeObservation
    if (Test-RunningEvidence -Observation $before) {
        return [pscustomobject]@{ Success = $true; Operation = 'Start'; Observation = $before; Message = 'Codex Bridge 已经在运行' }
    }
    if (-not $before.HealthOk -and
        @($before.ListenerPids).Count -eq 0 -and
        @($before.AllBridgePids).Count -eq 0 -and
        $before.PidFilePresent -and
        -not $before.PidFileProcessExists) {
        Remove-StalePidFileIfSafe -Observation $before
        $before = Get-BridgeObservation
    }
    if (@($before.ListenerPids).Count -gt 0) {
        return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = '18080 已被其他进程占用，未执行启动。' }
    }
    if (@($before.AllBridgePids).Count -gt 0 -or $before.PidFilePresent) {
        return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = '检测到 bridge 进程或 PID 文件，但运行证据不完整，未重复启动。请先执行停止或查看日志。' }
    }
    if (-not (Test-Path -LiteralPath $script:Config.UvxPath -PathType Leaf)) {
        return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = "找不到 uvx：$($script:Config.UvxPath)" }
    }
    if (-not (Test-Path -LiteralPath $script:Config.AuthJsonPath -PathType Leaf)) {
        return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = "找不到 Codex OAuth 文件：$($script:Config.AuthJsonPath)" }
    }

    $startArguments = @(
        '--from', $script:Config.BridgePackageSpec,
        $script:Config.BridgeCommand,
        'start', '--verbose'
    )
    try {
        $startProcess = New-HiddenProcess -Arguments $startArguments
    } catch {
        return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = $_.Exception.Message }
    }

    $waitResult = Wait-ForStarted -StartProcess $startProcess
    if ($startProcess) { $startProcess.Dispose() }
    [pscustomobject]@{
        Success = $waitResult.Success
        Operation = 'Start'
        Observation = $waitResult.Observation
        Message = $waitResult.Message
        ExitCode = $waitResult.ExitCode
        FallbackUsed = $false
    }
}

function Invoke-StopOperation {
    $before = Get-BridgeObservation
    if (-not $before.HealthOk -and @($before.ListenerPids).Count -eq 0 -and @($before.AllBridgePids).Count -eq 0) {
        Remove-StalePidFileIfSafe -Observation $before
        return [pscustomobject]@{ Success = $true; Operation = 'Stop'; Observation = $before; Message = 'Codex Bridge 已停止'; FallbackUsed = $false }
    }

    $stopArguments = @(
        '--from', $script:Config.BridgePackageSpec,
        $script:Config.BridgeCommand,
        'stop'
    )
    $official = Invoke-UvxCommand -Arguments $stopArguments -WaitMilliseconds 15000
    Start-Sleep -Milliseconds $script:Config.StopGraceMilliseconds
    $afterOfficial = Get-BridgeObservation
    if (-not $afterOfficial.HealthOk -and
        @($afterOfficial.ListenerPids).Count -eq 0 -and
        @($afterOfficial.AllBridgePids).Count -eq 0) {
        Remove-StalePidFileIfSafe -Observation $afterOfficial
        return [pscustomobject]@{
            Success = $true
            Operation = 'Stop'
            Observation = Get-BridgeObservation
            Message = 'Codex Bridge 已停止'
            FallbackUsed = $false
            OfficialExitCode = $official.ExitCode
        }
    }

    # Keep the pre-stop verified topology as the safety anchor. The official
    # stop may have removed the supervisor while leaving its verified child.
    $fallback = Stop-VerifiedBridgeTree -InitialObservation $before
    if ($fallback.Success) {
        Remove-StalePidFileIfSafe -Observation $fallback.Observation
    }
    [pscustomobject]@{
        Success = $fallback.Success
        Operation = 'Stop'
        Observation = Get-BridgeObservation
        Message = $fallback.Message
        FallbackUsed = $true
        Refused = $fallback.Refused
        OfficialExitCode = $official.ExitCode
        KilledPids = $fallback.KilledPids
    }
}

function Invoke-RestartOperation {
    $stopResult = Invoke-StopOperation
    if (-not $stopResult.Success) {
        $stopResult.Operation = 'Restart'
        $stopResult.Message = "重启未执行启动阶段：$($stopResult.Message)"
        return $stopResult
    }
    $startResult = Invoke-StartOperation
    $startResult.Operation = 'Restart'
    $startResult.FallbackUsed = $stopResult.FallbackUsed
    if ($startResult.Success) {
        $startResult.Message = 'Codex Bridge 已重启'
    }
    return $startResult
}

function Invoke-WorkItem {
    param([Parameter(Mandatory)] $WorkItem)

    try {
        switch ($WorkItem.Kind) {
            'Probe' {
                $observation = Get-BridgeObservation
                return [pscustomobject]@{ Success = $true; Operation = 'Probe'; Observation = $observation; Message = $null }
            }
            'Start' { return Invoke-StartOperation }
            'Stop' { return Invoke-StopOperation }
            'Restart' { return Invoke-RestartOperation }
            default { throw "未知后台操作：$($WorkItem.Kind)" }
        }
    } catch {
        $observation = Get-BridgeObservation
        return [pscustomobject]@{
            Success = $false
            Operation = $WorkItem.Kind
            Observation = $observation
            Message = $_.Exception.Message
        }
    }
}

$createdNewMutex = $false
$mutex = [System.Threading.Mutex]::new($true, 'Local\CodexBridgeController', [ref]$createdNewMutex)
if (-not $createdNewMutex) {
    $mutex.Dispose()
    exit 0
}

$script:WorkerRunspace = $null
$script:ActiveInvocation = $null

try {
    # The backend functions are loaded into one dedicated runspace. The UI runspace
    # only queues work and polls invocation state; it never performs bridge I/O.
    $mainSource = Get-Content -LiteralPath $PSCommandPath -Raw
    $backendStartMarker = '# ---- Runtime configuration'
    $backendEndMarker = '$createdNewMutex = $false'
    $backendStart = $mainSource.IndexOf($backendStartMarker, [System.StringComparison]::Ordinal)
    $backendEnd = $mainSource.IndexOf($backendEndMarker, [System.StringComparison]::Ordinal)
    if ($backendStart -lt 0 -or $backendEnd -le $backendStart) {
        throw '无法定位后台函数区域。'
    }

    $workerBootstrap = "`$ErrorActionPreference = 'Stop'`r`n" + $mainSource.Substring($backendStart, $backendEnd - $backendStart)
    $script:WorkerRunspace = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspace()
    $script:WorkerRunspace.Open()

    $bootstrapPowerShell = [System.Management.Automation.PowerShell]::Create()
    $bootstrapPowerShell.Runspace = $script:WorkerRunspace
    [void]$bootstrapPowerShell.AddScript($workerBootstrap)
    [void]$bootstrapPowerShell.Invoke()
    if ($bootstrapPowerShell.HadErrors) {
        $bootstrapError = ($bootstrapPowerShell.Streams.Error | Select-Object -First 1)
        throw "后台 runspace 初始化失败：$bootstrapError"
    }
    $bootstrapPowerShell.Dispose()
} catch {
    if ($script:WorkerRunspace) {
        try { $script:WorkerRunspace.Close() } catch { }
        try { $script:WorkerRunspace.Dispose() } catch { }
        $script:WorkerRunspace = $null
    }
    try { $mutex.ReleaseMutex() } catch { }
    $mutex.Dispose()
    [void][System.Windows.Forms.MessageBox]::Show(
        "无法初始化后台 worker：$($_.Exception.Message)",
        'Codex Bridge',
        [System.Windows.Forms.MessageBoxButtons]::OK,
        [System.Windows.Forms.MessageBoxIcon]::Error
    )
    exit 1
}

$script:CurrentObservation = $null
$script:CurrentState = '检查中'
$script:OperationState = 'Idle'
$script:OperationInFlight = $false
$script:ExitAfterStop = $false
$script:Closing = $false
$script:ProbeQueued = $false
$script:OperationSequence = 0
$script:WorkQueue = [System.Collections.Queue]::new()
$script:ActiveWorkItem = $null
$script:BackendFailureNotified = $false

$form = [System.Windows.Forms.Form]::new()
$form.ShowInTaskbar = $false
$form.FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::None
$form.StartPosition = [System.Windows.Forms.FormStartPosition]::Manual
$form.Location = [System.Drawing.Point]::new(-32000, -32000)
$form.Size = [System.Drawing.Size]::new(1, 1)
$form.Opacity = 0

$notifyIcon = [System.Windows.Forms.NotifyIcon]::new()
$notifyIcon.Icon = [System.Drawing.SystemIcons]::Application
$notifyIcon.Visible = $true
$notifyIcon.Text = 'Codex Bridge — Checking'

$contextMenu = [System.Windows.Forms.ContextMenuStrip]::new()
$statusMenu = [System.Windows.Forms.ToolStripMenuItem]::new('状态：检查中')
$statusMenu.Enabled = $false
$startMenu = [System.Windows.Forms.ToolStripMenuItem]::new('启动')
$stopMenu = [System.Windows.Forms.ToolStripMenuItem]::new('停止')
$restartMenu = [System.Windows.Forms.ToolStripMenuItem]::new('重启')
$openLogMenu = [System.Windows.Forms.ToolStripMenuItem]::new('打开日志')
$openRunDirectoryMenu = [System.Windows.Forms.ToolStripMenuItem]::new('打开运行目录')
$exitMenu = [System.Windows.Forms.ToolStripMenuItem]::new('退出控制器')

$null = $contextMenu.Items.Add($statusMenu)
$null = $contextMenu.Items.Add([System.Windows.Forms.ToolStripSeparator]::new())
$null = $contextMenu.Items.Add($startMenu)
$null = $contextMenu.Items.Add($stopMenu)
$null = $contextMenu.Items.Add($restartMenu)
$null = $contextMenu.Items.Add([System.Windows.Forms.ToolStripSeparator]::new())
$null = $contextMenu.Items.Add($openLogMenu)
$null = $contextMenu.Items.Add($openRunDirectoryMenu)
$null = $contextMenu.Items.Add([System.Windows.Forms.ToolStripSeparator]::new())
$null = $contextMenu.Items.Add($exitMenu)
$notifyIcon.ContextMenuStrip = $contextMenu

function Update-MenuForState {
    param([string]$State)

    $statusMenu.Text = "状态：$State"
    $startMenu.Enabled = $State -eq '已停止'
    $stopMenu.Enabled = $State -eq '已运行' -or ($State -eq '异常' -and $script:CurrentObservation -and $script:CurrentObservation.CanForceStop)
    $restartMenu.Enabled = $State -eq '已运行'
    $exitMenu.Enabled = -not $script:OperationInFlight
    $notifyIcon.Text = (Get-StatusTooltip -State $State).Substring(0, [Math]::Min(63, (Get-StatusTooltip -State $State).Length))
}

function Show-Notice {
    param(
        [string]$Title,
        [string]$Message,
        [System.Windows.Forms.ToolTipIcon]$Icon = [System.Windows.Forms.ToolTipIcon]::Info
    )

    if ($notifyIcon.Visible) {
        $notifyIcon.ShowBalloonTip(3000, $Title, $Message, $Icon)
    }
}

function Show-OperationError {
    param([string]$Message)

    $logPath = if ($script:CurrentObservation -and $script:CurrentObservation.LogPath) {
        $script:CurrentObservation.LogPath
    } else {
        Join-Path $script:Config.RunDirectory 'server-127.0.0.1-18080.log'
    }
    $fullMessage = "$Message`n`n日志：$logPath"
    [void][System.Windows.Forms.MessageBox]::Show($form, $fullMessage, 'Codex Bridge', [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Error)
}

function Apply-Observation {
    param([Parameter(Mandatory)] $Observation)

    $previousState = $script:CurrentState
    $script:CurrentObservation = $Observation
    $script:CurrentState = Get-BridgeState -Observation $Observation -OperationState $script:OperationState
    Update-MenuForState -State $script:CurrentState

    if ($previousState -eq '已运行' -and $script:CurrentState -eq '异常') {
        Show-Notice -Title 'Codex Bridge' -Message 'Bridge 状态异常，请打开日志查看。' -Icon Warning
    }
}

function Remove-QueuedProbes {
    $newQueue = [System.Collections.Queue]::new()
    while ($script:WorkQueue.Count -gt 0) {
        $item = $script:WorkQueue.Dequeue()
        if ($item.Kind -ne 'Probe') { $null = $newQueue.Enqueue($item) }
    }
    $script:ProbeQueued = $false
    $script:WorkQueue = $newQueue
}

function Start-NextWorkItem {
    if ($script:ActiveInvocation -or $script:WorkQueue.Count -eq 0 -or $script:Closing) { return }
    $item = $script:WorkQueue.Dequeue()
    $script:ActiveWorkItem = $item
    try {
        $workerPowerShell = [System.Management.Automation.PowerShell]::Create()
        $workerPowerShell.Runspace = $script:WorkerRunspace
        [void]$workerPowerShell.AddCommand('Invoke-WorkItem').AddParameter('WorkItem', $item)
        $asyncResult = $workerPowerShell.BeginInvoke()
        $script:ActiveInvocation = [pscustomobject]@{
            PowerShell = $workerPowerShell
            AsyncResult = $asyncResult
            WorkItem = $item
        }
    } catch {
        if ($workerPowerShell) {
            try { $workerPowerShell.Dispose() } catch { }
        }
        $script:ActiveWorkItem = $null
        if ($item.Kind -eq 'Probe') { $script:ProbeQueued = $false }
        $script:BackendFailureNotified = $true
        $script:CurrentState = '异常'
        Update-MenuForState -State $script:CurrentState
        Show-Notice -Title 'Codex Bridge' -Message "后台任务无法启动：$($_.Exception.Message)" -Icon Error
    }
}

function Request-Probe {
    if ($script:Closing -or $script:OperationInFlight -or $script:ProbeQueued) { return }
    if ($script:ActiveInvocation -and $script:ActiveWorkItem -and $script:ActiveWorkItem.Kind -eq 'Probe') { return }
    $script:ProbeQueued = $true
    $null = $script:WorkQueue.Enqueue([pscustomobject]@{ Kind = 'Probe'; Sequence = 0 })
    Start-NextWorkItem
}

function Request-Operation {
    param([ValidateSet('Start','Stop','Restart')] [string]$Kind)

    if ($script:Closing -or $script:OperationInFlight) { return }
    Remove-QueuedProbes
    $script:OperationInFlight = $true
    $script:OperationSequence++
    $script:OperationState = switch ($Kind) {
        'Start' { 'Starting' }
        'Stop' { 'Stopping' }
        'Restart' { 'Restarting' }
    }
    Update-MenuForState -State $script:OperationState
    $null = $script:WorkQueue.Enqueue([pscustomobject]@{ Kind = $Kind; Sequence = $script:OperationSequence })
    Start-NextWorkItem
}

function Show-ExitChoiceDialog {
    $dialog = [System.Windows.Forms.Form]::new()
    $dialog.Text = '退出 Codex Bridge 控制器'
    $dialog.StartPosition = 'CenterParent'
    $dialog.FormBorderStyle = 'FixedDialog'
    $dialog.MinimizeBox = $false
    $dialog.MaximizeBox = $false
    $dialog.ShowInTaskbar = $false
    $dialog.ClientSize = [System.Drawing.Size]::new(430, 150)

    $label = [System.Windows.Forms.Label]::new()
    $label.Text = "Codex Bridge 仍在后台运行。`r`n请选择退出方式："
    $label.AutoSize = $true
    $label.Location = [System.Drawing.Point]::new(18, 18)
    $dialog.Controls.Add($label)

    $onlyExit = [System.Windows.Forms.Button]::new()
    $onlyExit.Text = '仅退出控制器'
    $onlyExit.Size = [System.Drawing.Size]::new(120, 32)
    $onlyExit.Location = [System.Drawing.Point]::new(18, 92)
    $onlyExit.DialogResult = [System.Windows.Forms.DialogResult]::Yes
    $dialog.Controls.Add($onlyExit)

    $stopAndExit = [System.Windows.Forms.Button]::new()
    $stopAndExit.Text = '停止 Bridge 并退出'
    $stopAndExit.Size = [System.Drawing.Size]::new(140, 32)
    $stopAndExit.Location = [System.Drawing.Point]::new(150, 92)
    $stopAndExit.DialogResult = [System.Windows.Forms.DialogResult]::No
    $dialog.Controls.Add($stopAndExit)

    $cancel = [System.Windows.Forms.Button]::new()
    $cancel.Text = '取消'
    $cancel.Size = [System.Drawing.Size]::new(80, 32)
    $cancel.Location = [System.Drawing.Point]::new(302, 92)
    $cancel.DialogResult = [System.Windows.Forms.DialogResult]::Cancel
    $dialog.Controls.Add($cancel)
    $dialog.AcceptButton = $onlyExit
    $dialog.CancelButton = $cancel

    $result = $dialog.ShowDialog($form)
    $dialog.Dispose()
    return $result
}

function Close-Controller {
    if ($script:Closing) { return }
    $script:Closing = $true
    $timer.Stop()
    $notifyIcon.Visible = $false
    $form.Close()
}

function Request-ControllerExit {
    if ($script:OperationInFlight) { return }
    $observation = $script:CurrentObservation
    $bridgeStillPresent = $observation -and (
        $observation.HealthOk -or
        @($observation.ListenerPids).Count -gt 0 -or
        @($observation.AllBridgePids).Count -gt 0
    )
    if (-not $bridgeStillPresent) {
        Close-Controller
        return
    }

    $choice = Show-ExitChoiceDialog
    switch ($choice) {
        ([System.Windows.Forms.DialogResult]::Yes) { Close-Controller }
        ([System.Windows.Forms.DialogResult]::No) {
            $script:ExitAfterStop = $true
            Request-Operation -Kind Stop
        }
    }
}

function Open-Log {
    $path = Find-LogPath
    if (-not $path) {
        [void][System.Windows.Forms.MessageBox]::Show($form, "日志不存在。`n`n运行目录：$($script:Config.RunDirectory)", 'Codex Bridge', [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Information)
        return
    }
    try {
        $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
        $startInfo.FileName = $path
        $startInfo.UseShellExecute = $true
        [void][System.Diagnostics.Process]::Start($startInfo)
    } catch {
        Show-OperationError -Message "无法打开日志：$($_.Exception.Message)"
    }
}

function Open-RunDirectory {
    if (-not (Test-Path -LiteralPath $script:Config.RunDirectory -PathType Container)) {
        [void][System.Windows.Forms.MessageBox]::Show($form, "运行目录不存在：`n$($script:Config.RunDirectory)", 'Codex Bridge', [System.Windows.Forms.MessageBoxButtons]::OK, [System.Windows.Forms.MessageBoxIcon]::Information)
        return
    }
    try {
        Start-Process -FilePath 'explorer.exe' -ArgumentList @($script:Config.RunDirectory) -WindowStyle Hidden
    } catch {
        Show-OperationError -Message "无法打开运行目录：$($_.Exception.Message)"
    }
}

function Apply-WorkResult {
    param(
        [Parameter(Mandatory)] $Item,
        $Result,
        [string]$InvocationError
    )

    if ($Item.Kind -eq 'Probe') { $script:ProbeQueued = $false }

    if (-not $Result) {
        $Result = [pscustomobject]@{
            Success = $false
            Operation = $Item.Kind
            Observation = $null
            Message = if ($InvocationError) { $InvocationError } else { '后台操作没有返回结果。' }
        }
    }

    if ($Result.Observation) { Apply-Observation -Observation $Result.Observation }

    if ($Item.Kind -eq 'Probe') {
        if ($Result.Success) {
            $script:BackendFailureNotified = $false
        } else {
            $script:CurrentState = '异常'
            Update-MenuForState -State $script:CurrentState
            if (-not $script:BackendFailureNotified) {
                $script:BackendFailureNotified = $true
                Show-Notice -Title 'Codex Bridge' -Message "后台状态探测异常：$($Result.Message)" -Icon Error
            }
        }
    } else {
        $script:OperationInFlight = $false
        $script:OperationState = 'Idle'
        if ($Result.Success) {
            if ($Item.Kind -eq 'Start') { Show-Notice -Title 'Codex Bridge' -Message "Codex Bridge 已启动`r`n$($script:Config.BridgeHost):$($script:Config.BridgePort)" }
            elseif ($Item.Kind -eq 'Stop') { Show-Notice -Title 'Codex Bridge' -Message 'Codex Bridge 已停止' }
            elseif ($Item.Kind -eq 'Restart') { Show-Notice -Title 'Codex Bridge' -Message "Codex Bridge 已重启`r`n$($script:Config.BridgeHost):$($script:Config.BridgePort)" }
            if ($script:ExitAfterStop -and ($Item.Kind -eq 'Stop' -or $Item.Kind -eq 'Restart')) {
                $script:ExitAfterStop = $false
                Close-Controller
                return
            }
        } else {
            $script:ExitAfterStop = $false
            Show-OperationError -Message $Result.Message
        }
    }

    if (-not $script:Closing) {
        Update-MenuForState -State $script:CurrentState
        Request-Probe
        Start-NextWorkItem
    }
}

function Complete-ActiveWorkItem {
    $active = $script:ActiveInvocation
    if (-not $active) { return }

    $state = $active.PowerShell.InvocationStateInfo.State
    if ($state -notin @(
            [System.Management.Automation.PSInvocationState]::Completed,
            [System.Management.Automation.PSInvocationState]::Failed,
            [System.Management.Automation.PSInvocationState]::Stopped
        )) {
        return
    }

    $item = $active.WorkItem
    $result = $null
    $invocationError = $null
    try {
        $output = @($active.PowerShell.EndInvoke($active.AsyncResult))
        if ($output.Count -gt 0) { $result = $output[$output.Count - 1] }
        if ($state -ne [System.Management.Automation.PSInvocationState]::Completed) {
            $streamError = $active.PowerShell.Streams.Error | Select-Object -First 1
            $invocationError = if ($streamError) { $streamError.ToString() } else { "后台任务状态：$state" }
            $result = $null
        }
    } catch {
        $invocationError = $_.Exception.Message
        $result = $null
    } finally {
        $script:ActiveInvocation = $null
        $script:ActiveWorkItem = $null
        try { $active.PowerShell.Dispose() } catch { }
    }

    Apply-WorkResult -Item $item -Result $result -InvocationError $invocationError
}

$timer = [System.Windows.Forms.Timer]::new()
$timer.Interval = $script:Config.RefreshIntervalMilliseconds
$timer.Add_Tick({
    Complete-ActiveWorkItem
    Request-Probe
    Start-NextWorkItem
})

$startMenu.Add_Click({ Request-Operation -Kind Start })
$stopMenu.Add_Click({ Request-Operation -Kind Stop })
$restartMenu.Add_Click({ Request-Operation -Kind Restart })
$openLogMenu.Add_Click({ Open-Log })
$openRunDirectoryMenu.Add_Click({ Open-RunDirectory })
$exitMenu.Add_Click({ Request-ControllerExit })
$notifyIcon.Add_MouseClick({
    param($sender, $eventArgs)
    if ($eventArgs.Button -eq [System.Windows.Forms.MouseButtons]::Left -and $script:CurrentState -ne '检查中') {
        Show-Notice -Title 'Codex Bridge' -Message "状态：$($script:CurrentState)"
    }
})

$form.Add_FormClosed({
    $notifyIcon.Visible = $false
    $notifyIcon.Dispose()
    $contextMenu.Dispose()
    $timer.Dispose()
    if ($script:ActiveInvocation) {
        try { $script:ActiveInvocation.PowerShell.Stop() } catch { }
        try { $script:ActiveInvocation.PowerShell.Dispose() } catch { }
        $script:ActiveInvocation = $null
        $script:ActiveWorkItem = $null
    }
    if ($script:WorkerRunspace) {
        try { $script:WorkerRunspace.Close() } catch { }
        try { $script:WorkerRunspace.Dispose() } catch { }
        $script:WorkerRunspace = $null
    }
    $mutex.ReleaseMutex()
    $mutex.Dispose()
})

Update-MenuForState -State $script:CurrentState
$form.Show()
$form.Hide()
$timer.Start()
Request-Probe
[System.Windows.Forms.Application]::Run($form)
