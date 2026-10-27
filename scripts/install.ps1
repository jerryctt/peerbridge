<#
.SYNOPSIS
  Install hostrunner on Windows for Codex and/or Claude Desktop.

.DESCRIPTION
  Works with Windows PowerShell 5.1 and PowerShell 7+.

    powershell -ExecutionPolicy Bypass -File scripts\install.ps1            # Codex + Claude Desktop
    powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Codex     # Codex only
    powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Claude    # Claude Desktop only
    ... -Local .\hostrunner.exe      use a locally built binary instead of downloading
    ... -Version 0.3.1               pin a release (default: version in .claude-plugin\plugin.json)
    ... -ServerName hostrunner       MCP server name used in both configs
    ... -NoClaudeConfigWrite         print the Claude Desktop JSON snippet instead of merging it

  What it does (idempotent):
    1. downloads hostrunner_<ver>_windows_<arch>.zip from GitHub Releases, verifies
       it against checksums.txt and installs hostrunner.exe to %LOCALAPPDATA%\hostrunner
    2. creates %APPDATA%\hostrunner\config.yaml from the example if missing
    3. Codex: writes a managed [mcp_servers.<name>] block to %USERPROFILE%\.codex\config.toml
       (refuses if an unmanaged definition exists), copies skills\peer-review and
       skills\peer-chat to %USERPROFILE%\.agents\skills, validates with 'codex mcp list'
    4. Claude Desktop: merges mcpServers.<name> into %APPDATA%\Claude\claude_desktop_config.json
       (backup kept next to it)
#>
[CmdletBinding()]
param(
  [string]$Local = "",
  [string]$Version = "",
  [string]$ServerName = "hostrunner",
  [switch]$Codex,
  [switch]$Claude,
  [switch]$NoClaudeConfigWrite
)

Set-StrictMode -Version 2
$ErrorActionPreference = 'Stop'
$Repo = "jerryctt/hostrunner-mcp"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

# Windows PowerShell 5.1 reads BOM-less files and writes with the ANSI code
# page by default; config files and non-ASCII user names would be corrupted.
# Always go through UTF-8 (no BOM) explicitly.
$Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
function Read-Text([string]$Path) { [System.IO.File]::ReadAllText($Path, $Utf8NoBom) }
function Write-Text([string]$Path, [string]$Text) { [System.IO.File]::WriteAllText($Path, $Text, $Utf8NoBom) }

if (-not $Codex -and -not $Claude) { $Codex = $true; $Claude = $true }

# ---- platform ---------------------------------------------------------------
$onWindows = ($PSVersionTable.PSEdition -eq 'Desktop') -or ($PSVersionTable.PSVersion.Major -ge 6 -and $IsWindows)
if ($onWindows) {
  $home_ = $env:USERPROFILE
  $exeName = "hostrunner.exe"
  $installDir = Join-Path $env:LOCALAPPDATA "hostrunner"
  $hrConfig = Join-Path (Join-Path $env:APPDATA "hostrunner") "config.yaml"
  $claudeConfig = Join-Path (Join-Path $env:APPDATA "Claude") "claude_desktop_config.json"
  switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { $arch = "amd64" }
    "ARM64" { $arch = "arm64" }
    "x86"   { $arch = "386" }
    default { throw "unsupported Windows architecture '$($env:PROCESSOR_ARCHITECTURE)'" }
  }
  $os = "windows"; $ext = "zip"
} else {
  # Non-Windows PowerShell (testing / macOS / Linux users who prefer pwsh).
  $home_ = $HOME
  $exeName = "hostrunner"
  $installDir = Join-Path $HOME ".local/share/hostrunner"
  $hrConfig = Join-Path $HOME ".config/hostrunner/config.yaml"
  if ($IsMacOS) { $claudeConfig = Join-Path $HOME "Library/Application Support/Claude/claude_desktop_config.json"; $os = "darwin" }
  else { $claudeConfig = Join-Path $HOME ".config/Claude/claude_desktop_config.json"; $os = "linux" }
  $rawArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
  switch ($rawArch) {
    "X64"   { $arch = "amd64" }
    "Arm64" { $arch = "arm64" }
    "X86"   { $arch = "386" }
    "Arm"   { $arch = "armv7" }
    default { throw "unsupported architecture '$rawArch'" }
  }
  $ext = "tar.gz"
}

if (-not $Version -and -not $Local) {
  $pluginPath = Join-Path $Root ".claude-plugin\plugin.json"
  if (-not (Test-Path $pluginPath)) {
    throw "cannot determine the release version: $pluginPath is missing. Pass -Version X.Y.Z (or -Local <exe>)."
  }
  $Version = (Read-Text $pluginPath | ConvertFrom-Json).version
}

# ---- 1. binary --------------------------------------------------------------
New-Item -ItemType Directory -Force -Path $installDir | Out-Null
$bin = Join-Path $installDir $exeName

if ($Local) {
  $src = (Resolve-Path $Local).Path
  if ((Test-Path $bin) -and ((Get-Item $bin).FullName -eq (Get-Item $src).FullName)) {
    Write-Host "binary: $bin (already in place)"
  } else {
    Copy-Item -Force $src $bin
    Write-Host "binary: $bin (copied from $src)"
  }
} else {
  $archive = "hostrunner_${Version}_${os}_${arch}.$ext"
  $base = "https://github.com/$Repo/releases/download/v$Version"
  $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("hostrunner-" + [guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    Write-Host "downloading $archive (v$Version)..."
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt")
    $line = (Read-Text (Join-Path $tmp "checksums.txt")) -split "`r?`n" | Where-Object { $_ -match ("\s" + [regex]::Escape($archive) + "$") } | Select-Object -First 1
    if (-not $line) { throw "$archive not listed in checksums.txt" }
    $want = ($line -split "\s+")[0].ToLower()
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $archive)).Hash.ToLower()
    if ($want -ne $got) { throw "checksum mismatch for $archive (expected $want, got $got)" }
    if ($ext -eq "zip") {
      Expand-Archive -Force -Path (Join-Path $tmp $archive) -DestinationPath (Join-Path $tmp "x")
    } else {
      New-Item -ItemType Directory -Path (Join-Path $tmp "x") | Out-Null
      & tar -xzf (Join-Path $tmp $archive) -C (Join-Path $tmp "x")
      if ($LASTEXITCODE -ne 0) { throw "tar failed" }
    }
    $extracted = Get-ChildItem -Path (Join-Path $tmp "x") -Recurse -Filter $exeName | Select-Object -First 1
    if (-not $extracted) { throw "'$exeName' not found in archive" }
    Copy-Item -Force $extracted.FullName $bin
    if (-not $onWindows) { & chmod +x $bin }
    Write-Host "binary: $bin (v$Version, checksum verified)"
  } finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }
}

# ---- 2. hostrunner config -----------------------------------------------------
if (-not (Test-Path $hrConfig)) {
  New-Item -ItemType Directory -Force -Path (Split-Path $hrConfig) | Out-Null
  $example = Read-Text (Join-Path $Root "examples\config.example.yaml")
  if ($onWindows) {
    $example = $example -replace '/Users/you/code', ((Join-Path $home_ "code") -replace '\\', '/')
  }
  Write-Text $hrConfig $example
  Write-Host "created $hrConfig - edit allowed_roots before use"
} else {
  Write-Host "config: $hrConfig (unchanged)"
}

# ---- 3. Codex -----------------------------------------------------------------
if ($Codex) {
  $codexHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $home_ ".codex" }
  $cfg = Join-Path $codexHome "config.toml"
  New-Item -ItemType Directory -Force -Path $codexHome | Out-Null
  if (-not (Test-Path $cfg)) { Write-Text $cfg "" }

  $begin = "# >>> hostrunner (managed by install.ps1) >>>"
  $end = "# <<< hostrunner <<<"
  # Also recognise the block written by scripts/install-codex.sh.
  $beginSh = "# >>> hostrunner (managed by install-codex.sh) >>>"

  $lines = @((Read-Text $cfg) -split "`r?`n")
  $kept = New-Object System.Collections.Generic.List[string]
  $skip = $false
  foreach ($l in $lines) {
    if ($l -eq $begin -or $l -eq $beginSh) { $skip = $true; continue }
    if ($l -eq $end) { $skip = $false; continue }
    if (-not $skip) { $kept.Add($l) }
  }
  while ($kept.Count -gt 0 -and $kept[$kept.Count - 1] -match '^\s*$') { $kept.RemoveAt($kept.Count - 1) }

  $q = "[`"']?"
  $parent = "${q}mcp_servers${q}"
  $name = [regex]::Escape($ServerName)
  $hdr = "^\s*\[\s*$parent\s*\.\s*$q$name$q\s*(\.[^\]]*)?\]\s*(#.*)?$"
  $dotted = "^\s*$parent\s*\.\s*$q$name$q\s*[.=]"
  $inlineParent = "^\s*$parent\s*="
  $bareParent = "^\s*\[\s*$parent\s*\]\s*(#.*)?$"
  $bareKey = "^\s*$q$name$q\s*="
  if (@($kept | Where-Object { $_ -match $inlineParent }).Count -gt 0) {
    throw "$cfg defines mcp_servers as an inline table (mcp_servers = { ... }); a sub-table cannot be appended. Convert it to [mcp_servers.<name>] tables and re-run. Nothing was changed."
  }
  $conflict = @($kept | Where-Object { $_ -match $hdr -or $_ -match $dotted }).Count -gt 0
  if (-not $conflict -and @($kept | Where-Object { $_ -match $bareParent }).Count -gt 0 -and @($kept | Where-Object { $_ -match $bareKey }).Count -gt 0) { $conflict = $true }
  if ($conflict) {
    throw "$cfg already defines mcp_servers.$ServerName outside the block managed by this installer. Remove that definition (or re-run with -ServerName <other>). Nothing was changed."
  }

  $backup = "$cfg.bak.hostrunner"
  Copy-Item -Force $cfg $backup
  # TOML basic string: escape backslash and double quote so any path (including
  # ones with apostrophes, e.g. C:\Users\O'Brien) stays valid.
  function ConvertTo-TomlString([string]$s) { '"' + (($s -replace '\\', '\\') -replace '"', '\"') + '"' }
  $tomlBin = ConvertTo-TomlString $bin
  # Pass -config explicitly so Codex and Claude Desktop use the same file this
  # installer created, even if a ~/.config/hostrunner/config.yaml also exists.
  $tomlCfg = ConvertTo-TomlString $hrConfig
  $block = @(
    "",
    $begin,
    "[mcp_servers.$ServerName]",
    "command = $tomlBin",
    "args = [""-config"", $tomlCfg]",
    "# review/chat jobs are polled via job_status; keep hostrunner's status_poll",
    "# (config.yaml) below this tool timeout.",
    "tool_timeout_sec = 120",
    "startup_timeout_sec = 20",
    "# Approval prompts: 'writes' asks only for tools NOT marked read-only. The",
    "# peer tools (review_start, chat_start, job_status) are read-only, so only",
    "# run_command prompts. Use 'approve' to never ask, 'prompt' to always ask.",
    'default_tools_approval_mode = "writes"',
    $end
  )
  $newText = (@($kept) + $block) -join "`n"
  Write-Text $cfg ($newText + "`n")

  # Validate with codex itself when available; restore the original on any
  # failure. Under 5.1 a native command writing to stderr can itself throw
  # when $ErrorActionPreference is Stop, so relax it inside the try.
  $codexExe = Get-Command codex -ErrorAction SilentlyContinue
  if ($codexExe) {
    $ok = $false; $said = ""
    try {
      $prev = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
      $out = & $codexExe.Source mcp list 2>&1
      $ErrorActionPreference = $prev
      $ok = ($LASTEXITCODE -eq 0)
      $said = ($out | ForEach-Object { "$_" }) -join "`n"
    } catch {
      $ErrorActionPreference = 'Stop'
      $said = $_.Exception.Message
    }
    if (-not $ok) {
      Copy-Item -Force $backup $cfg
      Remove-Item -Force $backup
      throw "codex rejected the updated $cfg; original restored. codex said:`n$said"
    }
  }
  Remove-Item -Force $backup
  Write-Host "codex config: [mcp_servers.$ServerName] written to $cfg"

  $skills = Join-Path $home_ ".agents\skills"
  New-Item -ItemType Directory -Force -Path $skills | Out-Null
  foreach ($s in @("peer-review", "peer-chat")) {
    $dst = Join-Path $skills $s
    if (Test-Path $dst) { Remove-Item -Recurse -Force $dst }
    Copy-Item -Recurse (Join-Path $Root "skills\$s") $dst
    Write-Host "skill: $dst"
  }
}

# ---- 4. Claude Desktop ----------------------------------------------------------
if ($Claude) {
  $entry = [ordered]@{ command = $bin; args = @("-config", $hrConfig) }
  $snippet = ([ordered]@{ mcpServers = [ordered]@{ $ServerName = $entry } } | ConvertTo-Json -Depth 5)
  if ($NoClaudeConfigWrite) {
    Write-Host "Add this to $claudeConfig :"
    Write-Host $snippet
  } else {
    New-Item -ItemType Directory -Force -Path (Split-Path $claudeConfig) | Out-Null
    if (Test-Path $claudeConfig) {
      $raw = Read-Text $claudeConfig
      $json = if ($raw -and $raw.Trim()) { $raw | ConvertFrom-Json } else { New-Object PSObject }
      Copy-Item -Force $claudeConfig "$claudeConfig.bak.hostrunner"
    } else {
      $json = New-Object PSObject
    }
    if ($null -eq $json.PSObject.Properties["mcpServers"]) {
      $json | Add-Member -MemberType NoteProperty -Name mcpServers -Value (New-Object PSObject)
    }
    $servers = $json.mcpServers
    $entryObj = New-Object PSObject
    $entryObj | Add-Member NoteProperty command $bin
    $entryObj | Add-Member NoteProperty args @("-config", $hrConfig)
    if ($null -ne $servers.PSObject.Properties[$ServerName]) {
      $servers.$ServerName = $entryObj
    } else {
      $servers | Add-Member -MemberType NoteProperty -Name $ServerName -Value $entryObj
    }
    Write-Text $claudeConfig ($json | ConvertTo-Json -Depth 10)
    Write-Host "claude desktop: mcpServers.$ServerName written to $claudeConfig (backup: .bak.hostrunner)"
  }
}

Write-Host ""
Write-Host "Done. Next steps:"
Write-Host "  1. Edit $hrConfig : set allowed_roots; if codex/claude are not on the GUI app's PATH,"
Write-Host "     set agents.codex.command / agents.claude.command to full paths (e.g. (Get-Command claude).Source)."
if ($Codex) { Write-Host "  2. Restart the Codex app (or run 'codex mcp list') and check that '$ServerName' is listed." }
if ($Claude) { Write-Host "  3. Restart Claude Desktop; '$ServerName' should appear under MCP servers." }
Write-Host "  Then say 'peer review my changes' or 'ask claude/codex what it thinks about this design'."
