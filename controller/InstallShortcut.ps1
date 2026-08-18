#requires -Version 7.0

[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$controllerDirectory = Split-Path -Parent $MyInvocation.MyCommand.Path
$launchPath = Join-Path $controllerDirectory 'Launch.vbs'
$desktopPath = [Environment]::GetFolderPath('Desktop')
$shortcutPath = Join-Path $desktopPath 'Codex Bridge.lnk'
$wscriptPath = Join-Path $env:WINDIR 'System32\wscript.exe'

if (-not (Test-Path -LiteralPath $launchPath -PathType Leaf)) {
    throw "找不到启动器：$launchPath"
}
if (-not (Test-Path -LiteralPath $desktopPath -PathType Container)) {
    throw "找不到桌面目录：$desktopPath"
}
if (-not (Test-Path -LiteralPath $wscriptPath -PathType Leaf)) {
    throw "找不到 Windows Script Host：$wscriptPath"
}

$shell = New-Object -ComObject WScript.Shell
if (Test-Path -LiteralPath $shortcutPath -PathType Leaf) {
    $existing = $shell.CreateShortcut($shortcutPath)
    $existingTarget = [string]$existing.TargetPath
    $existingArguments = [string]$existing.Arguments
    $isOwnedByController =
        ($existingTarget -ieq $wscriptPath) -and
        ($existingArguments -like "*$launchPath*")
    if (-not $isOwnedByController) {
        throw "桌面已存在非本控制器的同名快捷方式，未覆盖：$shortcutPath"
    }
}

$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = $wscriptPath
$shortcut.Arguments = '"' + $launchPath + '"'
$shortcut.WorkingDirectory = $controllerDirectory
$shortcut.Description = 'Start the Codex Bridge tray controller'
$shortcut.IconLocation = "$env:SystemRoot\System32\shell32.dll,44"
$shortcut.Save()

[pscustomobject]@{
    Shortcut = $shortcutPath
    Target = $shortcut.TargetPath
    Arguments = $shortcut.Arguments
    ControllerDirectory = $controllerDirectory
} | Format-List
