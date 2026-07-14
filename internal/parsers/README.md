# Parsers

这里放后端可切换的解析器实现。

- `native/`：项目原生 Go 解析器，包含已有的站点适配器与 ID/URL 解析能力。
- `universal/`：聚合备用解析器，通过 Python bridge 调用 CharlesPikachu 的 `videodl` / `musicdl` 源码。

后台管理系统的“运行设置”可以切换主解析器：

- `native`：优先使用内置 Go 解析器，适合作为默认稳定解析器。
- `universal`：优先使用 videodl/musicdl 聚合解析器，适合作为备用或扩展平台测试。

`parserFallbackEnabled` 开启后，主解析器失败会自动尝试另一套解析器。
