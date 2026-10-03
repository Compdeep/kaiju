# Start the kaiju plugin host on $Port (default 8092), with $Workspace as the agent
# workspace (default: the directory this was launched from). The Windows twin of
# start.sh — same contract, same venv location policy, Scripts\ instead of bin/.
param(
  [int]$Port = 8092,
  [string]$Workspace = $PWD.Path
)
$ErrorActionPreference = "Stop"
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path

# Not beside this script: the venv is runtime, and the source tree is not where
# runtime goes. KAIJU_PLUGIN_VENV overrides.
if ($env:KAIJU_PLUGIN_VENV) {
  $Venv = $env:KAIJU_PLUGIN_VENV
} else {
  $Venv = Join-Path $env:LOCALAPPDATA "kaiju\plugin-host\venv"
}

$Py = Join-Path $Venv "Scripts\python.exe"
$Pip = Join-Path $Venv "Scripts\pip.exe"
if (-not (Test-Path $Py)) {
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Venv) | Out-Null
  python -m venv $Venv
  & $Pip install -q -r (Join-Path $Here "requirements.txt")
}

# Each plugin's own base tier, into the same venv — the host installs only its own,
# so a plugin's declared dependencies would otherwise never be installed. Stamped
# by write time; heavier tiers stay opt-in.
$Stamps = Join-Path (Split-Path -Parent $Venv) "stamps"
New-Item -ItemType Directory -Force -Path $Stamps | Out-Null
Get-ChildItem -Path $Here -Directory | ForEach-Object {
  $Req = Join-Path $_.FullName "requirements.txt"
  if (Test-Path $Req) {
    $Stamp = Join-Path $Stamps $_.Name
    if ((-not (Test-Path $Stamp)) -or ((Get-Item $Req).LastWriteTime -gt (Get-Item $Stamp).LastWriteTime)) {
      Write-Host "[plugin-host] installing $($_.Name) base tier"
      & $Pip install -q -r $Req
      if ($LASTEXITCODE -eq 0) { Set-Content -Path $Stamp -Value "" }
    }
  }
}

# A plugin that touches files enforces its own sandbox against this; unset, it
# refuses every path.
$env:KAIJU_WORKSPACE = $Workspace
# Named plugins only, when the caller said which; otherwise the host loads every
# folder it finds.
if (-not $env:KAIJU_PLUGINS) { $env:KAIJU_PLUGINS = "" }
$env:PYTHONPYCACHEPREFIX = Join-Path (Split-Path -Parent $Venv) "pycache"
Set-Location $Here
& $Py -m uvicorn host:app --host 127.0.0.1 --port $Port
