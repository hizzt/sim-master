# sim-master 部署说明

sim-master 是面向 Linux/Debian 蜂窝设备的 SIM/eSIM Web 管理平台。后端通过 ModemManager system D-Bus 管理 modem，默认监听 `0.0.0.0:3000`；没有 modem 时页面仍可启动，但蜂窝功能无法使用。

## Docker Compose（推荐快速试用）

Docker 需要能访问宿主机的 system D-Bus、USB modem 和 NetworkManager。推荐在运行 ModemManager 的 Linux 主机上执行：

```bash
git clone https://github.com/techblack/sim-master.git
cd sim-master
docker compose up -d --build
curl http://127.0.0.1:3000/api/health
```

首次打开 `http://<主机地址>:3000` 时设置管理员密码。数据保存在 Compose volume `sim-master-state`，删除容器不会删除数据；如需备份可执行 `docker run --rm -v sim-master-state:/data -v "$PWD":/backup alpine tar czf /backup/sim-master-state.tgz -C /data .`。

Compose 使用 host network，因此不需要 `ports:` 映射；这也让 modem 的蜂窝网卡和 VoWiFi 隧道保持宿主机网络语义。容器以 root/privileged 运行是为了 ModemManager、NetworkManager、`SO_BINDTODEVICE`、iptables 和 AT/QMI 访问，请仅在受信任主机上使用。

## 预构建镜像

Release workflow 会生成 `linux/amd64` 与 `linux/arm64` 镜像并发布二进制压缩包。登录 GHCR 后可将 compose 的 `build:` 替换为：

```yaml
image: ghcr.io/techblack/sim-master:latest
```

私有镜像需要先执行 `docker login ghcr.io`。Release 二进制包内包含 `simadmin`、VoWiFi helper、`www/` 和第三方声明，可用于传统 `/opt/simadmin` 安装脚本。

## 传统 systemd 安装

仓库和 Release 均为私有资源，先用 `gh auth login` 登录有权限的 GitHub 账号，再执行：

```bash
case "$(uname -m)" in
  x86_64|amd64) RELEASE_ARCH=x86_64 ;;
  aarch64|arm64) RELEASE_ARCH=arm64 ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac

mkdir -p /tmp/sim-master-release
gh release download v1.1.7 \
  --repo techblack/sim-master \
  --pattern "sim-master-${RELEASE_ARCH}.tar.gz" \
  --dir /tmp/sim-master-release
sudo mkdir -p /opt/simadmin
sudo tar -xzf "/tmp/sim-master-release/sim-master-${RELEASE_ARCH}.tar.gz" -C /opt/simadmin
sudo install -m 0644 scripts/simadmin.service /etc/systemd/system/simadmin.service
sudo systemctl daemon-reload
sudo systemctl enable --now simadmin.service
curl http://127.0.0.1:3000/api/health
```

运行时保留 `simadmin` 兼容路径和服务名。公开镜像或自建 Release 镜像环境也可以运行 `sudo ./install_latest.sh`，脚本会按主机架构选择压缩包；可通过 `REPO`、`ASSET_URL`、`INSTALL_DIR`、`SERVICE_NAME` 覆盖默认值。详细硬件依赖和卸载流程见 [`docs/install.md`](docs/install.md) 与 [`docs/environment.md`](docs/environment.md)。

## 从源码构建

需要 Rust stable、Go 1.26、Node.js 24 和 pnpm 9。开发机可分别构建：

```bash
cd frontend && corepack pnpm install --frozen-lockfile && pnpm run build:full
cd ../backend && cargo build --release --locked
cd ../vowifi-helper && go build -trimpath -ldflags='-s -w' -o simadmin-vowifi-helper ./
```

跨架构 Release、Docker 构建和 tag 发布由 `.github/workflows/release.yml` 自动完成；推送 `v*` tag 或在 Actions 中手动运行即可。

## 安全与能力边界

该项目会操作 modem、SIM、网络配置、systemd 和系统文件。请只在自己管理的设备上运行，并为 `/opt/simadmin` 或 Compose volume 做定期备份。IMS/VoWiFi 是否注册、通话或短信可用取决于实际 modem、SIM、运营商策略和网络路径，不能仅凭配置开关推断。
