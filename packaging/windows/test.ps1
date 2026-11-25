# Installs the given setup silently, checks it, upgrades over a running tray, uninstalls.
# For a disposable machine (CI): it installs into the current user's profile.
param(
  [Parameter(Mandatory = $true)][string]$Setup,
  [Parameter(Mandatory = $true)][string]$Version
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$Version = $Version -replace '^v', ''
$dir = Join-Path $env:LOCALAPPDATA "Programs\SecondLife"
$shortcut = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\Second Life.lnk"
$uninstallKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\SecondLife"
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$files = @("slife.exe", "slife-tray.exe", "icon.ico", "uninstall.exe")

function Assert([bool]$Condition, [string]$Message) {
  if (-not $Condition) { throw "FAILED: $Message" }
  Write-Output "ok: $Message"
}

function Install([string[]]$Extra = @()) {
  $p = Start-Process -FilePath $Setup -ArgumentList (@("/S") + $Extra) -Wait -PassThru
  Assert ($p.ExitCode -eq 0) "silent install exits 0 (got $($p.ExitCode))"
}

$Setup = (Resolve-Path -LiteralPath $Setup).Path
$elsewhere = Join-Path $env:TEMP "slife-elsewhere"
$pathBefore = [Environment]::GetEnvironmentVariable("Path", "User")

# Install. /D= must be ignored: the folder is fixed.
Install @("/D=$elsewhere")
Assert (-not (Test-Path -LiteralPath $elsewhere)) "/D= is ignored"
foreach ($f in $files) {
  Assert (Test-Path -LiteralPath (Join-Path $dir $f)) "$f installed"
}
Assert (Test-Path -LiteralPath $shortcut) "Start Menu shortcut"
$entry = Get-ItemProperty -LiteralPath $uninstallKey
Assert ($entry.DisplayVersion -eq $Version) "uninstall entry has version $Version"
Assert ([Environment]::GetEnvironmentVariable("Path", "User") -eq $pathBefore) "user PATH unchanged"

$output = (& (Join-Path $dir "slife.exe") version | Out-String).Trim()
Assert ($LASTEXITCODE -eq 0) "slife.exe version exits 0"
Write-Output "slife.exe version: $output"
Assert ($output.Contains($Version)) "slife.exe reports $Version"

# The tray must be a GUI program: PE subsystem 2.
$bytes = [IO.File]::ReadAllBytes((Join-Path $dir "slife-tray.exe"))
$pe = [BitConverter]::ToInt32($bytes, 0x3C)
Assert ([BitConverter]::ToUInt16($bytes, $pe + 0x5C) -eq 2) "slife-tray.exe is a GUI program"

# Upgrade over a running tray.
$tray = Start-Process -FilePath (Join-Path $dir "slife-tray.exe") -PassThru
Start-Sleep -Seconds 3
$wasRunning = -not $tray.HasExited
Install
if ($wasRunning) {
  Assert ($tray.WaitForExit(5000)) "upgrade closed the running tray"
} else {
  Write-Output "::warning::slife-tray.exe exited on its own, the close-before-overwrite check did not run"
}

# A running slife.exe is not killed: a silent upgrade stops with code 3. Holding the
# file open stands in for a running process.
$lock = [IO.File]::Open((Join-Path $dir "slife.exe"), "Open", "Read", "Read")
$p = Start-Process -FilePath $Setup -ArgumentList "/S" -Wait -PassThru
$lock.Dispose()
Assert ($p.ExitCode -eq 3) "silent upgrade over a locked slife.exe exits 3 (got $($p.ExitCode))"

# Uninstall. Foreign files and other Run values must survive.
if (-not (Test-Path -LiteralPath $runKey)) { New-Item -Path $runKey | Out-Null }
New-ItemProperty -LiteralPath $runKey -Name "SecondLife" -Value "x" -Force | Out-Null
New-ItemProperty -LiteralPath $runKey -Name "SecondLifeTestOther" -Value "x" -Force | Out-Null
$codexHome = Join-Path $env:USERPROFILE ".codex"
New-Item -ItemType Directory -Force -Path $codexHome | Out-Null
$codexFile = Join-Path $codexHome "slife-test.txt"
$foreign = Join-Path $dir "foreign.txt"
Set-Content -LiteralPath $codexFile -Value "keep"
Set-Content -LiteralPath $foreign -Value "keep"

# The uninstaller copies itself to TEMP and returns at once, so poll.
Start-Process -FilePath (Join-Path $dir "uninstall.exe") -ArgumentList "/S" -Wait
$deadline = (Get-Date).AddSeconds(60)
while ((Test-Path -LiteralPath $uninstallKey) -and ((Get-Date) -lt $deadline)) { Start-Sleep -Milliseconds 500 }

Assert (-not (Test-Path -LiteralPath $uninstallKey)) "uninstall entry removed"
foreach ($f in $files) {
  Assert (-not (Test-Path -LiteralPath (Join-Path $dir $f))) "$f removed"
}
Assert (-not (Test-Path -LiteralPath $shortcut)) "Start Menu shortcut removed"
$run = Get-Item -LiteralPath $runKey
Assert ($run.GetValueNames() -notcontains "SecondLife") "Run value removed"
Assert ($run.GetValueNames() -contains "SecondLifeTestOther") "other Run values kept"
Assert (Test-Path -LiteralPath $foreign) "foreign file in the install folder kept"
Assert (Test-Path -LiteralPath $codexFile) "~/.codex kept"

# With nothing foreign left, the folder goes too.
Remove-Item -LiteralPath $foreign, $codexFile
Remove-ItemProperty -LiteralPath $runKey -Name "SecondLifeTestOther"
Install
Start-Process -FilePath (Join-Path $dir "uninstall.exe") -ArgumentList "/S" -Wait
$deadline = (Get-Date).AddSeconds(60)
while ((Test-Path -LiteralPath $dir) -and ((Get-Date) -lt $deadline)) { Start-Sleep -Milliseconds 500 }
Assert (-not (Test-Path -LiteralPath $dir)) "install folder removed"
