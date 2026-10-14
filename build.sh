#!/bin/bash
# ============================================================================
#  坤面板 KunPanel - 交叉编译 & 打包发布脚本
#  产物: release/ 目录下的 linux(amd64/arm64/arm) 与 windows(amd64) 安装包
# ============================================================================
set -e
cd "$(dirname "$0")"

VERSION="${1:-v1.0.0}"
OUT="release"
LDFLAGS="-s -w -X main.version=${VERSION}"

echo "==> 构建坤面板 ${VERSION}"

TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "linux/arm"
  "windows/amd64"
)

rm -rf "$OUT"
mkdir -p "$OUT"

for target in "${TARGETS[@]}"; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  NAME="kunpanel"
  [[ "$GOOS" == "windows" ]] && NAME="kunpanel.exe"
  echo "---- 编译 $GOOS/$GOARCH ----"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/pkg_$GOOS-$GOARCH/$NAME" .
done

echo "---- 打包 ----"
for d in "$OUT"/pkg_*; do
  base=$(basename "$d")
  os="${base#pkg_}"; os="${os%-*}"
  arch="${base##*-}"
  name="kunpanel_${VERSION}_${os}_${arch}"
  if [[ "$os" == "windows" ]]; then
    cp scripts/install.ps1 scripts/install.bat "$d/"
    (cd "$OUT" && zip -qr "${name}.zip" "$(basename "$d")")
    # 无版本命名副本:供 GitHub releases/latest/download 直链
    (cd "$OUT" && cp "${name}.zip" "kunpanel_windows_${arch}.zip")
  else
    cp scripts/install.sh "$d/"
    tar -czf "$OUT/${name}.tar.gz" -C "$OUT" "$(basename "$d")"
    (cd "$OUT" && cp "${name}.tar.gz" "kunpanel_${os}_${arch}.tar.gz")
  fi
  rm -rf "$d"
done

echo "==> 完成,产物清单:"
ls -lh "$OUT" | grep -v '^total' | awk '{print "  " $NF "  (" $5 ")"}'
echo "==> 校验和:"
( cd "$OUT" && shasum -a 256 ./*.tar.gz ./*.zip 2>/dev/null || true )
