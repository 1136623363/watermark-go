# Parser boundary

本目录包含统一 Parser/Descriptor/Result/Registry 契约及固定来源的适配器。

- `native/`：26 个 Go adapter；所有 HTTP 经注入的 `internal/netguard` client。
- `universal/`：只构造结构化 helper command；无隔离 Runner/已验证 GuardProxy 时 fail-closed。
- `ytdlp/`：固定 binary、argv、最小 env、唯一 proxy 和 process-group 契约。

生产运行时不支持同步、热更新或加载可变解析器。平台 metadata 只来自 descriptor catalog；第三方研究候选不能在未更新 golden、契约测试与安全审查时进入 registry。

所有 URL 路由只依据 registry 的规范 host rule，未知 host 不猜测；一次 Parse 内的 DNS、redirect、物理请求和总 deadline 共用同一 RequestBudget。universal/yt-dlp 除隔离 executor 与已验证 loopback proxy 外，还必须获得不可伪造的 image path provenance policy；Task 4 完成这三项接线前 production 始终 fail-closed。
