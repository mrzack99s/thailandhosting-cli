# Installs th, the ThailandHosting CLI, from GitHub Releases (Windows).
#
#   irm https://raw.githubusercontent.com/mrzack99s/thailandhosting-cli/main/install.ps1 | iex
#
# $env:TH_VERSION picks a version (default: the latest release). th goes to
# %LOCALAPPDATA%\Programs\th, which is added to the user PATH. The archive
# is checked against the release's SHA256SUMS before anything is installed.
$ErrorActionPreference = 'Stop'
$repo = 'mrzack99s/thailandhosting-cli'

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$version = $env:TH_VERSION
if (-not $version) {
  $version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
}
$version = $version.TrimStart('v')

$archive = "th_${version}_windows_$arch.zip"
$base = "https://github.com/$repo/releases/download/v$version"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("th-install-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading th $version (windows/$arch)..."
  Invoke-WebRequest "$base/$archive" -OutFile (Join-Path $tmp $archive) -UseBasicParsing
  Invoke-WebRequest "$base/SHA256SUMS" -OutFile (Join-Path $tmp 'SHA256SUMS') -UseBasicParsing

  $line = Get-Content (Join-Path $tmp 'SHA256SUMS') | Where-Object { $_ -match " $([regex]::Escape($archive))$" } | Select-Object -First 1
  $want = if ($line) { ($line -split '\s+')[0].ToLower() } else { '' }
  $got = (Get-FileHash (Join-Path $tmp $archive) -Algorithm SHA256).Hash.ToLower()
  if (-not $want -or $want -ne $got) { throw "th: checksum mismatch for $archive" }

  Expand-Archive (Join-Path $tmp $archive) -DestinationPath $tmp -Force
  $bin = Get-ChildItem $tmp -Recurse -Filter 'th.exe' | Select-Object -First 1
  if (-not $bin) { throw 'th: archive has no th.exe' }

  $dir = Join-Path $env:LOCALAPPDATA 'Programs\th'
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  Copy-Item $bin.FullName (Join-Path $dir 'th.exe') -Force

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (($userPath -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $dir), 'User')
    Write-Host "Added $dir to your PATH (open a new terminal)."
  }
  Write-Host "Installed th $version to $dir\th.exe"
  Write-Host 'Next: th auth login'
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
