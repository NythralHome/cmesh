param(
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

if ([string]::IsNullOrWhiteSpace($Version)) {
  $Version = "v0.1.0-alpha.4"
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
$binPath = Join-Path $installDir "cmesh.exe"
$asset = "cmesh-windows-amd64.exe"
$url = "https://github.com/NythralHome/cmesh/releases/download/$Version/$asset"

New-Item -ItemType Directory -Force -Path $installDir | Out-Null
Invoke-WebRequest -Uri $url -OutFile $binPath

Write-Host "installed $(& $binPath version)"

if ($InstallService) {
  $serviceName = "CMeshWorker"
  $benchmarkArg = ""
  if (-not $NoBenchmark) {
    $benchmarkArg = " --benchmark"
  }
  $argsLine = "worker run --manager `"$ManagerUrl`" --token `"$JoinToken`" --name `"$NodeName`" --cpu $Cpu --memory-gb $MemoryGb --disk-gb $DiskGb --vram-gb $VramGb --gpu=$Gpu$benchmarkArg"
  $existing = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
  if ($existing) {
    Stop-Service -Name $serviceName -ErrorAction SilentlyContinue
    sc.exe delete $serviceName | Out-Null
  }
  New-Service -Name $serviceName -BinaryPathName "`"$binPath`" $argsLine" -DisplayName "CMesh Worker" -StartupType Automatic | Out-Null
  Start-Service -Name $serviceName
  Write-Host "CMesh worker service installed and started"
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
  "--gpu=$Gpu"
)
if (-not $NoBenchmark) {
  $runArgs += "--benchmark"
}

& $binPath @runArgs
