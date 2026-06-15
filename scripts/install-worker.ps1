param(
  [ValidateSet("install", "status", "start", "stop", "uninstall")]
  [string]$Action = "install",
  [string]$Version = $env:CMESH_VERSION,
  [string]$ManagerUrl = $env:CMESH_MANAGER_URL,
  [string]$JoinToken = $env:CMESH_JOIN_TOKEN,
  [string]$NodeName = $env:CMESH_NODE_NAME,
  [int]$Cpu = 0,
  [int]$MemoryGb = 2,
  [int]$DiskGb = 10,
  [int]$VramGb = 0,
  [string]$Gpu = "true",
  [switch]$NoBenchmark,
  [switch]$InstallService
)

$ErrorActionPreference = "Stop"
$serviceName = "CMeshWorker"

function Show-ServiceStatus {
  $service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
  if (-not $service) {
    Write-Host "CMesh worker service is not installed"
    return
  }
  $service | Format-List Name, DisplayName, Status, StartType
}

switch ($Action) {
  "status" {
    Show-ServiceStatus
    exit 0
  }
  "start" {
    Start-Service -Name $serviceName
    Show-ServiceStatus
    exit 0
  }
  "stop" {
    Stop-Service -Name $serviceName
    Write-Host "CMesh worker stopped"
    exit 0
  }
  "uninstall" {
    Stop-Service -Name $serviceName -ErrorAction SilentlyContinue
    sc.exe delete $serviceName | Out-Null
    Write-Host "CMesh worker service removed"
    exit 0
  }
}

if ([string]::IsNullOrWhiteSpace($Version)) {
  $Version = "v0.1.0-alpha.9"
}
if (($Cpu -le 0) -and -not [string]::IsNullOrWhiteSpace($env:CMESH_CPU)) {
  $Cpu = [int]$env:CMESH_CPU
}
if ((-not $PSBoundParameters.ContainsKey("MemoryGb")) -and -not [string]::IsNullOrWhiteSpace($env:CMESH_MEMORY_GB)) {
  $MemoryGb = [int]$env:CMESH_MEMORY_GB
}
if ((-not $PSBoundParameters.ContainsKey("DiskGb")) -and -not [string]::IsNullOrWhiteSpace($env:CMESH_DISK_GB)) {
  $DiskGb = [int]$env:CMESH_DISK_GB
}
if ((-not $PSBoundParameters.ContainsKey("VramGb")) -and -not [string]::IsNullOrWhiteSpace($env:CMESH_VRAM_GB)) {
  $VramGb = [int]$env:CMESH_VRAM_GB
}
if ((-not $PSBoundParameters.ContainsKey("Gpu")) -and -not [string]::IsNullOrWhiteSpace($env:CMESH_GPU)) {
  $Gpu = $env:CMESH_GPU
}
if (-not $InstallService -and $env:CMESH_INSTALL_SERVICE -match "^(true|TRUE|1|yes|YES|y|Y)$") {
  $InstallService = $true
}
if ([string]::IsNullOrWhiteSpace($ManagerUrl)) {
  $ManagerUrl = Read-Host "Manager URL, for example https://cmesh.nythral.com"
}
if ([string]::IsNullOrWhiteSpace($JoinToken)) {
  $JoinToken = Read-Host "Join token"
}
if ([string]::IsNullOrWhiteSpace($NodeName)) {
  $NodeName = $env:COMPUTERNAME
}
if ($Cpu -le 0) {
  $Cpu = [Environment]::ProcessorCount
}

$installDir = Join-Path $env:LOCALAPPDATA "CMesh"
$cacheDir = Join-Path $installDir "cache"
$binPath = Join-Path $installDir "cmesh.exe"
$asset = "cmesh-windows-amd64.exe"
$url = "https://github.com/NythralHome/cmesh/releases/download/$Version/$asset"

New-Item -ItemType Directory -Force -Path $installDir | Out-Null
New-Item -ItemType Directory -Force -Path $cacheDir | Out-Null
Invoke-WebRequest -Uri $url -OutFile $binPath

Write-Host "installed $(& $binPath version)"

if ($InstallService) {
  $benchmarkArg = ""
  if (-not $NoBenchmark) {
    $benchmarkArg = " --benchmark"
  }
  $argsLine = "worker run --manager `"$ManagerUrl`" --token `"$JoinToken`" --name `"$NodeName`" --cpu $Cpu --memory-gb $MemoryGb --disk-gb $DiskGb --vram-gb $VramGb --gpu=$Gpu --cache-dir `"$cacheDir`"$benchmarkArg"
  $existing = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
  if ($existing) {
    Stop-Service -Name $serviceName -ErrorAction SilentlyContinue
    sc.exe delete $serviceName | Out-Null
  }
  New-Service -Name $serviceName -BinaryPathName "`"$binPath`" $argsLine" -DisplayName "CMesh Worker" -StartupType Automatic | Out-Null
  Start-Service -Name $serviceName
  Write-Host "CMesh worker service installed and started"
  Show-ServiceStatus
  exit 0
}

$runArgs = @(
  "worker", "run",
  "--manager", $ManagerUrl,
  "--token", $JoinToken,
  "--name", $NodeName,
  "--cpu", "$Cpu",
  "--memory-gb", "$MemoryGb",
  "--disk-gb", "$DiskGb",
  "--vram-gb", "$VramGb",
  "--gpu=$Gpu",
  "--cache-dir", $cacheDir
)
if (-not $NoBenchmark) {
  $runArgs += "--benchmark"
}

& $binPath @runArgs
