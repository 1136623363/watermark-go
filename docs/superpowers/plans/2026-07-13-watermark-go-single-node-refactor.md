# Watermark Go 单机重构实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在 `/srv/watermark-go` 交付可由当前小程序直接使用的单机 Go 后端，由 GitHub Actions 构建 GHCR 镜像，并在 `192.168.31.222` 仅通过拉取镜像完成 Docker 部署。

**架构：** 采用 Go 模块化单体，HTTP/API、解析编排、认证、持久任务、缓存和数据访问通过明确接口组装；MySQL 是持久事实源，Redis 是可降级缓存。复用固定提交的原生解析器行为，选择性移植后期单机安全修复，删除集群、Jenkins 和多节点 HA 代码。

**技术栈：** Go module language `1.24.0`、首选工具链 `go1.26.5`、Gin、MySQL 8.4、Redis 7.4、yt-dlp、ffmpeg、受控 Python universal bridge、Docker Compose、GitHub Actions、GHCR、pytest/Node 契约测试。module language 与构建工具链刻意分开：当前宿主机可先执行仓库策略测试；任务 2 再通过 Go toolchain 自动获取或校验过的官方安装包启用 `go1.26.5`。

---

## 目标文件结构与职责

- `cmd/watermark-go/main.go`：信号处理和 `app.Run`，不包含路由或业务。
- `internal/app/app.go`：构造依赖、启动 worker/HTTP、优雅退出。
- `internal/config/config.go`：环境变量、默认值、生产强校验。
- `internal/httpapi/router.go`：组合公共、客户端、解析、下载、管理路由。
- `internal/httpapi/response.go`：`code/msg/data/requestId` 响应和兼容业务错误。
- `internal/httpapi/middleware.go`：request ID、恢复、日志、CORS、可信代理和限流。
- `internal/auth/`：客户端 session/token 和后台 session。
- `internal/parse/`：同步解析、异步任务、规范化、fallback、错误分类。
- `internal/parser/native/`：从旧提交迁移的 Go 平台解析器。
- `internal/parser/universal/`、`internal/parser/ytdlp/`：受控子进程适配器。
- `internal/netguard/`：URL、DNS、重定向和实际拨号目标 SSRF 防护。
- `internal/store/`、`migrations/`：MySQL 连接、迁移和仓储。
- `internal/cache/`：Redis/内存缓存、锁和限流。
- `internal/task/`：MySQL 持久任务租约、续租、重试和恢复。
- `internal/download/`：下载兜底、签名票据、Range 和临时文件清理。
- `internal/media/`：m3u8 验证、ffmpeg 合并和媒体结果验证。
- `internal/admin/`：后台登录、管理 API、样本和批量运行。
- `internal/observability/`：结构化日志、请求追踪和前端性能事件。
- `deploy/compose.yml`：只引用 GHCR SHA 镜像，无 `build:`。
- `.github/workflows/ci-image.yml`：测试、扫描、Buildx、GHCR 推送，无 Jenkins。
- `tests/contracts/`：当前小程序协议测试。
- `tests/e2e/`：运行中服务 E2E。
- `tests/baseline/fixtures/platform-samples.json`：排序稳定的 96 样本 manifest。
- `scripts/baseline/run.py`：并发 3、绕过缓存的 93 样本基准。
- `scripts/deploy-local.sh`、`scripts/rollback-local.sh`、`scripts/preflight.sh`、`scripts/observe.sh`、`scripts/verify-image.sh`、`scripts/host-snapshot.sh`、`scripts/smoke.sh`：只拉取/切换镜像、保护宿主机和验证。
- `tests/ops/test_scripts.py`：运维脚本的无副作用契约测试。
- `artifacts/`：可提交的脱敏验收证据；`reports/` 仅保存被忽略的本地临时输出。
- `约束文件.md`、`docs/requirements-traceability.md`：新项目单一权威约束与需求证据。

## 任务 1：建立新仓库边界与敏感信息门禁

**文件：**
- 修改：`.gitignore`
- 修改：`.dockerignore`
- 创建：`internal/policy/repository_test.go`
- 创建：`约束文件.md`
- 创建：`docs/requirements-traceability.md`
- 创建：`docs/source-provenance.json`
- 创建：`docs/frontend-provenance.json`
- 创建：`docs/baseline-provenance.json`
- 创建：`docs/research/media-parser-provenance.json`
- 创建：`docs/research/media-parser-review.md`
- 创建：`internal/policy/media_parser_research_test.go`

- [x] **步骤 1：编写失败的仓库策略测试**

```go
func TestRepositoryDoesNotTrackSensitiveOrRetiredDeploymentArtifacts(t *testing.T) {
    forbidden := []string{"jenkinsfile", "密码.pem", "服务器配置.md", "docker-compose.prod.worker.yml"}
    tracked := trackedFiles(t)
    for _, name := range forbidden {
        if tracked[name] { t.Fatalf("forbidden tracked artifact: %s", name) }
    }
}

func TestProductionComposeCannotBuildLocally(t *testing.T) {
    body := readRepoFile(t, "deploy/compose.yml")
    if strings.Contains(body, "build:") { t.Fatal("production compose must pull registry image") }
}
```

- [x] **步骤 2：运行策略测试并确认失败**

运行：`go test ./internal/policy -run TestRepository -count=1`

预期：FAIL，提示缺少新约束、Compose 或仍存在禁止产物。

- [x] **步骤 3：补齐 ignore 和单一约束文件**

`.gitignore` 与 `.dockerignore` 同时排除 `.env*`、`*.pem`、`*.key`、`*密码*`、
`*服务器配置*`、cache/logs/tmp、数据库、Redis、下载媒体和 Docker 导出文件。
新约束明确 Go 主业务、允许容器内受控工具、单机 Docker、Actions/GHCR、无 Jenkins、
并发 3 基准和服务器资源停止线。

- [x] **步骤 4：建立需求追踪矩阵**

矩阵逐项列出用户 8 条要求、当前前端接口、基准 `62/93 + 216000ms`、实现文件、测试和部署证据。

- [x] **步骤 5：运行测试和 secret 初筛**

运行：

```bash
go test ./internal/policy -count=1
git diff --check
```

策略测试自身扫描 tracked worktree、Git index、全部 heads/tags/HEAD 的可达历史、commit/tag message、
指向非 commit 对象的 refs、常见凭据形状、通用敏感变量默认值和全部 tracked YAML 中按顶层
`services` 识别的 Compose。对象按 OID 去重并通过单个 `git cat-file --batch` 读取；NUL、单对象超过
2 MiB 或累计对象超过上限都 fail closed。失败只报告哈希化 location/ref、行号、变量名与短 OID，
不回显字面量或潜在敏感路径。预期：测试 PASS；diff check exit 0。

- [x] **步骤 6：Commit**

```bash
git add .gitignore .dockerignore internal/policy 约束文件.md docs/requirements-traceability.md \
  docs/source-provenance.json docs/frontend-provenance.json docs/baseline-provenance.json docs/research
git commit -m "chore: establish repository safety policy"
```

该命令表示任务 1 的逻辑提交边界；安全前置纠偏完成后，最终仓库必须重建并复验为恰好一个
无 parent 根提交，因此不得声称保留了重建前的中间 commit 链。`docs/source-provenance.json` 必须
进入该最终根树并精确记录批准的 source commit 与 source tree。

**完成证据（2026-07-14）：** 批准树已重建为唯一无 parent 根：
`rootCommit=5a1dd14aa38c63091d8d7139fd0024718b79bdbb`、
`rootTree=525e9fb72308c4af5478ffcc1705d8af73c82c1e`。旧 refs/ORIG_HEAD/reflog 已清理并执行 aggressive GC/prune；
复验只有 `refs/heads/main`、恰好一个 root、该 root 无 parent、reflog 为空、fsck 无 unreachable
（`git fsck --full --unreachable --no-reflogs` 无输出）。`go test ./...`、`go test -race -p 2 ./...`、`go vet ./...`、格式/diff check、repository
policy 和固定 Gitleaks `v8.30.1 --log-opts=--all` full scan PASS，旧 Cookie blobs 不再可达或残留。

- [x] **步骤 7：审查批准后重建根并复验**

本轮 docs/代码 review 批准后，已由 sole writer 执行一次 root rewrite，把批准的 staged tree
写成唯一无 parent 根；随后删除临时 refs/reflog、执行可验证的 GC/prune，再对最终 refs、index、
worktree 和剩余 Git objects 做 policy + Gitleaks full scan。上述完成证据已通过；后续任务只允许从该
root 正常向前提交，禁止 fetch/恢复旧仓库 refs、旧对象或重写前历史。

**已暂存、待步骤 7 固化的安全前置纠偏：** `github.com/goccy/go-yaml` 因 Compose AST 门禁成为直接
依赖；旧树中的 `WEIBO_COOKIE`/`XIGUA_COOKIE` 已改为仅在环境变量非空时发送并保留回归测试。来源树中的通用签名
默认值、旧根 Compose、旧反向代理入口和可变上游同步脚本已从 staged tree 清除，且已补充
`docs/source-provenance.json`。`ucmao/media-parser` 研究也固定为 commit/tree/license hash，明确
`codeCopied=false`、只采用概念与测试设计且不成为 93 样本基线权威；这些事实已由步骤 7 的新根、GC
和全扫描固化并复验。

## 任务 2：重命名模块并建立可测试应用骨架

**文件：**
- 修改：`go.mod`
- 移动：`cmd/watermark-backend/main.go` → `cmd/watermark-go/main.go`
- 创建：`internal/config/config.go`
- 创建：`internal/config/config_test.go`
- 创建：`internal/app/app.go`
- 创建：`internal/app/app_test.go`
- 创建：`internal/server/application_config.go`
- 创建：`internal/server/service.go`
- 创建：`internal/server/service_test.go`
- 修改：`internal/server/infrastructure.go`
- 修改：`internal/server/migrations.go`、`internal/server/migrations_test.go`（迁移改为最外层显式、可取消 one-shot，禁止组件
  goroutine 调用 `os.Exit` 或使用脱离 process context 的 `context.Background`）
- 创建：`internal/policy/module_import_policy_test.go`
- 修改：`internal/server/main.go`、`internal/server/download_fallback.go` 及对应安全测试（只消费 typed config，
  删除业务层旧/新下载密钥双读和入口内信号处理）
- 修改：`internal/server/legacy_security_config.go` 并删除被 typed config 取代的
  `internal/server/legacy_security_config_test.go`
- 修改：步骤 4 精确扫描得到的全部 tracked Go import-prefix callsite

- [x] **步骤 1：编写生产配置失败测试**

```go
func TestLoadProductionRejectsMissingShortAndPlaceholderSecrets(t *testing.T) {
    cases := []struct{ key, value string }{
        {"ADMIN_PASSWORD", ""}, {"ADMIN_PASSWORD", "short"}, {"ADMIN_PASSWORD", "change-me"},
        {"ADMIN_SESSION_SECRET", ""}, {"ADMIN_SESSION_SECRET", "short"}, {"ADMIN_SESSION_SECRET", "example-test"},
        {"DOWNLOAD_TOKEN_SECRET", ""}, {"DOWNLOAD_TOKEN_SECRET", "short"}, {"DOWNLOAD_TOKEN_SECRET", "invalid-for-test-only"},
		{"WECHAT_MINI_APP_SECRET", ""}, {"WECHAT_MINI_APP_SECRET", "short"}, {"WECHAT_MINI_APP_SECRET", "placeholder"},
    }
    for _, tc := range cases {
        t.Run(tc.key+"/invalid", func(t *testing.T) {
            _, err := LoadWith(validProductionEnvironmentExcept(tc.key, tc.value))
            require.ErrorContains(t, err, "weak production secret")
        })
    }
}

func TestLoadRejectsUnknownEnvironment(t *testing.T) {
    _, err := LoadWith(getenvMap(map[string]string{"APP_ENV":"prodution"}))
    require.ErrorContains(t, err, "unknown APP_ENV")
}

func TestLoadProductionRequiresPersistentMySQLAndWechatIdentity(t *testing.T) {
    for _, key := range []string{"MYSQL_DSN", "WECHAT_MINI_APP_ID"} {
        env := validProductionEnvironment()
        env[key] = ""
        _, err := LoadWith(env)
        require.Error(t, err)
    }
    env := validProductionEnvironment()
    env["WECHAT_MINI_APP_ID"] = "change-me"
    _, err := LoadWith(env)
    require.Error(t, err)
}

func TestLoadMigratesLegacyDownloadSecretWithoutDualRuntimeReads(t *testing.T) {
    legacyValue := strongTestValue("legacy")
    canonicalValue := strongTestValue("canonical")

    legacyOnly := validProductionEnvironment()
    delete(legacyOnly, "DOWNLOAD_TOKEN_SECRET")
    legacyOnly["DOWNLOAD_FALLBACK_TOKEN_SECRET"] = legacyValue
    cfg, err := LoadWith(getenvMap(legacyOnly))
    require.NoError(t, err)
    assert.Equal(t, legacyValue, cfg.Download.TokenSecret)
    _, hasLegacyField := reflect.TypeOf(cfg.Download).FieldByName("FallbackTokenSecret")
    assert.False(t, hasLegacyField, "business config must expose only the canonical field")

    same := validProductionEnvironment()
    same["DOWNLOAD_TOKEN_SECRET"] = legacyValue
    same["DOWNLOAD_FALLBACK_TOKEN_SECRET"] = legacyValue
    cfg, err = LoadWith(getenvMap(same))
    require.NoError(t, err)
    assert.Equal(t, legacyValue, cfg.Download.TokenSecret)

    conflict := validProductionEnvironment()
    conflict["DOWNLOAD_TOKEN_SECRET"] = canonicalValue
    conflict["DOWNLOAD_FALLBACK_TOKEN_SECRET"] = legacyValue
    _, err = LoadWith(getenvMap(conflict))
    require.Error(t, err)
    assert.NotContains(t, err.Error(), canonicalValue)
    assert.NotContains(t, err.Error(), legacyValue)
}

func TestLoadErrorsAndDeprecationWarningsNeverExposeConfiguredValues(t *testing.T) {
    configured := strongTestValue("configured")
    env := validProductionEnvironment()
    env["MYSQL_DSN"] = "user:" + configured + "@tcp(db:3306)/watermark"
    delete(env, "DOWNLOAD_TOKEN_SECRET")
    env["DOWNLOAD_FALLBACK_TOKEN_SECRET"] = configured
    var warnings []string
    _, err := LoadWithOptions(getenvMap(env), LoadOptions{Warn: func(message string) {
        warnings = append(warnings, message)
    }})
    require.NoError(t, err)
    assert.NotContains(t, strings.Join(warnings, "\n"), configured)
}

func TestLoadProductionValidatesOptionalLegacyAESKey(t *testing.T) {
    for _, value := range []string{"", "short", "placeholder"} {
        env := validProductionEnvironment()
        env["APP_CLIENT_SIGNATURE_REQUIRED"] = "true"
        env["APP_CLIENT_SIGNATURE_KEY"] = value
        _, err := LoadWith(env)
        require.Error(t, err)
    }
}

func TestLoadSingleNodeDefaults(t *testing.T) {
    cfg, err := LoadWith(getenvMap(map[string]string{"APP_ENV":"test"}))
    require.NoError(t, err)
    assert.Equal(t, "5001", cfg.HTTP.Port)
    assert.Equal(t, 3, cfg.Baseline.Concurrency)
}
```

同一步先写 `TestLoadRunnerConfigAtEnvironmentBoundary`、
`TestLoadRunnerConfigDefaultsArePinnedAndSingleNodeSafe`、
`TestLoadRejectsInvalidRunnerConfigWithoutEchoingValues`、
`TestLoadKeepsMusicAndSohuCredentialsOpaque` 与
`TestLoadRejectsInvalidSensitiveMusicConfigWithoutEchoingIt`；先确认缺少 typed Runner/Sohu 配置时失败，
不能把这些测试推迟到 parser 迁移之后。

- [x] **步骤 2：运行配置测试并确认失败**

运行：`go test ./internal/config -count=1`

预期：FAIL，`Load`/`Config` 尚不存在。

- [x] **步骤 3：实现 typed config 和 app 生命周期接口**

```go
type Config struct {
    Environment string
    HTTP HTTPConfig
    MySQL MySQLConfig
    Redis RedisConfig
    Parser ParserConfig
    Runner RunnerConfig
    Tasks TaskConfig
    Download DownloadConfig
    Security SecurityConfig
    Baseline BaselineConfig
}

type Component interface {
    Start(context.Context) error
    Stop(context.Context) error
    Done() <-chan error // Start ready 后的唯一 terminal event；Stop 后也必须有界完成
}
```

`APP_ENV` 经 trim/lower 规范化后只接受 `development`、`test`、`production`，任何未知值或拼写错误
都失败，绝不按非生产环境继续。production 必须提供可用 `MYSQL_DSN`，因为 MySQL 是业务事实存储；
Redis 仍是可选的缓存/锁/限流组件并允许降级。production 同时要求非空且非占位的
`WECHAT_MINI_APP_ID` 与满足强度/占位词门禁的 `WECHAT_MINI_APP_SECRET`，禁止退回 clientId 身份。
`ParserConfig` 在配置边界读取可选 `WEIBO_COOKIE`、`XIGUA_COOKIE`；错误、弃用告警和配置摘要只记录
字段是否配置，绝不记录 Cookie、DSN、secret 或其 URL 编码形式。环境配置只来自 `ParserConfig`；
parser 构造函数接收一个 `Dependencies`，其中 `Config ParserConfig` 是唯一配置来源，其他字段只允许是
受控 Fetcher/clock/token provider/logger 等运行依赖。这为任务 3 删除业务包中的环境变量读取建立唯一
来源，同时避免把配置对象误当成完整依赖容器。

`RunnerConfig` 同样只在本边界读取并校验 engine/fallback、yt-dlp binary+timeout，以及 universal 的
Python binary、bridge script、video/music source、workdir、video/music timeout 和 item limit；默认 engine
为 native、fallback 关闭，所有默认路径指向任务 13 固定进不可变镜像的绝对路径。music provider JSON
与无法证明为公开协议标识的 Sohu token 使用不可直接格式化/序列化的 opaque sensitive value，只能经
显式 consumer 取值；测试覆盖 `%v/%+v/%#v`、错误和 summary 均不回显。Task 3 的 Runner 还必须对
production 重新验证 executable/source 位于镜像固定 allowlist、guard sandbox 已握手，否则 fail closed；
typed config 不能被解释成允许动态下载、PATH 搜索或任意命令执行。
这里的 typed music value 仅用于识别“配置存在”并安全迁移，**本轮 production 没有允许消费它的
helper 路径**：任何依赖该 secret 的 universal/music descriptor 固定返回 `credential_required`/disabled，
值不得进入 UDS/job/env/argv/log/output。Sohu token 只能由 API 进程内 native parser 的窄
`TokenProvider` 显式消费，不能下发 helper。

下载签名密钥只在 typed config 加载边界兼容旧名 `DOWNLOAD_FALLBACK_TOKEN_SECRET`：若规范名
`DOWNLOAD_TOKEN_SECRET` 缺失则映射旧值并记录不含值的弃用告警；两者同时存在但不一致时启动失败。
任务 2 之后所有业务代码只读取 `Config.Download.TokenSecret`，不得继续直接读取任一环境变量；任务 9
删除旧名的运行时读取，部署样例只写规范名。这是唯一迁移窗口，不能形成双变量静默优先级。

入口捕获 `SIGINT/SIGTERM`，设置 20 秒退出预算；配置加载失败时退出且不监听端口。`Start` 只有在组件
真正 ready 后才能返回；`App.Run` 同时等待 process context 与每个已启动组件的 `Done`，任一组件在
ready 后异常退出必须保留原始错误、逆序停止其余组件并让进程非零退出，禁止 HTTP 已死但进程假活。
startup cancellation 在 listener ready 前后都必须有界完成，不能在 cancel 后继续 bind/Serve 或无界等
goroutine。红测至少覆盖 post-ready Serve failure、ready 前 cancel、ready 后 cancel、启动失败逆序清理
与 Stop 幂等。

旧迁移 argv 分支不能留在 server component：任务 2 即把它移到 `cmd/watermark-go` 最外层的显式
one-shot command，注入 process context 并只返回 error；只有 `main` 可以决定 `os.Exit`。one-shot 不构造
HTTP app/listener/worker，也不与 `ready/done` 竞争。任务 5 再把该命令扩展成绑定 receipt 的完整
`data-gate`，不能以“后续会重构”为由提交当前竞态。

- [x] **步骤 4：重命名 module 与入口**

模块名改为 `github.com/1136623363/watermark-go`，`go.mod` 使用 module language `go 1.24.0` 并固定首选 `toolchain go1.26.5`；入口只负责 `config.Load()`、显式 argv one-shot 分派以及 `app.New()`/`Run()`，不得包含路由或业务实现。若宿主机尚无该工具链，任务 2 允许 Go 自动下载，或安装经过校验的官方 `go1.26.5` 归档；不得把“宿主机当前仍是 Go 1.24.4”误判为仓库策略失败。

同一步先用 `git grep -l '"watermark-backend/' -- '*.go'` 记录精确文件清单，再机械替换全部 tracked Go
import prefix 为 `github.com/1136623363/watermark-go/`，执行 `go mod tidy`。替换后
`git grep -n 'watermark-backend/' -- '*.go'` 必须零结果；不得只改 go.mod/cmd 后用 scoped tests 掩盖全树
不可编译。机械修改的每个精确路径都纳入本任务 commit，提交前以 cached name list 对账。

- [x] **步骤 5：验证骨架**

运行：

```bash
test -z "$(git grep -n 'watermark-backend/' -- '*.go')"
GOMAXPROCS=2 go test ./... -count=1
```

预期：全部 PASS。

- [x] **步骤 6：Commit**

```bash
git add -- go.mod go.sum cmd/watermark-go internal/config internal/app \
  internal/server/application_config.go internal/server/service.go internal/server/service_test.go \
  internal/server/main.go internal/server/infrastructure.go \
  internal/server/migrations.go internal/server/migrations_test.go \
  internal/server/download_fallback.go internal/server/download_fallback_secret_test.go \
  internal/server/security_defaults_test.go internal/server/legacy_security_config.go \
  internal/server/legacy_security_config_test.go internal/policy/module_import_policy_test.go \
  internal/policy/repository_test.go
# 另按步骤 4 保存的精确清单逐个 git add -- 所有 import-prefix 机械修改文件
git diff --cached --name-only
git commit -m "refactor: add typed configuration and app lifecycle"
```

## 任务 3：迁移解析器并消除源码凭据

**文件：**
- 创建：`internal/netguard/url.go`
- 创建：`internal/netguard/validator.go`
- 创建：`internal/netguard/transport.go`
- 创建：`internal/netguard/validator_test.go`
- 移动：`internal/parsers/native/*` → `internal/parser/native/*`
- 移动：`internal/parsers/universal/*` → `internal/parser/universal/*`
- 创建：`internal/parser/parser.go`
- 创建：`internal/parser/descriptor.go`
- 创建：`internal/parser/result.go`
- 创建：`internal/parser/result_test.go`
- 创建：`internal/parser/registry.go`
- 创建：`internal/parser/registry_test.go`
- 创建：`internal/parser/session_material.go`
- 创建：`internal/parser/session_material_test.go`
- 创建：`internal/parser/native/descriptors.go`
- 创建：`internal/parser/native/structured_json_test.go`
- 创建：`internal/parser/native/testdata/catalog.golden.json`（精确锁定现有 26 key、41 host rule、21 个
  SupportsID、每项 capability 与 QueryKeys，不含研究项目未批准 alias）
- 创建：`internal/parser/native/testdata/structured/`（Bilibili/快手的完整、转义、截断、字段换层、
  登录/风控、空核心字段和多载体脱敏合成 fixture）
- 创建：`internal/parser/native/testdata/`（脱敏合成 golden，不含 Cookie/token/个人信息/媒体本体）
- 创建：`internal/parser/ytdlp/runner.go`
- 创建：`internal/policy/parser_egress_test.go`（Task 3 先封 parser scope，Task 4 扩为全 production）
- 修改：`internal/parser/native/weibo.go`
- 修改：`internal/parser/native/xigua.go`
- 修改：`internal/parser/native/sohu.go`（固定 API-key-like material 必须分类并移出源码或证明为公开协议标识）
- 修改：`internal/parser/universal/bridge.go`
- 修改：`internal/config/config.go`、`internal/app/app.go`（仅当 RunnerConfig/wiring 需要）
- 修改：`go.mod`、`go.sum`（仅当 netguard/parser 依赖实际变化）
- 重写：`docs/目录与解析器架构.md`（删除上游同步/热更新/可变 tools 叙述，改为 descriptor + pinned runner）
- 修改：`internal/policy/parser_cookie_security_test.go`（迁移路径后继续覆盖 production parser）
- 修改：所有由 production import 精确字面量
  `git grep -l '"github.com/1136623363/watermark-go/internal/parsers/' -- '*.go' ':!internal/policy/**'`
  发现的现有 callsite（含尚未拆除的
  `internal/server`），同一 commit 改到 `internal/parser`；禁止留下破坏全树编译的旧 import

**安全原子依赖：** Task 3 先实现 `internal/netguard` 的安全 core（typed URL、请求前/DNS/dial/redirect
校验和受控 transport/Fetcher），再迁移任何 production parser；Task 4 在此基础上完成全树出口、
subprocess proxy 与 AST/go-types 门禁。Task 3 不能提交任何临时裸 HTTP client/adapter，也不能先迁移
parser、留待 Task 4 才修网络安全。

- [x] **步骤 1：编写 parser 契约和注册表失败测试**

```go
type Parser interface {
    Parse(context.Context, Request) (Result, error)
}

type HostRule struct {
    Host              string
    IncludeSubdomains bool
}

type Descriptor struct {
    Key          PlatformKey // 稳定 ASCII ID；展示名不作内部主键
    DisplayName  string
    Aliases      []PlatformKey
    HostRules    []HostRule // 明确 exact host 或受控 subdomain，禁止 Contains
    Capabilities Capability
    Priority     int
    QueryKeys    []string
    SupportsID   bool
    MaxRequests  int
    MaxRedirects int
    New          func(Dependencies) (Parser, error) // 纯构造，不联网/读环境/启动进程
}

func TestRegistryContainsLegacyNativePlatforms(t *testing.T) {
    registry, err := NewRegistry(native.Descriptors())
    require.NoError(t, err)
    got := registry.CatalogSnapshot()
    want := mustReadCatalogGolden(t, "native/testdata/catalog.golden.json")
    require.Equal(t, 26, len(got.Platforms))
    require.Equal(t, 41, got.HostRuleCount())
    require.Equal(t, 21, got.SupportsIDCount())
    require.Equal(t, want, got)
}

func TestRegistryRejectsAmbiguousDescriptorMetadata(t *testing.T) {
    descriptors := []Descriptor{
        {Key:"douyin", HostRules:[]HostRule{{Host:"v.example"}}, Aliases:[]PlatformKey{"dy"}},
        {Key:"other", HostRules:[]HostRule{{Host:"v.example"}}, Aliases:[]PlatformKey{"dy"}},
    }
    _, err := NewRegistry(descriptors)
    require.Error(t, err)
}

func TestParserFetchesUpstreamOnceForRichMediaResult(t *testing.T) {
    fetcher := &countingFetcher{fixture: richMediaFixture()}
    got := parseWith(fetcher)
    assert.Equal(t, int32(1), fetcher.calls.Load())
    require.NotEmpty(t, got.Images[0].LivePhotoURL)
    require.NotEmpty(t, got.AudioURL)
}

func TestSnapshotHonorsDescriptorRequestBudgetAndRejectsDuplicateURL(t *testing.T) {
    parser := multiStageParser(Descriptor{MaxRequests: 3, MaxRedirects: 2})
    _, err := parser.Parse(t.Context(), requestForFixture("bilibili"))
    require.NoError(t, err)
    assert.LessOrEqual(t, parser.fetcher.Calls(), 3)
    require.ErrorIs(t, parser.fetcher.FetchAgain(sameFetchURL()), ErrDuplicateFetch)
}

func TestParserConstructionPerformsNoIO(t *testing.T) {
    deps := failingOnUseDependencies()
    parser, err := descriptorFor("bilibili").New(deps)
    require.NoError(t, err)
    require.NotNil(t, parser)
    assert.Zero(t, deps.TotalCalls())
}

func TestEveryProductionParserRejectsEmbeddedCookieHeaders(t *testing.T) {
    // 对 internal/parser 下每个 production Go blob 执行 AST 门禁：map/index assignment、
    // Set/SetHeader/Add、字符串拼接，以及 const/VarSpec/:= alias 均不得形成 Cookie 字面量。
    require.NoError(t, auditProductionParserCookies("internal/parser"))
}
```

同一步必须先写并确认以下命名红测失败，不能只用 registry 数量测试替代研究成果的行为闭环：

- `TestDescriptorCapabilitiesMatchResult`：descriptor 声明的 video/gallery/audio/live-photo 与实际结果一致；
- `TestRegistryRejectsUnknownHostWithTypedError`：未知 host 返回稳定 typed error，绝不猜测或 fallback；
- `TestStructuredJSONGoldenMatrix`：覆盖 Bilibili/快手的完整、转义、截断、字段换层、登录/风控、
  空核心字段与 INIT_STATE/Apollo 多载体，fixture 标记 `synthetic=true` 且不含真实会话/个人数据；
- `TestMediaCandidateOrderIsStable` 与 `TestCandidateFallbackHonorsTotalBudget`：显式质量 comparator、稳定
  tie-breaker、缺 metadata 时保留 parser 顺序，且候选切换共用总次数/总时限预算；
- `TestScopedSessionMaterialInvalidatesOnlyOnTypedExpiry`：只有 typed `session_expired` 使 exact
  platform+host 的短期材料失效并至多刷新一次，取消、超时、安全拒绝、`schema_changed`、
  `credential_required` 与 internal error 都不能触发刷新风暴。

`catalog.golden.json` 还要逐 descriptor 锁定 query policy，而非只锁总数。表格至少包含研究中已识别的
`vid`、`id`、`xsec_token`、`modal_id`、`v`、`s`、`pid`，以及当前平台明确允许的其他 key；测试覆盖
重复 key、大小写、空值、百分号编码、稳定排序和 tracking 剥离。会话/capability query 的原值或可逆
编码不得进入日志、错误、`CacheKey`、fixture report 或 evidence。

Cookie AST 门禁必须用对象绑定实现 scope-correct alias 解析：覆盖 package/local `const`、`VarSpec`、
短声明 `:=`、多赋值、拼接和赋值传播，正确处理内层 shadowing，不能按变量名做跨作用域串联。恶意
fixture 分别覆盖 const/var/:= alias 与 shadowing，安全 fixture 证明同名局部变量不会误报。
同一步先写 parser-local AST/go-types 红测：`internal/parser`（含 generated production，只排 `_test.go` 和
netguard adapter）不得自建 http/resty client/transport、用 DefaultClient/Get/Post/Head、直接 `net.Dial*`、
调用 `os.Getenv`/`os.LookupEnv`/`os.Environ`，或绕过结构化 Runner 调用 `exec.Command*`。Task 4 把同一
分析器扩到全 production tree；不能等 Task 4 才让 parser scope 首次安全。
`m3u8` 不属于现有 26-platform native catalog，Task 3 不伪造对应 descriptor；它作为安全媒体合并能力由
Task 9 在 Go 预取/本地 ffmpeg 门禁完成后注册路由与 capability。

- [x] **步骤 2：确认测试失败**

运行：`go test ./internal/netguard ./internal/parser/... -count=1`

预期：FAIL，netguard core、parser 包和接口尚未建立。

- [x] **步骤 3：先建立安全 core，再机械迁移并适配 Parser 接口**

先实现 `internal/netguard` 的安全 core：不可日志化 `FetchURL`、允许持久化/响应的 `SafeURL`、请求前
host/IP 校验、DNS/实际 dial 绑定、每跳 redirect 重验、跨 origin/host 敏感 header 剥离、TLS 强校验，
以及带 response header/wire body/解压后 body/时长上限的受控 `Fetcher`。core 使用依赖注入 resolver/
dialer 形成 hermetic 测试，禁止 production parser 自建 client/transport。Task 4 再补 subprocess egress
proxy、全 production tree 出口迁移和 AST/go-types 封口；Task 3 不能提交任何临时裸 HTTP。

保留固定提交中 URL/ID 解析、平台别名和字段行为；`redbook→xiaohongshu`、
`quanminkge→kgqq`、`xigua→ixigua` 兼容在规范化层完成。native/universal/ytdlp 构造函数显式接收
一个 `Dependencies`；其中环境值只能来自任务 2 的 `Config ParserConfig`，其余是 netguard Fetcher、
clock、token provider、logger/runner 接口。迁移完成后 `internal/parser` 内不得出现
`os.Getenv`/`os.LookupEnv`。
universal 与 yt-dlp 命令构造测试必须证明无法省略或覆盖任务 4 提供的 localhost netguard egress proxy。

按 `docs/research/media-parser-review.md` 吸收其集中注册和富媒体能力思路，但不复制上游代码：注册表使用
metadata-driven `Descriptor`，至少包含稳定 ASCII `PlatformKey`、display name、aliases、精确 domains、
capabilities（video/gallery/audio/live-photo/m3u8）、确定性 priority、每平台允许保留的 query keys 与
constructor。
`catalog.golden.json` 必须与当前 26 platform key、41 host rule、21 个 ID 能力逐项完全相等；测试拒绝
缺失、多出、重排后语义漂移或把 `media-parser` 的 50-domain 候选静默并入 production。研究 alias 只能
经独立合法性、安全、契约和基准审查后由显式任务更新 golden，不能使用 `Subset` 放宽目录。

本次对固定 commit 与 `ucmao/media-parser` 的逐条复核还明确了一个兼容性裁决：研究项目的 50 条
`exact netloc` 不是 baseline authority，不能用来静默缩小固定 commit 对现有 41 条 domain 一律执行的
label-boundary controlled-subdomain 契约。Task 3 保持并由 golden/行为测试锁定这 41 条显式
`IncludeSubdomains=true`，同时继续拒绝 `<approved-host>.evil` 这类恶意后缀。把其中 31 条收紧为 exact、
其余 10 条改成完整 exact alias 集合是 Task 10 的隔离研究候选：只有 canonical/legacy fixture、93 样本、
前端契约和独立安全审查证明不减少行为后，才能由单独变更更新 production golden；不得把安全理想值
冒充已经批准的兼容性变化。
构建 registry 时拒绝重复或歧义的 key/alias/domain、保持确定顺序并使用规范 host 精确匹配；研究项目的
50 个 domain alias 只作候选目录，未经当前来源、canonical 93 样本或新增独立 fixture 验证不得进入
production registry。`Result` 内部增加强类型 `ImageAsset{URL, LivePhotoURL}` 与
`MediaCandidate{URL, Kind, Quality, Bitrate, Width, Height, SourceRank}`；一次 Parse 只抓取/解析一个上游
快照，不能由多个 getter 重复联网。候选按显式元数据与稳定 tie-breaker 排序并受统一重试预算约束，
禁止照搬“固定数组下标即最高质量”的假设。

所有 parser constructor 必须零 I/O，只接收注入的 netguard client、clock、短期 token provider、logger
和 typed config；不得在构造/import/package init 时联网、读环境、创建目录或启动进程。优先结构化 API；
需要从页面提取嵌入 JSON 时使用转义/嵌套感知实现，并以脱敏合成 golden 覆盖完整、截断、字段换层、
登录/风控页、空核心字段和多载体分支，不保存真实响应中的会话/个人数据。

本轮采用受限 `SessionMaterialProvider`，不复用上游类级全局缓存：key 只由稳定 platform key + exact
normalized host 组成，配置 TTL、singleflight 与硬容量上限，value 为不可格式化/不可序列化的敏感类型。
仅 parser 返回 typed `session_expired` 时允许原子失效并在同一 Parse 总请求预算内刷新一次；timeout、
context cancellation、security rejection、`schema_changed`、`credential_required`、internal error 或
普通空结果均不得失效/重取。provider 没有源码常量 fallback，过期/驱逐/取消也不得把材料写入日志、
错误、结果、普通 cache 或 evidence。`session_material_test.go` 对并发 singleflight、TTL、容量、exact-host
隔离、一次刷新上限与所有不失效错误做 deterministic 测试。

“一次上游快照”定义为每次 `Parse` 只调用一次平台 `AcquireSnapshot`，不是强迫每个平台只发一个 HTTP
请求。多阶段 snapshot 可包含 descriptor `MaxRequests/MaxRedirects` 内的多个响应，但禁止重复抓同一
FetchURL 或多个 getter 分别联网；单页 rich-media fixture 才断言物理 fetch=1，Bilibili/CCTV/短链等另测
总请求/重定向预算、取消和重复 URL 拒绝。`DisplayName`、ID 能力和 host rules 只来自 registry snapshot，
server/admin 不再保留第二份 `platformNames` 或导出的可变 `VideoSourceInfoMapping`。

Task 2/3 的 typed `ParserConfig`/`RunnerConfig` 必须覆盖 engine/fallback、固定 binary/script/source/workdir、
video/music timeout、item limit 与 opaque sensitive music config 引用；app 从 typed config 构造依赖，
parser/runner 禁止回读 `runtimecfg` 或环境。`os.Environ` 同样禁止，subprocess env 从空集合构造最小 allowlist。
Sohu 的固定 API-key-like query material 不得原样迁移：先以公开一手协议证据判定；无法证明时改为可选
typed token provider，缺失返回 `credential_required`，错误/测试不回显值；provider 只注入 API 内 native
Sohu adapter。opaque music config 非空时依赖它的 universal/music descriptor 仍必须 production disabled/
`credential_required`，不得把值交给 Runner/helper。跨 UDS payload、helper env/argv、child env、日志、
错误和 output 各注入独立 sentinel，逐层断言无原值或可逆编码。

Task 3 尚未有 Task 4 的 network-isolated helper/proxy runtime。universal/yt-dlp runner constructor 在无
已验证 `GuardProxy`/sandbox endpoint 时必须返回 error，production descriptors 保持 disabled；argv 只允许
唯一、不可覆盖 proxy，env 不继承父进程。Task 4 完成 helper/proxy handshake 和隔离拓扑后才启用，禁止
Task 3 在 API 内执行裸 subprocess 或把“disabled”冒充 fallback enabled。

移动前保存上述 production import 精确字面量的清单；移动后机械更新所有 callsite，并要求同一命令零
结果。`internal/policy/**` 必须继续把旧 `internal/parsers/` 路径作为 legacy history scanner 的兼容输入，
因此禁止用全树 substring 零命中误删历史扫描分支或恶意 fixture。`docs/目录与解析器架构.md` 需同时重写，
删除旧的运行时上游同步脚本、组件热更新、可变 `/app/tools`/yt-dlp 自动更新叙述，改为固定 provenance、
descriptor registry、pinned bridge/runner 与 Actions-only immutable image 边界。不得等 Task 11 删除旧
server 才修 import，也不得用 scoped parser tests掩盖 broken tree。

- [x] **步骤 4：保留并复验已前置完成的 Cookie 安全边界**

安全纠偏已将微博和西瓜请求改为仅在 typed `ParserConfig` 对应 Cookie 非空时添加 Cookie header；
本步骤迁移文件时必须保持空值不设置、trim 后注入的行为与回归测试。AST policy helper 同时接入仓库
audit 的 history/index/worktree blob 扫描，路径兼容 `internal/parsers/` 与 `internal/parser/`；安全
worktree 不能遮住 staged Cookie literal，测试/仓库默认不含 Cookie，也不得保留“取消注释恢复”说明。
现有 live unit test 必须保留行为覆盖但改为 fake Fetcher + 脱敏 golden；`go test ./...` 不访问真实上游。
测试不得打印派生 token、完整 URL/query 或上游响应。universal 从无测试状态补齐 constructor/env/output/
timeout/process-group/Raw-field allowlist 覆盖；未知 `Raw map` 字段不得进入统一 Result、日志或响应。

- [x] **步骤 5：验证原生解析器回归**

运行：

```bash
GOMAXPROCS=2 go test ./internal/netguard ./internal/parser/... ./internal/policy -count=1
if git grep -nE 'os\.(Getenv|LookupEnv|Environ)' -- internal/parser; then exit 1; fi
if git grep -n '"github.com/1136623363/watermark-go/internal/parsers/' -- '*.go' ':!internal/policy/**'; then exit 1; fi
GOMAXPROCS=2 go test ./... -count=1
```

预期：全部 PASS，原固定提交 parser 测试均保留。

- [x] **步骤 6：Commit**

```bash
# 同时 stage rename/delete/new；不要在 git mv 后再执行可能 pathspec 失败的 git rm。
git add -A -- internal/parsers internal/parser
git add -- internal/netguard internal/policy/parser_cookie_security_test.go \
  internal/policy/parser_egress_test.go docs/目录与解析器架构.md
# 逐个 git add -- 步骤 3 保存清单中的 7 个精确 server callsite；若 wiring/deps 变化再精确加入：
git add -- internal/server/admin_handlers.go internal/server/admin_test_samples.go \
  internal/server/admin_test_samples_test.go internal/server/main.go internal/server/parse_attempts.go \
  internal/server/universal_parser.go internal/server/ytdlp.go
# 条件清单：internal/app internal/config go.mod go.sum 只在本任务实际修改并审查后 stage。
git diff --cached --name-only
git commit -m "refactor: isolate parser adapters and remove embedded credentials"
```

## 任务 4：实现统一网络安全层

**文件：**
- 修改：`internal/netguard/url.go`
- 修改：`internal/netguard/validator.go`
- 修改：`internal/netguard/transport.go`
- 修改：`internal/netguard/validator_test.go`
- 创建：`internal/netguard/authority.go`
- 创建：`internal/netguard/authority_test.go`
- 创建：`internal/netguard/proxy.go`
- 创建：`internal/netguard/proxy_test.go`
- 创建：`cmd/parser-helper/main.go`
- 创建：`cmd/parser-helper/main_test.go`
- 创建：`cmd/netguard-proxy/main.go`
- 创建：`cmd/netguard-proxy/main_test.go`
- 创建：`internal/parser/sandbox/client.go`
- 创建：`internal/parser/sandbox/server.go`
- 创建：`internal/parser/sandbox/sandbox_test.go`
- 创建：`internal/policy/network_egress_test.go`
- 创建：`tests/policy/test_python_bridge_security.py`
- 修改：`bridges/universal/python/bridge.py`
- 修改：`internal/parser/native/http_client.go`
- 修改：`internal/parser/universal/bridge.go`
- 修改：`internal/runtimecfg/settings.go`
- 修改：`internal/server/client_auth.go`
- 修改：`internal/server/download_fallback.go`
- 修改：`internal/server/cluster.go`
- 修改：`internal/server/cluster_platform_tests.go`
- 修改：`internal/server/main.go`
- 修改：`internal/server/ytdlp.go`
- 修改：`internal/server/universal_parser.go`
- 修改：`internal/server/m3u8_task.go`
- 修改：`internal/server/tool_updates.go`
- 修改：`internal/server/infrastructure.go`（仅 DB/Redis typed connector 的窄豁免，不得泛化为 HTTP/dial）

任务 3 已提供 netguard core，Task 4 不得另建第二套 validator/transport；本任务把同一 core 扩展到
全树出口、subprocess egress proxy、response/resource 完整矩阵和 AST/go-types 封口，并机械迁移全部
production callsite。任何无法强制使用同一 guard 的网络路径在 production fail closed。

- [x] **步骤 1：编写 SSRF 表格测试**

覆盖 `127.0.0.1`、`::1`、RFC1918、CGNAT、链路本地、metadata、十进制/八进制 IP、IDNA、大小写、
尾点、userinfo、非常规端口、redirect loop、重定向到私网、DNS 首次公网随后私网，以及允许的公网地址。
特殊地址矩阵还覆盖 benchmark/documentation/multicast/unspecified/reserved、IPv4-mapped IPv6、混合
public/private DNS answer；DNS error/timeout/NXDOMAIN/空答案全部 fail closed。拒绝 opaque/scheme-relative/
fragment/control/backslash/非法 escape/IPv6 zone/非法 bidi 或 joiner；端口按 purpose allowlist。
另测跨 origin/host redirect 必须剥离 Cookie/Authorization/平台会话 header，response header、wire body、
解压后 body 任一超限都拒绝；禁止盲目 HTTP→HTTPS 字符串改写和跳过 TLS 校验。

同时先写 purpose-scoped authority 红测，不能把“输入识别目录”误当成“解析器可访问的全部公网”：

- `TestPurposeScopedOutboundAuthority`：`InputShare`、`MetadataAPI`、`SessionBootstrap/SessionConsumer`、
  `MediaCandidate` 四类用途互不替代；
- `TestEveryNativeFixedEndpointHasPolicyOwner`：production parser 中每个固定 API authority 都有唯一
  descriptor/purpose owner，漏登记立即 fail closed；
- `TestParserAPIAuthorityCannotBeUsedAsInputRoute`：例如 `api.bilibili.com` 只允许 Bilibili metadata，
  绝不能因此成为用户输入 host；
- `TestSensitiveHeadersNeverReachDynamicMediaCandidateHost` 与 `TestCrossPurposeRedirectFailsClosed`：动态
  CDN 可在公网/资源门禁内使用，但永远不继承 Cookie、Authorization、session、Origin 或跨源 Referer，
  redirect 也不能从低权限 purpose 升级到凭据/API purpose。

```go
func TestDialContextRejectsResolvedPrivateTarget(t *testing.T) {
    resolver := fakeResolver{"media.example": {netip.MustParseAddr("127.0.0.1")}}
    guard := New(WithResolver(resolver))
    _, err := guard.DialContext(context.Background(), "tcp", "media.example:443")
    require.ErrorIs(t, err, ErrPrivateTarget)
}
```

同时先写 `internal/policy/network_egress_test.go` 红测。门禁对 `cmd/`、`internal/` 的 production Go
只排除 `*_test.go`；generated production 同样扫描，`internal/netguard/` 也不得目录级豁免。validator/
transport/proxy 仅对必要网络 primitive 使用对象绑定的精确文件+精确符号 allowlist，并有独立 self-audit，
新增 `backdoor.go` 或在允许文件增加未列符号仍失败。其余代码执行 AST/go-types 分析，解析正常 import、
import alias 与 dot import；在 netguard 外拒绝 `&http.Client`/`http.Client{}`、`http.Transport`、
`http.DefaultClient`、`http.DefaultTransport`、`http.Get/Post/Head`、`net.Dial*`、`net.Dialer` 直建，
第三方 client constructor（含 import alias/dot import 的 `resty.New`）未注入 netguard transport，
以及自定义 RoundTripper/`RoundTrip` 实现或赋值。它还分析 `os/exec.Command*`：production subprocess
只允许列入 allowlist 的 yt-dlp/universal/ffmpeg adapter 通过结构化 argv builder 启动，拒绝 sh/bash
`-c`、curl/wget/nc/python raw shell 和动态可执行名。测试包含每一种绕过、alias/dot import 的恶意 fixture 和经
`netguard.NewTransport` 构造的安全 fixture，不能只 grep `DefaultClient/Get`。
同一 AST/go-types 门禁拒绝 production 中 `tls.Config{InsecureSkipVerify:true}`、可把验证后的目标替换为
另一 host 的自定义 redirect hook，以及 parser 自行创建 client/transport；测试/fixture 也不得包含可被
复制到 production 的固定会话或 tokenized URL。
门禁必须由 AST 实际枚举全树出口并生成精确 callsite 清单，至少覆盖现存的 `server/main.go`、
`server/ytdlp.go`、`parser/universal/bridge.go`、`server/universal_parser.go`、`server/m3u8_task.go`、
`server/tool_updates.go` 和 `server/infrastructure.go`；文件表不是 allowlist，出现新出口也必须失败。
DB/Redis 连接只可通过 typed DSN/config connector 的窄对象绑定豁免，不能使任意 `net.Dial` 或 HTTP
逃逸。负测还要让 Python/第三方 helper 尝试 raw socket 直连，证明仅设置代理环境变量不能通过门禁。
Go AST 之外，`tests/policy/test_python_bridge_security.py` 用 Python AST/结构化配置检查
`bridges/universal/python/bridge.py`：删除任意 `requestOverride`/`requests_overrides`，禁止调用方覆盖
proxies/verify/stream/redirect 或注入 session/header；最多允许 clamp 到更严格的 timeout/size。bridge
源码与固定 hash 纳入 policy，直接 requests/socket/subprocess 新出口未走 sandbox adapter 即失败。

- [x] **步骤 2：确认失败**

运行：`go test ./internal/netguard ./internal/policy -run 'TestDial|TestProductionNetworkEgress' -count=1`

预期：FAIL，Task 3 的 core 已存在，但全树出口门禁、subprocess proxy 与剩余 callsite 尚未完成。

- [x] **步骤 3：实现请求前、重定向和 DialContext 三层校验**

类型层区分不可日志化且只供受控 client 使用的 `FetchURL`、允许持久化/响应的 `SafeURL` 和用途域分离的
不可逆 `CacheKey`，避免普通 `String()` 意外输出完整敏感 query。不得通过字符串替换假设目标支持 TLS。
所有业务 HTTP client 只能由 `netguard.NewTransport` 创建；代理地址显式配置，目标地址仍逐跳校验。
resty 只能在 netguard adapter 内由已注入的受控 transport 构造，禁止默认 transport。
禁止替换全局 `http.DefaultClient/DefaultTransport`。DNS 预检不是证明：实际 DialContext 再解析全部结果、
全部校验后只把已验证 literal IP 交给底层 dialer，原 canonical host 仅用于 Host/TLS SNI。ambient
HTTP(S)_PROXY/ALL_PROXY/NO_PROXY 一律忽略；外部 HTTP/SOCKS proxy 只有在自身地址先验证且使用 pinned-IP
CONNECT、不让远端重新解析目标时才可启用，`socks5h`/remote DNS 和 proxy userinfo 禁止进入普通配置、
DB、日志或 0644 文件。

`internal/netguard/authority.go` 维护与 Descriptor 绑定、不可由 parser 伪造的用途能力：用户输入只走
现有 41 条显式 HostRule；Bilibili/虎牙/皮皮虾/微博/腾讯视频等固定 metadata/API host 必须逐项 exact
登记到对应 parser；session bootstrap 与 consumer 继续 exact host 且只有该用途能取得敏感材料；动态
MediaCandidate 仅获得无凭据的公网 fetch 权限。校验发生在首次请求、每次 redirect 和实际 dial 之前，
并把 parser key、purpose 与 authority policy fingerprint 绑定到同一 request budget。禁止 parser 通过
自行拼接静态 API URL、借用另一 parser 的 endpoint 或把 metadata authority 反向注册成 InputShare 绕过。
Go native parser 在 API 进程内使用上述 transport。yt-dlp/universal 不在可直接访问外网的 API 进程
启动：API 只通过 shared UDS 的有界、鉴权 job protocol 把任务交给 stateless、
network-isolated `parser-helper`。helper 无业务 HTTP、公开端口、DB/Redis/运行秘密，只连接 Compose `internal: true` 的
专用 parser sandbox network；同一 network 上只有 `egress-proxy` 能同时连接受控外网 network。
`parser-helper` 的 raw socket 即使绕过代理设置也没有外网路由，只能到 netguard proxy；proxy 对每个目标
执行同一校验。Task 13 对 recovery/candidate 两个可并存 role-set 分别验证 API/helper/proxy 使用该
role 的同一不可变 digest，并验证 command/network/专属 UDS identity；两个 role-set 不得共享
UDS/sandbox/proxy。无已验证 helper/proxy handshake 或隔离拓扑时 production fail closed，
禁用 descriptor 不算 fallback enabled，不能静默回退到 API 内 subprocess。

helper 内 yt-dlp 强制 `--ignore-config --proxy "$NETGUARD_URL"`，并用空的临时 HOME/XDG 隔离用户配置；
Python universal 子进程先清空大小写
`HTTP_PROXY/http_proxy`、`HTTPS_PROXY/https_proxy`、`ALL_PROXY/all_proxy`、
`NO_PROXY/no_proxy`，再把大小写 HTTP(S)_PROXY/ALL_PROXY 设为唯一 netguard
地址，并将 `NO_PROXY/no_proxy` 置空。明确不做 TLS MITM：对 HTTPS CONNECT，proxy 只验证 authority、
DNS、实际 dial、descriptor/job 允许的 host，并限制 tunnel wire bytes、并发与时长；它不能宣称可看到
加密后的 redirect、header 或解压后 body。helper 不接收 Cookie/Authorization/平台凭据，credentialed
platform 禁止走 universal；跨 origin header 只能由受控工具 hook 证明剥离，不能证明则该 descriptor 在
production fail closed。解压资源由 instrumented fetch layer 加 helper container memory/CPU/PID/disk
hard limits 和 UDS output limit 双重约束；第三方路径无法 instrument 时，超限只能隔离杀死 helper job，
不得影响 API 或返回成功。plain HTTP 若允许，proxy 仍执行逐跳 header/body 门禁。
命令构造表格测试覆盖参数遗漏、重复 `--proxy`、环境覆盖和重定向/DNS rebinding。不能强制全部流量经过
该 proxy 的工具在 production 配置加载时禁用，不能宣称只校验首跳 URL 就覆盖 subprocess 网络。

Task 4 的 hermetic sandbox 测试启动 helper/proxy 拓扑模拟器：正常代理请求可达 fake public upstream；
raw socket、显式 IP、替代 DNS、代理变量覆盖和直接 HTTP client 都不可达；UDS payload/response 有大小、
超时、并发、身份和取消门禁。Compose 的真实 `internal: true`/network membership/runtime inspect 由
Task 13 policy 与 CI integration 再证明；二者缺一都不能启用 production fallback。
同一步实现固定 `parser-helper healthcheck` 与 `netguard-proxy healthcheck` 子命令：前者只验证本组 UDS
主进程 handshake/policy fingerprint，后者只验证本容器 loopback listener、policy fingerprint 和 resolver/
dialer self-test fixture；二者不加载/格式化任何平台/API/DB secret、不访问公网、不使用业务 job，不因
另一 role 健康而通过。红测覆盖主进程缺失、错 role/run/digest、cross-role socket、外网探测企图和
sentinel 泄漏；Task 13 只能引用这两个已在 Task 4 全树测试中存在的命令。

同一任务立即关闭尚待 Task 11 删除的旧出口：cluster health/test、跨节点 `/api/download/node` 和任何
worker dispatch 固定返回 disabled/404 且不发网络；runtime pip/git source/tool updater 删除或 production
hard-disable，不进入 subprocess allowlist。微信交换只允许固定官方 HTTPS host、零 redirect、零外部
proxy、严格 body max+1；fallback origin 使用 exact/label-boundary HostRule、手工 redirect 与 streaming
字节/idle limit，禁止 `strings.Contains` host、跨 origin Referer/Origin/header 和无界 `io.Copy`。
`targetForLog`、unsupported error 与所有 URL parse failure 只记录 purpose/stage/host hash，绝不回退输出
原始 URL/path/query 或 child stderr。

ffmpeg 不联网：Task 4 在 Task 9 的安全预取尚未实现前先 hard-disable 所有把远程 URL/manifest 交给
ffmpeg 的 production 路径，并以测试证明 argv/protocol whitelist 只允许本地 `file`。对应 m3u8 merge
暂时返回固定 typed unavailable，不得保留远程 fallback。任务 9 再由 Go 通过 netguard 预取 manifest、
子清单和全部分片并重写成本地清单后恢复功能；始终拒绝
`http/https/tcp/tls/crypto/concat/data`。

- [x] **步骤 4：验证所有网络路径未绕过**

运行：

```bash
go test ./internal/netguard ./internal/parser/... ./internal/policy -run 'TestDial|TestProductionNetworkEgress' -count=1
python3 -m pytest tests/policy/test_python_bridge_security.py -q
GOMAXPROCS=2 go test ./... -count=1
git grep -n -- '--proxy\|HTTP_PROXY\|HTTPS_PROXY\|NO_PROXY' internal/parser
```

预期：测试 PASS；全树编译覆盖 cmd helper/proxy、server/runtimecfg 和 parser；AST/go-types/Python 门禁对
全 production tree 证明没有未迁移出口，grep 只命中统一、
不可覆盖的 egress proxy 构造器与对应测试。以门禁报告的精确 callsite 清单和 `git diff --name-only`
双重核对本任务修改面，不能靠手写的少数 grep 猜测完整性。

**完成证据（2026-07-18）：** `go test ./internal/netguard ./internal/parser/... ./internal/policy -run 'TestDial|TestProductionNetworkEgress|TestRunner|TestPythonBridge|TestSandbox|TestProxy' -count=1`、
`GOMAXPROCS=2 go test ./... -count=1`、`go vet ./...`、`git diff --check` PASS。当前服务器未安装
pytest，未在服务器安装依赖；已用 Python 直接加载 `tests/policy/test_python_bridge_security.py` 并执行
四个 `test_` 函数，均 PASS。`python3 -m pytest tests/policy/test_python_bridge_security.py -q` 的失败原因为
`No module named pytest`，不是 policy 断言失败。

- [x] **步骤 5：Commit**

```bash
# 依据 AST 门禁保存的精确 callsite 清单逐个 stage；下面是当前已知必需路径，出现新出口须追加。
git add -- cmd/parser-helper cmd/netguard-proxy internal/netguard internal/parser/sandbox \
  internal/parser/native/http_client.go internal/parser/universal/bridge.go \
  internal/runtimecfg/settings.go internal/policy/network_egress_test.go \
  bridges/universal/python/bridge.py tests/policy/test_python_bridge_security.py \
  internal/server/client_auth.go internal/server/download_fallback.go internal/server/cluster.go \
  internal/server/cluster_platform_tests.go internal/server/main.go internal/server/ytdlp.go \
  internal/server/universal_parser.go internal/server/m3u8_task.go internal/server/tool_updates.go \
  internal/server/infrastructure.go
# 用保存的 AST 清单逐项对账 staged/worktree；任一发现路径仍 unstaged 即失败。
test -z "$(git diff --name-only -- \
  cmd/parser-helper cmd/netguard-proxy internal/netguard internal/parser/sandbox \
  internal/parser/native/http_client.go internal/parser/universal/bridge.go internal/runtimecfg/settings.go \
  internal/policy/network_egress_test.go internal/server)"
git diff --cached --name-only
git commit -m "feat: guard all outbound network targets"
```

提交前最后一条列表必须与本任务文件表和 AST 门禁发现的精确 callsite 一致；上述路径必须全部 staged，
不得把任何迁移后的 HTTP 构造留到工作树，也不得混入其他任务文件。

## 任务 5：重建迁移、仓储与可降级缓存

**文件：**
- 创建：`migrations/001_core.sql` 至 `migrations/012_task_leases.sql`
- 创建：`internal/store/mysql.go`
- 创建：`internal/store/migrate.go`
- 创建：`internal/store/repositories.go`
- 创建：`internal/store/migrate_test.go`
- 创建：`internal/store/legacy_import.go`
- 创建：`internal/store/legacy_import_test.go`
- 创建：`internal/store/legacy_delta.go`
- 创建：`internal/store/legacy_delta_test.go`
- 创建：`internal/store/gate_receipt.go`
- 创建：`internal/store/gate_receipt_test.go`
- 创建：`tests/integration/store/migration_test.go`（CI/Task 17 integration profile，禁止本地主机隐式启动）
- 创建：`internal/cache/cache.go`
- 创建：`internal/cache/redis.go`
- 创建：`internal/cache/memory.go`
- 创建：`internal/cache/cache_test.go`
- 修改：`cmd/watermark-go/main.go`、`cmd/watermark-go/main_test.go`（显式 `serve`/`data-gate`/
  `healthcheck api`
  子命令；未知或缺少 production command fail closed）
- 修改：`internal/config/config.go`、`internal/config/config_test.go`（`data-gate` 只加载最小 migration
  配置，不读取或接收 API/admin/微信/下载/parser secret）
- 修改：`internal/app/app.go`、`internal/app/app_test.go`（`serve` 在构造/启动任何 component 前验证本次
  gate receipt）
- 修改：`internal/server/service.go`（删除 server 内根据 argv 自动迁移的路径，listener 只由 `serve` 启动）

- [x] **步骤 1：编写迁移幂等和 Redis 降级测试**

```go
func TestMigrationsAreOrderedAndIdempotent(t *testing.T) {
    migrations := mustLoadMigrationGolden(t)
    requireOrderedVersions(t, migrations)
    requireEveryDDLIdempotent(t, migrations)
}

func TestCacheFallsBackWhenRedisUnavailable(t *testing.T) {
    c := NewTiered(failingRedis{}, NewMemory(128))
    require.NoError(t, c.Set(t.Context(), "k", []byte("v"), time.Minute))
    assert.Equal(t, []byte("v"), mustGet(t, c, "k"))
}

func TestDataGateNeverConstructsApplicationOrListener(t *testing.T) {
    result := runCommand(t.Context(), []string{"data-gate"}, commandFakes{
        Gate: successfulGate(),
        NewApplication: func(config.Config) (applicationRunner, error) {
            t.Fatal("data-gate constructed the serving application")
            return nil, nil
        },
    })
    require.NoError(t, result)
}

func TestServeRejectsStaleOrMismatchedGateReceiptBeforeStartingComponents(t *testing.T) {
    receipt := validReceiptFor("recovery", digestA(), "run-current", finalDBIdentity())
    for _, mutate := range receiptMismatchCases() {
        started := atomic.Bool{}
        err := startServeWithReceipt(mutate(receipt), func() { started.Store(true) })
        require.Error(t, err)
        assert.False(t, started.Load())
    }
}

func TestAPIHealthcheckIsLocalSecretFreeAndReceiptBound(t *testing.T) {
    probe := newLocalAPIProbe(validReceiptForCurrentRole())
    require.NoError(t, runHealthcheck(t.Context(), "api", probe))
    assert.Zero(t, probe.ExternalCalls())
    assert.NotContains(t, probe.CapturedOutput(), secretSentinel())
}

func TestRevalidateGateIsReadOnly(t *testing.T) {
    store := mutationFailingStoreWithStableFinalFixture()
    receipt, err := RunDataGate(t.Context(), GateRequest{Mode: GateRevalidate}, store)
    require.NoError(t, err)
    assert.Zero(t, store.DDLCalls()+store.ImportCalls()+store.ScrubCalls())
    require.True(t, receipt.Passed)
}
```

缓存层同时先写以下命名红测：

- `TestCacheKeyBindsPlatformResourceParserAndSchemaVersion` 与 `TestCacheVersionChangeMisses`，证明 key 精确
  绑定稳定 platform、canonical resource digest、parserVersion、resultSchemaVersion，任一版本变化自动 miss；
- `TestCacheSingleflightCoalescesSameKey`，证明相同 key 并发只执行一次 parser，同时不同 key 不互相阻塞；
- `TestNegativeCachePolicyRejectsNonCacheableErrors`，证明 context cancellation、internal、
  `credential_required`、`schema_changed`、security rejection 和短期 session 失效均不写负缓存，只有稳定
  typed failure 写 180 秒负缓存，force refresh 同时绕过正负缓存；
- `TestRedisAndMemoryShareCacheSemantics`，证明 Redis/内存使用同一 key/version/TTL/容量与敏感 query
  摘要语义，格式化输出不含 query/capability/session 原值或可逆编码。

另以脱敏 fixture 验证旧表映射、重复导入幂等、每表行数与稳定业务字段校验和；导入失败必须保持
目标事务未提交，并生成不含行内容的核对 manifest。迁移测试先记录 engine/version/capabilities 和
`chosenMigrationMode`：可靠 ROW binlog fixture 才要求 GTID/binlog position 与幂等 delta；实机
MariaDB 11.8.6 的 `@@log_bin=0`/GTID off fixture 必须选择 `final_full_no_binlog`，position 显式
`notApplicable`，门禁改为 snapshot hash + table-scoped no-writer proof + import/checksum，不能因没有
不存在的位点失败，也不能伪造 delta。另测目标 MySQL 8.4 映射。reverse outbox 只属于经 discovery
验证的 conditional legacy 分支；actual absent A→B 不要求或伪造 reverse。

本地 Task 5 测试必须是 sqlmock/golden/纯 typed importer/cache 单测，禁止 `startTestMySQL`、testcontainers、
docker 或连接宿主 MariaDB。真实 DDL 二次执行、MariaDB 11.8 fixture restore → typed importer → pinned
MySQL 8.4、事务回滚/字符集/默认值/checksum 集成测试放在 `tests/integration/store`，只由 Actions 的 pinned
services/profile 与 Task 17 已 pull 的隔离 migration profile 运行；CI service 未 ready 必须 FAIL，不能 skip。

用运行配置和审计 payload sentinel 测试 allowlist scrub：只保留非秘密单机字段，丢弃
cluster/worker/auto-update；proxy userinfo、Cookie/token/password/secret 及嵌套 musicdl 配置不得进入
新 DB、API、日志、manifest 或 artifact，失败消息只报字段分类而不回显 sentinel。

同一步对 `GateReceipt` 做 table-driven 负测：role（recovery/candidate）、data stage（shadow/final）、
完整 image digest、deploymentRunId/gateAttemptId、schema version、target DB/Redis/outbox identity、input
snapshot hash、config hash、完成时间和 passed 必须全部匹配；旧 run、另一 role/digest/DB、失败/过期/
截断 receipt 均在构造 app、listener、worker 前拒绝。receipt 由 `data-gate` 经 0600 temp、file fsync、
rename、directory fsync 原子写入本组专属 volume，API 只读挂载；不能用 Compose 的
`depends_on` 退出码代替内容绑定。
部署脚本每次 shadow/final/drill/cutover/rollback 都必须先以当前 run/attempt 执行
`docker compose up --force-recreate --no-deps data-gate-${role}`（或经过 tests/ops 证明语义完全相同的
固定封装），等待本次容器退出 0，再核验新 receipt 的容器 start time/run/digest/data identity，随后才
启动该 role 的 API/helper/proxy。上一次已成功退出的 gate 容器或仅满足
`depends_on: service_completed_successfully` 一律视为 stale；负测证明不 force-recreate、旧 receipt、容器
启动时间早于 attempt、以及 API 先行都会非零失败。

- [x] **步骤 2：确认失败**

运行：`go test ./internal/store ./internal/cache -count=1`

预期：FAIL，仓储和缓存包尚不存在。

- [x] **步骤 3：迁移核心表与 lease 字段**

从旧迁移保留结果、尝试、session、设置、样本、平台运行、任务、管理员和审计；
任务表增加 `locked_by/locked_until/next_attempt_at` 与索引。生产不使用本地 JSON 作为权威配置。

同时把入口分成固定 `watermark-go serve`、one-shot `watermark-go data-gate` 与
`watermark-go healthcheck api`。healthcheck 只探测本容器 loopback API 并核对 role/digest/run/gate
identity，不加载/输出 secret、不访问外网。`data-gate` 只加载最小
migration config。`GATE_MODE=apply` 才可顺序执行 schema migration（含二次幂等验证）、typed import、
scrub、count/checksum 和 mode-specific no-writer/position gate；`GATE_MODE=revalidate` 必须使用只读
凭据，只核对已有 schema version、稳定字段 checksum、accepted-write/outbox 坐标、DB/Redis namespace
identity 与 config hash，任何 DDL/import/scrub/write 调用都由类型化接口和 DB privilege 双重拒绝。
任一步失败非零退出且写本 attempt 的 failed receipt；它绝不
构造 HTTP app、监听端口、启动 worker/helper 或加入 egress。`serve` 不再隐式执行迁移；它必须先读取
本 role 专属 receipt，并与外部 runtime inspect 提供的实际 RepoDigest、当前 run/config/data identity
交叉验证，成功后才构造任何 component。production 缺显式 subcommand 或使用未知 subcommand 立即失败。

- [x] **步骤 4：实现源 MariaDB 到目标 MySQL 的兼容导入**

数据源 discovery 必须记录 `engine/version/capabilities`；当前已知实机源为 MariaDB 11.8.6，目标为
MySQL 8.4，不能把 MariaDB GTID 当 MySQL GTID 解析。提供只读源库、写入目标库的显式两阶段 importer：
initial full snapshot 同时记录 MariaDB GTID/binlog 能力/位点、schema/version、每个保留表的行数和稳定
业务字段校验和，由 typed importer 显式映射 MariaDB 类型/默认值/字符集到 MySQL 8.4，并在目标事务中
重复计算；影子期仅在能力实测可靠时幂等追 delta。切流 gate 要求已验证 writer 进入短写栅栏并排空；
若 discovery 证明当前没有 writer，则保存静态无 writer 证明并在切流前重验。MariaDB binlog/GTID
不可靠时不得用 `updated_at` 猜增量，而是在受控无 writer 窗口重做最终一致性全量快照/恢复；此 mode
把 position/delta 标为 `notApplicable`。任何缺失能力或当前 mode 所要求的 snapshot hash、writer
证明、import/checksum 差异都禁止切流；只有可靠 binlog mode 才因缺 position/delta 失败。

备份恢复只能通过 Compose `migration-tools` profile 中、记录于 `deploy/image-lock.json` 的官方 digest
MariaDB recovery image 执行，或使用同样被固定 hash 且经恢复测试证明兼容的转换工具；禁止裸
`docker run`、裸 tag、本地 build 或用 MySQL-only 工具假装成功。隔离恢复后再由 typed importer 写入
MySQL 8.4，不直接把 MariaDB 数据目录挂到 MySQL。

仅当 discovery 验证 `oldServicePresent=true`、旧 writer/route/DSN identity 且选择 conditional legacy
rollback 时，新服务才在首次回滚窗口为可变事实写审计 outbox；回滚前栅栏/排空，把增量幂等反向
重放到唯一指定的隔离旧库克隆。该克隆就是回滚后生产库；完成 checksum 后，必须原子切换旧服务 DSN
到这一确切克隆并验证连接身份，才恢复旧路由。不得在演练库重放后连接另一数据库，也不能拿早期
备份覆盖切流后新写入。actual absent_two_stage 使用 A/B 同一兼容 final DB，legacy reverse 明确
notApplicable。

迁移 manifest 记录 mode、snapshot/position 的 applicable 状态、计数、checksum 与 gate；证据路径固定
`artifacts/migration/legacy-data-rehearsal.json`。`final_full_no_binlog` 必须含 final snapshot/import/
checksum/no-writer proof，delta/reverse 为 mode 合法的 notApplicable，不记录行内容。

`runtime_settings` 只按 allowlist 转换非秘密单机运营字段；cluster/worker/auto-update 丢弃。含 userinfo
的 proxy URL、Cookie/token/password/secret、musicdl 嵌套敏感配置和可能包含整份 settings 的 audit
payload 均 scrub。必要运行秘密只由人工安全导入目标机仓库外 0600 runtime file，不进新 DB、日志、
API 或 artifact。

- [x] **步骤 5：实现 Redis 可降级策略**

Redis 负责 7 天热缓存、180 秒失败缓存、60 秒 URL 锁和限流；Redis 错误记录告警后回退内存，
MySQL 读写不因 Redis 失败回滚。
解析结果 key 必须绑定稳定 platform key、canonical resource ID、parser version 与 result schema version；
同一 key 使用 singleflight，版本变化自动 miss。仅稳定且明确可缓存的失败进入短 TTL 负缓存；context
取消、internal、credential_required、schema_changed 与 security rejection 不得进入普通负缓存。
force refresh 同时绕过正/负缓存，Redis 与内存实现共享相同 key/version 语义和容量上限。

- [x] **步骤 6：验证**

运行：

```bash
GOMAXPROCS=2 go test ./internal/store ./internal/cache ./internal/app ./cmd/watermark-go -count=1
GOMAXPROCS=2 go test ./... -count=1
```

预期：全部 PASS。

**完成证据（2026-07-18）：** 已新增顶层 `migrations/001_core.sql` 至
`migrations/012_task_leases.sql`、`internal/store`、`internal/cache`、`tests/integration/store`。
本地仅运行纯 Go/sqlmock/golden/内存测试，未启动 Docker、testcontainers、宿主 MySQL/MariaDB 或 Redis。
`GOMAXPROCS=2 go test ./internal/store ./internal/cache ./internal/config ./internal/app ./cmd/watermark-go -count=1`
与 `GOMAXPROCS=2 go test ./... -count=1` PASS；integration profile 由 `//go:build integration`
隔离，未配置 `STORE_INTEGRATION_READY=true` 时会 fail 而不是 skip。

Actions 另运行（本地主机 Task 5 不运行）：

```bash
go test -tags=integration ./tests/integration/store -count=1
```

使用 workflow 已固定 digest 的 MariaDB 11.8/MySQL 8.4 services，未执行或 skip 均使 image job 失败。

- [x] **步骤 7：Commit**

```bash
git add migrations internal/store internal/cache tests/integration/store \
  cmd/watermark-go/main.go cmd/watermark-go/main_test.go internal/config internal/app \
  internal/server/service.go
git commit -m "refactor: add durable stores and degradable cache"
```

## 任务 6：实现客户端 session 与兼容鉴权

**文件：**
- 创建：`internal/auth/client.go`
- 创建：`internal/auth/token.go`
- 创建：`internal/auth/client_test.go`
- 创建：`internal/httpapi/client_handlers.go`
- 创建：`internal/httpapi/client_handlers_test.go`

- [x] **步骤 1：编写当前前端契约测试**

测试空微信 code 的开发身份、稳定 UID、token 过期、`token` header、Bearer 兼容，以及
无效 token 用 HTTP 200 返回 `code=1008`。另外覆盖安全熵失败时 identity/session 均零写、微信
transport/status/body/JSON/业务拒绝错误只能产生固定脱敏响应与固定分类日志，以及上游错误中即使
含完整 query URL、登录 code 和应用密钥也不得出现在响应/日志。微信身份 metadata 只允许保存
`programType`、openid 绑定所需字段和 unionid，明确断言不含 `session_key`、其 camelCase 变体或值。
增加 `TestClientSessionSecretsNeverReachParserDependencies`：微信 `session_key`、client token、openid、
登录 code 与应用密钥不能进入 parser `Dependencies`、Fetcher header、普通 parse cache 或 parser upstream
session material；客户端 session 与 Task 3 的平台短期材料是两个互不转换、互不共享 key-space 的边界。

已覆盖：`internal/auth/client_test.go` 验证空 code + `clientId` 开发身份、稳定 `uid`、TTL 过期、
`token`/Bearer 兼容、熵失败零写、微信 transport/status/body/JSON/business 脱敏分类、identity metadata
禁止 `session_key`；`internal/httpapi/client_handlers_test.go` 验证无效 token 的 HTTP 200/`1008` 前端刷新契约。

```go
func TestInvalidTokenUsesFrontendRefreshContract(t *testing.T) {
    res := postJSON(t, router, "/api/parse", `{"url":"https://example.com/v"}`, header("token", "bad"))
    assert.Equal(t, http.StatusOK, res.Code)
    assert.JSONEq(t, `{"code":1008,"msg":"登录状态已失效，请重试"}`, res.Body.String())
}
```

- [x] **步骤 2：确认失败**

运行：`go test ./internal/auth ./internal/httpapi -run 'TestClient|TestInvalidToken' -count=1`

预期：FAIL。

证据：实现前运行该命令，结果为 `FAIL ./internal/auth [setup failed]` 与
`FAIL ./internal/httpapi [setup failed]`（新包尚无 Go 实现文件），确认红测阶段成立。

- [x] **步骤 3：实现 session**

token 使用 256 位安全随机值的 SHA-256 摘要落库，响应只返回明文一次；默认 TTL 30 天。随机值必须
在任何 identity/session 写入前成功生成，熵源失败返回固定 `code=1008` 且两类状态均为零写，禁止
伪随机 fallback。`uid` 使用 `30000000 + userID` 的稳定十进制格式。正式微信配置存在时绑定
openid，测试/开发允许空 code 的 clientId 身份。微信上游 transport/status/read/JSON/业务错误在
边界内归类，handler 只返回固定通用错误，日志不格式化原始 error/request URL/code；上游
`session_key` 仅用于本次交换，既不进入 identity metadata，也不落库或写日志。

已实现：`internal/auth` 提供注入式 `Store`、熵源、clock、WeChat exchanger 与固定分类日志；token
由 32 字节安全随机值生成，落库/内存仅保存 SHA-256 摘要；`AuthenticatedClient.ParserContext()` 只暴露
`userId/uid/publicId/programType`，不携带 token、openid、login code、微信应用密钥或 `session_key`。
`internal/httpapi` 提供客户端 session 与 parse 鉴权 handler，失效 token 统一返回
`{"code":1008,"msg":"登录状态已失效，请重试"}`。

- [x] **步骤 4：验证**

运行：`go test ./internal/auth ./internal/httpapi -run 'TestClient|TestInvalidToken' -count=1`

预期：全部 PASS。

证据：

```bash
go test ./internal/auth ./internal/httpapi -run 'TestClient|TestInvalidToken' -count=1
go test ./internal/auth ./internal/httpapi -count=1
GOMAXPROCS=2 go test ./... -count=1
go vet ./...
git diff --check
```

结果：全部通过；期间未运行 Docker/Buildx/镜像构建命令。

- [x] **步骤 5：Commit**

```bash
git add internal/auth internal/httpapi/client_handlers.go internal/httpapi/client_handlers_test.go
git commit -m "feat: implement mini program session authentication"
```

已提交：`feat: implement mini program session authentication`。

## 任务 7：实现同步解析用例与响应兼容层

**文件：**
- 创建：`internal/parse/model.go`
- 创建：`internal/parse/service.go`
- 创建：`internal/parse/url.go`
- 创建：`internal/parse/url_test.go`
- 创建：`internal/parse/normalize.go`
- 创建：`internal/parse/service_test.go`
- 创建：`internal/httpapi/parse_handlers.go`
- 创建：`internal/httpapi/parse_contract_test.go`

- [ ] **步骤 1：编写视频、图集、音频、m3u8 和错误契约测试**

```go
func TestNormalizeVideoProvidesAllFrontendAliases(t *testing.T) {
    got := Normalize(Result{Type:"video", VideoURL:"https://cdn.example/v.mp4", AudioURL:"https://cdn.example/a.mp3"})
    assert.Equal(t, got.Music, got.MP3)
    assert.Equal(t, got.Music, got.AudioURL)
    require.NotEmpty(t, got.Downloads)
}

func TestForceRefreshBypassesPositiveAndNegativeCache(t *testing.T) {
    p := &countingParser{result: Result{Type:"video", VideoURL:"https://cdn.example/v.mp4"}}
    cache := newFakeCache()
    cache.positive["https://example.com/v"] = Result{Title:"stale"}
    cache.negative["https://example.com/v"] = ErrUpstream
    svc := NewService(Dependencies{Parser:p, Cache:cache, Store:newFakeStore()})
    got, err := svc.Parse(t.Context(), Request{URL:"https://example.com/v", ForceRefresh:true})
    require.NoError(t, err)
    assert.Equal(t, int32(1), p.calls.Load())
    assert.Equal(t, "https://cdn.example/v.mp4", got.VideoURL)
}

func TestCanonicalURLKeepsOnlyDescriptorQueryKeys(t *testing.T) {
    got := canonicalize("https://www.example/video/1?vid=42&utm_source=x&ticket=secret",
        Descriptor{QueryKeys: []string{"vid"}})
    assert.Equal(t, "https://www.example/video/1?vid=42", got.URL)
    assert.NotContains(t, got.LogFields, "42")
}

func TestCanonicalURLQueryPolicyMatrix(t *testing.T) {
    // 每个平台的 catalog QueryKeys 是唯一 authority；覆盖 vid/id/xsec_token/modal_id/v/s/pid、
    // 重复 key、大小写、空值、百分号编码、稳定排序和 tracking 剥离。
    requireQueryPolicyGolden(t, mustLoadDescriptorCatalog())
}

func TestNormalizeLivePhotoKeepsLegacyImagesShape(t *testing.T) {
    got := Normalize(Result{Images: []ImageAsset{{URL:"https://cdn.example/a.jpg",
        LivePhotoURL:"https://cdn.example/a.mp4"}}})
    assert.Equal(t, []string{"https://cdn.example/a.jpg"}, got.Images)
    assert.Equal(t, "https://cdn.example/a.mp4", got.ImageAssets[0].LivePhotoURL)
}
```

- [ ] **步骤 2：确认失败**

运行：

```bash
go test ./internal/parse ./internal/httpapi \
  -run 'TestNormalize|TestForceRefresh|TestCanonicalURL|TestCacheKey|TestNegativeCache|TestParseContract' -count=1
```

预期：FAIL。

- [ ] **步骤 3：实现解析数据流**

实现 URL 提取、缓存、URL 锁、平台识别、native→yt-dlp/universal 有界 fallback、媒体校验、
MySQL 保存和 Redis 回填。新 `shareId` 必须由 crypto-random 生成、熵至少 128 位、带 cache 用途与 TTL，
其本身作为 bearer capability，禁止从 URL/内容 hash 推导。迁移进来的 legacy shareId 只允许限流的
只读恢复，不再生成 legacy ID，也不得提供枚举接口；随机熵失败时不得持久化结果或返回成功。

URL 提取/规范化采用 descriptor 驱动的纯函数：只接受 HTTP(S)，host/path 规范化后只保留该平台显式
query allowlist；query 中的 capability/会话材料不得进入日志、错误、cache key 或 evidence，锁/cache
需要身份时使用用途域分离的不可逆摘要。canonicalizer 不联网；首次网络请求之前以及每次 DNS、实际
dial 和每跳 redirect 都必须经过任务 4 netguard，不能复用研究项目“先请求、后检查域名”的顺序。

内部 `ImageAsset`/`MediaCandidate` 全部先经协议、SSRF、数量、大小/类型门禁。兼容投影继续原样输出
当前前端使用的 `images` 字符串数组，并仅以可选加法字段 `imageAssets` 表达静态图与 Live Photo 配对；
`video/music/downloads` aliases 不变。没有 Live Photo 的结果必须保持旧响应形状，含配对资源时索引稳定。

编排层不得使用 `safe_execute` 式静默吞错。内部 typed error 至少区分 `invalid_input`、`unsupported`、
`credential_required`、`upstream_timeout`、`upstream_blocked`、`empty_media`、`schema_changed` 和
`internal`，并携带 stage/platform/retryable；外部只映射当前前端固定脱敏 `code/msg/data/requestId`。
同一 canonical resource 使用 singleflight；cache key 含 parser/result schema version，取消、内部错误、
凭据缺失、安全拒绝或 schema_changed 不写普通负缓存，force refresh 绕过正/负缓存。

- [ ] **步骤 4：注册同步和兼容接口**

保留 `/api/parse`、`/api/parse/cache/:id`、`/api/hybrid/video_data`、legacy 和 v1 接口。
新 envelope 使用 `msg` 而不是顶层 `message`，避免被当前前端误判为 Layzz envelope。

- [ ] **步骤 5：验证**

运行：`GOMAXPROCS=2 go test ./internal/parser/... ./internal/parse ./internal/httpapi -count=1`

预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/parse internal/httpapi/parse_handlers.go internal/httpapi/parse_contract_test.go
git commit -m "feat: implement compatible synchronous parse service"
```

## 任务 8：实现持久异步解析任务

**文件：**
- 创建：`internal/task/model.go`
- 创建：`internal/task/store.go`
- 创建：`internal/task/worker.go`
- 创建：`internal/task/worker_test.go`
- 创建：`internal/parse/tasks.go`
- 创建：`internal/httpapi/parse_task_handlers.go`
- 创建：`internal/httpapi/parse_task_contract_test.go`

- [ ] **步骤 1：编写状态、幂等、续租和重启恢复测试**

```go
func TestParseTaskFrontendLifecycle(t *testing.T) {
    id := submit(t, "/api/parse/task")
    assertStates(t, id, "pending", "running", "completed")
    require.NotNil(t, poll(t, id).Result)
}

func TestWorkerRenewsLeaseAndDoesNotExecuteTwice(t *testing.T) {
    clock, store, executor := newTaskHarness(t)
    id := store.InsertPending(t.Context(), "parse", []byte(`{"url":"https://example.com/v"}`))
    startTwoWorkers(t, clock, store, executor)
    clock.Advance(45 * time.Second)
    require.Eventually(t, func() bool { return store.Status(id) == Completed }, time.Second, time.Millisecond)
    assert.Equal(t, 1, executor.Calls(id))
}

func TestExpiredRunningTaskReturnsToPendingAfterRestart(t *testing.T) {
    clock, store, executor := newTaskHarness(t)
    id := store.InsertRunningWithLease(t.Context(), "parse", clock.Now().Add(-time.Second))
    worker := NewWorker(store, executor, WithClock(clock))
    require.NoError(t, worker.RecoverExpired(t.Context()))
    assert.Equal(t, Pending, store.Status(id))
}
```

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/task ./internal/httpapi -run 'TestParseTask|TestWorker|TestExpired' -count=1`

预期：FAIL。

- [ ] **步骤 3：实现 MySQL worker**

单机 worker 用 `SELECT ... FOR UPDATE SKIP LOCKED` 领取任务，15 秒 lease、5 秒续租、最多 2 次，
指数退避 2 秒；同 request ID + client ID 保持幂等。启动时只回收过期 running 任务。
worker 同时实施全局与按平台 bulkhead，context 从 HTTP/lease 贯穿 parser 和 subprocess；取消、lease
丢失或超时必须停止新子任务、杀死允许的进程组并清理 `.part`，不能泄漏 goroutine/进程/临时文件。

- [ ] **步骤 4：实现前端任务协议**

提交 8 秒内返回 `taskId/status/progress/pollUrl/requestId`；parse task ID 由 crypto-random 生成且熵至少
128 位，其本身是带 parse-poll 用途/TTL 的 bearer capability，公开轮询不要求 token；熵失败零写。
完成结果嵌入 `result`。写入与新响应的规范初始状态为 `pending`；读取旧数据时可兼容 `queued`，但不得把 `queued` 作为新任务的权威状态。

- [ ] **步骤 5：验证**

运行：`GOMAXPROCS=2 go test ./internal/task ./internal/parse ./internal/httpapi -count=1`

预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/task internal/parse/tasks.go internal/httpapi/parse_task_handlers.go internal/httpapi/parse_task_contract_test.go
git commit -m "feat: add durable asynchronous parse tasks"
```

## 任务 9：实现受限下载兜底和 m3u8 任务

**文件：**
- 创建：`internal/download/service.go`
- 创建：`internal/download/ticket.go`
- 创建：`internal/download/service_test.go`
- 创建：`internal/media/m3u8.go`
- 创建：`internal/media/m3u8_test.go`
- 创建：`internal/media/dash.go`
- 创建：`internal/media/dash_test.go`
- 创建：`internal/httpapi/download_handlers.go`
- 创建：`internal/httpapi/download_contract_test.go`
- 修改：`internal/policy/network_egress_test.go`（把本地-only ffmpeg 固定 argv builder 加入精确符号清单，
  并拒绝任何 remote protocol/动态 executable；不得按目录豁免 `internal/media`）

- [ ] **步骤 1：编写大小、并发、Range、TTL、SSRF 和前端状态测试**

视频/音频/图片上限分别为 300/50/20 MiB，全局并发 2、单客户端并发 1；
只接受任务创建 `attempt>=4`；完成下载必须对完整 GET 返回 200、对合法 Range 返回 206。
另加表格测试覆盖下载 secret 缺失、签名函数拒绝空 secret、空 ticket/空 task ID、过期 ticket 和用途不匹配；
任何 URL builder 返回错误时 handler 必须返回非零业务码，禁止 `code=0` 携带空 `downloadUrl/pollUrl`。
慢速下载流测试让响应持续超过全局 40 秒 WriteTimeout，证明专用 streaming idle/deadline 只在无进展时
终止，不会截断持续有界进展的合法流。m3u8 恶意表格覆盖过深子清单、过多分片、累计字节超限、
绝对/`..` 路径以及 `file/concat/data/crypto/http` 等重写后协议注入。
另测 DASH 音视频合并只能作为持久异步媒体任务执行；任务内下载子任务最多并行 2，context 取消/超时、
worker lease 丢失或 ffmpeg 失败都必须杀死进程组、清理部分文件且不得返回成功。
`TestDASHCandidateOrderAndFallbackBudget` 必须证明 DASH 消费 Task 3 的稳定 comparator，不把数组下标当质量；
首候选失败后仅在统一总预算内切换，每个 fallback 候选仍逐项经过 netguard、协议、大小、媒体类型和
重复 URL 门禁，任何单候选错误都不能形成无限重试。文件系统负测还覆盖受控临时根权限 `0700`、
`.part`/最终临时媒体权限 `0600`、symlink/path traversal 拒绝，以及成功、失败、取消、lease 丢失后
均无越界或遗留部分文件。
Task 4 的全树 network/subprocess self-audit 必须先因新增 ffmpeg sink 红灯；只对
`internal/media/m3u8.go` 中经过类型检查的精确本地 argv-builder/runner 符号做最小 allowlist。恶意 fixture
证明同目录新 `exec.Command*`、动态 executable、remote URL 或扩大 protocol whitelist 仍被拒绝。

另加 Bilibili 回归门禁：未配对的 DASH video/audio 轨不得投影成同步 `VideoURL`，多条 `durl` 是顺序
分片而不是 fallback；只有 Task 9 的持久媒体任务取得并验证明确 video+audio pair 后，才允许以并发最多
2 的 Go 预取和本地 file-only ffmpeg 合并。

同一行为测试锁定 canonical route-auth inventory：fallback create 可保持匿名兼容，但必须同时满足
attempt/limit/SSRF（`attempt>=4`、限流/并发/大小门禁与 netguard）。cache shareId 与 parse poll 使用
用途/TTL 绑定的 crypto-random >=128-bit ID 本身作为 bearer capability；fallback poll/download 使用
服务端返回的有用途/TTL 签名 ticket URL。m3u8 create 与 task poll 按固定前端实际无 token 兼容：前端
固定用随机 >=128-bit task ID 请求 `/api/task/:id`，不能强制其未发送的 poll query ticket；最终 file URL
必须签名并绑定用途/TTL。任一 ID/ticket 缺失、篡改、跨用途或过期都失败，不能把“无 token”实现成
可枚举资源。前端实际发送认证的入口同时兼容 `token` header 与
`Authorization: Bearer`（token/Bearer），不得要求前端未发送的 header。

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/download ./internal/media ./internal/httpapi -run 'TestDownload|TestM3U8|TestDASH' -count=1`

预期：FAIL。

- [ ] **步骤 3：实现下载任务与签名 ticket**

ticket 使用独立 `DOWNLOAD_TOKEN_SECRET` 的 HMAC-SHA256，不能回退到管理员 session secret，绑定非空
task ID、过期时间和用途；文件名由服务生成，禁止路径穿越。
所有临时根由服务创建为 `0700`，临时与最终任务文件固定 `0600` 且拒绝 symlink；文件先写 `.part`，
校验长度和媒体类型后 fsync + 原子重命名，TTL 清理不删除运行中任务，失败/取消/lease 丢失必须清理。

- [ ] **步骤 4：实现 m3u8 安全合并**

Go 通过 netguard 预取并验证 manifest、有限层级子清单和全部分片，限制层级、分片数、单片与累计
字节；拒绝加密、私网目标、绝对路径、`..` 和 `file/concat/data/crypto`。每个资源写入受控临时根内由
服务生成的文件名，重写后的本地清单只引用这些文件。ffmpeg 仅启用本地 `file` protocol whitelist，
不得解析任何远程 URL；120 秒超时后杀死进程组并删除部分文件。
本步骤在上述预取/本地化门禁通过后恢复 Task 4 暂时 hard-disable 的 m3u8 merge，并将其注册为 media
capability/route，而不是伪装成 native platform descriptor。

DASH 同样有明确 owner：`internal/media/dash.go` 只接受规范化阶段已验证的一组 video/audio
`MediaCandidate`，由 Go netguard 分别以有界并发 2、独立/累计字节和 idle/total deadline 预取到受控
临时根，完整 hash/media-type 校验成功后才调用 ffmpeg。ffmpeg argv 只含服务生成的本地 file path，
固定 executable、`-protocol_whitelist file`，不接收 manifest/
remote URL/shell；lease/context 取消杀整个进程组并清理 `.part`。DASH 只能走持久异步任务，不能阻塞
同步 parse handler。对应 `TestDASH*` 和 network policy 精确 symbol 与 m3u8 同 commit，禁止留下无 owner
的验收声明。

- [ ] **步骤 5：注册兼容接口并验证**

运行：

```bash
GOMAXPROCS=2 go test ./internal/download ./internal/media ./internal/httpapi ./internal/policy -count=1
GOMAXPROCS=2 go test ./... -count=1
```

预期：全部 PASS；任务成功响应兼容 `status:"done", url:"..."` 和 fallback `completed/downloadUrl`。

- [ ] **步骤 6：Commit**

```bash
git add internal/download internal/media internal/httpapi/download_handlers.go \
  internal/httpapi/download_contract_test.go internal/policy/network_egress_test.go
git commit -m "feat: add bounded download and m3u8 tasks"
```

## 任务 10：重构后台、平台样本和基准运行

**文件：**
- 创建：`internal/admin/auth.go`
- 创建：`internal/admin/service.go`
- 创建：`internal/admin/baseline.go`
- 创建：`internal/admin/baseline_test.go`
- 创建：`internal/admin/service_test.go`
- 创建：`internal/httpapi/admin_handlers.go`
- 创建：`internal/httpapi/admin_contract_test.go`
- 创建：`tests/baseline/fixtures/platform-samples.json`
- 可选创建：`tests/research/media-parser/manifest.json` 与其独立、脱敏候选 fixture（仅当本任务实际取得合法
  样本；否则明确记录 `coverage clue not adopted`，不得生成空壳证据）
- 读取、验证并更新：`docs/baseline-provenance.json`（仅在 Task 10 生成 fixture 并独立审查后写入 trust anchor）

- [ ] **步骤 1：编写后台认证、RBAC 和批次口径测试**

测试未登录 401、viewer 不能写、owner 危险操作需要确认 header、审计落库。认证矩阵锁定：MySQL
配置后查询错误/用户不存在都 fail closed；environment 认证只允许无 MySQL 的 development/test；
breakglass 必须显式开关和强密码。cookie HMAC payload 签名 `mysql|environment|breakglass` auth mode，
旧/未知格式失效，MySQL cookie 不得提升为同名 env 用户，breakglass 关闭后即使有同名 DB 用户也失效。
浏览器管理写接口另测 CSRF token/Origin，cookie 必须 Secure（HTTPS）、HttpOnly、SameSite。

批次固定并发 3、绕过正负缓存，成功要求至少一个媒体 URL。测试预置 Redis 成功缓存、失败缓存和
MySQL 历史结果，仍断言 93 个 enabled 样本每项本轮实际调用 parser；连续三轮使用独立 run ID，
各自 `completed=93` 且独立墙钟不超过 216 秒，不能复用 fixture 自报 SHA 或历史结果伪装执行。

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/admin ./internal/httpapi -run 'TestAdmin|TestBaseline' -count=1`

预期：FAIL。

- [ ] **步骤 3：迁移单机后台能力**

保留摘要、解析、结果库、请求、日志、诊断、设置、工具状态、样本和平台运行；不注册集群节点、
内部平台 worker 和跨节点下载接口。把已前置修复的 MySQL fail-closed、显式 breakglass、受签名 auth
mode/禁止模式升级、旧 cookie 失效测试迁入 `internal/admin/auth_test.go`，不得在拆包时回退。
所有管理写 handler 统一经过 RBAC + CSRF/Origin + audit middleware。`/api/profile` 保持明确
`1002 unsupported`。

- [ ] **步骤 4：导出版本化样本 manifest**

先精确验证 `docs/baseline-provenance.json`：批准源 commit/tree、`测试结果基准.md` SHA-256
`a470a87e64242e5e97ee1a03571c43198a6bd7036c0b756e8c69fd9b639df29a`，以及 5 个 catalog 输入按
“文件名 + NUL + source-commit 文件内容”顺序组合的 SHA-256
`05d832a7d59897d16cd4bd26a7d02d6f6bdf5ec6829c1a280e974579fa29bf6a`。随后从这些固定输入导出 fixture：
96 行 `platformKey/sampleURL/enabled/expectedPlatform/expectedType`，按 platformKey 排序；3 个无 URL
平台 disabled，93 个 enabled。首次导出后用独立 `sha256sum` 得到 canonical fixture hash，把该字面量
的输入严格定义为导出文件的完整 bytes，不做 JSON 重排、字段剔除或规范化。canonical hash 是独立
trust anchor，禁止写入 fixture 自身造成自引用。fixture 当前尚未生成，因此本计划阶段不得伪造 hash；
任务 10 生成并独立审查后，才在 `docs/baseline-provenance.json` 增加
`canonicalFixturePath=tests/baseline/fixtures/platform-samples.json` 与真实
`canonicalFixtureSha256`。`internal/admin/baseline_test.go` 与 `tests/baseline/test_report.py` 使用同一
经审查值的 Go/Python 固定字面量，并分别从文件完整 bytes 复算；provenance、Go、Python 三者必须
一致。fixture/report 自报 hash 只用于交叉核对，不能成为信任来源或绕过独立常量。任何源 provenance
或 canonical hash 变化必须独立审查提交。

`docs/research/media-parser-provenance.json` 与其 50-domain 目录不得参与上述 trust anchor 计算，也不得
替换、启用或删除任何 canonical 样本。若为评估新平台创建 `tests/research/media-parser/`，只能使用独立
合法取得且脱敏的合成/候选 fixture；`manifest.json` 必须记录独立合法来源、license/consent、脱敏状态、
hash 与 `productionEnabled=false`，并证明该路径/内容不参与 baseline fixture/hash。若没有这种候选，
Task 10 evidence 必须写 `coverage clue not adopted`，不能把上游 50-domain 线索计作已支持平台。只有单独变更同时通过 descriptor
唯一性、URL/netguard、资源门禁、当前 API 契约和稳定性测试后，候选才能进入 production registry。

本次固定 tree 的 exact 目录差异还要作为显式候选而非口头建议记录：TikTok/YouTube/知乎共 8 条当前
未批准平台 host，以及 `c.kuaishou.com`、`m.kuaishou.com`、`m.chenzhongtech.com`、
`v.m.chenzhongtech.com`、`izuiyou.com`、`kg2.qq.com`、`m.acfun.cn`、`m.huya.com`、`pipix.com`、
`quanmin.hao224.com` 共 10 条现有平台 alias，全部默认 `productionEnabled=false`。同一步评估把当前
broad HostRule 改成完整 exact alias 集合的可行性，特别把 `m.weibo.cn`（微博）与
`m.oasis.weibo.cn`（绿洲）的语义冲突作为必测负例；任何收紧都必须证明不减少固定 41-domain/93 样本
兼容覆盖后再单独更新 golden。

- [ ] **步骤 5：验证**

运行：

```bash
GOMAXPROCS=2 go test ./internal/admin ./internal/httpapi -count=1
GOMAXPROCS=2 go test ./internal/parser/... ./internal/policy -count=1
```

预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/admin internal/httpapi/admin_handlers.go internal/httpapi/admin_contract_test.go \
  tests/baseline docs/baseline-provenance.json
# 仅当实际生成并审查候选时，精确加入 tests/research/media-parser/manifest.json 与 manifest 列出的文件；
# 否则不得创建或暂存该目录。
git commit -m "refactor: add single-node admin and baseline runner"
```

## 任务 11：完成 HTTP 组合、遥测和服务器保护

**文件：**
- 创建：`internal/httpapi/router.go`
- 创建：`internal/httpapi/response.go`
- 创建：`internal/httpapi/middleware.go`
- 创建：`internal/httpapi/router_test.go`
- 创建：`internal/observability/log.go`
- 创建：`internal/observability/client_performance.go`
- 创建：`internal/observability/client_performance_test.go`
- 修改：`internal/app/app.go`
- 修改：`README.md`
- 修改：`tests/README.md`
- 删除：`internal/runtimecfg/`
- 删除：`internal/server/`
- 删除：`docs/集群化部署方案.md`
- 移动：`docs/多节点解析性能排查与优化.md` → `docs/archive/多节点解析性能排查与优化.md`
- 修改：`internal/admin/web/templates/index.html`（删除节点/cluster UI）
- 修改：`internal/policy/network_egress_test.go` 及所有路径绑定的 parser/network policy（删除
  `internal/server`/`internal/runtimecfg` 前先让 stale allowlist 自审计失败，再迁移到新精确 owner）

- [ ] **步骤 1：编写 route inventory、request ID、CORS 和超时测试**

```go
func TestRequiredFrontendRoutesRegistered(t *testing.T) {
    requireRoutes(t, Router(), []string{
        "POST /api/client/session", "POST /api/parse", "POST /api/parse/task",
        "GET /api/parse/task/:id", "GET /api/parse/cache/:id",
        "POST /api/download/fallback", "GET /api/download/fallback/:id",
        "GET /api/m3u8/merge", "GET /api/task/:id",
    })
}
```

同一 inventory 明确断言跨节点 `/api/download/node` 与内部 worker 路由不存在（404），health payload
不含 node/cluster 字段。增加慢速下载专用响应测试：持续有进展超过 40 秒仍完成，无进展超过 streaming
idle deadline 才中止，证明全局 WriteTimeout 不会截断下载。

route inventory 还必须把鉴权逐路由写成行为表而非全局猜测：fallback create 匿名但受
attempt/limit/SSRF；cache/parse poll 使用用途/TTL 绑定的 random >=128-bit ID bearer；fallback poll/download
使用有用途/TTL 的签名 ticket URL；m3u8 create/task poll 保持固定前端实际无 token，task poll 以随机
>=128-bit ID bearer 兼容前端固定 `/api/task/:id`，file download 才要求签名 URL。会发送认证的
session/parse/fallback 入口覆盖 token/Bearer 两种形态。表格测试逐项覆盖匿名成功、猜测 ID、
缺/坏/过期/错用途 ID 或 ticket、
限流和 SSRF 拒绝。

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/httpapi ./internal/observability -count=1`

预期：FAIL。

- [ ] **步骤 3：实现中间件和 HTTP Server**

接受/生成 32 位十六进制 request ID 并回显；CORS 允许并暴露前端所需 headers；
Gin 不信任任意代理。Server 设置 10 秒 ReadHeader、20 秒 Read、40 秒 Write、60 秒 Idle，
下载流和任务轮询使用专用响应路径；streaming handler 自己施加 idle/progress deadline、总字节和总时长
上限，不继承会在 40 秒截断合法慢流的普通响应 WriteTimeout。

- [ ] **步骤 4：实现非阻塞性能遥测**

`POST /api/client/performance` 允许匿名，限制 body 16 KiB，写入有界 channel；channel 满时丢弃并计数，
5 秒前端超时内立即返回。
服务日志以 JSON stdout 为主，只允许稳定低基数字段：request/task ID、稳定 platform key、parser、stage、
attempt、cache status、fallback、typed error kind 和 duration；完整 URL/path/query、redirect location、
Cookie/token、上游 body/error 不得写入日志。指标按相同低基数维度覆盖 success/timeout/blocked/fallback，
不得把 URL、用户/任务 capability 或任意 error string 作为 label。捕获日志测试逐类注入 sentinel 并断言
响应、日志和指标均不泄漏。

- [ ] **步骤 5：删除旧聚合包、运行时集群配置和界面**

只有步骤 1 新增的 route inventory、request ID、CORS、timeout 和 performance 测试已经提交到工作树，
并且新 `internal/httpapi`/`internal/observability` 实现使这些测试转绿后，才允许删除旧包。不得先删
`internal/server`/`internal/runtimecfg` 再用“旧代码不存在”代替行为对照；删除前后都要运行同一组新增测试。

确认新入口不再依赖 `internal/server` 或 `internal/runtimecfg` 后删除两个包；从后台模板删除节点、cluster 状态和跨节点操作 UI，删除 `docs/集群化部署方案.md`。`docs/多节点解析性能排查与优化.md` 仅作为历史基准背景移入 `docs/archive/`，文件头明确“不可作为生产架构或部署指令”。同步更新根 README、`tests/README.md` 和其余普通文档，使其只描述单机新结构。

旧根 Compose、旧 Nginx 入口、worker Compose、Jenkinsfile 和可变上游同步脚本已在任务 2 前的安全纠偏中删除；本步骤只用 `test ! -e` 条件验证它们仍不存在，不执行会因文件已经缺失而失败的 `git rm`。

- [ ] **步骤 6：验证**

运行：

```bash
GOMAXPROCS=2 go test ./internal/httpapi ./internal/observability ./internal/app -count=1
GOMAXPROCS=2 go test ./internal/policy -count=1
GOMAXPROCS=2 go test ./... -count=1
if git grep -niE 'cluster|jenkins|集群|worker endpoint' -- \
  cmd internal deploy scripts README.md tests/README.md docs \
  ':!internal/policy/**' ':!docs/archive/**' ':!docs/superpowers/**' ':!docs/requirements-traceability.md'; then exit 1; fi
test ! -e docker-compose.yml
test ! -e docker-compose.prod.yml
test ! -e deploy/nginx/watermark-backend.conf
test ! -e scripts/sync-universal-parser.sh
test ! -e scripts/sync-universal-parser.ps1
```

预期：测试 PASS；生产代码、模板、部署、脚本、README 与普通文档中大小写不敏感扫描无禁止架构/流水线命中。`internal/policy/` 必须为门禁规则命名禁止项，`docs/archive/` 和三份治理文档（设计、计划、追踪矩阵）用于记录历史决策；它们构成显式白名单，不得被生产入口引用。

- [ ] **步骤 7：Commit**

```bash
git add internal/httpapi internal/observability internal/app internal/policy
git rm -r internal/server internal/runtimecfg docs/集群化部署方案.md
git add internal/admin/web/templates/index.html docs/archive README.md tests/README.md
git commit -m "refactor: compose single-node HTTP application"
```

## 任务 12：建立前端行为契约和服务 E2E

**文件：**
- 创建：`tests/contracts/frontend_contract_test.go`
- 修改：`tests/e2e/conftest.py`
- 修改：`tests/e2e/test_health_and_auth.py`
- 修改：`tests/e2e/test_public_api_contracts.py`
- 修改：`tests/e2e/test_admin_api_contracts.py`
- 修改：`tests/e2e/test_admin_and_fallback.py`
- 创建：`tests/e2e/test_frontend_flow.py`
- 创建：`scripts/verify-frontend-provenance.sh`
- 读取并验证：`docs/frontend-provenance.json`
- 修改：`tests/README.md`

`tests/e2e/conftest.py` 已随批准基线存在；本任务扩展其 fixture 和隔离逻辑，不得把它描述成新建文件
或覆盖掉已有安全默认值。

- [ ] **步骤 1：编写前端 envelope 和任务状态契约**

覆盖 session、`1008/1009` HTTP 200、同步/异步解析、公开 poll、分享缓存、performance 降级、
fallback 双响应形态、m3u8 done/error、绝对同域 HTTPS downloadUrl。
同时覆盖研究融合后的富媒体兼容：普通图集的现有 `images` 字符串数组与响应 envelope 必须完全保持，
`imageAssets` 仅为可选加法字段；Live Photo 的静态/动态 URL 配对索引稳定且两者都通过 media/netguard
校验，audio 仍投影到现有 `music/mp3/audioUrl` aliases。未知客户端忽略 `imageAssets` 时视频/图集流程
不得改变。
`TestMediaParserIntegrationContract` 还把 registry golden、structured JSON golden、query policy、
candidate ranking/有界 fallback、cache/version/negative semantics、rich-media 兼容投影与 unsafe-pattern
policy 聚合为一个 hermetic contract result；其 machine section 名固定为 `mediaParserIntegration`，供
Actions 与最终 verifier 绑定同一 source commit/image digest，而不是靠人工说明宣称研究融合完成。
负向契约明确拒绝把研究项目的 `POST {text}`、`retcode/retdesc/succ` 或成功业务码 `200` 注册成公开
协议；权威仍是固定前端的 `url/source/timestamp/version` 输入和 `code=0/msg/data/requestId` envelope。
修改现有 E2E 断言：`/api/download/node` 必须 404/不存在，health/admin summary 不再要求 node/cluster；
默认前端请求只要求有效 token，AES signature 仅在隔离、显式 opt-in 的兼容测试中启用。CI/test session
通过注入 fake WeChat exchanger 验证 token/identity，不依赖真实 code，也不得把空 code 当 production 成功。

前端契约按同一 canonical route-auth inventory 固定真实行为：fallback create 可匿名但必须通过
attempt/limit/SSRF；cache/parse poll 用随机 >=128-bit ID bearer，fallback 按服务端签名 poll/download
URL，m3u8 task poll 则按前端固定行为只发送随机 >=128-bit ID、最终 file URL 才签名；这些路径实际无
token。前端发送认证的请求同时验证 token/Bearer。Node fixture 必须证明没有隐式补 token 时上述每条
路径仍可完成，任意 ID/ticket 篡改均失败。

- [ ] **步骤 2：运行并确认至少一个失败**

运行：`go test ./tests/contracts -count=1`

预期：FAIL，指出尚未对齐的协议细节。

- [ ] **步骤 3：修正协议差异并建立分层测试 harness**

本任务本地只运行 hermetic/in-process Go 契约，依赖注入 fake store/cache/WeChat exchanger，不在目标宿主
执行 `docker run`、`go run` 或启动服务。服务级 Python E2E 由任务 13 的 GitHub Actions runner 使用
固定 digest 的 MySQL/Redis service containers 与当前源码测试二进制执行；任务 17 再对已经从 GHCR
拉取的 production 镜像执行同一服务契约。pytest 若依赖服务未就绪必须 FAIL，不能 skip 成功。

- [ ] **步骤 4：运行 E2E**

```bash
go test ./tests/contracts -count=1
go test ./internal/httpapi ./internal/auth -count=1
```

预期：本地 hermetic 测试全部 PASS；这里不调用需要活服务的 pytest。

- [ ] **步骤 5：在固定且未修改的原前端快照运行 Node 契约**

`scripts/verify-frontend-provenance.sh` 接受 `FRONTEND_REPO`，验证 clean、commit
`5d72c4925017676b6183b907dfe11ec60a4885bf`、tree `03c72a16532f51db76203967a3b982d49d4909d1`，以及
`miniprogram`、`test`、`project.config.json` tracked manifest SHA-256
`3e3f172b90439252e3601892e15fef2d398747ac9630fbc148013304d8c776f8`。运行前后都执行 guard：

```bash
FRONTEND_REPO=/srv/watermark scripts/verify-frontend-provenance.sh
for f in /srv/watermark/test/test_miniprogram_*.js; do node "$f"; done
FRONTEND_REPO=/srv/watermark scripts/verify-frontend-provenance.sh
```

预期：所有脚本退出 0 且前端仓库前后均 clean/hash 不变。Actions 不依赖该绝对路径，而是 second
checkout `1136623363/watermark` 的精确 commit 到隔离目录、运行同一 guard 与 Node contracts。

- [ ] **步骤 6：Commit**

```bash
git add tests scripts/verify-frontend-provenance.sh docs/frontend-provenance.json
git commit -m "test: lock current mini program API behavior"
```

## 任务 13：创建可复现 Docker 镜像和无 Jenkins Actions

**文件：**
- 修改：`Dockerfile`
- 创建：`requirements.lock`
- 创建：`deploy/compose.yml`
- 创建：`deploy/env.example`
- 创建：`deploy/image-lock.json`
- 创建：`deploy/migration/source-dump.sh`
- 创建：`deploy/migration/restore-clone.sh`
- 创建：`release/promotion-marker.txt`
- 修改：`.dockerignore`（promotion marker 不进入 rootfs）
- 创建：`.github/workflows/ci-image.yml`
- 创建：`scripts/verify-gitleaks.sh`
- 创建：`internal/policy/docker_ci_test.go`

- [ ] **步骤 1：编写 Docker/CI 策略失败测试**

测试每个 tracked `.yml/.yaml`：只要顶层出现 `services` 就按 Compose 解析，唯一允许路径是
`deploy/compose.yml`；拒绝锚点/merge 带入的 `build`、顶层 `include` 和 service `extends`。
运行镜像只接受 registry digest 引用；Dockerfile 的 builder/runtime 与 Compose 的 MySQL/Redis
基础镜像、MariaDB recovery image 都固定官方 digest并记录于 `deploy/image-lock.json`，同时固定
videodl/musicdl commit、非 root。policy 还锁定运行层包与源码绑定，不能只锁 base image。
workflow 无 Jenkins、执行 tests、启用 provenance/SBOM 并推送 GHCR。policy 枚举所有 workflow
`uses:`，要求带版本注释的精确 40 位 commit SHA，并拒绝 tag/branch、`pull_request_target`、checkout
持久化凭据和超出 job 所需的 permissions。
可复现负向测试还要只在 GitHub Actions runner 用两个模拟 revision 构建相同批准输入，比较 canonical
rootfs inventory 与 app hash；当前目标服务器绝不运行 Docker/Buildx 镜像构建；
若 Go VCS/buildid/time、ldflags commit/time、Python `.pyc` 或可变生成元数据进入 rootfs 必须失败，只有
OCI config 中明确 allowlist 的 revision label 可不同。
Compose policy 还必须证明 recovery 与 candidate 两个 role-set 可同时存在：每组的 API、
`parser-helper`、`egress-proxy`、`data-gate` 使用该组同一不可变 digest，而 A/B 两组允许且在 promotion 后
要求不同 digest；每组有专属 UDS volume、`internal: true` parser sandbox、proxy、gate receipt 和运行/
数据 identity，禁止跨组共享或串线。helper 只连本组 sandbox 和 UDS，无公开端口且不得连接 MySQL/
Redis；本组 proxy 是该 sandbox 唯一双网成员。`data-gate` 只连 data network，使用最小 DB 配置，固定
one-shot command、无 API/worker/listener/UDS/egress/API secrets，成功 receipt 必须绑定 role、digest、
deployment run、schema/DB identity 与输入 hash，API 启动时重验。所有命令固定为镜像内 allowlist
entrypoint。负向配置覆盖 A/B 共用 UDS/sandbox/proxy/receipt、helper 加入外网/data network、proxy
加入 DB network、gate 加入 egress/拿到 API secret、暴露端口、组内不同镜像、host network、额外 DNS/
volume/socket 或绕过 runtime inspect/receipt，任一都失败。
同时锁定 `scripts/verify-gitleaks.sh` 的版本、官方 URL、归档 SHA-256、`set -euo pipefail`、0700
临时目录、0600 扫描日志、EXIT/signal trap 清理和 `--log-opts=--all`；脚本只能输出 PASS/FAIL 与
退出码，不能输出 finding、路径、ref 名或字面量。workflow、任务 15 和任务 16 必须调用同一脚本。
下载必须安全跟随 GitHub HTTPS redirect，并锁定 HTTPS-only protocol/redirect、最大重定向、连接/总超时；
302、超时、降级协议或 hash 不符都 fail closed。

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/policy -run 'TestDocker|TestWorkflow|TestCompose' -count=1`

预期：FAIL。

- [ ] **步骤 3：实现 Dockerfile**

builder 的 `FROM` 同时包含 `golang:1.26.5` tag 与 `deploy/image-lock.json` 中经官方 manifest 校验的
64 位 digest（首选工具链 `go1.26.5`）；runtime 同样用 lock 中的官方 Python 基础镜像 digest，并固定
yt-dlp/videodl/musicdl 版本。ffmpeg 精确包版本必须来自固定日期的 reproducible snapshot；若平台不能
提供 snapshot，则改用经审查的静态制品并在 lock 记录固定制品 SHA-256，禁止浮动 apt/apk 包。Python
依赖 lock 为每个 Python 依赖记录 hash，安装强制 `--require-hashes`。实际 tag+digest、snapshot URL/
包版本或制品 hash 均写入 `deploy/image-lock.json`，policy 要求非空、格式合法且与 Dockerfile 一致。
镜像写入 `org.opencontainers.image.revision`（完整 `GITHUB_SHA`）和
`org.opencontainers.image.source`（固定新仓库 URL）OCI label。创建 UID 10001，只让 `/app/cache`、
`/app/logs`、`/app/tmp` 可写；`/app/tools` 与全部解析工具、Python 依赖随 rootfs 固定且运行时只读，
任何 pip/git/yt-dlp 自更新或替换工具文件都必须失败。Dockerfile 不设置会被所有 role 继承的通用 HTTP
`HEALTHCHECK`；Compose 必须逐服务配置 API HTTP+gate identity、helper UDS handshake、proxy self-check，
one-shot data-gate 禁用轮询 healthcheck 并只以 exit+receipt 判定。精确路径
`release/promotion-marker.txt` 只用于 B promotion 的 source commit/workflow trigger，必须由
`.dockerignore` 排除，Dockerfile 不得 COPY 进 rootfs；policy 验证 marker 不影响 app binary、工具或
运行配置。

Go 应用使用 `-trimpath -buildvcs=false` 且清空 buildid 的固定 flags 构建，禁止用 ldflags 把 commit、
构建时间或 workflow identity 注入 binary；source identity 只存在于 OCI label 与 attestation。Python
设置禁止生成 `.pyc`，固定 wheel/hash 并归一化允许进入 rootfs 的时间/生成元数据。A/B 比较以解包后的
canonical rootfs inventory、app binary hash、tool versions 与 schema 为准，不把 OCI config label 当作
rootfs 差异。

- [ ] **步骤 4：实现单机 Compose**

```yaml
services:
  data-gate-recovery:
    profiles: ["recovery"]
    image: ${RECOVERY_IMAGE:?set verified A registry digest}
    command: ["/app/bin/watermark-go", "data-gate"]
    environment:
      DEPLOY_ROLE: recovery
      DEPLOY_STAGE: ${RECOVERY_DATA_STAGE:?shadow or final}
      EXPECTED_IMAGE_DIGEST: ${RECOVERY_IMAGE}
      DEPLOYMENT_RUN_ID: ${DEPLOYMENT_RUN_ID:?current run}
      GATE_ATTEMPT_ID: ${RECOVERY_GATE_ATTEMPT_ID:?current gate attempt}
      GATE_MODE: ${RECOVERY_GATE_MODE:?apply or revalidate}
      MIGRATION_SOURCE_DSN: ${RECOVERY_MIGRATION_SOURCE_DSN:?clone DSN}
      MYSQL_DSN: ${RECOVERY_MIGRATION_TARGET_DSN:?least-privilege migrator DSN}
      REDIS_NAMESPACE: ${RECOVERY_REDIS_NAMESPACE:?role-stage namespace}
    networks: ["data"]
    volumes: ["gate-receipt-recovery:/run/watermark-gate"]
    read_only: true
    restart: "no"
    healthcheck: {disable: true}
    # 只注入本次 source/target DB 与 gate identity；无 API secret、端口、worker、UDS 或 egress。
  api-recovery:
    profiles: ["recovery"]
    image: ${RECOVERY_IMAGE:?same verified A registry digest}
    command: ["/app/bin/watermark-go", "serve"]
    environment:
      APP_ENV: production
      PORT: "5001"
      DEPLOY_ROLE: recovery
      DEPLOY_STAGE: ${RECOVERY_DATA_STAGE:?shadow or final}
      EXPECTED_IMAGE_DIGEST: ${RECOVERY_IMAGE}
      DEPLOYMENT_RUN_ID: ${DEPLOYMENT_RUN_ID:?current run}
      MYSQL_DSN: ${RECOVERY_MYSQL_DSN:?role target DSN}
      REDIS_ADDR: ${RECOVERY_REDIS_ADDR:?role Redis identity}
      REDIS_NAMESPACE: ${RECOVERY_REDIS_NAMESPACE:?role-stage namespace}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD:?required}
      ADMIN_SESSION_SECRET: ${ADMIN_SESSION_SECRET:?required}
      DOWNLOAD_TOKEN_SECRET: ${DOWNLOAD_TOKEN_SECRET:?required}
      WECHAT_MINI_APP_ID: ${WECHAT_MINI_APP_ID:?required}
      WECHAT_MINI_APP_SECRET: ${WECHAT_MINI_APP_SECRET:?required}
      WEIBO_COOKIE: ${WEIBO_COOKIE:-}
      XIGUA_COOKIE: ${XIGUA_COOKIE:-}
      SOHU_API_KEY: ${SOHU_API_KEY:-}
    ports: ["127.0.0.1:${RECOVERY_API_HOST_PORT:-5001}:5001"]
    networks: ["data", "egress"]
    mem_limit: 2g
    cpus: 2.0
    volumes:
      - "parser-ipc-recovery:/run/watermark-parser"
      - "gate-receipt-recovery:/run/watermark-gate:ro"
      - "app-cache-recovery:/app/cache"
      - "app-logs-recovery:/app/logs"
    read_only: true
    pids_limit: 256
    security_opt: ["no-new-privileges:true"]
    tmpfs: ["/app/tmp:size=268435456,mode=0700,uid=10001,gid=10001,nodev,nosuid,noexec"]
    healthcheck:
      test: ["CMD", "/app/bin/watermark-go", "healthcheck", "api"]
    depends_on:
      data-gate-recovery: {condition: service_completed_successfully}
  parser-helper-recovery:
    profiles: ["recovery"]
    image: ${RECOVERY_IMAGE:?same verified A registry digest}
    command: ["/app/bin/parser-helper"]
    environment:
      DEPLOY_ROLE: recovery
      DEPLOY_STAGE: ${RECOVERY_DATA_STAGE:?shadow or final}
      EXPECTED_IMAGE_DIGEST: ${RECOVERY_IMAGE}
      DEPLOYMENT_RUN_ID: ${DEPLOYMENT_RUN_ID:?current run}
    networks: ["parser_sandbox_recovery"]
    volumes: ["parser-ipc-recovery:/run/watermark-parser"]
    read_only: true
    mem_limit: 1g
    cpus: 1.5
    pids_limit: 64
    security_opt: ["no-new-privileges:true"]
    tmpfs: ["/app/tmp:size=536870912,mode=0700,uid=10001,gid=10001,nodev,nosuid,noexec"]
    healthcheck:
      test: ["CMD", "/app/bin/parser-helper", "healthcheck"]
    # 无公开端口、无 DB/Redis network/DSN/secret。
  egress-proxy-recovery:
    profiles: ["recovery"]
    image: ${RECOVERY_IMAGE:?same verified A registry digest}
    command: ["/app/bin/netguard-proxy"]
    environment:
      DEPLOY_ROLE: recovery
      EXPECTED_IMAGE_DIGEST: ${RECOVERY_IMAGE}
      DEPLOYMENT_RUN_ID: ${DEPLOYMENT_RUN_ID:?current run}
    networks: ["parser_sandbox_recovery", "egress"]
    read_only: true
    mem_limit: 256m
    cpus: 0.5
    pids_limit: 32
    security_opt: ["no-new-privileges:true"]
    healthcheck:
      test: ["CMD", "/app/bin/netguard-proxy", "healthcheck"]
    # 无公开端口、不得连接 MySQL/Redis。

  data-gate-candidate:
    profiles: ["candidate"]
    image: ${CANDIDATE_IMAGE:?set verified B registry digest}
    command: ["/app/bin/watermark-go", "data-gate"]
    environment:
      DEPLOY_ROLE: candidate
      DEPLOY_STAGE: ${CANDIDATE_STAGE:?disabled, shadow, drill or final}
      EXPECTED_IMAGE_DIGEST: ${CANDIDATE_IMAGE}
      DEPLOYMENT_RUN_ID: ${DEPLOYMENT_RUN_ID:?current run}
      GATE_ATTEMPT_ID: ${CANDIDATE_GATE_ATTEMPT_ID:?current gate attempt}
      GATE_MODE: ${CANDIDATE_GATE_MODE:?apply or revalidate}
      MIGRATION_SOURCE_DSN: ${CANDIDATE_MIGRATION_SOURCE_DSN:?clone or revalidate source}
      MYSQL_DSN: ${CANDIDATE_MIGRATION_TARGET_DSN:?least-privilege migrator DSN}
      REDIS_NAMESPACE: ${CANDIDATE_REDIS_NAMESPACE:?role-stage namespace}
    networks: ["data"]
    volumes: ["gate-receipt-candidate:/run/watermark-gate"]
    read_only: true
    restart: "no"
    healthcheck: {disable: true}
  api-candidate:
    profiles: ["candidate"]
    image: ${CANDIDATE_IMAGE:?same verified B registry digest}
    command: ["/app/bin/watermark-go", "serve"]
    environment:
      APP_ENV: production
      PORT: "5001"
      DEPLOY_ROLE: candidate
      DEPLOY_STAGE: ${CANDIDATE_STAGE:?disabled, shadow, drill or final}
      EXPECTED_IMAGE_DIGEST: ${CANDIDATE_IMAGE}
      DEPLOYMENT_RUN_ID: ${DEPLOYMENT_RUN_ID:?current run}
      MYSQL_DSN: ${CANDIDATE_MYSQL_DSN:?role target DSN}
      REDIS_ADDR: ${CANDIDATE_REDIS_ADDR:?role Redis identity}
      REDIS_NAMESPACE: ${CANDIDATE_REDIS_NAMESPACE:?role-stage namespace}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD:?required}
      ADMIN_SESSION_SECRET: ${ADMIN_SESSION_SECRET:?required}
      DOWNLOAD_TOKEN_SECRET: ${DOWNLOAD_TOKEN_SECRET:?required}
      WECHAT_MINI_APP_ID: ${WECHAT_MINI_APP_ID:?required}
      WECHAT_MINI_APP_SECRET: ${WECHAT_MINI_APP_SECRET:?required}
      WEIBO_COOKIE: ${WEIBO_COOKIE:-}
      XIGUA_COOKIE: ${XIGUA_COOKIE:-}
      SOHU_API_KEY: ${SOHU_API_KEY:-}
    ports: ["127.0.0.1:${CANDIDATE_API_HOST_PORT:-15001}:5001"]
    networks: ["data", "egress"]
    mem_limit: 2g
    cpus: 2.0
    volumes:
      - "parser-ipc-candidate:/run/watermark-parser"
      - "gate-receipt-candidate:/run/watermark-gate:ro"
      - "app-cache-candidate:/app/cache"
      - "app-logs-candidate:/app/logs"
    read_only: true
    pids_limit: 256
    security_opt: ["no-new-privileges:true"]
    tmpfs: ["/app/tmp:size=268435456,mode=0700,uid=10001,gid=10001,nodev,nosuid,noexec"]
    healthcheck:
      test: ["CMD", "/app/bin/watermark-go", "healthcheck", "api"]
    depends_on:
      data-gate-candidate: {condition: service_completed_successfully}
  parser-helper-candidate:
    profiles: ["candidate"]
    image: ${CANDIDATE_IMAGE:?same verified B registry digest}
    command: ["/app/bin/parser-helper"]
    environment:
      DEPLOY_ROLE: candidate
      DEPLOY_STAGE: ${CANDIDATE_STAGE:?disabled, shadow, drill or final}
      EXPECTED_IMAGE_DIGEST: ${CANDIDATE_IMAGE}
      DEPLOYMENT_RUN_ID: ${DEPLOYMENT_RUN_ID:?current run}
    networks: ["parser_sandbox_candidate"]
    volumes: ["parser-ipc-candidate:/run/watermark-parser"]
    read_only: true
    mem_limit: 1g
    cpus: 1.5
    pids_limit: 64
    security_opt: ["no-new-privileges:true"]
    tmpfs: ["/app/tmp:size=536870912,mode=0700,uid=10001,gid=10001,nodev,nosuid,noexec"]
    healthcheck:
      test: ["CMD", "/app/bin/parser-helper", "healthcheck"]
  egress-proxy-candidate:
    profiles: ["candidate"]
    image: ${CANDIDATE_IMAGE:?same verified B registry digest}
    command: ["/app/bin/netguard-proxy"]
    environment:
      DEPLOY_ROLE: candidate
      DEPLOY_STAGE: ${CANDIDATE_STAGE:?disabled, shadow, drill or final}
      EXPECTED_IMAGE_DIGEST: ${CANDIDATE_IMAGE}
      DEPLOYMENT_RUN_ID: ${DEPLOYMENT_RUN_ID:?current run}
    networks: ["parser_sandbox_candidate", "egress"]
    read_only: true
    mem_limit: 256m
    cpus: 0.5
    pids_limit: 32
    security_opt: ["no-new-privileges:true"]
    healthcheck:
      test: ["CMD", "/app/bin/netguard-proxy", "healthcheck"]

  source-mariadb-dump:
    profiles: ["migration-tools"]
    image: ${MARIADB_RECOVERY_IMAGE:?set pinned recovery-tool digest}
    entrypoint: ["/usr/local/bin/source-dump"]
    command: []
    network_mode: "none"
    read_only: true
    restart: "no"
    volumes:
      - type: bind
        source: /run/mysqld/mysqld.sock
        target: /run/source/mariadb.sock
        read_only: true
        bind: {create_host_path: false}
      - type: bind
        source: ${SOURCE_MARIADB_DEFAULTS_FILE:?0600 temporary file}
        target: /run/secrets/source.cnf
        read_only: true
        bind: {create_host_path: false}
      - "migration-backup:/backup"
      - type: bind
        source: ./deploy/migration/source-dump.sh
        target: /usr/local/bin/source-dump
        read_only: true
        bind: {create_host_path: false}
    # 只经已纳入 host-before allowlist 的 Unix socket读取源库；绝不挂源数据目录。
  mariadb-clone:
    profiles: ["migration-tools"]
    image: ${MARIADB_RECOVERY_IMAGE:?same pinned recovery-tool digest}
    environment:
      MARIADB_DATABASE: watermark_source_clone
      MARIADB_ROOT_PASSWORD: ${MARIADB_CLONE_ROOT_PASSWORD:?temporary clone secret}
    networks: ["data"]
    volumes: ["mariadb-clone-data:/var/lib/mysql"]
    healthcheck:
      test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"]
    # 无 host port；凭据只来自 migration-tools 专属临时 secret。
  restore-mariadb-clone:
    profiles: ["migration-tools"]
    image: ${MARIADB_RECOVERY_IMAGE:?same pinned recovery-tool digest}
    entrypoint: ["/usr/local/bin/restore-clone"]
    command: []
    networks: ["data"]
    read_only: true
    restart: "no"
    volumes:
      - "migration-backup:/backup:ro"
      - type: bind
        source: ${MARIADB_CLONE_DEFAULTS_FILE:?0600 temporary file}
        target: /run/secrets/clone.cnf
        read_only: true
        bind: {create_host_path: false}
      - type: bind
        source: ./deploy/migration/restore-clone.sh
        target: /usr/local/bin/restore-clone
        read_only: true
        bind: {create_host_path: false}
    depends_on:
      mariadb-clone: {condition: service_healthy}
    # 只把备份恢复到 data network 内的隔离 MariaDB clone，无 host network/host port。

  mysql:
    image: ${MYSQL_IMAGE:?set pinned mysql registry digest}
    environment:
      MYSQL_DATABASE: watermark_control
      MYSQL_USER: watermark
      MYSQL_PASSWORD: ${MYSQL_PASSWORD:?application DB secret}
      MYSQL_ROOT_PASSWORD: ${MYSQL_ROOT_PASSWORD:?bootstrap/migrator secret}
    networks: ["data"]
    volumes:
      - type: bind
        source: /var/lib/watermark-go/data/mysql
        target: /var/lib/mysql
        bind: {create_host_path: false}
    mem_limit: 1g
  redis:
    image: ${REDIS_IMAGE:?set pinned redis registry digest}
    command: ["redis-server", "--appendonly", "yes", "--appendfsync", "everysec", "--save", "900", "1", "--protected-mode", "no"]
    networks: ["data"]
    volumes:
      - type: bind
        source: /var/lib/watermark-go/data/redis
        target: /data
        bind: {create_host_path: false}
    mem_limit: 256m
networks:
  data:
    internal: true
  parser_sandbox_recovery:
    internal: true
  parser_sandbox_candidate:
    internal: true
  egress: {}
volumes:
  parser-ipc-recovery: {}
  parser-ipc-candidate: {}
  gate-receipt-recovery: {}
  gate-receipt-candidate: {}
  app-cache-recovery: {}
  app-cache-candidate: {}
  app-logs-recovery: {}
  app-logs-candidate: {}
  migration-backup: {}
  mariadb-clone-data: {}
```

`docker compose --env-file` 只负责插值，不会自动注入容器；实际 Compose 必须像上表一样逐键列出每个
service 的环境 allowlist，禁止 service-level `env_file:` 或把整份 runtime environment 传入。API 只拿
本 role 当前 data identity 与 typed API/parser/native secret；data-gate 只拿 clone/target DSN、run/gate/
receipt identity；helper/proxy 只拿非秘密 role/stage/digest/run/UDS/proxy 配置，绝不拿 DB/Redis、微信、
admin、download、Cookie/Sohu/music provider secret。B shadow 的 candidate variables 必须指向专属 shadow
schema/Redis/outbox，不能出现 A/final sentinel。policy 为每组注入唯一 sentinel，runtime inspect 验证
允许的 key 集与 configured 状态，并断言 helper/proxy/job payload/env/argv/log/output 无原值。
Compose config/hash 若含插值秘密，只能写到 0700 目录内的 0600 temp，生成 secret-keyed redacted hash 后
立即删除；终端和 artifact 不得输出渲染内容或 `docker inspect` 的 Env values。

`source-dump.sh`/`restore-clone.sh` 是经 policy 固定 hash/argv 审计的无参数 wrapper：`set -Eeuo
pipefail`、`umask 077`，数据库与表只来自 tracked allowlist；source dump 固定
`--defaults-extra-file=/run/secrets/source.cnf --socket=/run/source/mariadb.sock --single-transaction --quick
--skip-lock-tables --hex-blob`，写 `source.sql.part` 后 fsync、生成固定文件名 SHA-256 manifest、原子 rename
和 directory fsync。restore 在读任何 SQL 前复算固定 manifest/hash，只用
`--defaults-extra-file=/run/secrets/clone.cnf --host=mariadb-clone` 恢复到隔离 clone，失败不留下 passed
receipt。wrapper 不接受数据库名/表名/flag 的用户 argv，不 eval/拼 shell。host bind 只允许 discovery
已写入 `host-before` allowlist 的精确 `/run/mysqld/mysqld.sock`，拒绝其他 socket、host network、修改
源监听、宿主 MariaDB data-dir mount。data-gate 只连接 clone/target，不直接连接宿主源。
所有 host bind 使用 long syntax `type: bind` + `bind.create_host_path: false`。preflight 在 Compose 前对
socket 执行 `-S`/非 symlink/owner+device+inode 与 host-before identity 核对，对 defaults file 执行
regular-file/0600/0700 parent/非 symlink，对 wrapper 执行 tracked path+SHA-256，对 data dir 执行已存在
directory/owner/mode/device/free-space 检查；任一缺失直接失败，绝不让 Docker 自动创建同名目录。

recovery/candidate host bind 使用严格 allowlist：任何 shadow 或 B drill 只允许
`127.0.0.1:15001`；当前 recovery A final 与最终 candidate B 只允许 `127.0.0.1:5001`。A final 运行时，
B candidate 可在 15001 并存；Task 18 必须先 fence/stop A，确认 5001 释放，才把 B recreate 到 5001。
B→A 回退必须先停止 B，再按保留的 A config/digest 把 recovery role 恢复到 5001。preflight 拒绝
`0.0.0.0`、空地址、其他端口和两组同时绑定同一 host port。可选 LAN 只能通过独立、临时受限
override，在测试后立即撤销，不得写入正式 runtime file。

Compose 在 profile 未启用时仍会做变量插值，因此 A-only 阶段不得留空 `CANDIDATE_IMAGE`：正式 0600
runtime file 明确令 `CANDIDATE_IMAGE=$RECOVERY_IMAGE` 且 `CANDIDATE_STAGE=disabled`，只为安全渲染，
preflight 与 candidate 四个 entrypoint 必须拒绝启动。B digest+attestation+A/B rootfs equivalence 通过后
才可设 `shadow`（只允许 15001+全新 shadow identity）；B shadow 通过且切流前 A/B final-DB receipt
重验完成后才可短暂设 `drill`（只允许 15001+锁定 final identity）；drill/canonical B evidence 通过后
才可设 `final`（只允许 5001+锁定 final identity）。policy 负测覆盖 inactive variable 缺失、disabled
启动、A/B 同 digest 却进入非 disabled、stage/port/data identity 错配，以及绕过 profile 直接指定
service。禁止用裸 tag、空值或不可验证的假 digest 充当 sentinel。

Redis identity 不等于单纯 `redis:6379` 地址：每次 A-shadow、B-shadow、final/recovery 都必须有稳定的
`REDIS_NAMESPACE`（含 role+stage，shadow 另绑定 run），并由 gate receipt、API startup、runtime inspect
和 acceptance artifact 四方对账。A/B shadow namespace 彼此不同且都不得等于 final；drill/final/
rollback 只能使用已锁定的同一 final namespace。cache、lock、rate-limit、task/outbox key builder 必须
强制加此前缀，禁止调用方绕过。

Compose profiles 分离 `recovery`、`candidate` 与 `migration-tools`，而 shadow/final 是每次 role 启动时
显式绑定并写入 receipt 的 data identity，不靠共享可变 service 名冒充。shadow 使用 A/B 各自新建的
DB/schema、Redis namespace、outbox 和 gate run；final production DB/Redis 与全部 shadow identity
隔离。`migration-tools` 只包含由 `MARIADB_RECOVERY_IMAGE` 指向官方 digest 的 MariaDB recovery image，
不发布端口、不以裸 `docker run` 启动。应用 `data-gate-*` 必须与对应 role 使用同一 A/B digest，固定
执行 `watermark-go data-gate`，成功退出且生成本次绑定 receipt 后 API 才能监听。policy 检查 profile、
role image、网络、卷、数据库 identity、receipt 和运行变量，防止 shadow/rehearsal 串到 final 数据面。

两套 `parser-helper`/`egress-proxy` 都是同一应用镜像的 stateless sandbox role，不是另一套 backend 或
独立业务服务。每组 API 与 helper 只经本组 UDS 有界 job protocol 通信；helper 只有本组
`parser_sandbox_*` membership，raw socket 无外网路由，本组 proxy 才同时加入 `egress`。API/native Go
egress 仍走进程内 netguard。Compose policy、CI integration 与目标 runtime inspect 三者都必须按
recovery/candidate role 验证 RepoDigest、固定 command、network IDs/membership、UDS/receipt 不交叉、
无公开端口、helper/proxy 不含 MySQL/Redis DSN/secret；验证缺失时
production fallback fail closed。helper 的 memory/CPU/PID/read-only/tmpfs/no-new-privileges hard limits
必须由渲染后的 Compose 与 runtime inspect 同时验证，用来隔离第三方解压/进程资源；超限 job 失败但
不得拖垮 API。sandbox network/UDS volume 随 project 生命周期管理，不绑定宿主任意路径。

`RECOVERY_IMAGE`、`CANDIDATE_IMAGE`、MySQL/Redis/recovery-tool 等全部镜像变量都必须是
`repository@sha256:` 后紧跟 64 位小写十六进制 digest；裸 tag、短 SHA 和 `latest` 被 policy
拒绝。Task 13 通过官方 registry manifest/只读 imagetools inspect 在受控提交中解析并人工核对平台，
把 tag+digest 固定到 lock；依赖更新必须独立审查提交。MySQL/Redis 不发布宿主机端口，数据 bind
mount 只允许精确 `/var/lib/watermark-go/data/{mysql,redis}`，preflight 验证 root-owned parent、服务 UID
所需最小权限、非 symlink、可用空间/inode 与备份恢复；禁止匿名卷、相对路径、挂宿主源 MariaDB 数据
目录或把整个 `/var/lib/watermark-go` 暴露给容器。
环境示例文件使用可跟踪的 `deploy/env.example`；`.env*` 继续全局禁止跟踪。安全前置已删除旧根 Compose、旧 Nginx 配置和 mutable sync 脚本，Task 13 不得恢复它们。

- [ ] **步骤 5：实现 Actions**

push main 的 checkout 必须设置 `fetch-depth: 0` 和 `fetch-tags: true`，使 secret scan 覆盖所有
heads/tags 可达历史、annotated tag message 与即将推送 refs。Gitleaks Action 固定为
`gitleaks/gitleaks-action@ff98106e4c7b2bc287b24eaf42907196329070c7 # v2.3.9`，并在该 step 显式设置
`GITLEAKS_VERSION: "8.30.1"`，不得使用浮动 tag。该 Action 的 push 扫描是 first-parent 增量门禁，
不能被描述为全 refs/history 证据；checkout 后必须另行执行 `scripts/verify-gitleaks.sh`，以已校验的
固定 v8.30.1 CLI 和显式 `--log-opts=--all` 作为权威全历史门禁。先运行
`gofmt/vet/test -race`、policy/pytest/Node/store integration；随后 Buildx **只构建一次**并把 manifest
推到隔离索引 `ghcr.io/1136623363/watermark-go:sha-${GITHUB_SHA}`（完整 40 位），捕获其精确 manifest
digest。Trivy、解包 canonical rootfs/tool inventory、role command/health/runtime smoke、SBOM、
provenance attestation subject 与 OCI label 全部针对该 `repository@sha256:` digest 验证。全部成功后才
用 registry manifest copy/retag 把便利标签 `latest` 指向同一 digest，禁止二次 build 生成另一个 digest。
失败 digest/sha tag 可留在 quarantine 供调查，但不得写 canonical release evidence、不得移动
`latest`、不得部署。运行、验收和回滚永远只使用经过上述门禁的 digest，不用任何 tag；不调用远程
服务器或 Jenkins。

workflow 顶层默认 `permissions: {}`；所有 checkout/setup-go/setup-python/buildx/login/build-push/
attest/Trivy action 固定 40 位 commit SHA 并带版本注释。checkout 设置 `persist-credentials:false`；
测试 job 只授予 `contents:read`，image job 才最小授予 `packages:write` 和 attestation 所需的
`id-token/attestations` 权限，禁止 `pull_request_target`。second checkout 原前端仓库精确 commit
`5d72c4925017676b6183b907dfe11ec60a4885bf` 到隔离目录，运行 provenance guard 与 Node contracts。
服务级 pytest 使用 lock 中固定 digest 的 MySQL/Redis services、当前源码测试二进制和 test-only fake
WeChat exchanger。
CI 不 clone、执行或按默认分支/tag 同步 `ucmao/media-parser`；它只是由 repository policy 校验的固定研究
provenance，不进入 runtime/SBOM dependency graph。若未来独立审查后复制实质代码/数据，必须先新增
MIT NOTICE/license attribution、复制文件 manifest 与 policy 门禁，并让 SBOM/provenance 反映真实依赖。
测试 job 必须显式运行 Task 15 的 media-parser focused suite 并保存逐测试 manifest/hash，不能只依赖宽泛
`go test ./...`。image job 捕获唯一 tested manifest digest 后，把该 source-level 结果、精确
`sourceCommit`、实际 `imageDigest`、GitHub `ciRunId`、runtime smoke 与 unsafe runtime/build-graph scan
组合成 versioned `mediaParserIntegration` artifact，并将其 subject/identity 纳入 attestation；任何 source/
digest/run 不一致都不得移动便利标签或发布 canonical evidence。
镜像测试还必须逐项验证运行所需 yt-dlp/Python bridge/ffmpeg 的固定版本与可执行性，防止“代码调用工具
但 runtime 未安装”的缺口。
单独的 store integration job 使用 `deploy/image-lock.json` 固定的 MariaDB 11.8 recovery 与 MySQL 8.4
service，等待 readiness 后强制执行 `go test -tags=integration ./tests/integration/store -count=1`；service
未 ready、测试未执行或 skip 都使 workflow 失败。目标宿主 Task 17 前不运行这些测试容器或碰源库。

CI 自锁分两级。所有 push 只运行 verifier unit/schema-of-present：先跑 `tests/ops` 中 verifier 单元/
负向夹具，再执行 `python3 scripts/verify-acceptance.py --schema-of-present`，仅校验当前存在 artifact 的
schema/原子生成标记和内部一致性，不要求尚未产生的部署证据。仅 final evidence push 执行
`python3 scripts/verify-acceptance.py --require-complete`；该模式缺任一 R1–R8 artifact 都失败。不得在普通
源码 push 上调用 complete 模式形成 CI 自锁。image job 仅在 Dockerfile、锁文件、Go/Python 依赖、
精确路径 `release/promotion-marker.txt` 或应用源码构建输入变化时执行；docs/artifacts-only push 不运行 image job、不重建镜像，
也不移动 `latest`。
marker 触发是额外硬门禁：workflow 必须定位 marker 绑定的 immutable `evidence/recovery-<runId>` ref，
执行 `verify-acceptance.py --require-recovery-ready`，验证 evidence commit parent=A source、只含允许的脱敏
artifact、payload hash/A digest/promotionRunId 与 marker 一致，且 B main commit parent=A、diff 只有 marker。
任一不满足时 image job 在 Buildx 前失败；普通手工 marker 或 evidence ref 更新被 policy 拒绝。

Buildx 在唯一一次 build/push 中同时生成 provenance attestation subject，把捕获的 manifest digest 绑定
完整 40 位 source commit 和固定 source URL。workflow 必须把 exact tested digest 作为 Trivy/SBOM/
rootfs/tool/runtime-smoke/attestation 的唯一 subject，并在移动便利 tag 前交叉核对全部 subject 相同；
policy 用恶意 workflow fixture 拒绝 scan-before-build、scan tag、第二次 build、测试 digest 与 retag
digest 不同或失败仍更新 tag。`scripts/verify-image.sh`、发布证据与目标机 runtime inspect 联合验证：
实际 RepoDigest 等于该 tested/attested digest，OCI `org.opencontainers.image.revision` 等于完整 commit，
`org.opencontainers.image.source` 等于新仓库；tag 仅作索引，不能作为运行身份或通过依据。

本地与 CI 的权威 Gitleaks CLI 固定为 `v8.30.1`。Linux x64 官方 release 归档的 SHA-256 固定为
`551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb`。统一脚本只从官方 release
下载归档与 checksum 文件，静默核对 checksum 文件中的固定值及下载归档本身，任一步失败立即退出；
随后执行 `gitleaks git --no-banner --redact --log-opts=--all .`。所有 stdout/stderr 写入 0600 临时
日志，终端只输出 PASS/FAIL 与退出码。

`scripts/verify-gitleaks.sh` 按以下安全骨架实现，策略测试校验关键语义且后续任务不得复制另一份命令：

```bash
#!/usr/bin/env bash
set -euo pipefail
umask 077
fail() { status="$1"; printf 'FAIL exit=%d\n' "$status" >&2; exit "$status"; }
tmpdir="$(mktemp -d 2>/dev/null)" || fail "$?"
chmod 700 "$tmpdir" 2>/dev/null || fail "$?"
cleanup() { rm -rf -- "$tmpdir" >/dev/null 2>&1 || true; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

version='8.30.1'
archive="$tmpdir/gitleaks.tar.gz"
checksums="$tmpdir/checksums.txt"
scan_log="$tmpdir/scan.log"
: >"$scan_log" 2>/dev/null || fail "$?"
chmod 600 "$scan_log" 2>/dev/null || fail "$?"
verify_all() {
  curl -fsSL --proto '=https' --proto-redir '=https' --max-redirs 5 --connect-timeout 10 --max-time 120 \
    -o "$archive" "https://github.com/gitleaks/gitleaks/releases/download/v${version}/gitleaks_${version}_linux_x64.tar.gz" || return $?
  curl -fsSL --proto '=https' --proto-redir '=https' --max-redirs 5 --connect-timeout 10 --max-time 120 \
    -o "$checksums" "https://github.com/gitleaks/gitleaks/releases/download/v${version}/gitleaks_${version}_checksums.txt" || return $?
  grep -F '551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb  gitleaks_8.30.1_linux_x64.tar.gz' "$checksums" >/dev/null || return $?
  printf '%s  %s\n' '551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb' "$archive" | sha256sum -c - >/dev/null || return $?
  tar -xzf "$archive" -C "$tmpdir" gitleaks >/dev/null || return $?
  "$tmpdir/gitleaks" git --no-banner --redact --log-opts=--all . || return $?
}
if verify_all >"$scan_log" 2>&1; then
  printf 'PASS\n'
else
  status=$?
  printf 'FAIL exit=%d\n' "$status" >&2
  exit "$status"
fi
```

- [ ] **步骤 6：只做静态验证，禁止本地 build**

运行：

```bash
go test ./internal/policy -count=1
RECOVERY_IMAGE=ghcr.io/1136623363/watermark-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  CANDIDATE_IMAGE=ghcr.io/1136623363/watermark-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  CANDIDATE_STAGE=disabled RECOVERY_DATA_STAGE=final \
  RECOVERY_API_HOST_PORT=5001 CANDIDATE_API_HOST_PORT=15001 \
  DEPLOYMENT_RUN_ID=static-config-test RECOVERY_GATE_ATTEMPT_ID=recovery-static RECOVERY_GATE_MODE=apply \
  CANDIDATE_GATE_ATTEMPT_ID=candidate-disabled-static \
  CANDIDATE_GATE_MODE=apply RECOVERY_MIGRATION_SOURCE_DSN=redacted-source \
  RECOVERY_MIGRATION_TARGET_DSN=redacted-migrator RECOVERY_MYSQL_DSN=redacted-target \
  RECOVERY_REDIS_ADDR=redis:6379 RECOVERY_REDIS_NAMESPACE=recovery-final \
  CANDIDATE_MIGRATION_SOURCE_DSN=disabled \
  CANDIDATE_MIGRATION_TARGET_DSN=disabled CANDIDATE_MYSQL_DSN=disabled CANDIDATE_REDIS_ADDR=disabled \
  CANDIDATE_REDIS_NAMESPACE=disabled \
  MYSQL_IMAGE=mysql@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  REDIS_IMAGE=redis@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc \
  MARIADB_RECOVERY_IMAGE=mariadb@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd \
  SOURCE_MARIADB_DEFAULTS_FILE=/tmp/source.cnf MARIADB_CLONE_DEFAULTS_FILE=/tmp/clone.cnf \
  MYSQL_PASSWORD=x MYSQL_ROOT_PASSWORD=x MARIADB_CLONE_ROOT_PASSWORD=x \
  ADMIN_PASSWORD=x ADMIN_SESSION_SECRET=x DOWNLOAD_TOKEN_SECRET=x \
  WECHAT_MINI_APP_ID=x WECHAT_MINI_APP_SECRET=x \
  docker compose -f deploy/compose.yml config --quiet
scripts/verify-gitleaks.sh
```

预期：PASS；策略测试枚举所有 tracked YAML 并按顶层 `services` 识别 Compose，任何非规范路径、
`build`、`include` 或 `extends` 都失败；仓库扫描与 Gitleaks 覆盖完整 refs/history，执行历史中没有
`docker build` 或 `docker compose build`。

- [ ] **步骤 7：Commit**

```bash
git add Dockerfile requirements.lock deploy .github scripts/verify-gitleaks.sh \
  internal/policy/docker_ci_test.go release/promotion-marker.txt .dockerignore
git commit -m "ci: publish reproducible GHCR image without Jenkins"
```

## 任务 14：实现基准、部署、监控和回滚脚本

**文件：**
- 创建：`scripts/baseline/run.py`
- 创建：`scripts/smoke.sh`
- 创建：`scripts/deploy-local.sh`
- 创建：`scripts/rollback-local.sh`
- 创建：`scripts/preflight.sh`
- 创建：`scripts/observe.sh`
- 创建：`scripts/verify-image.sh`
- 创建：`scripts/host-snapshot.sh`
- 创建：`scripts/verify-acceptance.py`
- 创建：`scripts/promote.sh`
- 创建：`scripts/write-evidence.py`
- 创建：`tests/baseline/test_report.py`
- 创建：`tests/ops/test_scripts.py`
- 创建：`docs/runbook.md`

- [ ] **步骤 1：编写报告门禁测试**

```python
def test_gate_recomputes_canonical_baseline(canonical_full_report):
    report = canonical_full_report(records=93, success=62,
                                   wall_clock_ms=216000,
                                   max_observed_concurrency=3)
    assert evaluate(report).passed is True
    # 伪造仍为 216000 的 aggregate 不能掩盖原始 record 已超时。
    report["records"][-1]["endedMonotonicMs"] += 1
    report["durationMs"] = 216000
    assert evaluate(report).passed is False
```

`tests/ops` 为 `verify-acceptance.py` 构造负向夹具逐项覆盖：缺文件、schema 错、commit/digest 不一致、
基准不足或超过 3 轮、重复/空 `unique runId`、固定 `canonicalFixtureSha256` 不一致、
`concurrency=3` 不满足、`nativeEnabled=true`/`fallbackEnabled=true`/`cacheBypass=true` 任一不满足、
`completed=93` 不满足、`success>=62` 不满足、`durationMs<=216000` 不满足，以及 93 个 enabled 样本
缺项、重复项、跨轮复用或缺少本轮唯一 `parserInvocationId`。验证器从原始字段与逐项调用证据重新计算
结论，`passed=true 不能绕过`任一失败条件；`passed` 仅交叉核对，缺失、为 false 或与重算结果不一致
也失败。另按 chosenMigrationMode 覆盖迁移负例：`final_full_no_binlog` 缺 final snapshot/import/checksum/
table-scoped no-writer 任一项都失败，只有可靠 binlog mode 才要求 delta position；还覆盖观察原始样本
不足、适用回滚超过 5 分钟、前端 provenance 不匹配、`deploymentRunId`/`cutoverAttemptId` 缺失或
跨 artifact 不一致，以及旧 `passed=true` artifact 的 attemptId 与本次 B 切换不匹配。旧证据即使自身
schema/aggregate 合法也必须拒绝；任一条件必须非零退出。
`tests/ops` 还必须覆盖写正式文件前崩溃、rename/fsync 失败、ERR/EXIT/HUP/INT/TERM 中断、遗留
`in_progress` attempt、旧 PASS scan/status 文件和重启 reconcile；任何情形都不能复用旧 passed。
同组负测以 fake Compose runner 锁定 data-gate 生命周期：每个 role/stage/run 必须先
`--force-recreate --no-deps` one-shot、核对新 container start identity/exit/receipt，再允许启动 API；复用
旧 exited container、只依赖 `depends_on`、缺 force-recreate、receipt 早于 attempt 或跨 role volume 都失败。
promotion 负测覆盖：缺任一 A recovery artifact、evidence commit parent 非 A、evidence diff 夹带源码、
marker parent 非 A、marker 为空/手写/绑定错 digest/OID/payload hash、重复或可变 evidence ref，以及 marker
外任何 A→B tracked diff；都必须在 push/Buildx 前失败。`scripts/promote.sh` 只在本地 recovery-ready
verifier 通过后用临时 index 构造 evidence-only commit/ref 和唯一 marker commit，不修改 A source tree，
并复用安全 ASKPASS、index-aware repository policy 与全 refs Gitleaks 门禁。

`verify-acceptance.py` 还必须重算并验证 `mediaParserIntegration` machine evidence，而不是相信顶层
`passed`：至少绑定完整 `sourceCommit`、实际 `imageDigest`、可信 `ciRunId`、test manifest/hash 与
registry/structured JSON/query policy/candidate ranking+budget/cache semantics/rich-media/unsafe-pattern
各子门的独立结果。缺字段、source/digest/CI run 不匹配、任一子门缺失或 false、用 live aggregate 代替
hermetic 原始测试清单，均由 `tests/ops` 负向夹具证明非零失败。

观察证据的负向夹具禁止只信聚合：要求 `endedAt-startedAt>=1800s`；第一个样本在 startedAt 后约 30 秒，
第 60 个样本不早于 startedAt+1800s，恰好 60 个唯一且严格递增的序号/时间戳，相邻采样合理间隔为
25–35 秒（单调时钟口径）。每个原始样本都必须含 `imageDigest`、health 状态与 `healthLatencyMs`、
`restartCount`、`oomCount`、`ioErrors`、MemAvailable/swap、`memoryPSI`、`ioPSI`、disk/inode；验证器从
60 个原始样本重算 P95、成功数和全部停止线，不信任 60/60 聚合或自报 P95。

基准证据同样从 records 验真：每轮必须包含与 provenance canonical enabled set 精确相等的 93 个唯一
sampleKey，无缺失/重复；从 records 重算 completed/success/wall-clock，每个成功 record 都有 media
success 和本轮唯一 `parserInvocationId`。三轮时间窗不重叠、runId/record-set hash 独立且 records 不复用；
验证器还从每条 record 的 started/ended 区间用 half-open 语义重算 `maxObservedConcurrency`，要求不超过
3，且在 93 个可运行样本下实际达到 3；负向夹具覆盖自报 concurrency=3 但四条重叠，以及全程串行。
任何逐项记录或时间证据失败时，即使 aggregate/`passed` 为真也拒绝，明确不信任 aggregate。

同时测试主机快照必须采集 `MemAvailable`、`vmstat` 的 swap `si/so` 速率、memory/io PSI、OOM
计数、磁盘与 inode 使用率；保护器按连续采样判断压力，静态 swap 已占用但 `si/so=0` 不能单独触发
停机。持续换入换出、PSI/IO 压力、OOM 增量、inode/磁盘越线或 `MemAvailable` 低于安全线时，必须
停止创建新基准/候选栈重任务且只操作 Compose project `watermark-go`，不得停止其他容器。

- [ ] **步骤 2：确认失败**

运行：`python3 -m pytest tests/baseline -q`

预期：FAIL，runner/evaluator 尚不存在。

- [ ] **步骤 3：实现基准和主机保护**

runner 输出 fixture hash、image digest、commit、代理、timeout、并发、每平台耗时/错误和总墙钟；
强制 `enabled=completed=93`、`success>=62`、`durationMs<=216000`。
主机脚本保留 CPU/内存/磁盘持续 85% 停止线，并增加以下默认安全线：`MemAvailable` 低于
`max(1 GiB, MemTotal 的 15%)`、inode 使用率达到 85%、OOM 计数增加，或连续 3 个采样周期出现
swap `si+so>0`、memory PSI `some avg10>10`、io PSI `full avg10>5`，均停止创建新重任务并停止/撤销
候选 `watermark-go` 栈。静态 swap 占用本身只记录不误停。脚本输出脱敏 JSON，包含采样时间窗、
MemAvailable/阈值、swap 已用量与 si/so、memory/io PSI、OOM、磁盘/inode、触发原因和采取的候选栈
动作；只允许操作 project `watermark-go`，不停止、重启或修改其他容器。

- [ ] **步骤 4：实现只拉取部署与回滚**

部署脚本拒绝包含 `build:` 的 Compose，并先 discovery 决定状态机。通用顺序是：宿主机/运行 route
权威快照 → MariaDB engine/capability/writer 发现 → 固定 recovery tool 的备份与隔离恢复 → 只 pull
digest → shadow/final 分离的 migration/import/checksum gate → shadow 自动验收。任何 API/listener/worker
都不得早于对应数据 gate 启动。

当前 `oldServicePresent=false` 选择 `rollbackMode=absent_two_stage`：先 A shadow/真实域名/真微信/>=30m
成为 recovery，再发布等价 B、B shadow、真实 B→A drill，最后 Task 18 切 B。状态文件按 role 记录 A/B
各自 source commit、digest/attestation、Compose config、route identity、shadow/final DB identity、
`chosenMigrationMode` 与适用 rollback；不得强迫不同角色共用一个 commit/digest。

preflight 对 Compose 渲染后的 API bind 做严格 allowlist，只允许 shadow `127.0.0.1:15001` 或 final
`127.0.0.1:5001`；运行文件固定 `/var/lib/watermark-go/runtime.env` 且为 0600。默认所有脚本只操作
Compose project `watermark-go`；只有 cutover/rollback 状态机可额外操作 `host-before.json` 中
host-before 精确 identity/hash allowlist 已验证的旧 watermark service 与 route，identity 不存在或变化
则 fail closed，绝不枚举或修改无关容器、进程、systemd、网络、卷和路由。

回滚脚本只接受状态中已验证的 digest/identity。absent_two_stage 的实际 previous digest 分支只能从 B
回 verified A，并共用已证明双向 schema 兼容的 final DB。A 首次 bootstrap 失败时只能 fence/drain A、
撤回新 route、恢复原 502 状态并记录 FAILED，不得冒充健康回滚。

只有 discovery 证明确有 allowlist 内旧服务时，才启用 conditional legacy：首次部署没有 previous image
时拒绝伪造“上一 SHA”，按唯一安全顺序执行栅栏/排空新服务 → outbox reverse replay 到
唯一指定、实际承接回滚生产流量的隔离旧库克隆 → checksum → 原子切换旧服务 DSN 到同一克隆 →
验证连接身份 → 恢复旧路由。禁止用早期备份覆盖切流后新写入，也禁止演练库/实际连接库不一致。
不存在已验证旧服务时该分支为 not_applicable，不得计入 passed。

`scripts/verify-acceptance.py` 按 artifact role 交叉验证：A shadow/domain/recovery observation 绑定 A，
B promotion/shadow/final/domain/observation 绑定 B，由 promotion map 连接；不再要求所有运行证据使用
同一 `deployedSourceCommit`/RepoDigest。它对三轮报告逐轮执行 unique runId、canonical fixture、配置、
门槛和 93 项 parser 调用证据，不信任 `passed`/aggregate。迁移按 `chosenMigrationMode` 条件要求；当前
`final_full_no_binlog` 要求 final consistent full/import/checksum + table-scoped no-writer proof，并将
不适用的 delta/reverse 标为 `not_applicable`，绝不伪造相应位点。tracked final report 不得自含其当前
commit SHA：只记录最终证据提交的
`evidenceParentCommit` 与排除 final report 自身的 `evidencePayloadTreeSha256`；提交后由 CI 的
`GITHUB_SHA` 或本地 `git rev-parse HEAD` 外部传入 `verifiedEvidenceCommit`，验证 parent、payload hash
和 docs/artifacts-only diff，不得把治理提交冒充 A/B 镜像 commit。

验证器提供互斥模式：`--schema-of-present` 供普通 push 校验现有证据，`--require-complete` 只供 final
evidence push。两种模式都从原始 records/样本重算并拒绝仅凭 aggregate 或 `passed` 放行；complete
模式再按 rollbackMode 要求 applicable 分支通过并验证非适用分支原因；不得无条件要求两条分支都通过，
也不得用 isolatedCompatibilityRehearsal 代替真实 rollback。它还强制最终容器 diff、shadow/final DB
identity 隔离和全部必需证据。

`pull-and-up.txt` 虽为 `.txt` 后缀但采用 versioned JSON event ledger，按 A/B role 记录 A pull、A shadow
up、A final up、B pull、B shadow up 与 B final up；每个事件包含 sourceCommit、expected digest、实际
RepoDigest verify、Compose config hash、runtime digest、数据面 identity、时间和
`localBuild=false`/`localLoad=false`。Task 17 原子更新到 B shadow，Task 18 只在 B final runtime inspect
通过后原子补齐 B final up；verifier 拒绝缺事件、乱序、role/digest/attempt 不一致或任何本地 build/load。

所有可提交 `.json/.txt/.md` machine evidence 统一经 `scripts/write-evidence.py` 生成：同目录 0600 temp，
写入/flush、file fsync、原子 rename、directory fsync；内容至少含 `schemaVersion`、`passed`、run/role，
适用时含 attempt IDs。`.txt` 后缀不表示自由文本。失败优先原子写当前 run 的 passed=false tombstone；
若文件系统故障使 tombstone 也失败，verifier 依靠 run/attempt mismatch 拒绝旧 PASS。禁止 `printf >` 或
先 truncate 正式证据文件。

部署/回滚脚本使用 `set -Eeuo pipefail`，对 ERR/EXIT/HUP/INT/TERM 安装幂等 trap，并在任何 mutation 前
把 attempt `in_progress` 原子持久化。脚本启动或宿主恢复时若发现未关闭 attempt，必须先 runtime inspect、
立即 fence 可疑 writer，并按状态协调：A bootstrap 恢复原 502，B cutover 执行已演练 B→A；reconcile
写入当前 attempt 的终态且核对 route/data/running digest 前，不得开始新 attempt 或解除 trap。

- [ ] **步骤 5：验证脚本**

运行：

```bash
python3 -m pytest tests/baseline -q
python3 -m pytest tests/ops -q
shellcheck scripts/*.sh
git diff --check
```

预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add scripts tests/baseline tests/ops docs/runbook.md
git commit -m "ops: add guarded baseline deploy and rollback tooling"
```

## 任务 15：全量本地验证与代码审查

**文件：**
- 修改：由测试失败确定的精确实现文件
- 创建：`artifacts/verification/local-verification.md`（脱敏、无凭据和外部媒体，可提交）
- 创建：`artifacts/verification/secret-scan.txt`（versioned JSON，含 schemaVersion/passed/runId/version/scope，
  不含 finding/path/ref）
- 创建：`artifacts/verification/media-parser-integration.json`（Task 15 的 source-level hermetic 前置证据；
  Task 16 Actions 再生成绑定实际 digest/CI run 的部署证据）

`artifacts/verification/local-verification.md` 使用可机器解析的 YAML front matter，至少包含
`schemaVersion`、`passed`、source commit、命令/退出码和生成时间，正文只含脱敏摘要。生成器先写
同目录 0600 临时文件，flush + `fsync` 后原子 rename，失败删除临时文件且不得留下半份/旧 `passed`
报告。策略测试与 `verify-acceptance.py --schema-of-present` 校验 schema、原子生成标记和脱敏字段。

- [ ] **步骤 1：格式、vet、race 和全量测试**

```bash
test -z "$(gofmt -l .)"
GOMAXPROCS=2 GOMEMLIMIT=2GiB go vet ./...
GOMAXPROCS=2 GOMEMLIMIT=2GiB go test -race -p 2 ./... -count=1
python3 -m pytest tests/policy/test_python_bridge_security.py -q
python3 -m pytest tests/baseline tests/ops -q
FRONTEND_REPO=/srv/watermark scripts/verify-frontend-provenance.sh
for f in /srv/watermark/test/test_miniprogram_*.js; do node "$f"; done
FRONTEND_REPO=/srv/watermark scripts/verify-frontend-provenance.sh
```

另显式运行 media-parser focused suite，避免宽泛 `./...` 因测试被改名、skip 或 owner 漂移而假绿：
suite manifest 至少逐名包含 `TestStructuredJSONGoldenMatrix`、`TestMediaCandidateOrderIsStable`、
`TestCanonicalURLQueryPolicyMatrix`、cache negative/version 测试和 `TestDASHCandidateOrderAndFallbackBudget`。

```bash
go test ./internal/parser/... ./internal/parse ./internal/cache ./internal/media ./tests/contracts \
  -run 'Test(Registry|StructuredJSON|MediaCandidate|CanonicalURL|CacheKey|NegativeCache|NormalizeLivePhoto|DASH)' \
  -count=1
go test ./internal/policy -run 'TestMediaParser' -count=1
```

通过后以统一 atomic writer 生成 `mediaParserIntegration` evidence，包含 implementation `sourceCommit`、
test manifest/hash 和每个子门的原始命令/退出码；Task 15 尚未发布镜像，因此明确记录
`imageDigest=notApplicablePreBuild`、`ciRunId=notApplicableLocal`，不得伪造 registry digest。Task 16
可信 Actions 在精确 tested digest 上重跑同一套件并生成同 schema 的 actual `imageDigest`/`ciRunId` 证据，
后续 Task 17/18 只能使用这一部署级版本。

预期：全部退出 0。这里只运行 hermetic/in-process 测试；需要活服务的 `tests/e2e` 已由 Actions runner
执行，并将在任务 17 对已拉 GHCR 镜像重跑，Task 15 不在目标宿主启动依赖或服务。

- [ ] **步骤 2：Compose、secret 和差异检查**

```bash
RECOVERY_IMAGE=ghcr.io/1136623363/watermark-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  CANDIDATE_IMAGE=ghcr.io/1136623363/watermark-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  CANDIDATE_STAGE=disabled RECOVERY_DATA_STAGE=final \
  RECOVERY_API_HOST_PORT=5001 CANDIDATE_API_HOST_PORT=15001 \
  DEPLOYMENT_RUN_ID=local-verification RECOVERY_GATE_ATTEMPT_ID=recovery-local RECOVERY_GATE_MODE=apply \
  CANDIDATE_GATE_ATTEMPT_ID=candidate-disabled-local \
  CANDIDATE_GATE_MODE=apply RECOVERY_MIGRATION_SOURCE_DSN=redacted-source \
  RECOVERY_MIGRATION_TARGET_DSN=redacted-migrator RECOVERY_MYSQL_DSN=redacted-target \
  RECOVERY_REDIS_ADDR=redis:6379 RECOVERY_REDIS_NAMESPACE=recovery-final \
  CANDIDATE_MIGRATION_SOURCE_DSN=disabled \
  CANDIDATE_MIGRATION_TARGET_DSN=disabled CANDIDATE_MYSQL_DSN=disabled CANDIDATE_REDIS_ADDR=disabled \
  CANDIDATE_REDIS_NAMESPACE=disabled \
  MYSQL_IMAGE=mysql@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  REDIS_IMAGE=redis@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc \
  MARIADB_RECOVERY_IMAGE=mariadb@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd \
  SOURCE_MARIADB_DEFAULTS_FILE=/tmp/source.cnf MARIADB_CLONE_DEFAULTS_FILE=/tmp/clone.cnf \
  MYSQL_PASSWORD=x MYSQL_ROOT_PASSWORD=x MARIADB_CLONE_ROOT_PASSWORD=x \
  ADMIN_PASSWORD=x ADMIN_SESSION_SECRET=x DOWNLOAD_TOKEN_SECRET=x \
  WECHAT_MINI_APP_ID=x WECHAT_MINI_APP_SECRET=x \
  docker compose -f deploy/compose.yml config --quiet
go test ./internal/policy -count=1
scan_status="$(scripts/verify-gitleaks.sh)"
test "$scan_status" = PASS
python3 scripts/write-evidence.py --output artifacts/verification/secret-scan.txt \
  --schema-version 1 --passed true --run-id "$LOCAL_VERIFICATION_RUN_ID" \
  --field version=v8.30.1 --field scope=all-refs-history
git diff --check
git status --short --branch
```

预期：Compose PASS；policy scanner 覆盖 tracked worktree、index、全部 refs、commit/tag message、
指向非 commit 对象的 refs 与全部可达 commits，Gitleaks `v8.30.1` 也对完整历史退出 0；两者都不
回显敏感字面量、路径或 ref 名。工作树只含预期 artifact。`reports/` 可保留本地临时输出，但永不 `git add`。

- [ ] **步骤 3：请求代码审查**

并行审查 API 兼容、安全/SSRF、数据/任务、Docker/CI，以及外部研究/license 边界。确认 parser constructor
零 I/O、registry metadata/fixtures、FetchURL/SafeURL/CacheKey、typed error/cache、富媒体兼容投影均已按
计划实现，且没有研究项目的 Flask 协议、网络/密钥反模式或自动同步入口。每项 must-fix 先写失败测试并确认红灯，再做
最小实现转绿；审查记录列出精确 implementation/test 路径。修复提交禁止 `git add -A`、目录级盲加或
夹带 evidence，只按审查表逐个 `git add --` 精确实现与测试文件，确认 cached diff 后提交：

```bash
git diff --name-only
# 依据已审查清单逐条 git add -- 精确 implementation/test 路径
go test ./internal/policy -count=1
git diff --cached --check
git diff --cached --name-only
git commit -m "fix: address pre-release review findings"
test "$(scripts/verify-gitleaks.sh)" = PASS
```

该修复 commit 后必须重跑步骤 1–2；若又有 must-fix，重复红/绿与精确提交，直到代码审查无阻塞项。
只有最新实现 commit 的全套验证通过，才允许生成下面的独立 evidence commit。

- [ ] **步骤 4：Commit**

```bash
git add artifacts/verification/local-verification.md artifacts/verification/secret-scan.txt \
  artifacts/verification/media-parser-integration.json
go test ./internal/policy -count=1
git diff --cached --check
git diff --cached --name-only
git commit -m "test: record local verification evidence"
test "$(scripts/verify-gitleaks.sh)" = PASS
```

此独立 evidence commit 只含三份脱敏验证 artifact；任何 must-fix 实现或测试若仍未提交，必须返回步骤 3，
不得借 evidence commit 带入。

## 任务 16：创建 GitHub 仓库并发布两阶段中的 recovery 候选 A

**文件：**
- 修改：Git remote（仓库外状态）
- 读取：GitHub Actions run、GHCR package/digest
- 创建：`artifacts/release/full-history-secret-scan.txt`（versioned JSON，含 schemaVersion/passed/runId/
  sourceCommit/version/scope）
- 创建：`artifacts/release/repository-and-image.txt`
- 创建：`artifacts/release/image-digest.txt`
- 创建：`artifacts/release/recovery-image-digest.txt`
- 创建：`artifacts/release/final-image-digest.txt`（任务 17 的 promotion B 发布后填写）
- 创建：`artifacts/release/promotion-equivalence.json`（任务 17 比较 A/B 后填写）
- 创建：`artifacts/release/sbom.spdx.json`
- 创建：`artifacts/release/sbom-recovery.spdx.json`
- 创建：`artifacts/release/sbom-final.spdx.json`（任务 17 B 发布后填写）

所有 `.txt` release/status 文件实际采用 versioned machine-readable JSON，并统一原子 writer：至少含
`schemaVersion`、`passed`、runId、role、sourceCommit、digest/attestation 状态和生成时间；未验证字段用
明确 state 而非空字符串。`repository-and-image.txt` 是 A/B role ledger，recovery/final/image-digest
分别标记 candidate/verified 与角色，不能用自由文本或旧 PASS 覆盖当前 run。

- [ ] **步骤 1：通过 GitHub API 创建公开仓库**

使用当前资料提供且验证有效的 GitHub token 作为仅进程环境 `GH_TOKEN`，以 `gh api` 创建
`1136623363/watermark-go` 并关闭自动初始化；禁止 `gh auth login`、curl `-H "$TOKEN"`、token URL 或把
token 放入 argv/config/日志。`GH_TOKEN` 不落盘，命令结束 unset。

- [ ] **步骤 2：设置安全 remote 并推送**

```bash
test "$(git rev-list --max-parents=0 --all | wc -l)" -eq 1
go test ./internal/policy -count=1
scan_status="$(scripts/verify-gitleaks.sh)"
test "$scan_status" = PASS
python3 scripts/write-evidence.py --output artifacts/release/full-history-secret-scan.txt \
  --schema-version 1 --passed true --run-id "$RELEASE_RUN_ID" --source-commit "$(git rev-parse HEAD)" \
  --field version=v8.30.1 --field scope=all-refs-history
git remote add origin https://github.com/1136623363/watermark-go.git
askpass_dir="$(mktemp -d)"
chmod 700 "$askpass_dir"
askpass="$askpass_dir/git-askpass"
printf '%s\n' '#!/bin/sh' \
  'case "$1" in *Username*) printf "%s\\n" "x-access-token";; *Password*) printf "%s\\n" "$GH_TOKEN";; *) exit 1;; esac' \
  >"$askpass"
chmod 700 "$askpass"
cleanup_push_auth() { rm -rf -- "$askpass_dir"; unset GH_TOKEN GIT_ASKPASS GIT_TERMINAL_PROMPT; }
trap cleanup_push_auth EXIT HUP INT TERM
GIT_ASKPASS="$askpass" GIT_TERMINAL_PROMPT=0 \
  git -c credential.helper= push -u origin main
cleanup_push_auth
trap - EXIT HUP INT TERM
```

本任务前置要求任务 1 步骤 7 已把批准树重建为单一无 parent 根并完成 GC/full scan；若仍是审查前旧根
则不得 push。本任务再用 policy object scanner 与
固定 `v8.30.1` Gitleaks 重新扫描 `HEAD`、index、全部 heads/tags、annotated tag message、非 commit
ref 及所有即将推送的 refs。扫描器只能输出脱敏摘要，不能用 `for-each-ref` 把潜在敏感 ref 名直接
打印到终端。全部退出 0 后才添加新仓库 `origin`。不得添加、恢复或 fetch 旧仓库 remote。预期：
push 成功；本地 remote 仅有 `origin` 指向新仓库。
ASKPASS helper 只读取进程环境且自身不含 token，0700 临时目录在成功/失败/signal 时清理；remote 必须始终
是无 credential HTTPS URL，禁止长期 credential helper 或让 `GH_TOKEN` 留在后续步骤环境。

- [ ] **步骤 3：轮询 Actions**

通过 GitHub API 等待 recovery 候选 A 的 source commit 对应 `ci-image` conclusion 为 `success`；失败则
读取 job 日志、修复、本地验证、commit 和再次 push，不能跳过失败步骤。A 此时只是候选，未经过真实
域名、真微信与 30 分钟观察前不得称为 previous/recovery。

- [ ] **步骤 4：记录不可变镜像证据**

把 A 的完整 40 位 `sha-${GITHUB_SHA}` 标签、manifest digest、workflow run URL、SBOM/attestation 和
扫描结果写入 `artifacts/release/`。`repository-and-image.txt` 记录新仓库 URL、唯一根、完整 source
commit、workflow URL、tag、RepoDigest 与 PASS；`recovery-image-digest.txt` 先记录 `candidate=true`，
只有任务 17 的 A 真实域名全矩阵和 `A observation>=1800s` 通过后才原子更新为 verified recoveryDigest。
`sbom-recovery.spdx.json` 固定 A 的 SBOM；`image-digest.txt` 最终只指向任务 17 后续发布并验证的 B
final digest。文件不得含 token；部署不使用
`latest` 或裸 SHA tag。

A 验证前禁止发布 B。任务 17 把 A 提升为 verified recovery 并生成 marker 绑定的 immutable recovery
evidence ref 后，才允许创建 tracked diff 只改精确路径 `release/promotion-marker.txt` 的 promotion commit
并由 Actions 构建 B；OCI revision 仅是 CI 自动生成的 label，不是 source diff。B 的 source/digest/
attestation、A/B 等价性和最终
reference 分别写入 `final-image-digest.txt`、`promotion-equivalence.json` 与 `image-digest.txt`。
`repository-and-image.txt` 按 A/B role 保存两组 commit/digest/workflow/attestation；B 的 SBOM 写入
`sbom-final.spdx.json`，canonical `sbom.spdx.json` 最终明确指向 B role，禁止把 A 证据冒充 B。

## 任务 17：在 192.168.31.222 完成 A→B 两阶段隔离验收并建立 recovery

**文件：**
- 服务器运行目录：`/var/lib/watermark-go`
- 创建：`artifacts/deploy/host-before.json`
- 创建：`artifacts/deploy/state-before.json`
- 创建：`artifacts/deploy/running-digest.txt`
- 创建：`artifacts/deploy/rollback-drill.txt`
- 创建：`artifacts/deploy/pull-and-up.txt`
- 创建：`artifacts/deploy/before-after-containers.json`
- 创建：`artifacts/migration/legacy-data-rehearsal.json`
- 创建：`artifacts/acceptance/shadow-e2e.json`
- 创建：`artifacts/acceptance/final-shadow-e2e.json`
- 创建：`artifacts/acceptance/media-parser-integration.json`（可信 CI artifact 绑定 B source/digest/ciRunId，
  Task 18 继续复验/补充 live-or-hermetic capability disposition）
- 创建：`artifacts/acceptance/admin-and-baseline.json`
- 创建：`artifacts/acceptance/redis-degraded.json`
- 创建：`artifacts/acceptance/recovery-domain-e2e.json`
- 创建：`artifacts/deploy/recovery-observation-30m.json`
- 创建：`artifacts/benchmark/run-{1,2,3}.json`
- 修改：`artifacts/release/recovery-image-digest.txt`
- 修改：`artifacts/release/final-image-digest.txt`
- 修改：`artifacts/release/image-digest.txt`
- 修改：`artifacts/release/promotion-equivalence.json`
- 修改：`artifacts/release/repository-and-image.txt`
- 修改：`artifacts/release/full-history-secret-scan.txt`
- 修改：`artifacts/release/sbom.spdx.json`
- 修改：`artifacts/release/sbom-final.spdx.json`

deploy/rollback `.txt` 同样是含 `schemaVersion`/`passed`/runId/role/identity 的 JSON，不是自由文本；
Task 18 相关 status 还必须绑定 `deploymentRunId`/`cutoverAttemptId`。所有阶段更新均走统一原子 writer。

- [ ] **步骤 1：重新 discovery 并保存宿主机/路由权威快照**

先机器复验本轮实机事实，而不是沿用旧计划假设：当前预期 `oldServicePresent=false`，没有运行或停止的
watermark 容器、进程、systemd unit 或本地应用镜像，5001/15001 均空闲，
`watermark.bxsn.cn 当前 502`（记录为 `baselineHTTPS=502`），本机数据源为 MariaDB 11.8.6，另有 Redis。
`host-before.json` 明确记录 `oldServicePresent`、`oldServiceIdentity`、`routeIdentity`、
`dbWriterIdentity`、engine/version/capabilities、端口与基线 HTTPS；与预期不一致立即停下并重新选择状态
机分支，不能把 absence 当作已验证旧服务。

当前 MariaDB 能力事实为 `@@log_bin=0`、`binlog_format=MIXED`、`gtid_strict_mode=0`，因此 actual absent
分支固定 `chosenMigrationMode=final_full_no_binlog`，禁止声称可用 GTID/delta。对 watermark 相关 legacy
schema/table 使用 table-scoped fence 与连接 identity/重复 hash 证明无 writer，禁止全实例
read lock/read_only 影响其他数据库；发现任何新 writer、连接身份变化或重复 hash 变化都 fail closed。

Tunnel 权威只能来自运行 tunnel/dashboard 的脱敏 route identity，或受控凭据下安全 API 查询，再以
真实 HTTPS 探测交叉验证；`/etc/cloudflared/config.yml 不是权威`，因为 host 文件未必挂载到 token
tunnel。不得打印 token，也不编辑/重建 token tunnel，不用 host file hash 伪造路由快照。

默认操作面只允许新 Compose project `watermark-go`。cutover/rollback 若确需额外操作旧对象，只允许
`host-before 精确 identity/hash allowlist` 中已验证的旧 watermark service/route；identity 不存在则
fail closed，绝不碰无关容器、进程、systemd、镜像、网络、卷或路由。当前 absent 分支不得声称可执行
旧服务栅栏、旧服务 DSN 切换或 legacy recovery。

同时记录 load、CPU、MemAvailable、swap `si/so`、memory/io PSI、磁盘/inode、OOM、I/O 错误和
`docker ps` 脱敏集合。`before-after-containers.json` 此时只原子写 before 集合/状态/hash；最终 after/diff
留到任务 18 观察结束，静态 swap 占用不单独误停。

- [ ] **步骤 2：用固定 MariaDB 工具备份、隔离恢复并选择迁移模式**

在任何候选写操作前，用 Compose `migration-tools` profile 拉取并运行 `deploy/image-lock.json` 中官方
digest 的 MariaDB recovery image；禁止裸 docker run、裸 tag、本地 build。凭据放 0700 临时目录内
0600 `--defaults-extra-file`，只传给兼容 MariaDB 11.8.6 的工具，EXIT/signal trap 删除；备份本体在仓库
外 0600 保存，artifact 只记录 hash/schema/count/checksum，不记录行内容或凭据。

先把备份恢复到隔离 MariaDB clone 并核对，再由 typed importer 映射到 disposable MySQL 8.4 clone，
验证字符集、默认值、时间/JSON/数值语义、schema version、行数与稳定字段 checksum。artifact 记录
engine/version/capabilities 和 `chosenMigrationMode=final_full_no_binlog`。由于 log_bin/GTID 不可用，
shadow 数据可来自早期 full 仅用于测试；首次或已证明前次零接受写入时，final production DB 必须在
table-scoped fence、连接身份和重复 hash 证明无 writer后重新生成一致性 full snapshot/import/checksum；
若已有失败 attempt 的 retained fact source，则禁止重做 full，必须按步骤 7 reconcile/reuse。禁止全实例锁或 read_only，
也禁止用 `updated_at` 伪造 delta。任何新 writer、hash 漂移或转换差异都停止。

- [ ] **步骤 3：只拉取并验证 recovery 候选 A**

创建 0700 临时 `DOCKER_CONFIG` 并注册 EXIT/signal trap；token 只经 stdin 传给 `docker login`。
`RECOVERY_IMAGE` 此时只能读取 `recovery-image-digest.txt` 的 A candidate digest；因 Compose inactive
profile 也插值，`CANDIDATE_IMAGE` 暂时精确等于 A 且 `CANDIDATE_STAGE=disabled`，candidate services 禁止
启动，不能提前使用尚未生成的 final `image-digest.txt`。Compose pull 后用
`scripts/verify-image.sh` 和 runtime inspect 验证实际
RepoDigest、attestation subject、OCI revision/source 与 A 的完整 commit 一致；随后 logout 并删除临时
目录。不得 build/load，终端与 artifact 不输出 token；pull、完整 commit/digest 与验证结果写入
`artifacts/deploy/pull-and-up.txt` 的 A pull event，明确 `localBuild=false`、`localLoad=false`；后续步骤按
同一 event ledger 原子补齐角色 up 事件。

- [ ] **步骤 4：建立互不污染的数据面，先完成 gate 再启动任何 API/worker**

Compose profiles 分别创建 shadow 的独立 DB/schema、独立 Redis namespace/卷，以及 final production
DB/Redis。记录并强制 `shadow DB identity != final DB identity`；rollback rehearsal 另用隔离 DB clone 和
隔离 old-service clone。优先独立 schema/实例，不采用清理 shadow 写入的脆弱方案。shadow E2E/admin/
三轮基准/pending/outbox 全部只写 shadow 数据面；每条 shadow outbox 不得进入 production outbox。

先对 shadow DB 执行 migration 两次、导入测试用 full snapshot、scrub 并重复 checksum；只有 gate
`passed=true` 后才允许启动 shadow API listener/worker。`final production DB` 只接收 initial+delta
（源能力可靠时）或当前 `chosenMigrationMode=final_full_no_binlog` 的全新 production full import，以及
scrub 后的生产数据；任何 API/listener/worker 启动前必须先完成 final migration+import+checksum gate。
final checksum 无本轮 shadow/A-B acceptance 的 runId/taskId/sentinel/outbox；
legacy snapshot 中原有合法历史（包括 platform_runs/测试记录）按 source checksum 保留，不把历史事实误删。
artifact 记录两套 DB/Redis identity、导入来源 hash 和 outbox namespace；
`verify-acceptance.py 强制证明`上述隔离关系。
“全新 production full”只适用于尚无 retained predecessor，或前次 attempt 已由 route/access log、DB 与
outbox 三方共同证明 `acceptedWrites=0` 的零写重试；一旦失败 attempt 接受过写入，必须走步骤 7 的
retained fact source reconciliation，禁止重新 full 覆盖。

- [ ] **步骤 5：在 A shadow 上跑服务 E2E、后台、恢复和三轮基准**

仓库外 `/var/lib/watermark-go/runtime.env` 先设 `RECOVERY_API_HOST_PORT=15001` 并绑定 recovery role 的
shadow DSN/Redis namespace；production preflight 只报字段分类。对
`http://127.0.0.1:15001` 运行完整服务契约、后台登录/RBAC/CSRF、诊断、Redis 降级恢复、重启后的
completed/pending/outbox 恢复，以及三轮 93 样本基准。production 微信在 shadow 只测 preflight 和无效
code 安全失败，不能冒充真实绑定。

三轮各有 unique runId、固定 canonical hash、`concurrency=3`、native/fallback/cacheBypass、93 个唯一
sampleKey 与本轮 parserInvocationId，原始 records 重算门槛且不复用。`shadow-e2e.json`、
`admin-and-baseline.json`、`redis-degraded.json` 和三轮报告均含 `schemaVersion`/`passed`、A commit/digest、
shadow DB/Redis identity、requestId 与脱敏用例结果；生成时写同目录 0600 临时文件，flush + `fsync`
后原子 rename，失败不保留半份证据。若需 LAN，只启用防火墙来源 allowlist 的临时受限 LAN override，
结束立即恢复 localhost-only。
所有 shadow/recovery/final E2E 统一禁止保存 share/task capability ID、ticket、原始媒体/分享 URL 或完整
path/query；只记 route template、same-origin/HTTPS、状态、字节数/内容 hash、requestId，capability 省略
或不可逆 hash。`tests/ops` 用 sentinel 证明脱敏器拒绝原值。

A shadow runtime inspect 通过时，原子追加 A shadow up event，记录实际 RepoDigest、Compose config hash、
shadow DB/Redis identity 与 runtime digest；不得只凭 `docker compose up` 退出码标记成功。

- [ ] **步骤 6：仅在隔离 old-service/DB/影子路由完成 initial 分支演练**

Task 17 的 legacy/initial rollback rehearsal 只能操作隔离 old-service clone、隔离 DB clone 与影子路由；
不得修改在线旧服务 DSN、不得切换在线写流，实际切换只允许任务 18 的受控状态机（A bootstrap 除外，
其失败规则见下一步）。在隔离环境创建 pending/outbox/已完成结果，演练 fence/drain、reverse/checksum、
连接 identity 与路由恢复。当前 absent 模式必须记录
`branches.initialDeployment.applicable=false`、`result=not_applicable_no_verified_legacy_service`；另一分支
隔离等价演练只能单列为 `isolatedCompatibilityRehearsal`，不得计入实际 rollback/pass，也不得伪造真实
旧服务或 legacy reverse 成功。

- [ ] **步骤 7：以干净 final DB 把 A bootstrap 到真实域名并建立 recovery**

重新执行 table-scoped fence/无 writer 证明，创建 final consistent full snapshot，经 typed importer 写入
全新 final DB；migration+import+scrub+checksum 全部通过且 final checksum 证明无本轮 shadow/A-B
acceptance 的 runId/taskId/sentinel/outbox 后，才允许
启动 A final listener/worker。runtime file 改为 `RECOVERY_API_HOST_PORT=5001`。
切流前确认已登录 DevTools/真机和一次性 wx.login readiness；route 只根据运行 tunnel/dashboard 或安全
API identity 更新/验证，不编辑 token tunnel。
readiness code 只证明外部前置、不得保存或复用；A 真实矩阵开始 session 前必须再次调用 `wx.login`
取得全新一次性 code，交换后立即丢弃，artifact 不记录 code/openid/token/session。

以 `rollbackMode=absent_two_stage` bootstrap A。A 必须先完成 A shadow 隔离全验，再在 A 真实域名通过
真微信、固定前端全矩阵和 `A observation>=1800s`；只有这些原始证据通过，A 才成为 recoveryDigest。
若 A 首上任一步失败，立即 fence/drain A 新写并撤销本次 route 变更、恢复原 502 路由，保留 final DB/
outbox 并标记 `FAILED`。只要 route/access log/DB/outbox 任一证明接受过写入，该确切 final DB/outbox
立即成为 retained fact source，不是可丢弃的调查副本；失败 evidence 固定
`predecessorDBIdentity`、accepted-write coordinate/count、稳定 checksum、outbox high-water mark 和
`retryDisposition=pending_reconcile`。下一次 A/A' attempt 必须先在 fence 下 reconcile pending/running/
outbox/idempotency 并复用该 DB，或用可证明无丢失的 forward-only migration/replay 到唯一 successor，
记录 predecessor→successor checksum；禁止重新 full 覆盖、丢弃卷或回放早期 snapshot。只有 DB、
outbox、route 三方共同证明 `acceptedWrites=0` 才允许 `retryDisposition=rebuild_zero_writes` 新建 final DB。
verifier 拒绝缺 predecessor identity/坐标、写入后 rebuild、未完成 reconciliation 或 successor checksum
不一致。任务未完成，不得声称 5 分钟健康回滚或已有 recovery。

真实前端证据写 `recovery-domain-e2e.json`，其 cases schema 与 Task 18 完全相同，逐项包含 session、
syncParse、asyncSubmit/asyncPoll、cacheRestore、performance、fallbackCreate/fallbackPoll/fallbackDownload、
m3u8Create/m3u8Poll/m3u8Download、video、gallery、requestId/passed。观察证据写
`recovery-observation-30m.json`；两者使用原子/脱敏/schema/passed 契约。A observation 的 60 个原始
health 样本必须请求真实 `https://watermark.bxsn.cn/api/health` 并绑定 A digest（可附 localhost 对照），
首末 >=1800 秒并重算 P95/资源停止线，不能以 localhost 观察冒充公网可用。
通过后原子更新 `recovery-image-digest.txt` 为 `verified=true`。
同时只有 A final runtime inspect 与真实域名启动检查通过后，才原子追加 A final up event；失败回到 502
时 ledger 记录失败结果，不能留下 A 正在成功运行的假状态。

- [ ] **步骤 8：Actions 只做等价 promotion 生成并隔离验收 B**

A 成为 recovery 后才创建 B。先由唯一 `scripts/promote.sh` 对 A shadow/domain/真微信/30m/benchmark/
migration/recovery digest 全部运行 `verify-acceptance.py --require-recovery-ready`，生成一个 parent 为 A source
commit、diff 只含脱敏 A evidence 的 immutable `evidence/recovery-<runId>` tag/ref；再让 marker 绑定该
evidence commit OID、排除自身的 evidence payload hash、A digest/source commit 与 promotionRunId。marker
commit 的 parent 必须仍精确为 A source commit，tracked A→B source diff allowlist **唯一**允许
`release/promotion-marker.txt`；OCI revision 是 CI 根据 B commit 自动写入 OCI config 的 label 差异，不是
source path/diff。禁止 Go/依赖/Dockerfile/执行/config/migration/schema 或其他 tracked 文件变化。

marker push 的 image job fetch immutable evidence ref，复跑 `--require-recovery-ready`、复算 payload hash/
A digest/parent/diff allowlist，并拒绝空 marker、手工 marker、可变/缺失 evidence ref 或尚未 verified 的 A；
evidence ref push 本身只跑 scan/verifier且不构建镜像。Actions 从 B 完整 source commit 构建不同 digest，
要求 `recoveryDigest != finalDigest`。机器比较 A/B 的 rootfs/app binary/tool versions/schema，除
`org.opencontainers.image.revision` 等仅 OCI label 白名单差异外必须逐字节/哈希一致，否则 B 不得称为
等价 promotion。

只 pull B digest 并验证 attestation/OCI source binding，原子追加 B pull event；随后执行 B shadow 隔离全验，
复用全新 shadow DB clone 而不是 final 数据面。B shadow 证据固定写入
`artifacts/acceptance/final-shadow-e2e.json`，包含 schemaVersion/passed、B commit/digest、实际 RepoDigest、
shadow DB/Redis identity、完整用例与脱敏 requestId；runtime inspect 通过才追加 B shadow up event。
`promotion-equivalence.json` 记录 source diff、rootfs/app/tool/schema hash；`final-image-digest.txt` 记录 B
commit/digest。B promotion push 前执行 fixed Gitleaks full-history
门禁并重跑 policy，更新 `full-history-secret-scan.txt` 使 scope 覆盖 B commit；再把
`repository-and-image.txt` 的 B role 补齐 workflow/digest/attestation，生成 `sbom-final.spdx.json` 并让
canonical `sbom.spdx.json` 明确 final B role。只有 B attestation、等价性、扫描和 B shadow 全部通过后，
才原子生成 canonical `artifacts/release/image-digest.txt`，标记 verified B commit/RepoDigest 并与
`final-image-digest.txt` 对账。任何一步失败都保留 A 运行、不得更新 canonical final reference。

- [ ] **步骤 9：真实演练 B→A 并锁定 Task 18 状态**

在 A 已稳定、final DB 已验证同时兼容两镜像后，先生成唯一 `drillRunId`，把 candidate stage 设为
`drill`，以只读 DSN 和 `GATE_MODE=revalidate` 分别 `--force-recreate --no-deps`
`data-gate-recovery`/`data-gate-candidate`。两份新 receipt 必须绑定本 drill、A/B digest、同一 final DB/
Redis namespace、schema checksum/config hash 和当前 accepted-write/outbox coordinate；禁止 DDL/import/
scrub。只有两份 receipt 与 runtime inspect 均通过，才短暂把 B 接到同一兼容 final DB 和真实 route，立刻
实跑 B→A 真实 drill；这是 actual absent 模式的当前适用分支真实演练。要求
`durationSeconds<=300`、`healthPassed=true`、`dataPassed=true`、route/digest/DB identity 全部通过，
结束时恢复 A。统一 `rollback-drill.txt` 虽为 `.txt` 后缀但内容是 JSON：`schemaVersion`、`passed`、
`rollbackMode`、`branches.previousDigest`、`branches.initialDeployment` 与独立
`isolatedCompatibilityRehearsal`。verifier 按 mode 校验 applicable 分支：previousDigest 含
durationSeconds、healthPassed、dataPassed、routePassed、DB identity、坐标和 result；absent 下 initial
明确 not-applicable，不能让隔离实验抬高顶层 `passed`。

`state-before.json` 锁定 A/B digest+attestation+config/DB identity，明确
`schemaCompatibleWithRecovery=true`、`schemaCompatibleWithFinal=true`。在
`rollbackMode=absent_two_stage` 下 verifier 要求 A domain+30m、A/B 等价证据和 B→A 真实 drill，
不要求或伪造 legacy reverse；legacy reverse 分支仅当 discovery 为 `oldServicePresent=true` 且旧服务、
route、writer identity 均验证时条件启用。此时 `running-digest.txt` 仍记录 A，Task 18 才最终切 B。

## 任务 18：把等价 promotion B 切为 final、自动回退 A 并完成最终观察

**文件：**
- 修改：`/var/lib/watermark-go/runtime.env`（服务器本地、仓库外、0600、不跟踪）
- 创建：`artifacts/deploy/public-cutover.json`
- 创建：`artifacts/deploy/observation-30m.json`
- 创建：`artifacts/acceptance/frontend-domain-e2e.json`
- 创建：`artifacts/acceptance/media-parser-integration.json`（可信 CI hermetic 结果绑定 B source/digest/run，
  合法稳定样本存在时才附加 live audio/Live Photo 结果）
- 创建：`artifacts/acceptance/final-acceptance.md`
- 修改：`artifacts/migration/legacy-data-rehearsal.json`（mode-specific final full/import/checksum/no-writer）
- 修改：`artifacts/deploy/before-after-containers.json`（最终 after/diff）
- 修改：`artifacts/deploy/state-before.json`、`artifacts/deploy/running-digest.txt`
- 修改：`docs/requirements-traceability.md`、设计与本计划完成状态

- [ ] **步骤 1：复验 A recovery、B 等价性、运行 route 与外部前置**

要求 `state-before.json` 已锁定 verified A recovery、B final candidate、两者 attestation/config/final DB
identity、A/B 等价证据和通过的 B→A drill；任一缺失都不得切 B。重新从运行 tunnel/dashboard 脱敏
route identity或安全 API 查询取得权威路由，并用真实 HTTPS 探测交叉验证；不读取
`/etc/cloudflared/config.yml` 作为权威，不编辑/重建 token tunnel。

本次切换开始时生成唯一 `deploymentRunId` 与 `cutoverAttemptId`，在任何 fence 前原子更新
`state-before.json`；后续 `public-cutover.json`、B `frontend-domain-e2e.json`、B
`observation-30m.json`、`final-acceptance.md`、`running-digest.txt` 和 B final up event 必须使用完全相同
的两个 ID 与时间窗。Task 18 只允许读取 Task 17 已原子标记 verified 的 canonical
`artifacts/release/image-digest.txt` 作为 B digest，并与 final-image-digest/attestation/state 对账；不得
从 tag、未验证 candidate 或手工环境值选择 B。
同时以统一原子 writer 把本次 attempt 写成 `in_progress`，再安装 `set -Eeuo pipefail` 下覆盖
ERR/EXIT/HUP/INT/TERM 的幂等 trap。若启动时发现遗留未关闭 attempt，必须先 runtime inspect、fence
可疑 writer，并按 A bootstrap→原 502 或 B cutover→A 进行 reconcile；route/data/running digest 与终态
证据未通过前，不得开始新 attempt 或解除 trap。

仍在 fence 前，以当前 Task 18 `deploymentRunId`、final DB identity 和只读凭据分别强制重建两次
one-shot gate：A recovery 使用 `GATE_MODE=revalidate` 生成 **rollback receipt**，B candidate 使用
`GATE_MODE=revalidate` 生成 **final receipt**。两份 receipt 分别绑定 A/B digest、各自 config hash、同一
final schema checksum/Redis namespace、当前 accepted-write/outbox coordinate 和本 attempt；任一旧 Task17
receipt、apply mode、DDL/import/scrub 调用、DB identity 不同或 schema 漂移都禁止进入 fence。state 原子
锁定这两份 receipt hash。B 接受业务写入后 coordinate 可单调前进，但 schema/config/data identity 不得
变化；B→A 时做有界只读 quick-check，要求 current coordinate 不回退且 schema/identity 仍匹配，即可消费
预生成 A receipt 而不重跑 full gate，保证 300 秒目标。意外 schema/identity 漂移使 receipt 失效并进入
受控只读 FAILED，不能盲启 A。

```bash
docker compose --env-file /var/lib/watermark-go/runtime.env -f deploy/compose.yml --profile recovery \
  up --force-recreate --no-deps --abort-on-container-exit \
  --exit-code-from data-gate-recovery data-gate-recovery
docker compose --env-file /var/lib/watermark-go/runtime.env -f deploy/compose.yml --profile candidate \
  up --force-recreate --no-deps --abort-on-container-exit \
  --exit-code-from data-gate-candidate data-gate-candidate
```

这两条只运行已从 GHCR 拉取的 one-shot role，不构建镜像；脚本在执行前验证 Compose 无 `build:`，并在
每条后核对新 container start identity/exit/receipt，禁止复用 exited container。

再次执行 host discovery 和 `host-before 精确 identity/hash allowlist` 比对，确认 A、final DB、route 与
无关对象未漂移；默认仍只操作 `watermark-go`。采集 MemAvailable、swap si/so、memory/io PSI、OOM、
磁盘/inode、端口与无关容器 hash，任一停止线触发都不切流。

同时在写栅栏/切流前完成一次性 wx.login code readiness：确认已登录 DevTools/真机在线、目标小程序
环境可调用 `wx.login` 并能取得新的单次 code。这里只记录 readiness 布尔值、设备类型和时间，不记录、
持久化或回显 code；正式交换在步骤 4 再获取新 code。未就绪不得进入写栅栏或切流。运行配置必须从
仓库外固定路径加载、验证权限并明确 final bind：

```bash
test "$(stat -c '%a' /var/lib/watermark-go/runtime.env)" = 600
test "$(grep -Fx 'RECOVERY_API_HOST_PORT=5001' /var/lib/watermark-go/runtime.env)" = 'RECOVERY_API_HOST_PORT=5001'
test "$(grep -Fx 'CANDIDATE_API_HOST_PORT=5001' /var/lib/watermark-go/runtime.env)" = 'CANDIDATE_API_HOST_PORT=5001'
test "$(grep -Fx 'CANDIDATE_STAGE=final' /var/lib/watermark-go/runtime.env)" = 'CANDIDATE_STAGE=final'
test "$(grep -Fx 'RECOVERY_GATE_MODE=revalidate' /var/lib/watermark-go/runtime.env)" = 'RECOVERY_GATE_MODE=revalidate'
test "$(grep -Fx 'CANDIDATE_GATE_MODE=revalidate' /var/lib/watermark-go/runtime.env)" = 'CANDIDATE_GATE_MODE=revalidate'
docker compose --env-file /var/lib/watermark-go/runtime.env -f deploy/compose.yml config --quiet
```

最终 runtime 的 candidate role 只允许隐式固定 bind `127.0.0.1` 与
`CANDIDATE_API_HOST_PORT=5001`；recovery role 保留同一 5001 配置但必须停止，二者不能同时占用端口。
LAN 调试只能在 Task 17 使用
临时受限 LAN override，Task 18 禁止启用 LAN override。

- [ ] **步骤 2：fence A 后把 B 切到同一兼容 final DB**

actual `rollbackMode=absent_two_stage` 不存在可栅栏的 legacy 服务。先对正在运行的 A 启用短写 fence、
排空 in-flight/worker，固定 final DB/outbox 坐标并重算稳定字段 checksum；复验
`chosenMigrationMode=final_full_no_binlog` 的 source snapshot/import 证据、final DB identity、
`schemaCompatibleWithRecovery=true` 与 `schemaCompatibleWithFinal=true`。只有 `passed=true` 才把
停止 recovery role 并确认端口释放后，以已预置的 `CANDIDATE_IMAGE` B digest 和
`CANDIDATE_API_HOST_PORT=5001` 启动 candidate role；B 使用同一兼容 final DB/Redis 和
`127.0.0.1:5001`，不重跑 shadow 数据、不导入测试记录。切换成功后解除 B fence，A 容器/image、
attestation、配置和 route rollback state 继续保留。

B final up 后必须先用 runtime inspect 证明实际 RepoDigest、完整 B commit、attestation subject、Compose
config hash 与 final DB identity，再原子追加 `pull-and-up.txt` 的 B final up event（含本次两个 ID、
`localBuild=false`、`localLoad=false`），并原子更新 `running-digest.txt` 为同一 B 身份。若任一验证失败或
trap 回退 A，禁止留下 `passed` 的 B final up/运行证据；回退成功后 `running-digest.txt` 必须原子恢复为
实际 A identity，失败状态则写当前 attempt 的 passed=false/tombstone。

切流前安装并保持统一 post-cutover failure trap；旧侧可恢复状态在 absent 模式指 verified A recovery、
同一 final DB、A config/digest 和权威 route 快照，在步骤 3—6 全部通过前不得解除或清理。步骤
3、4、5、6 任一失败，trap 先立即 fence/drain 新写，再自动调用已演练且适用的 rollback 分支；正常
失败以预生成 A rollback receipt + 当前 schema/identity/monotonic coordinate quick-check 自动完成 B→A，
不得在回滚窗口执行 apply/full import；要求 duration<=300s、health/data/route passed，不能只告警后继续运行。

若 B→A rollback 本身不能证明成功，禁止恢复旧路由，且不得让疑似坏版本继续接受写；保持 B/A 新侧
受控只读/隔离、保留旧侧可恢复状态并把结果标为 `FAILED`，等待人工处置而不做破坏性猜测。只有
discovery 的 `oldServicePresent=true` 且 identity allowlist 全部匹配时，才可调用 conditional legacy
reverse 分支；absent 模式不要求或伪造 legacy reverse。

`artifacts/deploy/public-cutover.json` 至少含 `schemaVersion`、`passed`、`deploymentRunId`、
`cutoverAttemptId`、rollbackMode、A/B commit/
digest/attestation、config/final DB identity、route before/after、trap 状态，以及脱敏的
full/final/reverse 坐标和 checksum/route/DB identity/duration/result；不适用坐标必须显式
`notApplicable` 并由 mode 约束，不能伪造值。生成器写同目录 0600 临时文件，flush + `fsync` 后原子
rename。成功文件生成失败时必须为当前 attempt 原子落 `passed=false` tombstone；若连 tombstone 也不能
落盘，verifier 必须因 state 中 attemptId 与旧 artifact 不匹配而拒绝旧 `passed=true`，不得复用上一轮
成功证据。只有步骤 6 机器验收通过才解除 trap。

- [ ] **步骤 3：真实域名冒烟**

不使用 `-k`、hosts 伪造或直连 IP，对 `https://watermark.bxsn.cn` 执行 health、production 无效微信
code 安全失败、同步解析、异步提交/轮询、cache、performance、download fallback 和 m3u8 状态测试。

- [ ] **步骤 4：当前前端默认配置联调**

外部运行时前置是已登录微信开发者工具或真机；当前主机只有 Node/npm 不能替代。先对
`FRONTEND_REPO` 运行 provenance clean/commit/tree/manifest guard，清除 `api_config` 覆盖并使用默认
`https://watermark.bxsn.cn`。由 DevTools/真机 `wx.login` 获取一次性 code，经真实域名完成 session，
再运行固定矩阵。`frontend-domain-e2e.json.cases` 必须逐项包含：`session`、`syncParse`、
`asyncSubmit`、`asyncPoll`、`cacheRestore`、`performance`、`fallbackCreate`、`fallbackPoll`、
`fallbackDownload`、`m3u8Create`、`m3u8Poll`、`m3u8Download`、`video`、`gallery`；每项都有 requestId、
started/ended、HTTP/业务结果和 passed，顶层也含 `schemaVersion`/`passed`、frontend provenance、A/B
commit/digest、`deploymentRunId`/`cutoverAttemptId`、wechatBound/identityType，且 started/ended 必须落在
本次 B 切换时间窗。fallback/m3u8 download 必须实际通过同域 HTTPS URL 完成有界
下载，不得只看到 create/poll 成功就算通过。

同一切换时间窗内生成/核对 `mediaParserIntegration`：顶层绑定 B `sourceCommit`、实际 `imageDigest`、可信
`ciRunId` 与 test manifest hash；registry golden、structured JSON golden、query policy、candidate
ranking/有界 fallback、cache semantics、rich-media compatibility 和 unsafe-pattern policy 每项必须有原始
hermetic test 清单与通过结果。`evidenceMode` 按能力逐项只能是 `live` 或 `hermetic`：只有取得合法、稳定、
可重复且不泄露会话/个人数据的真实样本时，才把 `audio`/`livePhoto` 加入真实域名 live cases；否则必须
使用绑定同一 B source/digest/CI run 的 hermetic contract evidence，并明确 `audio=hermetic`、
`livePhoto=hermetic`，禁止用不稳定上游页面、缺样本或 aggregate 冒充 live 验收。

结束后再次运行同一 provenance guard。不能用空 code、重放 code 或 fake exchanger 宣称 production
通过。artifact 不记录 code、openid、token、session 或 secret；request/downloadFile 最终 URL 必须
同域 HTTPS。任何 share/task ID、签名 ticket、原始分享/媒体 URL 和完整 path/query 都属于 capability，
artifact 只能记录 route template、same-origin/HTTPS 布尔、状态、字节数/内容 hash、requestId，并省略
或不可逆 hash capability；`tests/ops` 用 sentinel 负测防泄漏。证据用同目录 0600 临时文件、fsync + 原子 rename 生成，任一 case 失败则顶层 passed=false
并触发 post-cutover trap。

- [ ] **步骤 5：30 分钟观察和主机保护验收**

以单调时钟记录 `startedAt`，第一次采样在约 +30 秒，之后每 25–35 秒一次；恰好 60 个唯一、严格递增
的 sample，第 60 个不早于 startedAt+1800s，`endedAt-startedAt>=1800s`。每个 sample 请求真实
`https://watermark.bxsn.cn/api/health`（可附 localhost 对照），并含 sequence/timestamp、B
`imageDigest`、health 状态/`healthLatencyMs`、容器 `restartCount`、CPU、MemAvailable、
swap si/so、`memoryPSI`/`ioPSI`、磁盘/inode、`oomCount`、`ioErrors`。验证器从原始 samples 重算 60/60、
P95 和停止线，不信 aggregate；顶层 `deploymentRunId`/`cutoverAttemptId` 必须与 state/cutover/E2E 一致，
全部 sample 属于本次 B 切换时间窗；要求无 restart/OOM/I/O/持续压力且 health P95 <200ms。

观察结束再次写最终 after/diff 到 `before-after-containers.json`，与任务 17 的 before 比较，并把 A→B
受控变化按 identity allowlist 分类；无关容器变化必须为零。`verify-acceptance.py 强制校验` before hash、
最终 after、diff 分类、60 个原始观察样本和 B running digest；缺 final after/diff 不得通过。两份 JSON
都以同目录 0600 临时文件、fsync、原子 rename 生成。停止线触发时立即走 post-cutover trap，只操作
allowlist 内 A/B 和 route。

- [ ] **步骤 6：最终全量审计**

运行 `python3 scripts/verify-acceptance.py --require-complete`，机器校验 R1–R8 全部必需文件、schema、
原子生成和重算 passed。证据按 role 绑定：A shadow/domain/recovery observation 必须绑定 A commit/digest；
B equivalence/shadow/final domain/final observation 必须绑定 B；`promotion-equivalence.json` 负责跨角色
绑定，禁止要求 A/B 使用同一 deployedSourceCommit/RepoDigest。state/public-cutover/B E2E/B observation/
final acceptance/running digest/B final up event 的 `deploymentRunId`/`cutoverAttemptId` 必须完全一致，
时间窗必须属于当前切换；任何 stale artifact 都失败。

迁移按 chosenMigrationMode 条件验证：当前 absent 模式必须有 final full snapshot/import/checksum、
table-scoped no-writer proof、shadow/final identity 隔离和 final 无本轮 shadow/A-B acceptance 的
runId/taskId/sentinel/outbox（合法 legacy 历史按 source checksum 保留），不伪造 delta/reverse。
verifier 还逐项重算三轮 93 records、A/B 两份 >=1800 秒观察、固定前端全矩阵、B→A <=300 秒真实 drill、
最终容器 diff、frontend provenance、`mediaParserIntegration` 的 sourceCommit/imageDigest/ciRunId/
evidenceMode 与 post-cutover trap。任何缺失都继续修复。全部通过后把 trace 状态
更新为实际结果并同步计划/设计；`final-acceptance.md` 用含 schemaVersion/passed 的 front matter、同目录
0600 临时文件、fsync + 原子 rename 生成，并记录同一 attempt IDs、`evidenceParentCommit` 与排除该报告
自身的 `evidencePayloadTreeSha256`，绝不内嵌尚未存在的当前治理 commit SHA。

- [ ] **步骤 7：Commit 最终脱敏报告并推送**

```bash
python3 scripts/verify-acceptance.py --require-complete
git add artifacts/deploy artifacts/acceptance artifacts/benchmark artifacts/migration artifacts/release artifacts/verification \
  docs/requirements-traceability.md docs/superpowers 约束文件.md
go test ./internal/policy -count=1
git diff --cached --check
git diff --cached --name-only
git commit -m "docs: record production acceptance evidence"
test "$(scripts/verify-gitleaks.sh)" = PASS
python3 scripts/verify-acceptance.py --require-complete --verified-evidence-commit "$(git rev-parse HEAD)"
git push origin main
```

该 docs/artifacts-only push 只运行安全与 `--require-complete` acceptance 门禁，不触发 image job、不移动
`latest`。final evidence job 必须把 `GITHUB_SHA` 作为 `--verified-evidence-commit` 外部传给 verifier，
核对该 commit 的 parent 等于 report 的 `evidenceParentCommit`、payload hash、同一 attempt IDs 以及该
commit 仅含 docs/artifacts allowlist diff；verifiedEvidenceCommit 只进入可信 CI job summary/status，
不得回写 tracked report 形成自引用。证据按 A/B role 记录对应 deployedSourceCommit/digest 并由
promotion map 关联，不得要求 A/B 相同，也不得把治理 commit 冒充任何已部署镜像。
