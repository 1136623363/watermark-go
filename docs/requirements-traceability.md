# watermark-go 需求追踪矩阵

## 使用规则

本文件把用户八项要求映射到实现、自动化测试和部署验收证据。路径均相对仓库根目录。pytest、诊断等本地临时输出写入被忽略的 `reports/`；需要提交的脱敏证据统一写入 `artifacts/`，并记录服务 commit、不可变镜像标签与 digest。状态为“计划任务 N 验证”的条目，只有指定任务及证据通过后才能关闭。

## 用户八项要求

| 编号 | 用户要求 | 当前状态 | 实现证据路径 | 测试证据路径 | 构建、部署或运行证据路径 |
| --- | --- | --- | --- | --- | --- |
| R1 | 建立独立 GitHub 仓库 `1136623363/watermark-go` 与本地项目 `/srv/watermark-go`；新仓库不携带凭据或退役部署入口。 | 任务 1 门禁与 staged tree 已就绪；唯一根 rewrite、GC、full scan 尚待本轮审查批准后完成，任务 16 再验证远端。 | `.gitignore`、`.dockerignore`、`约束文件.md`、`docs/source-provenance.json`、`docs/research/media-parser-provenance.json`、`deploy/env.example` | `internal/policy/repository_test.go`、`internal/policy/media_parser_research_test.go` | `.github/workflows/ci-image.yml`、`artifacts/release/repository-and-image.txt`、`artifacts/release/full-history-secret-scan.txt` |
| R2 | 交付 Go 模块化单体，由 Go 承担 HTTP、业务、数据和调度，主业务不依赖旧后端。 | 计划任务 15 验证；实现分布在任务 2、3、5、7、8、9、11。 | `cmd/watermark-go/main.go`、`internal/app/app.go`、`internal/httpapi/router.go`、`internal/parser/descriptor.go`、`internal/parse/service.go`、`internal/task/worker.go` | `internal/app/app_test.go`、`internal/httpapi/router_test.go`、`internal/parser/registry_test.go`、`internal/parse/service_test.go`、`internal/task/worker_test.go` | `Dockerfile`、`artifacts/verification/local-verification.md` |
| R3 | 保持当前小程序的客户端 session、同步解析、异步解析、缓存分享、下载兜底、m3u8 和 performance 遥测契约，并以加法字段支持音频/图集/Live Photo。 | 计划任务 12、18 验证；接口实现分布在任务 3、6—9、11。 | `internal/auth/client.go`、`internal/parser/parser.go`、`internal/parse/normalize.go`、`internal/httpapi/client_handlers.go`、`internal/download/service.go`、`internal/media/m3u8.go`、`internal/observability/client_performance.go`、`docs/frontend-provenance.json` | `tests/contracts/frontend_contract_test.go`、`internal/parse/url_test.go`、`tests/e2e/test_frontend_flow.py`、`scripts/verify-frontend-provenance.sh` | `artifacts/acceptance/frontend-domain-e2e.json` |
| R4 | 保留后台登录、运行诊断、结果库、平台样本和批量基准等单机管理能力。 | 任务 12 契约验证 + 任务 17 运行验证；后台与样本实现由任务 10、11 完成。 | `internal/admin/service.go`、`internal/admin/baseline.go`、`internal/httpapi/admin_handlers.go` | `internal/admin/service_test.go`、`internal/admin/baseline_test.go`、`internal/httpapi/admin_contract_test.go` | `artifacts/acceptance/admin-and-baseline.json` |
| R5 | MySQL 持久化业务事实并保留旧数据；Redis 只用于缓存、锁和限流，故障时核心同步解析可降级。 | 计划任务 15 本地验证、任务 17 数据迁移/运行验证、任务 18 final 验收；存储实现由任务 5、7、8 完成。 | `internal/store/mysql.go`、`internal/store/legacy_import.go`、`internal/store/legacy_delta.go`、`internal/cache/redis.go`、`internal/parse/service.go`、`migrations/` | `internal/store/migrate_test.go`、`internal/store/legacy_import_test.go`、`internal/store/legacy_delta_test.go`、`internal/cache/cache_test.go` | `artifacts/migration/legacy-data-rehearsal.json`（MariaDB 11.8.6 → MySQL 8.4、`chosenMigrationMode=final_full_no_binlog`、final full/import/checksum/no-writer）、`artifacts/acceptance/redis-degraded.json` |
| R6 | GitHub Actions 执行测试与安全检查，以 Docker Buildx 构建并发布 GHCR 镜像。 | 任务 16 验证 recovery candidate A；任务 17 在 A 完整验收后生成并验证 promotion B；工作流由任务 13 完成。 | `.github/workflows/ci-image.yml`、`Dockerfile`、`deploy/image-lock.json`、`release/promotion-marker.txt`、`scripts/verify-gitleaks.sh` | `internal/policy/docker_ci_test.go`、`scripts/verify-image.sh` | `artifacts/release/full-history-secret-scan.txt`、`artifacts/release/recovery-image-digest.txt`、`artifacts/release/final-image-digest.txt`、verified canonical `artifacts/release/image-digest.txt`、`artifacts/release/promotion-equivalence.json`、`artifacts/release/sbom-recovery.spdx.json`、`artifacts/release/sbom-final.spdx.json`、`artifacts/release/sbom.spdx.json` |
| R7 | 目标机只拉取不可变 GHCR manifest digest，并以单机 Docker Compose 部署；目标机不构建或导入应用镜像。 | 任务 17 建立 verified recovery A、等价 final candidate B 与 B→A drill；任务 18 切换并观察 B。 | `deploy/compose.yml`、`deploy/image-lock.json`、`scripts/deploy-local.sh`、`scripts/rollback-local.sh`、`/var/lib/watermark-go/runtime.env`（服务器仓库外 0600） | `tests/ops/test_scripts.py`、`scripts/verify-image.sh` | `artifacts/acceptance/final-shadow-e2e.json`、`artifacts/deploy/pull-and-up.txt`（A/B role event ledger，最终含 B final up、actual RepoDigest、`localBuild=false/localLoad=false`）、`artifacts/deploy/state-before.json`、`artifacts/deploy/running-digest.txt`、`artifacts/deploy/public-cutover.json`、`artifacts/release/repository-and-image.txt`（A/B role） |
| R8 | 不改动、不停止现有无关容器；新栈使用独立名称、网络、卷和空闲端口，并遵守 CPU/磁盘/inode、MemAvailable、swap 速率、PSI 与 OOM 停止线。 | 任务 17 保存 before 与 A/B shadow/recovery 状态；任务 18 写 final after/diff 并验证无关变化为零。 | `deploy/compose.yml`、`scripts/preflight.sh`、`scripts/observe.sh`、`scripts/host-snapshot.sh`、`scripts/smoke.sh` | `tests/ops/test_scripts.py` | `artifacts/deploy/host-before.json`、`artifacts/deploy/before-after-containers.json`、`artifacts/deploy/recovery-observation-30m.json`、`artifacts/deploy/observation-30m.json`、`artifacts/deploy/rollback-drill.txt` |

## 当前前端关键接口

| 能力 | 必须兼容的接口 | 实现与测试责任 | 最终部署证据 |
| --- | --- | --- | --- |
| Session | `POST /api/client/session`；解析与下载同时兼容 `token` 请求头和 Bearer 认证。 | 任务 6：上游错误脱敏、熵失败零写、不持久化 `session_key`；任务 12：fake exchanger 契约；任务 18：固定前端 provenance 的真实微信 code。 | `artifacts/acceptance/frontend-domain-e2e.json` 只记录 `wechatBound/identityType/requestId`，不记录 code/openid/token。 |
| 同步 parse | `POST /api/parse`，并保留 `/api/hybrid/video_data`、`/video/share/url/parse`、`/video/id/parse`、`/api/v1/parse` 及 `/api/v1/parse/:source/:video_id`。 | 任务 7：`internal/parse/service.go`、`internal/httpapi/parse_contract_test.go`；任务 12：前端契约。 | 同一证据的 `syncParse` 用例。 |
| 异步 parse | `POST /api/parse/task` 与 `GET /api/parse/task/:id`；规范状态是 `pending/running/completed/failed/expired`，读取时兼容旧 `queued`。 | 任务 8：`internal/task/worker.go`、`internal/httpapi/parse_task_contract_test.go`；任务 12：`tests/e2e/test_frontend_flow.py`。 | 同一证据的 `asyncSubmit` 与 `asyncPoll` 用例。 |
| Cache 分享恢复 | `GET /api/parse/cache/:id`，分享 ID 可恢复规范化结果。 | 任务 5、7：`internal/cache/redis.go`、`internal/store/repositories.go`、`internal/httpapi/parse_contract_test.go`。 | 同一证据的 `cacheRestore` 用例。 |
| Performance 遥测 | `POST /api/client/performance`，保持匿名快速接收与安全边界。 | 任务 11：`internal/observability/client_performance.go`、`internal/observability/client_performance_test.go`。 | 同一证据的 `performance` 用例。 |
| Download fallback | `POST /api/download/fallback`、`GET /api/download/fallback/:id`、`GET /api/download/status/:ticket`、`GET /api/download/proxy/:ticket`、`GET /api/download/cdn/:ticket`。 | 任务 9：`internal/download/service.go`、`internal/httpapi/download_contract_test.go`。 | 同一证据的 `fallbackCreate`、`fallbackPoll`、`fallbackDownload` 用例。 |
| m3u8 | `GET /api/m3u8/merge`、`GET /api/task/:id` 与 `GET /api/task/file/:id`。 | 任务 9：`internal/media/m3u8.go`、`internal/httpapi/download_contract_test.go`。 | 同一证据的 `m3u8Create`、`m3u8Poll`、`m3u8Download` 用例。 |

当前前端真实域名固定为 `https://watermark.bxsn.cn`。局域网地址和临时端口只产生影子证据；任务 18 必须通过真实域名重跑上表全部流程。

route-auth compatibility inventory 固定如下：fallback create 保持匿名兼容，但必须通过 attempt/limit/SSRF
边界；新 cache shareId 与 parse task ID 是 crypto-random >=128-bit、用途/TTL 绑定的 bearer capability，
legacy shareId 只限流只读且不再生成。fallback poll/download 使用服务端签名 URL；m3u8 create/task poll
对固定前端实际无 token 保持兼容，poll 只用随机 >=128-bit task ID 请求 `/api/task/:id`，最终 file URL
才签名。所有确实发送凭据的 route 同时接受 `token` header 与 `Authorization: Bearer`；不得强制前端
未发送的 query ticket/header。

## 外部解析研究融合

| 研究输入或优点 | 实现责任 | 自动化门禁 | 不可突破的边界 |
| --- | --- | --- | --- |
| `ucmao/media-parser` 固定 commit/tree 中的集中 parser factory 与 50-domain alias 目录 | 任务 3：`internal/parser/descriptor.go`、`internal/parser/registry.go` | registry ASCII key/alias/domain 唯一性、稳定 priority/顺序、精确 host 路由、constructor 零 I/O、一次 Parse 最多一次 fetch | `docs/research/media-parser-provenance.json` 固定来源/license；50 aliases 仅为候选，不自动成为 production 支持项 |
| 平台必需 query 参数的显式保留 | 任务 4/7：`internal/netguard/url.go`、`internal/parse/url.go` | 表格测试覆盖 FetchURL/SafeURL/CacheKey、allowlist、tracking 剥离、IDNA/端口/恶意 host 后缀、首次请求前 SSRF、redirect/DNS rebinding、跨域敏感 header 剥离与日志脱敏 | canonicalizer 不联网/不盲改 HTTPS；raw query/capability 不进入日志、cache key 或 evidence，TLS 不得关闭 |
| video/gallery/audio/live-photo 与多媒体候选 | 任务 3 内部 `ImageAsset`/`MediaCandidate`，任务 7 兼容投影，任务 12 契约 | 单次抓取、能力/结果一致、稳定质量排序、有界 fallback、旧 `images` 字符串数组不变、可选 `imageAssets` 配对 | 不盲信数组下标；每个媒体 URL 都经 netguard/media 门禁，不改变当前前端既有字段 |
| 结构化 API、嵌入 JSON 页面漂移和短期会话失效线索 | 任务 3 脱敏合成 golden；任务 5/7 versioned singleflight cache/typed error；任务 8/9 bulkhead/异步媒体任务 | 截断/字段换层/登录/风控/空核心 fixture，缓存版本自动 miss、取消/凭据缺失不负缓存，DASH/ffmpeg 取消无泄漏 | import/constructor 无副作用，无源码会话 fallback；实时页面不能替代离线 fixture |
| 上游平台覆盖线索 | 任务 10 可创建隔离 `tests/research/media-parser/` 候选 fixture | 候选必须独立合法取得并通过 descriptor、URL、资源、安全和 API 契约测试 | 不得作为 93 样本基线权威，不得改变 canonical fixture hash/门槛；本轮上游代码复制为无 |
| 上游风险反例 | 任务 4、7、9、15 | policy/SSRF/TLS/secret/download/logging 负向测试 | 拒绝首次请求后才校验 domain、关闭 TLS、硬编码会话/反爬材料、原始 URL 日志、0777/无界下载和吞异常 |

## 解析基准追踪

| 基准约束 | 实现证据路径 | 自动化测试路径 | 运行证据与关闭任务 |
| --- | --- | --- | --- |
| 使用同一组 93 个启用样本。 | `docs/baseline-provenance.json` 独立 trust anchor、`tests/baseline/fixtures/platform-samples.json`、`internal/admin/baseline.go`、`scripts/baseline/run.py` | Task 10 生成 fixture 后，以完整 bytes 计算并独立审查 `canonicalFixturePath`/`canonicalFixtureSha256`；hash 不写入 fixture 自身。Go/Python 固定字面量复算，fixture/report 自报只交叉核对。 | 任务 17：三份独立 run artifact。 |
| 固定并发 3。 | `internal/admin/baseline.go`、`scripts/baseline/run.py` | 从每条 record 的 started/ended 半开区间重算 `maxObservedConcurrency`；负测拒绝自报 3 但实际四条重叠或全程串行。 | 三份报告均不超过 3，并在 93 个可运行样本下实际达到 3；不能只信 `concurrency=3`。 |
| native 首选并开启 fallback。 | `internal/parser/registry.go`、`internal/parse/service.go` | `internal/parse/fallback_test.go`、`tests/baseline/test_report.py` | 三份报告的 `nativeEnabled` 与 `fallbackEnabled` 均为真。 |
| 绕过成功缓存、失败缓存和历史结果复用。 | `internal/admin/baseline.go`、`internal/parse/options.go`、`scripts/baseline/run.py` | `internal/admin/baseline_test.go` | 三份报告的 `cacheBypass` 均为真。 |
| 每轮证据完整且 `success >= 62`、`durationMs <= 216000`。 | `internal/admin/baseline.go`、`scripts/baseline/run.py` | `verify-acceptance.py` 的负向夹具逐轮覆盖 unique runId、固定 canonical hash、concurrency/native/fallback/cacheBypass、completed、success、duration；records 必须恰好覆盖 93 个唯一 sampleKey，每个成功记录含 media success 与本轮唯一 parserInvocationId，并从 records 重算 completed/success/wall-clock，不信任 `passed` 或 aggregate。 | 三轮时间窗不重叠、runId/record-set hash 独立且 records 不复用；任意逐项证据不满足均不得关闭任务 18。 |

## 发布、影子验收与切流证据

- 实机 discovery 的当前分支输入是 `oldServicePresent=false`、5001/15001 空闲、`baselineHTTPS=502`、
  源 MariaDB 11.8.6（`@@log_bin=0`、`binlog_format=MIXED`、`gtid_strict_mode=0`）和可用 Redis；因此
  `rollbackMode=absent_two_stage`、`chosenMigrationMode=final_full_no_binlog`。`host-before.json` 保存
  oldServiceIdentity/routeIdentity/dbWriterIdentity；默认只操作 `watermark-go`，任何额外旧对象都必须命中
  `host-before 精确 identity/hash allowlist`。
- route 权威只来自运行 tunnel/dashboard 的脱敏 identity 或安全 API 查询，并由真实 HTTPS 探测交叉
  验证；`/etc/cloudflared/config.yml` 不是权威，不编辑/重建 token tunnel。
- 任务 13 验证镜像与 Compose 边界；全部 runtime/base/recovery image 固定 registry digest，ffmpeg 固定
  精确包版本与 reproducible snapshot（或固定制品 SHA-256），每个 Python 依赖用 `--require-hashes`。
  workflow `uses:` 固定 40 位 SHA、最小 permissions。统一 `scripts/verify-gitleaks.sh` 以固定 v8.30.1 和
  `--log-opts=--all` 生成权威全历史证据，pinned Action 仅作增量门禁。普通 push 运行 unit tests 与
  `--schema-of-present`；仅 final evidence push 运行 `--require-complete`，docs/artifacts-only 不构建镜像。
  Go 构建固定 `-trimpath -buildvcs=false`/空 buildid 且不注入 commit/time，Python 禁 `.pyc` 并归一化元
  数据；两模拟 revision 的 canonical rootfs/app hash 只能在 OCI config label allowlist 上不同。
- 任务 14 验证 `scripts/deploy-local.sh`、`scripts/rollback-local.sh`、`scripts/preflight.sh`、`scripts/observe.sh`、`scripts/verify-image.sh`、`scripts/host-snapshot.sh`、`scripts/smoke.sh`、`scripts/verify-frontend-provenance.sh`、`scripts/verify-acceptance.py` 与 `tests/ops/test_scripts.py`。
- 任务 15 的 must-fix 先用失败测试锁定并形成精确 code/test commit，复验后再以独立 evidence commit
  输出原子、脱敏、带 schemaVersion/passed 的 `artifacts/verification/local-verification.md` 与
  `artifacts/verification/secret-scan.txt`。
- 任务 16 在首次 push 前扫描全部可推送 refs且只添加新仓库 `origin`，发布 recovery candidate A；
  `repository-and-image.txt` 的 A role、`sbom-recovery.spdx.json` 和 attestation subject 绑定其完整 commit/
  RepoDigest。root rewrite、GC、full scan 未完成前，R1 与任务 16 仍 pending。
- 任务 17 先以独立 DB/schema、独立 Redis namespace 完成 A shadow 隔离全验；强制
  `shadow DB identity != final DB identity`。干净 final DB 只接收 production full import 与 scrub 后的
  生产数据；final checksum 证明无本轮 shadow/A-B acceptance 的 runId/taskId/sentinel/outbox，legacy
  snapshot 中原有合法历史记录按 source checksum 保留。A 经过真实域名、真微信固定矩阵和
  `A observation>=1800s` 后才成为 recovery digest；A 首上失败只能恢复原 502 路由并标记 FAILED，
  不得声称 5 分钟健康回滚。
- A 验证后才允许 A→B source diff allowlist：只改 `release/promotion-marker.txt`/OCI revision，不允许
  Go/依赖/Dockerfile/执行/config/migration/schema 变化。A/B 的 rootfs/app binary/tool versions/schema
  只允许 OCI label 白名单差异，`recoveryDigest != finalDigest`；A 是 recovery digest，B 是 final digest。
  B 完成独立 `final-shadow-e2e.json` 后才原子更新 canonical verified `image-digest.txt`，并在同一兼容 final
  DB 上执行 B→A 真实 drill（`durationSeconds<=300` 且 health/data/route passed）；
  `state-before` 锁定 A/B digest+attestation+config/DB identity。
- 当前 absent 分支的 `branches.initialDeployment.applicable=false`、
  `result=not_applicable_no_verified_legacy_service`；隔离 old-service/DB clone 的
  `isolatedCompatibilityRehearsal` 不计入 rollback/pass。legacy reverse 只有未来
  `oldServicePresent=true` 且 service/route/writer identity 全部验证才适用。
- 任务 18 切换 B 前验证已登录 DevTools/真机的一次性 `wx.login` readiness；未就绪不得进入写栅栏或
  切流。统一 post-cutover failure trap 在步骤 3、4、5、6 任一失败时立即 fence/drain 新写并自动走
  已演练 B→A；若回退不能证明成功，则禁止恢复旧路由、A/B 受控只读/隔离且标记 FAILED。
- `pull-and-up.txt` 是 A/B role JSON event ledger，Task 17 写到 B shadow，Task 18 runtime inspect 验证
  actual RepoDigest/commit/attestation/config/final DB 后才写 B final up 与 `running-digest.txt`；所有事件
  明确 `localBuild=false/localLoad=false`，失败或 B→A 不得留下假 B running/up。
- Task 18 的 state/public-cutover/B E2E/B observation/final acceptance/running digest/B final up 绑定同一
  `deploymentRunId`/`cutoverAttemptId` 与当前时间窗；旧 passed artifact 因 ID 不匹配拒绝。脚本以
  `set -Eeuo pipefail` 覆盖 ERR/EXIT/HUP/INT/TERM、预写 `in_progress` 并在重启时先 reconcile。
- 所有 evidence（包括 `.txt` status/scan）是含 schemaVersion/passed/run/role 的 machine-readable 文件，
  经同目录 0600 temp、flush/file fsync、rename/directory fsync 原子写入；失败写本次 tombstone或以 run/attempt
  mismatch 拒绝旧 PASS。tracked final report 只记 `evidenceParentCommit` 与排除自身的 payload hash，
  `verifiedEvidenceCommit` 由 CI `GITHUB_SHA`/本地 HEAD 外部验证 docs/artifacts-only diff，避免自引用。
- A/B 两份公网观察都以真实 `https://watermark.bxsn.cn/api/health` 生成 60 个 raw sample，首个约 +30s、
  第 60 个不早于 +1800s、间隔 25–35s，并从原始样本重算 P95/资源停止线。Task 18 最后写
  `before-after-containers.json` final after/diff，要求无关容器变化为零；`--require-complete` 通过后才
  更新本矩阵状态。
