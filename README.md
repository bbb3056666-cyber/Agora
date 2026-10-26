# Agora

Agora 是一个面向资讯、社区及综合应用首页场景的内容分发与互动系统，统一支持视频、1～9 张图片和长文章。三类内容共享发布、时间线、关注流、热门流、点赞、评论、通知、话题和活动能力，并按照发布时间、互动热度与关注关系进行规则分发。

系统采用 React/TypeScript 前端与 Go/Gin API + Worker 后端架构：MySQL 保存业务事实，Redis 负责三级缓存中的 L2、ZSET 时间线与热榜、限流和活动名额预扣，RabbitMQ 承担可靠异步分发与通知解耦；三个 API 实例通过 Nginx 对外提供统一入口，并使用 distcache 组成内容详情的多节点进程内缓存。

完整的业务流程、架构取舍与可靠性设计见 [Agora 系统设计说明](Agora-系统设计说明.html)。

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
| 缓存 | 内容详情使用 distcache 构建多节点 L1，结合一致性哈希、节点间读取、singleflight 与失效广播，并与 Redis L2、MySQL L3 组成三级缓存；Feed 批量查询保留 Redis MGET 路径 |
| 可靠消息 | Publisher Confirm、事件幂等、延迟重试和死信队列 |
| 实时通知 | MySQL 持久化通知，Redis Pub/Sub 跨进程广播，SSE 推送在线用户 |

点赞意图按内容 ID 路由到 8 个固定队列，每个队列启用 RabbitMQ 单活消费者；Worker 在最多 10 ms、32 条消息的窗口内按内容聚合，以实际点赞状态变化计算增量，并在事务末尾一次更新该内容的计数。旧队列仍有消费者处理切换前的待处理消息；部署时先启动带新字段迁移的 API，再更新 Worker，并观察旧队列及其重试队列是否清空。分区数变更会改变路由，不能直接修改常量上线。

点赞数定期校准只作用于新发布且初始点赞数为 0 的内容；历史内容和手动设置过计数的内容默认不参与，以免覆盖缺少点赞关系记录的初始计数。校准通过计数版本做条件更新，遇到并发修改会跳过本轮。分区与聚合后的吞吐需重新压测，不能沿用旧版性能数据。
| 限额活动 | Redis Lua 原子预扣，MQ 异步落库，MySQL 条件扣减和唯一索引兜底 |

## 一键启动

需要 Docker Engine 和 Docker Compose v2：

```bash
cp .env.example .env
docker compose up -d --build
docker compose ps
```

默认启动八个容器：`mysql`、`redis`、`rabbitmq`、`api1`、`api2`、`api3`、`gateway` 和 `worker`。`gateway` 镜像同时提供 React 单页应用和 Nginx 网关，Nginx 使用 `least_conn` 将 API 请求分发到三个 API；三个 API 各自运行一个 distcache 节点，并共享上传卷。

- Web 与 API 统一入口：`http://localhost`
- 健康检查：`http://localhost/healthz`
- MySQL：`localhost:3306`
- Redis：`localhost:6379`
- RabbitMQ AMQP / 管理台：`localhost:5672` / `http://localhost:15672`
- API pprof：`http://127.0.0.1:6060/debug/pprof/`、`6061`、`6062`
- Worker pprof：`http://127.0.0.1:6063/debug/pprof/`

前端本地开发：

```bash
cd frontend
npm install
npm run dev
```

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
