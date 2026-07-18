# 自动化测试与验收证据

本目录承载 watermark-go 的 Go 契约测试、pytest 服务端到端测试、解析基准门禁和运维脚本测试。测试必须明确区分“通过”“失败”和“依赖未就绪”；不得把未执行或跳过关键依赖伪装为成功。

当前 Task 12 已补入 hermetic Go 前端契约、固定小程序快照 provenance guard、以及面向运行中服务的 pytest E2E 草案。本地 Task 12 验证不启动服务；服务级 E2E 由后续 runner 或已拉取的不可变运行镜像执行。

## 目标结构

- `contracts/frontend_contract_test.go`：锁定当前小程序的 session、同步/异步解析、分享缓存、下载兜底、m3u8 与 performance 协议。
- `e2e/test_frontend_flow.py`：对运行中服务复现当前前端完整流程。
- `e2e/test_admin_api_contracts.py`、`e2e/test_admin_and_fallback.py`：后台单机能力、认证、输入边界和下载兜底回归。
- `baseline/fixtures/platform-samples.json`：96 个版本化样本，其中 93 个启用。
- `baseline/test_report.py`：验证三项硬门槛和报告完整性。
- `ops/test_scripts.py`：部署、回滚、镜像验证、主机快照和资源停止线的脚本测试。

## 执行方式

Task 12 的本地入口为：

```bash
go test ./tests/contracts -count=1
python3 -m py_compile tests/e2e/conftest.py tests/e2e/test_*.py
FRONTEND_REPO=/srv/watermark scripts/verify-frontend-provenance.sh
for f in /srv/watermark/test/test_miniprogram_*.js; do node "$f"; done
FRONTEND_REPO=/srv/watermark scripts/verify-frontend-provenance.sh
```

运行中服务的 pytest E2E 不在本机 Task 12 启动服务；后续 CI/验收环境准备好服务后执行
`python3 -m pytest tests/e2e -q`，若服务依赖未就绪必须失败，不得以 skip 伪装成功。

Task 14 完成后，再执行：

```bash
python3 -m pytest tests/baseline tests/ops -q
```

本地 pytest 的临时 HTML、JUnit、JSON 和诊断报告写到被忽略的 `reports/`；需要提交的脱敏验收证据统一写到 `artifacts/`，并记录对应服务 commit 与镜像 digest。

## 测试环境变量

- `E2E_BASE_URL`：测试目标地址，默认 `http://127.0.0.1:5001`。
- `E2E_TIMEOUT_SECONDS`：普通 API 请求超时。
- `E2E_DOWNLOAD_TIMEOUT_SECONDS`：下载链路超时。
- `E2E_ADMIN_USERNAME`：后台测试账号。
- `E2E_ADMIN_PASSWORD`：后台测试密码；本地缺省仅使用 `invalid-for-test-only`。
- `E2E_CLIENT_SIGNATURE_KEY`：兼容旧测试服务的签名材料；本地缺省仅使用可正常执行 AES 夹具的明显无效占位 `example-test-key`。
- `E2E_MEDIA_URL`、`E2E_SOURCE_URL`、`E2E_M3U8_URL`：显式提供的公开测试样本地址。
- `E2E_RUN_SLOW=1`：显式开启受网络影响的慢速检查。

生产凭据不得作为测试默认值，不得写入夹具、报告或命令行输出。
