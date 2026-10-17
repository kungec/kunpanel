<div align="center">

<img src="web/static/logo.png" width="88" alt="坤面板">

# 坤面板 KunPanel

**轻量级服务器运维管理面板 —— Go 单二进制,开箱即用**

[![](https://img.shields.io/badge/版本-v2.75.2-3b82f6?style=flat-square)](https://github.com/kungec/kunpanel/releases)
[![](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat-square)](https://golang.org)
[![](https://img.shields.io/badge/平台-Linux%20%7C%20Windows-lightgrey?style=flat-square)](./README.md)
[![](https://img.shields.io/badge/License-MIT-green?style=flat-square)](./LICENSE)
[![](https://img.shields.io/badge/QQ群-307386089-eb1923?style=flat-square)](#交流群)
[![](https://img.shields.io/badge/📖_使用教程-TUTORIAL-8a2be2?style=flat-square)](docs/TUTORIAL.md)

**🎉 隆重感谢金牌赞助商 🎉**

[![跨境 VPS · 专注家宽住宅 IP 云服务器](docs/sponsor-banner.svg)](https://www.kjvps.com)

💬 交流 QQ 群:**`307386089`** —— 反馈 bug / 功能建议 / 运维经验交流

</div>

---

> 📖 **[完整使用教程(TUTORIAL.md)](docs/TUTORIAL.md)** —— 首次登录、建站、SSL、迁移、WAF、计划任务、FAQ,手把手图文步骤。

---

## 🔄 面板一键迁移(宝塔 / 1Panel 双向互通)

从宝塔或 1Panel 换到坤面板(或换回去),**全程自动化、随时可回滚**:

### 宝塔 → 坤面板
- **远程迁移,无脑傻瓜式**:只需输入宝塔服务器的 **IP + root 密码**,自动 SSH 对接、扫描站点、搬运文件
- **一键搬全家**:域名、网站文件、SSL 证书、伪静态/自定义配置(**逐条保真保留,不是重新生成**)、计划任务、数据库(可选),PHP 站点版本自动映射并自动装好对应运行时
- **零停机切换**:迁移后反向代理上游自动回源旧机,DNS 切换前网站照常访问;证书导入后异步重签接管续期
- **随时可回滚**:迁移不动宝塔原有站点与数据,宝塔原计划任务自动注释防双跑

### 1Panel → 坤面板
- **本机迁移**:自动识别 1Panel(v1/v2),迁移 OpenResty 站点(静态/反代/PHP)、SSL 证书与 MySQL 数据库(从 1Panel 容器 mysqldump 导入);静态站文件自动拷贝、反代上游自动改写并建端口转发
- **远程迁移**:同样只需输入 1Panel 服务器的 **IP + root 密码**,SSH 自动对接流式搬运;反代/PHP 上游回源远程机,**DNS 切换前零停机**
- **流量切换带回滚**:切换时自动停用 1Panel OpenResty(禁自启),逐域名×逐路径验证状态码,**任何异常自动回滚**

### 坤面板 → 宝塔 / 1Panel(反向导出)
- 站点一键导出为两个面板可直接生效的形态(目录 + 配置 + 证书 + 自动重载)

## ⚡ 一键安装

### Linux(自适应发行版与架构)

支持 Ubuntu / Debian / CentOS / RHEL / Rocky / Alma / Fedora / openSUSE / Arch / Alpine 等主流发行版,
x86_64 / aarch64 / armv7 三架构,自动识别 systemd / OpenRC / SysV:

```bash
curl -fsSL https://raw.githubusercontent.com/kungec/kunpanel/main/scripts/install.sh | bash
```

安装完成自动输出:**随机端口 + 随机安全入口 + 随机管理员密码**。
国内服务器安装与「在线更新」均内置 GitHub 加速兜底,不必科学上网。

<details>
<summary>📦 安装参数与卸载</summary>

```bash
bash install.sh --port 20000 --username admin --password yourpwd   # 自定义初始化
bash install.sh --with-docker            # 同时安装 Docker 引擎(自动配国内镜像源)
bash install.sh --mirror aliyun          # Docker 使用阿里云镜像源
bash install.sh --proxy https://gh-proxy.com/   # GitHub 下载加速
bash install.sh --version v2.75.2        # 安装指定版本
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
| **网站管理** | **1Panel 一键迁移** |
| ![](docs/screenshots/websites.png) | ![](docs/screenshots/1panel-migrate.png) |
| **安全中心(WAF)** | **监控中心(拨测)** |
| ![](docs/screenshots/security.png) | ![](docs/screenshots/monitor.png) |
| **文件管理** | **容器管理** |
| ![](docs/screenshots/files.png) | ![](docs/screenshots/docker.png) |

## ✨ 功能全景

### 🔄 面板迁移
- **宝塔 / 1Panel 双向互通**:本机迁移、远程 SSH 迁移、反向导出,全流程任务化带实时日志
- 站点文件 / SSL 证书 / 伪静态 / 自定义规则保真迁移,数据库可选导入并重建账号
- 流量切换自动停用源面板网关(禁自启)、改绑 80/443、**逐域名×逐路径状态码比对**,异常自动回滚

### 🛡 安全
- **WAF 防火墙**(OpenResty + Lua):CC 攻击防御 **七种模式**(限流 / 5 秒跳转 / JS 无感 / Cloudflare 风格 Verifying / 滑块 / 点击 / 算术验证码)——**人机验证模式选完即全站启用**,首次访问先验证、通过后免验,阈值作为已验证访客的频率上限;IP 黑白名单(**支持 CIDR 网段**)、UA 黑名单与搜索引擎蜘蛛白名单、敏感文件与危险方法拦截、SQL 注入 / XSS / 文件包含 / 命令执行检测、**自定义正则规则**(热更新)
- **自动封禁**:违规超阈值的公网 IP 自动拉黑(私网强制排除防误伤),拦截日志一键封禁;**容器端口 DOCKER-USER 管控**(堵 Docker 绕过系统防火墙的口子)
- **一键安全自检**:9 项体检评分;SSH 加固;登录限流 + PNG 验证码 + TOTP 两步验证 + 随机安全入口;ICMP ping 开关

### 🌐 网站
- 静态 / PHP / 反向代理三类型,**支持随时互相转换**(转换即生效,数据保留)
- 反向代理**负载均衡**(多上游 + 权重)、**自定义发送域名 Host 头**(默认访客原始域名,可指定,上游按域名区分站点时免改上游配置)
- HTTP/2、HSTS、Gzip、浏览器缓存、限速、防盗链、防恶意解析、维护模式、安全响应头、伪静态预设、301 重定向
- Let's Encrypt 一键申请与自动续期(支持测试环境预签);证书上传 / 下载 / 自签
- **网站流量统计**(PV/UV/流量/Top 排行);站点日志独立落盘 + 一键轮转归档
- 站点备份:服务端留存 / 打包下载 / 上传恢复(原子化,失败不伤原站)

### 🗄 数据库 · 应用 · 容器
- MySQL / MariaDB / PostgreSQL / Redis 管理:SQL 控制台、备份导入导出、root 改密、phpMyAdmin 快捷入口
- 应用商店 **17+ 应用**一键部署(WordPress / Nginx / OpenResty / 多版本 PHP / MySQL / Redis / 坤发卡 / NAT Cloud 等),官方多版本可选;**自定义应用**(compose 在线校验)
- 容器 / 镜像 / 网络 / 卷管理,容器终端,资源限制热更新,日志轮转配置

### 📂 文件 · 任务 · 监控
- **专业代码编辑器**:CodeMirror 高亮 13 种语言、快捷键、**历史版本回滚**、全屏;文件名 / 内容全文搜索
- 文件管理:拖拽上传 / 远程下载 / 压缩解压 / 权限 / **回收站(可恢复、定时清理)** / 图片预览
- 计划任务七类型(Shell / 目录备份 / 日志切割 / 数据库备份 / 回收站清理 / 面板备份 / 容器日志清理)+ 可视化执行周期,支持**备份完成后自动上传 SFTP 远程**
- **网站拨测**:每分钟探测 HTTP/TCP,连续失败防抖判宕,状态翻转推送告警,24h 可用率与延迟历史
- 磁盘 IO 与网络监控(24h 历史,异常退出不丢数据);**后台任务中心**(长任务实时进度条)+ 消息中心 + 每日威胁摘要
- **告警推送**:邮件 / 钉钉 / 飞书 / Webhook

### ⚡ 极致轻量
- Go 单二进制 + 原生前端(无 Node 运行时),空载内存 **约 12MB**、CPU **<0.5%**
- Docker 状态轮询自适应降频(操作时 8 秒、空闲 60 秒),后台资源占用可忽略
- 全 API 实测毫秒级响应;GC 调参 + 内存软上限,7×24 长驻不飘

## 🔒 安全设计

| 维度 | 措施 |
|---|---|
| 认证 | PNG 图形验证码、IP + 用户名双维度限流、TOTP 两步验证、随机安全入口 |
| 会话 | HMAC-SHA256 签名令牌,改密即轮换密钥强制下线 |
| 注入 | 域名白名单、compose 参数转义、解压 ZipSlip 防护、SQL 标识符白名单、反代 Host 头注入防护 |
| 传输 | CSP 脚本哈希白名单(无 unsafe-inline)、HSTS 可选、面板 HTTPS;远程迁移 SSH 指纹 TOFU 校验 |
| 供应链 | 升级包 SHA-256 校验和验证(直连 GitHub 优先,国内回退加速代理并明确标注信任来源) |
| 沙箱 | systemd NoNewPrivileges / ProtectSystem / ReadWritePaths 白名单 / 私网自动封禁排除 |
| 审计 | 登录与变更操作全留痕、真实状态码、安全回归测试锁定 |

> ⚠️ 注意:Docker 发布的端口**不经过** ufw / firewalld( iptables DOCKER 链直通),
> 请使用面板「容器端口管控」或 WAF 的 DOCKER-USER 管控功能限制外部访问。

## 🛠 从源码构建

```bash
git clone https://github.com/kungec/kunpanel.git && cd kunpanel
bash build.sh v2.75.2     # 产出 linux(amd64/arm64/arm) + windows 全平台安装包
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
