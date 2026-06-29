param(
  [string]$ServiceName = "mcmon-agent",
  [string]$InstallDir = "$env:ProgramFiles\mcmon-agent",
  [string]$ConfigPath = "$env:ProgramData\mcmon-agent\config.json",
  [string]$Version = "latest",
  [Parameter(Mandatory = $true)][string]$HostUrl,
  [string]$AgentId = "",
  [Parameter(Mandatory = $true)][string]$Token,
  [Parameter(Mandatory = $true)][string]$ConfigBase64
)

$ErrorActionPreference = "Stop"

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw "Please run this installer from an elevated PowerShell session."
}

$repo = "YOUR_PATH/mcmon-agent"
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { "amd64" }
  "ARM64" { "arm64" }
  default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

if ($Version -eq "latest") {
  $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest"
  $Version = $release.tag_name
}
if (-not $Version) {
  throw "Unable to resolve latest version."
}

$binaryName = "mcmon-agent-windows-$arch.exe"
$url = "https://github.com/$repo/releases/download/$Version/$binaryName"
$binPath = Join-Path $InstallDir "mcmon-agent.exe"

Write-Host "Installing mcmon-agent $Version"
Write-Host "Host: $HostUrl"
Write-Host "Task: $ServiceName"
Write-Host "Install dir: $InstallDir"

schtasks.exe /End /TN $ServiceName 2>$null | Out-Null
schtasks.exe /Delete /TN $ServiceName /F 2>$null | Out-Null

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $ConfigPath) | Out-Null
Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $binPath

[IO.File]::WriteAllBytes($ConfigPath, [Convert]::FromBase64String($ConfigBase64))

$arguments = "--config `"$ConfigPath`" --host-url `"$HostUrl`" --agent-id `"$AgentId`" --token `"$Token`""
$taskAction = New-ScheduledTaskAction -Execute $binPath -Argument $arguments
$taskTrigger = New-ScheduledTaskTrigger -AtStartup
$taskPrincipal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest
$taskSettings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName $ServiceName -Action $taskAction -Trigger $taskTrigger -Principal $taskPrincipal -Settings $taskSettings -Force | Out-Null
Start-ScheduledTask -TaskName $ServiceName

Write-Host "mcmon-agent installed and started."
