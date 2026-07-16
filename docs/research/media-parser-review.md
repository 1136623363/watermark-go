# ucmao/media-parser 研究结论与融合边界

## 固定研究对象

本次只研究 `https://github.com/ucmao/media-parser` 默认分支 `starter` 的固定 commit
`033424b08ac6468c8c37b6fb0c98a0446bb09d9e`、tree
`56e556db619a296340fa8b00f3c726676cf32bcf`。许可证为 MIT，精确来源与许可证文件哈希记录在
`docs/research/media-parser-provenance.json`。

`starter` 相对旧 `main` 已前进且没有对应 GitHub Release，默认分支也不是受保护的发布接口；因此禁止
用 tag、默认分支或同步脚本自动更新研究输入。固定 tree 没有 `tests/`、`.github/` 或 Actions 运行记录，
即使 README 列出测试目录，平台支持声明也只能视作研究线索，不能当作可运行证据。

上游代码复制：无。本轮只吸收架构概念、平台行为线索和测试设计；若以后移植任何实质代码或数据表，
必须单独审查、保留 MIT 归属/许可证、更新 provenance，并在复制发生前先增加许可证门禁。

## 值得吸收的优点

1. 集中注册与域名目录。`src/parser_factory.py:26-58` 把平台与 parser 构造集中起来；
   `configs/business_config.json` 汇总了 50 个 domain alias、映射到 24 个平台。这比散落在 handler
   中的 switch 更容易审计覆盖率、别名冲突和平台能力。
2. 统一但可选的富媒体能力。`src/parsers/base_parser.py:13-39` 为视频、标题、封面、作者、音频和图集
   提供共同形状；`src/api/parse.py:39-59` 统一输出和 HTTPS 规范化。这个方向有利于让编排层不依赖
   单个平台字段。
3. 保留平台必需的 URL 参数。`utils/web_fetcher.py:75-119` 体现了 canonical URL 不能一律丢弃 query：
   不同平台可能依赖 `vid`、`id`、`xsec_token`、`modal_id`、`v`、`s` 或 `pid`。我们采用“每平台显式
   query allowlist”，不复制它的实现。
4. 图集、音频和 Live Photo。小红书与抖音 parser 会同时表达静态图和配对动态资源；抖音还表达独立
   音频。该能力适合进入内部强类型 `ImageAsset`/`LivePhotoURL` 模型，再投影为当前前端兼容响应。
5. 多候选媒体线索。部分 parser 能看到清晰度/CDN 候选并做有限重试。我们把这一点改造成显式
   `MediaCandidate`、确定性排序和有界切换，而不是依赖数组位置或无限重试。
6. 结构化数据优先与页面漂移容错。Bilibili parser 优先调用结构化 API；快手 parser 包含候选去重、
   转义感知的嵌入 JSON 边界识别和多种 INIT_STATE/Apollo 载体线索。我们只迁移这一测试思路：优先
   稳定结构化端点，并用合成 golden fixture 覆盖截断 JSON、字段换层、登录/风控页和空核心字段。
7. 会话材料失效后刷新。抖音 parser 能在语义失败后使短期会话缓存失效并重取。我们保留“按错误类型
   失效”的概念，但缓存必须有 TTL、singleflight、容量上限与精确 host 作用域，且无源码 fallback 值。

## 融入 watermark-go 的具体方式

### Parser 注册表

任务 3 将简单 map 提升为 metadata-driven Descriptor registry。每个 descriptor 至少包含稳定
ASCII `PlatformKey`、显示名、aliases、显式 HostRule、capabilities、确定性 priority、允许保留的 query
keys 和构造函数。
注册时必须拒绝重复/歧义 key、alias、domain，输出顺序稳定；host 使用规范化后的精确匹配，不做可被
恶意后缀利用的字符串包含匹配。上游的 50 个 alias 只作为候选覆盖目录，只有被当前固定来源、93 样本
或新增独立 fixture 验证的平台/domain 才能进入 production registry。

这里的“精确匹配”指 exact host 或 DNS label-boundary 的显式 controlled-subdomain rule，不是把研究项目
的 50 条 exact netloc 反向当成当前兼容基线。固定 commit 对已有 41 条 domain 全部采用
`host == domain || HasSuffix(host, "."+domain)`；Task 3 因而锁定 41/41 `IncludeSubdomains=true` 和恶意
后缀拒绝行为。研究发现的“31 条可收紧、10 条需先补全已知 child alias”只进入 Task 10 候选；未通过
canonical/legacy fixture、93 样本与前端契约前，不能静默缩小 production 输入集合。

能力位至少区分 video、gallery、audio、live-photo、m3u8。parser 构造函数必须纯净、零 I/O；依赖注入
受控 HTTP client、clock、短期 token provider 和 logger，不能自行读取环境、构造 client 或启动进程。
`Parse` 一次获取并解析上游快照，再返回完整结果；getter 不得各自重复发起请求。注册表测试覆盖唯一性、
稳定路由、声明能力与结果的一致性、未知 host typed failure、构造零 I/O 和单次 parse 的上游 fetch 次数。

### 输入目录与出口 authority 分离

上游集中 `DOMAIN_TO_NAME` 的真正优点是可审计的用户输入识别，但它没有解决 parser 内固定 API host
散落的问题。任务 4 将 authority 按用途拆成 `InputShare`、`MetadataAPI`、
`SessionBootstrap/SessionConsumer` 与 `MediaCandidate`：输入 HostRule 不能自动授权同域 API；固定
metadata endpoint 必须 exact 归属唯一 parser；session 材料只交给 exact consumer；动态 CDN 只取得
无 Cookie/Authorization/session/Origin/跨源 Referer 的公网 fetch 能力。`api.bilibili.com` 之类 endpoint
可由 Bilibili metadata purpose 使用，但绝不能作为用户输入路由或被其他 parser 借用。

首次请求、每跳 redirect 和实际 dial 都校验 parser key + purpose + authority policy fingerprint；跨
purpose redirect fail closed。任务 4 的 `TestPurposeScopedOutboundAuthority`、
`TestEveryNativeFixedEndpointHasPolicyOwner`、`TestParserAPIAuthorityCannotBeUsedAsInputRoute`、
`TestSensitiveHeadersNeverReachDynamicMediaCandidateHost` 和 `TestCrossPurposeRedirectFailsClosed` 共同形成
可执行门禁。

### URL 提取与规范化

任务 7 增加表驱动 extractor/canonicalizer：只接受 HTTP(S)，从分享文本提取单一候选，先按 descriptor
规范化 host/path，再只保留该平台明确允许的 query key。含 capability/会话性质的 query 值不得写日志、
错误、缓存 key 或证据；缓存/锁需要身份时使用带用途域分离的不可逆摘要。

类型层明确区分只供受控 client 使用且不可格式化到日志的 `FetchURL`、允许持久化/返回的 `SafeURL`，以及
绑定 platform/parser version/result schema version 的不可逆 `CacheKey`。禁止盲目把 HTTP 字符串改为
HTTPS；是否支持 TLS 必须由平台策略与真实受控连接验证。

任何 URL 都必须在首次网络请求前完成 SSRF 校验，且每次 DNS 解析、实际 dial 和每一跳 redirect 都由
任务 4 的 netguard 重新验证。跨 origin/host redirect 必须剥离 Cookie、Authorization 和平台会话 header；
header/body/解压后 body 都有硬上限。canonicalizer 只做纯函数变换，不能自行联网或绕过统一 transport。

### 富媒体结果与兼容投影

任务 3 的内部模型增加：

```text
ImageAsset{URL, LivePhotoURL}
MediaCandidate{URL, Kind, Quality, Bitrate, Width, Height, SourceRank}
```

所有候选先经协议、SSRF、大小/类型和结果数量门禁。候选排序必须由明确元数据与稳定 tie-breaker 决定，
缺失元数据时保留 parser 声明顺序；不能假定某个数组下标天然是“最高质量”或“源站”。失败切换受统一
超时/次数预算约束。

任务 7 的兼容层继续原样输出当前前端使用的 `images` 字符串数组；另以可选、加法字段 `imageAssets`
表达静态图与 Live Photo 配对，现有 `video/music/downloads` alias 不变。契约测试必须证明旧前端响应
字节形状不因无 Live Photo 的结果改变，并验证含 Live Photo 时配对索引稳定、所有 URL 为 HTTPS 且
均经过 media/netguard 校验。

### 研究线索的准入

任务 10 可维护独立 `tests/research/media-parser/` 候选夹具，但不得把它混入
`tests/baseline/fixtures/platform-samples.json`。ucmao/media-parser 不得作为 93 样本基线权威，也不能
改变 `success >= 62`、`durationMs <= 216000` 或 canonical fixture hash。候选平台只有在独立合法样本、
当前 API 契约、SSRF/资源限制和稳定性测试全部通过后，才能由单独变更进入 production registry。

任务 12 增加 registry/URL/富媒体差分契约：同一批准输入在 native 与兼容投影中平台 key、视频/图集
类型和旧字段保持一致；研究夹具失败只能报告脱敏分类，不能回显原始分享 URL/query。

任务 5/7 的结果缓存以 `platformKey + canonical resource ID + parserVersion + resultSchemaVersion` 组成
域分离 key，并对同一资源使用 singleflight。仅稳定、明确可缓存的失败进入短 TTL 负缓存；context 取消、
内部错误、凭据缺失、schema changed 和安全拒绝不得污染普通负缓存。force refresh 同时绕过正/负缓存。

任务 7 的内部 typed error 至少区分 `invalid_input`、`unsupported`、`credential_required`、
`upstream_timeout`、`upstream_blocked`、`empty_media`、`schema_changed` 和 `internal`，并携带 stage/platform/
retryable；外部仍只映射当前前端固定 `code/msg/data/requestId`，不得引入研究项目的 `retcode/retdesc`。

任务 8/9 保留 MySQL lease worker 和全局/平台 bulkhead；DASH 音视频并行最多 2 个子任务，且只能在有界
异步媒体任务中运行。context 必须贯穿 HTTP、worker 与 subprocess；取消/超时杀死进程组并清理临时文件。

## 明确拒绝的做法

- 固定 tree 的快手 parser 含大段硬编码 Cookie/用户会话样材料，Douyin 含固定 ttwid/webid；这些内容只
  作为“不得复制”的审计证据，不能进入源码、fixture、日志或 provenance artifact。
- TikTok 实际调用第三方 `tikwm.com`，多个网络路径无 timeout，并存在大量 bare `except`/静默吞错；
  README 的平台支持与测试声明也没有仓库内 tests/CI 佐证。因此它们不是 native/fallback 准入证据。
- 上游 Dockerfile/Compose 的可变基础 tag、root/`chmod 777`、运行时镜像源和本地 `build:` 全部拒绝；
  当前项目继续 Actions-only immutable image、目标机 pull-only，本服务器也不得编译镜像。
- `utils/web_fetcher.py:16-41` 在第一次 GET 之后才检查受支持域名。我们要求首次网络请求前完成 SSRF 校验，
  后续每跳继续校验，绝不复用这个顺序。
- 上游多个位置会记录原始 URL或异常文本。我们的日志和 evidence 不保存完整 path/query、Cookie、
  capability、重定向位置或上游响应片段。
- `src/parsers/douyin_parser.py:24,43-44,84,97` 包含关闭告警/TLS 校验及固定会话材料的模式。生产代码
  不得关闭 TLS 校验，不得硬编码会话或反爬材料，也不得在动态获取失败时回退到源码常量。
- `src/parsers/base_parser.py:69-97` 使用宽权限目录、无下载总超时/大小限制，并可能在请求失败后继续使用
  response。我们的下载只能写 0700/受控根、0600 临时文件，执行超时/并发/字节/媒体类型门禁并原子落盘。
- `src/api/parse.py:70-97` 的广泛异常吞噬会丢失可靠错误分类。我们保留 typed error、request ID 和可恢复性
  分类，只向客户端暴露固定脱敏信息。
- import 时加载环境/创建目录、parser 构造时联网，以及中文展示名作为内部主键都属于不可测试副作用；
  production 构造/注册必须纯净，环境只在 typed config 边界读取。
- 类级全局缓存、可变依赖、本地 Compose build，以及缺少仓库内 tests/CI 的现状都不进入设计。README
  提到的测试目录与实际固定 tree 不一致，因此功能声称只以固定 tree 的代码和可运行测试为证据。

## 验收门槛

- provenance policy 精确校验 repository/branch/commit/tree/license hash、`codeCopied=false` 和
  `baselineAuthority=false`。
- registry 单测证明 key/alias/domain 无冲突、路由确定、能力声明一致、一次 Parse 最多一次上游抓取。
- URL 表格测试覆盖需保留 query、需剥离 tracking query、Unicode/大小写/端口、恶意 host 后缀、首次请求
  前 SSRF 拒绝、重定向到私网、DNS rebinding 和日志脱敏。
- 富媒体测试覆盖 video/gallery/audio/live-photo、候选稳定排序/有界 fallback、旧 `images` 兼容投影和
  `imageAssets` 加法字段。
- 独立研究候选不得改变 canonical 93 样本集合或生产支持矩阵；任何未来代码复制必须先更新许可证归属
  与 policy 测试。
