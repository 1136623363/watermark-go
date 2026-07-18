# Admin Console

后台管理系统拆成两个清晰区域：

- `web/`：后台管理系统前端资源，当前为 Go embed 的 HTML 模板。
- `service.go`、`auth.go` 和 `baseline.go`：后台认证、审计、汇总与基线执行领域逻辑。
- `internal/httpapi/admin_handlers.go`：后台 HTTP API 适配层，负责把 Gin 请求转换为 admin service 调用。

后续如果后台管理继续变大，应继续保持 `internal/admin` 的领域逻辑与 `internal/httpapi` 的协议适配解耦。
