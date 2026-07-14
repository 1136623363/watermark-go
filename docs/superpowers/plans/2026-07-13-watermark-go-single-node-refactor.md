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

- [ ] **步骤 1：编写生产配置失败测试**

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

- [ ] **步骤 2：运行配置测试并确认失败**

运行：`go test ./internal/config -count=1`

预期：FAIL，`Load`/`Config` 尚不存在。

- [ ] **步骤 3：实现 typed config 和 app 生命周期接口**

```go
type Config struct {
    Environment string
    HTTP HTTPConfig
    MySQL MySQLConfig
    Redis RedisConfig
    Parser ParserConfig
    Tasks TaskConfig
    Download DownloadConfig
    Security SecurityConfig
    Baseline BaselineConfig
}

type Component interface {
    Start(context.Context) error
    Stop(context.Context) error
}
```

`APP_ENV` 经 trim/lower 规范化后只接受 `development`、`test`、`production`，任何未知值或拼写错误
都失败，绝不按非生产环境继续。production 必须提供可用 `MYSQL_DSN`，因为 MySQL 是业务事实存储；
Redis 仍是可选的缓存/锁/限流组件并允许降级。production 同时要求非空且非占位的
`WECHAT_MINI_APP_ID` 与满足强度/占位词门禁的 `WECHAT_MINI_APP_SECRET`，禁止退回 clientId 身份。
`ParserConfig` 在配置边界读取可选 `WEIBO_COOKIE`、`XIGUA_COOKIE`；错误、弃用告警和配置摘要只记录
字段是否配置，绝不记录 Cookie、DSN、secret 或其 URL 编码形式。parser 构造函数只接收
`ParserConfig`，为任务 3 删除业务包中的环境变量读取建立唯一来源。

下载签名密钥只在 typed config 加载边界兼容旧名 `DOWNLOAD_FALLBACK_TOKEN_SECRET`：若规范名
`DOWNLOAD_TOKEN_SECRET` 缺失则映射旧值并记录不含值的弃用告警；两者同时存在但不一致时启动失败。
任务 2 之后所有业务代码只读取 `Config.Download.TokenSecret`，不得继续直接读取任一环境变量；任务 9
删除旧名的运行时读取，部署样例只写规范名。这是唯一迁移窗口，不能形成双变量静默优先级。

入口捕获 `SIGINT/SIGTERM`，设置 20 秒退出预算；配置加载失败时退出且不监听端口。

- [ ] **步骤 4：重命名 module 与入口**

模块名改为 `github.com/1136623363/watermark-go`，`go.mod` 使用 module language `go 1.24.0` 并固定首选 `toolchain go1.26.5`；入口只调用 `config.Load()`、`app.New()`、`Run()`。若宿主机尚无该工具链，任务 2 允许 Go 自动下载，或安装经过校验的官方 `go1.26.5` 归档；不得把“宿主机当前仍是 Go 1.24.4”误判为仓库策略失败。

同一步先用 `git grep -l '"watermark-backend/' -- '*.go'` 记录精确文件清单，再机械替换全部 tracked Go
import prefix 为 `github.com/1136623363/watermark-go/`，执行 `go mod tidy`。替换后
`git grep -n 'watermark-backend/' -- '*.go'` 必须零结果；不得只改 go.mod/cmd 后用 scoped tests 掩盖全树
不可编译。机械修改的每个精确路径都纳入本任务 commit，提交前以 cached name list 对账。

- [ ] **步骤 5：验证骨架**

运行：

```bash
test -z "$(git grep -n 'watermark-backend/' -- '*.go')"
GOMAXPROCS=2 go test ./... -count=1
```

预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add go.mod go.sum cmd/watermark-go internal/config internal/app
# 另按步骤 4 保存的精确清单逐个 git add -- 所有 import-prefix 机械修改文件
git diff --cached --name-only
git commit -m "refactor: add typed configuration and app lifecycle"
```

## 任务 3：迁移解析器并消除源码凭据

**文件：**
- 移动：`internal/parsers/native/*` → `internal/parser/native/*`
- 移动：`internal/parsers/universal/*` → `internal/parser/universal/*`
- 创建：`internal/parser/parser.go`
- 创建：`internal/parser/descriptor.go`
- 创建：`internal/parser/registry.go`
- 创建：`internal/parser/registry_test.go`
- 创建：`internal/parser/native/testdata/`（脱敏合成 golden，不含 Cookie/token/个人信息/媒体本体）
- 创建：`internal/parser/ytdlp/runner.go`
- 修改：`internal/parser/native/weibo.go`
- 修改：`internal/parser/native/xigua.go`
- 修改：`internal/policy/parser_cookie_security_test.go`（迁移路径后继续覆盖 production parser）
- 修改：所有由 `git grep -l 'internal/parsers' -- '*.go'` 精确发现的现有 callsite（含尚未拆除的
  `internal/server`），同一 commit 改到 `internal/parser`；禁止留下破坏全树编译的旧 import

- [ ] **步骤 1：编写 parser 契约和注册表失败测试**

```go
type Parser interface {
    Parse(context.Context, Request) (Result, error)
}

type Descriptor struct {
    Key PlatformKey // 稳定 ASCII ID；展示名不作内部主键
    Domains, Aliases []string
    Capabilities Capability
    Priority int
    QueryKeys []string
    New func(Dependencies) Parser // 纯构造，不得联网/读环境/启动进程
}

func TestRegistryContainsLegacyNativePlatforms(t *testing.T) {
    keys := NewRegistry(native.Parsers()).Platforms()
    require.Subset(t, keys, []string{"douyin","kuaishou","bilibili","weibo","redbook","m3u8"})
}

func TestRegistryRejectsAmbiguousDescriptorMetadata(t *testing.T) {
    descriptors := []Descriptor{
        {Key:"douyin", Domains:[]string{"v.example"}, Aliases:[]string{"dy"}},
        {Key:"other", Domains:[]string{"v.example"}, Aliases:[]string{"dy"}},
    }
    require.Error(t, NewRegistry(descriptors))
}

func TestParserFetchesUpstreamOnceForRichMediaResult(t *testing.T) {
    fetcher := &countingFetcher{fixture: richMediaFixture()}
    got := parseWith(fetcher)
    assert.Equal(t, int32(1), fetcher.calls.Load())
    require.NotEmpty(t, got.Images[0].LivePhotoURL)
    require.NotEmpty(t, got.AudioURL)
}

func TestParserConstructionPerformsNoIO(t *testing.T) {
    deps := failingOnUseDependencies()
    parser := descriptorFor("bilibili").New(deps)
    require.NotNil(t, parser)
    assert.Zero(t, deps.TotalCalls())
}

func TestEveryProductionParserRejectsEmbeddedCookieHeaders(t *testing.T) {
    // 对 internal/parser 下每个 production Go blob 执行 AST 门禁：map/index assignment、
    // Set/SetHeader/Add、字符串拼接，以及 const/VarSpec/:= alias 均不得形成 Cookie 字面量。
    require.NoError(t, auditProductionParserCookies("internal/parser"))
}
```

Cookie AST 门禁必须用对象绑定实现 scope-correct alias 解析：覆盖 package/local `const`、`VarSpec`、
短声明 `:=`、多赋值、拼接和赋值传播，正确处理内层 shadowing，不能按变量名做跨作用域串联。恶意
fixture 分别覆盖 const/var/:= alias 与 shadowing，安全 fixture 证明同名局部变量不会误报。

- [ ] **步骤 2：确认测试失败**

运行：`go test ./internal/parser/... -count=1`

预期：FAIL，包和接口尚未建立。

- [ ] **步骤 3：机械迁移并适配 Parser 接口**

保留固定提交中 URL/ID 解析、平台别名和字段行为；`redbook→xiaohongshu`、
`quanminkge→kgqq`、`xigua→ixigua` 兼容在规范化层完成。native/universal/ytdlp 构造函数显式接收
任务 2 的 `ParserConfig`；迁移完成后 `internal/parser` 内不得出现 `os.Getenv`/`os.LookupEnv`。
universal 与 yt-dlp 命令构造测试必须证明无法省略或覆盖任务 4 提供的 localhost netguard egress proxy。

按 `docs/research/media-parser-review.md` 吸收其集中注册和富媒体能力思路，但不复制上游代码：注册表使用
metadata-driven `Descriptor`，至少包含稳定 ASCII `PlatformKey`、display name、aliases、精确 domains、
capabilities（video/gallery/audio/live-photo/m3u8）、确定性 priority、每平台允许保留的 query keys 与
constructor。
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

移动前保存 `git grep -l 'internal/parsers' -- '*.go'` 的精确清单；移动后机械更新所有 callsite，并要求
`git grep -n 'internal/parsers' -- '*.go'` 零结果。不得等 Task 11 删除旧 server 才修 import，也不得用
scoped parser tests掩盖 broken tree。

- [ ] **步骤 4：保留并复验已前置完成的 Cookie 安全边界**

安全纠偏已将微博和西瓜请求改为仅在 typed `ParserConfig` 对应 Cookie 非空时添加 Cookie header；
本步骤迁移文件时必须保持空值不设置、trim 后注入的行为与回归测试。AST policy helper 同时接入仓库
audit 的 history/index/worktree blob 扫描，路径兼容 `internal/parsers/` 与 `internal/parser/`；安全
worktree 不能遮住 staged Cookie literal，测试/仓库默认不含 Cookie，也不得保留“取消注释恢复”说明。

- [ ] **步骤 5：验证原生解析器回归**

运行：

```bash
GOMAXPROCS=2 go test ./internal/parser/... ./internal/policy -count=1
if git grep -n 'os\.Getenv\|os\.LookupEnv' -- internal/parser; then exit 1; fi
if git grep -n 'internal/parsers' -- '*.go'; then exit 1; fi
GOMAXPROCS=2 go test ./... -count=1
```

预期：全部 PASS，原固定提交 parser 测试均保留。

- [ ] **步骤 6：Commit**

```bash
git add internal/parser internal/policy/parser_cookie_security_test.go
# 逐个 git add -- 步骤 3 保存清单中的精确 callsite
git rm -r internal/parsers
git diff --cached --name-only
git commit -m "refactor: isolate parser adapters and remove embedded credentials"
```

## 任务 4：实现统一网络安全层

**文件：**
- 创建：`internal/netguard/url.go`
- 创建：`internal/netguard/validator.go`
- 创建：`internal/netguard/transport.go`
- 创建：`internal/netguard/validator_test.go`
- 创建：`internal/policy/network_egress_test.go`
- 修改：`internal/parser/native/http_client.go`
- 修改：`internal/runtimecfg/settings.go`
- 修改：`internal/server/client_auth.go`
- 修改：`internal/server/download_fallback.go`
- 修改：`internal/server/cluster.go`
- 修改：`internal/server/cluster_platform_tests.go`

- [ ] **步骤 1：编写 SSRF 表格测试**

覆盖 `127.0.0.1`、`::1`、RFC1918、CGNAT、链路本地、metadata、十进制/八进制 IP、IDNA、大小写、
尾点、userinfo、非常规端口、redirect loop、重定向到私网、DNS 首次公网随后私网，以及允许的公网地址。
另测跨 origin/host redirect 必须剥离 Cookie/Authorization/平台会话 header，response header、wire body、
解压后 body 任一超限都拒绝；禁止盲目 HTTP→HTTPS 字符串改写和跳过 TLS 校验。

```go
func TestDialContextRejectsResolvedPrivateTarget(t *testing.T) {
    resolver := fakeResolver{"media.example": {netip.MustParseAddr("127.0.0.1")}}
    guard := New(WithResolver(resolver))
    _, err := guard.DialContext(context.Background(), "tcp", "media.example:443")
    require.ErrorIs(t, err, ErrPrivateTarget)
}
```

同时先写 `internal/policy/network_egress_test.go` 红测。门禁对 `cmd/`、`internal/` 的 production Go
（排除 `*_test.go`、生成文件和 `internal/netguard/`）执行 AST/go-types 分析，解析正常 import、
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

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/netguard ./internal/policy -run 'TestDial|TestProductionNetworkEgress' -count=1`

预期：FAIL，安全 transport 尚不存在。

- [ ] **步骤 3：实现请求前、重定向和 DialContext 三层校验**

类型层区分不可日志化且只供受控 client 使用的 `FetchURL`、允许持久化/响应的 `SafeURL` 和用途域分离的
不可逆 `CacheKey`，避免普通 `String()` 意外输出完整敏感 query。不得通过字符串替换假设目标支持 TLS。
所有业务 HTTP client 只能由 `netguard.NewTransport` 创建；代理地址显式配置，目标地址仍逐跳校验。
resty 只能在 netguard adapter 内由已注入的受控 transport 构造，禁止默认 transport。
另启动仅监听 loopback 的 netguard egress proxy：yt-dlp 强制 `--ignore-config --proxy "$NETGUARD_URL"`，并用
空的临时 HOME/XDG 隔离用户配置；Python universal 子进程先清空大小写
`HTTP_PROXY/http_proxy`、`HTTPS_PROXY/https_proxy`、`ALL_PROXY/all_proxy`、
`NO_PROXY/no_proxy`，再把大小写 HTTP(S)_PROXY/ALL_PROXY 设为唯一 netguard
地址，并将 `NO_PROXY/no_proxy` 置空；proxy 对每次 CONNECT/请求和重定向
重新解析并校验 DNS、剥离跨 origin/host 的敏感 header，限制响应头、wire/解压后 body 和时长。命令构造表格测试覆盖参数遗漏、重复 `--proxy`、环境覆盖
和重定向/DNS rebinding。不能强制全部流量经过该 proxy 的工具在 production 配置加载时禁用，不能
宣称只校验首跳 URL 就覆盖 subprocess 网络。

ffmpeg 不联网：任务 9 由 Go 通过 netguard 预取 manifest、子清单和全部分片并重写成本地清单；
ffmpeg protocol whitelist 只保留实际需要的本地 `file`，拒绝 `http/https/tcp/tls/crypto/concat/data`。

- [ ] **步骤 4：验证所有网络路径未绕过**

运行：

```bash
go test ./internal/netguard ./internal/parser/... ./internal/policy -run 'TestDial|TestProductionNetworkEgress' -count=1
git grep -n -- '--proxy\|HTTP_PROXY\|HTTPS_PROXY\|NO_PROXY' internal/parser
```

预期：测试 PASS；AST/go-types 门禁对全 production tree 证明没有未迁移出口，grep 只命中统一、
不可覆盖的 egress proxy 构造器与对应测试。以门禁报告的精确 callsite 清单和 `git diff --name-only`
双重核对本任务修改面，不能靠手写的少数 grep 猜测完整性。

- [ ] **步骤 5：Commit**

```bash
git add internal/netguard internal/parser internal/runtimecfg/settings.go \
  internal/server/client_auth.go internal/server/download_fallback.go internal/server/cluster.go \
  internal/server/cluster_platform_tests.go internal/policy/network_egress_test.go
test -z "$(git diff --name-only -- internal/netguard internal/parser internal/runtimecfg/settings.go \
  internal/server/client_auth.go internal/server/download_fallback.go internal/server/cluster.go \
  internal/server/cluster_platform_tests.go internal/policy/network_egress_test.go)"
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
- 创建：`tests/integration/store/migration_test.go`（CI/Task 17 integration profile，禁止本地主机隐式启动）
- 创建：`internal/cache/cache.go`
- 创建：`internal/cache/redis.go`
- 创建：`internal/cache/memory.go`
- 创建：`internal/cache/cache_test.go`

- [ ] **步骤 1：编写迁移幂等和 Redis 降级测试**

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
```

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

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/store ./internal/cache -count=1`

预期：FAIL，仓储和缓存包尚不存在。

- [ ] **步骤 3：迁移核心表与 lease 字段**

从旧迁移保留结果、尝试、session、设置、样本、平台运行、任务、管理员和审计；
任务表增加 `locked_by/locked_until/next_attempt_at` 与索引。生产不使用本地 JSON 作为权威配置。

- [ ] **步骤 4：实现源 MariaDB 到目标 MySQL 的兼容导入**

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

- [ ] **步骤 5：实现 Redis 可降级策略**

Redis 负责 7 天热缓存、180 秒失败缓存、60 秒 URL 锁和限流；Redis 错误记录告警后回退内存，
MySQL 读写不因 Redis 失败回滚。
解析结果 key 必须绑定稳定 platform key、canonical resource ID、parser version 与 result schema version；
同一 key 使用 singleflight，版本变化自动 miss。仅稳定且明确可缓存的失败进入短 TTL 负缓存；context
取消、internal、credential_required、schema_changed 与 security rejection 不得进入普通负缓存。
force refresh 同时绕过正/负缓存，Redis 与内存实现共享相同 key/version 语义和容量上限。

- [ ] **步骤 6：验证**

运行：`GOMAXPROCS=2 go test ./internal/store ./internal/cache -count=1`

预期：全部 PASS。

Actions 另运行（本地主机 Task 5 不运行）：

```bash
go test -tags=integration ./tests/integration/store -count=1
```

使用 workflow 已固定 digest 的 MariaDB 11.8/MySQL 8.4 services，未执行或 skip 均使 image job 失败。

- [ ] **步骤 7：Commit**

```bash
git add migrations internal/store internal/cache tests/integration/store
git commit -m "refactor: add durable stores and degradable cache"
```

## 任务 6：实现客户端 session 与兼容鉴权

**文件：**
- 创建：`internal/auth/client.go`
- 创建：`internal/auth/token.go`
- 创建：`internal/auth/client_test.go`
- 创建：`internal/httpapi/client_handlers.go`
- 创建：`internal/httpapi/client_handlers_test.go`

- [ ] **步骤 1：编写当前前端契约测试**

测试空微信 code 的开发身份、稳定 UID、token 过期、`token` header、Bearer 兼容，以及
无效 token 用 HTTP 200 返回 `code=1008`。另外覆盖安全熵失败时 identity/session 均零写、微信
transport/status/body/JSON/业务拒绝错误只能产生固定脱敏响应与固定分类日志，以及上游错误中即使
含完整 query URL、登录 code 和应用密钥也不得出现在响应/日志。微信身份 metadata 只允许保存
`programType`、openid 绑定所需字段和 unionid，明确断言不含 `session_key`、其 camelCase 变体或值。

```go
func TestInvalidTokenUsesFrontendRefreshContract(t *testing.T) {
    res := postJSON(t, router, "/api/parse", `{"url":"https://example.com/v"}`, header("token", "bad"))
    assert.Equal(t, http.StatusOK, res.Code)
    assert.JSONEq(t, `{"code":1008,"msg":"登录状态已失效，请重试"}`, res.Body.String())
}
```

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/auth ./internal/httpapi -run 'TestClient|TestInvalidToken' -count=1`

预期：FAIL。

- [ ] **步骤 3：实现 session**

token 使用 256 位安全随机值的 SHA-256 摘要落库，响应只返回明文一次；默认 TTL 30 天。随机值必须
在任何 identity/session 写入前成功生成，熵源失败返回固定 `code=1008` 且两类状态均为零写，禁止
伪随机 fallback。`uid` 使用 `30000000 + userID` 的稳定十进制格式。正式微信配置存在时绑定
openid，测试/开发允许空 code 的 clientId 身份。微信上游 transport/status/read/JSON/业务错误在
边界内归类，handler 只返回固定通用错误，日志不格式化原始 error/request URL/code；上游
`session_key` 仅用于本次交换，既不进入 identity metadata，也不落库或写日志。

- [ ] **步骤 4：验证**

运行：`go test ./internal/auth ./internal/httpapi -run 'TestClient|TestInvalidToken' -count=1`

预期：全部 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/auth internal/httpapi/client_handlers.go internal/httpapi/client_handlers_test.go
git commit -m "feat: implement mini program session authentication"
```

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

func TestNormalizeLivePhotoKeepsLegacyImagesShape(t *testing.T) {
    got := Normalize(Result{Images: []ImageAsset{{URL:"https://cdn.example/a.jpg",
        LivePhotoURL:"https://cdn.example/a.mp4"}}})
    assert.Equal(t, []string{"https://cdn.example/a.jpg"}, got.Images)
    assert.Equal(t, "https://cdn.example/a.mp4", got.ImageAssets[0].LivePhotoURL)
}
```

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/parse ./internal/httpapi -run 'TestNormalize|TestForceRefresh|TestParseContract' -count=1`

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
- 创建：`internal/httpapi/download_handlers.go`
- 创建：`internal/httpapi/download_contract_test.go`

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

同一行为测试锁定 canonical route-auth inventory：fallback create 可保持匿名兼容，但必须同时满足
attempt/limit/SSRF（`attempt>=4`、限流/并发/大小门禁与 netguard）。cache shareId 与 parse poll 使用
用途/TTL 绑定的 crypto-random >=128-bit ID 本身作为 bearer capability；fallback poll/download 使用
服务端返回的有用途/TTL 签名 ticket URL。m3u8 create 与 task poll 按固定前端实际无 token 兼容：前端
固定用随机 >=128-bit task ID 请求 `/api/task/:id`，不能强制其未发送的 poll query ticket；最终 file URL
必须签名并绑定用途/TTL。任一 ID/ticket 缺失、篡改、跨用途或过期都失败，不能把“无 token”实现成
可枚举资源。前端实际发送认证的入口同时兼容 `token` header 与
`Authorization: Bearer`（token/Bearer），不得要求前端未发送的 header。

- [ ] **步骤 2：确认失败**

运行：`go test ./internal/download ./internal/media ./internal/httpapi -run 'TestDownload|TestM3U8' -count=1`

预期：FAIL。

- [ ] **步骤 3：实现下载任务与签名 ticket**

ticket 使用独立 `DOWNLOAD_TOKEN_SECRET` 的 HMAC-SHA256，不能回退到管理员 session secret，绑定非空
task ID、过期时间和用途；文件名由服务生成，禁止路径穿越。
所有临时文件先写 `.part`，校验长度和媒体类型后原子重命名，TTL 清理不删除运行中任务。

- [ ] **步骤 4：实现 m3u8 安全合并**

Go 通过 netguard 预取并验证 manifest、有限层级子清单和全部分片，限制层级、分片数、单片与累计
字节；拒绝加密、私网目标、绝对路径、`..` 和 `file/concat/data/crypto`。每个资源写入受控临时根内由
服务生成的文件名，重写后的本地清单只引用这些文件。ffmpeg 仅启用本地 `file` protocol whitelist，
不得解析任何远程 URL；120 秒超时后杀死进程组并删除部分文件。

- [ ] **步骤 5：注册兼容接口并验证**

运行：`GOMAXPROCS=2 go test ./internal/download ./internal/media ./internal/httpapi -count=1`

预期：全部 PASS；任务成功响应兼容 `status:"done", url:"..."` 和 fallback `completed/downloadUrl`。

- [ ] **步骤 6：Commit**

```bash
git add internal/download internal/media internal/httpapi/download_handlers.go internal/httpapi/download_contract_test.go
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
合法取得且脱敏的合成/候选 fixture，并明确 `productionEnabled=false`；只有单独变更同时通过 descriptor
唯一性、URL/netguard、资源门禁、当前 API 契约和稳定性测试后，候选才能进入 production registry。

- [ ] **步骤 5：验证**

运行：`GOMAXPROCS=2 go test ./internal/admin ./internal/httpapi -count=1`

预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/admin internal/httpapi/admin_handlers.go internal/httpapi/admin_contract_test.go \
  tests/baseline docs/baseline-provenance.json
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
git add internal/httpapi internal/observability internal/app
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
可复现负向测试还要用两个模拟 revision 构建相同批准输入，比较 canonical rootfs inventory 与 app hash；
若 Go VCS/buildid/time、ldflags commit/time、Python `.pyc` 或可变生成元数据进入 rootfs 必须失败，只有
OCI config 中明确 allowlist 的 revision label 可不同。
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
`/app/logs`、`/app/tmp`、`/app/tools` 可写，加入镜像 healthcheck。精确路径
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
  api:
    image: ${BACKEND_IMAGE:?set BACKEND_IMAGE to ghcr registry digest}
    ports: ["${API_BIND_ADDRESS:-127.0.0.1}:${API_HOST_PORT:-15001}:5001"]
    mem_limit: 2g
    cpus: 2.0
  mysql:
    image: ${MYSQL_IMAGE:?set pinned mysql registry digest}
    mem_limit: 1g
  redis:
    image: ${REDIS_IMAGE:?set pinned redis registry digest}
    mem_limit: 256m
```

`API_BIND_ADDRESS`/`API_HOST_PORT` 使用严格 allowlist：默认和 shadow 只允许 `127.0.0.1:15001`，final
只允许 `127.0.0.1:5001`；preflight 拒绝任何其他组合，尤其拒绝 `0.0.0.0`、空地址、其他端口或把
final 配成 LAN。可选 LAN 只能通过独立、临时受限 override profile，在宿主防火墙已验证来源 allowlist
后启用并在测试后立即撤销，不得写入仓库外正式 runtime file。

Compose profiles 分离 `shadow`、`final`、`recovery` 与 `migration-tools`：shadow API 连接独立
DB/schema、独立 Redis namespace 和独立卷；final production DB/Redis 与 shadow 物理或逻辑隔离；
`migration-tools` 只包含由 `MARIADB_RECOVERY_IMAGE` 指向官方 digest 的 MariaDB recovery image，
不发布端口、不以裸 `docker run` 启动。policy 检查 profile、网络、卷、数据库 identity 和运行变量，
防止 shadow/rehearsal 服务误连 final 数据面。

三个运行变量都必须是 `repository@sha256:` 后紧跟 64 位小写十六进制 digest；裸 tag、短 SHA 和 `latest` 被 policy
拒绝。Task 13 通过官方 registry manifest/只读 imagetools inspect 在受控提交中解析并人工核对平台，
把 tag+digest 固定到 lock；依赖更新必须独立审查提交。MySQL/Redis 不发布宿主机端口，数据 bind
mount 到 `/var/lib/watermark-go`。
环境示例文件使用可跟踪的 `deploy/env.example`；`.env*` 继续全局禁止跟踪。安全前置已删除旧根 Compose、旧 Nginx 配置和 mutable sync 脚本，Task 13 不得恢复它们。

- [ ] **步骤 5：实现 Actions**

push main 的 checkout 必须设置 `fetch-depth: 0` 和 `fetch-tags: true`，使 secret scan 覆盖所有
heads/tags 可达历史、annotated tag message 与即将推送 refs。Gitleaks Action 固定为
`gitleaks/gitleaks-action@ff98106e4c7b2bc287b24eaf42907196329070c7 # v2.3.9`，并在该 step 显式设置
`GITLEAKS_VERSION: "8.30.1"`，不得使用浮动 tag。该 Action 的 push 扫描是 first-parent 增量门禁，
不能被描述为全 refs/history 证据；checkout 后必须另行执行 `scripts/verify-gitleaks.sh`，以已校验的
固定 v8.30.1 CLI 和显式 `--log-opts=--all` 作为权威全历史门禁。随后运行
`gofmt/vet/test -race`、pytest、Trivy，再用 Buildx 推送
`ghcr.io/1136623363/watermark-go:sha-${GITHUB_SHA}`（完整 40 位）和便利标签 `latest`。运行、验收和
回滚永远只使用该 manifest 的 GHCR repository 加 `@sha256:` 和 64 位 digest，不用任何 tag；
不调用远程服务器或 Jenkins。

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

Buildx 同时生成 provenance attestation subject，把 manifest digest 绑定完整 40 位 source commit 和
固定 source URL。`scripts/verify-image.sh`、发布证据与目标机 runtime inspect 必须联合验证：实际
RepoDigest 等于 attestation subject digest，OCI `org.opencontainers.image.revision` 等于证据中的完整
commit，`org.opencontainers.image.source` 等于新仓库；tag 仅作索引，不能作为运行身份或通过依据。

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
BACKEND_IMAGE=ghcr.io/1136623363/watermark-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  MYSQL_IMAGE=mysql@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  REDIS_IMAGE=redis@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc \
  MARIADB_RECOVERY_IMAGE=mariadb@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd \
  API_BIND_ADDRESS=127.0.0.1 API_HOST_PORT=15001 \
  MYSQL_PASSWORD=x MYSQL_ROOT_PASSWORD=x ADMIN_PASSWORD=x ADMIN_SESSION_SECRET=x DOWNLOAD_TOKEN_SECRET=x \
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

`artifacts/verification/local-verification.md` 使用可机器解析的 YAML front matter，至少包含
`schemaVersion`、`passed`、source commit、命令/退出码和生成时间，正文只含脱敏摘要。生成器先写
同目录 0600 临时文件，flush + `fsync` 后原子 rename，失败删除临时文件且不得留下半份/旧 `passed`
报告。策略测试与 `verify-acceptance.py --schema-of-present` 校验 schema、原子生成标记和脱敏字段。

- [ ] **步骤 1：格式、vet、race 和全量测试**

```bash
test -z "$(gofmt -l .)"
GOMAXPROCS=2 GOMEMLIMIT=2GiB go vet ./...
GOMAXPROCS=2 GOMEMLIMIT=2GiB go test -race -p 2 ./... -count=1
python3 -m pytest tests/baseline tests/ops -q
```

预期：全部退出 0。这里只运行 hermetic/in-process 测试；需要活服务的 `tests/e2e` 已由 Actions runner
执行，并将在任务 17 对已拉 GHCR 镜像重跑，Task 15 不在目标宿主启动依赖或服务。

- [ ] **步骤 2：Compose、secret 和差异检查**

```bash
BACKEND_IMAGE=ghcr.io/1136623363/watermark-go@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  MYSQL_IMAGE=mysql@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  REDIS_IMAGE=redis@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc \
  MARIADB_RECOVERY_IMAGE=mariadb@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd \
  API_BIND_ADDRESS=127.0.0.1 API_HOST_PORT=15001 \
  MYSQL_PASSWORD=x MYSQL_ROOT_PASSWORD=x ADMIN_PASSWORD=x ADMIN_SESSION_SECRET=x DOWNLOAD_TOKEN_SECRET=x \
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
git commit -m "fix: address pre-release review findings"
```

该修复 commit 后必须重跑步骤 1–2；若又有 must-fix，重复红/绿与精确提交，直到代码审查无阻塞项。
只有最新实现 commit 的全套验证通过，才允许生成下面的独立 evidence commit。

- [ ] **步骤 4：Commit**

```bash
git add artifacts/verification/local-verification.md artifacts/verification/secret-scan.txt
git commit -m "test: record local verification evidence"
```

此独立 evidence commit 只含两份脱敏验证 artifact；任何 must-fix 实现或测试若仍未提交，必须返回步骤 3，
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

A 验证前禁止发布 B。任务 17 把 A 提升为 verified recovery 后，才允许创建只改精确路径
`release/promotion-marker.txt`/OCI revision 的 promotion commit 并由 Actions 构建 B；B 的 source/digest/attestation、A/B 等价性和最终
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
shadow 数据可来自早期 full 仅用于测试；final production DB 必须在 table-scoped fence、连接身份和
重复 hash 证明无 writer后重新生成一致性 full snapshot/import/checksum。禁止全实例锁或 read_only，
也禁止用 `updated_at` 伪造 delta。任何新 writer、hash 漂移或转换差异都停止。

- [ ] **步骤 3：只拉取并验证 recovery 候选 A**

创建 0700 临时 `DOCKER_CONFIG` 并注册 EXIT/signal trap；token 只经 stdin 传给 `docker login`。
`BACKEND_IMAGE` 此时只能读取 `recovery-image-digest.txt` 的 A candidate digest，禁止提前使用尚未生成的
final `image-digest.txt`。Compose pull 后用 `scripts/verify-image.sh` 和 runtime inspect 验证实际
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

- [ ] **步骤 5：在 A shadow 上跑服务 E2E、后台、恢复和三轮基准**

仓库外 `/var/lib/watermark-go/runtime.env` 先设 `API_BIND_ADDRESS=127.0.0.1`、
`API_HOST_PORT=15001` 并绑定 shadow DSN/Redis namespace；production preflight 只报字段分类。对
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
启动 A final listener/worker。runtime file 改为 `API_BIND_ADDRESS=127.0.0.1`、`API_HOST_PORT=5001`。
切流前确认已登录 DevTools/真机和一次性 wx.login readiness；route 只根据运行 tunnel/dashboard 或安全
API identity 更新/验证，不编辑 token tunnel。
readiness code 只证明外部前置、不得保存或复用；A 真实矩阵开始 session 前必须再次调用 `wx.login`
取得全新一次性 code，交换后立即丢弃，artifact 不记录 code/openid/token/session。

以 `rollbackMode=absent_two_stage` bootstrap A。A 必须先完成 A shadow 隔离全验，再在 A 真实域名通过
真微信、固定前端全矩阵和 `A observation>=1800s`；只有这些原始证据通过，A 才成为 recoveryDigest。
若 A 首上任一步失败，立即 fence/drain A 新写并撤销本次 route 变更、恢复原 502 路由，保留 final DB/
outbox 已接受写入供调查并标记 `FAILED`；任务未完成，不得声称 5 分钟健康回滚或已有 recovery。

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

A 成为 recovery 后才创建 B。A→B source diff allowlist 仅允许精确路径
`release/promotion-marker.txt`/OCI revision；禁止
Go/依赖/Dockerfile/执行/config/migration/schema 变化。Actions 从 B 完整 source commit 构建不同 digest，
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

在 A 已稳定、final DB 已验证同时兼容两镜像后，短暂把 B 接到同一兼容 final DB 和真实 route，立刻
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

再次执行 host discovery 和 `host-before 精确 identity/hash allowlist` 比对，确认 A、final DB、route 与
无关对象未漂移；默认仍只操作 `watermark-go`。采集 MemAvailable、swap si/so、memory/io PSI、OOM、
磁盘/inode、端口与无关容器 hash，任一停止线触发都不切流。

同时在写栅栏/切流前完成一次性 wx.login code readiness：确认已登录 DevTools/真机在线、目标小程序
环境可调用 `wx.login` 并能取得新的单次 code。这里只记录 readiness 布尔值、设备类型和时间，不记录、
持久化或回显 code；正式交换在步骤 4 再获取新 code。未就绪不得进入写栅栏或切流。运行配置必须从
仓库外固定路径加载、验证权限并明确 final bind：

```bash
test "$(stat -c '%a' /var/lib/watermark-go/runtime.env)" = 600
test "$(grep -Fx 'API_BIND_ADDRESS=127.0.0.1' /var/lib/watermark-go/runtime.env)" = 'API_BIND_ADDRESS=127.0.0.1'
test "$(grep -Fx 'API_HOST_PORT=5001' /var/lib/watermark-go/runtime.env)" = 'API_HOST_PORT=5001'
docker compose --env-file /var/lib/watermark-go/runtime.env -f deploy/compose.yml config --quiet
```

最终 runtime 只允许 `API_BIND_ADDRESS=127.0.0.1` 与 `API_HOST_PORT=5001`。LAN 调试只能在 Task 17 使用
临时受限 LAN override，Task 18 禁止启用 LAN override。

- [ ] **步骤 2：fence A 后把 B 切到同一兼容 final DB**

actual `rollbackMode=absent_two_stage` 不存在可栅栏的 legacy 服务。先对正在运行的 A 启用短写 fence、
排空 in-flight/worker，固定 final DB/outbox 坐标并重算稳定字段 checksum；复验
`chosenMigrationMode=final_full_no_binlog` 的 source snapshot/import 证据、final DB identity、
`schemaCompatibleWithRecovery=true` 与 `schemaCompatibleWithFinal=true`。只有 `passed=true` 才把
`BACKEND_IMAGE` 从 A recovery digest 原子切为 B final digest；B 使用同一兼容 final DB/Redis 和
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
失败自动完成 B→A，要求 duration<=300s、health/data/route passed，不能只告警后继续运行。

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
最终容器 diff、frontend provenance 与 post-cutover trap。任何缺失都继续修复。全部通过后把 trace 状态
更新为实际结果并同步计划/设计；`final-acceptance.md` 用含 schemaVersion/passed 的 front matter、同目录
0600 临时文件、fsync + 原子 rename 生成，并记录同一 attempt IDs、`evidenceParentCommit` 与排除该报告
自身的 `evidencePayloadTreeSha256`，绝不内嵌尚未存在的当前治理 commit SHA。

- [ ] **步骤 7：Commit 最终脱敏报告并推送**

```bash
python3 scripts/verify-acceptance.py --require-complete
git add artifacts/deploy artifacts/acceptance artifacts/benchmark artifacts/migration artifacts/release artifacts/verification \
  docs/requirements-traceability.md docs/superpowers 约束文件.md
git commit -m "docs: record production acceptance evidence"
python3 scripts/verify-acceptance.py --require-complete --verified-evidence-commit "$(git rev-parse HEAD)"
git push origin main
```

该 docs/artifacts-only push 只运行安全与 `--require-complete` acceptance 门禁，不触发 image job、不移动
`latest`。final evidence job 必须把 `GITHUB_SHA` 作为 `--verified-evidence-commit` 外部传给 verifier，
核对该 commit 的 parent 等于 report 的 `evidenceParentCommit`、payload hash、同一 attempt IDs 以及该
commit 仅含 docs/artifacts allowlist diff；verifiedEvidenceCommit 只进入可信 CI job summary/status，
不得回写 tracked report 形成自引用。证据按 A/B role 记录对应 deployedSourceCommit/digest 并由
promotion map 关联，不得要求 A/B 相同，也不得把治理 commit 冒充任何已部署镜像。
