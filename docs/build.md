# 从源码编译

在包含 `go.mod` 的源码根目录执行命令。工具链最低版本以 [go.mod](../go.mod) 为准，当前为 Go 1.26.11。Go 可执行文件应在 `PATH` 中。使用较早的 Go 安装时，`GOTOOLCHAIN=auto` 会在构建时选择并下载模块要求的工具链。

WebUI 随网关一同编译，SQLite 驱动支持 `CGO_ENABLED=0`。运行主程序用 `./cmd/hashinex`，需要单独部署 gscore 协议适配器时用 `./cmd/hashinex-gscore`。

## Windows（PowerShell）

下面的示例生成 Windows amd64 二进制。构建产物、Go 工具链和依赖缓存均放在源码目录旁的 `cache/`。

```powershell
$buildCache = [System.IO.Path]::GetFullPath((Join-Path (Get-Location).Path "../cache/hashinex-build"))
$env:GOPATH = Join-Path $buildCache "gopath"
$env:GOMODCACHE = Join-Path $buildCache "modules"
$env:GOCACHE = Join-Path $buildCache "objects"
$env:GOTMPDIR = Join-Path $buildCache "tmp"
$env:TEMP = $env:GOTMPDIR
$env:TMP = $env:GOTMPDIR
$env:GOTOOLCHAIN = "auto"
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$out = Join-Path $buildCache "windows-amd64"
New-Item -ItemType Directory -Force -Path $out, $env:GOTMPDIR | Out-Null
go version
go build -trimpath -o "$out/hashinex.exe" ./cmd/hashinex
```

同一终端可继续编译 Linux amd64。

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$out = Join-Path $buildCache "linux-amd64"
New-Item -ItemType Directory -Force -Path $out | Out-Null
go build -trimpath -o "$out/hashinex" ./cmd/hashinex
```

编译 Linux arm64 时，将 `GOARCH` 改成 `arm64`，并把输出目录换成 `linux-arm64`。目标架构对应部署机器，常见的 `x86_64` 对应 `amd64`，`aarch64` 对应 `arm64`。

## Linux（Shell）

```sh
build_cache="$(cd .. && pwd)/cache/hashinex-build"
export GOPATH="$build_cache/gopath"
export GOMODCACHE="$build_cache/modules"
export GOCACHE="$build_cache/objects"
export GOTMPDIR="$build_cache/tmp"
export TMPDIR="$GOTMPDIR"
export GOTOOLCHAIN=auto
export CGO_ENABLED=0
export GOOS=linux
mkdir -p "$GOTMPDIR"
export GOARCH="$(go env GOHOSTARCH)"
out="$build_cache/$GOOS-$GOARCH"
mkdir -p "$out"
go version
go build -trimpath -o "$out/hashinex" ./cmd/hashinex
```

跨架构编译时，将 `GOARCH` 显式设为目标机器的 `amd64` 或 `arm64`。如需独立适配器，沿用相同的编译参数，把包路径改为 `./cmd/hashinex-gscore`，输出文件名改为 `hashinex-gscore`，Windows 用 `hashinex-gscore.exe`。使用 gscore 直连输出时，直接运行网关即可。 [gscore 接入说明](gscore.md)

## 下载失败

依赖或工具链下载失败时，可以在当前终端临时设置 `GOPROXY` 后重试。PowerShell 使用 `$env:GOPROXY = "https://goproxy.cn,direct"`，Shell 使用 `export GOPROXY=https://goproxy.cn,direct`。保持 Go 的模块校验开启。网络受限的机器也可以事先安装满足 `go.mod` 要求的 Go 工具链。

构建完成后，将目标平台的可执行文件放到运行目录，按[部署指南](operations.md)准备配置并启动。
