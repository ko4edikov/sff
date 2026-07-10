# Install the latest sff binary from GitHub Releases (Windows).
#
#   irm https://raw.githubusercontent.com/ko4edikov/sff/master/install.ps1 | iex
#
# Overrides via env:
#   $env:SFF_VERSION = 'v0.2.0'           pin a version (default: latest release)
#   $env:SFF_INSTALL_DIR = 'C:\tools'     where to install (default: %LOCALAPPDATA%\sff)

$ErrorActionPreference = 'Stop'

$repo = 'ko4edikov/sff'
$bin = 'sff'

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
	'AMD64' { 'amd64' }
	'ARM64' { 'arm64' }
	default { throw "sff: unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$version = $env:SFF_VERSION
if (-not $version) {
	$version = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
}
if (-not $version) {
	throw 'sff: could not determine the latest release'
}

# GoReleaser strips the leading v from archive names (v0.1.0 -> 0.1.0).
$vnum = $version.TrimStart('v')
$url = "https://github.com/$repo/releases/download/$version/${bin}_${vnum}_windows_${arch}.zip"

$tmp = Join-Path ([IO.Path]::GetTempPath()) "sff-install-$PID"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
	Write-Host "sff: downloading $url"
	$zip = Join-Path $tmp "$bin.zip"
	Invoke-WebRequest -Uri $url -OutFile $zip
	Expand-Archive -Path $zip -DestinationPath $tmp -Force

	$dir = $env:SFF_INSTALL_DIR
	if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA $bin }
	New-Item -ItemType Directory -Force -Path $dir | Out-Null
	Copy-Item (Join-Path $tmp "$bin.exe") (Join-Path $dir "$bin.exe") -Force
	Write-Host "sff: installed $version to $dir\$bin.exe"

	$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
	if (($userPath -split ';') -notcontains $dir) {
		[Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
		Write-Host "sff: added $dir to your user PATH (restart the terminal to pick it up)"
	}
}
finally {
	Remove-Item -Recurse -Force $tmp
}
