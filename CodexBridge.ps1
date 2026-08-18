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

$ControllerDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$ModelPath = Join-Path $ControllerDirectory 'lib\BridgeModel.ps1'
if (-not (Test-Path -LiteralPath $ModelPath -PathType Leaf)) {
    [Console]::Error.WriteLine("Bridge model not found: $ModelPath")
    exit 3
}
. $ModelPath

# ---- Runtime configuration: change BridgeVersion only when upgrading the verified bridge. ----
$UvxPath = 'E:\Dev\uv\uvx.exe'
$BridgeExecutablePath = 'E:\Codex\openai-api-server-via-codex-monitor\bin\openai-api-server-via-codex.exe'
$BridgePackage = 'openai-api-server-via-codex'
$BridgeVersion = '0.2.0'
$BridgeCommand = $BridgePackage
$BridgePackageSpec = "$BridgePackage==$BridgeVersion"
$BridgeIdentityMarker = $BridgePackage
$BridgeExecutableName = "$BridgePackage.exe"
$BridgeSupervisorCommandMarker = 'daemon-run'
$BridgeHost = '127.0.0.1'
[int]$BridgePort = 18080
$TelemetryEnabled = $true
$DashboardEnabled = $true
$ReasoningSummaryDefault = 'none'

if ([string]::IsNullOrWhiteSpace($env:USERPROFILE)) {
    throw 'USERPROFILE is not available; refusing to construct Codex runtime paths.'
}
$RunDirectory = Join-Path (Join-Path $env:USERPROFILE '.config') 'openai-api-server-via-codex\run'
$PidFileName = "server-$BridgeHost-$BridgePort.pid"
$LogFileName = "server-$BridgeHost-$BridgePort.log"
$HealthUri = "http://$BridgeHost`:$BridgePort/healthz"
$DashboardUri = "http://$BridgeHost`:$BridgePort/dashboard"
$AuthJsonPath = Join-Path (Join-Path $env:USERPROFILE '.codex') 'auth.json'
$DesktopPath = [Environment]::GetFolderPath('Desktop')

[int]$HealthTimeoutMilliseconds = 1500
[int]$LightProbeIntervalMilliseconds = 4000
[int]$DeepProbeIntervalMilliseconds = 45000
[int]$UiTimerIntervalMilliseconds = 250
[int]$StartTimeoutSeconds = 60
[int]$StopGraceMilliseconds = 2000
[int]$StopTimeoutSeconds = 12
[int]$FallbackPassLimit = 8
[int]$MaxDeepProcessRecords = 512
[int]$CliOutputLimit = 8000

$script:Config = [pscustomobject]@{
    ControllerDirectory = $ControllerDirectory
    ModelPath = $ModelPath
    UvxPath = $UvxPath
    BridgeExecutablePath = $BridgeExecutablePath
    BridgePackage = $BridgePackage
    BridgeVersion = $BridgeVersion
    BridgeCommand = $BridgeCommand
    BridgePackageSpec = $BridgePackageSpec
    BridgeIdentityMarker = $BridgeIdentityMarker
    BridgeExecutableName = $BridgeExecutableName
    BridgeSupervisorCommandMarker = $BridgeSupervisorCommandMarker
    BridgeHost = $BridgeHost
    BridgePort = $BridgePort
    TelemetryEnabled = $TelemetryEnabled
    DashboardEnabled = $DashboardEnabled
    ReasoningSummaryDefault = $ReasoningSummaryDefault
    RunDirectory = $RunDirectory
    PidFilePath = (Join-Path $RunDirectory $PidFileName)
    LogFileName = $LogFileName
    LogFilePath = (Join-Path $RunDirectory $LogFileName)
    HealthUri = $HealthUri
    DashboardUri = $DashboardUri
    AuthJsonPath = $AuthJsonPath
    HealthTimeoutMilliseconds = $HealthTimeoutMilliseconds
    LightProbeIntervalMilliseconds = $LightProbeIntervalMilliseconds
    DeepProbeIntervalMilliseconds = $DeepProbeIntervalMilliseconds
    UiTimerIntervalMilliseconds = $UiTimerIntervalMilliseconds
    StartTimeoutSeconds = $StartTimeoutSeconds
    StopGraceMilliseconds = $StopGraceMilliseconds
    StopTimeoutSeconds = $StopTimeoutSeconds
    FallbackPassLimit = $FallbackPassLimit
    MaxDeepProcessRecords = $MaxDeepProcessRecords
    CliOutputLimit = $CliOutputLimit
}

$script:HealthClient = $null
$script:LastDeepObservation = $null

function Initialize-WorkerResources {
    if ($script:HealthClient) { return }
    $script:HealthClient = [System.Net.Http.HttpClient]::new()
    $script:HealthClient.Timeout = [TimeSpan]::FromMilliseconds($script:Config.HealthTimeoutMilliseconds)
}

function Dispose-WorkerResources {
    if ($script:HealthClient) {
        try { $script:HealthClient.Dispose() } catch { }
        $script:HealthClient = $null
    }
}

function Get-HealthProbe {
    $cancellation = $null
    $response = $null
    try {
        Initialize-WorkerResources
        $cancellation = [System.Threading.CancellationTokenSource]::new($script:Config.HealthTimeoutMilliseconds)
        $response = $script:HealthClient.GetAsync($script:Config.HealthUri, $cancellation.Token).GetAwaiter().GetResult()
        $statusCode = [int]$response.StatusCode
        return [pscustomobject]@{
            Known = $true
            Ok = $statusCode -ge 200 -and $statusCode -lt 300
            StatusCode = $statusCode
            Error = $null
        }
    } catch {
        return [pscustomobject]@{
            Known = $true
            Ok = $false
            StatusCode = $null
            Error = $_.Exception.Message
        }
    } finally {
        if ($response) { try { $response.Dispose() } catch { } }
        if ($cancellation) { try { $cancellation.Dispose() } catch { } }
    }
}

function Get-PidFileInfo {
    $path = $script:Config.PidFilePath
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        return [pscustomobject]@{ Present = $false; ReadSucceeded = $true; Pid = $null; Error = $null; Path = $path }
    }
    try {
        $raw = (Get-Content -LiteralPath $path -Raw).Trim()
        $filePid = 0
        if ([int]::TryParse($raw, [ref]$filePid) -and $filePid -gt 0) {
            return [pscustomobject]@{ Present = $true; ReadSucceeded = $true; Pid = $filePid; Error = $null; Path = $path }
        }
        return [pscustomobject]@{ Present = $true; ReadSucceeded = $true; Pid = $null; Error = 'PID file does not contain a positive integer.'; Path = $path }
    } catch {
        return [pscustomobject]@{ Present = $true; ReadSucceeded = $false; Pid = $null; Error = $_.Exception.Message; Path = $path }
    }
}

function Get-ProcessRecordByPidFromOs {
    param([int]$ProcessId)
    if ($ProcessId -le 0) { return [pscustomobject]@{ Succeeded = $true; Record = $null; Error = $null } }
    try {
        $records = @(
            Get-CimInstance Win32_Process -Filter ("ProcessId = {0}" -f $ProcessId) -ErrorAction Stop |
                Select-Object ProcessId,ParentProcessId,CreationDate,Name,ExecutablePath,CommandLine |
                Select-Object -First 1
        )
        return [pscustomobject]@{ Succeeded = $true; Record = if ($records.Count -gt 0) { $records[0] } else { $null }; Error = $null }
    } catch {
        return [pscustomobject]@{ Succeeded = $false; Record = $null; Error = $_.Exception.Message }
    }
}

function Get-ChildProcessRecordsFromOs {
    param([int]$ParentProcessId)
    try {
        $records = @(
            Get-CimInstance Win32_Process -Filter ("ParentProcessId = {0}" -f $ParentProcessId) -ErrorAction Stop |
                Select-Object ProcessId,ParentProcessId,CreationDate,Name,ExecutablePath,CommandLine
        )
        return [pscustomobject]@{ Succeeded = $true; Records = $records; Error = $null }
    } catch {
        return [pscustomobject]@{ Succeeded = $false; Records = @(); Error = $_.Exception.Message }
    }
}

function Get-ListenerProbe {
    try {
        # Query the CIM class directly so an empty result is represented as an
        # empty successful observation. Do not classify a localized exception
        # message as the stopped state.
        $records = @(
            Get-CimInstance -Namespace 'Root/StandardCimv2' -ClassName 'MSFT_NetTCPConnection' -Filter ("LocalPort = {0} AND State = 2" -f $script:Config.BridgePort) -ErrorAction Stop |
                Select-Object LocalAddress,LocalPort,OwningProcess,State
        )
        return [pscustomobject]@{ Succeeded = $true; Records = $records; Error = $null }
    } catch {
        return [pscustomobject]@{ Succeeded = $false; Records = @(); Error = $_.Exception.Message }
    }
}

function Get-DeepProcessRecords {
    param(
        [Parameter(Mandatory)] $PidInfo,
        [object[]] $ListenerRecords = @()
    )
    $recordsByPid = @{}
    $processObservationComplete = $true
    $errors = [System.Collections.Generic.List[string]]::new()
    $candidatePids = [System.Collections.Generic.HashSet[int]]::new()
    if ($PidInfo.Pid) { [void]$candidatePids.Add([int]$PidInfo.Pid) }
    foreach ($listenerRecord in @($ListenerRecords)) {
        $listenerPid = [int](Get-BridgeRecordValue -Record $listenerRecord -Name 'OwningProcess')
        if ($listenerPid -gt 0) { [void]$candidatePids.Add($listenerPid) }
    }
    foreach ($candidatePid in $candidatePids) {
        $probe = Get-ProcessRecordByPidFromOs -ProcessId $candidatePid
        if (-not $probe.Succeeded) {
            $processObservationComplete = $false
            $errors.Add("PID $candidatePid 查询失败：$($probe.Error)")
            continue
        }
        if ($probe.Record) { $recordsByPid[$candidatePid] = $probe.Record }
    }
    $rootRecord = if ($PidInfo.Pid -and $recordsByPid.ContainsKey([int]$PidInfo.Pid)) { $recordsByPid[[int]$PidInfo.Pid] } else { $null }
    $pendingParents = [System.Collections.Generic.Queue[int]]::new()
    if ($rootRecord -and (Test-BridgeSupervisorIdentity -Record $rootRecord -Config $script:Config)) { $pendingParents.Enqueue([int]$PidInfo.Pid) }
    while ($pendingParents.Count -gt 0) {
        $parentProcessId = $pendingParents.Dequeue()
        $childrenProbe = Get-ChildProcessRecordsFromOs -ParentProcessId $parentProcessId
        if (-not $childrenProbe.Succeeded) {
            $processObservationComplete = $false
            $errors.Add("Parent PID $parentProcessId 的子进程查询失败：$($childrenProbe.Error)")
            continue
        }
        foreach ($childRecord in @($childrenProbe.Records)) {
            $childProcessId = [int](Get-BridgeRecordValue -Record $childRecord -Name 'ProcessId')
            if ($childProcessId -le 0 -or $recordsByPid.ContainsKey($childProcessId)) { continue }
            if ($recordsByPid.Count -ge $script:Config.MaxDeepProcessRecords) {
                $processObservationComplete = $false
                $errors.Add("ownership tree 超过 $($script:Config.MaxDeepProcessRecords) 个进程，停止扩展。")
                break
            }
            $recordsByPid[$childProcessId] = $childRecord
            $pendingParents.Enqueue($childProcessId)
        }
    }
    [pscustomobject]@{ Records = @($recordsByPid.Values); Succeeded = $processObservationComplete; Errors = @($errors) }
}

function Find-LogPath {
    return $script:Config.LogFilePath
}

function New-BridgeObservation {
    param(
        [Parameter(Mandatory)] $PidInfo,
        [Parameter(Mandatory)] $HealthProbe,
        [Parameter(Mandatory)] $Topology,
        [Parameter(Mandatory)] [bool] $ObservationComplete,
        [Parameter(Mandatory)] [string] $ProbeKind,
        [string] $LogPath
    )
    $observedState = Get-ObservedBridgeState -Topology $Topology -HealthKnown $HealthProbe.Known -HealthOk $HealthProbe.Ok -ObservationComplete $ObservationComplete
    [pscustomobject]@{
        Timestamp = [DateTimeOffset]::Now
        ProbeKind = $ProbeKind
        ObservedState = $observedState
        HealthKnown = [bool]$HealthProbe.Known
        HealthOk = [bool]$HealthProbe.Ok
        HealthError = $HealthProbe.Error
        PidFilePresent = [bool]$PidInfo.Present
        PidFileReadSucceeded = [bool]$PidInfo.ReadSucceeded
        PidFilePid = $Topology.PidFilePid
        PidFileState = $Topology.PidFileState
        PidFileProcessExists = $null -ne $Topology.PidFileRecord
        ListenerPids = @($Topology.ListenerPids)
        OwnedListenerPids = @($Topology.OwnedListenerPids)
        ForeignListenerPids = @($Topology.ForeignListenerPids)
        UnverifiedBridgeListenerPids = @($Topology.UnverifiedBridgeListenerPids)
        UnknownForeignListenerPids = @($Topology.UnknownForeignListenerPids)
        AllBridgePids = @($Topology.OwnedProcessPids)
        BridgeTreePids = @($Topology.OwnedProcessPids)
        OwnedProcessRecords = @($Topology.OwnedProcessRecords)
        OwnedListenerRecords = @($Topology.OwnedListenerRecords)
        OwnedListenerOwnerRecords = @($Topology.OwnedListenerOwnerRecords)
        OwnedListenerOwnerFingerprints = @($Topology.OwnedListenerOwnerFingerprints)
        TargetInstanceAffinity = [string]$Topology.TargetInstanceAffinity
        TargetAffinityReason = [string]$Topology.TargetAffinityReason
        CanAttemptOfficialStop = [bool]$Topology.CanAttemptOfficialStop
        CanForceStop = [bool]$Topology.CanForceStop
        Topology = $Topology
        OwnershipSnapshot = New-OwnershipSnapshot -Topology $Topology
        ObservationComplete = $ObservationComplete
        ProcessObservationComplete = [bool]$Topology.ProcessObservationComplete
        ListenerObservationComplete = [bool]$Topology.ListenerObservationComplete
        LogPath = if ($LogPath) { $LogPath } else { Find-LogPath }
        RunDirectoryExists = Test-Path -LiteralPath $script:Config.RunDirectory -PathType Container
    }
}

function Get-DeepObservation {
    $pidInfo = Get-PidFileInfo
    $listenerProbe = Get-ListenerProbe
    $healthProbe = Get-HealthProbe
    $processProbe = Get-DeepProcessRecords -PidInfo $pidInfo -ListenerRecords $listenerProbe.Records
    $historicalAffinity = if ($script:LastDeepObservation) { $script:LastDeepObservation.Topology } else { $null }
    $topology = Resolve-BridgeOwnership -ProcessRecords $processProbe.Records -PidFilePresent $pidInfo.Present -PidFilePid $pidInfo.Pid -ListenerRecords $listenerProbe.Records -Config $script:Config -ProcessObservationComplete $processProbe.Succeeded -ListenerObservationComplete $listenerProbe.Succeeded -HistoricalAffinity $historicalAffinity
    $observationComplete = $pidInfo.ReadSucceeded -and $processProbe.Succeeded -and $listenerProbe.Succeeded
    $observation = New-BridgeObservation -PidInfo $pidInfo -HealthProbe $healthProbe -Topology $topology -ObservationComplete $observationComplete -ProbeKind 'Deep'
    $script:LastDeepObservation = $observation
    return $observation
}

function Test-LightObservationStable {
    param(
        [Parameter(Mandatory)] $Cache,
        [Parameter(Mandatory)] $PidInfo,
        $PidRecord,
        [object[]] $ListenerRecords = @(),
        [object[]] $LightProcessRecords = @(),
        [Parameter(Mandatory)] $HealthProbe
    )
    if ($Cache.ObservedState -notin @('Running','Stopped')) { return $false }
    if (-not $HealthProbe.Known -or [bool]$HealthProbe.Ok -ne [bool]$Cache.HealthOk) { return $false }
    if ([bool]$PidInfo.Present -ne [bool]$Cache.PidFilePresent) { return $false }
    if ([int]($PidInfo.Pid ?? 0) -ne [int]($Cache.PidFilePid ?? 0)) { return $false }
    $currentListenerPids = @($ListenerRecords | ForEach-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'OwningProcess') } | Where-Object { $_ -gt 0 } | Sort-Object -Unique)
    $cachedListenerPids = @($Cache.Topology.ListenerPids | Sort-Object -Unique)
    if (($currentListenerPids -join ',') -ne ($cachedListenerPids -join ',')) { return $false }
    if ($Cache.ObservedState -eq 'Stopped') { return -not $PidInfo.Present -and $currentListenerPids.Count -eq 0 -and -not $HealthProbe.Ok }
    if ([string]$Cache.Topology.TargetInstanceAffinity -ne 'Bound') { return $false }
    if (-not $PidRecord -or -not (Test-ProcessFingerprintMatch -Expected $Cache.Topology.OwnershipRootFingerprint -Actual $PidRecord)) { return $false }
    $expectedOwnerFingerprints = @($Cache.Topology.OwnedListenerOwnerFingerprints)
    if ($expectedOwnerFingerprints.Count -ne @($Cache.Topology.OwnedListenerPids).Count) { return $false }
    foreach ($expectedOwnerFingerprint in $expectedOwnerFingerprints) {
        $listenerProcessId = [int](Get-BridgeRecordValue -Record $expectedOwnerFingerprint -Name 'ProcessId')
        $actualOwnerRecord = Get-BridgeRecordByPid -Records $LightProcessRecords -ProcessId $listenerProcessId
        if (-not $actualOwnerRecord -or -not (Test-ProcessFingerprintMatch -Expected $expectedOwnerFingerprint -Actual $actualOwnerRecord)) { return $false }
    }
    return $true
}

function Get-LightObservation {
    $cache = $script:LastDeepObservation
    if (-not $cache) { return Get-DeepObservation }
    if (([DateTimeOffset]::Now - [DateTimeOffset]$cache.Timestamp).TotalMilliseconds -ge $script:Config.DeepProbeIntervalMilliseconds) {
        return Get-DeepObservation
    }
    $pidInfo = Get-PidFileInfo
    $listenerProbe = Get-ListenerProbe
    $healthProbe = Get-HealthProbe
    if (-not $pidInfo.ReadSucceeded -or -not $listenerProbe.Succeeded) { return Get-DeepObservation }
    $lightRecords = [System.Collections.Generic.List[object]]::new()
    $pidRecord = $null
    if ($pidInfo.Pid) {
        $pidProbe = Get-ProcessRecordByPidFromOs -ProcessId ([int]$pidInfo.Pid)
        if (-not $pidProbe.Succeeded) { return Get-DeepObservation }
        $pidRecord = $pidProbe.Record
        if ($pidRecord) { $lightRecords.Add($pidRecord) }
    }
    foreach ($listenerRecord in @($listenerProbe.Records)) {
        $listenerProcessId = [int](Get-BridgeRecordValue -Record $listenerRecord -Name 'OwningProcess')
        if ($listenerProcessId -le 0 -or ($lightRecords | Where-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'ProcessId') -eq $listenerProcessId })) { continue }
        $listenerOwnerProbe = Get-ProcessRecordByPidFromOs -ProcessId $listenerProcessId
        if (-not $listenerOwnerProbe.Succeeded) { return Get-DeepObservation }
        if ($listenerOwnerProbe.Record) { $lightRecords.Add($listenerOwnerProbe.Record) }
    }
    if (Test-LightObservationStable -Cache $cache -PidInfo $pidInfo -PidRecord $pidRecord -ListenerRecords $listenerProbe.Records -LightProcessRecords @($lightRecords) -HealthProbe $healthProbe) {
        return New-BridgeObservation -PidInfo $pidInfo -HealthProbe $healthProbe -Topology $cache.Topology -ObservationComplete $true -ProbeKind 'Light' -LogPath $cache.LogPath
    }
    return Get-DeepObservation
}

function Get-BridgeObservation {
    param([switch]$Deep)
    if ($Deep) { return Get-DeepObservation }
    return Get-LightObservation
}

function Test-RunningEvidence {
    param([Parameter(Mandatory)] $Observation)
    return $Observation.ObservedState -eq 'Running'
}

function Test-StoppedEvidence {
    param([Parameter(Mandatory)] $Observation)
    return $Observation.ObservedState -eq 'Stopped' -and -not $Observation.HealthOk -and @($Observation.ListenerPids).Count -eq 0 -and @($Observation.AllBridgePids).Count -eq 0
}

function Remove-StalePidFileIfSafe {
    param([Parameter(Mandatory)] $Observation)
    $safeState = $Observation.PidFileState -in @('StaleMissingProcess','StaleReusedPid')
    $safe = $safeState -and $Observation.HealthKnown -and -not $Observation.HealthOk -and @($Observation.ListenerPids).Count -eq 0 -and @($Observation.AllBridgePids).Count -eq 0 -and @($Observation.UnverifiedBridgeListenerPids).Count -eq 0 -and $Observation.ObservationComplete -and (Test-Path -LiteralPath $script:Config.PidFilePath -PathType Leaf)
    if (-not $safe) { return $false }
    try { Remove-Item -LiteralPath $script:Config.PidFilePath -Force -ErrorAction Stop; return $true } catch { return $false }
}

function Limit-DiagnosticText {
    param([string]$Text)
    if ($null -eq $Text) { return '' }
    if ($Text.Length -le $script:Config.CliOutputLimit) { return $Text }
    return $Text.Substring(0, $script:Config.CliOutputLimit) + "`r`n...[truncated]"
}

function Get-BridgeRunnerPath {
    if (-not [string]::IsNullOrWhiteSpace([string]$script:Config.BridgeExecutablePath)) {
        return [string]$script:Config.BridgeExecutablePath
    }
    return [string]$script:Config.UvxPath
}

function New-HiddenProcess {
    param([Parameter(Mandatory)] [string[]] $Arguments)
    $runnerPath = Get-BridgeRunnerPath
    if (-not (Test-Path -LiteralPath $runnerPath -PathType Leaf)) { throw "找不到 Bridge runner：$runnerPath" }
    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $runnerPath
    $startInfo.WorkingDirectory = $script:Config.ControllerDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in $Arguments) { [void]$startInfo.ArgumentList.Add($argument) }
    $process = [System.Diagnostics.Process]::Start($startInfo)
    [pscustomobject]@{ Process = $process; StdOutTask = $process.StandardOutput.ReadToEndAsync(); StdErrTask = $process.StandardError.ReadToEndAsync(); StartedAt = [DateTimeOffset]::Now }
}

function Stop-ControllerOwnedInvocation {
    param([Parameter(Mandatory)] $Invocation)
    $process = $Invocation.Process
    try {
        if ($process.HasExited) { return $true }
        try { $process.Kill($true) } catch { }
        try { $process.WaitForExit(3000) } catch { }
        return $process.HasExited
    } catch { return $false }
}

function Stop-ControllerOwnedWrapperOnly {
    param([Parameter(Mandatory)] $Invocation)
    $process = $Invocation.Process
    try {
        if ($process.HasExited) { return $true }
        # This is intentionally a direct-process kill. It is only used after
        # the bridge has reached Running, so the uvx wrapper must not take its
        # newly-created bridge descendants with it.
        $process.Kill()
        try { $process.WaitForExit(3000) } catch { }
        return $process.HasExited
    } catch { return $false }
}

function Get-InvocationOutput {
    param([Parameter(Mandatory)] $Invocation)
    $stdout = ''; $stderr = ''
    try { $stdout = Limit-DiagnosticText -Text $Invocation.StdOutTask.GetAwaiter().GetResult() } catch { }
    try { $stderr = Limit-DiagnosticText -Text $Invocation.StdErrTask.GetAwaiter().GetResult() } catch { }
    [pscustomobject]@{ StdOut = $stdout; StdErr = $stderr }
}

function Dispose-Invocation {
    param($Invocation)
    if ($Invocation -and $Invocation.Process) {
        try { $Invocation.Process.Dispose() } catch { }
    }
}

function Invoke-UvxCommand {
    param([Parameter(Mandatory)] [string[]] $Arguments, [int] $WaitMilliseconds = 15000)
    $invocation = $null
    try {
        $invocation = New-HiddenProcess -Arguments $Arguments
        $exited = $invocation.Process.WaitForExit($WaitMilliseconds)
        $timedOut = -not $exited
        $terminated = $false
        if (-not $exited) { $terminated = Stop-ControllerOwnedInvocation -Invocation $invocation; $exited = $terminated }
        if (-not $exited) {
            return [pscustomobject]@{ Started = $true; Exited = $false; TimedOut = $true; ExitCode = $null; Terminated = $false; StdOut = ''; StdErr = ''; Error = 'controller-owned CLI invocation timed out and could not be confirmed exited.' }
        }
        $output = Get-InvocationOutput -Invocation $invocation
        [pscustomobject]@{ Started = $true; Exited = $true; TimedOut = $timedOut; ExitCode = $invocation.Process.ExitCode; Terminated = $terminated; StdOut = $output.StdOut; StdErr = $output.StdErr; Error = $null }
    } catch {
        [pscustomobject]@{ Started = $false; Exited = $true; TimedOut = $false; ExitCode = $null; Terminated = $false; StdOut = ''; StdErr = ''; Error = $_.Exception.Message }
    } finally { Dispose-Invocation -Invocation $invocation }
}

function Get-InvocationSummary {
    param([Parameter(Mandatory)] $Invocation)
    $exited = $false
    try { $exited = $Invocation.Process.HasExited } catch { }
    if (-not $exited) { return [pscustomobject]@{ Exited = $false; ExitCode = $null; StdOut = ''; StdErr = '' } }
    $output = Get-InvocationOutput -Invocation $Invocation
    [pscustomobject]@{ Exited = $true; ExitCode = $Invocation.Process.ExitCode; StdOut = $output.StdOut; StdErr = $output.StdErr }
}

function Wait-ForStarted {
    param([Parameter(Mandatory)] $Invocation)
    $deadline = [DateTime]::UtcNow.AddSeconds($script:Config.StartTimeoutSeconds)
    $lastObservation = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        $lastObservation = Get-BridgeObservation -Deep
        if (Test-RunningEvidence -Observation $lastObservation) {
            $summary = Get-InvocationSummary -Invocation $Invocation
            return [pscustomobject]@{ Success = $true; Observation = $lastObservation; Message = 'Codex Bridge 已启动'; ExitCode = $summary.ExitCode; InvocationExited = $summary.Exited; StdOut = $summary.StdOut; StdErr = $summary.StdErr }
        }
        try {
            if ($Invocation.Process.HasExited) {
                $summary = Get-InvocationSummary -Invocation $Invocation
                if ($summary.ExitCode -ne 0) { return [pscustomobject]@{ Success = $false; Observation = $lastObservation; Message = "启动命令失败（exit $($summary.ExitCode)）。$($summary.StdErr)"; ExitCode = $summary.ExitCode; InvocationExited = $true; StdOut = $summary.StdOut; StdErr = $summary.StdErr } }
            }
        } catch { }
        Start-Sleep -Milliseconds 500
    }
    if (-not $lastObservation) { $lastObservation = Get-BridgeObservation -Deep }
    $terminated = Stop-ControllerOwnedInvocation -Invocation $Invocation
    $summary = if ($terminated) { Get-InvocationSummary -Invocation $Invocation } else { [pscustomobject]@{ Exited = $false; ExitCode = $null; StdOut = ''; StdErr = '' } }
    [pscustomobject]@{ Success = $false; Observation = $lastObservation; Message = if ($terminated) { "Codex Bridge 启动超时。请打开日志：$($lastObservation.LogPath ?? $script:Config.LogFilePath)" } else { '启动命令超时且未能确认回收，未继续执行其他进程操作。' }; ExitCode = $summary.ExitCode; InvocationExited = $summary.Exited; StdOut = $summary.StdOut; StdErr = $summary.StdErr }
}

function Cleanup-StartedInvocation {
    param([Parameter(Mandatory)] $Invocation)
    $process = $Invocation.Process
    try {
        if ($process.HasExited) { return $true }
        $deadline = [DateTime]::UtcNow.AddSeconds(1)
        while ([DateTime]::UtcNow -lt $deadline) {
            if ($process.WaitForExit(100)) { return $true }
        }
        # The bridge is already verified Running. Kill only the direct uvx
        # wrapper if it did not exit naturally; never recursively kill here.
        return Stop-ControllerOwnedWrapperOnly -Invocation $Invocation
    } catch { return $false }
}

function Wait-ForStopped {
    param([int]$TimeoutSeconds = $script:Config.StopTimeoutSeconds)
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastObservation = $null
    while ([DateTime]::UtcNow -lt $deadline) {
        $lastObservation = Get-BridgeObservation -Deep
        if ((Test-StoppedEvidence -Observation $lastObservation) -and @($lastObservation.ForeignListenerPids).Count -eq 0) { return [pscustomobject]@{ Success = $true; Observation = $lastObservation } }
        Start-Sleep -Milliseconds 500
    }
    if (-not $lastObservation) { $lastObservation = Get-BridgeObservation -Deep }
    [pscustomobject]@{ Success = $false; Observation = $lastObservation }
}

function Get-FilteredBridgeIdentityRecordsFromOs {
    try {
        $escapedMarker = [string]$script:Config.BridgeIdentityMarker -replace "'", "''"
        $escapedName = [string]$script:Config.BridgeExecutableName -replace "'", "''"
        $filter = "Name = '$escapedName' OR CommandLine LIKE '%$escapedMarker%'"
        return [pscustomobject]@{ Succeeded = $true; Records = @(Get-CimInstance Win32_Process -Filter $filter -ErrorAction Stop | Select-Object ProcessId,ParentProcessId,CreationDate,Name,ExecutablePath,CommandLine); Error = $null }
    } catch { return [pscustomobject]@{ Succeeded = $false; Records = @(); Error = $_.Exception.Message } }
}

function Stop-VerifiedBridgeTree {
    param([Parameter(Mandatory)] $OwnershipSnapshot)
    if (-not $OwnershipSnapshot -or -not $OwnershipSnapshot.CanForceStopAtCapture -or -not $OwnershipSnapshot.RootFingerprint -or @($OwnershipSnapshot.ForeignListenerPids).Count -gt 0) {
        return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @(); Message = '停止前 ownership snapshot 不满足安全强制终止条件，未执行强制终止。' }
    }
    $killedPids = [System.Collections.Generic.List[int]]::new()
    $verifiedFingerprintsByPid = @{}
    foreach ($fingerprint in @($OwnershipSnapshot.ProcessFingerprints)) { $verifiedFingerprintsByPid[[int]$fingerprint.ProcessId] = $fingerprint }
    $rootProcessId = [int]$OwnershipSnapshot.RootFingerprint.ProcessId

    for ($pass = 0; $pass -lt $script:Config.FallbackPassLimit; $pass++) {
        $listenerProbe = Get-ListenerProbe
        if (-not $listenerProbe.Succeeded) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = "无法确认 18080 listener，未执行强制终止：$($listenerProbe.Error)" } }
        $rootProbe = Get-ProcessRecordByPidFromOs -ProcessId $rootProcessId
        if (-not $rootProbe.Succeeded) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = "无法重新验证 supervisor PID $rootProcessId，未执行强制终止：$($rootProbe.Error)" } }
        $rootLive = $rootProbe.Record
        $targetRecords = [System.Collections.Generic.List[object]]::new()
        $rootStillVerified = $false

        if ($rootLive) {
            $rootStillVerified = $true
            $liveTree = Get-DeepProcessRecords -PidInfo ([pscustomobject]@{ Pid = $rootProcessId }) -ListenerRecords @()
            if (-not $liveTree.Succeeded) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = '无法完整读取 verified supervisor 的当前 descendants，未执行强制终止。' } }
            $plan = Get-FallbackActionPlan -OwnershipSnapshot $OwnershipSnapshot -LiveRecords $liveTree.Records -SupervisorLive $true -ListenerRecords $listenerProbe.Records -Config $script:Config
            if (-not $plan.Allowed) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = "fallback action plan 拒绝：$($plan.Reason)" } }
            foreach ($fingerprint in @($plan.VerifiedFingerprints)) {
                $verifiedFingerprintsByPid[[int]$fingerprint.ProcessId] = $fingerprint
            }
            foreach ($record in @($plan.TargetRecords)) { $targetRecords.Add($record) }
        } else {
            # Once the supervisor is gone, only fingerprints captured before or
            # while that verified supervisor was live can prove child ownership.
            $liveChildRecords = [System.Collections.Generic.List[object]]::new()
            foreach ($fingerprint in @($verifiedFingerprintsByPid.Values | Where-Object { [int]$_.ProcessId -ne $rootProcessId })) {
                $liveProbe = Get-ProcessRecordByPidFromOs -ProcessId ([int]$fingerprint.ProcessId)
                if (-not $liveProbe.Succeeded) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = "无法验证 snapshot child PID $($fingerprint.ProcessId)，未执行强制终止。" } }
                if (-not $liveProbe.Record) { continue }
                $liveChildRecords.Add($liveProbe.Record)
            }
            $plan = Get-FallbackActionPlan -OwnershipSnapshot $OwnershipSnapshot -LiveRecords @($liveChildRecords) -SupervisorLive $false -ListenerRecords $listenerProbe.Records -Config $script:Config
            if (-not $plan.Allowed) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = "fallback action plan 拒绝：$($plan.Reason)" } }
            foreach ($fingerprint in @($plan.VerifiedFingerprints)) {
                $verifiedFingerprintsByPid[[int]$fingerprint.ProcessId] = $fingerprint
            }
            foreach ($record in @($plan.TargetRecords)) { $targetRecords.Add($record) }
        }

        if ($rootStillVerified) {
            $recheck = Get-ProcessRecordByPidFromOs -ProcessId $rootProcessId
            if (-not $recheck.Succeeded) { continue }
            if ($recheck.Record -and (Test-ProcessFingerprintMatch -Expected $OwnershipSnapshot.RootFingerprint -Actual $recheck.Record)) {
                try { Stop-Process -Id $rootProcessId -Force -ErrorAction Stop; $killedPids.Add($rootProcessId) } catch { }
            }
        } else {
            foreach ($targetRecord in @($targetRecords)) {
                $targetProcessId = [int](Get-BridgeRecordValue -Record $targetRecord -Name 'ProcessId')
                $expected = $verifiedFingerprintsByPid[$targetProcessId]
                $recheck = Get-ProcessRecordByPidFromOs -ProcessId $targetProcessId
                if (-not $recheck.Succeeded) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = "无法在终止前重新验证 PID $targetProcessId，未执行强制终止。" } }
                if (-not $recheck.Record) { continue }
                if (-not (Test-ProcessFingerprintMatch -Expected $expected -Actual $recheck.Record) -or -not (Test-BridgeIdentity -Record $recheck.Record -Config $script:Config)) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = "PID $targetProcessId 的 fingerprint 在终止前发生变化，未执行强制终止。" } }
                try { Stop-Process -Id $targetProcessId -Force -ErrorAction Stop; $killedPids.Add($targetProcessId) } catch { }
            }
        }

        Start-Sleep -Milliseconds 350
        $afterPass = Get-BridgeObservation -Deep
        if (-not $rootStillVerified) {
            $filtered = Get-FilteredBridgeIdentityRecordsFromOs
            if (-not $filtered.Succeeded) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = Get-BridgeObservation -Deep; KilledPids = @($killedPids); Message = '无法确认是否出现未纳入 snapshot 的 bridge process，未继续强制终止。' } }
            $knownPids = @($verifiedFingerprintsByPid.Keys | ForEach-Object { [int]$_ })
            foreach ($candidate in @($filtered.Records)) {
                $candidatePid = [int](Get-BridgeRecordValue -Record $candidate -Name 'ProcessId')
                if ($knownPids -notcontains $candidatePid) { return [pscustomobject]@{ Success = $false; FallbackUsed = $true; Refused = $true; Observation = $afterPass; KilledPids = @($killedPids); Message = "发现未纳入 verified ownership 的 bridge process PID $candidatePid，未执行强制终止。" } }
            }
        }

        if ((Test-StoppedEvidence -Observation $afterPass) -and @($afterPass.ForeignListenerPids).Count -eq 0) {
            Remove-StalePidFileIfSafe -Observation $afterPass | Out-Null
            $final = Get-BridgeObservation -Deep
            if ((Test-StoppedEvidence -Observation $final) -and -not $final.PidFilePresent) {
                if ($rootStillVerified) { continue }
                return [pscustomobject]@{ Success = $true; FallbackUsed = $true; Refused = $false; Observation = $final; KilledPids = @($killedPids); Message = 'Codex Bridge 已停止' }
            }
        }
    }

    $finalObservation = Get-BridgeObservation -Deep
    $success = (Test-StoppedEvidence -Observation $finalObservation) -and @($finalObservation.ForeignListenerPids).Count -eq 0 -and -not $finalObservation.PidFilePresent
    [pscustomobject]@{ Success = $success; FallbackUsed = $true; Refused = -not $success; Observation = $finalObservation; KilledPids = @($killedPids); Message = if ($success) { 'Codex Bridge 已停止' } else { '无法在安全验证进程树后完成停止，未继续终止未知进程。' } }
}

function Invoke-StartOperation {
    $before = Get-BridgeObservation -Deep
    if (Test-RunningEvidence -Observation $before) { return [pscustomobject]@{ Success = $true; Operation = 'Start'; Observation = $before; Message = 'Codex Bridge 已经在运行'; FallbackUsed = $false } }
    if ($before.ObservedState -eq 'Conflict' -or @($before.ForeignListenerPids).Count -gt 0) { return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = '18080 已被未归属到当前 bridge ownership tree 的进程占用，未执行启动。'; FallbackUsed = $false } }
    if ($before.PidFileState -in @('StaleMissingProcess','StaleReusedPid')) { Remove-StalePidFileIfSafe -Observation $before | Out-Null; $before = Get-BridgeObservation -Deep }
    if ($before.ObservedState -ne 'Stopped' -or $before.PidFilePresent -or @($before.AllBridgePids).Count -gt 0) { return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = '检测到 bridge 进程、PID 文件或不完整运行证据，未重复启动。请先执行停止或查看日志。'; FallbackUsed = $false } }
    $runnerPath = Get-BridgeRunnerPath
    if (-not (Test-Path -LiteralPath $runnerPath -PathType Leaf)) { return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = "找不到 Bridge runner：$runnerPath"; FallbackUsed = $false } }
    if (-not (Test-Path -LiteralPath $script:Config.AuthJsonPath -PathType Leaf)) { return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = "找不到 Codex OAuth 文件：$($script:Config.AuthJsonPath)"; FallbackUsed = $false } }
    $startArguments = New-BridgeRuntimeArguments -Config $script:Config -Verb 'start' -IncludeAuthJson -IncludeVerbose
    $invocation = $null
    try { $invocation = New-HiddenProcess -Arguments $startArguments } catch { return [pscustomobject]@{ Success = $false; Operation = 'Start'; Observation = $before; Message = $_.Exception.Message; FallbackUsed = $false } }
    $waitResult = $null
    try {
        $waitResult = Wait-ForStarted -Invocation $invocation
        [pscustomobject]@{ Success = $waitResult.Success; Operation = 'Start'; Observation = $waitResult.Observation; Message = $waitResult.Message; ExitCode = $waitResult.ExitCode; StdOut = $waitResult.StdOut; StdErr = $waitResult.StdErr; FallbackUsed = $false }
    } finally {
        if ($waitResult -and $waitResult.Success) {
            Cleanup-StartedInvocation -Invocation $invocation | Out-Null
        } else {
            # A failed or exceptional start is the only path where the
            # controller-owned invocation may be explicitly tree-aborted.
            Stop-ControllerOwnedInvocation -Invocation $invocation | Out-Null
        }
        Dispose-Invocation -Invocation $invocation
    }
}

function Invoke-StopOperation {
    $before = Get-BridgeObservation -Deep
    if ((Test-StoppedEvidence -Observation $before) -and @($before.ForeignListenerPids).Count -eq 0) { Remove-StalePidFileIfSafe -Observation $before | Out-Null; return [pscustomobject]@{ Success = $true; Operation = 'Stop'; Observation = Get-BridgeObservation -Deep; Message = 'Codex Bridge 已停止'; FallbackUsed = $false } }
    if (-not $before.CanAttemptOfficialStop) { return [pscustomobject]@{ Success = $false; Operation = 'Stop'; Observation = $before; Message = '没有足够证据确认当前 bridge daemon，未执行官方 stop 或强制终止。'; FallbackUsed = $false; Refused = $true } }
    $snapshot = $before.OwnershipSnapshot
    $stopArguments = New-BridgeRuntimeArguments -Config $script:Config -Verb 'stop'
    $official = Invoke-UvxCommand -Arguments $stopArguments -WaitMilliseconds 15000
    if (-not $official.Exited) { return [pscustomobject]@{ Success = $false; Operation = 'Stop'; Observation = Get-BridgeObservation -Deep; Message = "官方 stop invocation 未能确认退出，未进入 bridge fallback。$($official.Error)"; FallbackUsed = $false; Refused = $true; OfficialExitCode = $official.ExitCode; StdOut = $official.StdOut; StdErr = $official.StdErr } }
    Start-Sleep -Milliseconds $script:Config.StopGraceMilliseconds
    $afterOfficial = Get-BridgeObservation -Deep
    if ((Test-StoppedEvidence -Observation $afterOfficial) -and @($afterOfficial.ForeignListenerPids).Count -eq 0) {
        Remove-StalePidFileIfSafe -Observation $afterOfficial | Out-Null
        $final = Get-BridgeObservation -Deep
        $success = (Test-StoppedEvidence -Observation $final) -and -not $final.PidFilePresent
        return [pscustomobject]@{ Success = $success; Operation = 'Stop'; Observation = $final; Message = if ($success) { 'Codex Bridge 已停止' } else { 'Bridge 已无运行证据，但 PID 文件未能安全清理。' }; FallbackUsed = $false; OfficialExitCode = $official.ExitCode; StdOut = $official.StdOut; StdErr = $official.StdErr }
    }
    if (-not $snapshot -or -not $snapshot.CanForceStopAtCapture) { return [pscustomobject]@{ Success = $false; Operation = 'Stop'; Observation = $afterOfficial; Message = '官方 stop 未完成停止，但停止前 ownership 不满足安全强制终止条件；未杀任何 bridge 进程。'; FallbackUsed = $true; Refused = $true; OfficialExitCode = $official.ExitCode; StdOut = $official.StdOut; StdErr = $official.StdErr } }
    $fallback = Stop-VerifiedBridgeTree -OwnershipSnapshot $snapshot
    [pscustomobject]@{ Success = $fallback.Success; Operation = 'Stop'; Observation = $fallback.Observation; Message = $fallback.Message; FallbackUsed = $true; Refused = $fallback.Refused; OfficialExitCode = $official.ExitCode; KilledPids = $fallback.KilledPids; StdOut = $official.StdOut; StdErr = $official.StdErr }
}

function Invoke-RestartOperation {
    $stopResult = Invoke-StopOperation
    if (-not $stopResult.Success) { $stopResult.Operation = 'Restart'; $stopResult.Message = "重启未执行启动阶段：$($stopResult.Message)"; return $stopResult }
    $startResult = Invoke-StartOperation
    $startResult.Operation = 'Restart'
    $startResult.FallbackUsed = $stopResult.FallbackUsed
    if ($startResult.Success) { $startResult.Message = 'Codex Bridge 已重启' }
    return $startResult
}

function Invoke-WorkItem {
    param([Parameter(Mandatory)] $WorkItem)
    try {
        switch ($WorkItem.Kind) {
            'Probe' { return [pscustomobject]@{ Success = $true; Operation = 'Probe'; Observation = Get-BridgeObservation; Message = $null } }
            'Start' { return Invoke-StartOperation }
            'Stop' { return Invoke-StopOperation }
            'Restart' { return Invoke-RestartOperation }
            default { throw "未知后台操作：$($WorkItem.Kind)" }
        }
    } catch {
        $observation = Get-BridgeObservation -Deep
        return [pscustomobject]@{ Success = $false; Operation = $WorkItem.Kind; Observation = $observation; Message = $_.Exception.Message }
    }
}

$createdNewMutex = $false
$mutex = [System.Threading.Mutex]::new($true, 'Local\CodexBridgeController', [ref]$createdNewMutex)
if (-not $createdNewMutex) { $mutex.Dispose(); exit 0 }

$script:WorkerRunspace = $null
$script:ActiveInvocation = $null

try {
    $mainSource = Get-Content -LiteralPath $PSCommandPath -Raw
    $backendStartMarker = '# ---- Runtime configuration'
    $backendEndMarker = '$createdNewMutex = $false'
    $backendStart = $mainSource.IndexOf($backendStartMarker, [System.StringComparison]::Ordinal)
    $backendEnd = $mainSource.IndexOf($backendEndMarker, [System.StringComparison]::Ordinal)
    if ($backendStart -lt 0 -or $backendEnd -le $backendStart) { throw '无法定位后台函数区域。' }
    $modelSource = Get-Content -LiteralPath $ModelPath -Raw
    $controllerLiteral = "'" + $ControllerDirectory.Replace("'", "''") + "'"
    $workerBootstrap = "`$ErrorActionPreference = 'Stop'`r`n`$ControllerDirectory = $controllerLiteral`r`n" + $modelSource + "`r`n" + $mainSource.Substring($backendStart, $backendEnd - $backendStart)
    $script:WorkerRunspace = [System.Management.Automation.Runspaces.RunspaceFactory]::CreateRunspace()
    $script:WorkerRunspace.Open()
    $bootstrapPowerShell = [System.Management.Automation.PowerShell]::Create()
    $bootstrapPowerShell.Runspace = $script:WorkerRunspace
    [void]$bootstrapPowerShell.AddScript($workerBootstrap)
    [void]$bootstrapPowerShell.Invoke()
    if ($bootstrapPowerShell.HadErrors) { $bootstrapError = ($bootstrapPowerShell.Streams.Error | Select-Object -First 1); throw "后台 runspace 初始化失败：$bootstrapError" }
    $bootstrapPowerShell.Dispose()
} catch {
    if ($script:WorkerRunspace) { try { $script:WorkerRunspace.Close() } catch { }; try { $script:WorkerRunspace.Dispose() } catch { }; $script:WorkerRunspace = $null }
    try { $mutex.ReleaseMutex() } catch { }
    $mutex.Dispose()
    [void][System.Windows.Forms.MessageBox]::Show("无法初始化后台 worker：$($_.Exception.Message)",'Codex Bridge',[System.Windows.Forms.MessageBoxButtons]::OK,[System.Windows.Forms.MessageBoxIcon]::Error)
    exit 1
}

$script:CurrentObservation = $null
$script:CurrentObservedState = 'Unknown'
$script:CurrentOperationState = 'Idle'
$script:CurrentPresentation = Get-BridgePresentation -OperationState Idle -ObservedState Unknown
$script:OperationInFlight = $false
$script:ExitAfterStop = $false
$script:Closing = $false
$script:ProbeQueued = $false
$script:NextProbeAt = [DateTimeOffset]::MinValue
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
$openDashboardMenu = [System.Windows.Forms.ToolStripMenuItem]::new('打开数据面板')
$exitMenu = [System.Windows.Forms.ToolStripMenuItem]::new('退出控制器')
$null = $contextMenu.Items.Add($statusMenu)
$null = $contextMenu.Items.Add([System.Windows.Forms.ToolStripSeparator]::new())
$null = $contextMenu.Items.Add($startMenu)
$null = $contextMenu.Items.Add($stopMenu)
$null = $contextMenu.Items.Add($restartMenu)
$null = $contextMenu.Items.Add([System.Windows.Forms.ToolStripSeparator]::new())
$null = $contextMenu.Items.Add($openLogMenu)
$null = $contextMenu.Items.Add($openRunDirectoryMenu)
$null = $contextMenu.Items.Add($openDashboardMenu)
$null = $contextMenu.Items.Add([System.Windows.Forms.ToolStripSeparator]::new())
$null = $contextMenu.Items.Add($exitMenu)
$notifyIcon.ContextMenuStrip = $contextMenu

function Update-MenuForState {
    $presentation = Get-BridgePresentation -OperationState $script:CurrentOperationState -ObservedState $script:CurrentObservedState -CanAttemptOfficialStop ([bool]($script:CurrentObservation -and $script:CurrentObservation.CanAttemptOfficialStop))
    $script:CurrentPresentation = $presentation
    $statusMenu.Text = "状态：$($presentation.DisplayText)"
    $startMenu.Enabled = $presentation.StartEnabled
    $stopMenu.Enabled = $presentation.StopEnabled
    $restartMenu.Enabled = $presentation.RestartEnabled
    $openDashboardMenu.Enabled = $script:CurrentObservedState -eq 'Running' -and -not $script:OperationInFlight
    $exitMenu.Enabled = -not $script:OperationInFlight
    $notifyIcon.Text = $presentation.Tooltip.Substring(0, [Math]::Min(63, $presentation.Tooltip.Length))
}

function Show-Notice {
    param([string]$Title,[string]$Message,[System.Windows.Forms.ToolTipIcon]$Icon = [System.Windows.Forms.ToolTipIcon]::Info)
    if ($notifyIcon.Visible) { $notifyIcon.ShowBalloonTip(3000, $Title, $Message, $Icon) }
}

function Show-OperationError {
    param([string]$Message)
    $logPath = if ($script:CurrentObservation -and $script:CurrentObservation.LogPath) { $script:CurrentObservation.LogPath } else { $script:Config.LogFilePath }
    [void][System.Windows.Forms.MessageBox]::Show($form,"$Message`n`n日志：$logPath",'Codex Bridge',[System.Windows.Forms.MessageBoxButtons]::OK,[System.Windows.Forms.MessageBoxIcon]::Error)
}

function Apply-Observation {
    param([Parameter(Mandatory)] $Observation)
    $previousState = $script:CurrentObservedState
    $script:CurrentObservation = $Observation
    $script:CurrentObservedState = if ($Observation.ObservedState) { [string]$Observation.ObservedState } else { 'Unknown' }
    Update-MenuForState
    if ($previousState -eq 'Running' -and $script:CurrentObservedState -in @('Degraded','Conflict')) { Show-Notice -Title 'Codex Bridge' -Message 'Bridge 状态异常，请打开日志查看。' -Icon Warning }
}

function Remove-QueuedProbes {
    $newQueue = [System.Collections.Queue]::new()
    while ($script:WorkQueue.Count -gt 0) { $item = $script:WorkQueue.Dequeue(); if ($item.Kind -ne 'Probe') { $null = $newQueue.Enqueue($item) } }
    $script:ProbeQueued = $false
    $script:WorkQueue = $newQueue
}

function Start-NextWorkItem {
    if ($script:ActiveInvocation -or $script:WorkQueue.Count -eq 0 -or $script:Closing) { return }
    $item = $script:WorkQueue.Dequeue()
    $script:ActiveWorkItem = $item
    $workerPowerShell = $null
    try {
        $workerPowerShell = [System.Management.Automation.PowerShell]::Create()
        $workerPowerShell.Runspace = $script:WorkerRunspace
        [void]$workerPowerShell.AddCommand('Invoke-WorkItem').AddParameter('WorkItem', $item)
        $asyncResult = $workerPowerShell.BeginInvoke()
        $script:ActiveInvocation = [pscustomobject]@{ PowerShell = $workerPowerShell; AsyncResult = $asyncResult; WorkItem = $item }
    } catch {
        if ($workerPowerShell) { try { $workerPowerShell.Dispose() } catch { } }
        $script:ActiveWorkItem = $null
        if ($item.Kind -eq 'Probe') { $script:ProbeQueued = $false }
        $script:BackendFailureNotified = $true
        $script:CurrentObservedState = 'Unknown'
        Update-MenuForState
        Show-Notice -Title 'Codex Bridge' -Message "后台任务无法启动：$($_.Exception.Message)" -Icon Error
    }
}

function Request-Probe {
    param([switch]$Force)
    if ($script:Closing -or $script:OperationInFlight -or $script:ProbeQueued) { return }
    if ($script:ActiveInvocation -and $script:ActiveWorkItem -and $script:ActiveWorkItem.Kind -eq 'Probe') { return }
    if (-not $Force -and [DateTimeOffset]::Now -lt $script:NextProbeAt) { return }
    $script:ProbeQueued = $true
    $script:NextProbeAt = [DateTimeOffset]::Now.AddMilliseconds($script:Config.LightProbeIntervalMilliseconds)
    $null = $script:WorkQueue.Enqueue([pscustomobject]@{ Kind = 'Probe'; Sequence = 0 })
    Start-NextWorkItem
}

function Request-Operation {
    param([ValidateSet('Start','Stop','Restart')] [string]$Kind)
    if ($script:Closing -or $script:OperationInFlight) { return }
    if ($Kind -eq 'Stop' -and $script:CurrentObservation -and -not $script:CurrentObservation.CanAttemptOfficialStop -and $script:CurrentObservedState -notin @('Running')) { return }
    Remove-QueuedProbes
    $script:OperationInFlight = $true
    $script:OperationSequence++
    $script:CurrentOperationState = switch ($Kind) { 'Start' { 'Starting' } 'Stop' { 'Stopping' } 'Restart' { 'Restarting' } }
    Update-MenuForState
    $null = $script:WorkQueue.Enqueue([pscustomobject]@{ Kind = $Kind; Sequence = $script:OperationSequence })
    Start-NextWorkItem
}

function Show-ExitChoiceDialog {
    $dialog = [System.Windows.Forms.Form]::new()
    $dialog.Text = '退出 Codex Bridge 控制器'; $dialog.StartPosition = 'CenterParent'; $dialog.FormBorderStyle = 'FixedDialog'; $dialog.MinimizeBox = $false; $dialog.MaximizeBox = $false; $dialog.ShowInTaskbar = $false; $dialog.ClientSize = [System.Drawing.Size]::new(430,150)
    $label = [System.Windows.Forms.Label]::new(); $label.Text = "Codex Bridge 仍在后台运行。`r`n请选择退出方式："; $label.AutoSize = $true; $label.Location = [System.Drawing.Point]::new(18,18); $dialog.Controls.Add($label)
    $onlyExit = [System.Windows.Forms.Button]::new(); $onlyExit.Text = '仅退出控制器'; $onlyExit.Size = [System.Drawing.Size]::new(120,32); $onlyExit.Location = [System.Drawing.Point]::new(18,92); $onlyExit.DialogResult = [System.Windows.Forms.DialogResult]::Yes; $dialog.Controls.Add($onlyExit)
    $stopAndExit = [System.Windows.Forms.Button]::new(); $stopAndExit.Text = '停止 Bridge 并退出'; $stopAndExit.Size = [System.Drawing.Size]::new(140,32); $stopAndExit.Location = [System.Drawing.Point]::new(150,92); $stopAndExit.DialogResult = [System.Windows.Forms.DialogResult]::No; $dialog.Controls.Add($stopAndExit)
    $cancel = [System.Windows.Forms.Button]::new(); $cancel.Text = '取消'; $cancel.Size = [System.Drawing.Size]::new(80,32); $cancel.Location = [System.Drawing.Point]::new(302,92); $cancel.DialogResult = [System.Windows.Forms.DialogResult]::Cancel; $dialog.Controls.Add($cancel)
    $dialog.AcceptButton = $onlyExit; $dialog.CancelButton = $cancel
    $result = $dialog.ShowDialog($form); $dialog.Dispose(); return $result
}

function Close-Controller {
    if ($script:Closing) { return }
    $script:Closing = $true; $timer.Stop(); $notifyIcon.Visible = $false; $form.Close()
}

function Request-ControllerExit {
    if ($script:OperationInFlight) { return }
    $observation = $script:CurrentObservation
    $bridgeStillPresent = $observation -and ($observation.HealthOk -or @($observation.ListenerPids).Count -gt 0 -or @($observation.AllBridgePids).Count -gt 0 -or $observation.CanAttemptOfficialStop)
    if (-not $bridgeStillPresent) { Close-Controller; return }
    $choice = Show-ExitChoiceDialog
    switch ($choice) { ([System.Windows.Forms.DialogResult]::Yes) { Close-Controller } ([System.Windows.Forms.DialogResult]::No) { $script:ExitAfterStop = $true; Request-Operation -Kind Stop } }
}

function Open-Log {
    $path = if ($script:CurrentObservation -and $script:CurrentObservation.LogPath) { $script:CurrentObservation.LogPath } else { $script:Config.LogFilePath }
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { [void][System.Windows.Forms.MessageBox]::Show($form,"日志不存在。`n`n运行目录：$($script:Config.RunDirectory)",'Codex Bridge',[System.Windows.Forms.MessageBoxButtons]::OK,[System.Windows.Forms.MessageBoxIcon]::Information); return }
    try { $startInfo = [System.Diagnostics.ProcessStartInfo]::new(); $startInfo.FileName = $path; $startInfo.UseShellExecute = $true; [void][System.Diagnostics.Process]::Start($startInfo) } catch { Show-OperationError -Message "无法打开日志：$($_.Exception.Message)" }
}

function Open-RunDirectory {
    if (-not (Test-Path -LiteralPath $script:Config.RunDirectory -PathType Container)) { [void][System.Windows.Forms.MessageBox]::Show($form,"运行目录不存在：`n$($script:Config.RunDirectory)",'Codex Bridge',[System.Windows.Forms.MessageBoxButtons]::OK,[System.Windows.Forms.MessageBoxIcon]::Information); return }
    try { Start-Process -FilePath 'explorer.exe' -ArgumentList @($script:Config.RunDirectory) -WindowStyle Hidden } catch { Show-OperationError -Message "无法打开运行目录：$($_.Exception.Message)" }
}

function Open-Dashboard {
    if ($script:CurrentObservedState -ne 'Running' -or $script:OperationInFlight) { return }
    try {
        $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
        $startInfo.FileName = [string]$script:Config.DashboardUri
        $startInfo.UseShellExecute = $true
        [void][System.Diagnostics.Process]::Start($startInfo)
    } catch { Show-OperationError -Message "无法打开数据面板：$($_.Exception.Message)" }
}

function Apply-WorkResult {
    param([Parameter(Mandatory)] $Item,$Result,[string]$InvocationError)
    if ($Item.Kind -ne 'Probe' -and $Item.Sequence -ne $script:OperationSequence) { return }
    if ($Item.Kind -eq 'Probe') { $script:ProbeQueued = $false }
    if (-not $Result) { $Result = [pscustomobject]@{ Success = $false; Operation = $Item.Kind; Observation = $null; Message = if ($InvocationError) { $InvocationError } else { '后台操作没有返回结果。' } } }
    if ($Result.Observation) { Apply-Observation -Observation $Result.Observation }
    if ($Item.Kind -eq 'Probe') {
        if ($Result.Success) { $script:BackendFailureNotified = $false }
        elseif (-not $script:BackendFailureNotified) { $script:BackendFailureNotified = $true; $script:CurrentObservedState = 'Unknown'; Update-MenuForState; Show-Notice -Title 'Codex Bridge' -Message "后台状态探测异常：$($Result.Message)" -Icon Error }
    } else {
        $script:OperationInFlight = $false; $script:CurrentOperationState = 'Idle'
        Update-MenuForState
        if ($Result.Success) {
            if ($Item.Kind -eq 'Start') { Show-Notice -Title 'Codex Bridge' -Message "Codex Bridge 已启动`r`n$($script:Config.BridgeHost):$($script:Config.BridgePort)" }
            elseif ($Item.Kind -eq 'Stop') { Show-Notice -Title 'Codex Bridge' -Message 'Codex Bridge 已停止' }
            elseif ($Item.Kind -eq 'Restart') { Show-Notice -Title 'Codex Bridge' -Message "Codex Bridge 已重启`r`n$($script:Config.BridgeHost):$($script:Config.BridgePort)" }
            if ($script:ExitAfterStop -and $Item.Kind -eq 'Stop') { $script:ExitAfterStop = $false; Close-Controller; return }
        } else { $script:ExitAfterStop = $false; Show-OperationError -Message $Result.Message }
    }
    if (-not $script:Closing) { Update-MenuForState; Request-Probe -Force; Start-NextWorkItem }
}

function Complete-ActiveWorkItem {
    $active = $script:ActiveInvocation
    if (-not $active) { return }
    $state = $active.PowerShell.InvocationStateInfo.State
    if ($state -notin @([System.Management.Automation.PSInvocationState]::Completed,[System.Management.Automation.PSInvocationState]::Failed,[System.Management.Automation.PSInvocationState]::Stopped)) { return }
    $item = $active.WorkItem; $result = $null; $invocationError = $null
    try {
        $output = @($active.PowerShell.EndInvoke($active.AsyncResult)); if ($output.Count -gt 0) { $result = $output[$output.Count - 1] }
        if ($state -ne [System.Management.Automation.PSInvocationState]::Completed) { $streamError = $active.PowerShell.Streams.Error | Select-Object -First 1; $invocationError = if ($streamError) { $streamError.ToString() } else { "后台任务状态：$state" }; $result = $null }
    } catch { $invocationError = $_.Exception.Message; $result = $null }
    finally { $script:ActiveInvocation = $null; $script:ActiveWorkItem = $null; try { $active.PowerShell.Dispose() } catch { } }
    Apply-WorkResult -Item $item -Result $result -InvocationError $invocationError
}

$timer = [System.Windows.Forms.Timer]::new()
$timer.Interval = $script:Config.UiTimerIntervalMilliseconds
$timer.Add_Tick({ Complete-ActiveWorkItem; Request-Probe; Start-NextWorkItem })
$startMenu.Add_Click({ Request-Operation -Kind Start })
$stopMenu.Add_Click({ Request-Operation -Kind Stop })
$restartMenu.Add_Click({ Request-Operation -Kind Restart })
$openLogMenu.Add_Click({ Open-Log })
$openRunDirectoryMenu.Add_Click({ Open-RunDirectory })
$openDashboardMenu.Add_Click({ Open-Dashboard })
$exitMenu.Add_Click({ Request-ControllerExit })
$notifyIcon.Add_MouseClick({ param($sender,$eventArgs); if ($eventArgs.Button -eq [System.Windows.Forms.MouseButtons]::Left -and $script:CurrentObservedState -ne 'Unknown') { Show-Notice -Title 'Codex Bridge' -Message "状态：$($script:CurrentPresentation.DisplayText)" } })

$form.Add_FormClosed({
    $notifyIcon.Visible = $false; $notifyIcon.Dispose(); $contextMenu.Dispose(); $timer.Dispose()
    if ($script:ActiveInvocation) { try { $script:ActiveInvocation.PowerShell.Stop() } catch { }; try { $script:ActiveInvocation.PowerShell.Dispose() } catch { }; $script:ActiveInvocation = $null; $script:ActiveWorkItem = $null }
    if ($script:WorkerRunspace) {
        try { $cleanupPowerShell = [System.Management.Automation.PowerShell]::Create(); $cleanupPowerShell.Runspace = $script:WorkerRunspace; [void]$cleanupPowerShell.AddCommand('Dispose-WorkerResources'); [void]$cleanupPowerShell.Invoke(); $cleanupPowerShell.Dispose() } catch { }
        try { $script:WorkerRunspace.Close() } catch { }; try { $script:WorkerRunspace.Dispose() } catch { }; $script:WorkerRunspace = $null
    }
    try { $mutex.ReleaseMutex() } catch { }; $mutex.Dispose()
})

Update-MenuForState
$form.Show(); $form.Hide(); $timer.Start(); Request-Probe -Force
[System.Windows.Forms.Application]::Run($form)
