#!/usr/bin/env bash
# stdseek 发布构建：交叉编译三个目标的单二进制，附带版本戳与校验和。
#
# 为什么强调「单二进制」：这是自托管卖点的全部依据——下载一个文件、双击、
# 开浏览器就能用，不需要 Node/npm/模型文件/数据库服务。所以构建必须
# CGO_ENABLED=0（纯 Go），否则产物会依赖 libc 版本，不再能随便拷走。
#
# 用法：
#   ./scripts/build-release.sh                 # 版本取 git describe，构建三目标
#   ./scripts/build-release.sh v1.2.0          # 指定版本
#   ./scripts/build-release.sh v1.2.0 --web    # 先重建前端（需要 npm）
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  echo "找不到 go 命令：请先安装 Go 并确保它在 PATH 里。" >&2
  exit 1
fi

VERSION="${1:-}"
WEB=0
if [ "$VERSION" = "--web" ]; then
  VERSION=""
  WEB=1
elif [ "${2:-}" = "--web" ]; then
  WEB=1
fi
if [ -z "$VERSION" ]; then
  # 版本号优先取根目录 VERSION（发版脚本 `scripts/release.sh` 维护的唯一来源），
  # 没有才退回 git describe——两者混用会让同一个提交打出两个版本号。
  if [ -f VERSION ]; then
    VERSION="v$(tr -d ' \t\r\n' < VERSION)"
  else
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
  fi
fi
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
OUT="dist"

# --web：重建前端产物（web/ → internal/webui/dist）。
# 前端产物是入库的，所以默认不重建——但改了前端就必须重建，
# 否则打出来的二进制里还是旧页面（这类问题在单二进制里极难发现）。
if [ "$WEB" = "1" ]; then
  echo "==> 重建前端"
  # 构建产物先落 web/dist，再由 tools/emit-dist.mjs 同步到 internal/webui/dist
  # （两步构建必须用同一份产物，否则 chunk 哈希对不上，页面会点不动）
  ( cd web && npm ci --silent && npm run build --silent )
fi

mkdir -p "$OUT"
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}"

build() {
  local goos="$1" goarch="$2" ext="$3"
  local name="stdseek-${VERSION}-${goos}-${goarch}${ext}"
  echo "==> ${name}"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${OUT}/${name}" ./cmd/stdseek
}

build windows amd64 .exe
build linux   amd64 ""
build linux   arm64 ""

echo "==> 校验和"
( cd "$OUT" && sha256sum stdseek-"${VERSION}"-* > SHA256SUMS.txt && cat SHA256SUMS.txt )

echo
echo "版本   : ${VERSION} (${COMMIT})"
echo "构建于 : ${BUILD_TIME}"
echo "产物   : ${OUT}/"
for f in "$OUT"/stdseek-"${VERSION}"-*; do
  [ -f "$f" ] && echo "  $(du -h "$f" | cut -f1)	$(basename "$f")"
done
