# Watermark Go 单机重构设计

## 1. 决策

新项目位于 `/srv/watermark-go`，以旧仓库提交
`1d3dc9a6064f3f2e41af9ea92a29566885939175` 的解析器和 API 行为为兼容基线，
选择性回迁旧仓库 `main` 中与单机有关的安全、异步任务、客户端遥测和可靠性修复。
不回迁集群注册、跨节点调度、MySQL/Redis 高可用编排、Jenkins 和多服务器部署代码。

`ucmao/media-parser` 仅作为第二研究输入，固定在 commit
`033424b08ac6468c8c37b6fb0c98a0446bb09d9e`、tree
`56e556db619a296340fa8b00f3c726676cf32bcf`。本轮不复制其代码，不把它提升为 API、样本或网络安全
基线；只吸收集中 parser metadata、domain alias 目录、平台 query 策略、音频/图集/Live Photo 和媒体
候选建模思路。来源、MIT license hash、采用项和拒绝项固定在 `docs/research/` 并由 policy 测试校验。

项目采用 Go 模块化单体：一个 API/后台容器、一个 MySQL 容器、一个 Redis 容器。
解析引擎仍以 Go 原生解析器为首选，并保留 yt-dlp 与 universal bridge 作为提高样本覆盖率的兜底工具。
Docker 镜像只能由 GitHub Actions 构建并推送到 GHCR；当前服务器只执行登录、拉取、迁移和启动，
不得执行 `docker build`、`docker compose build`、`docker save/load` 或复制镜像层。

## 2. 方案比较

### 方案 A：全部从零重写

边界最纯粹，但需要重新验证几十个平台的反爬、页面结构和媒体字段，
最容易低于 93 个样本的准确率基线，也会重复已经存在的 Go 原生解析器工作。

### 方案 B：新 Go 网关代理旧服务

交付速度快，但核心解析、缓存、任务和管理能力仍依赖旧部署，
没有消除全局状态、大文件和集群/Jenkins 耦合，不满足“重构新项目”的目标。

### 方案 C：兼容迁移后模块化重构

复用经过样本验证的解析能力，通过契约测试锁定外部行为，逐步把配置、路由、解析编排、
缓存、任务和后台从旧 `internal/server` 大包拆出。该方案兼顾准确率、交付风险和长期可维护性，
是本次采用方案。

## 3. 范围

### 必须交付

- 独立 GitHub 仓库 `1136623363/watermark-go` 和本地目录 `/srv/watermark-go`。
- Go module language `1.24.0`、首选工具链 `go1.26.5` 的模块化单体服务，主业务不依赖另一套 watermark 后端。module language 保持现有宿主兼容；任务 2 通过 Go toolchain 自动获取或校验过的官方安装包启用首选工具链。
- 保持当前小程序的客户端会话、同步解析、异步解析、缓存分享、下载兜底、m3u8 和遥测契约。
- 保留后台登录、运行诊断、结果库、平台样本和批量基准测试。
- MySQL 持久化业务事实，Redis 仅用于缓存、锁和限流；Redis 故障时核心同步解析可降级。
- GitHub Actions 执行测试、安全检查、Docker Buildx 构建并发布 GHCR 镜像。
- 当前服务器 `192.168.31.222` 从 GHCR 拉取镜像并用 Docker Compose 单机部署。
- 不改动、不停止现有无关容器；新栈使用独立容器名、网络、卷目录和空闲端口。

### 明确不做

- 不做多节点、集群注册、分布式调度、Redis Sentinel、MySQL 复制或自动故障转移。
- 不接 Jenkins，不触发任何 Jenkins job。
- 不引入 Kubernetes、消息中间件或对象存储作为本次上线前置条件。
- 不在当前服务器构建或导入应用镜像。
- 不在影子验收完成前切换公网 DNS、Cloudflare 或正式小程序默认域名。
  最终验收必须让 `https://watermark.bxsn.cn` 到达新服务；切换前保存现网健康和路由快照，
  切换失败时按快照恢复，避免影响正在运行的公网服务。

## 4. 代码结构

目标结构如下：

```text
cmd/watermark-go/             进程入口，只负责配置加载、依赖组装和信号退出
internal/app/                 应用生命周期与依赖容器
internal/config/              环境变量解析、默认值和生产校验
internal/httpapi/             Gin 路由、中间件、统一响应和外部 API handler
internal/admin/               后台认证、管理 API 和嵌入式模板
internal/auth/                客户端会话、token 和用户标识
internal/parse/               解析用例、异步任务、结果规范化和错误分类
internal/parser/              Parser 接口、注册表、fallback 策略
internal/parser/native/       Go 原生平台解析器
internal/parser/universal/    universal bridge 适配器
internal/parser/ytdlp/        yt-dlp 进程适配器
internal/cache/               Redis/内存缓存与锁
internal/store/               MySQL、迁移和各仓储实现
internal/task/                单机持久任务领取、租约、重试和清理
internal/download/            下载兜底、签名下载票据和临时文件
internal/media/               m3u8 合并与媒体地址安全校验
internal/observability/       request ID、结构化日志、指标和客户端遥测
migrations/                   顺序 SQL 迁移
deploy/                       仅包含单机 Compose 与无密钥环境变量示例
scripts/                      基准、冒烟、部署与回滚脚本
tests/                        API 契约、E2E、基准和前端兼容夹具
artifacts/                    可提交的脱敏验收证据（本地临时报告仍写入被忽略的 reports/）
docs/research/                固定 commit/tree/license 的外部研究与明确采用/拒绝边界
```

仓库部署面只允许 `deploy/compose.yml` 与 `deploy/env.example`。Cloudflare Tunnel 与反向代理均由
宿主机管理，仓库不保存 Nginx/Caddy 配置，也不提供会修改宿主路由的代理安装脚本。

跨任务的权威路径固定如下：样本 manifest 为 `tests/baseline/fixtures/platform-samples.json`；Go 后台基准引擎与测试为 `internal/admin/baseline.go`、`internal/admin/baseline_test.go`；Python 门禁为 `scripts/baseline/run.py`、`tests/baseline/test_report.py`；前端总契约与运行 E2E 为 `tests/contracts/frontend_contract_test.go`、`tests/e2e/test_frontend_flow.py`。部署和保护工具固定使用 `scripts/deploy-local.sh`、`scripts/rollback-local.sh`、`scripts/preflight.sh`、`scripts/observe.sh`、`scripts/verify-image.sh`、`scripts/host-snapshot.sh`、`scripts/smoke.sh`，由 `tests/ops/test_scripts.py` 验证。

包之间通过小接口通信。`httpapi` 不直接操作 SQL/Redis/外部进程；handler 只完成协议转换。
`parse.Service` 负责编排解析器、缓存、锁和结果存储；具体 parser 不依赖 Gin。
任务 worker 与 HTTP server 共进程运行，但任务状态写入 MySQL，容器重启后可以恢复。

## 5. 外部 API 兼容

### 健康与客户端

- `GET /api/health`
- `GET /api/v1/health`
- `GET /api/v1/platforms`
- `POST /api/client/session`
- `POST /api/client/performance`

客户端 session 接口接受 `code`、`programType`、`clientId`，返回 `code=0` 和包含
`token`、`userId`、`uid`、`publicId`、`identityType`、`wechatBound`、`expiresAt` 的 `data`。
解析和下载兜底接受 `token` 请求头，也兼容 `Authorization: Bearer`。

### 解析

- `POST /api/parse`：同步解析，35 秒客户端超时兼容。
- `POST /api/parse/task`：创建异步解析任务，8 秒内返回任务。
- `GET /api/parse/task/:id`：轮询异步任务。
- `GET /api/parse/cache/:id`：分享 ID/缓存 ID 恢复结果。
- `GET /api/hybrid/video_data`
- `GET /video/share/url/parse`
- `GET /video/id/parse`
- `GET /api/v1/parse`
- `GET /api/v1/parse/:source/:video_id`

成功结果至少保留：

```text
title, platform, type, cover, author, avatar, duration,
downloads, images, music, m3u8, previewUrl, playAddr,
shareId, sourceUrl, requestId
```

异步任务结果至少保留：

```text
taskId, status, progress, message, pollUrl, requestId,
result, createdAt, updatedAt
```

任务状态使用 `pending`、`running`、`completed`、`failed`、`expired`。
同一 `X-Request-ID` 或幂等键重复提交不得重复创建可执行任务。

### 下载和 m3u8

- `POST /api/download/fallback`
- `GET /api/download/fallback/:id`
- `GET /api/download/status/:ticket`
- `GET /api/download/proxy/:ticket`
- `GET /api/download/cdn/:ticket`
- `GET /api/m3u8/merge`
- `GET /api/task/:id`
- `GET /api/task/file/:id`

下载任务返回 `taskId`、`status`、`progress`、`downloadUrl`、`pollUrl`、`fileSize`、
`expiresAt`。媒体下载只能访问经过 SSRF 校验的公网 HTTP(S) 目标；拒绝回环、私网、
链路本地、云元数据地址和 DNS 重绑定结果。临时媒体有大小、并发、超时和 TTL 限制。

### 后台

保留 `/admin` 登录页面和现有管理 API 的单机功能：摘要、解析、结果库、请求、日志、
诊断、设置、工具状态、平台样本和平台运行。集群节点页面和内部 worker 接口不进入新项目。
管理写操作要求有效 session、角色检查和审计日志。
MySQL 配置后认证 fail closed；environment 仅用于无 MySQL 的开发/测试，显式 breakglass 需要强密码。
session cookie 把 `mysql/environment/breakglass` auth mode 纳入签名并禁止模式升级，旧格式失效；浏览器
管理写操作还要求 CSRF/Origin 校验，cookie 使用 Secure/HttpOnly/SameSite。

## 6. 解析数据流

```text
请求 -> request ID/鉴权/限流 -> URL 提取与目标安全校验
     -> 成功缓存 -> URL 级锁 -> 平台识别
     -> native -> yt-dlp/universal fallback
     -> 结果规范化与媒体地址验证
     -> MySQL 持久化 -> Redis 热缓存 -> 响应
```

解析器选择按平台能力配置，不以全局单一超时覆盖所有工具：

- native 首选，单平台默认 8 秒；
- yt-dlp 适合已验证的海外/通用站点，默认 30 秒；
- universal 视频默认 60 秒，音乐子任务默认 15 秒；
- 整个同步请求受 34 秒服务端预算约束，超出预算转异步任务或返回可重试错误；
- 批量基准通过受控 worker pool 并发执行，固定并发 3 以匹配历史口径并保护 4 核服务器。

parser registry 使用 metadata-driven `Descriptor`，每个平台集中声明稳定 ASCII key、显示名、aliases、精确
domains、video/gallery/audio/live-photo/m3u8 capabilities、确定性 priority、允许保留的 query keys 和构造函数。启动时
拒绝重复或歧义 key/alias/domain，路由使用规范 host 精确匹配且输出顺序确定。研究项目的 50 个 domain
alias 只是候选覆盖目录；未经固定旧来源、canonical 93 样本或新增独立 fixture 验证的条目不进入
production registry，也不能改变基准 trust anchor。

parser constructor 必须纯净且零 I/O，只接收注入的 netguard client、clock、短期 token provider、logger
与 typed config；不得在 package init/import/构造时联网、读环境、创建目录或启动进程。优先结构化 API；
页面内嵌 JSON 提取必须转义/嵌套感知，并用脱敏合成 golden 覆盖截断、字段换层、登录/风控页、空核心
字段与多载体分支。一个 parser 的 `Parse(ctx)` 返回完整 Result 或 typed error，不拼装多个吞错 getter。

URL extractor/canonicalizer 是不联网的纯函数，只接受 HTTP(S)，按 descriptor 保留平台必需 query，剥离
tracking/未知参数。query 中 capability/会话性质的值不进入日志、错误、cache key 或 evidence；需要
锁/cache 身份时用用途域分离摘要。首次网络请求前、每次 DNS/实际 dial 和每跳 redirect 都重新经过
netguard，禁止“先请求再判断 host”。类型明确区分不可日志化 `FetchURL`、允许持久化/返回的 `SafeURL`
与绑定 platform/parser/result schema version 的不可逆 `CacheKey`；不盲目把 HTTP 字符串改为 HTTPS。
跨 origin/host redirect 必须剥离 Cookie/Authorization/平台会话 header，response header、wire body 与
解压后 body 都有硬上限，TLS 校验不可关闭。

内部解析结果以 `ImageAsset{URL, LivePhotoURL}` 表达静态/动态配对，以 `MediaCandidate` 表达 kind、
quality、bitrate、尺寸和稳定 source rank。一个 Parse 最多抓取一次上游快照；候选按明确元数据与稳定
tie-breaker 排序并做有界 fallback，不能把固定数组下标当作可信质量信号。兼容层继续输出当前前端的
`images` 字符串数组和既有 aliases，只以可选 `imageAssets` 加法字段暴露 Live Photo 配对。

结果 cache key 由 platform、canonical resource、parser version 和 result schema version 组成，同一 key
使用 singleflight；context 取消、internal、credential_required、schema_changed 和安全拒绝不进入普通
负缓存。错误至少分为 invalid input、unsupported、credential required、upstream timeout/blocked、empty
media、schema changed 与 internal，并带 stage/platform/retryable，再映射到当前前端固定 envelope。
MySQL lease worker 同时实施全局/按平台 bulkhead；DASH/ffmpeg 等媒体合并只能在有界异步任务中运行，
context/lease 取消会终止进程组并清理部分文件。

失败分为不支持、无效输入、目标拒绝、上游超时、上游拒绝、空媒体和内部错误。
只对可恢复失败执行有界 fallback；不对相同失败无限重试。

所有 Go HTTP 流量使用统一 netguard transport。yt-dlp/universal 等 subprocess 不能继承外部代理或
用户配置：yt-dlp 使用 `--ignore-config`、隔离 HOME/XDG 和不可覆盖的 loopback guard proxy；Python
清空大小写 HTTP(S)/ALL/NO_PROXY 后只注入 guard。guard 对每跳重定向/DNS/CONNECT 重验并限制响应；
无法强制走 guard 的工具在 production 禁用。m3u8 的远程 manifest/子清单/分片全部由 Go 有界预取并
重写到受控临时根，ffmpeg 只读本地 file，不启用任何联网/crypto/concat/data 协议。

## 7. 数据与迁移

保留旧系统的核心表、已有数据和数据语义：解析结果、解析尝试、客户端 session、运行配置、平台样本、
平台运行、任务、后台账号和审计日志。迁移采用嵌入式顺序 SQL，启动时只做向前、幂等迁移。

单机任务表包含类型、payload、状态、进度、尝试次数、最大尝试、`locked_by`、
`locked_until`、下一次执行时间、错误和结果。启动时释放已过期租约，不删除未完成任务。

源数据引擎与目标引擎必须分开建模。当前实机源是 MariaDB 11.8.6，目标是 MySQL 8.4；discovery 已知
`@@log_bin=0`、`binlog_format=MIXED`、`gtid_strict_mode=0`，因此实际分支固定
`chosenMigrationMode=final_full_no_binlog`。不能把 MariaDB GTID 当作 MySQL GTID，不能声称存在
可追的 delta，也不能用 `updated_at` 伪造增量。备份只允许由 `deploy/image-lock.json` 固定官方 digest
的 MariaDB recovery image 在 Compose `migration-tools` profile 中完成；禁止裸 `docker run`、裸 tag、
本地 build 或把 MariaDB 数据目录直接挂给 MySQL。备份先恢复到隔离 MariaDB clone，再由 typed importer
显式映射类型、默认值、字符集和时间/JSON/数值语义到 MySQL 8.4。

当前 final gate 的唯一合法数据路径是：对 watermark 表执行 table-scoped fence，记录连接与 writer
identity，以重复 hash 证明无 writer，在该受控窗口重新生成 final consistent full snapshot，导入全新
final DB，并重算 schema version、逐表行数与稳定业务字段 checksum。禁止用全实例 read lock/read_only
影响其他数据库。仅未来 discovery 证明 ROW binlog/position 可靠时，另一个明确 mode 才可采用
initial+delta；artifact 中不适用的 position/delta/reverse 必须写 `notApplicable`，不能填假值。

shadow 与 final 数据面是两套身份：Compose profiles 强制
`shadow DB identity != final DB identity`，使用独立 DB/schema、独立 Redis namespace 和独立卷。
shadow E2E、后台、三轮基准、pending 与 shadow outbox 只能写 shadow；shadow outbox 不得进入
production outbox，也不采用“跑完再清理 shadow 写入”的方案。final production DB 只接收当前 mode 的
生产 full import 与 scrub 后的生产数据；任何 API/listener/worker 都只能在对应 migration/import/checksum
gate 通过后启动；final checksum 必须证明无本轮 shadow/A-B acceptance 的 runId/taskId/sentinel/outbox，
但 legacy snapshot 中原有合法历史 platform_runs/测试记录按 source checksum 保留。

只有 discovery 证明 `oldServicePresent=true`，并验证旧 service/route/writer/DSN identity 时，才启用
conditional legacy migration/reverse：回滚前栅栏/排空，把 outbox reverse replay 应用到唯一指定、
实际承接回滚生产流量的隔离旧库克隆，checksum 后原子切换旧服务 DSN 到同一克隆，验证连接身份后再
恢复旧路由。当前 `rollbackMode=absent_two_stage` 不存在此可信旧服务，legacy reverse 明确
notApplicable；隔离 compatibility rehearsal 不能冒充真实 rollback。

旧 `runtime_settings` 与审计 payload 按 allowlist 转换：只保留非秘密单机字段，丢弃 cluster/worker/
auto-update，scrub proxy userinfo、Cookie/token/password/secret 与嵌套 musicdl 敏感配置。必要秘密只
人工导入目标机仓库外 0600 runtime file。迁移证据固定为
`artifacts/migration/legacy-data-rehearsal.json`，只记录 engine/version/capabilities、mode、脱敏 identity、
snapshot hash、计数、checksum 和 gate；任一适用门禁失败时不得启动对应 listener/worker 或接管端口。

## 8. 配置与敏感信息

启动必需配置只来自环境变量或服务器本地仓库外的 `/var/lib/watermark-go/runtime.env`，该运行文件权限
固定为 `0600`，Compose/deploy 脚本只通过这一绝对路径加载，仓库内不得创建 runtime env 文件。
production 只接受已知 APP_ENV，要求 MySQL、真实非占位 WeChat AppID/强 secret、管理员/session/
下载密钥；Redis 可降级。下载旧 secret 名只在 typed config 加载边界迁移一次，业务只读 canonical
config。可选 Weibo/Xigua Cookie 也仅由 config 读取并传入 parser，错误/日志不回显任何值。
运营配置写入 MySQL `runtime_settings`，本地 JSON 只允许开发模式使用。

仓库必须忽略并通过 CI 扫描以下内容：

- `.env*`、`*.pem`、`*.key`、`*.p12`、凭据/密码文档；
- cache、logs、tmp、下载媒体、数据库和 Redis 数据；
- Docker 导出文件与本地构建产物。

旧源码中的硬编码 Cookie 不复制到新实现；需要 Cookie 的解析器只从可选环境变量读取。
当前资料目录中出现过的 GitHub token、服务器密码、私钥和平台 Cookie 不得提交到新仓库、
日志、测试夹具或最终报告。

外部研究来源同样 fail closed：`docs/research/media-parser-provenance.json` 固定 repository、默认分支、
commit、tree 和 MIT license SHA-256，并明确 `codeCopied=false`、`baselineAuthority=false`。未来任何实质
代码/数据移植必须先单独更新许可证归属和 policy 门禁；本轮明确拒绝研究项目中的首次请求后才校验
domain、关闭 TLS 校验、固定会话/反爬材料、原始 URL 日志、宽权限目录、无界下载、全局无界缓存和
广泛吞异常做法。

## 9. Docker 与 GitHub Actions

Dockerfile 使用多阶段构建：builder/runtime、Compose MySQL/Redis 与 MariaDB recovery image 都以官方
registry digest 固定并记录在 `deploy/image-lock.json`；builder 由首选 `go1.26.5` 工具链生成应用。
ffmpeg 使用固定日期 reproducible snapshot 的精确包版本，或使用记录固定制品 SHA-256 的已审查静态
制品；每个 Python 依赖都带 hash，并以 `--require-hashes` 安装。yt-dlp/videodl/musicdl 版本或 commit
同样固定。最终交付物只存在于 CI 生成的镜像中，目标机不得 build/load/import。
Go binary 使用 `-trimpath -buildvcs=false`、固定空 buildid，禁止 ldflags 注入 commit/time；身份只写 OCI
label/attestation。Python 禁止 `.pyc` 并归一化允许进入 rootfs 的可变元数据。policy 用两个模拟 revision
构建相同输入，要求 canonical rootfs inventory/app hash 相同，只有 OCI config label allowlist 可不同。

GitHub Actions 对 `main` push 执行：

1. Go 格式、vet、单元/契约测试、race test、Python/Node 测试和 Compose/policy 门禁；
2. 用固定 v8.30.1 CLI 对 `--log-opts=--all` 执行权威 full-history secret scan；
3. 对实际构建输入变更用 Buildx 构建 linux/amd64 镜像；
4. 推送完整 40 位 `sha-${GITHUB_SHA}` 和便利 tag，但运行只使用 manifest RepoDigest；
5. 输出 SBOM 与 provenance attestation，并把 subject digest、完整 40 位 source commit、
   `org.opencontainers.image.revision` 和 `org.opencontainers.image.source` 绑定。

所有 action `uses:` 固定 40 位 commit，最小 permissions、checkout 不持久化凭据，禁止
`pull_request_target`。pinned Gitleaks Action 仅作增量门禁，不能代替固定 CLI full-history 证据。
Actions second checkout 固定前端 provenance 并在 runner 跑 Node/服务契约。CI 自锁分两级：每次 push
先跑 verifier unit tests 和 `verify-acceptance.py --schema-of-present`；只有 final evidence push 才运行
`--require-complete`。docs/artifacts-only push 不运行 image job、不重建镜像、不移动 `latest`。

发布采用 A→B 两个不可变角色，而不是让全部证据冒充同一 commit/digest：A 是经 shadow、真实域名、
真微信和 `A observation>=1800s` 完整验收后才成立的 recovery digest；B 是其后由 promotion commit
构建的 final digest，且 `recoveryDigest != finalDigest`。A→B source diff allowlist 只允许精确路径
`release/promotion-marker.txt` 和 OCI revision；marker 必须被 `.dockerignore` 排除，任何 Dockerfile
都不得 COPY 进 rootfs。Go、依赖、Dockerfile、可执行输入、config、migration 或 schema 的变化均拒绝。
机器比较 A/B 的 rootfs、app binary、tool versions 与 schema，除明确的 OCI label 白名单差异外必须
一致；B 仍须完成独立 shadow 验收。

`scripts/verify-image.sh` 与 runtime inspect 校验实际 RepoDigest、attestation subject、OCI revision/
source 和完整 source commit；tag 仅作索引。发布证据按 A/B role 写入
`artifacts/release/repository-and-image.txt`、`recovery-image-digest.txt`、`final-image-digest.txt` 和
`promotion-equivalence.json`。A/B 分别生成 `sbom-recovery.spdx.json` 与 `sbom-final.spdx.json`，
canonical `sbom.spdx.json` 明确指向 final B。B promotion push 前重跑 policy 与 fixed Gitleaks
full-history，并让扫描证据覆盖 B commit。B attestation、equivalence 与独立
`artifacts/acceptance/final-shadow-e2e.json` 全部通过后，才原子更新 canonical
`artifacts/release/image-digest.txt` 为 verified B；Task 18 只能读取它。

## 10. 单机部署与资源保护

新栈只使用 Compose project `watermark-go`，shadow/final/recovery/migration-tools 由 profile 和独立
网络/卷隔离。API bind 严格限制为 shadow `127.0.0.1:15001` 或 final `127.0.0.1:5001`；MySQL/Redis
不发布宿主端口。正式运行配置固定在仓库外 `/var/lib/watermark-go/runtime.env`，权限 0600。

当前 discovery 是部署设计的事实输入，而不是“存在旧服务”的假设：`oldServicePresent=false`，没有
运行或停止的 watermark 容器、进程、systemd unit 或本地应用镜像，5001/15001 均空闲；
`watermark.bxsn.cn` 当前返回 502，即 `baselineHTTPS=502`。源数据库是 MariaDB 11.8.6，Redis PING
可用。`host-before.json` 必须重新记录 `oldServicePresent`、`oldServiceIdentity`、`routeIdentity`、
`dbWriterIdentity`、端口、引擎能力、资源和现网基线；实际事实变化即 fail closed 并重新选择状态机。

正式域名路由由宿主机外部的 token tunnel 管理。权威来源只能是运行 tunnel/dashboard 的脱敏 route
identity，或受控凭据下安全 API 查询，并以真实 HTTPS 探测交叉验证；
`/etc/cloudflared/config.yml 不是权威`，不得编辑/重建 token tunnel，也不得用未挂载的 host 文件 hash
伪造路由状态。默认操作面只允许新 project；cutover/rollback 额外对象必须同时属于
`host-before 精确 identity/hash allowlist` 中已验证的旧 watermark service/route，identity 不存在或变化
则拒绝，绝不枚举、停止或修改无关容器、进程、systemd、镜像、网络、卷或路由。

实际状态机选择 `rollbackMode=absent_two_stage`，并按下列顺序执行；任何数据 gate 都先于对应
API/listener/worker：

1. 保存宿主机与现网快照、运行 route 权威身份和 `before-after-containers.json` 的 before 集合，复验
   MariaDB 能力与 table-scoped no-writer 条件；
2. 用固定官方 digest 的 MariaDB recovery image 完成备份、隔离 MariaDB 恢复、typed importer 到
   disposable MySQL 8.4 clone，并选择 `chosenMigrationMode=final_full_no_binlog`；
3. 只从 GHCR 拉取 recovery candidate A，校验实际 RepoDigest、attestation、OCI source/revision；对
   独立 shadow DB/schema 与独立 Redis namespace 完成 migration/import/checksum gate 后，才以
   `127.0.0.1:15001` 启动 A shadow；
4. A shadow 隔离全验覆盖服务契约、后台/RBAC/CSRF、Redis 降级恢复、pending/outbox 恢复和三轮 93
   样本基准；可选 LAN 只在此后用受防火墙限制的临时 override，结束立即撤销；
5. 对源表执行 table-scoped fence 和重复 hash 证明无 writer，生成全新 production full，经 importer
   写入干净 final DB；migration/import/scrub/checksum gate 通过且无本轮 shadow/A-B acceptance 的
   runId/taskId/sentinel/outbox 后，
   才启动 A final；
6. 通过运行 route 权威渠道把 A 接入真实域名，使用已登录 DevTools/真机的一次性 wx.login code
   readiness；未就绪不得进入写栅栏或切流。readiness code 不保存/不复用，A 正式 session 前重新
   `wx.login` 获取全新 code。A 必须通过真微信、固定前端全矩阵并完成请求真实
   `https://watermark.bxsn.cn/api/health` 的 `A observation>=1800s`，才成为 recovery digest；
7. A 验证后才创建只改 `release/promotion-marker.txt`/OCI revision 的 B promotion commit。执行
   A→B source diff allowlist、rootfs/app binary/tool versions/schema 等价比较，确认
   `recoveryDigest != finalDigest`，再把 B shadow 隔离全验写入 `final-shadow-e2e.json`；
8. 在同一兼容 final DB 上执行当前适用分支真实演练：真实 route 暂接 B 后做 B→A 真实 drill，要求
   `durationSeconds<=300`、`healthPassed=true`、`dataPassed=true`、routePassed 和 DB identity 均通过，
   演练后恢复 A；
9. `artifacts/deploy/state-before.json` 锁定 A/B digest+attestation+config/DB identity、route、
   `schemaCompatibleWithRecovery=true` 与 `schemaCompatibleWithFinal=true`；Task 18 才 fence/drain A 并
   把 final digest B 接入同一兼容 final DB 和 `127.0.0.1:5001`；
   `artifacts/deploy/running-digest.txt` 在 Task 17 仍证明 A 在运行。Task 18 生成唯一
   `deploymentRunId`/`cutoverAttemptId` 并写入 state；B final runtime inspect 证明 RepoDigest/commit/
   attestation/config/final DB identity 后，才用同一 IDs 原子记录 B；回退 A 必须恢复实际 A identity；
10. B 通过真实域名完整矩阵和第二份 >=1800 秒公网观察后，写最终 after/diff，机器验收才允许解除
    failure trap 并完成发布；完整矩阵写入 `artifacts/acceptance/frontend-domain-e2e.json`，观察写入
    `artifacts/deploy/observation-30m.json`。

`artifacts/deploy/pull-and-up.txt` 是 versioned JSON event ledger，按顺序记录 A pull/shadow up/final up 与
B pull/shadow up/final up。事件包含 role、sourceCommit、expected/actual RepoDigest、Compose config、
runtime/data identity、时间、attempt IDs 和 `localBuild=false`/`localLoad=false`；Task 17 写到 B shadow，
Task 18 只在 B final inspect 通过后补 final up，失败/回退不能留下假 B 成功事件。

在 A 验证前不存在可信 recovery。A bootstrap 任一步失败只能 fence/drain A、撤回新 route、恢复原
502 路由，保留 final DB/outbox 已接受写入供调查并标记 `FAILED`；不得声称 5 分钟健康回滚或已经建立
recovery digest。A 验证后，previous digest 分支就是 B→A；它是实际
`rollbackMode=absent_two_stage` 的唯一当前适用分支。`artifacts/deploy/rollback-drill.txt` 中
`branches.initialDeployment.applicable=false` 且
`result=not_applicable_no_verified_legacy_service`。

Task 17 允许另一分支隔离等价演练，但只能操作隔离 old-service clone、隔离 DB clone 和影子路由；
不得修改在线旧服务 DSN、不得切换在线写流，`isolatedCompatibilityRehearsal` 不得计入实际
rollback/pass。只有未来 discovery 证明 `oldServicePresent=true` 且精确身份全部匹配，首次部署分支
才 applicable，并按唯一顺序完成新服务 fence/drain、outbox reverse replay 到唯一指定且实际承接
回滚流量的隔离旧库克隆、checksum、原子切换旧服务 DSN 到同一克隆、验证连接身份、恢复旧路由；
禁止用早期备份覆盖切流后新写入。

Task 18 在切 B 前安装统一 post-cutover failure trap，并在步骤 3、4、5、6 任一失败时立即
fence/drain 新写，自动调用已演练且适用的 rollback 分支，正常路径自动完成 B→A。步骤全部通过前的
旧侧可恢复状态就是 verified A recovery、A config/digest、同一 final DB 和权威 route 快照。若回滚
本身不能证明成功，则禁止恢复旧路由、不得让疑似坏版本继续接受写，保持 A/B 受控只读/隔离、保留
旧侧可恢复状态并记录 `FAILED`。`public-cutover.json` 记录适用或 notApplicable 的脱敏
full/final/reverse 坐标及 checksum/route/DB identity/duration/result。state/public-cutover/B E2E/B
observation/final acceptance/running digest/B final up 必须使用相同 `deploymentRunId`/`cutoverAttemptId`，
且时间属于当前 B 切换；原子成功文件写失败必须写当前 attempt 的 `passed=false` tombstone，或由 attempt
不匹配拒绝旧 `passed=true`，绝不复用 stale artifact。

资源上限保持 API 2 CPU/2 GiB、MySQL 1 CPU/1 GiB、Redis 0.5 CPU/256 MiB。主机保护采集 CPU、
MemAvailable、swap si/so、memory/io PSI、OOM、I/O error、磁盘与 inode；静态 swap 占用不单独误停，
但持续换页/PSI、低 MemAvailable、OOM/I/O 增量或磁盘/inode 越线会停止新重任务或触发适用回退。
所有 JSON/TXT/Markdown evidence 均是含 schemaVersion/passed/run/role 的 machine-readable 文件，以
同目录 0600 临时文件 flush、file fsync、原子 rename、directory fsync；失败写当前 run/attempt 的
passed=false tombstone，写不了时用 ID mismatch 拒绝旧 PASS。部署脚本用 `set -Eeuo pipefail`，trap 覆盖
ERR/EXIT/HUP/INT/TERM，mutation 前持久化 `in_progress`；启动发现未关闭 attempt 时先 inspect/fence，按
A→502 或 B→A reconcile 后才继续。任务 18 观察结束才补写容器 final after/diff，要求无关变化为零。

## 11. 验收标准

### 代码与安全

- `go test -race ./...` 通过，所有外部 API 有契约测试。
- `go vet ./...`、格式检查、Compose config、secret scan 通过。
- 无提交的密钥、Cookie、`.env`、私钥、媒体或数据库文件。
- 生产弱密钥/缺失关键配置启动失败，SSRF 表格测试覆盖 IPv4/IPv6/重定向/DNS 解析。
- AST/go-types policy 覆盖生产代码的全部网络出口和 Cookie alias；route-auth inventory 逐路由验证：
  fallback create 匿名但受 attempt/limit/SSRF；新 cache shareId 与 parse task ID 是 crypto-random
  >=128-bit、用途/TTL 绑定的 bearer capability（legacy shareId 仅限流只读）；fallback 使用服务端签名
  poll/download URL；m3u8 poll 按前端固定 `/api/task/:id` 使用随机 >=128-bit task ID，最终 file URL 才
  签名。发送 token 的 route 兼容 token/Bearer，不得强迫前端未发送的 query/header。
- root rewrite、GC 和 full scan 没完成前，R1 只能保持 pending；不得把 staged tree 或单次 worktree
  扫描写成“历史已清洁”。

### 解析基线

强制门槛严格采用 `测试结果基准.md` 的“仅主节点”同口径：

- 启用样本：93/96；
- 成功数：至少 62/93，即准确率不低于 66.67%；
- 批次墙钟时间：不超过 3 分 36 秒，即 216 秒；
- 解析配置：native 首选且 fallback 开启；
- 报告保存样本、状态、解析器、耗时、错误分类、开始/结束时间和服务 commit。

历史增强参考是 69/93，但它不替代用户点名文件的强制门槛。
worker 报告 23/93 中大量 HTTP 403，不能作为速度或准确率门槛。

`docs/baseline-provenance.json` 明确区分两个来源边界：用户提供但未被源 commit 跟踪的
`测试结果基准.md` 只绑定捕获内容 SHA-256，不伪称属于该 commit；93 样本 catalog 则绑定批准的
source commit/tree、输入文件清单和 manifest hash。Task 10 生成 fixture 后对导出文件完整 bytes 独立
计算 hash，把 `canonicalFixturePath`/`canonicalFixtureSha256` 写入 provenance trust anchor，禁止把
hash 写进 fixture 自身形成自引用；Go/Python 固定字面量复算并与 trust anchor 对账。fixture/report
自报 hash 只交叉核对，不能替代这些门禁；fixture 尚未生成时不得填伪造或占位 hash。

三轮报告不能只信 aggregate 或 `passed`。每轮 records 必须与 canonical enabled set 精确相等：93 个
唯一 sampleKey，无缺失、重复或跨轮复用；每个成功 record 同时含 media success 和本轮唯一
parserInvocationId。验证器从 records 重算 completed/success/wall-clock，并由 started/ended 半开区间
重算 `maxObservedConcurrency`（不得超过 3，93 个可运行样本应实际达到 3），要求三轮 unique runId、
时间窗不重叠且 record-set hash 独立。任一逐项记录失败即拒绝。

### API 与前端

- 健康、session、同步/异步解析、缓存分享、下载兜底、m3u8 和后台 E2E 全部通过。
- 使用当前 `miniprogram/utils/request.js`、`client-auth.js`、`download.js` 的契约测试通过。
- `docs/frontend-provenance.json` 固定原前端 clean commit/tree，以及 `miniprogram`、`test`、
  `project.config.json` manifest；Node/真实域名测试前后 guard 均通过且不修改原前端。
- 影子自动化通过 `http://127.0.0.1:15001` 完成服务契约；若启用受限 LAN override，当前前端可创建 session、提交异步解析、轮询完成、展示视频/图集、读取分享结果并创建下载兜底任务。LAN override 不是强制最终证据，也不能替代真实域名。
- 最终验收中，当前前端不修改默认 Base URL，通过 `https://watermark.bxsn.cn` 完成同一流程；
  已登录微信 DevTools/真机提供一次性 wx.login code 并完成真实 session；主机 Node/npm、空 code 或
  fake exchanger 不能替代该外部前置。request 和 downloadFile 均只使用已登记的 HTTPS 域名，证据
  不记录 code/openid/token/session/secret。
- A recovery 与 B final 的真实域名矩阵均固定包含 session、syncParse、asyncSubmit、asyncPoll、
  cacheRestore、performance、fallbackCreate/fallbackPoll/fallbackDownload、
  m3u8Create/m3u8Poll/m3u8Download、video、gallery；每项都有 requestId 和 passed。fallback/m3u8
  必须实际通过同域 HTTPS 下载，create/poll 成功不能代替 download。
- 所有 shadow/recovery/final E2E 只记录 route template、same-origin/HTTPS、状态、字节数/内容 hash 和
  requestId；share/task capability、ticket、原始媒体/分享 URL、完整 path/query 必须省略或不可逆 hash。

### 运行、迁移与部署

- GitHub Actions 分别构建 A recovery candidate 与 B final candidate。运行时实际 RepoDigest、attestation
  subject、OCI revision/source 必须绑定各自完整 source commit；tag 不能作为身份。
- `repository-and-image.txt`、SBOM、扫描和 digest 证据按 A/B role 分开，promotion map 连接两者；不得
  强迫 A/B 使用同一 `deployedSourceCommit`/RepoDigest。tracked final report 只记录
  `evidenceParentCommit` 与排除自身的 `evidencePayloadTreeSha256`；提交后由 `GITHUB_SHA`/`git rev-parse
  HEAD` 外部提供 `verifiedEvidenceCommit`，校验 parent、payload 和 docs/artifacts-only diff，不能自含
  当前 SHA 或冒充镜像 commit。
- MariaDB 11.8.6 → MySQL 8.4 的实际证据选择
  `chosenMigrationMode=final_full_no_binlog`，包含 final snapshot/import/checksum、table-scoped no-writer
  proof、shadow/final identity 隔离和 final 无本轮 shadow/A-B acceptance 的 runId/taskId/sentinel/
  outbox；合法 legacy 历史按 source checksum 保留。delta/reverse 合法标记
  notApplicable，不能伪造。
- A、B 各有一份公网观察。每份恰好 60 个唯一、严格递增的 raw sample，第一个在约 +30 秒，第 60 个
  不早于 +1800 秒，相邻间隔 25–35 秒；每个 sample 请求真实
  `https://watermark.bxsn.cn/api/health` 并绑定角色 digest，含 healthLatencyMs、restartCount、oomCount、
  ioErrors、MemAvailable/swap、memoryPSI/ioPSI、disk/inode。验证器从原始样本重算 60/60、P95 和资源
  停止线，不信任 aggregate。
- 当前 `rollbackMode=absent_two_stage` 必须先有 A domain+`A observation>=1800s`、A/B 等价性和
  B→A 真实 drill。previous digest 分支要求 durationSeconds<=300、health/data/route 及 final DB identity
  全部通过；initial 分支是 `not_applicable_no_verified_legacy_service`，隔离 rehearsal 不计入 passed。
- Task 18 的统一 post-cutover failure trap 保留到 B 的完整域名矩阵、公网观察、容器 final after/diff 和
  machine acceptance 全部通过。部署前后无关容器变化必须为零；完整证据必须绑定当前 attempt IDs，
  stale passed artifact 一律拒绝。
- `scripts/verify-acceptance.py --schema-of-present` 用于普通 push，
  `scripts/verify-acceptance.py --require-complete` 仅用于 final evidence push。complete 模式 fail closed
  校验 R1–R8 全部 artifacts、`schemaVersion`、`passed`、原子生成、role/source binding，并从原始基准/
  观察数据重算结论；缺失或自报 aggregate 不得通过。

## 12. 完成定义

只有设计、实现计划、代码、测试、GitHub 仓库、Actions 镜像、服务器 Compose 运行状态、
解析基准报告、前端直连报告、安全扫描和回滚证据全部存在且通过时，任务才完成。
本地单元测试通过、镜像只写了配置、或容器仅能启动都不能单独视为完成。
最终 docs/artifacts commit 同步更新 trace 与计划完成状态；workflow 只跑安全/证据门禁，不重建已验收
镜像或移动便利标签。
