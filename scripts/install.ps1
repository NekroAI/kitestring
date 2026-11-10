# 从 GitHub Release 安装 ks（Windows）：判断架构、下载、校验 SHA256、装到固定目录并加入 PATH。
# 用法（PowerShell）：irm https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.ps1 | iex
# 环境变量：KS_VERSION（如 v0.1.0，默认最新版）、KS_INSTALL_DIR（默认 ~\.local\bin）、
# KS_RELEASE_BASE（下载源，默认 https://github.com，试跑 / 镜像用）。
# 重跑即升级；ks install 写入的配置引用的是安装目录里的绝对路径，升级后重启常驻服务即可。
# 与 scripts/install.sh 行为一致；Windows PowerShell 5.1 起可用。

& {
  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue'
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

  $repo = 'NekroAI/kitestring'
  $base = if ($env:KS_RELEASE_BASE) { $env:KS_RELEASE_BASE.TrimEnd('/') } else { 'https://github.com' }
  $dir = if ($env:KS_INSTALL_DIR) { $env:KS_INSTALL_DIR } else { Join-Path $HOME '.local\bin' }

  function Fail($msg) { throw "ks install: $msg" }

  $arch = $env:PROCESSOR_ARCHITEW6432
  if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
  switch ($arch) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { Fail "暂不支持 $arch 架构" }
  }

  # 最新版从 releases/latest 的跳转地址里取，不走 API，避免匿名请求被限流。
  $tag = $env:KS_VERSION
  if (-not $tag) {
    $req = [Net.HttpWebRequest]::Create("$base/$repo/releases/latest")
    $req.Method = 'HEAD'
    $req.AllowAutoRedirect = $false
    try { $resp = $req.GetResponse() } catch [Net.WebException] { $resp = $_.Exception.Response }
    if (-not $resp) { Fail '查询最新版本失败' }
    $loc = $resp.Headers['Location']
    $resp.Close()
    if (-not $loc) { Fail '查询最新版本失败' }
    $tag = $loc.TrimEnd('/').Split('/')[-1]
    if (-not $tag.StartsWith('v')) { Fail "还没有发布版本（$loc）" }
  }
  $v = $tag.TrimStart('v')

  $name = "kitestring-$v-windows-$arch.zip"
  $dl = "$base/$repo/releases/download/$tag"
  $tmp = Join-Path ([IO.Path]::GetTempPath()) ("ks-install-" + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    Write-Host "下载 ks $tag（windows-$arch）…"
    try { Invoke-WebRequest -UseBasicParsing -Uri "$dl/$name" -OutFile (Join-Path $tmp $name) } catch { Fail "下载 $name 失败：$($_.Exception.Message)" }
    try { Invoke-WebRequest -UseBasicParsing -Uri "$dl/SHA256SUMS.txt" -OutFile (Join-Path $tmp 'SHA256SUMS.txt') } catch { Fail "下载 SHA256SUMS.txt 失败：$($_.Exception.Message)" }

    $want = $null
    foreach ($line in Get-Content (Join-Path $tmp 'SHA256SUMS.txt')) {
      $f = -split $line
      if ($f.Count -eq 2 -and $f[1].TrimStart('*') -eq $name) { $want = $f[0].ToLower() }
    }
    if (-not $want) { Fail "SHA256SUMS.txt 里没有 $name" }
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $name)).Hash.ToLower()
    if ($got -ne $want) { Fail "$name 校验失败，文件可能不完整" }

    Expand-Archive -Path (Join-Path $tmp $name) -DestinationPath (Join-Path $tmp 'x')
    $src = Join-Path $tmp 'x\ks.exe'
    if (-not (Test-Path $src)) { Fail "包里没有 ks.exe" }
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $dst = Join-Path $dir 'ks.exe'
    # 正在运行的 ks（常驻服务）不能覆盖，但可以改名：先挪开旧的再放新的，旧文件下次 ks update 时清掉。
    Copy-Item $src "$dst.new" -Force
    if (Test-Path $dst) {
      $old = "$dst.old-$([DateTime]::Now.Ticks)"
      Move-Item $dst $old
      Move-Item "$dst.new" $dst
      Remove-Item $old -ErrorAction SilentlyContinue
    } else {
      Move-Item "$dst.new" $dst
    }
  } finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }

  Write-Host "✓ 已安装 $(& $dst version) → $dst"
  # 加入用户的 PATH（新开的终端生效），当前窗口也立即可用
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  if (-not (($userPath -split ';') -contains $dir)) {
    [Environment]::SetEnvironmentVariable('Path', ((@($dir) + ($userPath -split ';' | Where-Object { $_ })) -join ';'), 'User')
    Write-Host "  已把 $dir 加入用户的 PATH"
  }
  if (-not (($env:Path -split ';') -contains $dir)) { $env:Path = "$dir;$env:Path" }

  Write-Host @'

接下来（网页「设置 → 设备 → 接入设备」弹出的指引里有按平台写好的完整命令）：
  1. ks login --hub <Hub 地址> --token kst_xxx
  2. ks install            # 写入 Claude Code / Codex 的 hook、MCP，并注册 daemon 常驻服务（--dry-run 先预览）
  3. ks status             # 看到 "connected": true 就连上了
已接入过的设备：再次运行 ks install，即用新版本重启常驻服务
'@
}
