# Admin Console

后台管理系统拆成两个清晰区域：

- `web/`：后台管理系统前端资源，当前为 Go embed 的 HTML 模板。
- 后台 API 与页面路由目前由 `internal/server/admin_*.go` 承载，文件统一以 `admin_` 前缀标识，避免与开放 API、解析流程混在一起。

后续如果后台管理继续变大，可以把 `internal/server/admin_*.go` 逐步抽到 `internal/admin/backend`，通过 service/interface 与 `internal/server` 解耦。
