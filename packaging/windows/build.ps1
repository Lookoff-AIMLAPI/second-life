# Builds the Windows installer. Needs Go (unless -C2cBin/-TrayBin are given) and makensis.
param(
  [string]$Version = "0.0.0-dev",
  [string]$C2cBin = "",
  [string]$TrayBin = "",
  [string]$Dist = ""
)
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$root = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$Version = $Version -replace '^v', ''
if ($Version -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') {
  throw "Version must look like 1.2.3 or 1.2.3-suffix, got: $Version"
}
if (($C2cBin -eq "") -ne ($TrayBin -eq "")) {
  throw "Give both -C2cBin and -TrayBin, or neither"
}
if ($Dist -eq "") {
  $Dist = Join-Path $root "dist"
}

$bin = Join-Path $Dist "windows-amd64"
New-Item -ItemType Directory -Force -Path $bin | Out-Null

if ($C2cBin -eq "") {
  $env:GOOS = "windows"
  $env:GOARCH = "amd64"
  $env:CGO_ENABLED = "0"
  Push-Location $root
  try {
    go build -trimpath -ldflags "-s -w -X github.com/Lookoff-AIMLAPI/second-life/internal/bridge.Version=$Version" -o (Join-Path $bin "slife.exe") ./cmd/slife
    if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/slife failed" }
    go build -trimpath -ldflags "-s -w -X github.com/Lookoff-AIMLAPI/second-life/internal/bridge.Version=$Version -H windowsgui" -o (Join-Path $bin "slife-tray.exe") ./cmd/slife-tray
    if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/slife-tray failed" }
  } finally {
    Pop-Location
  }
} else {
  Copy-Item -LiteralPath $C2cBin -Destination (Join-Path $bin "slife.exe") -Force
  Copy-Item -LiteralPath $TrayBin -Destination (Join-Path $bin "slife-tray.exe") -Force
}

$makensis = Get-Command makensis -ErrorAction SilentlyContinue
if ($makensis) {
  $makensisPath = $makensis.Source
} else {
  $makensisPath = Join-Path ${env:ProgramFiles(x86)} "NSIS\makensis.exe"
}
if (-not (Test-Path -LiteralPath $makensisPath)) {
  throw "makensis not found"
}

$out = Join-Path $Dist "Second-Life-$Version-windows-x64-setup.exe"
$numeric = ($Version -split '-')[0]
& $makensisPath /V3 "/DVERSION=$Version" "/DVERSION_NUM=$numeric" "/DBIN_DIR=$bin" "/DOUT_FILE=$out" (Join-Path $PSScriptRoot "installer.nsi")
if ($LASTEXITCODE -ne 0) { throw "makensis failed" }

Write-Output "installer: $out"
(Get-FileHash -Algorithm SHA256 -LiteralPath $out).Hash
