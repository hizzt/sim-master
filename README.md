<div align="center">
  # sim-master

  面向 Linux 蜂窝设备的 SIM / eSIM、短信、网络与 VoWiFi 管理平台
</div>

sim-master 是一套面向 Debian 蜂窝 CPE、随身 WiFi、软路由和实验设备的 Web 管理系统。它通过 ModemManager、NetworkManager、QMI 和 AT 指令管理 modem，并提供多模块、SIM/eSIM、蜂窝网络、短信、通知、自动化、出口代理和 IMS/VoWiFi 诊断能力。

项目名称已改为 **sim-master**。为兼容既有设备、OTA 包和 systemd 部署，运行时仍保留以下名称：

- 安装目录：`/opt/simadmin`
- 主程序：`simadmin`
- systemd 服务：`simadmin.service`
- VoWiFi helper：`simadmin-vowifi-helper`
- modem 请求头：`X-SimAdmin-Modem-Id`

> 配置开关不等于实际能力。IMS、VoWiFi、语音和短信是否可用，最终取决于 modem 固件、SIM、运营商策略、网络出口和实际协议交互。

## 主要功能

| 模块 | 能力 |
|---|---|
| 多模块管理 | 自动发现 ModemManager 设备，以稳定 modem ID 绑定每个请求 |
| SIM / eSIM | SIM 标识与锁卡状态、号码和短信中心缓存、eUICC Profile 管理 |
| 蜂窝网络 | 注册状态、服务小区与邻区、运营商扫描、APN、射频模式和频段管理 |
| 短信与电话 | 短信收发、SQLite 历史记录、通话控制与通话记录 |
| IMS / VoWiFi | IMS、ePDG、EAP-AKA、SWu 和 P-CSCF 分层诊断，不混淆配置与注册状态 |
| 网络代理 | 按 modem 网卡绑定 SOCKS5 / HTTP 代理，支持认证、启停和流量统计 |
| 设备网络 | WLAN 扫描与连接、IPv4/IPv6 状态、DDNS 同步 |
| 通知中心 | Webhook、邮件和消息平台通道，支持规则、模板、日志与测试发送 |
| 自动化 | 定时或周期任务、发送短信、备份、重启基带或设备、执行日志 |
| 备份与 OTA | 配置与业务数据备份、快照回滚、OTA 上传、校验和在线更新 |
| 安全 | 首次设置管理员密码、会话管理、密码策略和登录保护 |

## 技术架构

```text
浏览器
  │ HTTP :3000
  ▼
React + Vite + Material UI
  │ /api
  ▼
Rust + Axum + SQLite
  ├── system D-Bus ── ModemManager / NetworkManager
  ├── mmcli / qmicli / nmcli / AT
  ├── lpac ───────── eUICC Profile
  └── VoWiFi helper ─ IKEv2 / EAP-AKA / SWu / IMS
```

| 目录 | 说明 |
|---|---|
| `backend/` | Rust/Axum 后端、SQLite、modem 与系统集成 |
| `frontend/` | React/Vite Web 控制台 |
| `vowifi-helper/` | Go 编写的 SWu、IMS、语音和短信 helper |
| `scripts/` | 构建、OTA、ADB 部署与 systemd 单元 |
| `docs/` | 安装、运行环境、开发和更新记录 |
| `bruno-api/` | Bruno REST API 调试集合 |

## 运行要求

- Linux，推荐 Debian 11/12 或对应 Ubuntu 版本
- root 权限，或等价的 D-Bus、网络设备和 `SO_BINDTODEVICE` 能力
- 宿主机运行 ModemManager；设备网络功能还需要 NetworkManager
- 按功能安装 `mmcli`、`qmicli`、`nmcli`、`ip`、`iptables`、`tar`、`unzip`
- 一个可被 Linux 和 ModemManager 正确识别的蜂窝 modem

没有 modem 时服务和 Web 页面仍可启动，但蜂窝相关接口会不可用或返回诊断错误。

## Docker Compose 部署

Docker 适合快速部署和构建验证。由于项目需要访问宿主机 modem、system D-Bus 和网络命名空间，默认 Compose 使用 `privileged` 与 host network；请只在受信任的主机上运行。

### 1. 获取私有仓库

```bash
gh auth login
gh repo clone techblack/sim-master
cd sim-master
```

### 2. 本地构建并启动

```bash
docker compose up -d --build
docker compose ps
curl http://127.0.0.1:3000/api/health
```

访问：`http://<主机地址>:3000`

首次进入页面时设置管理员密码，项目没有默认密码。

### 3. 常用运维命令

```bash
# 查看日志
docker compose logs -f sim-master

# 重启
docker compose restart sim-master

# 更新源码并重建
git pull
docker compose up -d --build

# 停止，保留持久化 volume
docker compose down
```

不要在需要保留数据时执行 `docker compose down -v`。Compose volume 挂载到 `/opt/simadmin`，保存 SQLite 数据库、代理配置及运行时文件。

### 使用 GHCR 镜像

仓库为私有时，GHCR 镜像也需要认证：

```bash
echo "$GHCR_TOKEN" | docker login ghcr.io -u techblack --password-stdin
docker compose pull
docker compose up -d --no-build
```

`GHCR_TOKEN` 至少需要 `read:packages` 权限。

## 传统 systemd 部署

传统部署继续使用 `/opt/simadmin` 和 `simadmin.service`，适合需要直接控制宿主机 ModemManager、NetworkManager 和系统服务的设备。

公开仓库或可直接访问 Release 时，可以使用安装脚本：

```bash
curl -fsSL https://raw.githubusercontent.com/techblack/sim-master/main/install_latest.sh | sudo sh
sudo systemctl enable --now simadmin.service
curl http://127.0.0.1:3000/api/health
```

当前仓库为私有时，未经认证的 `raw.githubusercontent.com` 和 Release 下载会返回错误。请通过 `gh` 克隆后按 [`docs/install.md`](./docs/install.md) 部署，或为安装脚本配置可认证的 Release 镜像。

常用服务命令：

```bash
systemctl status simadmin --no-pager
journalctl -u simadmin -f
systemctl restart simadmin
```

## 从源码构建

推荐工具链：

- Rust stable
- Go 1.26+
- Node.js 24
- pnpm 9

```bash
# 前端
cd frontend
corepack enable
pnpm install --frozen-lockfile
pnpm run build:full

# Rust 后端
cd ../backend
cargo test --locked
cargo build --release --locked

# VoWiFi helper
cd ../vowifi-helper
go test ./...
go build -trimpath -ldflags='-s -w' -o simadmin-vowifi-helper ./
```

后端可执行文件位于 `backend/target/release/simadmin`，前端产物位于 `frontend/dist/`。

## Release 与多架构构建

`.github/workflows/release.yml` 自动生成：

- `sim-master-x86_64.tar.gz`
- `sim-master-arm64.tar.gz`
- 对应 SHA-256 校验文件
- `ghcr.io/techblack/sim-master` 的 `linux/amd64`、`linux/arm64` 多架构镜像

推送版本 tag 即可触发：

```bash
git tag v1.1.7
git push origin v1.1.7
```

也可以在 GitHub Actions 页面手动运行并指定版本号。Release 包内保留 `simadmin` 二进制名称，以兼容原有安装、OTA 和服务脚本。

## 数据与备份

传统安装的核心路径：

| 路径 | 内容 |
|---|---|
| `/opt/simadmin/data.db` | 登录、设置、短信、任务日志和业务数据 |
| `/opt/simadmin/proxy.json` | 蜂窝出口代理配置 |
| `/opt/simadmin/www/` | Web 静态资源 |
| `/opt/simadmin/backups/` | 本地备份与快照 |
| `/opt/simadmin/lpac/` | 私有 lpac helper 与依赖 |

升级、迁移或清理容器 volume 前，请先通过 Web 的“数据备份与恢复”页面导出备份。

## 能力边界与安全说明

本项目会直接操作 SIM、蜂窝注册、APN、频段、飞行模式、NetworkManager、systemd、网络设备和 OTA 文件。错误配置可能导致断网、漫游计费、modem 无法注册或需要手动恢复。

- 仅在你拥有和管理的设备上使用。
- 频段能力以 ModemManager 和实际固件暴露的能力为准。
- SWu Child SA 建立不代表 IMS 已注册，也不代表语音或短信一定可用。
- IMS 注册、Security-Agreement、语音/RTP 和 SMS over IMS 是相互独立的能力。
- 容器部署需要高权限，不应运行来源不明的镜像或代码。
- 修改 modem 固件、NV、IMEI、EDL 或 USB ID 属于高风险操作，不在普通部署流程内。

## 文档

- [安装与卸载](./docs/install.md)
- [运行环境与系统管理](./docs/environment.md)
- [开发者指南](./docs/developer.md)
- [版本更新记录](./docs/changelog.md)
- [REST API / Bruno 集合](./bruno-api/README.md)
- [Agent 协作约定](./Agents.md)

## License

本项目基于 [GNU General Public License v3.0](./LICENSE) 发布。分发修改版本时必须保留版权和许可证声明，并按 GPLv3 提供对应源代码。

第三方组件的许可证和声明见 `vowifi-helper/THIRD_PARTY_NOTICES.md` 及各 vendored 目录中的许可证文件。
