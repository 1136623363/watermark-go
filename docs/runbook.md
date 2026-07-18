# watermark-go 单节点部署运行手册

本项目只运行 Compose project `watermark-go`。目标宿主机不编译镜像、不加载本地镜像、不使用 tag 作为运行身份；
部署、回滚和验收均以 `repository@sha256:<digest>`、source commit、CI run 和 machine evidence 交叉核对。

## 基本顺序

1. 准备 `/var/lib/watermark-go/runtime.env`，权限必须是 `0600`。
2. 运行 `scripts/preflight.sh`，确认 Compose 不含 `build:`，API 只绑定 `127.0.0.1:5001` 或 shadow
   `127.0.0.1:15001`。
3. 运行 `scripts/deploy-local.sh`。脚本只执行 digest pull、`data-gate-${role}` one-shot
   `--force-recreate --no-deps`，再启动同 role 的 helper/proxy/API。
4. 运行 `scripts/observe.sh` 采集 30 分钟 60 个 raw samples；`scripts/verify-acceptance.py` 会从原始样本
   重算窗口、P95、停止线和 attempt identity。

## A/B 与回滚

当前首次迁移路径为 `rollbackMode=absent_two_stage`：A 先成为 verified recovery，再发布等价 B。B 切换失败时，
`scripts/rollback-local.sh` 只能回到状态文件中已验证的 A digest 和 identity；没有已验证旧服务时，不伪造
previous image，也不把隔离演练当成真实回滚。

## Evidence

所有可提交 machine evidence 使用 `scripts/write-evidence.py` 写入：同目录 `0600` 临时文件、flush、file fsync、
atomic rename、directory fsync。`.txt` 后缀也必须是 versioned JSON/event ledger，不写自由文本，不复用旧
`passed=true` artifact。失败时优先写当前 attempt 的 `passed=false` tombstone；如果文件系统故障导致 tombstone
也失败，验收器会依靠 deployment/cutover attempt mismatch 拒绝旧 PASS。

## media-parser 研究融合边界

`ucmao/media-parser` 只作为研究输入；验收 evidence 只接受 hermetic machine evidence：
`sourceCommit`、`imageDigest`、`ciRunId`、test manifest/hash，以及 registry、structured JSON、query policy、
candidate ranking/budget、cache semantics、rich-media、unsafe-pattern scan 子门全部为 true。不得 clone、同步或
把研究项目作为 runtime 依赖。

## 禁止事项

- 不在目标宿主机运行镜像构建或本地镜像导入。
- 不操作 Compose project `watermark-go` 之外的容器、网络、卷、进程或路由。
- 不编辑 Cloudflare tunnel token、系统级 Nginx/Caddy/systemd 配置或无关服务。
- 不用 aggregate 或顶层 `passed=true` 代替 raw records/raw samples 重算。
