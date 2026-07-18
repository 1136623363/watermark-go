# watermark-go

`watermark-go` 是面向当前小程序协议的单机 Go 后端重构。项目以固定来源提交的解析行为为兼容基线，逐任务拆分配置、HTTP、解析编排、持久任务、缓存、下载、媒体处理和后台能力。

当前仓库仍处于实现计划执行阶段：已推进到单机 HTTP 组合与观测保护阶段。后续镜像发布、迁移演练、真实域名验收和运维证据仍必须按计划逐项完成，未跑通对应测试和证据前不得标记为交付。

## 长期边界

- 一个 Go API/后台进程负责 HTTP、业务编排、数据访问和任务调度。
- MySQL 保存业务事实；Redis 仅承担缓存、锁和限流，并允许核心同步解析降级。
- Go 原生解析器优先，受控的 `yt-dlp`、`ffmpeg` 和 Python universal bridge 仅作为进程内适配工具。
- GitHub Actions 测试并构建 GHCR 镜像；目标机只拉取不可变镜像并运行单机 Compose。
- 最终接口必须由当前前端通过 `https://watermark.bxsn.cn` 无改动使用。

完整且优先级最高的约束见 [约束文件.md](约束文件.md)，设计与逐任务步骤见：

- `docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md`
- `docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md`
- `docs/requirements-traceability.md`
- `docs/source-provenance.json`

## 目标结构

```text
cmd/watermark-go/             进程入口与信号退出
internal/app/                 生命周期与依赖组装
internal/config/              typed 环境配置和生产校验
internal/httpapi/             路由、中间件和协议转换
internal/auth/                客户端与后台认证
internal/parse/               同步/异步解析编排与规范化
internal/parser/              native、universal、yt-dlp 适配器
internal/netguard/            URL、DNS、重定向与拨号 SSRF 防护
internal/store/               MySQL、迁移、仓储与旧数据导入
internal/cache/               Redis 和内存降级层
internal/task/                MySQL 持久任务租约和恢复
internal/download/            受限下载兜底和签名票据
internal/media/               m3u8 与媒体校验
internal/admin/               后台单机能力和基准执行
internal/observability/       日志、追踪和客户端遥测
migrations/                   顺序、幂等 SQL 迁移
deploy/                       只拉取镜像的单机 Compose 与环境示例
scripts/                      基准、预检、部署、观察和回滚工具
tests/                        前端契约、E2E、基准和运维测试
artifacts/                    可提交的脱敏验收证据
```

## 开发与验证

模块语言版本保持为 `go 1.24.0`，首选构建工具链固定为 `go1.26.5`。当前本地验证只运行源码测试、静态检查和契约检查；镜像只能由 GitHub Actions 基于已审查提交生成，当前服务器不得执行 Docker/Buildx 镜像构建。

仓库策略门禁可独立运行：

```bash
go test ./internal/policy -count=1
```

全量验证、Compose 渲染、基准、服务 E2E 和部署步骤以详细计划为准。开发临时报告写入被忽略的 `reports/`，可提交证据只写入 `artifacts/`，且不得包含凭据或外部媒体。

## 验收硬门槛

解析基准必须使用固定 93 个启用样本、并发 3、native 优先、fallback 开启并绕过全部缓存；三次运行每次都必须满足 `success >= 62` 且 `durationMs <= 216000`。最终还必须具备 Actions 镜像、运行 digest、旧数据迁移、真实域名前端流程、资源观察和回滚证据。
