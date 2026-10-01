# Agora

Agora 是一个事件驱动的内容分发与互动系统，统一支持视频、1～9 张图片和长文章。三类内容共享发布、时间线、关注流、热门流、点赞、评论、通知、话题和活动能力，并按照发布时间、互动热度与关注关系进行规则分发。

后端采用 Go/Gin 的 API + Worker 架构：MySQL 保存业务事实，Redis 负责三级缓存中的 L2、ZSET 时间线与热榜、限流和活动名额预扣，RabbitMQ 承担可靠异步分发与通知解耦。

详细流程和面试问答见 [Agora 项目指南](Agora-流程与面试问答.html)。

## 内容模型

`contents` 保存所有内容共有字段：作者、类型、标题、描述、正文、发布时间、点赞数和热度。`content_assets` 保存有序媒体资源，多图内容通过 `(content_id, asset_type, sort_order)` 保证顺序唯一。

| content_type | 类型数据 |
| --- | --- |
| `video` | `play_url`、`cover_url`，支持普通上传和分块续传 |
| `image` | `content_assets` 中 1～9 条有序图片 URL，第一张默认作为封面 |
| `article` | `body` 长正文，可选封面 |

Feed 的 Redis ZSET 只保存 `ContentID + 发布时间`，不区分内容类型；读取内容实体后再根据 `content_type` 渲染。因此最新流、热门流和关注流不需要为每种内容分别维护。

## 核心能力

| 模块 | 能力 |
| --- | --- |
| 账号安全 | JWT Access Token、Refresh Token Rotation、Token Version、黑名单与复用检测 |
| 内容发布 | 视频、图片、文章统一发布；内容、资源、话题关系和 Outbox 同事务提交 |
| 内容分发 | 最新流冷热分离、热门流分钟桶衰减、关注流普通作者写扩散与大 V 读扩散 |
| 互动 | 点赞、取消点赞、评论、关注；Intent → Outbox → Worker → Applied 事件链路 |
| 缓存 | 本地缓存 + Redis + MySQL 三级读取，singleflight 抑制热点内容并发回源 |
| 可靠消息 | Publisher Confirm、事件幂等、延迟重试和死信队列 |
| 实时通知 | MySQL 持久化通知，Redis Pub/Sub 跨进程广播，SSE 推送在线用户 |
| 限额活动 | Redis Lua 原子预扣，MQ 异步落库，MySQL 条件扣减和唯一索引兜底 |

## 一键启动

需要 Docker Engine 和 Docker Compose v2：

```bash
cp .env.example .env
docker compose up -d --build
docker compose ps
```

默认启动五个容器：`mysql`、`redis`、`rabbitmq`、`backend` 和 `worker`。

- API：`http://localhost:8080`
- 健康检查：`http://localhost:8080/healthz`
- RabbitMQ 管理台：`http://localhost:15672`
- API pprof：`http://127.0.0.1:6060/debug/pprof/`
- Worker pprof：`http://127.0.0.1:6061/debug/pprof/`

## 内容接口

除特别说明外均为 `POST`，受保护接口使用 `Authorization: Bearer <access_token>`。

| 接口 | 作用 |
| --- | --- |
| `/content/upload/video` | 上传完整视频 |
| `/content/upload/images` | 一次上传 1～9 张图片，字段名为 `files` |
| `/content/upload/cover` | 上传封面 |
| `/content/chunk/*` | 大视频分块上传、状态查询与合并 |
| `/content/publish` | 按 `content_type` 发布视频、图片或文章 |
| `/content/getDetail` | 获取统一内容详情 |
| `/content/listByAuthorID` | 获取作者内容列表 |
| `/content/delete` | 作者删除内容 |

视频发布示例：

```json
{
  "content_type": "video",
  "title": "Go 并发实践 #Go",
  "description": "Worker 与 Outbox",
  "play_url": "https://example.com/a.mp4",
  "cover_url": "https://example.com/a.jpg"
}
```

多图发布示例：

```json
{
  "content_type": "image",
  "title": "城市夜景 #摄影",
  "description": "九宫格内容",
  "image_urls": [
    "https://example.com/1.jpg",
    "https://example.com/2.jpg"
  ]
}
```

文章发布示例：

```json
{
  "content_type": "article",
  "title": "事务 Outbox 的实现",
  "body": "文章正文……",
  "cover_url": "https://example.com/outbox.jpg"
}
```

## 可靠发布流程

```text
API 校验不同内容类型的必填字段
→ MySQL 事务写 contents
→ 多图内容批量写 content_assets
→ 提取并写 tags / content_tags
→ 写 content_published Outbox
→ 事务提交后返回
→ Worker 进程中的 Outbox Poller 批量投递 RabbitMQ
→ Publisher Confirm 成功后清理 Outbox
→ Timeline Worker 将 ContentID 写入 Redis 时间线
```

## 验证

```bash
cd backend
go test ./...
```

生产或公开部署前必须替换 `.env` 中的默认密码与 JWT 密钥。
