# POST /feed/listLatest 测试与压测记录

## 第三步：测试对象建模与接口调用链分析——`listLatest`

> 分析对象：`POST /feed/listLatest`。本节仅依据当前代码整理，不包含测试执行、压测脚本或性能方案。

### 1. 接口协议

| 项目 | 真实代码行为 |
| --- | --- |
| 请求方法 | `POST` |
| 请求路径 | `/feed/listLatest` |
| Path 参数 | 无 |
| Query 参数 | Handler 未读取 Query 参数 |
| Header 参数 | `Authorization: Bearer <Access Token>` 为可选。路由使用 `SoftJWTAuth`：不携带 Header 时以游客身份继续；携带格式错误、无效、过期或已撤销的 Token 时返回 `401`。代码未显式校验 `Content-Type`。 |
| Body 参数 | JSON，对应 `feed.ListLatestRequest` |
| 成功响应 | `200 OK`，JSON `feed.ListLatestResponse`：`video_list`、`next_time`、`has_more` |
| 失败响应 | JSON `{"error":"..."}` |
| HTTP 状态码 | `200`：成功；`401`：软鉴权拒绝携带的无效 Token；参数绑定失败：`apierror.ClassifyHTTPStatus(err)` 的结果；Service 返回错误：`500`。无 gRPC 状态码。 |

成功响应的真实结构：

```json
{
  "video_list": [
    {
      "id": 1,
      "author": {"id": 2, "username": "user"},
      "title": "title",
      "description": "description",
      "play_url": "...",
      "cover_url": "...",
      "create_time": 0,
      "likes_count": 0,
      "is_liked": false
    }
  ],
  "next_time": 0,
  "has_more": false
}
```

说明：`FeedVideoItem.create_time` 由 `Video.CreateTime.Unix()` 生成，单位为秒；请求字段 `latest_time` 和响应字段 `next_time` 由 `UnixMilli()` 生成，单位为毫秒。

### 2. 请求字段说明

| 字段名 | 类型 | 是否必填 | 合法范围/格式 | 代码中的校验位置 |
| --- | -- | ---- | ------- | -------- |
| `limit` | JSON Number，绑定到 Go `int` | 否 | `1` 到 `50` 时原样使用；小于等于 `0` 或大于 `50` 时自动改为 `10`。未返回参数错误。 | `backend/internal/module/feed/handler.go`：`FeedHandler.ListLatest` |
| `latest_time` | JSON Number，绑定到 Go `int64` | 否 | 大于 `0` 时按 Unix 毫秒转换为分页游标；小于等于 `0` 时不设置游标，按第一页处理。 | `backend/internal/module/feed/handler.go`：`FeedHandler.ListLatest` |
| `Authorization` | HTTP Header，格式为 `Bearer <token>` | 否 | 不传时游客访问；传入后必须是合法且未过期的 JWT。Redis 可用时还校验 Token Version 与 JTI 黑名单；Redis 校验故障时记录日志并放行。 | `backend/internal/delivery/jwt/jwt.go`：`SoftJWTAuth`、`extractToken`、`check` |

补充：`ListLatestRequest` 位于 `backend/internal/module/feed/entity.go`。该结构体没有 Gin `binding:"required"`、数值范围或跨字段校验标签。JSON 不能绑定时，Handler 将错误交给 `apierror.ClassifyHTTPStatus`；该分类函数只显式识别 `ErrValidation`、`ErrUnauthorized` 与 `gorm.ErrRecordNotFound`，其他错误走默认 `500`。具体某一种 Gin 绑定错误最终是否匹配上述自定义错误，代码中未确认。

### 3. 测试前置条件

| 条件 | 是否必须 | 真实代码依据 |
| --- | --- | --- |
| API 进程已启动 | 是 | 路由由 `backend/cmd/main.go` 调用 `http.SetRouter` 注册并由 Gin 监听。 |
| MySQL 可用 | 对获得稳定的非空结果基本需要 | Redis 为空时会读取最新 1000 条视频重建全局时间线；Redis 不可用、冷数据分页、视频实体 L3 缓存未命中时均会读 MySQL。游客且热数据、L1/L2 视频实体缓存全部命中时，本次请求可不读 MySQL。 |
| Redis 可用 | 否 | `FeedService.ListLatest` 在 `redisCache == nil` 或首次读取 ZSET 出错时会降级到 `listLatestFromDB`。Redis 可用时才使用 `feed:global_timeline`、视频 L2 缓存及相关 singleflight 分支。 |
| RabbitMQ 可用 | 否 | 此 HTTP Handler、`FeedService.ListLatest` 与 `FeedRepository` 均不直接调用 MQ。RabbitMQ 和 Timeline Consumer 只影响全局时间线能否被发布链路持续更新，不是本请求的直接前提。 |
| Access Token | 否 | 软鉴权允许游客访问。携带合法 Token 时，从中取得 `viewerAccountID`，用于查询该用户对本页视频的点赞状态。 |
| 已存在视频数据 | 否，但非空结果需要 | 若 `videos` 表为空且 Redis ZSET 为空，Service 返回空 `video_list`、`has_more=false`，不会失败。 |
| 已存在 `feed:global_timeline` | 否 | ZSET 为空时，Service 会从 MySQL 读取最新 1000 条视频并 `ZADD` 重建；重建失败则降级查 MySQL。 |
| 已点赞关系数据 | 否 | 登录用户会批量查 `like_states`；没有记录时 `is_liked=false`。游客 `viewerAccountID=0` 时 `BatchGetLiked` 直接返回空 Map，不查询 `like_states` 表。 |

可能导致请求失败的已确认条件：

- 携带的 `Authorization` Header 格式错误、JWT 无效或过期、Token Version 不一致、JTI 在黑名单中：软鉴权返回 `401`。
- JSON 不能绑定到 `ListLatestRequest`：Handler 立即返回错误；状态码由 `apierror.ClassifyHTTPStatus` 决定。
- Service 中直接返回的错误，例如 MySQL 查询失败、`ZRevRangeByScore` 失败、视频实体查询失败或登录用户的批量点赞查询失败：Handler 返回 `500`。
- Redis 初始 `ZRangeWithScores` 查询失败不会使请求失败，而是降级到 MySQL；Redis 客户端为 `nil` 也直接走 MySQL。

### 4. 实际调用链

```text
POST /feed/listLatest
→ Gin 路由：backend/internal/delivery/http/router.go
  SetRouter()
  → softFeedGroup.POST("/listLatest", feedHandler.ListLatest)
  → 路由组中间件 jwt.SoftJWTAuth(cache)
     ├─ 无 Authorization：直接 c.Next()，游客继续
     └─ 有 Authorization：SoftJWTAuth → auth.ParseToken → check
        → Redis（可用时）读取 token_version 与 blacklist
        → 在 Gin Context 写入 accountID
→ backend/internal/module/feed/handler.go
  FeedHandler.ListLatest(c)
  → c.ShouldBindJSON(&feed.ListLatestRequest)
  → 修正 limit：非 1~50 改为 10
  → latest_time > 0 时：time.UnixMilli 转为 latestBefore
  → jwt.GetAccountID(c)，失败时 viewerAccountID = 0
→ backend/internal/module/feed/service.go
  FeedService.ListLatest(ctx, limit, latestBefore, viewerAccountID)
  ├─ Redis 为 nil：listLatestFromDB(...)
  ├─ Redis ZRangeWithScores(feed:global_timeline, 0, 0) 失败：listLatestFromDB(...)
  ├─ ZSET 为空：singleflight 重建
  │  → FeedRepository.ListLatest(ctx, 1000, 零时间)
  │  → Redis ZAdd(feed:global_timeline, 最新 1000 视频 ID 与创建时间毫秒分数)
  │  → 递归调用 FeedService.ListLatest(...)
  ├─ 游标时间 <= ZSET 最老分数：singleflight 冷数据分支
  │  → FeedRepository.ListLatest(ctx, limit, latestBefore)
  └─ 热数据分支：
     → Redis ZRevRangeByScore(feed:global_timeline, maxScore, -inf, 0, limit)
     → 字符串 videoID 逐个 ParseUint
     → FeedService.GetVideoByIDs(ctx, videoIDs)
        → L1 go-cache
        → L2 Redis MGet(video:entity:ID)
        → L3 FeedRepository.GetByIDs(ctx, []uint{videoID})
        → 回写 L1；异步 JSON 序列化并回写 Redis
     → 热数据不足 limit 时：FeedRepository.ListLatest 补冷数据
  → FeedService.buildFeedVideos(ctx, baseVideos, viewerAccountID)
     → LikeRepository.BatchGetLiked(ctx, videoIDs, viewerAccountID)
     → 逐个 Video 组装 FeedVideoItem
  → 返回 ListLatestResponse
→ FeedHandler.ListLatest
  → nonNilFeedVideoItems
  → c.JSON(200, response)
```

关键代码位置：

| 层级 | 文件 | 结构体/函数 |
| --- | --- | --- |
| 路由 | `backend/internal/delivery/http/router.go` | `SetRouter`；`softFeedGroup.POST("/listLatest", feedHandler.ListLatest)` |
| 鉴权中间件 | `backend/internal/delivery/jwt/jwt.go` | `SoftJWTAuth`、`check`、`GetAccountID` |
| Request / Response | `backend/internal/module/feed/entity.go` | `ListLatestRequest`、`ListLatestResponse`、`FeedVideoItem` |
| Handler | `backend/internal/module/feed/handler.go` | `FeedHandler.ListLatest`、`nonNilFeedVideoItems` |
| Service | `backend/internal/module/feed/service.go` | `FeedService.ListLatest`、`listLatestFromDB`、`GetVideoByIDs`、`buildFeedVideos`、`buildOrderedResult` |
| Repository | `backend/internal/module/feed/repo.go` | `FeedRepository.ListLatest`、`FeedRepository.GetByIDs` |
| 点赞 Repository | `backend/internal/module/like/repository.go` | `LikeRepository.BatchGetLiked` |

### 5. 数据流转

#### 请求字段如何使用

- `limit`：Handler 规范化后传入 `FeedService.ListLatest`，用于 Redis ZSET 查询上限、MySQL `LIMIT`、冷热拼接剩余数量及最终 `HasMore` 判断。
- `latest_time`：大于 0 时从毫秒时间戳转换为 `time.Time`。热数据分支会转换为 `reqTime`，将 Redis 查询上界设为 `reqTime - 1`；冷数据分支则传给 MySQL 条件 `create_time < latestBefore`。
- `Authorization`：可选。合法 Token 中的账户 ID 写入 Gin Context，并作为 `viewerAccountID` 传入 `buildFeedVideos`。该值只影响本页 `is_liked`；不影响最新视频本身的选择。

#### 查询、缓存与写入的数据

1. Redis 全局时间线 Key：`feed:global_timeline`。成员是视频 ID 字符串，分数是 `Video.CreateTime.UnixMilli()`。
2. Redis 视频实体 Key：`video:entity:<videoID>`。`GetVideoByIDs` 使用批量 `MGet` 读取。
3. 进程内 L1：`go-cache`，Key 与 Redis 视频实体 Key 相同；构造时默认过期时间为 3 秒，视频实体回填时以 5 秒 TTL 写入。
4. MySQL `videos`：`FeedRepository.ListLatest` 按 `create_time DESC` 查询；有游标时增加 `create_time < ?`；`FeedRepository.GetByIDs` 以 `id IN ?` 查询单个未命中视频 ID。
5. MySQL `like_states`：仅登录用户调用 `LikeRepository.BatchGetLiked`，执行 `video_id IN ? AND account_id = ? AND is_liked = true` 查询，构造 `map[videoID]bool`。
6. Redis 写入：全局时间线为空时写入至多 1000 条 ZSET 元素；视频实体 L3 未命中时，使用 goroutine 异步将 JSON 写回 Redis，TTL 为 1 小时。

#### 序列化、反序列化、循环与批量操作

- 入站：Gin `ShouldBindJSON` 将请求 Body 反序列化到 `ListLatestRequest`。
- Redis L2 命中：对每个 `MGet` 返回的字符串执行 `json.Unmarshal` 到 `video.Video`。
- Redis L3 回填：对 `video.Video` 执行 `json.Marshal` 后异步调用 `SetBytes`。
- 出站：`c.JSON` 将 `ListLatestResponse` 序列化为 JSON。
- ZSET 重建：遍历最多 1000 条 MySQL 视频，转换为 `redis.Z`。
- 热数据 ID 转换：遍历 Redis 返回的 ID 字符串，`strconv.ParseUint` 后放入 `[]uint`。
- L1 未命中：`GetVideoByIDs` 对每个未命中 ID 启动一个 goroutine；每个 goroutine 使用 `singleflight.Group.Do` 合并同一视频 ID 的 L3 查询。
- 点赞状态：`buildFeedVideos` 先批量查询一次 `like_states`，再遍历当前页视频组装 `[]FeedVideoItem`；不是逐条查询点赞关系。
- 分页：只使用 `create_time` 时间游标。代码未实现包含视频 ID 的复合游标。

#### 事务、锁、重试与异步操作

- MySQL 事务：该读接口自身未开启事务。
- Redis 分布式锁：该接口未调用 Redis `Lock` / `Unlock`。
- singleflight：存在三类进程内请求合并键：全局时间线重建、冷数据查询/冷热拼接、单视频 L3 实体查询。它是同一 API 进程内的并发合并，不是跨进程锁。
- 重试：该接口没有 HTTP、MySQL、Redis 或 MQ 重试循环。部分 Redis 首次读取失败会直接降级 MySQL；热数据 Redis 范围查询失败则直接返回错误。
- 异步：视频实体从 MySQL 查到后，Redis 实体缓存回写在独立 goroutine 中执行；该回写不阻塞当前响应，也不使用当前请求 Context。
- MQ：无 MQ 发布或消费。

### 6. 结果确认

`listLatest` 的核心输入是 `limit` 和毫秒游标 `latest_time`；可选 Token 只决定返回项中的 `is_liked`。它最依赖 Redis 全局时间线与视频实体缓存，同时依赖 MySQL 作为时间线重建、冷数据分页和缓存未命中的事实来源。测试前至少应启动 API 和 MySQL；Redis 不是硬性前提，但要验证热路径时必须可用。若要验证登录用户的点赞状态，还必须准备合法 Token、账户和 `like_states` 数据。

当前无法从代码确认的内容包括：真实访问频率、生产数据量、MySQL 索引实际定义与执行计划、Redis 中现有时间线/缓存规模、以及具体 Gin JSON 绑定错误在运行时会被 `apierror.ClassifyHTTPStatus` 映射成哪一种状态码。

---

## 第四步：测试场景与测试数据集设计——`listLatest`

> 本节只设计方案，不包含测试脚本、压测命令或实际执行结果。场景仅覆盖 `POST /feed/listLatest` 在当前代码中真实存在的路径。

### 1. 测试目标

本接口不是单一的 MySQL 查询。它会根据 Redis 全局时间线、游标位置、视频实体缓存状态和登录状态进入不同路径，因此本轮设计的目标是：

1. 验证接口在正常热数据路径下的吞吐、延迟、错误率和返回分页行为。
2. 对比视频实体在 L1 本地缓存、L2 Redis、L3 MySQL 三种状态下的差异。
3. 验证 Redis 全局时间线为空时，从 MySQL 读取至多 1000 条视频并重建 ZSET 的路径。
4. 验证游标落到全局时间线最老分数之前时，冷数据查询路径的表现。
5. 验证热数据页不足 `limit` 时，Redis 热数据与 MySQL 冷数据拼接的路径。
6. 验证游客和登录用户两种请求下，`is_liked` 批量查询带来的额外 MySQL 访问。
7. 验证重复请求与并发请求下，singleflight 对时间线重建、冷数据查询和单视频回源的影响。
8. 观察 Redis 或 MySQL 异常时，代码中已有的降级、返回错误和空结果行为。

本接口没有 MQ 发布或消费，因此本轮不把 RabbitMQ 吞吐、消息堆积或 Worker 处理作为 `listLatest` 的直接测试目标。

建议记录的结果指标：请求吞吐、成功率、HTTP 状态码分布、平均延迟、P50/P95/P99 延迟、API CPU/内存/GC、MySQL 查询耗时与连接池状态、Redis 命令耗时与错误数。现有代码是否已接入这些指标的统一采集方式需要确认。

### 2. 测试场景

| 场景名称 | 请求条件 | 数据条件 | 主要观察指标 | 测试目的 |
| ---- | ---- | ---- | ------ | ---- |
| 正常登录用户热路径 | 合法 `Authorization`；第一页；有效 JSON | `feed:global_timeline` 非空；当前账户对本页部分视频存在 `like_states` 记录；当前页视频实体已在 L1/L2 | 200 比例、吞吐、P50/P95/P99、Redis 访问、MySQL `like_states` 批量查询 | 建立真实登录热路径基准，并覆盖 `BatchGetLiked`。 |
| 游客热路径（对照候选） | 不带 `Authorization`；第一页；有效 JSON | `feed:global_timeline` 非空；当前页视频实体已在 L1/L2 | 延迟变化、Redis 访问、MySQL 查询数 | 后续如需量化登录态点赞查询的额外成本，可与登录场景对比。 |
| S03 L1 实体缓存命中 | 同一 API 进程内，短时间重复相同请求 | 全局 ZSET 非空；先完成一次请求，使本页视频进入 L1 | 延迟、CPU、Redis `MGet` 是否减少、MySQL 查询是否为零 | 验证 `go-cache` 的视频实体热命中路径。 |
| S04 L2 Redis 实体缓存命中 | 使 L1 无对应实体后发起请求 | 全局 ZSET 与 `video:entity:<id>` 均存在；L1 已过期或通过隔离 API 进程清空 | Redis `MGet`、`json.Unmarshal`、MySQL 查询数 | 验证跨请求/跨进程可共享的 Redis 视频实体缓存路径。 |
| S05 L3 MySQL 视频实体回源 | 有效游客请求；全局 ZSET 非空 | ZSET 中的每个视频 ID 在 MySQL 存在；L1 和对应 Redis 实体 Key 均未命中 | MySQL 查询数、singleflight 行为、异步 Redis 回填、延迟 | 验证 `GetVideoByIDs` 的 L3 回源和缓存回填路径。 |
| S06 全局时间线空，自动重建 | 有效第一页请求 | MySQL 至少有视频；`feed:global_timeline` 不存在或为空 | MySQL 一次读取至多 1000 条、Redis `ZAdd`、重建期间延迟、重建后响应 | 验证全局 ZSET 的代码内重建路径。 |
| S07 空数据 | 有效游客第一页请求 | `videos` 表为空；全局 ZSET 为空 | HTTP 状态、`video_list`、`has_more`、错误日志 | 验证空库返回空结果而非失败的路径。 |
| S08 冷数据分页 | 携带一个早于 ZSET 最老分数的 `latest_time` | 全局 ZSET 保留较新的视频；MySQL 还存在更早视频 | MySQL `ListLatest`、singleflight、分页字段、延迟 | 验证 `reqTime <= watermark` 时直接查 MySQL 的路径。 |
| S09 冷热边界拼接 | 游标落在 ZSET 热尾附近；`limit` 大于剩余热数据 | Redis 中在当前游标后不足 `limit` 条；MySQL 存在更早视频 | Redis 范围查询、MySQL 补齐查询、返回条数与顺序 | 验证热数据不足时拼接冷数据的实际逻辑。 |
| S10 小数据量 | 游客第一页与第二页 | 小规模数据集 | 延迟、分页、返回顺序、空页行为 | 排除大数据影响，建立功能和性能基线。 |
| S11 中等数据量 | 游客、第一页/后续页 | 中等规模热数据与冷数据 | 延迟、数据库读取、Redis 命中率、CPU | 对比数据规模增长后的成本变化。 |
| S12 大数据量 | 游客、第一页、冷数据页、冷热边界页 | 大规模 MySQL 视频数据；ZSET 只保留最新窗口 | P95/P99、MySQL 执行时间、内存、错误率 | 观察冷数据与大表背景下的变化。 |
| S13 高并发热路径 | 同一类正常登录请求并发发送 | 热数据及 L1/L2 均已准备 | 吞吐、延迟分位数、错误率、CPU、Redis 负载 | 观察核心热读在并发下是否出现错误或资源瓶颈。 |
| S14 并发全局时间线重建 | 多个相同第一页请求同时到达 | ZSET 为空；MySQL 有至少 1 条视频 | MySQL 重建查询次数、Redis `ZAdd` 次数、等待延迟、错误率 | 验证 `sf:fallback:global_timeline_rebuild` singleflight 分支。 |
| S15 并发 L3 同视频回源 | 多个相同请求同时到达 | ZSET 存在；对应视频实体不在 L1/L2，但 MySQL 存在 | 单视频 MySQL 查询合并情况、Redis 异步回填、延迟 | 验证 `sf:entity:<videoID>` singleflight 分支。 |
| S16 重复冷数据请求 | 同一 `limit`、同一冷数据 `latest_time` 的重复/并发请求 | 游标早于热尾；MySQL 有冷数据 | MySQL 查询合并情况、延迟、结果一致性 | 验证 `sf:cold:listLatest:<limit>:<time>` 分支。 |
| S17 非法 JSON | Body 不是可绑定 JSON | 不依赖业务数据 | HTTP 状态、错误 Body | 验证 `ShouldBindJSON` 失败后的真实响应；状态映射需以运行结果确认。 |
| S18 边界字段值 | `limit` 为 0、负数、超过 50；`latest_time` 为 0 或负数 | 可使用小规模数据 | 实际使用的条数、分页结果、HTTP 状态 | 验证 Handler 将非法/越界 `limit` 改为 10，非正 `latest_time` 当作无游标。 |
| S19 无效 Token | 携带格式错误、过期或已撤销的 Authorization | 不依赖视频数据 | 401 比例、错误 Body、Redis Token 检查 | 验证 SoftJWTAuth 对“携带但无效 Token”的拒绝行为。 |
| S20 Redis 不可用 | 有效游客请求；使初始 `ZRangeWithScores` 失败或 API 的 `redisCache` 为 nil | MySQL 中有视频 | HTTP 状态、MySQL 查询、降级延迟 | 验证代码已有的 `listLatestFromDB` 降级路径。 |
| S21 MySQL 不可用 | 选择必然访问 MySQL 的路径，例如 S05、S06 或 S08 | Redis 状态按目标场景准备 | HTTP 500、错误 Body、Redis 是否仍可返回结果 | 验证 MySQL 是该路径必须依赖时的失败表现。 |

说明：S20 中“Redis 不可用”只覆盖初始 ZSET 查询失败时的代码降级。热路径中 `ZRevRangeByScore` 失败会直接向上返回错误，并不会走同一降级分支；该差异来自 `FeedService.ListLatest` 的实际控制流。

### 3. 测试数据设计

#### 必需实体与关联

1. `videos` 表：最新流的事实数据。每条记录至少需要可被 `video.Video` 正常读取的字段，包括 ID、作者 ID、用户名、标题、播放 URL、封面 URL、创建时间和点赞数。
2. `feed:global_timeline`：Redis ZSET。成员必须是视频 ID 的字符串，Score 必须与视频创建时间的毫秒值对应。它用于正常热路径、三级缓存、冷热拼接和高并发等相关场景。
3. `video:entity:<videoID>`：Redis 视频实体缓存。Value 为 `video.Video` 的 JSON 序列化结果；用于构造 L2 命中。当前代码使用项目 Redis Key 前缀，实际完整 Key 需要以运行配置确认。
4. 进程内 L1：不能从 Redis 直接预写。需要先在同一 API 进程完成一次对应页面请求，或使用隔离 API 进程控制其初始状态。
5. `accounts` 与 `like_states`：仅登录场景需要。准备一个合法测试账户，以及该账户对本页部分视频的 `is_liked=true` 状态；这样能验证 `is_liked=true/false` 同时出现。
6. JWT：登录场景需要由项目真实鉴权逻辑签发的、未过期且未撤销的 Access Token。游客场景不需要 Token。

#### 数据规模

```text
小规模：
- videos：20 条，创建时间按毫秒严格递减，足够覆盖第一页、第二页和空页。
- global_timeline：20 条，成员与 videos 一一对应。
- 登录场景 `like_states`：对其中 5 条建立当前测试用户的 `is_liked=true` 状态。
- 适用：正常热路径、三级缓存、空数据、字段边界和鉴权相关场景。

中等规模：
- videos：3,000 条。
- global_timeline：最新 1,000 条。
- 冷数据：剩余 2,000 条，仅保留在 MySQL。
- 登录场景 `like_states`：当前测试用户对最新页中约 10 条建立 `is_liked=true` 状态。
- 适用：S06、S08、S09、S11、S14、S15、S16、S20、S21。

大规模：
- videos：建议从 20,000 条开始；是否扩展到更大规模需要结合本机内存、磁盘和 MySQL 配置确认。
- global_timeline：保持最新 1,000 条，与当前 Timeline Worker 的保留上限一致。
- 冷数据：其余视频只存在 MySQL，用于冷页和冷热边界页。
- 登录场景 `like_states`：只需为当前测试用户建立当前页相关的点赞状态；不需要给每条视频随机创建大量点赞状态。
- 适用：S12、S13，以及对 S08/S09 的规模对比。
```

#### 重复、随机与热点数据

- 应重复使用：同一组视频 ID、同一个游客请求 Body、同一个登录用户 Token、同一个冷数据游标。这样才能观察重复请求和 singleflight，而不是混入随机数据差异。
- 应随机生成：视频标题、播放 URL、封面 URL 可以生成不同值；这些字段不是该接口的筛选条件。生成规则需要确认，以免意外触发数据库长度或唯一约束。
- 应构造热点数据：以相同的第一页请求、相同视频 ID 集合构造热点；这是 L1/L2 命中和单视频 singleflight 场景的前提。
- 不需要构造大请求体：当前请求 Body 只有 `limit` 与 `latest_time` 两个数值字段。响应 Value 大小主要由视频标题、描述、URL 和返回条数决定；如需比较响应序列化成本，可准备短文本与较长描述两组视频，但描述长度上限需要先从模型或数据库定义确认。
- 创建时间：为得到可解释的分页结果，测试数据应让 `create_time` 严格递减且避免相同毫秒。当前分页只用时间游标，若多个视频时间相同，跨页结果是否遗漏需要运行后确认。

### 4. 变量控制

#### 固定变量

- 代码版本：固定为一个明确 Git Commit；测试过程不得混入业务代码修改。
- 机器配置：CPU、内存、磁盘、操作系统、电源模式与网络环境保持一致，并记录实际值。
- 部署形态：固定 API 进程数量、Worker 是否运行、Docker Compose 服务版本、MySQL/Redis 配置和连接池参数。具体参数值需要在执行前确认并记录。
- 数据库结构与索引：同一轮对比中保持一致；实际索引定义需要确认。
- 请求路径、HTTP 方法、响应校验规则、`limit`、登录/游客身份：单次对比中固定。
- 测试时长、预热时长、压测工具版本、采样和指标采集方式：执行前固定并记录。

#### 变化变量

- 视频数据规模：20、3,000、20,000 起步的分组。
- 数据位置：ZSET 是否存在、视频实体位于 L1/L2/L3 的哪一级、是否需要重建全局时间线。
- 分页位置：第一页、热数据页、冷热边界页、纯冷数据页。
- 访问身份：游客或登录用户。
- 并发数、请求速率、持续时间：在后续执行阶段按单变量递增；本设计阶段不设具体数值。
- Key 分布：固定热点第一页，或按已准备的不同分页游标分散访问。
- Redis / MySQL 可用性：仅在异常场景中改变。
- 响应实体大小：仅在单独的序列化对比场景中改变视频描述/URL 长度。

控制原则：一次实验只改变一个主要变量。例如比较 L2 与 L3 时，视频数量、游标、`limit`、身份、并发和机器配置均保持一致，只删除或保留 `video:entity:<id>`；比较游客与登录用户时，只改变 Token 与相应 `like_states` 数据。不能同时改变数据量、并发数和缓存状态，否则结果无法归因。

### 5. 推荐执行顺序

```text
环境与数据正确性确认
→ S07 空数据
→ S10 小规模游客正常请求
→ 小规模登录请求
→ S03 / S04 / S05：L1、L2、L3 视频实体路径对比
→ S06：全局时间线空时的自动重建
→ S08 / S09：纯冷数据与冷热边界拼接
→ S11 / S12：中等、大数据规模对比
→ S13：高并发热路径
→ S14 / S15 / S16：重复并发与 singleflight 路径
→ S17 / S18 / S19：非法 JSON、边界字段和无效 Token
→ S20 / S21：Redis、MySQL 依赖异常
```

执行前仍需要确认：测试数据的创建方式、MySQL 实际索引、Redis Key 前缀、API/Redis/MySQL 的运行配置，以及指标采集方式。上述内容均无法仅凭当前 Handler 和 Service 代码确定。

---

## 第五步：本轮执行场景——正常登录请求 QPS

### 1. 唯一需要执行的场景编号

**正常登录用户热路径。**

### 2. 选择原因

它符合当前目标：有效请求、第一页、登录用户、Redis 全局时间线已准备、数据已就绪。该路径用于测量最新 Feed 在正常热数据状态下的 QPS、延迟和性能拐点，同时真实执行一次批量 `BatchGetLiked` 查询；不混入冷数据回源、重建、异常降级等变量。

### 3. 固定请求内容

```http
POST /feed/listLatest
Authorization: Bearer <有效 Access Token>
Content-Type: application/json

{"limit":20,"latest_time":0}
```

固定使用 100 个测试账户轮换的合法 Access Token。

### 4. 固定数据条件

- MySQL、Redis、API 均正常启动。
- 已存在 100 个可登录测试账户，以及合法、未过期、未撤销的 Access Token。
- `videos` 表中存在 10,000 条可用视频，其中最近 1,000 条已写入 Redis 全局时间线。
- 100 个测试账户轮换访问第一页；登录路径会真实执行 `BatchGetLiked` 批量查询。
- 固定只取第一页最新 20 条。
- 压测前预热当前页的 Redis 视频实体缓存，作为正常热数据状态。
- 测试期间不修改视频数据、时间线、缓存、API 实例数和请求内容。

### 5. 本次明确不测

- 非法 JSON、非法 Token、`limit` 边界值；
- Redis/MySQL 故障与降级；
- 全局时间线重建；
- L1、L2、L3 缓存路径对比；
- singleflight；
- 冷数据、冷热拼接、翻页和游标边界；
- MQ、Worker、发布链路。

### 6. 需要观察的指标

- 实际 QPS；
- 成功率、HTTP 状态码分布、5xx 数；
- 平均延迟、P50、P95、P99；
- API CPU、内存、GC；
- Redis 全局时间线读取的耗时与错误；
- MySQL 是否出现非预期查询或连接池压力。

### 7. 测试完成的判断标准

逐步提高正常请求负载，记录每一级负载下的 QPS、P95/P99、错误率和资源使用情况。第一个“继续提高负载但 QPS 不再明显增长，同时 P95/P99 明显上升，或开始出现错误”的负载点，即为当前环境下该正常热路径的性能拐点。

---

## 第六步：压测环境与工具链

### 1. 当前环境分工

```text
Linux 虚拟机（k6 压测发起端）
        ↓ HTTP 请求
Windows 宿主机 Docker Compose（API + MySQL + Redis + RabbitMQ + Worker）
```

### 2. 已确认环境信息

| 项目 | 当前状态 | 已确认信息 |
| --- | --- | --- |
| 项目启动方式 | 已具备 | 注释版通过 `docker-compose.yml` 启动。 |
| API 服务 | 已启动、healthy | 宿主机端口 `8080`；`GET /healthz` 已返回 HTTP 200。 |
| Worker | 已启动、healthy | 异步消费者独立容器运行；pprof 映射到宿主机 `127.0.0.1:6061`。 |
| MySQL | 已启动、healthy | Docker 容器内端口 `3306`，映射宿主机 `3307`。 |
| Redis | 已启动、healthy | Docker 容器内端口 `6379`，映射宿主机 `6380`。 |
| RabbitMQ | 已启动、healthy | AMQP 端口 `5672`，管理界面端口 `15672`。 |
| API pprof | 已具备 | 地址 `http://127.0.0.1:6060/debug/pprof/`，仅宿主机可访问。 |
| Prometheus / Grafana | 未具备 | 项目文件中未发现对应编排或配置。 |
| 本机 k6 | 未安装 | Windows 宿主机未检测到 `k6` 命令；本轮不在本机执行压测。 |
| Linux 压测机 | 已连接 | SSH 用户与地址：`loadtest@192.168.86.155:22`。 |
| Linux 系统 | 已确认 | 主机名 `loadgen`，Linux 6.8，x86_64。 |
| Linux CPU | 已确认 | 8 个逻辑 CPU。 |
| Linux 内存 | 已确认 | 总内存约 3.8 GiB，可用约 3.3 GiB；Swap 约 3.8 GiB，检查时未使用。 |
| Linux k6 | 已安装 | 路径 `/usr/bin/k6`，版本 `v2.2.0`。 |
| Linux 压测目录 | 已清理 | `~/agora-k6/` 保留为空目录，只有 `results/` 空文件夹；旧脚本、旧数据和旧结果已删除。 |

### 3. 本轮正式场景的最小依赖

- 必需：Linux 虚拟机上的 k6、可从虚拟机访问的 API、API、Redis、MySQL，以及已准备的最新流热数据。
- 已启动但本场景不直接依赖：Worker、RabbitMQ。它们保留启动是为了维持完整项目运行环境；本接口不发送 MQ 消息。
- 暂不需要：Prometheus、Grafana、前端。

### 4. 当前仍需准备或确认

1. 从 Linux 虚拟机实际访问 `http://<Windows宿主机IP>:8080/feed/listLatest` 的网络连通性。
2. 写入并核对测试账户、视频、`feed:global_timeline`、视频实体缓存和点赞关系，随后执行一次登录预热请求。
3. 在正式压测前固定 API 镜像/代码版本、测试时长、升压方式和指标采集命令。

### 5. Linux 压测机连通性验证

验证路径：

```text
Linux 虚拟机 loadgen（192.168.86.155）
  → Windows 宿主机网关（192.168.86.1:8080）
  → Agora API 容器
```

| 请求 | HTTP 状态 | 连接时间 | 总耗时 | 结论 |
| --- | --- | --- | --- | --- |
| `GET /healthz` | 200 | 约 0.4ms | 约 2.1ms | 虚拟机可访问 API。 |
| 游客 `POST /feed/listLatest` | 200 | 约 0.4ms | 约 4.2ms | 网络、路由和已有 Feed 数据正常。 |
| 登录态 `POST /feed/listLatest` | 200 | 约 0.8ms | 约 5.6ms | Token 鉴权与 `BatchGetLiked` 路径可从压测机访问。 |

说明：以上仅为单次连通性请求，不能作为 QPS 或延迟压测结论。正式压测应从 Linux 压测机访问 `http://192.168.86.1:8080`。

---

## 第七步：正式场景数据与脚本准备

本轮固定测试正常登录用户读取第一页最新 Feed：100 个登录用户轮换请求 `POST /feed/listLatest`，请求体为 `{"limit":20,"latest_time":0}`。数据、Token、视频实体缓存均在压测前准备完成，k6 脚本使用 `constant-arrival-rate` 逐级提高目标 QPS，并校验 HTTP 200 与 `video_list` 长度为 20。

| 项目 | 正式场景固定条件 |
| --- | --- |
| 请求用户 | 100 个新生成的登录测试用户轮换使用 |
| 请求内容 | `POST /feed/listLatest`，JSON Body：`{"limit":20,"latest_time":0}` |
| 数据规模 | 1,000 个账号、200 个作者、10,000 条视频、16,000 条关注关系 |
| Redis 时间线 | `v1:feed:global_timeline` 共 1,020 条成员，其中最新 1,000 条为本轮测试视频 |
| 视频实体缓存 | 压测前通过一次正常请求预热最新页的 20 条视频实体缓存 |
| 持续时间 | 每档 60 秒 |
| 压测模型 | Linux 虚拟机 k6 的 `constant-arrival-rate`；100 预分配 VU、最多 1,000 VU |
| 测试 Token | 由 seed 工具生成，显式设置 2 小时有效期，避免阶梯测试期间过期 |
| 正确性校验 | HTTP 200，并解析 JSON 确认 `video_list` 长度为 20 |
| 连接池 | 使用当前 API 80/40、Worker 20/10 的 MySQL 连接池配置 |

## 第八步：阶梯压测结果

| 目标 QPS | 实际请求数 | 实际 QPS | HTTP 失败率 | 正确性校验 | P95 | P99 | dropped_iterations | 最大 VU | 判定 |
| ---: | ---: | ---: | ---: | --- | ---: | ---: | ---: | ---: | --- |
| 2000 | 120001 | 1999.91 | 0% | 100% | 4.36ms | 6.08ms | 0 | 100 / 1000 | 通过 |
| 2400 | 143898 | 2398.16 | 0% | 100% | 5.79ms | 12.36ms | 101（约 0.07%） | 108 / 1000 | 通过 |
| 2800 | 167886 | 2797.96 | 0% | 100% | 4.27ms | 5.93ms | 115（约 0.07%） | 154 / 1000 | 通过 |
| 3000 | 179945 | 2998.91 | 0% | 100% | 4.34ms | 6.16ms | 56（约 0.03%） | 155 / 1000 | 通过 |
| 3500 | 210010 | 3499.58 | 0% | 100% | 5.33ms | 8.06ms | 0 | 250 / 1200 | 通过 |
| 4000 | 239985 | 3999.16 | 0% | 100% | 13.82ms | 37.84ms | 16（约 0.01%） | 251 / 1200 | 不通过：P99 超过 20ms |
| 4000（复跑） | 239583 | 3992.66 | 0% | 100% | 24.80ms | 52.48ms | 418（约 0.17%） | 264 / 1200 | 不通过：P99 超过 20ms |
| 5000 | 299869 | 4997.43 | 0% | 100% | 18.22ms | 42.15ms | 132（约 0.04%） | 357 / 1500 | 不通过：P99 超过 20ms |
| 6000 | 353126 | 5883.31 | 0% | 100% | 89.84ms | 141.51ms | 6876（约 1.9%） | 603 / 1800 | 不通过：达到饱和，P99 与缺发显著恶化 |

本轮采用 **P99 < 20ms、HTTP 错误率 < 1%** 作为通过标准。当前代码和本轮数据条件下，2,000～3,500 QPS 均通过；4,000 QPS 连续两次虽然请求全部成功，但 P99 分别为 37.84ms 和 52.48ms，均不通过，因此可确认性能拐点位于 3,500～4,000 QPS。继续升至 5,000 QPS 后 P99 仍为 42.15ms，说明高负载下尾延迟恶化持续存在；6,000 QPS 时实际完成吞吐降至 5,883.31 QPS，P99 升至 141.51ms，且缺发约 1.9%，已进入明显饱和状态。`dropped_iterations` 表示 k6 未能按固定节奏启动的请求，不是 HTTP 请求失败。原始结果文件：[2000 QPS](loadtest/results/s02/s02_2000rps_60s_summary.json)、[2400 QPS](loadtest/results/s02/s02_2400rps_60s_summary.json)、[2800 QPS](loadtest/results/s02/s02_2800rps_60s_retry_summary.json)、[3000 QPS](loadtest/results/s02/s02_3000rps_60s_retry_summary.json)、[3500 QPS](loadtest/results/s02/s02_3500rps_60s_summary.json)、[4000 QPS 第一次](loadtest/results/s02/s02_4000rps_60s_summary.json)、[4000 QPS 复跑](loadtest/results/s02/s02_4000rps_60s_retry_summary.json)、[5000 QPS](loadtest/results/s02/s02_5000rps_60s_summary.json)、[6000 QPS](loadtest/results/s02/s02_6000rps_60s_summary.json)。

## 第九步：6000 QPS 三分钟诊断

在与前述相同的数据、请求和依赖条件下，将目标维持在 6,000 QPS 并延长至 180 秒，同时采集 Linux 压测机指标、API CPU pprof、API/MySQL/Redis 容器资源和 MySQL 全局状态。该诊断轮仅用于归因，不替代前述纯 QPS 阶梯结果。

| 指标 | 结果 |
| --- | ---: |
| 目标 QPS / 时长 | 6000 / 180 秒 |
| 实际完成请求数 | 995143 |
| 实际 QPS | 5527.57 |
| HTTP 失败率 | 0% |
| 响应正确性校验 | 100% |
| P95 / P99 | 396.21ms / 627.55ms |
| 最大延迟 | 2228.07ms |
| dropped_iterations | 84862（约 7.86%） |
| k6 最大 VU | 1800 |

#### 压测机与服务端资源

| 组件 | 采样结果 | 判断 |
| --- | --- | --- |
| Linux k6 压测机（8 逻辑 CPU） | k6 CPU 平均 489.83%、峰值 576.00%；load1 平均 4.25、峰值 6.39；可用内存最低约 2.38GiB | 压测机负载较高，但未出现 CPU 或内存耗尽，不能将本轮饱和主要归因于压测机。 |
| API 容器 | CPU 平均 612.52%、峰值 800.36% | 是主要 CPU 消耗方。 |
| MySQL 容器 | CPU 平均 345.41%、峰值 463.19% | 明显参与当前饱和链路。 |
| Redis 容器 | CPU 平均 58.94%、峰值 77.36% | 相对较低，不是当前首要 CPU 压力来源。 |

> Windows 端采样每轮还需执行 MySQL 状态查询，因此 180 秒内得到约 50 个有效采样点，而不是机械地每秒一次；所有容器/MySQL结论均基于同一 180 秒时间窗口。

#### MySQL 与 API profile

- MySQL 采样中 `Threads_connected` 峰值为 82、`Max_used_connections` 峰值为 86，符合 API 80 条加 Worker 20 条的连接池预算附近状态；`Threads_running` 峰值为 31，说明数据库存在并发执行压力。
- 采样窗口内 MySQL `Connections` 累计值增加 33,251，仍存在较多连接创建；但当前 API 的 CPU profile 中 `mysql.(*connector).Connect` 累计仅约 12 秒（约 1.18%），`database/sql.(*DB).conn` 约 2.05%，不再是旧测试中“频繁建连”那种主导成本。
- API 180 秒 CPU profile 共采到 1020.87 CPU 秒，约等于持续使用 5.67 个 CPU 核。`FeedService.buildFeedVideos` 的累计路径占 29.16%，其中 `LikeRepository.BatchGetLiked` 占 28.64%，GORM 查询路径与其对应；Redis 客户端处理路径约占 21%。
- `SoftJWTAuth` 在累计调用栈中占比较高，但其包含后续 Handler 执行，不能据此认定 JWT 校验本身是瓶颈。
- `syscall` / 网络写入相关路径 flat 占比较高，说明高负载时大量时间消耗于网络、数据库通信、响应写回和运行时调度，不能简化为单个 Go 算法函数耗尽 CPU。

结论：当前正式场景的高负载饱和是 **API Feed 组装、Redis 访问与 MySQL 点赞状态批量查询共同构成的链路压力**；API 与 MySQL 是优先关注对象，Redis 不是首要 CPU 瓶颈。当前证据不足以把问题归结为 `BatchGetLiked` 单独一处，也不应直接将全量点赞关系迁入 Redis。

原始诊断产物位于 `loadtest/results/s02/s02_6000rps_180s_diagnostic_retry/`。

> 有效性记录：此前使用正常 15 分钟 Access Token 时，部分阶梯档在 Token 过期后快速返回 401，未纳入上表。随后为 seed 工具增加显式 Token 有效期参数，正式场景使用 2 小时临时 Token 后重跑 2,800 与 3,000 QPS，得到上表有效结果；正常 HTTP 登录仍保持 15 分钟 Access Token。

---

## `listLatest` 最终测试总结

### 1. 本次最终采用的测试口径

最终性能结论以正式多用户场景为准：100 个登录用户轮换，`POST /feed/listLatest`，固定请求 `{"limit":20,"latest_time":0}`，1,000 个账号、10,000 条视频、最近 1,000 条 Redis 全局时间线，最新 20 条视频实体缓存已预热，每档持续 60 秒。k6 同时校验 HTTP 200 与响应中的 `video_list` 长度为 20。

这测量的是 **登录用户读取预热的最新首页 Feed**：请求会经过 JWT 软鉴权、Redis 全局时间线、视频实体缓存、MySQL 批量点赞状态查询和 JSON 响应组装。它不代表冷缓存、翻页、关注流、热榜、写入接口或整个系统的绝对性能上限。

### 2. 阶梯结论

以 `P99 < 20ms`、HTTP 错误率 `< 1%` 为通过标准：

- 2,000～3,500 QPS 均通过；3,500 QPS 时 P99 为 8.06ms，HTTP 错误为 0，响应内容校验 100%。
- 4,000 QPS 连续两次 P99 分别为 37.84ms 和 52.48ms，虽然无 HTTP 错误，但已超过尾延迟标准。
- 5,000 QPS 时 P99 为 42.15ms，尾延迟恶化持续存在。
- 6,000 QPS、60 秒时 P99 为 141.51ms；延长为 180 秒后，实际完成吞吐降至 5,527.57 QPS，P99 升至 627.55ms、`dropped_iterations` 为 84,862（约 7.86%），进入明显饱和状态。

因此，在当前固定环境与标准下，可表述为：**`listLatest` 登录热路径在 3,500 QPS 内保持稳定；4,000 QPS 起尾延迟明显上升；6,000 QPS 长时间压测进入饱和。**

### 3. 第一轮 2000 QPS：发现 MySQL 连接池抖动

在 `listLatest` 的第一轮 2,000 QPS、60 秒压测中，接口出现高尾延迟和 k6 缺发。随后没有直接猜测“数据库慢”，而是同时采集 k6、MySQL 状态和 API CPU profile：

| 证据 | 当时观察到的现象 |
| --- | --- |
| k6 | P99 为 273.82ms，出现 626 次 `dropped_iterations` |
| MySQL 状态 | 55 秒内 `Connections` 从 10,171 增至 29,569，新增约 19,398 条连接；但长期连接数并未持续堆积 |
| API pprof | `ListLatest -> buildFeedVideos -> BatchGetLiked -> database/sql.(*DB).conn -> mysql.(*connector).Connect -> net.Dial` 是主要热点链路；`mysql.(*connector).Connect` 累计约 24.94 CPU 秒 |

这些证据表明问题不是 SQL 本身先变慢，而是 `BatchGetLiked` 的高频查询不断触发 MySQL 建连，产生连接抖动。

### 4. 定位后配置连接池

为 API 与 Worker 的 `database/sql` 分别配置连接池，并由启动入口传给唯一的 GORM DB 实例：

| 进程 | 最大打开连接 | 最大空闲连接 | 最大连接生命周期 | 最大空闲时间 |
| --- | ---: | ---: | ---: | ---: |
| API | 80 | 40 | 30 分钟 | 5 分钟 |
| Worker | 20 | 10 | 30 分钟 | 5 分钟 |

连接预算合计为 100，低于 MySQL 的 `max_connections=151`。这样 API 请求结束后归还连接给连接池复用，而不是反复建立 TCP/MySQL 会话。

### 5. 连接池优化后验证

在相同的 2,000 QPS、60 秒场景复测后：

| 指标 | 优化前 | 优化后 |
| --- | ---: | ---: |
| 实际 QPS | 1,986.31 | 1,999.90 |
| HTTP 失败 | 0 | 0 |
| `dropped_iterations` | 626 | 0 |
| P99 | 273.82ms | 9.09ms |
| MySQL 新建连接数（55 秒） | 19,398 | 56 |

这构成了完整的 **压测发现问题 → 指标与 pprof 定位 → 配置连接池 → 同场景复测验证** 闭环。

完成连接池优化后，再进入多用户、大数据量阶梯压测。三分钟 6,000 QPS 诊断中，MySQL `Connections` 仍增加较多，但配置文件、启动入口和连接数上限均确认连接池生效；同时 `mysql.(*connector).Connect` 在 pprof 中仅占约 1.18%，不再是主导成本。因此当前高负载饱和不能简单归因于连接池问题。

### 6. 当前饱和时的结论

三分钟 6,000 QPS 诊断显示：Linux 压测机仍有 CPU 和内存余量；API 与 MySQL CPU 明显更高，Redis 相对较低；`buildFeedVideos`、`BatchGetLiked`、Redis 访问、网络写回及运行时调度共同构成压力链路。

因此下一步若要继续优化，应先单独验证点赞状态批量查询、Feed 响应组装和 Redis 访问各自的收益。当前测试已经足以说明系统的稳定区间、饱和表现和一次真实的连接池优化闭环。

---

## 当前测试流程总览

```text
第一步：接口盘点
  → 从路由、Handler、Service 一直追踪到 MySQL、Redis、MQ
  → 只统计代码中真实存在的 HTTP、SSE 和 Worker 任务

第二步：测试对象筛选
  → 按核心程度、依赖复杂度和并发风险排序
  → 先选 listLatest 作为第一个性能测试对象

第三步：listLatest 调用链分析
  → 明确请求协议、鉴权、参数、返回结构和前置条件
  → 梳理 Redis 时间线、视频缓存、MySQL 点赞状态查询的真实路径

第四步：候选场景与数据设计
  → 列出正常、缓存、回源、分页、异常等候选情况
  → 明确用户、视频、时间线、缓存和点赞关系需要如何准备

第五步：收敛第一轮范围
  → 不一次测完所有分支
  → 先固定“正常登录用户读取第一页最新 Feed”，只测 QPS 与性能拐点

第六步：压测环境准备
  → 启动 API、Worker、MySQL、Redis、RabbitMQ
  → 连接 Linux k6 压测机，确认 API、pprof 和依赖均可用

第七步：准备压测数据
  → 生成 1,000 个账号、200 个作者、10,000 条视频、16,000 条关注关系
  → 写入最近 1,000 条 Redis 全局时间线，并预热最新 20 条视频实体缓存
  → 生成 100 个登录用户的临时 Token，保证压测期间鉴权路径可用

第八步：准备 k6 压测脚本
  → 100 个登录用户轮换请求 `POST /feed/listLatest`
  → 固定请求体为 `{"limit":20,"latest_time":0}`，每档持续 60 秒
  → 校验 HTTP 200 和 `video_list` 返回 20 条，记录实际 QPS、P95/P99、错误率和缺发

第九步：从低压开始逐级加压至 2000 QPS
  → 固定环境、数据和请求内容，只提高目标 QPS
  → 在 2000 QPS / 60 秒时发现 P99 明显升高，并出现 k6 缺发

第十步：定位并解决 MySQL 连接池问题
  → 同时查看 MySQL `Connections` 和 API pprof
  → 发现 `BatchGetLiked` 高频查询触发频繁 MySQL 建连，造成连接抖动
  → 配置 API 80/40、Worker 20/10 的 database/sql 连接池后，在相同 2000 QPS 场景复测
  → 用 P99、缺发和 MySQL 新建连接数确认连接池优化有效

第十一步：继续阶梯压测至 6000 QPS
  → 在连接池修复后的相同场景下，从 2,400 QPS 逐级提高到 6,000 QPS
  → 3,500 QPS 内稳定通过；4,000 QPS 起尾延迟超过标准；6,000 QPS 出现明显饱和

第十二步：长时间诊断与归因
  → 在 6000 QPS 下延长至 3 分钟
  → 同时采集 Linux 压测机负载、API pprof、容器 CPU/内存和 MySQL 状态
  → 确认压测机未先满载，主要压力位于 API Feed 链路与 MySQL，Redis 相对较低

第十三步：形成最终结论
  → 明确本次结论仅适用于固定的登录 Feed 热路径
  → 输出稳定范围、饱和表现、诊断证据和连接池优化闭环
```

