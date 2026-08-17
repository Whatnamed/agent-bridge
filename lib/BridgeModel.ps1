# Pure bridge ownership, fingerprint, observed-state, and presentation model.
# This file intentionally performs no CIM, networking, process, or file I/O.

function Get-BridgeRecordValue {
    param(
        [Parameter(Mandatory)] $Record,
        [Parameter(Mandatory)] [string] $Name
    )

    if ($null -eq $Record) { return $null }
    $property = $Record.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Get-BridgeCreationStamp {
    param($Value)

    if ($null -eq $Value) { return $null }
    if ($Value -is [DateTime]) {
        return $Value.ToUniversalTime().ToString('o', [Globalization.CultureInfo]::InvariantCulture)
    }
    return ([string]$Value).Trim()
}

function New-ProcessFingerprint {
    param([Parameter(Mandatory)] $Record)

    $processId = 0
    [void][int]::TryParse(([string](Get-BridgeRecordValue -Record $Record -Name 'ProcessId')), [ref]$processId)
    if ($processId -le 0) { return $null }

    $parentProcessId = 0
    [void][int]::TryParse(([string](Get-BridgeRecordValue -Record $Record -Name 'ParentProcessId')), [ref]$parentProcessId)

    [pscustomobject]@{
        ProcessId = $processId
        ParentProcessId = $parentProcessId
        CreationDate = Get-BridgeCreationStamp (Get-BridgeRecordValue -Record $Record -Name 'CreationDate')
        Name = [string](Get-BridgeRecordValue -Record $Record -Name 'Name')
        ExecutablePath = [string](Get-BridgeRecordValue -Record $Record -Name 'ExecutablePath')
        CommandLine = [string](Get-BridgeRecordValue -Record $Record -Name 'CommandLine')
    }
}

function Test-ProcessFingerprintComplete {
    param([Parameter(Mandatory)] $Fingerprint)

    return $null -ne $Fingerprint -and
        [int]$Fingerprint.ProcessId -gt 0 -and
        -not [string]::IsNullOrWhiteSpace([string]$Fingerprint.CreationDate) -and
        -not [string]::IsNullOrWhiteSpace([string]$Fingerprint.Name) -and
        -not [string]::IsNullOrWhiteSpace([string]$Fingerprint.ExecutablePath) -and
        -not [string]::IsNullOrWhiteSpace([string]$Fingerprint.CommandLine)
}

function Test-ProcessFingerprintMatch {
    param(
        [Parameter(Mandatory)] $Expected,
        [Parameter(Mandatory)] $Actual
    )

    # Raw CIM records and fingerprints both have ProcessId. Always normalize
    # both sides so DateTime/DTD string representation cannot create a false
    # PID-reuse mismatch.
    $expectedFingerprint = New-ProcessFingerprint -Record $Expected
    $actualFingerprint = New-ProcessFingerprint -Record $Actual
    if (-not (Test-ProcessFingerprintComplete -Fingerprint $expectedFingerprint) -or
        -not (Test-ProcessFingerprintComplete -Fingerprint $actualFingerprint)) {
        return $false
    }

    return [int]$expectedFingerprint.ProcessId -eq [int]$actualFingerprint.ProcessId -and
        [string]$expectedFingerprint.CreationDate -eq [string]$actualFingerprint.CreationDate -and
        [string]$expectedFingerprint.Name -ieq [string]$actualFingerprint.Name -and
        [string]$expectedFingerprint.ExecutablePath -ieq [string]$actualFingerprint.ExecutablePath -and
        [string]$expectedFingerprint.CommandLine -eq [string]$actualFingerprint.CommandLine
}

function Test-BridgeIdentity {
    param(
        [Parameter(Mandatory)] $Record,
        [Parameter(Mandatory)] $Config
    )

    $name = [string](Get-BridgeRecordValue -Record $Record -Name 'Name')
    $path = [string](Get-BridgeRecordValue -Record $Record -Name 'ExecutablePath')
    $commandLine = [string](Get-BridgeRecordValue -Record $Record -Name 'CommandLine')
    $executableName = [string]$Config.BridgeExecutableName
    $identityMarker = [string]$Config.BridgeIdentityMarker

    if ($name -ieq $executableName -or $name -ieq [string]$Config.BridgePackage) {
        return $true
    }

    if (-not [string]::IsNullOrWhiteSpace($path)) {
        try {
            if ([IO.Path]::GetFileName($path) -ieq $executableName) { return $true }
        } catch { }
    }

    if ([string]::IsNullOrWhiteSpace($identityMarker) -or [string]::IsNullOrWhiteSpace($commandLine)) {
        return $false
    }

    $identityPattern = '(?i)(^|[\s"/\\])' + [regex]::Escape($identityMarker) + '(?:\.exe)?(?=([\s"/\\]|$))'
    return $commandLine -match $identityPattern
}

function Test-BridgeSupervisorIdentity {
    param(
        [Parameter(Mandatory)] $Record,
        [Parameter(Mandatory)] $Config
    )

    if (-not (Test-BridgeIdentity -Record $Record -Config $Config)) { return $false }
    $marker = [string]$Config.BridgeSupervisorCommandMarker
    $commandLine = [string](Get-BridgeRecordValue -Record $Record -Name 'CommandLine')
    if ([string]::IsNullOrWhiteSpace($marker) -or [string]::IsNullOrWhiteSpace($commandLine)) { return $false }
    return $commandLine -match ('(?i)(^|[\s"/\\])' + [regex]::Escape($marker) + '(?=([\s"/\\]|$))')
}

function Get-BridgeRecordByPid {
    param(
        [object[]] $Records = @(),
        [int] $ProcessId
    )

    if ($ProcessId -le 0) { return $null }
    return @($Records | Where-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'ProcessId') -eq $ProcessId } | Select-Object -First 1)
}

function Get-BridgeDescendantRecords {
    param(
        [object[]] $Records = @(),
        [Parameter(Mandatory)] [int] $RootPid
    )

    $childrenByParent = @{}
    foreach ($record in @($Records)) {
        $processId = 0
        $parentProcessId = 0
        [void][int]::TryParse(([string](Get-BridgeRecordValue -Record $record -Name 'ProcessId')), [ref]$processId)
        [void][int]::TryParse(([string](Get-BridgeRecordValue -Record $record -Name 'ParentProcessId')), [ref]$parentProcessId)
        if ($processId -le 0 -or $parentProcessId -le 0) { continue }
        if (-not $childrenByParent.ContainsKey($parentProcessId)) {
            $childrenByParent[$parentProcessId] = [System.Collections.Generic.List[object]]::new()
        }
        $childrenByParent[$parentProcessId].Add($record)
    }

    $seen = [System.Collections.Generic.HashSet[int]]::new()
    $queue = [System.Collections.Generic.Queue[int]]::new()
    $result = [System.Collections.Generic.List[object]]::new()
    [void]$seen.Add($RootPid)
    $queue.Enqueue($RootPid)

    while ($queue.Count -gt 0) {
        $parentPid = $queue.Dequeue()
        if (-not $childrenByParent.ContainsKey($parentPid)) { continue }
        foreach ($child in $childrenByParent[$parentPid]) {
            $childPid = [int](Get-BridgeRecordValue -Record $child -Name 'ProcessId')
            if ($childPid -le 0 -or -not $seen.Add($childPid)) { continue }
            $result.Add($child)
            $queue.Enqueue($childPid)
        }
    }

    return @($result)
}

function New-OwnershipEdges {
    param([object[]] $OwnedRecords = @())

    $ownedRecordList = @($OwnedRecords | Where-Object { $null -ne $_ })
    $ownedPids = [System.Collections.Generic.HashSet[int]]::new()
    foreach ($record in $ownedRecordList) {
        [void]$ownedPids.Add([int](Get-BridgeRecordValue -Record $record -Name 'ProcessId'))
    }

    return @(
        foreach ($record in $ownedRecordList) {
            $ownedProcessId = [int](Get-BridgeRecordValue -Record $record -Name 'ProcessId')
            $parentPid = [int](Get-BridgeRecordValue -Record $record -Name 'ParentProcessId')
            if ($parentPid -gt 0 -and $ownedPids.Contains($parentPid)) {
                [pscustomobject]@{ ParentProcessId = $parentPid; ChildProcessId = $ownedProcessId }
            }
        }
    )
}

function Resolve-BridgeOwnership {
    param(
        [object[]] $ProcessRecords = @(),
        [bool] $PidFilePresent = $false,
        [Nullable[int]] $PidFilePid = $null,
        [object[]] $ListenerRecords = @(),
        [Parameter(Mandatory)] $Config,
        [bool] $ProcessObservationComplete = $true,
        [bool] $ListenerObservationComplete = $true
    )

    $records = @($ProcessRecords | Where-Object { $null -ne $_ } | Sort-Object @{Expression={ [int](Get-BridgeRecordValue -Record $_ -Name 'ProcessId') }} -Unique)
    $pidRecord = if ($PidFilePid) { Get-BridgeRecordByPid -Records $records -ProcessId ([int]$PidFilePid) } else { $null }

    $pidFileState = if (-not $PidFilePresent) {
        'Missing'
    } elseif (-not $PidFilePid -or [int]$PidFilePid -le 0) {
        'Ambiguous'
    } elseif ($null -eq $pidRecord) {
        'StaleMissingProcess'
    } elseif (Test-BridgeSupervisorIdentity -Record $pidRecord -Config $Config) {
        'Owned'
    } elseif (Test-BridgeIdentity -Record $pidRecord -Config $Config) {
        'Ambiguous'
    } else {
        'StaleReusedPid'
    }

    $rootVerified = $pidFileState -eq 'Owned'
    $descendantRecords = if ($rootVerified) {
        @(Get-BridgeDescendantRecords -Records $records -RootPid ([int]$PidFilePid))
    } else {
        @()
    }
    $ownedRecordList = [System.Collections.Generic.List[object]]::new()
    if ($rootVerified -and $pidRecord) { $ownedRecordList.Add($pidRecord) }
    foreach ($descendantRecord in $descendantRecords) {
        if (Test-BridgeIdentity -Record $descendantRecord -Config $Config) {
            $ownedRecordList.Add($descendantRecord)
        }
    }
    $ownedRecords = @($ownedRecordList | Sort-Object @{Expression={ [int](Get-BridgeRecordValue -Record $_ -Name 'ProcessId') }} -Unique)

    $ownedPids = @($ownedRecords | ForEach-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'ProcessId') } | Sort-Object -Unique)
    $listenerRecordsNormalized = @($ListenerRecords | Where-Object { $null -ne $_ })
    $listenerPids = @($listenerRecordsNormalized | ForEach-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'OwningProcess') } | Where-Object { $_ -gt 0 } | Sort-Object -Unique)
    $ownedListenerRecords = @($listenerRecordsNormalized | Where-Object { $ownedPids -contains [int](Get-BridgeRecordValue -Record $_ -Name 'OwningProcess') })
    $foreignListenerRecords = @($listenerRecordsNormalized | Where-Object { $ownedPids -notcontains [int](Get-BridgeRecordValue -Record $_ -Name 'OwningProcess') })
    $foreignListenerPids = @($foreignListenerRecords | ForEach-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'OwningProcess') } | Sort-Object -Unique)

    $listenerOwnerRecords = @(
        foreach ($listenerPid in $listenerPids) {
            $listenerOwner = Get-BridgeRecordByPid -Records $records -ProcessId $listenerPid
            if ($listenerOwner) { $listenerOwner }
        }
    )
    $unverifiedBridgeListenerPids = @(
        foreach ($listenerOwner in $listenerOwnerRecords) {
            $listenerOwnerPid = [int](Get-BridgeRecordValue -Record $listenerOwner -Name 'ProcessId')
            if ($ownedPids -notcontains $listenerOwnerPid -and (Test-BridgeIdentity -Record $listenerOwner -Config $Config)) {
                $listenerOwnerPid
            }
        }
    ) | Sort-Object -Unique
    $unknownForeignListenerPids = @($foreignListenerPids | Where-Object { $unverifiedBridgeListenerPids -notcontains $_ })
    $hasIdentifiableBridgeEvidence =
        ($pidRecord -and (Test-BridgeIdentity -Record $pidRecord -Config $Config)) -or
        (@($listenerOwnerRecords | Where-Object { Test-BridgeIdentity -Record $_ -Config $Config }).Count -gt 0) -or
        ($ownedRecords.Count -gt 0)

    $fingerprints = @($ownedRecords | ForEach-Object { New-ProcessFingerprint -Record $_ })
    $fingerprintsComplete = $fingerprints.Count -gt 0 -and @($fingerprints | Where-Object { -not (Test-ProcessFingerprintComplete -Fingerprint $_) }).Count -eq 0
    $canForceStop = $rootVerified -and
        $ProcessObservationComplete -and
        $ListenerObservationComplete -and
        $fingerprintsComplete -and
        $foreignListenerPids.Count -eq 0

    [pscustomobject]@{
        PidFilePresent = $PidFilePresent
        PidFilePid = if ($PidFilePid) { [int]$PidFilePid } else { $null }
        PidFileState = $pidFileState
        PidFileRecord = $pidRecord
        OwnershipRoot = if ($rootVerified) { $pidRecord } else { $null }
        OwnershipRootFingerprint = if ($rootVerified) { New-ProcessFingerprint -Record $pidRecord } else { $null }
        RootVerified = $rootVerified
        DescendantRecords = @($descendantRecords)
        OwnedProcessRecords = @($ownedRecords)
        OwnedProcessFingerprints = @($fingerprints)
        OwnedProcessPids = @($ownedPids)
        OwnershipEdges = @(New-OwnershipEdges -OwnedRecords $ownedRecords)
        ListenerRecords = @($listenerRecordsNormalized)
        ListenerPids = @($listenerPids)
        OwnedListenerRecords = @($ownedListenerRecords)
        OwnedListenerPids = @($ownedListenerRecords | ForEach-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'OwningProcess') } | Sort-Object -Unique)
        ForeignListenerRecords = @($foreignListenerRecords)
        ForeignListenerPids = @($foreignListenerPids)
        UnverifiedBridgeListenerPids = @($unverifiedBridgeListenerPids)
        UnknownForeignListenerPids = @($unknownForeignListenerPids)
        ListenerOwnerRecords = @($listenerOwnerRecords)
        HasIdentifiableBridgeEvidence = [bool]$hasIdentifiableBridgeEvidence
        ProcessObservationComplete = $ProcessObservationComplete
        ListenerObservationComplete = $ListenerObservationComplete
        FingerprintsComplete = $fingerprintsComplete
        CanAttemptOfficialStop = [bool]$hasIdentifiableBridgeEvidence
        CanForceStop = [bool]$canForceStop
    }
}

function New-OwnershipSnapshot {
    param([Parameter(Mandatory)] $Topology)

    if (-not $Topology.RootVerified -or -not $Topology.ProcessObservationComplete -or -not $Topology.ListenerObservationComplete) {
        return $null
    }

    [pscustomobject]@{
        CapturedAt = [DateTimeOffset]::UtcNow
        PidFilePid = $Topology.PidFilePid
        RootFingerprint = $Topology.OwnershipRootFingerprint
        ProcessFingerprints = @($Topology.OwnedProcessFingerprints)
        OwnershipEdges = @($Topology.OwnershipEdges)
        ListenerPids = @($Topology.ListenerPids)
        OwnedListenerPids = @($Topology.OwnedListenerPids)
        ForeignListenerPids = @($Topology.ForeignListenerPids)
        CanForceStopAtCapture = [bool]$Topology.CanForceStop
    }
}

function Get-ObservedBridgeState {
    param(
        [Parameter(Mandatory)] $Topology,
        [bool] $HealthKnown = $true,
        [bool] $HealthOk = $false,
        [bool] $ObservationComplete = $true
    )

    if (-not $ObservationComplete -or -not $HealthKnown -or
        -not $Topology.ProcessObservationComplete -or -not $Topology.ListenerObservationComplete) {
        return 'Unknown'
    }
    if (@($Topology.ForeignListenerPids).Count -gt 0) { return 'Conflict' }
    if ($HealthOk -and $Topology.RootVerified -and @($Topology.OwnedListenerPids).Count -gt 0) {
        return 'Running'
    }
    if (-not $HealthOk -and @($Topology.ListenerPids).Count -eq 0 -and
        @($Topology.OwnedProcessPids).Count -eq 0 -and
        $Topology.PidFileState -in @('Missing','StaleMissingProcess','StaleReusedPid')) {
        return 'Stopped'
    }
    if ($Topology.HasIdentifiableBridgeEvidence -or $Topology.PidFileState -eq 'Ambiguous') {
        return 'Degraded'
    }
    return 'Stopped'
}

function Get-BridgePresentation {
    param(
        [ValidateSet('Idle','Starting','Stopping','Restarting')]
        [string] $OperationState = 'Idle',
        [ValidateSet('Running','Stopped','Degraded','Conflict','Unknown')]
        [string] $ObservedState = 'Unknown',
        [bool] $CanAttemptOfficialStop = $false
    )

    if ($OperationState -ne 'Idle') {
        $displayText = switch ($OperationState) {
            'Starting' { '启动中' }
            'Stopping' { '停止中' }
            'Restarting' { '重启中' }
        }
        $tooltip = switch ($OperationState) {
            'Starting' { 'Codex Bridge — Starting' }
            'Stopping' { 'Codex Bridge — Stopping' }
            'Restarting' { 'Codex Bridge — Restarting' }
        }
        return [pscustomobject]@{
            DisplayText = $displayText
            Tooltip = $tooltip
            StartEnabled = $false
            StopEnabled = $false
            RestartEnabled = $false
        }
    }

    switch ($ObservedState) {
        'Running' {
            return [pscustomobject]@{
                DisplayText = '已运行'
                Tooltip = 'Codex Bridge — Running'
                StartEnabled = $false
                StopEnabled = $true
                RestartEnabled = $true
            }
        }
        'Stopped' {
            return [pscustomobject]@{
                DisplayText = '已停止'
                Tooltip = 'Codex Bridge — Stopped'
                StartEnabled = $true
                StopEnabled = $false
                RestartEnabled = $false
            }
        }
        'Conflict' {
            return [pscustomobject]@{
                DisplayText = '端口冲突'
                Tooltip = 'Codex Bridge — Port conflict'
                StartEnabled = $false
                StopEnabled = $CanAttemptOfficialStop
                RestartEnabled = $false
            }
        }
        'Degraded' {
            return [pscustomobject]@{
                DisplayText = '异常'
                Tooltip = 'Codex Bridge — Degraded'
                StartEnabled = $false
                StopEnabled = $CanAttemptOfficialStop
                RestartEnabled = $false
            }
        }
        default {
            return [pscustomobject]@{
                DisplayText = '检查中'
                Tooltip = 'Codex Bridge — Checking'
                StartEnabled = $false
                StopEnabled = $false
                RestartEnabled = $false
            }
        }
    }
}
