Option Explicit

Dim fso, shell, controllerDirectory, scriptPath, pwshPath, commandLine, launched
Set fso = CreateObject("Scripting.FileSystemObject")
Set shell = CreateObject("WScript.Shell")

controllerDirectory = fso.GetParentFolderName(WScript.ScriptFullName)
scriptPath = fso.BuildPath(controllerDirectory, "CodexBridge.ps1")

If Not fso.FileExists(scriptPath) Then
    MsgBox "Controller script not found:" & vbCrLf & scriptPath, vbCritical, "Codex Bridge"
    WScript.Quit 2
End If

pwshPath = shell.ExpandEnvironmentStrings("%LOCALAPPDATA%\Microsoft\WindowsApps\pwsh.exe")
shell.CurrentDirectory = controllerDirectory

launched = False
If fso.FileExists(pwshPath) Then
    commandLine = ExecutableToken(pwshPath) & " -NoLogo -NoProfile -NonInteractive -STA -ExecutionPolicy Bypass -WindowStyle Hidden -File " & Quote(scriptPath)
    launched = TryLaunch(shell, commandLine, scriptPath)
End If

If Not launched Then
    pwshPath = shell.ExpandEnvironmentStrings("%ProgramFiles%\PowerShell\7\pwsh.exe")
    If fso.FileExists(pwshPath) Then
        commandLine = ExecutableToken(pwshPath) & " -NoLogo -NoProfile -NonInteractive -STA -ExecutionPolicy Bypass -WindowStyle Hidden -File " & Quote(scriptPath)
        launched = TryLaunch(shell, commandLine, scriptPath)
    End If
End If

If Not launched Then
    ' Last resort: use PowerShell 7 resolved through PATH without changing PATH.
    pwshPath = "pwsh.exe"
    commandLine = ExecutableToken(pwshPath) & " -NoLogo -NoProfile -NonInteractive -STA -ExecutionPolicy Bypass -WindowStyle Hidden -File " & Quote(scriptPath)
    launched = TryLaunch(shell, commandLine, scriptPath)
End If

If Not launched Then
    MsgBox "Unable to start PowerShell 7. WindowsApps, ProgramFiles and PATH were all tried.", vbCritical, "Codex Bridge"
    WScript.Quit 3
End If

Function Quote(value)
    Quote = Chr(34) & value & Chr(34)
End Function

Function ExecutableToken(value)
    If InStr(value, "\") > 0 Or InStr(value, " ") > 0 Then
        ExecutableToken = Quote(value)
    Else
        ExecutableToken = value
    End If
End Function

Function TryLaunch(shellObject, fullCommandLine, targetScript)
    Dim child, startError, attempt
    TryLaunch = False
    On Error Resume Next
    Set child = shellObject.Exec(fullCommandLine)
    startError = Err.Number
    Err.Clear
    On Error GoTo 0

    If startError <> 0 Or child Is Nothing Then Exit Function

    ' Confirm the controller process rather than treating a still-running pwsh
    ' launcher as success. This also lets the caller try the next PowerShell 7
    ' location when an alias or installation is unusable.
    For attempt = 1 To 20
        If ControllerIsRunning(targetScript) Then
            TryLaunch = True
            Exit For
        End If
        If child.Status <> 0 Then Exit For
        WScript.Sleep 100
    Next

    Set child = Nothing
End Function

Function ControllerIsRunning(targetScript)
    Dim locator, service, processes, process, commandLine
    ControllerIsRunning = False
    On Error Resume Next
    Set locator = CreateObject("WbemScripting.SWbemLocator")
    Set service = locator.ConnectServer(".", "root\cimv2")
    Set processes = service.ExecQuery("SELECT CommandLine FROM Win32_Process WHERE Name='pwsh.exe'")
    If Err.Number <> 0 Then
        Err.Clear
        On Error GoTo 0
        Exit Function
    End If
    For Each process In processes
        commandLine = process.CommandLine & ""
        If InStr(1, commandLine, targetScript, vbTextCompare) > 0 Then
            ControllerIsRunning = True
            Exit For
        End If
    Next
    On Error GoTo 0
End Function
