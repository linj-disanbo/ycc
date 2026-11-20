#!/bin/bash
# 静态编译全部迁移工具（linux-amd64，CGO_ENABLED=0 无运行时依赖）
# 用法: ./migrate/build.sh [输出目录，默认 ./bin]
# 依赖: Go >= 1.22（go.mod go 1.22；chain33 v1.72.2 + plugin v1.74.0）
# 覆盖工具链: YCC_GO=/path/to/go/bin/go  YCC_GOMODCACHE=...  YCC_GOCACHE=...
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"
cd "$REPO_ROOT"

OUT="${1:-bin}"
TOOLS="export transform build verify calib gendump pos33stat ticketdist txscan"

GO="${YCC_GO:-/home/linj/pkg/go/bin/go}"
if [ ! -x "$GO" ]; then
    echo "未找到 Go 工具链: $GO（用 YCC_GO=/path/to/go/bin/go ./migrate/build.sh 指定）"
    exit 1
fi
# 注意：GOROOT 必须从 GO 路径推导，勿用环境变量（可能被其它 Go 版本污染）
GOROOT="$(cd "$(dirname "$GO")/.." && pwd)"
# ~/work/pkg/mod 沙箱只读，默认用可写副本
GOMODCACHE="${YCC_GOMODCACHE:-/tmp/yccbuild/modcache}"
GOCACHE="${YCC_GOCACHE:-/tmp/yccbuild/gocache}"

mkdir -p "$OUT"
for t in $TOOLS; do
    echo "== 构建 $t =="
    CGO_ENABLED=0 GOROOT="$GOROOT" GOTOOLCHAIN=local GOMODCACHE="$GOMODCACHE" GOCACHE="$GOCACHE" \
        GOPROXY="${YCC_GOPROXY:-https://goproxy.cn,direct}" GOFLAGS=-mod=mod \
        "$GO" build -o "$OUT/$t-static-linux-amd64" "./migrate/$t"
done
echo "完成：$OUT/ 下 $(ls "$OUT" | wc -l) 个静态二进制"
ls -la "$OUT"
