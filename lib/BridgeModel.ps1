# Pure bridge ownership, fingerprint, observed-state, and presentation model.
# This file intentionally performs no CIM, networking, process, or file I/O.

function Get-BridgeRecordValue {
    param(
        $Record,
        [Parameter(Mandatory)] [string] $Name
    )

    if ($null -eq $Record) { return $null }
    $property = $Record.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function New-BridgeRuntimeArguments {
    param(
        [Parameter(Mandatory)] $Config,
        [ValidateSet('start','stop','status')]
        [string] $Verb,
        [switch] $IncludeAuthJson,
        [switch] $IncludeVerbose
    )

    $usesDirectBinary = -not [string]::IsNullOrWhiteSpace([string]$Config.BridgeExecutablePath)
    if ($usesDirectBinary) {
        $arguments = @($Verb)
    } else {
        $arguments = @(
            '--from', [string]$Config.BridgePackageSpec,
            [string]$Config.BridgeCommand,
            $Verb
        )
    }
    $arguments += @(
        '--host', [string]$Config.BridgeHost,
        '--port', [string]$Config.BridgePort,
        '--state-dir', [string]$Config.RunDirectory,
        '--pid-file', [string]$Config.PidFilePath,
        '--log-file', [string]$Config.LogFilePath
    )
    if ($IncludeAuthJson) { $arguments += @('--auth-json', [string]$Config.AuthJsonPath) }
    if ($IncludeVerbose) { $arguments += '--verbose' }
    return [string[]]$arguments
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

function Get-BridgeFingerprintDepth {
    param(
        [Parameter(Mandatory)] $Fingerprint,
        [hashtable] $ByPid = @{}
    )

    $depth = 0
    $currentProcessId = [int](Get-BridgeRecordValue -Record $Fingerprint -Name 'ProcessId')
    $seen = [System.Collections.Generic.HashSet[int]]::new()
    while ($ByPid.ContainsKey($currentProcessId) -and $seen.Add($currentProcessId)) {
        $parentProcessId = [int](Get-BridgeRecordValue -Record $ByPid[$currentProcessId] -Name 'ParentProcessId')
        if ($parentProcessId -le 0 -or $parentProcessId -eq $currentProcessId -or -not $ByPid.ContainsKey($parentProcessId)) { break }
        $depth++
        $currentProcessId = $parentProcessId
    }
    return $depth
}

function Get-FallbackActionPlan {
    param(
        [Parameter(Mandatory)] $OwnershipSnapshot,
        [object[]] $LiveRecords = @(),
        [bool] $SupervisorLive = $false,
        [object[]] $ListenerRecords = @(),
        [Parameter(Mandatory)] $Config
    )

    $emptyPlan = {
        param([string]$Reason)
        [pscustomobject]@{
            Allowed = $false
            Refused = $true
            Reason = $Reason
            RootPid = $null
            SupervisorLive = $SupervisorLive
            VerifiedRecords = @()
            VerifiedFingerprints = @()
            TargetRecords = @()
            TargetPids = @()
        }
    }

    if (-not $OwnershipSnapshot -or -not $OwnershipSnapshot.CanForceStopAtCapture -or
        -not $OwnershipSnapshot.RootFingerprint -or
        [string]$OwnershipSnapshot.TargetInstanceAffinity -notin @('Bound','HistoricalBound') -or
        @($OwnershipSnapshot.ForeignListenerPids).Count -gt 0) {
        return & $emptyPlan 'ownership snapshot 未确认属于目标 bridge instance。'
    }

    $rootPid = [int](Get-BridgeRecordValue -Record $OwnershipSnapshot.RootFingerprint -Name 'ProcessId')
    if ($rootPid -le 0) { return & $emptyPlan 'ownership snapshot 缺少有效 supervisor PID。' }

    $records = @($LiveRecords | Where-Object { $null -ne $_ })
    $rootLive = Get-BridgeRecordByPid -Records $records -ProcessId $rootPid
    if ($SupervisorLive -and -not $rootLive) { return & $emptyPlan "verified supervisor PID $rootPid 不在当前 live records 中。" }
    if (-not $SupervisorLive -and $rootLive) { return & $emptyPlan "supervisor PID $rootPid 的 live 状态与 fallback action plan 不一致。" }

    $verifiedRecords = [System.Collections.Generic.List[object]]::new()
    $verifiedFingerprints = [System.Collections.Generic.List[object]]::new()
    $allowedTreePids = [System.Collections.Generic.HashSet[int]]::new()

    if ($SupervisorLive) {
        if (-not (Test-ProcessFingerprintMatch -Expected $OwnershipSnapshot.RootFingerprint -Actual $rootLive) -or
            -not (Test-BridgeSupervisorIdentity -Record $rootLive -Config $Config)) {
            return & $emptyPlan "supervisor PID $rootPid 的身份或 fingerprint 未通过复核。"
        }

        $verifiedRecords.Add($rootLive)
        [void]$allowedTreePids.Add($rootPid)
        foreach ($record in @(Get-BridgeDescendantRecords -Records $records -RootPid $rootPid)) {
            if (-not (Test-BridgeIdentity -Record $record -Config $Config)) { continue }
            $fingerprint = New-ProcessFingerprint -Record $record
            if (-not (Test-ProcessFingerprintComplete -Fingerprint $fingerprint)) {
                return & $emptyPlan "当前 verified bridge child PID $([int](Get-BridgeRecordValue -Record $record -Name 'ProcessId')) 的 fingerprint 不完整。"
            }
            $verifiedRecords.Add($record)
            $verifiedFingerprints.Add($fingerprint)
            [void]$allowedTreePids.Add([int](Get-BridgeRecordValue -Record $record -Name 'ProcessId'))
        }
        $rootFingerprint = New-ProcessFingerprint -Record $rootLive
        if (-not (Test-ProcessFingerprintComplete -Fingerprint $rootFingerprint)) {
            return & $emptyPlan "supervisor PID $rootPid 的当前 fingerprint 不完整。"
        }
        $verifiedFingerprints.Insert(0, $rootFingerprint)
        $targetRecords = @($rootLive)
        $targetPids = @($rootPid)
    } else {
        $snapshotByPid = @{}
        foreach ($fingerprint in @($OwnershipSnapshot.ProcessFingerprints)) {
            $fingerprintPid = [int](Get-BridgeRecordValue -Record $fingerprint -Name 'ProcessId')
            if ($fingerprintPid -gt 0) { $snapshotByPid[$fingerprintPid] = $fingerprint }
        }

        foreach ($record in $records) {
            $recordPid = [int](Get-BridgeRecordValue -Record $record -Name 'ProcessId')
            if (-not $snapshotByPid.ContainsKey($recordPid)) { continue }
            $expected = $snapshotByPid[$recordPid]
            if (-not (Test-ProcessFingerprintMatch -Expected $expected -Actual $record) -or
                -not (Test-BridgeIdentity -Record $record -Config $Config)) {
                return & $emptyPlan "snapshot child PID $recordPid 的身份或 fingerprint 已变化。"
            }
            $verifiedRecords.Add($record)
            [void]$allowedTreePids.Add($recordPid)
            $verifiedFingerprints.Add((New-ProcessFingerprint -Record $record))
        }

        $verifiedByPid = @{}
        foreach ($fingerprint in @($OwnershipSnapshot.ProcessFingerprints)) {
            $verifiedByPid[[int](Get-BridgeRecordValue -Record $fingerprint -Name 'ProcessId')] = $fingerprint
        }
        $targetRecords = @(
            $verifiedRecords | Sort-Object @{ Expression = {
                - (Get-BridgeFingerprintDepth -Fingerprint $verifiedByPid[[int](Get-BridgeRecordValue -Record $_ -Name 'ProcessId')] -ByPid $verifiedByPid)
            } }
        )
        $targetPids = @($targetRecords | ForEach-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'ProcessId') } | Sort-Object -Unique)
    }

    foreach ($listenerRecord in @($ListenerRecords | Where-Object { $null -ne $_ })) {
        $listenerPid = [int](Get-BridgeRecordValue -Record $listenerRecord -Name 'OwningProcess')
        if ($listenerPid -le 0 -or -not $allowedTreePids.Contains($listenerPid)) {
            return & $emptyPlan "18080 listener PID $listenerPid 不属于当前 verified bridge ownership。"
        }
    }

    [pscustomobject]@{
        Allowed = $true
        Refused = $false
        Reason = $null
        RootPid = $rootPid
        SupervisorLive = $SupervisorLive
        VerifiedRecords = @($verifiedRecords)
        VerifiedFingerprints = @($verifiedFingerprints)
        TargetRecords = @($targetRecords)
        TargetPids = @($targetPids)
    }
}

function Resolve-BridgeOwnership {
    param(
        [object[]] $ProcessRecords = @(),
        [bool] $PidFilePresent = $false,
        [Nullable[int]] $PidFilePid = $null,
        [object[]] $ListenerRecords = @(),
        [Parameter(Mandatory)] $Config,
        [bool] $ProcessObservationComplete = $true,
        [bool] $ListenerObservationComplete = $true,
        [object] $HistoricalAffinity = $null
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
    $ownedListenerPids = @($ownedListenerRecords | ForEach-Object { [int](Get-BridgeRecordValue -Record $_ -Name 'OwningProcess') } | Sort-Object -Unique)
    $ownedListenerOwnerRecords = @($listenerOwnerRecords | Where-Object {
        $ownedListenerPids -contains [int](Get-BridgeRecordValue -Record $_ -Name 'ProcessId')
    })
    $ownedListenerOwnerFingerprints = @($ownedListenerOwnerRecords | ForEach-Object { New-ProcessFingerprint -Record $_ })
    $ownedListenerOwnerFingerprintsComplete = $ownedListenerPids.Count -gt 0 -and
        $ownedListenerOwnerRecords.Count -eq $ownedListenerPids.Count -and
        @($ownedListenerOwnerFingerprints | Where-Object { -not (Test-ProcessFingerprintComplete -Fingerprint $_) }).Count -eq 0
    $unverifiedBridgeListenerPids = @(
        foreach ($listenerOwner in $listenerOwnerRecords) {
            $listenerOwnerPid = [int](Get-BridgeRecordValue -Record $listenerOwner -Name 'ProcessId')
            if ($ownedPids -notcontains $listenerOwnerPid -and (Test-BridgeIdentity -Record $listenerOwner -Config $Config)) {
                $listenerOwnerPid
            }
        }
    ) | Sort-Object -Unique
    $unknownForeignListenerPids = @($foreignListenerPids | Where-Object { $unverifiedBridgeListenerPids -notcontains $_ })
    $currentTargetBound = $rootVerified -and
        $ownedListenerPids.Count -gt 0 -and
        $ownedListenerOwnerFingerprintsComplete -and
        @($ownedListenerOwnerRecords | Where-Object { Test-BridgeIdentity -Record $_ -Config $Config }).Count -eq $ownedListenerPids.Count

    $historicalStatus = [string](Get-BridgeRecordValue -Record $HistoricalAffinity -Name 'TargetInstanceAffinity')
    $historicalRootFingerprint = Get-BridgeRecordValue -Record $HistoricalAffinity -Name 'TargetAffinityRootFingerprint'
    if (-not $historicalRootFingerprint) { $historicalRootFingerprint = Get-BridgeRecordValue -Record $HistoricalAffinity -Name 'RootFingerprint' }
    $historicalPort = Get-BridgeRecordValue -Record $HistoricalAffinity -Name 'TargetAffinityPort'
    $historicalPortMatches = $null -eq $historicalPort -or [int]$historicalPort -eq [int]$Config.BridgePort
    $historicalOwnerPids = @(
        Get-BridgeRecordValue -Record $HistoricalAffinity -Name 'TargetAffinityOwnerPids' |
            ForEach-Object { [int]$_ } |
            Where-Object { $_ -gt 0 }
    )
    $historicalTargetBound = $rootVerified -and
        -not $currentTargetBound -and
        $foreignListenerPids.Count -eq 0 -and
        $historicalStatus -in @('Bound','HistoricalBound') -and
        $historicalPortMatches -and
        $historicalRootFingerprint -and
        (Test-ProcessFingerprintMatch -Expected $historicalRootFingerprint -Actual $pidRecord)
    $targetInstanceAffinity = if ($foreignListenerPids.Count -gt 0) {
        'Conflict'
    } elseif ($currentTargetBound) {
        'Bound'
    } elseif ($historicalTargetBound) {
        'HistoricalBound'
    } else {
        'Unbound'
    }
    $targetAffinityOwnerPids = if ($targetInstanceAffinity -eq 'Bound') {
        @($ownedListenerPids)
    } elseif ($targetInstanceAffinity -eq 'HistoricalBound') {
        @($historicalOwnerPids)
    } else {
        @()
    }
    $hasIdentifiableBridgeEvidence =
        ($pidRecord -and (Test-BridgeIdentity -Record $pidRecord -Config $Config)) -or
        (@($listenerOwnerRecords | Where-Object { Test-BridgeIdentity -Record $_ -Config $Config }).Count -gt 0) -or
        ($ownedRecords.Count -gt 0)

    $fingerprints = @($ownedRecords | ForEach-Object { New-ProcessFingerprint -Record $_ })
    $fingerprintsComplete = $fingerprints.Count -gt 0 -and @($fingerprints | Where-Object { -not (Test-ProcessFingerprintComplete -Fingerprint $_) }).Count -eq 0
    $canAttemptOfficialStop = $hasIdentifiableBridgeEvidence -and
        $targetInstanceAffinity -in @('Bound','HistoricalBound')
    $canForceStop = $rootVerified -and
        $targetInstanceAffinity -in @('Bound','HistoricalBound') -and
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
        OwnedListenerPids = @($ownedListenerPids)
        OwnedListenerOwnerRecords = @($ownedListenerOwnerRecords)
        OwnedListenerOwnerFingerprints = @($ownedListenerOwnerFingerprints)
        OwnedListenerOwnerFingerprintsComplete = [bool]$ownedListenerOwnerFingerprintsComplete
        ForeignListenerRecords = @($foreignListenerRecords)
        ForeignListenerPids = @($foreignListenerPids)
        UnverifiedBridgeListenerPids = @($unverifiedBridgeListenerPids)
        UnknownForeignListenerPids = @($unknownForeignListenerPids)
        ListenerOwnerRecords = @($listenerOwnerRecords)
        TargetInstanceAffinity = $targetInstanceAffinity
        TargetAffinityRootFingerprint = if ($rootVerified) { New-ProcessFingerprint -Record $pidRecord } else { $null }
        TargetAffinityOwnerPids = @($targetAffinityOwnerPids)
        TargetAffinityPort = [int]$Config.BridgePort
        TargetAffinityReason = switch ($targetInstanceAffinity) {
            'Bound' { '当前 18080 listener owner 属于已验证 supervisor tree。' }
            'HistoricalBound' { '当前 supervisor fingerprint 与此前已绑定的目标 instance 相同，当前 listener 暂时不可见。' }
            'Conflict' { '18080 存在不属于已验证 supervisor tree 的 listener。' }
            default { 'PID file supervisor 未证明属于当前 18080 target instance。' }
        }
        HasIdentifiableBridgeEvidence = [bool]$hasIdentifiableBridgeEvidence
        ProcessObservationComplete = $ProcessObservationComplete
        ListenerObservationComplete = $ListenerObservationComplete
        FingerprintsComplete = $fingerprintsComplete
        CanAttemptOfficialStop = [bool]$canAttemptOfficialStop
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
        TargetInstanceAffinity = [string]$Topology.TargetInstanceAffinity
        TargetAffinityRootFingerprint = $Topology.TargetAffinityRootFingerprint
        TargetAffinityOwnerPids = @($Topology.TargetAffinityOwnerPids)
        TargetAffinityPort = [int]$Topology.TargetAffinityPort
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
    if (@($Topology.ForeignListenerPids).Count -gt 0 -or $Topology.TargetInstanceAffinity -eq 'Conflict') { return 'Conflict' }
    if ($HealthOk -and $Topology.RootVerified -and
        $Topology.TargetInstanceAffinity -eq 'Bound' -and
        @($Topology.OwnedListenerPids).Count -gt 0) {
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
