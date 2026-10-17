#!/bin/bash
# ============================================================================
#  坤面板 KunPanel - Linux 一键安装脚本
#  支持: Ubuntu / Debian / CentOS / RHEL / Rocky / Alma / Fedora / openSUSE
#        Arch / Manjaro / Alpine / Gentoo / 以及任意 systemd/OpenRC/SysV 发行版
#  架构: x86_64(amd64) / aarch64(arm64) / armv7
#
#  在线安装(始终最新版):
#    curl -fsSL https://raw.githubusercontent.com/kungec/kunpanel/main/scripts/install.sh | bash
#  可选参数:
#    bash install.sh --port 20000 --username admin --password yourpwd
#                    --with-docker            同时安装 Docker 引擎
#                    --mirror aliyun          Docker 使用阿里云镜像源
#                    --proxy https://gh-proxy.com/   GitHub 下载加速
#                    --version v2.46.4        指定版本(默认 latest 取最新)
#                    --offline /path/kunpanel.tar.gz   离线安装
#                    --uninstall              卸载
# ============================================================================
set -e

# ---------------- 默认配置 ----------------
KP_VERSION="latest"
KP_PORT=""
KP_USERNAME=""
KP_PASSWORD=""
KP_ENTRANCE=""
KP_WITH_DOCKER=false
KP_DOCKER_MIRROR=""
KP_PROXY=""
KP_OFFLINE=""
KP_UNINSTALL=false
KP_DIR="/opt/kunpanel"
KP_SERVICE="kunpanel"
REPO_URL="https://github.com/kungec/kunpanel/releases/download"

# ---------------- 参数解析 ----------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port) KP_PORT="$2"; shift 2 ;;
    --username) KP_USERNAME="$2"; shift 2 ;;
    --password) KP_PASSWORD="$2"; shift 2 ;;
    --entrance) KP_ENTRANCE="$2"; shift 2 ;;
    --version) KP_VERSION="$2"; shift 2 ;;
    --with-docker) KP_WITH_DOCKER=true; shift ;;
    --mirror) KP_DOCKER_MIRROR="$2"; shift 2 ;;
    --proxy) KP_PROXY="$2"; shift 2 ;;
    --offline) KP_OFFLINE="$2"; shift 2 ;;
    --uninstall) KP_UNINSTALL=true; shift ;;
    -h|--help)
      sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

RED='\033[31m'; GREEN='\033[32m'; YELLOW='\033[33m'; BLUE='\033[36m'; NC='\033[0m'
info()  { echo -e "${BLUE}[信息]${NC} $*"; }
ok()    { echo -e "${GREEN}[成功]${NC} $*"; }
warn()  { echo -e "${YELLOW}[警告]${NC} $*"; }
fail()  { echo -e "${RED}[失败]${NC} $*"; exit 1; }

# ---------------- root 检查 ----------------
if [[ $EUID -ne 0 ]]; then
  fail "请使用 root 用户执行安装(sudo bash install.sh)"
fi

# ---------------- 卸载 ----------------
if $KP_UNINSTALL; then
  info "停止并卸载坤面板 ..."
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop $KP_SERVICE 2>/dev/null || true
    systemctl disable $KP_SERVICE 2>/dev/null || true
    rm -f /etc/systemd/system/$KP_SERVICE.service
    systemctl daemon-reload
  fi
  if command -v rc-service >/dev/null 2>&1; then
    rc-service $KP_SERVICE stop 2>/dev/null || true
    rc-update delete $KP_SERVICE 2>/dev/null || true
    rm -f /etc/init.d/$KP_SERVICE
  fi
  if [[ -f /etc/init.d/$KP_SERVICE ]]; then
    /etc/init.d/$KP_SERVICE stop 2>/dev/null || true
    rm -f /etc/init.d/$KP_SERVICE
  fi
  pkill -f "$KP_DIR/bin/kunpanel" 2>/dev/null || true
  echo -n "是否同时删除数据目录 $KP_DIR(网站/应用数据将丢失)?[y/N] "
  read -r ans
  if [[ "$ans" == "y" || "$ans" == "Y" ]]; then
    rm -rf "$KP_DIR"
  fi
  ok "坤面板已卸载"
  exit 0
fi

# ---------------- 环境识别 ----------------
ARCH_RAW=$(uname -m)
case "$ARCH_RAW" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  armv7l|armv7|armhf) ARCH="arm" ;;
  *) fail "不支持的 CPU 架构: $ARCH_RAW" ;;
esac

OS_NAME=$(uname -s)
[[ "$OS_NAME" == "Linux" ]] || fail "本脚本仅支持 Linux,Windows 请使用 install.ps1"

DISTRO="unknown"
PKG=""
if [[ -f /etc/os-release ]]; then
  . /etc/os-release
  DISTRO="${ID:-unknown}"
fi
# 包管理器(仅用于安装 curl 等依赖)
for pm in apt-get dnf yum apk pacman zypper; do
  if command -v "$pm" >/dev/null 2>&1; then PKG="$pm"; break; fi
done

info "系统: ${DISTRO} $(uname -m) | 包管理器: ${PKG:-无} | 架构: linux_${ARCH}"

# ---------------- 依赖检查 ----------------
if ! command -v curl >/dev/null 2>&1; then
  info "安装 curl ..."
  case "$PKG" in
    apt-get) apt-get update -y && apt-get install -y --no-install-recommends curl ;;
    dnf) dnf install -y curl ;;
    yum) yum install -y curl ;;
    apk) apk add --no-cache curl ;;
    pacman) pacman -Sy --noconfirm curl ;;
    zypper) zypper --non-interactive install curl ;;
    *) fail "未找到 curl 且无法自动安装,请先手动安装 curl" ;;
  esac
fi
command -v curl >/dev/null 2>&1 || fail "curl 安装失败"

# tar/gzip:Rocky/AlmaLinux 等最小化镜像不预装,自动补装(解压安装包必需)
if ! command -v tar >/dev/null 2>&1 || ! command -v gzip >/dev/null 2>&1; then
  info "安装 tar / gzip ..."
  case "$PKG" in
    apt-get) apt-get update -y && apt-get install -y --no-install-recommends tar gzip ;;
    dnf) dnf install -y tar gzip ;;
    yum) yum install -y tar gzip ;;
    apk) apk add --no-cache tar gzip ;;
    pacman) pacman -Sy --noconfirm tar gzip ;;
    zypper) zypper --non-interactive install tar gzip ;;
    *) fail "未找到 tar/gzip 且无法自动安装,请先手动安装 tar" ;;
  esac
fi
command -v tar >/dev/null 2>&1 || fail "tar 安装失败"

# ---------------- 下载 ----------------
mkdir -p "$KP_DIR/bin" "$KP_DIR/data" "$KP_DIR/logs"
TMP_TGZ=$(mktemp /tmp/kunpanel.XXXXXX.tar.gz)

if [[ -n "$KP_OFFLINE" ]]; then
  [[ -f "$KP_OFFLINE" ]] || fail "离线包不存在: $KP_OFFLINE"
  info "使用离线包: $KP_OFFLINE"
  cp "$KP_OFFLINE" "$TMP_TGZ"
else
  # latest = 直接取最新 release 的无版本产物;指定版本则用带版本产物
  if [[ "$KP_VERSION" == "latest" ]]; then
    DL_URL="https://github.com/kungec/kunpanel/releases/latest/download/kunpanel_linux_${ARCH}.tar.gz"
  else
    DL_URL="$REPO_URL/$KP_VERSION/kunpanel_${KP_VERSION}_linux_${ARCH}.tar.gz"
  fi
  if [[ -n "$KP_PROXY" ]]; then
    DL_URL="${KP_PROXY}${DL_URL}"
  fi
  info "下载坤面板 $KP_VERSION (linux_${ARCH}) ..."
  info "地址: $DL_URL"
  curl -fSL --connect-timeout 15 --retry 3 -o "$TMP_TGZ" "$DL_URL" || fail "下载失败,请检查网络或使用 --proxy 加速"
  # SHA-256 校验(供应链完整性;--no-verify 或离线模式跳过)
  if [[ "${KP_NO_VERIFY:-}" != "1" ]]; then
    SUM_URL="${DL_URL%/*}/SHA256SUMS"
    [[ "$SUM_URL" == *"/latest/download/"* ]] && SUM_URL="https://github.com/kungec/kunpanel/releases/latest/download/SHA256SUMS"
    TMP_SUM=$(mktemp)
    SUM_SRC="${KP_PROXY:+$KP_PROXY}$SUM_URL"
    if curl -fsSL --connect-timeout 15 --retry 2 -o "$TMP_SUM" "$SUM_SRC"; then
      # 兼容 "./name" 与 "name" 两种行格式(构建脚本生成的是 ./ 前缀)
      EXPECT=$(grep -E " [*.]?\.?/?kunpanel_linux_${ARCH}\.tar\.gz$" "$TMP_SUM" | awk '{print $1}' | head -1)
      ACTUAL=$(sha256sum "$TMP_TGZ" | awk '{print $1}')
      if [[ -z "$EXPECT" ]]; then rm -f "$TMP_SUM"; fail "校验和中没有 kunpanel_linux_${ARCH}.tar.gz 的记录,已中止安装(供应链校验不可跳过;如确需强制安装请 --no-verify)"
      elif [[ "$EXPECT" != "$ACTUAL" ]]; then rm -f "$TMP_SUM"; fail "SHA-256 校验失败,安装包可能被篡改"
      else ok "SHA-256 校验通过"; fi
    else
      rm -f "$TMP_SUM"
      if [[ -z "$KP_PROXY" ]]; then
        fail "下载 SHA256SUMS 失败,已中止安装(供应链校验不可跳过;如确需强制安装请 --no-verify)"
      else
        fail "经代理下载 SHA256SUMS 失败,已中止安装;可尝试 --no-verify(不推荐)或去掉 --proxy 直连"
      fi
    fi
  fi
fi

info "解压并安装到 $KP_DIR/bin ..."
tar -xzf "$TMP_TGZ" -C "$KP_DIR/bin"
chmod +x "$KP_DIR/bin/kunpanel"
rm -f "$TMP_TGZ"

# ---------------- 初始化配置(端口/账号) ----------------
ENV_LINE=""
if [[ -n "$KP_PORT" ]]; then ENV_LINE="KP_PORT=$KP_PORT"; fi
# 首次运行生成配置(随机端口/随机入口/随机密码,可被参数覆盖)
if [[ ! -f "$KP_DIR/data/config.json" ]]; then
  FIRST_ENV=()
  [[ -n "$KP_PORT" ]] && FIRST_ENV+=("KP_PORT=$KP_PORT")
  [[ -n "$KP_USERNAME" ]] && FIRST_ENV+=("KP_USERNAME=$KP_USERNAME")
  [[ -n "$KP_PASSWORD" ]] && FIRST_ENV+=("KP_PASSWORD=$KP_PASSWORD")
  [[ -n "$KP_ENTRANCE" ]] && FIRST_ENV+=("KP_ENTRANCE=$KP_ENTRANCE")
  if [[ ${#FIRST_ENV[@]} -gt 0 ]]; then
    env "${FIRST_ENV[@]}" timeout 3 "$KP_DIR/bin/kunpanel" >/dev/null 2>&1 || true
  fi
fi

# ---------------- 服务管理 ----------------
have_systemd() { command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; }
have_openrc()  { command -v rc-service >/dev/null 2>&1; }

install_systemd() {
  cat > /etc/systemd/system/$KP_SERVICE.service <<EOF
[Unit]
Description=KunPanel Server Panel
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$KP_DIR
ExecStart=$KP_DIR/bin/kunpanel
Restart=always
RestartSec=3
LimitNOFILE=65535
Environment=KP_DATA_DIR=$KP_DIR/data
Environment=HOME=/root
Environment=GIN_MODE=release
# 安全加固
NoNewPrivileges=true
ProtectSystem=full
ReadWritePaths=/etc/ssh -/etc/sysctl.d /etc/ufw /etc/systemd/system
Environment="GOGC=50"
Environment="GOMEMLIMIT=256MiB"
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable $KP_SERVICE >/dev/null 2>&1 || true
  systemctl restart $KP_SERVICE || systemctl start $KP_SERVICE
  ok "已注册 systemd 服务: systemctl {start|stop|restart|status} $KP_SERVICE"
}

install_openrc() {
  cat > /etc/init.d/$KP_SERVICE <<EOF
#!/sbin/openrc-run
name="KunPanel"
description="KunPanel Server Panel"
command="$KP_DIR/bin/kunpanel"
command_background="yes"
directory="$KP_DIR"
pidfile="/run/\${RC_SVCNAME}.pid"
output_log="$KP_DIR/logs/panel.log"
error_log="$KP_DIR/logs/panel.log"

depend() {
  need net
  after firewall
}
EOF
  chmod +x /etc/init.d/$KP_SERVICE
  rc-update add $KP_SERVICE default
  rc-service $KP_SERVICE restart || rc-service $KP_SERVICE start
  ok "已注册 OpenRC 服务: rc-service $KP_SERVICE {start|stop|restart|status}"
}

install_initd() {
  cat > /etc/init.d/$KP_SERVICE <<EOF
#!/bin/bash
# KunPanel SysV init script
DAEMON=$KP_DIR/bin/kunpanel
PIDFILE=/var/run/$KP_SERVICE.pid
case "\$1" in
  start)
    echo -n "Starting KunPanel ... "
    start-stop-daemon -S -b -m -p \$PIDFILE -x \$DAEMON 2>/dev/null || \$DAEMON >/dev/null 2>&1 &
    echo "OK" ;;
  stop)
    echo -n "Stopping KunPanel ... "
    if [ -f \$PIDFILE ]; then kill \$(cat \$PIDFILE) 2>/dev/null; rm -f \$PIDFILE; fi
    pkill -f "\$DAEMON" 2>/dev/null
    echo "OK" ;;
  restart) \$0 stop; sleep 1; \$0 start ;;
  status)
    if pgrep -f "\$DAEMON" >/dev/null; then echo "KunPanel is running"; else echo "KunPanel is stopped"; fi ;;
  *) echo "Usage: \$0 {start|stop|restart|status}" ;;
esac
EOF
  chmod +x /etc/init.d/$KP_SERVICE
  /etc/init.d/$KP_SERVICE start
  ok "已注册 SysV 服务: service $KP_SERVICE {start|stop|restart|status}"
}

install_nohup() {
  # 无 init 系统(如 Docker 容器内)直接后台拉起
  nohup "$KP_DIR/bin/kunpanel" >> "$KP_DIR/logs/panel.log" 2>&1 &
  warn "未检测到 init 系统,已用 nohup 后台启动(容器环境建议使用 Docker 方式部署)"
}

if have_systemd; then
  install_systemd
elif have_openrc; then
  install_openrc
elif [[ -d /etc/init.d ]] && command -v start-stop-daemon >/dev/null 2>&1; then
  install_initd
else
  install_nohup
fi

# ---------------- 防火墙放行(尽力而为) ----------------
# 面板自身也会在首次初始化时放行(双层保险);此处等待并提取实际端口再次确认
PORT_HINT=""
if [[ -n "$KP_PORT" ]]; then
  PORT_HINT="$KP_PORT"
else
  for i in {1..15}; do
    if [[ -f "$KP_DIR/data/initial_password.txt" ]]; then
      PORT_HINT=$(grep -oE 'http://<服务器IP>:[0-9]+' "$KP_DIR/data/initial_password.txt" 2>/dev/null | grep -oE '[0-9]+$' | head -1)
      [[ -n "$PORT_HINT" ]] && break
    fi
    sleep 1
  done
fi
if [[ -n "$PORT_HINT" ]]; then
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw list 2>/dev/null | grep -q "$PORT_HINT/tcp" || ufw allow "$PORT_HINT/tcp" >/dev/null 2>&1
    ufw status 2>/dev/null | grep -q "$PORT_HINT/tcp" && info "已放行 ufw 端口 $PORT_HINT" || warn "ufw 放行端口 $PORT_HINT 失败,请手动执行: ufw allow $PORT_HINT/tcp"
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port="$PORT_HINT/tcp" >/dev/null 2>&1 && firewall-cmd --reload >/dev/null 2>&1 && info "已放行 firewalld 端口 $PORT_HINT"
  fi
else
  warn "未能自动检测面板端口(面板可能尚未完成初始化),请从上方面板地址查看端口并手动放行防火墙"
fi

# ---------------- Docker(可选) ----------------
if $KP_WITH_DOCKER; then
  if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    ok "Docker 已安装,跳过"
  else
    info "一键安装 Docker 引擎 ..."
    if [[ "$KP_DOCKER_MIRROR" == "aliyun" ]]; then
      curl -fsSL https://get.docker.com | sh -s -- --mirror Aliyun || warn "Docker 安装失败,可稍后在面板【容器】页重试"
    else
      curl -fsSL https://get.docker.com | sh || warn "Docker 安装失败,可稍后在面板【容器】页重试"
    fi
    if have_systemd; then systemctl enable --now docker 2>/dev/null || true
    elif have_openrc; then rc-update add docker boot 2>/dev/null || true; rc-service docker start 2>/dev/null || true
    fi
  fi
  # 容器日志轮转:不配置的话容器 json.log 会无限增长撑爆磁盘(对所有新容器生效)
  if [[ -f /etc/docker/daemon.json ]] && ! grep -q "max-size" /etc/docker/daemon.json 2>/dev/null; then
    python3 - <<'PYD' 2>/dev/null || true
import json
p = '/etc/docker/daemon.json'
try:
    d = json.load(open(p))
except Exception:
    d = {}
if 'log-driver' not in d:
    d['log-driver'] = 'json-file'
d.setdefault('log-opts', {})['max-size'] = '50m'
d['log-opts']['max-file'] = '3'
json.dump(d, open(p, 'w'), indent=2)
print('daemon-log-opts-ok')
PYD
    if grep -q "max-size" /etc/docker/daemon.json 2>/dev/null; then
      systemctl restart docker 2>/dev/null || true
      ok "容器日志轮转已配置(单容器 50MB×3)"
    fi
  fi
fi

# ---------------- 等待就绪并输出信息 ----------------
sleep 2
LOCAL_IP=$(ip -4 addr show scope global 2>/dev/null | grep -oE '(?<=inet )[0-9.]+' 2>/dev/null || true)
if [[ -z "$LOCAL_IP" ]]; then
  LOCAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
fi
[[ -z "$LOCAL_IP" ]] && LOCAL_IP="127.0.0.1"

echo
REAL_VER=$("$KP_DIR/bin/kunpanel" --version 2>/dev/null | awk '{print $2}')
[[ -z "$REAL_VER" ]] && REAL_VER="$KP_VERSION"
echo "=============================================================="
echo -e "  ${GREEN}坤面板 KunPanel $REAL_VER 安装完成!${NC}"
echo "=============================================================="
if [[ -f "$KP_DIR/data/initial_password.txt" ]]; then
  # 用本机 IP 替换占位符,输出完整面板地址(端口与随机入口以实际配置为准)
  sed "s|<服务器IP>|$LOCAL_IP|g" "$KP_DIR/data/initial_password.txt" | sed 's/^/  /'
  echo "--------------------------------------------------------------"
  echo "  数据目录: $KP_DIR/data"
  echo "  常用命令: systemctl {start|stop|restart|status} $KP_SERVICE"
  echo "  卸载:     bash $(basename "$0") --uninstall"
  echo "  提示:     每台服务器的面板端口与后台入口均随机生成,"
  echo "            直接访问 http://IP:端口 返回 404,请牢记上方面板地址"
else
  echo "  面板已在运行,初始账号信息: cat $KP_DIR/data/initial_password.txt"
fi
echo "=============================================================="
