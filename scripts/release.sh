#!/usr/bin/env bash
# voidxo 发版：bump 版本号 → 写 CHANGELOG → 提交 → 打 tag → 推送 GitHub。
#
# 为什么要有这个脚本：版本号散在 VERSION / CHANGELOG / git tag 三处，
# 手工维护必然漂（改了两处忘了第三处，或 tag 打在上一个 commit 上）。
# 这里让 VERSION 成为唯一事实来源，其余两处由脚本同步生成。
#
# 用法：
#   ./scripts/release.sh patch -m "修复：xxx" -m "新增：yyy"
#   ./scripts/release.sh minor -m "新增：xxx"
#   ./scripts/release.sh 1.0.0 -m "新增：正式版"
#   ./scripts/release.sh patch -m "..." --no-push     # 只提交和打 tag，不推送
#   ./scripts/release.sh patch -m "..." --dry-run     # 只打印将要做什么
#
# 条目里的类型前缀会被转成粗体：`新增：` → `- **新增**：`。
# 支持的：安全 / 修复 / 新增 / 改进 / 重构 / 移除 / 文档；没写前缀按「改进」处理。
set -euo pipefail

cd "$(dirname "$0")/.."

die() { echo "错误：$*" >&2; exit 1; }

[ -f VERSION ] || die "找不到 VERSION 文件（版本号唯一事实来源，内容形如 0.10.0）"
[ -f CHANGELOG.md ] || die "找不到 CHANGELOG.md"

LEVEL="${1:-}"
[ -n "$LEVEL" ] || die "用法：./scripts/release.sh patch|minor|major|X.Y.Z [-m 条目]... [--no-push] [--dry-run]"
shift || true

ENTRIES=()
PUSH=1
DRY=0
SKIP_BUILD=0
while [ $# -gt 0 ]; do
  case "$1" in
    -m|--message) [ $# -ge 2 ] || die "$1 后面要跟一条日志"; ENTRIES+=("$2"); shift 2 ;;
    --no-push)    PUSH=0; shift ;;
    --dry-run)    DRY=1; shift ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    -h|--help)    sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)            die "未知参数：$1" ;;
  esac
done

# ---- 计算新版本号 ------------------------------------------------------------
CURRENT="$(tr -d ' \t\r\n' < VERSION)"
printf '%s' "$CURRENT" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
  || die "VERSION 内容不是 X.Y.Z 形式：$CURRENT"

IFS=. read -r MAJOR MINOR PATCH <<< "$CURRENT"
case "$LEVEL" in
  patch) PATCH=$((PATCH + 1)) ;;
  minor) MINOR=$((MINOR + 1)); PATCH=0 ;;
  major) MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0 ;;
  [0-9]*)
    printf '%s' "$LEVEL" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
      || die "版本号要写成 X.Y.Z（当前 ${CURRENT}）"
    IFS=. read -r MAJOR MINOR PATCH <<< "$LEVEL" ;;
  *) die "第一个参数必须是 patch / minor / major / X.Y.Z，收到：$LEVEL" ;;
esac
NEW="${MAJOR}.${MINOR}.${PATCH}"
TAG="v${NEW}"
DATE="$(date +%F)"

git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null && die "tag ${TAG} 已存在"
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
[ "$BRANCH" != "HEAD" ] || die "当前是 detached HEAD，先切回分支再发版"

# ---- 组织日志条目 ------------------------------------------------------------
if [ ${#ENTRIES[@]} -eq 0 ]; then
  # 没给条目就从提交历史聚合：上次 tag 之后的非 release 提交，按 commit 前缀归类
  PREV_TAG="$(git describe --tags --abbrev=0 2>/dev/null || true)"
  RANGE="${PREV_TAG:+${PREV_TAG}..HEAD}"
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    case "$line" in
      feat:*)     ENTRIES+=("新增：${line#feat: }") ;;
      fix:*)      ENTRIES+=("修复：${line#fix: }") ;;
      docs:*)     ENTRIES+=("文档：${line#docs: }") ;;
      refactor:*) ENTRIES+=("重构：${line#refactor: }") ;;
      perf:*)     ENTRIES+=("改进：${line#perf: }") ;;
      chore:*)    ;; # chore 不进日志
      *)          ENTRIES+=("改进：${line}") ;;
    esac
  done < <(git log --no-merges --pretty=%s ${RANGE:-HEAD} | grep -v '^chore(release):' || true)
  [ ${#ENTRIES[@]} -gt 0 ] || die "没有日志条目：用 -m 给一条，或先有提交"
fi

# 类型前缀解析：用精确的 case 分支而不是正则，中文在 C locale 下走 =~ 会失配
strip_type() {
  T="改进"; X="$1"
  case "$1" in
    安全：*)   T="安全"; X="${1#安全：}" ;;
    安全:*)    T="安全"; X="${1#安全:}" ;;
    修复：*)   T="修复"; X="${1#修复：}" ;;
    修复:*)    T="修复"; X="${1#修复:}" ;;
    新增：*)   T="新增"; X="${1#新增：}" ;;
    新增:*)    T="新增"; X="${1#新增:}" ;;
    改进：*)   T="改进"; X="${1#改进：}" ;;
    改进:*)    T="改进"; X="${1#改进:}" ;;
    重构：*)   T="重构"; X="${1#重构：}" ;;
    重构:*)    T="重构"; X="${1#重构:}" ;;
    移除：*)   T="移除"; X="${1#移除：}" ;;
    移除:*)    T="移除"; X="${1#移除:}" ;;
    文档：*)   T="文档"; X="${1#文档：}" ;;
    文档:*)    T="文档"; X="${1#文档:}" ;;
  esac
  # 冒号后可能多打一个空格，去掉前导空白
  while [ "${X# }" != "$X" ]; do X="${X# }"; done
}

BODY=""
for e in "${ENTRIES[@]}"; do
  strip_type "$e"
  BODY+="- **${T}**：${X}"$'\n'
done

echo "==> ${CURRENT} → ${NEW}（${TAG}，${DATE}）"
printf '%s' "$BODY"

if [ "$DRY" = "1" ]; then
  echo "==> --dry-run：未改动任何文件"
  exit 0
fi

# ---- 先确认能编译 ------------------------------------------------------------
# 推一个编译不过的 tag 比不发版更糟：别人 clone 下来第一件事就是构建。
if [ "$SKIP_BUILD" = "1" ]; then
  echo "==> 跳过编译校验"
else
  echo "==> 校验编译"
  go build -o "$(mktemp -u)" ./cmd/voidxo || die "编译失败，已中止发版"
fi

# ---- 写入 VERSION 与 CHANGELOG ----------------------------------------------
printf '%s\n' "$NEW" > VERSION

TMP_BLOCK="$(mktemp)"
{
  echo "## ${TAG}（${DATE}）"
  echo
  printf '%s' "$BODY"
} > "$TMP_BLOCK"

# 新版本段插在第一个 `## v` 之前；没有历史段落时直接追加到文末
TMP_OUT="$(mktemp)"
awk -v blockfile="$TMP_BLOCK" '
  !inserted && /^## v/ {
    while ((getline line < blockfile) > 0) print line
    print ""
    inserted = 1
  }
  { print }
  END {
    if (!inserted) {
      print ""
      while ((getline line < blockfile) > 0) print line
    }
  }
' CHANGELOG.md > "$TMP_OUT"
mv "$TMP_OUT" CHANGELOG.md
rm -f "$TMP_BLOCK"

# ---- 提交、打 tag、推送 ------------------------------------------------------
git add -A
git commit -q -m "chore(release): ${TAG}" -m "$(printf '%s' "$BODY")"
git tag -a "$TAG" -m "voidxo ${NEW}"

echo "==> 已提交并打 tag ${TAG}"

if [ "$PUSH" = "0" ]; then
  echo "==> --no-push：本地已完成，推送用 git push origin ${BRANCH} --follow-tags"
  exit 0
fi

if ! git remote get-url origin >/dev/null 2>&1; then
  echo "==> 未配置 origin，跳过推送。配置后执行：git push -u origin ${BRANCH} --follow-tags"
  exit 0
fi

echo "==> 推送 ${BRANCH} 与 ${TAG}"
git push origin "$BRANCH"
git push origin "$TAG"

echo
echo "完成：${TAG}（${DATE}）"
