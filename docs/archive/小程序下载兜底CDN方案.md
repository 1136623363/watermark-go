# 小程序下载兜底 CDN 方案

## 目标

小程序保存视频、图片、音频时，优先直接下载解析结果中的原始 CDN 地址；只有原始 CDN 多次失败时，才启用服务器短期兜底下载能力。

目标不是让服务器长期承担下载代理，而是在用户确实需要保存、且原 CDN 被小程序环境限制或拒绝时，提供一次受控的兜底保存路径。

## 结论

当前不建议实现 Go 服务实时流式下载代理。

推荐实现“服务端临时下载缓存 + Nginx 静态文件返回”的兜底 CDN：

1. 小程序前 3 次使用原始 CDN 地址保存。
2. 第 4 次向后端申请兜底下载地址。
3. 后端重新解析并拉取最新媒体 URL。
4. 后端把媒体文件先下载到临时缓存目录。
5. 下载完成后返回自有域名短期 URL。
6. 小程序通过 `wx.downloadFile` 下载这个自有域名 URL。
7. 临时文件按 TTL、容量和任务状态自动清理。

## 为什么不做实时下载代理

实时代理链路是：

```text
小程序 -> watermark-backend -> 原始 CDN
```

这个模式会让后端承担长连接、双向带宽和大量上游连接。视频文件可能几十 MB 到几百 MB，一旦并发上来，解析服务、后台管理、任务系统和集群调度都会被下载流量拖慢。

当前部署更适合 API 和解析任务：

- `/api/parse` 面向短请求，Nginx 读超时约 35 秒。
- 普通 `/api/` 面向短 API，读超时约 20 秒。
- 后端容器同时负责解析、缓存、后台、任务和节点调度。
- 还没有为大文件 Range、限速、断点续传、盗链防护设计专用下载通道。

因此，实时代理不是不能做，而是不适合作为当前阶段的主方案。

## 推荐架构

```text
小程序
  |
  | 1. 原 CDN 下载，最多 3 次
  v
原始媒体 CDN

如果失败：

小程序
  |
  | 2. 申请兜底下载任务
  v
watermark-backend
  |
  | 3. 重新解析最新媒体地址
  | 4. 服务端下载到临时缓存
  v
/app/cache/download-fallback
  |
  | 5. 返回短期自有域名 URL
  v
Nginx 静态文件
  |
  | 6. wx.downloadFile 保存
  v
小程序相册/文件系统
```

## 小程序重试策略

参考 `wx47d026b451350289` 的思路，小程序保存时最多尝试 4 次：

| 次数 | 下载来源 | 行为 |
| --- | --- | --- |
| 第 1 次 | 原 CDN | 直接使用解析结果中的媒体 URL |
| 第 2 次 | 原 CDN | 使用解析结果中的下一个候选 URL，或继续尝试原始直链 |
| 第 3 次 | 原 CDN | 最后一次直连尝试，最多只尝试 3 个原始 CDN 候选 |
| 第 4 次 | 自有兜底 CDN | 请求后端创建临时下载文件，返回自有域名 URL |

第 4 次只在用户主动点击保存时触发，不能在解析成功页预加载，避免无意义占用服务器带宽和磁盘。

本轮实现先不在保存阶段反复重新解析分享链接，避免一次保存动作额外放大解析压力；后续如果需要，可以在第 2、3 次直连前增加“强制刷新解析结果”的开关。

## 后端接口设计

### 1. 创建兜底下载任务

```http
POST /api/download/fallback
Content-Type: application/json
```

请求：

```json
{
  "sourceUrl": "https://v.douyin.com/xxxx/",
  "mediaUrl": "https://origin-cdn.example.com/video.mp4",
  "mediaType": "video",
  "shareId": "optional-cache-share-id"
}
```

字段说明：

- `sourceUrl`：原始分享链接，优先用于重新解析。
- `mediaUrl`：当前解析结果中的媒体 URL，可作为兜底输入。
- `mediaType`：`video`、`image`、`audio`。
- `shareId`：解析结果缓存 ID，可选。

响应：

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "taskId": "fb_20260518_xxx",
    "status": "running",
    "pollUrl": "/api/download/fallback/fb_20260518_xxx"
  }
}
```

如果文件已经存在且未过期，可以直接返回：

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "taskId": "fb_20260518_xxx",
    "status": "completed",
    "downloadUrl": "https://watermark.bxsn.cn/api/download/file/video_xxx.mp4?expires=...&token=...",
    "expiresAt": "2026-05-18T15:30:00+08:00"
  }
}
```

### 2. 查询兜底下载任务

```http
GET /api/download/fallback/:taskId
```

响应：

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "taskId": "fb_20260518_xxx",
    "status": "completed",
    "progress": 100,
    "downloadUrl": "https://watermark.bxsn.cn/api/download/file/video_xxx.mp4?expires=...&token=...",
    "expiresAt": "2026-05-18T15:30:00+08:00"
  }
}
```

状态：

- `pending`：等待下载。
- `running`：正在下载。
- `completed`：已完成，可返回自有域名 URL。
- `failed`：下载失败。
- `expired`：文件已过期。

## 文件存储设计

建议目录：

```text
/app/cache/download-fallback
  /video
  /image
  /audio
```

生产环境映射到持久化目录：

```text
${PERSIST_ROOT}/backend/cache/download-fallback
```

worker 节点可使用：

```text
${WORKER_PERSIST_ROOT}/backend/cache/download-fallback
```

但第一阶段建议只在 gateway 主节点提供兜底下载，避免多节点文件同步问题。

## 文件命名

文件名不能使用用户 URL 原文，应该使用稳定哈希：

```text
{mediaType}_{sha256(sourceUrl + mediaUrl)}.{ext}
```

示例：

```text
video_8f1c2a...9b3.mp4
audio_98ac42...ee1.mp3
image_a13fb2...817.jpg
```

扩展名来源优先级：

1. `Content-Type`
2. 原媒体 URL path
3. `mediaType` 默认值

## Nginx 访问设计

第一阶段先由 Go 后端提供签名文件接口：

```http
GET /api/download/file/:key?expires=...&token=...
```

该接口校验 token 后使用 `http.ServeContent` 返回文件，支持 Range 请求。Nginx 需要把 `/api/download/` 固定转发到 gateway 主节点，避免 worker 没有本地兜底文件。

后续可优化为静态路径或 `X-Accel-Redirect`：

```nginx
location /cdn/tmp/ {
    alias /app/cache/download-fallback/public/;
    add_header Cache-Control "private, max-age=1800";
    add_header Accept-Ranges bytes;
    limit_rate_after 5m;
    limit_rate 2m;
}
```

生产环境需要把容器内静态目录挂载给 Nginx：

```yaml
volumes:
  - ${PERSIST_ROOT}/backend/cache/download-fallback/public:/app/cache/download-fallback/public:ro
```

注意：Nginx 只负责返回已经下载完成的文件，不负责反向代理原始 CDN。

## 安全策略

### 1. 签名 URL

返回 URL 必须包含短期 token：

```text
/cdn/tmp/video_xxx.mp4?expires=...&token=...
```

token 生成：

```text
HMAC_SHA256(secret, fileKey + expires + mediaType)
```

后端或 Nginx 鉴权模块校验：

- token 正确。
- 未过期。
- 文件存在。
- mediaType 匹配。

第一阶段可以由后端提供 `/api/download/file/:key` 校验后 `X-Accel-Redirect` 给 Nginx，避免直接暴露裸文件。

### 2. 文件大小限制

建议默认：

| 类型 | 最大大小 |
| --- | --- |
| video | 300 MB |
| audio | 50 MB |
| image | 20 MB |

超过上限直接失败，返回明确错误：

```json
{
  "code": 1001,
  "msg": "文件过大，暂不支持服务器兜底下载"
}
```

### 3. 并发限制

建议第一阶段：

- 全局兜底下载并发：2
- 单 IP 兜底任务并发：1
- 单 IP 每小时创建任务数：10
- 单用户/设备每天创建任务数：20

### 4. 域名白名单与协议限制

只允许：

- `http`
- `https`

禁止：

- 内网 IP
- localhost
- file 协议
- ftp 协议
- 重定向到内网地址

必须防 SSRF。

### 5. 内容类型校验

下载前后都要校验：

- `Content-Type`
- 文件头 magic bytes
- 文件扩展名

避免把 HTML、错误页、脚本文件当作媒体文件返回给小程序。

## 清理策略

建议配置：

```text
DOWNLOAD_FALLBACK_TTL_SECONDS=3600
DOWNLOAD_FALLBACK_MAX_TOTAL_BYTES=10737418240
DOWNLOAD_FALLBACK_MAX_FILE_BYTES=314572800
DOWNLOAD_FALLBACK_CLEAN_INTERVAL_SECONDS=300
```

默认：

- 文件保留 1 小时。
- 总缓存不超过 10 GB。
- 每 5 分钟清理一次。
- 优先清理过期文件。
- 超过容量时按最旧访问时间清理。

## 数据表设计

建议新增表 `download_fallback_tasks`：

```sql
CREATE TABLE download_fallback_tasks (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  task_id VARCHAR(64) NOT NULL,
  source_url TEXT,
  media_url TEXT,
  media_type VARCHAR(32) NOT NULL,
  file_key VARCHAR(128) NOT NULL,
  file_path TEXT,
  file_size BIGINT DEFAULT 0,
  content_type VARCHAR(128),
  status VARCHAR(32) NOT NULL,
  progress INT DEFAULT 0,
  error_message TEXT,
  client_ip VARCHAR(64),
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  expires_at TIMESTAMP NULL,
  UNIQUE KEY uk_task_id (task_id),
  KEY idx_status (status),
  KEY idx_expires_at (expires_at),
  KEY idx_file_key (file_key)
);
```

第一阶段如果不想加表，也可以用 Redis/MySQL 现有任务系统扩展，但最终建议落表，方便后台管理和清理审计。

## 小程序流程

保存视频：

```text
用户点击保存
  |
  v
重新解析最新结果
  |
  v
第 1 次下载原 CDN
  |
  | 失败
  v
第 2 次重新解析 + 下载原 CDN
  |
  | 失败
  v
第 3 次重新解析 + 下载原 CDN
  |
  | 失败
  v
申请服务端兜底下载
  |
  v
轮询任务状态
  |
  v
拿到自有域名 URL
  |
  v
wx.downloadFile 保存
```

错误提示建议：

- 前 3 次失败：不弹出，只更新进度文案。
- 第 4 次创建任务中：显示“正在准备备用下载地址”。
- 兜底下载失败：自动复制原始媒体链接，提示可到浏览器保存。

## 后台管理系统

建议在“任务与日志”或新增“下载兜底”页面展示：

- 当前兜底任务数。
- 正在下载任务。
- 失败任务。
- 临时文件占用空间。
- 今日兜底流量。
- 单任务详情：平台、大小、耗时、失败原因、来源节点。
- 手动清理过期文件。
- 手动禁用兜底功能。

## 配置项

建议新增：

```text
DOWNLOAD_FALLBACK_ENABLED=false
DOWNLOAD_FALLBACK_PUBLIC_BASE_URL=https://watermark.bxsn.cn
DOWNLOAD_FALLBACK_TTL_SECONDS=3600
DOWNLOAD_FALLBACK_MAX_FILE_BYTES=314572800
DOWNLOAD_FALLBACK_MAX_TOTAL_BYTES=10737418240
DOWNLOAD_FALLBACK_CONCURRENCY=2
DOWNLOAD_FALLBACK_RATE_LIMIT_PER_IP_HOUR=10
DOWNLOAD_FALLBACK_TOKEN_SECRET=change_me
DOWNLOAD_FALLBACK_TMP_DIR=/app/cache/download-fallback
```

默认必须关闭，等 Nginx、清理任务、限流和监控都完成后再打开。

## 分阶段落地

### 阶段 1：只做文档和开关

- 写入本方案。
- 增加配置项设计。
- 不实现下载能力。

### 阶段 2：实现任务模型和后台可见性

- 新增任务表。
- 新增后台“下载兜底”页面。
- 支持创建任务但不开放给小程序。
- 本地测试下载、清理、失败状态。

### 阶段 3：实现临时文件下载

- 服务端下载原 CDN 到临时目录。
- 限制大小、Content-Type、并发。
- 下载完成后返回短期 URL。
- Nginx 静态返回文件。

### 阶段 4：接入小程序第 4 次兜底

- 小程序前三次仍使用原 CDN。
- 第四次才调用兜底接口。
- 保存失败时复制链接作为最后降级。

### 阶段 5：灰度上线

- 只对少量用户或后台开关开启。
- 观察磁盘、带宽、下载耗时、失败率。
- 确认稳定后再扩大。

## 风险

| 风险 | 处理方式 |
| --- | --- |
| 带宽被打满 | 限速、限并发、按 IP 限流 |
| 磁盘被占满 | TTL 清理、总容量限制 |
| 被盗链 | 短期签名 URL |
| SSRF | URL 协议、IP、重定向校验 |
| 大文件拖垮服务 | 文件大小上限、任务队列 |
| 平台封禁 | 只在用户保存失败时触发，不主动批量下载 |
| 微信审核风险 | 海外平台仍按既有规则限制分享，仅提供保存兜底 |

## 本轮实现范围

本轮按“阶段 3 + 阶段 4 的最小可用闭环”实现，先不引入数据库任务表和独立后台页面，避免把迁移、审计、报表一次性做复杂。

已实现范围：

1. 后端新增 `POST /api/download/fallback` 创建兜底下载任务。
2. 后端新增 `GET /api/download/fallback/:taskId` 查询任务。
3. 后端新增 `GET /api/download/file/:key` 返回短期签名文件，支持 HTTP Range。
4. 兜底文件保存到 `/app/cache/download-fallback/public`，临时文件保存到 `/app/cache/download-fallback/tmp`。
5. 对 `mediaUrl` 执行协议、域名、内网 IP、重定向目标校验，降低 SSRF 风险。
6. 限制默认视频最大 300 MB、音频 50 MB、图片 20 MB。
7. 默认兜底并发 2，任务只在用户保存失败后触发。
8. 小程序视频保存最多尝试 3 个直链候选，第 4 次调用服务器兜底接口。
9. Nginx 将 `/api/download/` 固定转发到 gateway 主节点，避免 worker 本地没有兜底文件。
10. Jenkins 部署时创建持久化目录，并在 gateway 启用兜底、worker 禁用兜底。

暂不实现范围：

1. MySQL 兜底任务表。
2. 后台“下载兜底”独立页面。
3. Nginx 静态目录直出或 `X-Accel-Redirect`。
4. 多节点兜底文件同步。
5. 按用户/设备的精细额度扣减。

当前实现可以先用于验证抖音 CDN `invalid_referer`、302 跳转域名不断变化时的小程序保存兜底效果。稳定后再补任务表、后台可视化和更细粒度限流。
