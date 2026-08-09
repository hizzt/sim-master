# sim-master agent guide

## 项目边界

sim-master 是一个 Rust/Axum 后端与 React/Vite 前端组成的 SIM/eSIM 蜂窝设备管理平台。源码根目录是本目录；`backend/`、`frontend/` 和 `vowifi-helper/` 分别是后端、Web 控制台和 VoWiFi SWu helper。

运行时默认仍使用 `/opt/simadmin`、`simadmin` 二进制和 `simadmin.service` 这些兼容名称。它们是已部署设备的稳定接口，仓库/产品名称改为 sim-master 不应破坏已有安装。

## 修改约定

- 先阅读相关模块和 `docs/`，不要把 ModemManager 的配置开关当成 modem 已实际支持的能力。
- 涉及 modem、SIM、APN、频段、iptables、OTA 或系统重启的改动必须保持失败可见，并优先提供只读诊断。
- 配置和业务数据应持久化到 SQLite；涉及配置的改动要验证写入、重启后的读取和 API 返回。
- 前端 API 统一走 `frontend/src/api/`，不要在页面中新增重复的 fetch 封装。
- 不要提交 `target/`、`node_modules/`、`dist/` 或 VoWiFi 调试二进制；`.gitignore` 已覆盖这些目录。

## 常用检查

```bash
cd backend && cargo test --locked
cd ../vowifi-helper && go test ./...
cd ../frontend && corepack pnpm install --frozen-lockfile && pnpm run build:full
```

完整的跨架构产物由 `.github/workflows/release.yml` 使用 Docker Buildx 生成。容器需要宿主机 system D-Bus、`/dev` 和 ModemManager；没有真实 modem 时只能验证 Web 健康检查和静态页面。

## 交付前检查

1. `git diff --check` 和相关语言测试通过。
2. 更新 `VERSION` 时同步 Rust 与前端版本（`scripts/build.sh` 可执行同步）。
3. 如果改变部署行为，更新 `Readme.md` 和对应 `docs/`，并说明需要的权限、设备挂载和持久化目录。
