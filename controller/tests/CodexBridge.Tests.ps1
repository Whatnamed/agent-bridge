$ErrorActionPreference = 'Stop'

$modelPath = Join-Path $PSScriptRoot '..\lib\BridgeModel.ps1'
. (Resolve-Path -LiteralPath $modelPath)

$script:TestConfig = [pscustomobject]@{
    BridgePackage = 'openai-api-server-via-codex'
    BridgeExecutableName = 'openai-api-server-via-codex.exe'
    BridgeIdentityMarker = 'openai-api-server-via-codex'
    BridgeSupervisorCommandMarker = 'daemon-run'
    BridgePort = 18080
}

$script:RuntimeTestConfig = [pscustomobject]@{
    BridgePackageSpec = 'openai-api-server-via-codex==0.2.0'
    BridgeCommand = 'openai-api-server-via-codex'
    BridgeHost = '127.0.0.1'
    BridgePort = 18080
    RunDirectory = 'C:\Users\hasee\.config\openai-api-server-via-codex\run'
    PidFilePath = 'C:\Users\hasee\.config\openai-api-server-via-codex\run\server-127.0.0.1-18080.pid'
    LogFilePath = 'C:\Users\hasee\.config\openai-api-server-via-codex\run\server-127.0.0.1-18080.log'
    AuthJsonPath = 'C:\Users\hasee\.codex\auth.json'
    TelemetryEnabled = $true
    DashboardEnabled = $true
    ReasoningSummaryDefault = 'none'
}

function Get-TestArgumentValue {
    param([string[]]$Arguments, [string]$Flag)
    $index = [Array]::IndexOf($Arguments, $Flag)
    if ($index -lt 0 -or $index + 1 -ge $Arguments.Count) { return $null }
    return $Arguments[$index + 1]
}

function New-TestProcessRecord {
    param(
        [int]$ProcessId,
        [int]$ParentProcessId,
        [string]$Name = 'openai-api-server-via-codex.exe',
        [string]$CommandLine = 'openai-api-server-via-codex daemon-run',
        [string]$CreationDate = '2026-08-17T00:00:00.0000000Z',
        [string]$ExecutablePath = 'E:\Dev\bridge\openai-api-server-via-codex.exe'
    )

    [pscustomobject]@{
        ProcessId = $ProcessId
        ParentProcessId = $ParentProcessId
        CreationDate = $CreationDate
        Name = $Name
        ExecutablePath = $ExecutablePath
        CommandLine = $CommandLine
    }
}

function Resolve-TestTopology {
    param(
        [object[]]$Records,
        [int]$PidFilePid = 100,
        [object[]]$Listeners = @(),
        [object]$HistoricalAffinity = $null
    )

    Resolve-BridgeOwnership -ProcessRecords $Records -PidFilePresent $true -PidFilePid $PidFilePid -ListenerRecords $Listeners -Config $script:TestConfig -ProcessObservationComplete $true -ListenerObservationComplete $true -HistoricalAffinity $HistoricalAffinity
}

Describe 'Codex Bridge ownership and state model' {
    It 'owns only the PID-file supervisor and bridge descendants' {
        $supervisor = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10
        $server = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server'
        $otherBridge = New-TestProcessRecord -ProcessId 300 -ParentProcessId 10
        $topology = Resolve-TestTopology -Records @($supervisor, $server, $otherBridge) -Listeners @([pscustomobject]@{ OwningProcess = 200 })

        (@($topology.OwnedProcessPids) -contains 100) | Should Be $true
        (@($topology.OwnedProcessPids) -contains 200) | Should Be $true
        (@($topology.OwnedProcessPids) -contains 300) | Should Be $false
        $topology.TargetInstanceAffinity | Should Be 'Bound'
        $topology.OwnedListenerOwnerFingerprints[0].ProcessId | Should Be 200
        $topology.CanForceStop | Should Be $true
    }

    It 'does not expand ownership through a common ancestor' {
        $supervisorA = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10
        $serverA = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server'
        $supervisorB = New-TestProcessRecord -ProcessId 300 -ParentProcessId 10
        $serverB = New-TestProcessRecord -ProcessId 400 -ParentProcessId 300 -CommandLine 'openai-api-server-via-codex server'
        $topology = Resolve-TestTopology -Records @($supervisorA, $serverA, $supervisorB, $serverB) -Listeners @([pscustomobject]@{ OwningProcess = 200 })

        $topology.OwnershipEdges.Count | Should Be 1
        (@($topology.OwnedProcessPids) -contains 300) | Should Be $false
        (@($topology.OwnedProcessPids) -contains 400) | Should Be $false
    }

    It 'rejects a reused PID file instead of treating the new process as the supervisor' {
        $reused = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10 -Name 'unrelated.exe' -CommandLine 'unrelated.exe' -ExecutablePath 'E:\Other\unrelated.exe'
        $topology = Resolve-TestTopology -Records @($reused)

        $topology.PidFileState | Should Be 'StaleReusedPid'
        $topology.OwnedProcessPids.Count | Should Be 0
        $topology.CanForceStop | Should Be $false
    }

    It 'does not treat a valid daemon on another port as the target instance' {
        $otherSupervisor = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10 -CommandLine 'openai-api-server-via-codex daemon-run --port 19090'
        $otherServer = New-TestProcessRecord -ProcessId 400 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server --port 19090'
        $topology = Resolve-TestTopology -Records @($otherSupervisor, $otherServer) -Listeners @()

        $topology.PidFileState | Should Be 'Owned'
        $topology.TargetInstanceAffinity | Should Be 'Unbound'
        $topology.CanAttemptOfficialStop | Should Be $false
        $topology.CanForceStop | Should Be $false
        (Get-ObservedBridgeState -Topology $topology -HealthKnown $true -HealthOk $false) | Should Be 'Degraded'
    }

    It 'retains only a fingerprint-matched historical target affinity' {
        $supervisor = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10
        $server = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server --port 18080'
        $bound = Resolve-TestTopology -Records @($supervisor, $server) -Listeners @([pscustomobject]@{ OwningProcess = 200 })

        $historical = Resolve-TestTopology -Records @($supervisor) -Listeners @() -HistoricalAffinity $bound
        $historical.TargetInstanceAffinity | Should Be 'HistoricalBound'
        $historical.CanAttemptOfficialStop | Should Be $true
        $historical.CanForceStop | Should Be $true

        $reusedSupervisor = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10 -CreationDate '2026-08-17T01:00:00.0000000Z' -CommandLine 'openai-api-server-via-codex daemon-run --port 19090'
        $reused = Resolve-TestTopology -Records @($reusedSupervisor) -Listeners @() -HistoricalAffinity $bound
        $reused.TargetInstanceAffinity | Should Be 'Unbound'
        $reused.CanAttemptOfficialStop | Should Be $false
        $reused.CanForceStop | Should Be $false
    }

    It 'requires creation date and the complete fingerprint to match a snapshot child' {
        $supervisor = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10
        $child = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server'
        $topology = Resolve-TestTopology -Records @($supervisor, $child)
        $snapshot = New-OwnershipSnapshot -Topology $topology
        $sameChild = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server'
        $reusedChild = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server' -CreationDate '2026-08-17T00:01:00.0000000Z'

        $snapshot.ProcessFingerprints.Count | Should Be 2
        (Test-ProcessFingerprintMatch -Expected $snapshot.ProcessFingerprints[1] -Actual $sameChild) | Should Be $true
        (Test-ProcessFingerprintMatch -Expected $snapshot.ProcessFingerprints[1] -Actual $reusedChild) | Should Be $false
    }

    It 'includes a newly respawned child only when it remains below the verified supervisor' {
        $supervisor = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10
        $newChild = New-TestProcessRecord -ProcessId 201 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server'
        $topology = Resolve-TestTopology -Records @($supervisor, $newChild)

        (@($topology.OwnedProcessPids) -contains 201) | Should Be $true
        $topology.OwnershipEdges[0].ParentProcessId | Should Be 100
    }

    It 'marks a foreign listener as conflict and disables force stop' {
        $supervisor = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10
        $server = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server'
        $foreign = New-TestProcessRecord -ProcessId 900 -ParentProcessId 10 -Name 'foreign-server.exe' -CommandLine 'foreign-server --port 18080' -ExecutablePath 'E:\Other\foreign-server.exe'
        $listeners = @([pscustomobject]@{ OwningProcess = 200 }, [pscustomobject]@{ OwningProcess = 900 })
        $topology = Resolve-TestTopology -Records @($supervisor, $server, $foreign) -Listeners $listeners

        (@($topology.ForeignListenerPids) -contains 900) | Should Be $true
        $topology.CanForceStop | Should Be $false
        (Get-ObservedBridgeState -Topology $topology -HealthKnown $true -HealthOk $true) | Should Be 'Conflict'
    }

    It 'does not include another bridge instance listening on a different port' {
        $supervisorA = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10
        $serverA = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server --port 18080'
        $supervisorB = New-TestProcessRecord -ProcessId 300 -ParentProcessId 10
        $serverB = New-TestProcessRecord -ProcessId 400 -ParentProcessId 300 -CommandLine 'openai-api-server-via-codex server --port 19090'
        $topology = Resolve-TestTopology -Records @($supervisorA, $serverA, $supervisorB, $serverB) -Listeners @([pscustomobject]@{ OwningProcess = 200 })

        (@($topology.OwnedProcessPids) -contains 100) | Should Be $true
        (@($topology.OwnedProcessPids) -contains 200) | Should Be $true
        (@($topology.OwnedProcessPids) -contains 300) | Should Be $false
        (@($topology.OwnedProcessPids) -contains 400) | Should Be $false
    }

    It 'distinguishes a missing PID process from a reused PID' {
        $missing = Resolve-TestTopology -Records @()
        $missing.PidFileState | Should Be 'StaleMissingProcess'

        $reused = Resolve-TestTopology -Records @(New-TestProcessRecord -ProcessId 100 -ParentProcessId 10 -Name 'other.exe' -CommandLine 'other.exe' -ExecutablePath 'E:\Other\other.exe')
        $reused.PidFileState | Should Be 'StaleReusedPid'
    }

    It 'does not allow stop when the PID file points directly to a bridge child' {
        $actualServer = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10 -CommandLine 'openai-api-server-via-codex server'
        $topology = Resolve-TestTopology -Records @($actualServer) -Listeners @([pscustomobject]@{ OwningProcess = 100 })

        $topology.PidFileState | Should Be 'Ambiguous'
        $topology.TargetInstanceAffinity | Should Be 'Conflict'
        $topology.CanAttemptOfficialStop | Should Be $false
        $topology.CanForceStop | Should Be $false
    }

    It 'builds a pure fallback action plan for verified supervisor and child phases' {
        $supervisor = New-TestProcessRecord -ProcessId 100 -ParentProcessId 10
        $server = New-TestProcessRecord -ProcessId 200 -ParentProcessId 100 -CommandLine 'openai-api-server-via-codex server'
        $listeners = @([pscustomobject]@{ OwningProcess = 200 })
        $topology = Resolve-TestTopology -Records @($supervisor, $server) -Listeners $listeners
        $snapshot = New-OwnershipSnapshot -Topology $topology

        $rootPlan = Get-FallbackActionPlan -OwnershipSnapshot $snapshot -LiveRecords @($supervisor, $server) -SupervisorLive $true -ListenerRecords $listeners -Config $script:TestConfig
        $rootPlan.Allowed | Should Be $true
        $rootPlan.TargetPids.Count | Should Be 1
        $rootPlan.TargetPids[0] | Should Be 100

        $childPlan = Get-FallbackActionPlan -OwnershipSnapshot $snapshot -LiveRecords @($server) -SupervisorLive $false -ListenerRecords $listeners -Config $script:TestConfig
        $childPlan.Allowed | Should Be $true
        $childPlan.TargetPids.Count | Should Be 1
        $childPlan.TargetPids[0] | Should Be 200

        $refusedPlan = Get-FallbackActionPlan -OwnershipSnapshot $snapshot -LiveRecords @($server) -SupervisorLive $false -ListenerRecords @([pscustomobject]@{ OwningProcess = 900 }) -Config $script:TestConfig
        $refusedPlan.Allowed | Should Be $false
        $refusedPlan.TargetPids.Count | Should Be 0
    }

    It 'maps operation and observed states to safe menu capabilities' {
        $starting = Get-BridgePresentation -OperationState Starting -ObservedState Stopped
        $running = Get-BridgePresentation -OperationState Idle -ObservedState Running
        $stopped = Get-BridgePresentation -OperationState Idle -ObservedState Stopped
        $abnormal = Get-BridgePresentation -OperationState Idle -ObservedState Degraded -CanAttemptOfficialStop $false

        $starting.StartEnabled | Should Be $false
        $starting.StopEnabled | Should Be $false
        $starting.Tooltip | Should Be 'Codex Bridge — Starting'
        $running.StartEnabled | Should Be $false
        $running.StopEnabled | Should Be $true
        $running.RestartEnabled | Should Be $true
        $stopped.StartEnabled | Should Be $true
        $stopped.StopEnabled | Should Be $false
        $abnormal.StopEnabled | Should Be $false
    }
}

Describe 'Codex Bridge deterministic runtime arguments' {
    It 'passes explicit target host, port, state, PID, log, and auth paths' {
        $start = New-BridgeRuntimeArguments -Config $script:RuntimeTestConfig -Verb 'start' -IncludeAuthJson -IncludeVerbose
        $stop = New-BridgeRuntimeArguments -Config $script:RuntimeTestConfig -Verb 'stop'
        $status = New-BridgeRuntimeArguments -Config $script:RuntimeTestConfig -Verb 'status'

        foreach ($arguments in @($start, $stop, $status)) {
            (Get-TestArgumentValue -Arguments $arguments -Flag '--host') | Should Be '127.0.0.1'
            (Get-TestArgumentValue -Arguments $arguments -Flag '--port') | Should Be '18080'
            (Get-TestArgumentValue -Arguments $arguments -Flag '--state-dir') | Should Be $script:RuntimeTestConfig.RunDirectory
            (Get-TestArgumentValue -Arguments $arguments -Flag '--pid-file') | Should Be $script:RuntimeTestConfig.PidFilePath
            (Get-TestArgumentValue -Arguments $arguments -Flag '--log-file') | Should Be $script:RuntimeTestConfig.LogFilePath
        }

        (Get-TestArgumentValue -Arguments $start -Flag '--auth-json') | Should Be $script:RuntimeTestConfig.AuthJsonPath
        (Get-TestArgumentValue -Arguments $start -Flag '--telemetry-enabled') | Should Be 'true'
        (Get-TestArgumentValue -Arguments $start -Flag '--dashboard-enabled') | Should Be 'true'
        (Get-TestArgumentValue -Arguments $start -Flag '--reasoning-summary-default') | Should Be 'none'
        (@($start | Where-Object { $_ -eq '--verbose' }).Count) | Should Be 1
        (@($stop | Where-Object { $_ -eq '--auth-json' }).Count) | Should Be 0
        (@($status | Where-Object { $_ -eq '--auth-json' }).Count) | Should Be 0
        (@($stop | Where-Object { $_ -eq '--telemetry-enabled' }).Count) | Should Be 0
        (@($status | Where-Object { $_ -eq '--dashboard-enabled' }).Count) | Should Be 0
    }

    It 'does not inherit host or port from an external config object' {
        $externalConfig = [pscustomobject]@{ Host = '0.0.0.0'; Port = 19090 }
        $start = New-BridgeRuntimeArguments -Config $script:RuntimeTestConfig -Verb 'start' -IncludeAuthJson

        (@($start | Where-Object { $_ -eq $externalConfig.Host }).Count) | Should Be 0
        (@($start | Where-Object { $_ -eq ([string]$externalConfig.Port) }).Count) | Should Be 0
        (Get-TestArgumentValue -Arguments $start -Flag '--host') | Should Be $script:RuntimeTestConfig.BridgeHost
        (Get-TestArgumentValue -Arguments $start -Flag '--port') | Should Be ([string]$script:RuntimeTestConfig.BridgePort)
    }

    It 'uses the direct monitor binary without reintroducing the uvx package wrapper' {
        $directConfig = [pscustomobject]@{
            BridgeExecutablePath = 'E:\Projects\agent-bridge\agent-bridge\monitor\bin\openai-api-server-via-codex.exe'
            BridgePackageSpec = 'openai-api-server-via-codex==0.2.0'
            BridgeCommand = 'openai-api-server-via-codex'
            BridgeHost = '127.0.0.1'
            BridgePort = 18080
            RunDirectory = 'C:\temp\codex-bridge-run'
            PidFilePath = 'C:\temp\codex-bridge-run\server.pid'
            LogFilePath = 'C:\temp\codex-bridge-run\server.log'
            AuthJsonPath = 'C:\Users\hasee\.codex\auth.json'
        }

        $start = New-BridgeRuntimeArguments -Config $directConfig -Verb 'start' -IncludeAuthJson -IncludeVerbose

        $start[0] | Should Be 'start'
        (@($start | Where-Object { $_ -eq '--from' }).Count) | Should Be 0
        (@($start | Where-Object { $_ -eq $directConfig.BridgeCommand }).Count) | Should Be 0
        (Get-TestArgumentValue -Arguments $start -Flag '--host') | Should Be $directConfig.BridgeHost
        (Get-TestArgumentValue -Arguments $start -Flag '--auth-json') | Should Be $directConfig.AuthJsonPath
    }

    It 'keeps the configured PID and log paths under the configured run directory' {
        ([IO.Path]::GetDirectoryName($script:RuntimeTestConfig.PidFilePath)) | Should Be $script:RuntimeTestConfig.RunDirectory
        ([IO.Path]::GetDirectoryName($script:RuntimeTestConfig.LogFilePath)) | Should Be $script:RuntimeTestConfig.RunDirectory
        ([IO.Path]::GetFileName($script:RuntimeTestConfig.PidFilePath)) | Should Be 'server-127.0.0.1-18080.pid'
        ([IO.Path]::GetFileName($script:RuntimeTestConfig.LogFilePath)) | Should Be 'server-127.0.0.1-18080.log'
    }
}

Describe 'Codex Bridge launcher' {
    It 'uses hidden non-blocking Run and WMI confirmation instead of Exec' {
        $launcher = Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\Launch.vbs') -Raw

        (@($launcher | Select-String -Pattern 'shellObject\.Run\(fullCommandLine, 0, False\)').Count) | Should Be 1
        (@($launcher | Select-String -Pattern 'shellObject\.Exec|child\.Status').Count) | Should Be 0
        (@($launcher | Select-String -Pattern 'ControllerIsRunning').Count) | Should BeGreaterThan 0
    }
}
