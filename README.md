<div align="center">

<img src="web/static/logo.png" width="88" alt="坤面板">

# 坤面板 KunPanel

**轻量级服务器运维管理面板 —— Go 单二进制,开箱即用**

[![](https://img.shields.io/badge/版本-v2.57.1-3b82f6?style=flat-square)](https://github.com/kungec/kunpanel/releases)
[![](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat-square)](https://golang.org)
[![](https://img.shields.io/badge/平台-Linux%20%7C%20Windows-lightgrey?style=flat-square)](./README.md)
[![](https://img.shields.io/badge/License-MIT-green?style=flat-square)](./LICENSE)
[![](https://img.shields.io/badge/QQ群-307386089-eb1923?style=flat-square)](#交流群)

**🎉 隆重感谢金牌赞助商 🎉**

[![跨境 VPS · 专注家宽住宅 IP 云服务器](docs/sponsor-banner.svg)](https://www.kjvps.com)

💬 交流 QQ 群:**`307386089`** —— 反馈 bug / 功能建议 / 运维经验交流

</div>

---

## ⚡ 一键安装

### Linux(自适应发行版与架构)

支持 Ubuntu / Debian / CentOS / RHEL / Rocky / Alma / Fedora / openSUSE / Arch / Alpine 等主流发行版,
x86_64 / aarch64 / armv7 三架构,自动识别 systemd / OpenRC / SysV:

```bash
curl -fsSL https://raw.githubusercontent.com/kungec/kunpanel/main/scripts/install.sh | bash
```

安装完成自动输出:**随机端口 + 随机安全入口 + 随机管理员密码**。

<details>
<summary>📦 安装参数与卸载</summary>

```bash
bash install.sh --port 20000 --username admin --password yourpwd   # 自定义初始化
bash install.sh --with-docker            # 同时安装 Docker 引擎
bash install.sh --mirror aliyun          # Docker 使用阿里云镜像源
bash install.sh --proxy https://gh-proxy.com/   # GitHub 下载加速
bash install.sh --version v2.57.1        # 安装指定版本
bash install.sh --offline ./kunpanel_linux_amd64.tar.gz   # 离线安装(含 SHA-256 校验)
bash install.sh --uninstall              # 卸载
```
</details>

### Windows

下载 [Release](https://github.com/kungec/kunpanel/releases) 中的 `kunpanel_windows_amd64.zip`,
解压后**右键以管理员身份运行 `install.bat`** —— 自动注册 Windows 服务、放行防火墙并输出访问地址。

### 🐳 Docker

```bash
docker build -t kunpanel:latest .
docker run -d --name kunpanel -p 20000:20000 \
  -v /opt/kunpanel/data:/opt/kunpanel/data \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --restart unless-stopped kunpanel:latest
```

## 🖼 界面预览

| 概览 | 应用商店 |
|---|---|
| ![](docs/screenshots/dashboard.png) | ![](docs/screenshots/store.png) |
| **网站管理** | **文件管理** |
| ![](docs/screenshots/websites.png) | ![](docs/screenshots/files.png) |
| **安全中心(WAF)** | **监控中心** |
| ![](docs/screenshots/security.png) | ![](docs/screenshots/monitor.png) |

## ✨ 功能全景

### 🛡 安全
- **WAF 防火墙**(OpenResty + Lua):CC 攻击防御 **七种人机验证模式**(限流 / 5 秒跳转 / JS 无感 / Cloudflare 风格 Verifying / 滑块 / 点击 / 算术验证码)、IP 黑白名单(**支持 CIDR 网段**)、UA 黑名单与搜索引擎蜘蛛白名单(可选严格反向 DNS 验证)、敏感文件与危险方法拦截、SQL 注入 / XSS / 文件包含 / 命令执行检测、**自定义正则规则**(热更新)
- **自动封禁**:违规超阈值的公网 IP 自动拉黑(私网强制排除防误伤),拦截日志一键封禁
- **一键安全自检**:9 项体检评分;SSH 加固;登录限流 + PNG 验证码 + TOTP 两步验证 + 随机安全入口

### 🌐 网站
- 静态 / PHP / 反向代理三类型;HTTP/2、HSTS、Gzip、浏览器缓存、限速、防盗链、防恶意解析、维护模式、安全响应头、伪静态预设、301 重定向
- 反向代理**负载均衡**(多上游 + 权重);Let's Encrypt 一键申请与自动续期;证书上传 / 下载 / 自签
- 站点备份:服务端留存 / 打包下载 / 上传恢复(原子化,失败不伤原站)

### 🗄 数据库 · 应用 · 容器
- MySQL / MariaDB / PostgreSQL / Redis 管理:SQL 控制台、备份导入导出、root 改密、phpMyAdmin 快捷入口
- 应用商店 14+ 应用一键部署,官方多版本可选;**自定义应用**(compose 在线校验)
- 容器 / 镜像 / 网络 / 卷管理,容器终端,资源限制热更新

### 📂 文件 · 任务 · 监控
- **专业代码编辑器**:CodeMirror 高亮 13 种语言、快捷键、**历史版本回滚**、全屏
- 文件管理:拖拽上传 / 远程下载 / 压缩解压 / 权限 / 回收站 / 图片预览
- 计划任务七类型 + 可视化执行周期;磁盘 IO 与网络监控;拨测;告警推送(邮件 / 钉钉 / 飞书 / Webhook)
- **后台任务中心** + 消息中心 + 每日威胁摘要

## 🔒 安全设计

| 维度 | 措施 |
|---|---|
| 认证 | PNG 图形验证码、IP + 用户名双维度限流、TOTP 两步验证、随机安全入口 |
| 会话 | HMAC-SHA256 签名令牌,改密即轮换密钥强制下线 |
| 注入 | 域名白名单、compose 参数转义、解压 ZipSlip 防护、SQL 标识符白名单 |
| 传输 | CSP 脚本哈希白名单(无 unsafe-inline)、HSTS 可选、面板 HTTPS |
| 沙箱 | systemd NoNewPrivileges / ProtectSystem / 私网自动封禁排除 |
| 审计 | 登录与变更操作全留痕、真实状态码、安全回归测试锁定 |

> ⚠️ 注意:Docker 发布的端口**不经过** ufw / firewalld( iptables DOCKER 链直通),
> 请使用面板「容器端口管控」或 WAF 的 DOCKER-USER 管控功能限制外部访问。

## 🛠 从源码构建

```bash
git clone https://github.com/kungec/kunpanel.git && cd kunpanel
bash build.sh v2.57.1     # 产出 linux(amd64/arm64/arm) + windows 全平台安装包
```

Go 1.23+,前端为原生 JS(`go:embed` 内嵌),无 Node 构建步骤。

## 🤝 赞助商

<div align="center">

[![跨境 VPS](docs/sponsor-banner.svg)](https://www.kjvps.com)

**感谢 [跨境 VPS](https://www.kjvps.com) 为坤面板提供服务器资源赞助**
家宽住宅 IP · 原生 IP · 大带宽 · 高防 —— [www.kjvps.com](https://www.kjvps.com)

</div>

## 📄 License

[MIT](LICENSE) · 交流 QQ 群:**307386089**

