# 引入 Redis：从容器配置到任务缓存

> 阶段笔记，更新于 2026-11-11。以 day9 项目保存的代码和本阶段测试日志为基础；历史章节保留当时状态，标为“建议”的方案不代表源码已经完成修改。singleflight 见第 17 节；随机 TTL、Redis 故障超时和回源限流的最新进度见第 18—20 节，状态有冲突时以最新章节为准。

## 目录

1. 阶段目标与当前进度
2. Docker Compose 配置、网络与认证
3. Go 加载环境变量并初始化 Redis Client
4. Cache、Service、Repository 的职责与依赖
5. Key、JSON、TTL 与缓存读写
6. Service 中的缓存编排
7. Context 的父子关系与生命周期
8. 常见报错与排查方式
9. 验证方法与后续工作
10. 更新后的缓存失效与一致性
11. Redis 故障降级复盘
12. VS Code 断点调试与工作目录
13. 空值缓存：缓存穿透防护与修复复盘
14. 布隆过滤器接入：构建、查询与增量维护
15. 布隆过滤器接入中的问题复盘
16. 本阶段验收与下一步
17. 缓存击穿复现与 singleflight 接入
18. 随机 TTL：分散过期时间与实验边界
19. Redis 停机降级：Context 预算与超时复盘
20. 回源保护：Semaphore、连接池与令牌桶

## 1. 阶段目标与当前进度

为 `GET /tasks/:id` 引入 Redis，采用 Cache-Aside（旁路缓存）模式：PostgreSQL 保存业务数据，Redis 保存可过期、可重建的查询副本。

```text
请求 → Handler → Service
                  ├─ Redis 命中 → 返回
                  └─ 未命中或缓存异常 → PostgreSQL
                                         ├─ 查询失败 → 返回错误
                                         └─ 查询成功 → 尝试回填 Redis → 返回任务
```

“回源”指缓存没有提供可用结果时，查询 PostgreSQL。

当前源码检查结果：

| 项目 | 状态 |
|---|---|
| Compose 添加 Redis、端口绑定、密码和健康检查 | 已配置 |
| Go 加载 .env、初始化 Client 并注入 TaskCache | 已实现，注意工作目录 |
| JSON 序列化、缓存读写、5 分钟 TTL | 已实现 |
| 未命中的结果表示 | 已升级为 TaskCacheResult，Hit=false、err=nil |
| 命中直接返回，未命中查询 PostgreSQL | 已实现 |
| 数据库错误正常返回 | 已实现 |
| 缓存回填失败只记录日志 | 已实现 |
| Service 用 nil 判断是否启用缓存 | 已实现 |
| Redis 读取异常时继续查询数据库 | 已调整为记录日志后回源，需故障验证 |
| Redis 不可用时允许 HTTP 服务启动 | 已调整为 Ping 失败只记录日志，需故障验证 |
| UpdateTask 成功后删除详情缓存 | 已实现，删除失败仅记录日志 |
| DeleteTask、状态修改后的缓存失效 | 待完善 |
| 提交后使用独立、有界的缓存失效上下文 | 待完善：当前仍使用请求 ctx |
| 正常任务与空值缓存使用包装 JSON，key 升级 v2 | 已实现 |
| Service 区分空值命中，并保留数据库未找到错误 | 已实现 |
| 空值缓存有效命中、阻止重复回源 | 尚缺 GetResult 的 Exists=false 返回分支，见第 13 节 |
| RedisBloom 模块与 BF 命令 | Compose 已显式加载模块 |
| TaskBloom 的构建、增量加入和查询 | 已实现 BFReserve、BFMAdd、BFAdd、BFExists |
| 启动全量装载与 UUID 构建 key | 已实现，失败时注入 nil 禁用过滤器 |
| 新建任务后维护布隆过滤器 | 已加入 Add、nil 判断和失败日志 |
| 运行期过滤器有效性、新增同步失败后的处理 | 待完善，目前否定结果仍直接拦截 |

## 2. Docker Compose 配置、网络与认证

### 2.1 配置文件与层级

`docker-compose.yml` 保存在宿主机的项目目录，Compose 读取它来创建和配置容器，不会自动将整个 YAML 文件复制到容器里。

Redis 配置放在 `services:` 下，与 `db:` 同级：

```yaml
services:
  redis:
    image: redis:8-alpine
    ports:
      - "127.0.0.1:6379:6379"
    environment:
      REDIS_PASSWORD: "${REDIS_PASSWORD:?请在 .env 中设置 REDIS_PASSWORD}"
    command:
      - sh
      - -c
      - |
        exec redis-server \
          --requirepass "$$REDIS_PASSWORD" \
          --loadmodule /usr/local/lib/redis/modules/redisbloom.so
    healthcheck:
      test:
        - CMD-SHELL
        - 'REDISCLI_AUTH="$$REDIS_PASSWORD" redis-cli ping'
      interval: 5s
      timeout: 3s
      retries: 5
      start_period: 5s
```

`redis:8-alpine` 固定了主版本和镜像变体，但标签仍可能更新；需要严格复现时应进一步固定补丁版本或镜像摘要。

健康检查每 5 秒执行一次，单次超时 3 秒，连续失败 5 次标记为不健康。启动宽限期内的失败不计入失败次数，成功仍可提前标记健康。健康状态本身不等于自动重启策略。

### 2.2 为什么有两个 volumes

```yaml
services:
  db:
    volumes:
      - postgres_data:/var/lib/postgresql/data
      - ./db/init:/docker-entrypoint-initdb.d:ro

volumes:
  postgres_data:
```

- 顶层 `volumes:` 声明由 Docker 管理的命名数据卷。
- 服务内部的 `volumes:` 将数据卷或宿主机目录挂载到容器路径。
- `postgres_data:` 留空表示使用默认配置，声明本身不会自动保存容器数据，必须挂载到正确目录。
- `./db/init` 是宿主机目录挂载，不需要顶层声明；`:ro` 表示只读。
- `docker compose down` 默认保留命名数据卷；`docker compose down -v` 会删除相关数据卷。

当前 Redis 没有配置持久化挂载，不应依赖其在容器重建后保留缓存；缓存应能从 PostgreSQL 重建。

### 2.3 为什么本机用 127.0.0.1，容器用 redis

| Go 的运行位置 | 地址 | 路径 |
|---|---|---|
| Windows 本机 | `127.0.0.1:6379` | 宿主机端口映射 → Redis 容器 |
| 同一 Compose 网络中的容器 | `redis:6379` | Compose 服务名解析 → Redis 容器端口 |

`localhost` 指向程序自己所在的网络环境。Go 容器里的 localhost 是 Go 容器自身，不是 Redis 容器。

`redis:6379` 通常写在 Go 的配置或 Go 服务的环境变量中。`redis` 来自 Compose 的服务名，容器间通信不依赖宿主机的 `ports` 映射。

### 2.4 密码与变量展开

同目录 `.env` 示例：

```dotenv
REDIS_PASSWORD=替换成你的密码
```

仅设置 `REDIS_PASSWORD` 环境变量不会自动让官方 Redis 镜像启用认证，当前通过 `--requirepass` 设置默认用户 `default` 的密码。

| 写法 | 展开阶段 |
|---|---|
| `${REDIS_PASSWORD}` | Compose 读取环境变量或 .env 后展开 |
| `$$REDIS_PASSWORD` | Compose 转义，交给容器内 Shell 在运行时展开 |

因此，`docker compose config` 中仍显示 `$$REDIS_PASSWORD` 是正常现象。

当前认证测试：

```powershell
docker compose exec redis redis-cli --askpass ping
```

输入密码后返回 `PONG`。认证仅对当前连接生效，健康检查的认证不会授权新开的 redis-cli 连接。

如需 root 用户，可将启动命令改成 ACL 配置，并同步修改健康检查和 Go 的 Username：

```yaml
command:
  - sh
  - -c
  - |
    exec redis-server \
      --user "default off" \
      --user "root on >$$REDIS_PASSWORD ~* &* +@all"
healthcheck:
  test:
    - CMD-SHELL
    - 'REDISCLI_AUTH="$$REDIS_PASSWORD" redis-cli --user root ping'
```

这是可选方案，当前源码使用 default。`root` 是 Redis 用户名，与容器的 Linux 用户无关。`~*`、`&*`、`+@all` 分别允许所有键、频道和命令。

## 3. Go 加载环境变量并初始化 Redis Client

### 3.1 自动读取 .env

```powershell
go get github.com/joho/godotenv
go get github.com/redis/go-redis/v9
```

Go 的 `os.Getenv` 不会自动读取文件，需要先加载：

```go
if err := godotenv.Load("../../.env"); err != nil {
    log.Printf("加载 .env 失败：%v", err)
    return
}
password := os.Getenv("REDIS_PASSWORD")
```

相对路径由执行命令时的工作目录决定，与源文件位置无关：

| 工作目录 | Load 参数 |
|---|---|
| `day9/internal/cache`（之前的独立练习） | `../../.env` |
| `day9/cmd/server` | `../../.env` |
| `day9` | `.env`，也可以调用无参数 Load |

当前 server 写的是 `../../.env`，对应从 `day9/cmd/server` 执行 `go run .`。若从 day9 执行 `go run ./cmd/server`，应使用 `.env`。

`godotenv.Load` 默认不覆盖已有环境变量。PowerShell 中残留旧值时，可以先清除：

```powershell
Remove-Item Env:REDIS_PASSWORD -ErrorAction SilentlyContinue
```

.env 不应提交真实密码；.env.example 只放变量名和示例值。

### 3.2 创建并验证客户端

```go
rdb := redis.NewClient(&redis.Options{
    Addr:     "127.0.0.1:6379",
    Username: "default",
    Password: os.Getenv("REDIS_PASSWORD"),
    DB:       0,
})
defer rdb.Close()

ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
err := rdb.Ping(ctx).Err()
cancel()
if err != nil {
    log.Printf("Redis 连接检查失败：%v", err)
    // 是否退出取决于 Redis 是必需依赖还是可选缓存。
}
```

`NewClient` 创建客户端对象；`Ping` 验证连接及认证。Client 内部管理连接池，适合长期复用并供并发请求使用，不应在每次 GetTaskByID 调用中创建和关闭。

当前项目使用 go-redis/v9.23.0。该版本提供 `ContextTimeoutEnabled: true`，用于让网络读写遵循传入上下文的期限；同时仍需理解 DialTimeout、ReadTimeout、WriteTimeout 和重试等机制，不能把 ctx 理解为强制杀死操作的计时器。

## 4. Cache、Service、Repository 的职责与依赖

### 4.1 分层

| 层 | 职责 |
|---|---|
| Handler | 解析请求、调用 Service、返回 HTTP 响应 |
| Service | 协调缓存和数据库，决定降级和回填策略 |
| Repository | 操作 PostgreSQL |
| Cache | 操作 Redis，封装 key、JSON 和 TTL |

Cache.GetTask 是从 Redis 读取任务，不是查询 PostgreSQL。查询数据库由 Service 调用 Repository 完成。

### 4.2 结构与构造函数

```go
// internal/cache/task_cache.go
package cache

type TaskCache struct {
    client *redis.Client
}

func NewTaskCache(client *redis.Client) *TaskCache {
    return &TaskCache{client: client}
}
```

```go
// internal/service/task_service.go
// import "github.com/Im-Sage/cloudtask-go/day9/internal/cache"
type TaskService struct {
    taskRepository *repository.TaskRepository
    taskCache      *cache.TaskCache
}

func NewTaskService(repo *repository.TaskRepository, tc *cache.TaskCache) *TaskService {
    return &TaskService{taskRepository: repo, taskCache: tc}
}
```

入口统一组装：

```go
taskCache := cache.NewTaskCache(rdb)
taskService := service.NewTaskService(taskRepository, taskCache)
```

这叫依赖注入：Service 使用外部提供的对象，连接地址、密码和资源生命周期由入口管理。

`taskCache cache.TaskCache` 也可以，但赋值会复制结构体。当前结构体里只有 Client 指针，复制后仍共享同一个 Client，不会复制出新连接池。使用 `*cache.TaskCache` 与构造函数一致，也能表达共享对象，nil 可约定表示禁用缓存。nil 不等同于实时健康状态。

### 4.3 包导入与可见性

模块路径来自 go.mod：

```go
module github.com/Im-Sage/cloudtask-go
```

因此，本地 `day9/internal/cache` 可以直接按完整模块路径导入，不需要先上传 GitHub。

- 缓存文件应声明 `package cache`，入口声明 `package main`。
- cache 包不能导入自己，否则出现 `import cycle not allowed`，编辑器可能显示 missing metadata。
- 小写 `taskKey` 仅包内可见；大写 `TaskKey` 才能跨包访问。
- 建议 Service 传 ID，由 Cache 内部生成 key，避免向 Service 暴露命名细节。
- `day9/internal/cache` 只能被 day9 目录树内的代码导入。
- 转为 cache 包后，不能再通过 `go run .` 直接运行这个包；练习入口可移到 `cmd/cache-demo`。

## 5. Key、JSON、TTL 与缓存读写

本节的裸 Task JSON、v1 key 和 GetTask 三返回值示例记录引入空值缓存前的设计。当前已升级为 v2 包装格式和 GetResult，最新设计与剩余问题见第 13 节，不应将两种格式混写到同一个 key。

### 5.1 Key 设计

建议统一使用模型和查询使用的 int64：

```go
func taskKey(id int64) string {
    return fmt.Sprintf("cloudtask:task:%d:v1", id)
}
```

例如 ID 12 对应 `cloudtask:task:12:v1`。v1 是缓存格式版本标识，不会自动跟随数据库的 Version 字段变化。

Go 中十进制整数占位符是 `%d`，不是 `%i`；int 和 int64 不会自动互转。当前源码通过 int(id) 转换，建议后续统一成 int64。

列表缓存的 key 还需包含所有影响结果的条件，如用户、分页、状态与排序，避免错误复用。

### 5.2 JSON 的表示

```go
data, err := json.Marshal(task)
```

data 类型是 `[]byte`（字节切片），其中包含 JSON 文本。JSON 是格式，字节是存储和传输形式。

- `string(data)` 用于按文本查看内容，不是再次序列化。
- go-redis 可以直接接收 []byte，当前 Redis 数据类型是 String。
- 未赋值的 time.Time 会显示为 `0001-01-01T00:00:00Z`，表示 Go 零值时间，不是 Redis 生成的时间。

### 5.3 写入及覆盖

```go
err := c.client.Set(ctx, key, data, 5*time.Minute).Err()
```

相同 key 再次 SET 会覆盖旧值，并把 TTL 重新设为从本次写入起的 5 分钟，不会产生两条同名记录。即使值完全相同，也会重新设置 TTL。

传 0 表示不自动过期。若希望仅在 key 不存在时写入，可用 SetNX，其 bool 结果表示是否实际写入。

### 5.4 读取的三种结果

```go
func (c *TaskCache) GetTask(ctx context.Context, id int64) (model.Task, bool, error) {
    data, err := c.client.Get(ctx, taskKey(id)).Bytes()
    if errors.Is(err, redis.Nil) {
        return model.Task{}, false, nil
    }
    if err != nil {
        return model.Task{}, false, err
    }

    var task model.Task
    if err := json.Unmarshal(data, &task); err != nil {
        return model.Task{}, false, err
    }
    return task, true, nil
}
```

此建议示例需要 context、encoding/json、errors、model、redis 等对应导入，且 taskKey 使用 int64。

| 情况 | Task | hit | err |
|---|---|---|---|
| 命中且 JSON 解析成功 | 缓存任务 | true | nil |
| key 不存在或已过期 | 零值 | false | nil |
| 连接、认证或解析失败 | 零值 | false | 对应错误 |

不存在的 key 在 go-redis 的 Get(...).Bytes() 中表现为 data 为 nil、err 为 redis.Nil。Cache 层将它转换为正常的“未命中”，不将其当作基础设施故障。

### 5.5 数据库没有记录

单条查询使用 QueryRow(...).Scan(...) 时，pgx 没有查到记录会返回 pgx.ErrNoRows。当前 Repository 已将它包装成自己的 ErrTaskNotFound。

Service 应通过 err 判断，Handler 按项目错误约定映射 HTTP 状态，不应仅凭 task.ID == 0 判断任务不存在。列表查询为空通常返回空结果，而不是 ErrNoRows。

## 6. Service 中的缓存编排

本节示例是尚未引入空值缓存时的流程。加入空值缓存后，命中还需判断 Exists，数据库未找到需尝试写入短期空值并保留未找到错误，详见第 13 节。

下面是满足本阶段四项 TODO 的建议流程，不代表当前源码已经完全实现：

```go
func (s *TaskService) GetTaskByID(ctx context.Context, id int64) (model.Task, error) {
    // 1. 启用缓存才查询；只有命中且无错误时提前返回。
    if s.taskCache != nil {
        task, hit, err := s.taskCache.GetTask(ctx, id)
        if err == nil && hit {
            log.Printf("任务缓存命中：id=%d", id)
            return task, nil
        }
        if err != nil {
            log.Printf("读取缓存失败，准备回源：id=%d err=%v", id, err)
        } else {
            log.Printf("任务缓存未命中，准备回源：id=%d", id)
        }
    }

    // 2. 查询数据库，正常返回数据库错误。
    task, err := s.taskRepository.GetTaskByID(ctx, id)
    if err != nil {
        return model.Task{}, err
    }

    // 3. 只缓存成功查到的任务；TTL 由 SetTask 设置为 5 分钟。
    if s.taskCache != nil {
        if err := s.taskCache.SetTask(ctx, id, task); err != nil {
            log.Printf("回填缓存失败：id=%d err=%v", id, err)
        }
    }

    // 4. 回填失败仍返回数据库结果。
    return task, nil
}
```

此处缓存读取错误只记录日志，不直接返回。早期实际代码保留了 `if err != nil { return model.Task{}, err }`，因此 Redis 离线时返回 500；最新源码已经去掉这个提前返回，支持继续查询 PostgreSQL。源码路径已调整，实际故障验证见第 11 节。

上述降级以请求本身仍然有效为前提。如果请求 ctx 已经超时或取消，用同一个 ctx 查询数据库也会失败。后续可以为缓存分配较短的子上下文，同时保留原始请求上下文用于数据库查询。

启动降级和运行时降级是两件事：早期 main 中 Ping 失败就退出，会让 HTTP 服务无法启动；最新源码已改为只记录日志并继续启动。Service 能处理 nil 缓存，并不意味着入口已经提供了禁用缓存的配置开关。

## 7. Context 的父子关系与生命周期

### 7.1 用途与父子关系

Context 传递取消信号、截止时间和请求范围内的值，不是连接或连接池。

```go
ctx := context.Background()
pgCtx, pgCancel := context.WithTimeout(ctx, 5*time.Second)
defer pgCancel()
redisCtx, redisCancel := context.WithTimeout(ctx, 3*time.Second)
defer redisCancel()
```

```text
ctx
├─ pgCtx
└─ redisCtx
```

父取消会向下传播；子取消不影响父和兄弟。子上下文有效截止时间取父子较早者，不能延长父期限。

如果共同父上下文只是 Background，与分别从 Background 派生的取消和超时行为基本相同；如果父上下文可取消或有统一期限，共享父上下文便能统一控制一组操作。子上下文也能读取父上下文通过 WithValue 携带的值。

### 7.2 初始化上下文与请求上下文

| 上下文 | 用途 |
|---|---|
| main 中的启动 ctx | 初始化与连接检查 |
| c.Request.Context() | 当前 HTTP 请求内的缓存和数据库操作 |
| 从请求派生的子 ctx | 为某次操作增加更短期限 |

请求上下文通常不是 main 启动上下文的子上下文。客户端取消、连接关闭或 Handler 返回后，请求上下文会取消。

Client 和数据库连接池长期复用，每次操作传入当前请求的 ctx。初始化完成后取消初始化 ctx 不等于关闭池；资源通过 Close 关闭，前提是初始化函数没有把该 ctx 用作长期后台任务的生命周期。

### 7.3 超时预算与缓存 TTL

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

client.Set(ctx, key, data, 5*time.Minute)
client.Get(ctx, key)
```

- 5 秒是从创建 ctx 开始的共同操作预算，Set 和 Get 共享，不是每条命令各有 5 秒。
- 5 分钟是 Redis 数据有效期，与操作超时无关。
- ctx 到期不会直接结束 main，也不会强制杀掉 goroutine；操作需要配合处理取消信号。
- 调用 cancel 及时释放相关资源；长时间运行的 main 中，初始化检查结束后可立即调用对应 cancel。

### 7.4 判断操作为什么结束

```go
start := time.Now()
err := client.Set(ctx, key, data, 5*time.Minute).Err()
fmt.Printf("Set 耗时：%v，操作错误：%v，ctx 状态：%v\n",
    time.Since(start), err, ctx.Err())
if err != nil {
    return
}
```

| 输出 | 含义 |
|---|---|
| 操作错误 nil，ctx.Err() nil | 操作成功，此时上下文有效 |
| NOAUTH，ctx.Err() nil | 认证问题，不是超时 |
| connection refused | 目标地址没有可连接的监听服务或连接被拒绝 |
| ctx.Err() 为 DeadlineExceeded | 上下文已到期，不单凭这一点推断具体命令的返回原因 |

return 结束当前函数；在 main 中执行 return 后会运行 defer 并结束程序。练习中 Set 失败后 return 是流程选择，不是 ctx 自动退出程序。真实缓存回填失败时通常不应放弃已经查到的数据库结果。

## 8. 常见报错与排查方式

| 现象 | 原因或检查方向 |
|---|---|
| no configuration file provided | 工作目录不对，进入 day9 或用 -f 指定文件 |
| volumes.redis additional properties ... not allowed | redis 被写在顶层 volumes 下，应移到 services 下 |
| services 中出现名为 volumes 的服务 | 顶层 volumes 缩进错误，应与 services 对齐 |
| NOAUTH Authentication required | 当前连接未认证，检查 Go 是否读取到密码 |
| WRONGPASS | 用户名、密码或用户启用状态不正确 |
| connection refused | 检查 Docker、Redis 容器和 6379 端口映射 |
| missing metadata / import cycle not allowed | 本阶段原因是 cache 包导入了自身 |
| fmt.Sprintf unknown verb i | 十进制整数应使用 %d |
| int 不能用作 int64 参数 | 显式转换或统一 ID 类型 |
| non-declaration statement outside function body | := 只能写在函数内，包级变量使用 var |
| 日志原样显示 %s、%v | Println 不解析占位符，使用 Printf 或 log.Printf |
| 调用 fmt.Errorf 后没有日志 | Errorf 创建错误，不会打印，需返回或输出 |

检查密码是否读取到时，只输出是否为空，不直接打印密码：

```go
fmt.Println("是否读取到密码：", os.Getenv("REDIS_PASSWORD") != "")
```

## 9. 验证方法与后续工作

### 9.1 常用命令

在 day9 目录执行：

```powershell
docker compose config --quiet
docker compose up -d redis
docker compose ps redis
docker compose logs --tail 50 redis
docker compose exec redis redis-cli --askpass ping
```

config 检查并展示变量展开、合并和规范化后的配置，不启动容器；默认只输出终端，不保存文件。完整输出可能包含密码，分享前需要脱敏。配置合法不等于服务一定能启动。

查看指定 key 的值和 TTL：

```powershell
docker compose exec redis redis-cli --askpass GET cloudtask:task:11:v1
docker compose exec redis redis-cli --askpass TTL cloudtask:task:11:v1
```

TTL 正数表示剩余秒数，-1 表示无过期时间，-2 表示 key 不存在。需要重现冷缓存时，仅删除练习目标 key，不要清空整个 Redis：

```powershell
docker compose exec redis redis-cli --askpass DEL cloudtask:task:11:v1
```

### 9.2 本阶段观察

```text
第一次 GET /tasks/11：200，31.31ms
第二次 GET /tasks/11：200，519.9µs（约 0.52ms）
```

符合第一次回源并回填、第二次缓存命中的预期，但耗时不能单独证明缓存命中，也可能受连接建立和数据库自身缓存影响。应结合命中、未命中、回源及回填日志确认，不把这两次观察当成正式性能基准。

### 9.3 待验证场景

- 冷缓存：数据库有记录，首次查询成功并回填，后续命中。
- 数据库无记录：返回项目约定的未找到结果，不缓存 Task 零值。
- 缓存过期：重新查询数据库并回填。
- Redis 读取故障：最新源码已改为回源，需确认请求仍有效时实际返回数据库结果。
- Redis 写入故障：已有数据库结果仍能返回。
- 缓存关闭：传入 nil 后仍能查询；需在入口提供配置方式。
- 启动时 Redis 不可用：最新源码已允许继续启动，需确认 HTTP 服务实际监听。
- 请求超时或取消：普通查询应停止；已提交更新的缓存清理可使用独立、有界的上下文，见第 10 节。

### 9.4 后续改进

1. 对已调整的运行时降级与启动降级进行故障验证，记录状态码、数据和耗时。
2. UpdateTask 已加入失效；继续补齐删除任务、普通状态修改和乐观锁状态修改的详情缓存失效。
3. 统一 key 函数的 int64 ID 类型，整理遗留练习代码和错误日志。
4. 为缓存操作设置合适的短超时与连接超时，避免故障缓存耗尽整个请求预算。
5. 并发更新与回填可能再次写入旧值，简单删除缓存并不保证强一致；后续再根据实际需求研究版本校验等方案。

本笔记整理了已有观察和源码状态，尚未执行上述完整故障验证。代码片段按用途分段展示，需要放入相应包并补齐导入。

## 10. 更新后的缓存失效与一致性

### 10.1 先提交数据库，再删除缓存

更新采用“先写数据库、成功后删除缓存”的顺序，不要求更新时直接重写缓存；下一次查询未命中时再回源并回填。

```text
参数校验
  → Repository 更新 PostgreSQL 并提交事务
      → 失败：返回数据库错误，不执行正常缓存失效流程
      → 成功：尝试删除详情缓存
          → 删除失败：记录日志，仍返回数据库更新结果
```

最新 UpdateTask 已保留校验、检查 Repository 错误、判断 taskCache 非 nil，并在删除失败后返回数据库结果。Repository 在事务提交成功后才正常返回。

注释应写成“Redis 缓存删除失败，不改变数据库已更新成功的事实”，而不是“数据库删除失败”。返回更新成功符合数据库的实际结果；返回错误不能撤销已经提交的事务。

### 10.2 DEL 不需要提前 GET

```go
func (c *TaskCache) DeleteTask(ctx context.Context, id int64) error {
    key := taskKey(int(id)) // 与当前 key 函数参数类型一致
    return c.client.Del(ctx, key).Err()
}
```

DEL 是幂等的：同一 key 删除多次，最终仍是不在缓存中。key 不存在时返回删除数量 0、错误 nil，不是删除失败。提前 GET 会增加一次请求，还可能把 redis.Nil 误报为异常，检查与删除之间也不是原子操作。

最新 Cache.DeleteTask 已直接执行 DEL。当前 Cache 与 Service 都可能记录删除错误，后续可统一在 Service 记录，避免重复日志。

### 10.3 提交后的独立、有界 Context

问题场景：数据库已提交成功，客户端随即断开，请求 ctx 取消；如果删除缓存继续使用这个 ctx，清理操作可能立即失败。

建议仅在数据库提交成功后创建独立上下文，并给出新的短期限：

```go
task, err := s.taskRepository.UpdateTask(ctx, id, title, status)
if err != nil {
    return model.Task{}, err
}

if s.taskCache != nil {
    invalidateCtx, cancel := context.WithTimeout(
        context.WithoutCancel(ctx),
        2*time.Second,
    )
    defer cancel()

    if err := s.taskCache.DeleteTask(invalidateCtx, id); err != nil {
        log.Printf("提交后删除缓存失败：id=%d err=%v", id, err)
    }
}
return task, nil
```

- 独立：WithoutCancel 保留上下文值，不继承原请求的取消信号与截止时间。
- 有界：WithTimeout 设置自己的 2 秒期限，不允许无期限清理。
- 也可从 Background 派生，但会丢失原请求上下文中的追踪信息。
- 独立不等于异步，上例仍会等待删除返回；实际网络行为需要客户端配合。
- 不能保证成功：Redis 故障、进程崩溃等仍可能让缓存残留。

此示例是建议，当前 UpdateTask 仍使用传入的请求 ctx。若确认提交前取消且事务回滚，则无需执行正常的提交后缓存失效。若 COMMIT 时连接中断或取消，提交结果可能不确定，不能把所有提交错误都解释成“数据库肯定没改”。

### 10.4 删除失败为什么会不一致

```text
数据库标题 A → B，提交成功
Redis 删除失败，保留 A
后续读取命中 Redis，返回旧值 A
```

这是数据库与缓存之间的一致性问题，不是数据库数据丢失。PostgreSQL 的本地事务不能将 Redis 删除一起原子提交。

| 措施 | 价值 | 局限 |
|---|---|---|
| 5 分钟 TTL | 残留副本最终过期 | 有效期内可能读到旧值，不保证所有并发场景严格 5 分钟内收敛 |
| 独立、有界 Context | 减少请求取消导致的清理失败 | 无法解决 Redis 离线 |
| 有限重试与日志 | 应对短暂故障，便于排查 | 进程崩溃可能丢失内存中的重试任务 |
| Outbox 持久化失效任务 | 更新与“需要失效”的待办同事务保存，后台可恢复重试 | 增加实现复杂度，仍需处理并发回填 |

另一种竞争条件即使 DEL 成功也会出现：

```text
请求 A：缓存未命中，从数据库读到旧值
请求 B：更新数据库，删除缓存成功
请求 A：把之前读到的旧值回填 Redis
```

所以“先更新数据库再删除缓存”是常用策略，不等于强一致保证。当前学习项目先落实 TTL、正确失效顺序、日志和降级；关键业务若要求立即读到新值，可让关键读取绕过缓存，并另行设计并发一致性机制。

## 11. Redis 故障降级复盘

### 11.1 对象存在不代表远端服务在线

```text
TaskService → TaskCache 对象 → Redis Client 对象 → 网络 → Redis 服务
```

`s.taskCache != nil` 只表示 Go 内存中提供了缓存对象，不是健康检查。关闭 Redis 容器不会把已有指针自动改成 nil。

| 状态 | 含义 |
|---|---|
| taskCache 为 nil | 按项目约定未启用缓存 |
| 对象非 nil，命令报错 | 本次缓存访问失败，可能离线、认证失败或超时 |
| 命令成功，hit 为 false | 缓存正常，但没有目标数据 |
| 命令成功，hit 为 true | 有可用缓存，可以直接返回 |

保留 Client 后，Redis 恢复时后续命令可以再次尝试连接。对象不会因为一次操作失败自动消失。

### 11.2 运行时降级：避免在缓存错误处提前返回

实际观察：关闭 Redis 后查询存在的任务，接口返回 500，日志包含 `dial tcp 127.0.0.1:6379 ... actively refused it`。

当时原因是 Service 在缓存读取错误处分支 return，根本没有执行 PostgreSQL 查询。打印“准备回源”不会改变控制流程。

最新源码改为：读取失败只记录日志；只有“无错误且命中”提前返回，其余情况继续查询数据库。数据库查询成功后，回填失败仍仅记录日志。

需要注意，读取和回填都可能发生连接重试；接口能够返回 200 不代表故障时延已经合适。后续应限制缓存操作预算，并避免反复访问已知故障依赖。

### 11.3 启动降级：Ping 失败不一定要退出

此前启动日志先显示 PostgreSQL 连接池创建成功，再报告 Redis 连接失败，程序退出。Go 程序实际上已运行，只是 main 的 Ping 错误分支执行 return，HTTP 监听尚未开始。

最新源码已经改成：

```go
if err := rdb.Ping(redisCtx).Err(); err != nil {
    log.Printf("Redis 暂不可用，继续启动服务：%v", err)
}
// 继续组装 Service 和启动 HTTP 服务。
```

| 故障时机 | 负责处理的位置 |
|---|---|
| HTTP 服务启动前 Redis 不可用 | main 允许继续启动 |
| HTTP 服务运行中 Redis 不可用 | Service 回源并容忍回填失败 |
| 数据库提交后删除缓存失败 | 返回数据库结果，记录失效失败并按策略补偿 |

允许继续启动仍可能包含 Ping 的失败等待和重试。这里把 Redis 定义为可选缓存；若承担会话或必需业务状态，不能不加分析地使用相同策略。

### 11.4 下一轮验证清单

以下是待执行的检查，不是已通过的测试结果：

1. 运行中关闭 Redis，查询 PostgreSQL 已有任务，确认 200 与正确数据，记录故障耗时。
2. Redis 保持关闭，重新启动 Go 服务，确认 HTTP 端口实际开始监听。
3. Redis 关闭时更新任务，确认数据库更新成功，不因缓存清理失败误报失败。
4. 重新启动 Redis，确认客户端后续可以恢复访问；检查旧缓存是否残留。
5. 预热详情缓存后更新任务，确认缓存被删除，下一次查询返回新值并回填。
6. 更新不存在的任务，确认数据库错误正常返回，不走成功后的失效流程。
7. 补齐其他写入入口的失效；单独验证请求取消与提交后清理的边界。

本次只更新开发笔记，没有修改业务代码，也没有实际启停服务或更新数据库。

## 12. VS Code 断点调试与工作目录

### 12.1 现象与排查结论

在 task_handler.go 的 GetTaskByID 中，对调用 Service 的语句设置断点后，按 F5 没有停住：

```go
task, err := h.taskService.GetTaskByID(c.Request.Context(), idInt)
```

需要分别确认“调试程序是否成功启动”和“请求是否执行到断点”。本次检查发现，原 launch.json 的 cwd 是 cloudtask-go，而 main 使用 `godotenv.Load("../../.env")`，工作目录与相对路径不匹配，可能导致加载失败并提前 return。

当前 launch.json 已将 cwd 调整为 day9/cmd/server，与该加载路径匹配。但配置正确不代表已验证断点命中，仍需启动调试并发送请求。

### 12.2 program 与 cwd 的区别

| 配置 | 作用 |
|---|---|
| program | 指定要编译和调试的 Go 入口包目录 |
| cwd | 指定被调试程序运行时的工作目录，决定相对文件路径如何解析 |
| workspaceFolder | VS Code 当前打开的工作区文件夹路径 |

源文件所在目录、入口包目录和运行工作目录可以不同。设置 program 不代表 cwd 自动与之相同；显式配置可以减少环境差异。

本项目在工作区根目录为 cloudnative_internship_plan_2028 时，使用以下配置：

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "调试 CloudTask",
      "type": "go",
      "request": "launch",
      "mode": "debug",
      "program": "${workspaceFolder}/cloudtask-go/day9/cmd/server",
      "cwd": "${workspaceFolder}/cloudtask-go/day9/cmd/server"
    }
  ]
}
```

如果直接打开 cloudtask-go 作为工作区，应去掉路径中重复的 `/cloudtask-go`。launch.json 位于当前工作区的 .vscode 目录下。

在上述 cwd 下，`../../.env` 对应 day9/.env。如果将 cwd 改成 day9，则代码中应改用 `.env`。不要为了调试而在多个目录复制密码文件，优先保持配置和路径一致。

### 12.3 F5 不会自动调用 HTTP 接口

```text
F5 → 调试器构建并启动 main
   → 初始化配置、数据库和 Redis
   → HTTP 服务开始监听
   → 等待请求
发送 GET /tasks/12
   → 路由匹配
   → Handler 解析 ID
   → 执行到 Service 调用语句，断点暂停
```

GetTaskByID 不是程序启动时自动执行的函数。服务正常等待请求时没有停在 Handler 断点，是正常现象。

### 12.4 实际操作步骤

1. 确认 VS Code 已安装并启用 Go 扩展，Delve 调试工具可用；缺少工具时查看扩展提示。
2. 停止之前通过 go run 启动的同端口服务，避免 8080 被占用，也避免请求发送给未受调试器控制的旧进程。
3. 保存代码，在“运行和调试”面板选择“调试 CloudTask”，按 F5，或使用菜单“运行 → 启动调试”。
4. 检查调试控制台或终端，确认没有编译、.env 加载或数据库初始化错误，且 HTTP 监听没有失败。单独打印 Starting server 不足以证明监听成功，还要检查后续错误。
5. 对 Handler 中实际调用 Service 的可执行语句设置断点，不依赖行号永远是 39。
6. 在浏览器访问接口，或在另一个 PowerShell 终端执行：

```powershell
curl.exe http://127.0.0.1:8080/tasks/12
```

7. 程序暂停后查看 idInt、ctx 等变量，按 F10 单步跳过，按 F11 进入 Service，按 F5 继续运行，按 Shift+F5 停止调试。

请求需要匹配 GET /tasks/:id，且 ID 是合法正整数。无效 ID 会提前返回，无法到达调用 Service 的断点。

### 12.5 按现象定位

| 现象 | 优先检查 |
|---|---|
| F5 没有启动调试工具栏 | 当前调试配置、Go 扩展、快捷键绑定；尝试菜单启动并查看提示 |
| 启动后立即退出 | 编译错误、.env 路径、数据库初始化失败、main 提前 return |
| 报端口已占用 | 停止旧服务，或统一修改服务端口和请求地址 |
| 服务运行但断点不命中 | 是否发送了正确请求，是否访问了当前调试进程 |
| 断点为空心或提示未验证 | 查看断点提示，确认文件属于当前构建、入口与源码匹配，且行上有可执行代码 |
| 已暂停但客户端一直等待 | Handler 尚未返回，通常是调试暂停的预期表现 |

断点暂停期间真实时间仍继续流逝，Context 的截止时间可能到期；逐步执行后出现超时，不一定代表正常运行时也会超时。不要为此直接移除生产逻辑中的超时和取消处理。

本节记录配置检查与建议操作，未实际通过 VS Code 启动调试器验证。调试配置字段参考 [Go 扩展官方调试文档](https://github.com/golang/vscode-go/blob/master/docs/debugging.md)。

## 13. 空值缓存：缓存穿透防护与修复复盘

### 13.1 为什么缓存“不存在”

缓存穿透指请求的数据在缓存和数据库中都不存在，反复查询仍会访问数据库。针对相同不存在 ID，可短暂缓存数据库确认的“不存在”结论，减少重复回源。

该结论不是永久事实：后续创建、导入或恢复记录时可能改变。因此当前空值 TTL 为 30 秒，正常任务 TTL 为 5 分钟。短 TTL 平衡数据库保护、结果新鲜度和随机 ID 产生的缓存空间占用。新建记录时也应考虑清除对应空值，或明确接受剩余 TTL 内的短暂旧结论。

仅 PostgreSQL 明确返回 ErrTaskNotFound 才写空值。连接失败、超时、权限错误都不是“不存在”，不能据此写空值。

### 13.2 存储格式与返回类型

两个类型都定义在 internal/cache/task_cache.go，分别表达内部存储协议和对 Service 的返回结果：

```go
// Redis 内部 JSON 格式，小写类型仅在 cache 包内使用。
type taskCacheEntry struct {
    Exists bool        `json:"exists"`
    Task   *model.Task `json:"task,omitempty"`
}

// 对外查询结果，大写类型允许 Service 使用。
type TaskCacheResult struct {
    Hit    bool
    Exists bool
    Task   model.Task
}
```

```json
{"exists":true,"task":{"id":12,"title":"学习 Redis"}}
```

上面只节选任务字段。空值缓存则为：

```json
{"exists":false}
```

Task 是指针，nil 加上 omitempty 可以省略 task 字段；`&model.Task{}` 是非 nil 指针，不会被省略。

| 状态 | Hit | Exists | err | Service 行为 |
|---|---|---|---|---|
| Redis 没有 key | false | 无意义 | nil | 查数据库 |
| 命中不存在缓存 | true | false | nil | 返回任务不存在 |
| 命中正常任务 | true | true | nil | 返回任务 |
| Redis 访问或缓存解析失败 | 不使用 | 不使用 | 非 nil | 记录错误并回源 |

Hit 表示有可用缓存结论，不代表任务一定存在；只有 Hit 为 true 时才解释 Exists。

当前方法为 GetResult、SetEntry、SetNotFound、DeleteTask，使用 `cloudtask:task:<id>:v2`。升级版本用于隔离原来的裸 Task JSON。旧 v1 可自然过期，不必清空整个 Redis。

### 13.3 四处问题的修复情况

| 问题 | 当前源码状态 |
|---|---|
| 按 model.Task 解析包装 JSON，得到零值任务 | 已改为解析 taskCacheEntry，但遗漏空值分支，尚未完整修复 |
| Service 命中后不检查 Exists | 已修复：Exists=false 返回 repository.ErrTaskNotFound |
| 数据库未找到分支未判 nil、缓存错误覆盖数据库错误、返回 nil 错误 | 已修复：先判断缓存对象，写入失败仅记录，最终返回原数据库错误 |
| SetNotFound 保存非 nil 的零值 Task 指针 | 已修复：Task=nil，JSON 只保留 exists=false |

原先将包装 JSON 解码为 Task 时，encoding/json 默认忽略未知字段，可能没有解析错误却得到零值 Task。“Unmarshal 返回 nil”不等于业务内容正确，还需校验缓存协议和任务 ID。

### 13.4 GetResult 仍缺少的关键分支

当前读取在解析 entry 后立即判断：

```go
if entry.Task == nil || entry.Task.ID != id {
    return TaskCacheResult{}, fmt.Errorf("任务缓存内容无效：id=%d", id)
}
```

合法空值恰好是 Exists=false、Task=nil，所以会被误判为无效缓存。Service 随后继续查询数据库，并再次写入空值；接口可以正确返回 404，但防穿透并未生效，反复请求还可能不断刷新 30 秒 TTL。

应先识别不存在结论，再校验正常任务。以下是建议补齐的分支，当前尚未写入业务源码：

```go
var entry taskCacheEntry
if err := json.Unmarshal(data, &entry); err != nil {
    return TaskCacheResult{}, err
}

if !entry.Exists {
    return TaskCacheResult{
        Hit:    true,
        Exists: false,
    }, nil
}

if entry.Task == nil || entry.Task.ID != id {
    return TaskCacheResult{}, fmt.Errorf("任务缓存内容无效：id=%d", id)
}

return TaskCacheResult{
    Hit:    true,
    Exists: true,
    Task:   *entry.Task,
}, nil
```

该示例假设 v2 只由遵循协议的写入方法维护。若要严格识别缺失 exists 字段、空对象或 JSON null 等异常内容，还需增加协议字段存在性校验，不能把所有缺失布尔字段都当成合法空值。

### 13.5 Service 保留数据库结果语义

缓存命中后的处理：

```go
if err != nil {
    log.Printf("读取缓存失败，准备回源：%v", err)
} else if result.Hit {
    if !result.Exists {
        return model.Task{}, repository.ErrTaskNotFound
    }
    return result.Task, nil
}
```

数据库明确未找到后的处理：

```go
task, err := s.taskRepository.GetTaskByID(ctx, id)
if errors.Is(err, repository.ErrTaskNotFound) {
    if s.taskCache != nil {
        if cacheErr := s.taskCache.SetNotFound(ctx, id); cacheErr != nil {
            log.Printf("空值缓存写入失败：id=%d err=%v", id, cacheErr)
        }
    }
    return model.Task{}, err
}
if err != nil {
    return model.Task{}, err
}
// 查询成功：尝试 SetEntry，再返回 task。
```

Repository 将 pgx.ErrNoRows 转换为项目的 repository.ErrTaskNotFound；Service 决定写空值；Handler 使用 errors.Is 将未找到映射为 404。错误通过 %w 包装仍可识别，不应靠 Task.ID==0 或错误文本比较。

Redis 离线时，空值写入失败不能覆盖数据库的未找到错误，否则本应返回 404 的请求会错误地变成 500。未启用缓存时也应安全返回 404，而不是调用 nil 对象。

### 13.6 剩余清理与验证

遗留 fun1 仍使用 v2 key 写入裸 Task JSON，目前不是 HTTP 请求路径，但如果以后调用，会污染新格式。应删除旧练习写入，或改用 SetEntry；不应在 v2 下混用两种协议。更新、删除的失效也要和读写使用相同 v2 key，当前 DeleteTask 已使用 v2。

| 验证场景 | 预期 |
|---|---|
| 存在 ID 首次请求 | 查库、写正常包装 JSON，返回完整 Task |
| 存在 ID 再次请求 | 命中正常缓存，数据与数据库一致 |
| 不存在 ID 首次请求 | 查库、写 exists=false，返回 404 |
| 同一不存在 ID 在 30 秒内再次请求 | 命中空值、返回 404，不增加数据库回源次数 |
| 空值过期后请求 | 再次查库，根据当前结果重建缓存 |
| Redis 离线、数据库无该记录 | 记录缓存错误，最终仍返回 404 |
| 缓存对象为 nil、数据库无该记录 | 正常返回 404，不 panic |
| 正常任务缓存内容缺失或 ID 错误 | 视为无效缓存并回源，不能返回零值任务 |

验证空值是否有效，必须看数据库查询计数或回源日志，不能只看接口一直返回 404。缓存穿透防护主要减少同一不存在 ID 的重复回源；大量不同随机 ID 仍可能访问数据库，短 TTL 空值缓存不等于覆盖全部穿透场景。

本节基于最新保存的源码检查，未执行接口或数据库验证；只更新笔记，不修改业务代码。

## 14. 布隆过滤器接入：构建、查询与增量维护

### 14.1 与空值缓存配合的目的

空值缓存擅长减少同一个不存在 ID 的重复回源；大量不同的随机 ID 仍可能逐个访问数据库。布隆过滤器用于在回源前排除明显未加入过的 ID。

| 组件 | 保存的内容 | 结论 |
|---|---|---|
| PostgreSQL | 完整任务记录 | 业务数据的最终依据 |
| 任务缓存 | 正常 Task 包装 JSON 或不存在结论 | 命中则复用最近查询结果 |
| 布隆过滤器 | 已加入 ID 的概率性标记 | 未加入，或可能加入过 |

过滤器不判断任务缓存是否存在。任务缓存过期，过滤器中的标记不会因此消失。只有数据库 ID 已完整装载且增量同步正确时，否定结果才可辅助判断数据库不存在。

允许假阳性：不存在的 ID 可能被放行；对已成功加入且过滤器未损坏的元素，不会因概率性碰撞产生假阴性。漏装载、新增未同步和过滤器丢失属于维护错误，不能归因于正常误判率。

### 14.2 对象、key 与依赖注入

```go
type TaskBloom struct {
    client *redis.Client
    key    string
}
```

key 是整个过滤器的 Redis 名称，不是单个任务 ID：

```text
cloudtask:task:12:v2                → 任务 12 的普通/空值缓存
cloudtask:task:bloom:build:<UUID>   → 一组任务 ID 的过滤器
```

TaskBloom 必须大写，Service 才能声明 `*cache.TaskBloom`。task_cache.go 和 task_bloom.go 虽同属 package cache，但 Service 属于另一个包，不能访问 cache.taskBloom。client、key 保持私有即可。

当前 TaskService 通过 NewTaskService 同时接收 Repository、TaskCache 和 TaskBloom。TaskBloom 不持有 Repository：由启动流程取 ID，再交给 Build，保持数据库读取与 Redis 操作的分工。

### 14.3 Build、Add、MayContain

| 方法 | 当前职责 |
|---|---|
| Build(ctx, ids) | 创建新过滤器，批量装载已有 ID |
| Add(ctx, id) | 新增单个 ID，供新建任务等增量维护使用 |
| MayContain(ctx, id) | 查询 ID 是否可能已加入 |

当前 Build 使用目标误判率 0.001、初始容量 100000，每批 1000 个 ID。关键调用：

```go
b.client.BFReserve(ctx, b.key, 0.001, 100000).Err()
b.client.BFMAdd(ctx, b.key, items...).Err()
b.client.BFAdd(ctx, b.key, id).Err()
b.client.BFExists(ctx, b.key, id).Result()
```

BFMAdd 接收多个元素，需要把 []int64 转为 []interface{} 或 []any，再用 `items...` 展开。BFAdd 只加入一个元素，不能把 ID 切片整体传进去充当批量加入。

Add 与 Build 的底层写入目的相似，但生命周期不同：前者是单条增量，后者包含创建与全量装载。Build 逐个调用 Add 也可以，但会产生更多网络往返。

### 14.4 启动与请求生命周期

```text
main
  → 创建数据库连接池和 Redis Client
  → 生成构建 key，创建 TaskBloom
  → Repository.GetIds(ctx) 查询数据库 ID
  → TaskBloom.Build(ctx, ids)
      → 失败：taskBloom=nil
      → 成功：将对象注入 Service
  → 启动 HTTP 服务
```

当前启动失败分支已真正设置 nil，不再只是打印“禁用”。不过 nil 只表示本次未启用对象，不代表运行期持续健康检查。

CreateTask 已改为：数据库失败返回原错误；成功后在 taskBloom 非 nil 时 Add；加入失败记录任务 ID 和错误，仍返回数据库创建结果。查询目前则先检查过滤器，否定直接返回未找到，其余继续缓存和数据库流程。

建议后续将任务缓存查询放在过滤器之前，减少热缓存请求的一次网络操作。更重要的是，在可信状态未完善前保持观察模式：过滤器否定时仍查数据库，验证是否存在遗漏。

### 14.5 构建 key 的自动生成与重启

```go
bloomKey := "cloudtask:task:bloom:build:" + uuid.NewString()
log.Printf("本次布隆过滤器 key：%s", bloomKey)
taskBloom := cache.NewTaskBloom(rdb, bloomKey)
```

使用 github.com/google/uuid，每次构建生成一次，Build、Add、MayContain 共用同一个对象里的 key，不能每次操作都重新生成。

固定 key 本身合法，但 BFReserve 对已存在 key 会报错；Add 则向原过滤器继续加入。不能把“启动”永远等同于“重新创建同名过滤器”，也不能仅凭 key 存在就认为它完整。

当前每次启动新建 UUID 版本，避免手动递增 001、002；代价是重建开销和旧 key 积累。不要先删正在使用的版本再装载，以免把不完整状态暴露给查询。后续可以实现构建新版本、发布版本、延后清理旧版本，但需处理构建期间增量与多实例协调。

### 14.6 可用性与存在性是两个维度

曾讨论的建议类型：

```go
type BloomResult struct {
    Usable   bool
    MayExist bool
}
```

MayExist 来自 BFExists 的返回值；Usable 表示系统是否允许依据这次否定结果拦截请求。命令成功不自动等于 Usable=true，过滤器 key 不存在时 BFExists 也可能正常返回 false。

当前源码仍使用 `(bool, error)`，BloomResult 只是注释草稿，尚未实现。因此不能把“已注入对象”和“过滤器可信”混为一谈。构建完整性、版本仍存在、新增同步失败后的停用及重新启用条件，都需补齐。

## 15. 布隆过滤器接入中的问题复盘

### 15.1 COMMAND INFO 返回 nil：文件存在不代表模块已加载

```powershell
docker compose exec redis redis-cli --askpass COMMAND INFO BF.RESERVE
```

当时返回 `(nil)`，表示当前实例没有注册命令，不是密码错误。检查容器确认已含 `/usr/local/lib/redis/modules/redisbloom.so`，问题是自定义启动命令没有加载它。

当前 Compose 已加入：

```yaml
command:
  - sh
  - -c
  - |
    exec redis-server \
      --requirepass "$$REDIS_PASSWORD" \
      --loadmodule /usr/local/lib/redis/modules/redisbloom.so
```

模块路径以实际镜像为准；不能仅凭此问题断言所有 redis:8-alpine 都不支持 Bloom。修改后运行 config 检查并更新容器，再通过 COMMAND INFO 和 MODULE LIST 确认实例实际能力。

### 15.2 environment.command 验证错误：缩进改变配置含义

错误为 `services.redis.environment.command must be a boolean, null, number or string`。command 多缩进后被当作环境变量，列表值不符合环境变量要求。

command 应与 environment、ports、healthcheck 同级，不能放在 environment 内。排查时先读错误路径，再检查对应 YAML 层级。

### 15.3 missing metadata：先看实际编译错误

当时 cache 中写了 `repository.GetIds()`，但定义是：

```go
func (r *TaskRepository) GetIds(ctx context.Context) ([]int64, error)
```

它是对象方法，需要 `taskRepository.GetIds(ctx)`，不是包级函数；从外部取 ids 再传入 Build 后，TaskBloom 不需要导入 repository。

missing metadata 是编辑器提示，不一定代表循环依赖或包未上传。应运行 go list、go build 查看底层原因。同包不能导入自己，但本次已确认的具体错误是 undefined: repository.GetIds。

数据库遍历结束还应检查 rows.Err()，避免部分扫描失败却把过滤器当作完整装载。

### 15.4 Go API 和错误处理易错点

| 问题 | 正确处理 |
|---|---|
| BFExists 直接赋给两个变量 | 它返回 BoolCmd，需要 `.Result()` |
| BFMAdd 传 items 而不是 items... | 展开切片，分别传入每个元素 |
| log 导入未使用 | 删除或实际使用，否则编译失败 |
| build 小写却从 main 调用 | 改为公开方法 Build |
| fmt.Errorf(...) 作为独立语句 | 只创建 error，既不打印也不结束函数，需 return 或改用日志 |
| Repository 创建失败返回 Task{}, nil | 应返回原 err，否则接口可能误报创建成功 |
| 构建失败后仍调用 Add | 先判断对象非 nil，避免 panic |

当前 CreateTask 已修复原错误返回、nil 判断和 Add 失败日志。GetTaskByID 仍无条件打印“布隆过滤器判断失败”，即使返回 true、nil 也会输出，后续应仅在 err 非 nil 时打印失败日志。

### 15.5 新任务查不到：先确认正在查哪个版本

曾在创建成功后查询 `cloudtask:task:bloom:build:001`，返回 0。此时 main 已使用 UUID key，Add 操作的是本次进程的新版本，旧版本不会自动同步。

排查顺序：

1. 从本次启动日志复制完整 bloomKey。
2. 使用创建响应里的真实任务 ID。
3. 检查 Add 失败日志，不把 HTTP 创建成功等同于 Redis 加入成功。
4. 确认连接的是相同 Redis 实例和逻辑数据库；当前 Go 使用 DB 0。

命令中的 `<UUID>` 是占位符，需替换后执行：

```powershell
docker compose exec redis redis-cli --askpass BF.EXISTS cloudtask:task:bloom:build:<UUID> 20017
docker compose exec redis redis-cli --askpass BF.MEXISTS cloudtask:task:bloom:build:<UUID> 12 20017
docker compose exec redis redis-cli --askpass BF.INFO cloudtask:task:bloom:build:<UUID>
```

BF.EXISTS 返回 1 表示可能加入过，0 表示未加入或过滤器不存在。过滤器保存位标记，不能列出原始 ID；验证完整性应把数据库 ID 集合逐个或分批查询，不能只看 BF.INFO 的统计。

## 16. 本阶段验收与下一步

### 16.1 已形成的功能方向

已加入“空值缓存 + 布隆过滤器”的穿透防护主流程、UUID 构建版本、启动全量装载和创建后的增量加入；不能据此宣称全部正确性边界已解决。

当前仍需优先处理：

1. GetResult 在 Task=nil 校验之前处理 Exists=false。最新保存源码仍缺这一分支，合法空值仍会被判为异常并回源。
2. 新任务提交后 Add 失败或尚未完成时，不能继续无条件信任否定结果；需有观察模式或可恢复的停用策略。不要在并发请求中无锁修改共享指针来充当健康状态。
3. 运行中 Redis 重建、过滤器 key 丢失或部分恢复时，必须绕过否定拦截。单独先 EXISTS 再 BF.EXISTS 也有竞争窗口，需一致性检查。
4. 启动中的 PostgreSQL 检查、Redis 检查、GetIds 和 Build 仍共享最外层 5 秒期限。前面耗时会压缩构建预算，建议初始化阶段分别设置合适的子期限，不让已过期 ctx 参与构建。
5. 数据库扫描与构建期间的新写入、多个实例各自持有不同 UUID 版本、绕过服务直接导入数据等同步问题尚未解决。
6. 定期清理确认不再使用的旧构建版本；清理不能删除其他实例仍在使用的过滤器。

RedisBloom 本身不替你保证 PostgreSQL 与过滤器同步。BFAdd 在 key 丢失时还可能自动创建只含少数新 ID 的过滤器，因此“Add 成功”也不能证明全量过滤器完整。

### 16.2 建议验证矩阵

| 场景 | 期望与观测方式 |
|---|---|
| 已有 ID 全量装载 | 所有数据库 ID 在当前版本中返回可能存在 |
| 正常任务缓存命中 | 返回完整任务，记录缓存命中 |
| 同一不存在 ID 重复请求 | 用过滤器放行的无效 ID 验证空值命中，不重复回源 |
| 大量不同不存在 ID | 观察模式统计潜在拦截率、回源数和误拦截情况 |
| 新建任务 | 当前版本包含真实返回 ID，GET 可查到 |
| Add 失败或请求取消 | 不把已提交任务错误地永久拦截；观察模式下仍可回源 |
| 构建中途失败 | 不使用半成品过滤器，服务继续查询数据库 |
| Go 重启 | 输出新 UUID，构建成功后使用，旧版本不误当作当前版本 |
| Redis 关闭或重建 | 请求可回源，不出现已有任务被统一返回 404 |
| 删除任务 | 不清除标准 Bloom 的共享位；查询以缓存失效和数据库结果为准 |

验证空值缓存时，若过滤器已直接拦截同一 ID，请求不会到达空值分支，不能据此宣称空值命中有效。应在关闭否定拦截的观察模式中验证，或单独构造“过滤器放行但数据库不存在”的测试数据。

本次更新依据 2026-11-09 保存的源码与此前排错记录，仅修改学习笔记；没有启停容器、创建任务或执行完整故障测试。当前属于已接入主流程、仍需补齐验证与一致性边界的阶段。

## 17. 2026-11-10：缓存击穿复现与 singleflight 接入

### 17.1 本次目标与涉及文件

本次针对“同一个任务的缓存失效后，多个并发请求同时回源数据库”接入 singleflight。缓存穿透通常涉及不存在的数据；缓存击穿关注热点数据在缓存未命中期间的集中回源，两者需要分别处理。

| 文件 | 本次用途 |
|---|---|
| `internal/service/task_test.go` | 使用真实 PostgreSQL、Redis，发起 50 个同 ID 的并发查询 |
| `internal/service/singleflight_learning_test.go` | 使用模拟数据，观察 20 个调用共享一次回调结果 |
| `internal/service/task_service.go` | 在缓存未命中或读取失败后合并同 key 的回源 |
| `internal/repository/task_repository.go` | 在数据库查询前记录回源日志 |
| `internal/cache/task_cache.go` | 读取空值缓存，区分缓存未命中与任务不存在 |

测试放在 Service 所在目录，是因为需要检查缓存读取、数据库回源、缓存回填这一完整业务流程。测试函数使用 `TestXxx(t *testing.T)`，文件名以 `_test.go` 结尾。

### 17.2 并发复现：统一准备依赖、集中放行请求

集成测试先创建数据库连接池、TaskRepository、Redis Client、TaskCache 和 TaskService；本次还构建了布隆过滤器。所有 goroutine 共用同一个 TaskService，不在每个 goroutine 内重新创建依赖。后续 singleflight.Group 属于 Service 实例，只有共用实例才能通过同一个 Group 合并请求。

复现前提：数据库中存在任务 12，Redis 正常可用，布隆过滤器允许该 ID 通过，并在并发开始前删除对应任务缓存。Redis 故障产生的回源应单独测试，不能混同为正常缓存失效场景。

核心并发结构如下，`svc` 和 `ctx` 由前面的初始化阶段准备：

```go
var wg sync.WaitGroup
startChannel := make(chan struct{})

for i := 0; i < 50; i++ {
    wg.Add(1)
    go func() {
        defer wg.Done()
        <-startChannel

        task, err := svc.GetTaskByID(ctx, 12)
        if err != nil {
            t.Errorf("查询失败：%v", err)
            return
        }
        if task.ID != 12 {
            t.Errorf("期望 ID=12，实际 ID=%d", task.ID)
        }
    }()
}

close(startChannel)
wg.Wait()
```

`make(chan struct{})` 创建一个只用于同步信号的无缓冲 channel。`<-startChannel` 接收但不保存值；没有数据且未关闭时阻塞，收到数据或 channel 关闭后可以继续执行。本例没有发送数据，使用 `close(startChannel)` 广播开始信号。

关闭后，当前等待者和稍后运行到接收语句的 goroutine 都能继续。只发送一次 `startChannel <- struct{}{}` 则只会放行一个接收者。channel 只能关闭一次。

集中放行不能保证所有 goroutine 在完全相同的时刻执行。若首次查询和回填非常快，部分请求可能直接命中缓存；稳定复现需要控制回源执行窗口，不能把请求数直接当成数据库查询数。

### 17.3 如何统计回源，以及本次前后对比

在 Repository.GetTaskByID 中，紧挨数据库 `QueryRow` 调用之前记录：

```go
log.Printf("[DB回源] GetTaskByID id=%d", id)
```

该日志统计到达数据库查询点的次数，也包括可能失败的查询；不表示这些 SQL 全部同时在数据库内执行，连接池可能让部分调用等待。不要把日志放在 Service 入口，否则统计到的是请求数。

本次用户提供的两轮输出对比如下：

| 观测项 | 接入前：10:04 | 接入后：11:26 |
|---|---:|---:|
| 并发请求数 | 50 | 50 |
| `启用了缓存` 日志 | 50 | 50 |
| `[DB回源] GetTaskByID id=12` 日志 | 50 | 1 |
| 测试结果 | PASS | PASS |

接入前，同一任务产生了 50 次回源；接入后，本轮只产生 1 次回源，且返回结果检查通过。`启用了缓存` 只代表进入缓存读取流程，不代表命中。

这份日志不能区分哪些请求等待了 singleflight，哪些请求直接命中了回填后的缓存。PASS 也只说明现有断言通过：当前集成测试检查查询错误和任务 ID，并未自动断言数据库查询次数；本次次数来自日志统计。

如需自动验收，可以在回源处使用 `atomic.Int64` 计数，在并发前后读取差值。普通整数的 `count++` 不适合多个 goroutine 并发修改。测试准备阶段的查询应排除在本轮计数之外。

### 17.4 singleflight 学习测试与 WaitGroup 问题

学习测试使用一个共享 Group，让 20 个 goroutine 调用相同 key 的 Do。在回调内用 `atomic.Int32.Add(1)` 统计实际执行次数，并用 `time.Sleep(100 * time.Millisecond)` 模拟耗时查询，返回字符串“任务12的数据”。

只要调用在执行时间上重叠，同一 key 的调用就能共享回调结果。Sleep 便于演示，但不严格保证所有 goroutine 都在这一时间窗口内进入 Do；调度很晚的调用可能开启新一轮执行。因此该学习测试有调度时序的局限，不能当作严格同步证明。

本次曾出现：

```text
20 个请求，回调实际执行 1 次
panic: sync: negative WaitGroup counter
```

原因是在循环内、goroutine 外写了 `defer wg.Done()`，同时 goroutine 内也写了一次。20 次 Add 对应了 40 次 Done。外面的 defer 属于整个测试函数，不在单次循环结束时执行；goroutine 全部完成使计数归零后，测试函数退出又触发额外 Done，计数变为负数并 panic。

当前已删除外层多余 defer，保留 goroutine 内的一次。原则是每个 `Add(1)` 对应一次 `Done()`，并在启动 goroutine 前完成 Add。

### 17.5 Service 中的 Group、key 与返回值

TaskService 新增字段：

```go
loadGroup singleflight.Group
```

Group 的零值可直接使用。构造函数原来按字段名初始化结构体，未填写的新字段自动取零值，因此 NewTaskService 参数和 main 调用不需要跟着改变。若改成 `*singleflight.Group`，零值会是 nil，需要初始化。Group 使用后不能复制，Service 继续使用指针接收者并共用实例。

本次回源 key 为：

```go
key := fmt.Sprintf("task:%d", id)
```

相同任务使用相同 key，不同任务使用不同 key。固定为 `"task"` 会错误地合并不同任务；加入随机数或每次变化的时间戳则无法合并。key 不必等于 Redis key，但必须包含所有影响结果的条件；将来若引入租户隔离，应把租户 ID 也纳入 key。共享范围仅限当前 Group，不跨服务进程。

Do 的返回值如下：

| 返回值 | 含义与处理 |
|---|---|
| `value` | 类型为 any，来自回调的第一个返回值；本项目断言为 model.Task |
| `err` | 回调返回的错误；先判断，非 nil 时向上返回 |
| `shared` | 本次执行结果是否被多个调用共享；业务中用 `_` 忽略 |

处理方式：

```go
if err != nil {
    return model.Task{}, err
}
task, ok := value.(model.Task)
if !ok {
    return model.Task{}, fmt.Errorf("singleflight 返回类型异常：%T", value)
}
return task, nil
```

回调应统一返回 model.Task，不要有的分支返回值、有的分支返回指针。`shared=true` 不能判断谁执行了回调：执行者和等待者都可能返回 true。它也不表示 Redis 命中，不能直接统计 true 的数量来计算节省的查询数。

### 17.6 双重检查：为什么 Do 内还要查 Redis

当前 GetTaskByID 保留入口处的布隆过滤器检查，之后按以下路径处理：

```text
第一次读取 Redis
  → 命中：返回任务或任务不存在
  → 未命中或读取失败：进入 loadGroup.Do(task:<id>)
      → 第二次读取 Redis
          → 命中：返回任务或任务不存在
          → 未命中或读取失败：查询数据库
              → 任务不存在：写入空值缓存，返回未找到
              → 其他数据库错误：返回错误
              → 查询成功：回填任务缓存，返回任务
```

第一次检查到 Do 回调执行之间，缓存可能已经被其他请求回填。例如 A 查缓存未命中后暂停，B 完成回源并回填，待 A 再进入 Do 时，B 的那轮执行已经结束。singleflight 不缓存完成后的结果，此时 A 会执行新的回调；第二次检查缓存可以避免 A 再查一次数据库。

第二次检查只在实际执行回调时进行，同一轮等待者直接共享结果。数据库查询和缓存回填都位于回调内，避免回调结束后才回填造成额外空窗。

### 17.7 穿透防护、故障降级与本次修复

接入 singleflight 后，Service 保留了以下处理：

| 情况 | 当前处理 |
|---|---|
| 布隆过滤器返回不存在且无错误 | 返回 ErrTaskNotFound；可信性限制仍见前文 |
| 布隆过滤器报错或未启用 | 继续缓存和数据库流程 |
| Redis 读取报错 | 记录日志，进入或继续共享回源 |
| 数据库任务不存在 | 尝试写入空值缓存，保留原未找到错误 |
| 空值或任务缓存写入失败 | 记录日志，不覆盖数据库查询结果 |
| 数据库其他错误 | 向上返回，不当作任务不存在缓存 |

排查时发现 GetResult 原先在处理 Exists=false 之前就检查 Task=nil，而 SetNotFound 正常写入的恰好是 Exists=false、Task=nil，导致合法空值缓存被误判为无效内容并回源。

截至本次文档更新，源码已在 Task 校验之前增加：

```go
if !entry.Exists {
    return TaskCacheResult{
        Hit:    true,
        Exists: false,
    }, nil
}
```

这说明第 16 节记录的该项待办已在代码中修复；仍需单独验证空值命中，避免请求被布隆过滤器提前拦截而无法覆盖此分支。

此前每个请求还会打印 `布隆过滤器判断失败：<nil>`。原因是正常放行分支也无条件打印失败日志。当前已改为仅在 err 非 nil 时记录，`<nil>` 本身不表示操作失败。

降级仍受请求期限影响：Redis 等待若耗尽共用 ctx 的总期限，随后数据库查询也可能失败。保留降级路径不等于故障时一定查询成功。本次正常并发测试没有验证 Redis 宕机、超时和数据库失败。

### 17.8 Context 的两个边界：当前尚未改造

当前源码使用 `Do()`，回调捕获执行该回调的请求 ctx。需要区分两个问题：

1. 等待者取消：B 正在 Do 内等待 A 的结果，即使 B 自己的 ctx 已取消，Do 也不会自动因此结束等待，因为 Do 没有监听 B 的 ctx.Done()。
2. 执行者取消：若 A 用自己的 ctx 查询数据库，A 被取消可能使查询失败；共享这轮执行的 B 即使仍然有效，也会收到该取消错误。

讨论过但尚未实现的方案：使用 DoChan，并在调用侧 select 结果 channel 和各自的 ctx.Done()，让每个等待者独立退出；共享查询另设有超时上限的 Context，避免绑定某一个请求的取消。

仅更换为 DoChan 不会自动解除回调对执行者 ctx 的依赖。等待者返回也不会自动停止共享查询；共享查询必须有明确的超时和资源生命周期。不能给数据库查询简单换成没有期限的 Background 后就认为问题已经解决。

### 17.9 执行命令与后续验收

在 `cloudtask-go/day9/internal/service` 目录运行：

```powershell
go test -run '^TestSingleflightSameKey$' -v -count=1
go test -run '^TestConcurrentCacheMiss$' -v -count=1
```

在 `cloudtask-go` 根目录运行时，在 go test 后加包路径 `./day9/internal/service`。

`-run` 使用正则表达式，`^` 和 `$` 限定完整测试名；单引号用于 PowerShell 原样传参。`-v` 显示测试日志，`-count=1` 禁用测试结果缓存。每轮集成测试仍需清理 Redis 任务缓存，Go 测试结果缓存和 Redis 数据缓存是两回事。

后续验证重点：

- 保留同 ID 冷缓存并发检查，并增加回源次数的自动断言。
- 不同 ID 并发返回各自任务，避免 key 设计造成串数据。
- 验证不存在任务的空值命中、Redis 读写失败和数据库错误传播。
- 修正测试初始化中的假通过：当前 .env 加载或数据库初始化失败仍可能打印后 return，应改为 t.Fatalf；Redis Ping 失败虽已 Fatalf，提示语“继续启动服务”仍需调整。
- Context 取消和独立等待退出需另行实现、测试；多进程协调不在当前 singleflight.Group 的能力范围内。

本节依据 2026-11-10 保存的源码及用户提供的测试输出整理。本次只更新学习文档，没有修改 Go 代码，也没有重新执行集成测试或故障测试。

## 18. 随机 TTL：分散过期时间与实验边界

### 18.1 从固定过期到随机过期

大量不同 Key 在相近时间写入相同 TTL，可能在相近时间失效，使后续请求集中回源。随机 TTL 用于分散过期时间，缓解这类缓存雪崩；它不能解决 Redis 整体停机，也不保证单个热点 Key 不发生击穿。

当前 `internal/cache/task_cache.go` 将逻辑提取为包内普通函数：

```go
// base > 0，maxJitter >= 0；这些是调用方需要满足的前提。
func randomTTL(base, maxJitter time.Duration) time.Duration {
    if maxJitter == 0 {
        return base
    }
    return base + time.Duration(rand.Int64N(int64(maxJitter)))
}

ttl := randomTTL(10*time.Second, 60*time.Second)
```

- 使用 `math/rand/v2`，包级随机函数支持并发调用，不需要手动设置种子。
- 当前 TTL 范围为 `[10s, 70s)`，按 Duration 精度生成，均值接近 40 秒。
- 早期 `rand.Int64N(61) * time.Second` 的写法是整秒随机，范围包含 70 秒，与当前函数略有区别。
- 随机值重复正常，不要求所有 Key 的 TTL 不同。
- 当前正常任务和空值缓存都使用此函数；两者是否使用相同范围应根据业务单独决定。
- 函数不依赖 TaskCache 的字段，提取后可复用，也能脱离 Redis 验证范围。当前函数没有显式校验负数参数，不能把注释当成运行时校验。

随机 TTL 改变的是过期时刻，不一定减少查询总数。例如 50 个缓存过期后各被访问一次，固定 TTL 和随机 TTL 都可能产生 50 次回源，只是后者更分散。过期本身不查询数据库，过期后的访问才触发回源；总查询数还取决于请求负载、观察时长和平均 TTL。

### 18.2 两次运行的证据与限制

2026-11-10 提供的日志统计如下，属于接入令牌桶之前的运行：

| 指标 | 16:00:46 第一次 | 16:01:05 第二次 |
|---|---:|---:|
| 查询请求 | 51 | 51 |
| GetTaskByID 回源日志 | 51 | 7 |
| 测试报告的查询错误 | 0 | 0 |
| 测试函数耗时 | 0.12 秒 | 0.10 秒 |

两次约间隔 19 秒。第二次只有 7 个 ID 回源，其余 44 个按执行路径可推断为缓存返回，与随机过期机制一致。但没有读取每个 Key 的 PTTL，也没有固定 TTL 对照组，因此不能据此量化削峰幅度。

测试要点：

1. 从数据库选择 50 个真实存在的 ID，不要假定连续 ID 都存在。`for i := 11; i < 62; i++` 实际是 51 个。
2. 预热后确认缓存内容存在且正确；Service 成功不保证缓存写入成功，因为回填错误会被记录后忽略。
3. 验证随机分布时读取 PTTL，考虑测量耗时，不要求样本全部不同。
4. 固定 TTL 与随机 TTL 使用相同负载、相近平均 TTL 和相同观察窗口，比较每秒回源量、错误数和耗时。
5. 如果为了制造集中失效，把所有 Key 统一改成 3 秒，就覆盖了随机 TTL；这是集中失效实验，不能用来验证随机 TTL 的分散效果。

### 18.3 Goroutine 参数与测试命令中的问题

查询不同 ID 时可显式传参：

```go
go func(id int64) {
    defer wg.Done()
    <-startChannel
    _, err := taskService.GetTaskByID(ctx, id)
    // 检查 err……
}(int64(i))
```

`GetTaskByID` 接收 int64，因此需要转换。项目声明 Go 1.26，`for i := ...` 也适用 Go 1.22 起每轮独立循环变量的语义，显式传参主要使意图更直观。

```powershell
go test -run '^TestRandomTTL$' -v -count=1
go test -run '^TestRedisStop$' -v -count=1
go test -run '^TestTokenBu$' -v -count=1
```

`count=1` 前漏写 `-`，会被当作包路径而报非法字符。测试文件须以 `_test.go` 结尾，测试函数须满足 `func TestXxx(t *testing.T)` 规则；`TokenBuTest` 不会被识别。`no tests to run` 后的 PASS 不表示业务测试执行过。

## 19. Redis 停机降级：Context 预算与超时复盘

### 19.1 为什么已有回源分支仍然失败

最初 `TestRedisStop` 用一个 5 秒 ctx 完成数据库初始化、Redis Ping、布隆过滤器构建和任务查询。Redis 连接失败后不断尝试，最终耗尽整个期限。日志虽打印了 `[DB回源]`，但数据库收到的也是已过期 ctx，因此返回 `context deadline exceeded`。

`[DB回源]` 打印在 QueryRow 之前，只证明进入仓储方法，不证明 PostgreSQL 实际执行了 SQL。

```text
共享的 5 秒预算
  → Ping 重试
  → 布隆过滤器构建失败
  → 第一次读取缓存
  → singleflight 内再次读取缓存
  → ctx 已到期，数据库回源失败
```

### 19.2 初始化、请求与缓存操作分别管理期限

初始化使用 initCtx；初始化完成后，查询从独立根创建请求 ctx。并发请求可在收到开始信号后各自创建请求 ctx，不复用前一个查询已经消耗过的预算。

请求内部，缓存使用原请求的短时子 context，数据库使用原请求 context：

```go
cacheCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
cacheResult, err := s.taskCache.GetResult(cacheCtx, id)
cancel() // 必须在读取之后

// 后续数据库仍使用 ctx，而非 cacheCtx。
```

- 子 context 不能延长父 context 的期限；父取消会传给子，子取消不会反向取消父。
- `defer cancel()` 也正确，表示当前函数返回时释放资源；100 毫秒期限仍会自动生效。
- 曾经把 `cancel()` 放在 GetResult 之前，导致所有读取立即报 `context canceled`。这会绕过正常缓存，不能把测试 PASS 当作修复成功。
- 第二次读取若需要执行，应创建新的 checkCtx，不能复用已取消或过期的 cacheCtx。首次读取已经报连接错误时，可考虑跳过第二次检查；该优化尚未接入当前代码。
- 当前 SetEntry、SetNotFound 已各自设置 100 毫秒写入子 context，避免回源成功后又长时间等待缓存回填。
- 100 毫秒是实验预算，不是所有环境都适用的固定配置；还需核对客户端的 ContextTimeoutEnabled、连接和读写超时，并测量实际耗时。

### 19.3 为什么一次 Get 会慢：客户端重试

本阶段检查的 go-redis v9.23.0 源码中，默认连接尝试数 DialerRetries 为 5，连接失败尝试间隔 DialerRetryTimeout 为 100 毫秒；命令 MaxRetries 默认 3，即初始执行加最多 3 次重试。实际尝试会受取消、错误类别等条件影响。

因此一次 Get 可能触发多轮连接尝试和退避，Service 再调用一次 Get，又会继续消耗预算。`failed to dial after 5 attempts` 是一轮连接尝试失败，不是五个业务请求失败。

缩短 DialTimeout 只限制单次连接尝试，不能直接当作整个命令总耗时。当前版本若选择关闭命令重试，配置是 MaxRetries=-1，填 0 表示默认值；DialerRetries=1 表示一轮只尝试一次连接。这些是可选调整，需在实际环境验证，当前笔记不表示源码已应用。

### 19.4 故障测试结果如何解读

以下是接入令牌桶之前、用户提供的多次 Redis 停机测试：

| 阶段 | 测试结果 | 测试函数耗时 | 结论 |
|---|---|---:|---|
| 初始化与查询共用 5 秒 ctx | FAIL | 5.00 秒 | 回源时父 ctx 已超时 |
| 查询改为独立 3 秒 ctx | FAIL | 5.29 秒 | 两次缓存读取仍耗尽请求预算 |
| 误在读取前 cancel | PASS | 4.08 秒 | 缓存被立即取消，不能代表正确超时控制 |
| 两次读取各用短时 ctx | PASS | 3.53 秒 | 查询成功，但回填仍等待连接失败 |
| 回填也增加短时 ctx | PASS | 2.60 秒 | 51 次回源、51 次回填失败，未报告查询错误 |

最后一次证明该次负载下数据库结果没有被 Redis 回填错误覆盖，不证明任意故障或负载下都能成功。测试仍需断言返回的 Task.ID，并验证 Redis 恢复后能够正常写入与命中。

“启用了缓存”只代表对象非 nil，不代表命中或服务在线。Cache 与 Service 可能重复打印同一个错误，不能把超时日志条数当成请求错误数。

测试函数耗时包含初始化；`ok ... Ns` 是包级耗时。要测量并发阶段，应在 close(startChannel) 前计时，在 wg.Wait() 后结束。日志精确到秒也不足以验证单次 100 毫秒预算。

### 19.5 main 与测试的区别及剩余问题

服务入口 `cmd/server/main.go` 的启动阶段也共用一个 5 秒 ctx，Redis 重试仍可能影响后续初始化。但 HTTP Handler 使用 `c.Request.Context()`，不会继承已到期的启动 ctx，也不会因为初始化 ctx 到期就自动关闭连接池。

当前 Ping 失败后仍创建缓存对象，并继续尝试构建过滤器；启动时跳过已知不可用依赖是待优化项。运行中 Redis 突然故障仍需依靠请求路径的超时控制。布隆过滤器 MayContain、DeleteTask 等 Redis 路径尚未统一加短时预算，不能认为所有 Redis 操作已完成改造。

## 20. 回源保护：Semaphore、连接池与令牌桶

### 20.1 四种机制分别负责什么

| 机制 | 保护范围 | 不保证什么 |
|---|---|---|
| singleflight | 同一 Group、同一 ID 的重叠请求共享一轮结果 | 不限制不同 ID 的总并发，也不缓存已完成的结果 |
| Semaphore | 限制受控代码段的同时执行数量 | 不直接限制每秒执行次数或排队数量 |
| rate.Limiter | 限制放行速率，并允许有限突发 | 不直接限制同时执行数量 |
| PostgreSQL 连接池 MaxConns | 限制该连接池的连接数量 | 不区分缓存回源与创建、更新等业务，也不自动限制等待者数量 |

如果查询只耗时 10 毫秒，10 个并发名额理论上每秒可周转约 1000 次，实际还受其他开销影响。因此“并发最多 10”并不等于“每秒最多 10”。

回源 Semaphore 设为 10、共享连接池 MaxConns 设为 20，可以降低回源占满整个连接池的机会，但不是严格为其他业务预留 10 个连接。若所有查询都经过同一 Semaphore、容量与连接池相同，作用可能重复，简单场景不一定需要两层。

### 20.2 共享字段与当前执行顺序

当前 TaskService 在构造函数中初始化：

```go
slots:   make(chan struct{}, 10),
limiter: rate.NewLimiter(rate.Limit(10), 5),
```

两者必须被同一 Service 的请求共享，不能每次调用 GetTaskByID 都创建新实例。可以在 main 创建后注入，也可以像当前代码一样由构造函数创建；关键不在 main，而在共享范围。多进程、多 Service 实例各有自己的配额，总量会叠加。

当前实际顺序：

```text
布隆过滤器 → 第一次读缓存 → singleflight.Do(按 ID 合并)
  → 第二次读缓存 → Limiter.Allow → Semaphore 获取名额
  → 数据库查询 → 回填缓存 → 回调返回、释放名额
```

用于保护数据库时，缓存命中不消耗回源令牌。同一轮 singleflight 的等待者也不各自获取令牌和名额。若目标是整个 HTTP 接口限流，则应在入口限制，缓存命中也计入。

### 20.3 管道信号量的获取、取消与释放

```go
select {
case s.slots <- struct{}{}:
    defer func() { <-s.slots }()
case <-ctx.Done():
    return nil, ctx.Err()
}
if err := ctx.Err(); err != nil {
    return nil, err
}
task, err := s.taskRepository.GetTaskByID(ctx, id)
```

管道满时，goroutine 阻塞在整个 select，尚未进入第一个 case，也不会执行后续查询。有空位则发送成功并占用一个名额；请求超时或取消则返回错误。两个分支同时就绪时 select 随机选择，因此获取后再检查 ctx.Err()。

直接写 `slots <- struct{}{}` 不会自动响应 ctx 取消；有了 select 才能取消排队。使用原请求 ctx 或其派生的排队 ctx 都合理，后者可另外限制排队时长；从 Background 创建独立排队 ctx 会丢失原请求取消关系。

成功获取后立即登记 defer。数据库报错时忘记释放会泄漏名额，最终后续请求持续排队、超时；若没有退出机制可能永久阻塞，但不一定触发 Go 全局死锁报错。defer 只在当前函数返回时执行，调用一直不返回也不会释放，所以数据库本身仍需受 context 约束。

这里位于 `func() (interface{}, error)` 回调内，错误时可返回 nil；外层 GetTaskByID 返回 `(model.Task, error)`，结构体不能为 nil，应返回 model.Task{}。

当前 defer 在 Do 回调结束才执行，因此名额覆盖数据库查询和缓存回填。若只想控制查询并发，可用独立方法包住获取、查询、释放，然后再回填。

### 20.4 为什么回源日志超过 10 条仍可能限流正确

2026-11-11 10:35:37 的 TestRandomTTL 在 0.23 秒内完成 51 次回源。日志都在同一秒，不表示 51 个同时查询：先完成的请求会释放名额，后续请求立刻接续。

“启用了缓存”在 Semaphore 之前打印，不受名额限制；回源日志只打印开始，没有结束。增加微秒时间戳和获取、释放日志有助于观察，但严格验证应记录受控区间并发峰值并断言不超过 10。len(slots) 是瞬时快照，日志还可能交错，不能代替并发断言。

### 20.5 令牌桶参数与 Allow、Wait

`rate.NewLimiter(rate.Limit(10), 5)` 表示每秒补充 10 个令牌、最多存储 5 个，初始桶满。速率单位是个/秒，容量单位是个，容量不需要装下一秒产生的全部令牌；空闲时桶满后的多余补充量被丢弃。

| 方法 | 令牌不足时 | 适合的策略 |
|---|---|---|
| Allow() | 立即返回 false | 快速拒绝，避免积压 |
| Wait(ctx) | 等待；取消或预计超过期限时返回错误 | 允许有界排队 |

当前 Service 使用 Allow，不等待：

```go
if !s.limiter.Allow() {
    return nil, repository.ErrRateLimited
}
```

当前 ErrRateLimited 定义在 Repository 包，消息为“请求过于频繁，请稍后重试”。这是 Service 侧限流错误，后续可考虑移到 Service 或公共错误包；Handler 应使用 errors.Is 映射 HTTP 429，当前检查尚未发现对应映射，不能把 HTTP 层限流响应视为已完成。

令牌消耗后不因执行结束而归还，数据库错误通常也不退令牌。当前先取令牌再等名额，排队取消也可能已消耗令牌，因此准确含义是限制进入回源流程的速率，并非每个令牌都对应实际 SQL。长期平均速率不等于任意一秒内的严格上限，等待名额也可能使实际 SQL 启动时间重新聚集。

### 20.6 令牌桶实验与验收待办

容量为 5 时，前三次 Allow、紧接着第四次和等待后第五次都返回 true 是正常的：第四次仍有令牌。要观察“前三次通过、第四次拒绝、补充后通过”，容量设为 3。当前 TestTokenBu 已设容量 3、等待 150 毫秒，但注释仍写 5，且只打印，没有断言。

`fmt.Println("passed为%w", passed)` 不解析占位符；布尔日志可用 `t.Logf("passed=%t", passed)`。%w 用于 fmt.Errorf 包装错误，不用于布尔输出。自动化测试应断言结果；可使用 AllowN 的显式时间参数，避免真实 Sleep 和调度间隔影响实验。

下一步验收：

- 单独验证 Semaphore 并发峰值不超过 10，测试中避免令牌桶先拒绝大多数请求而掩盖信号量行为。
- 验证 Allow 拒绝与 HTTP 429 映射；引入限流后，突发 51 个不同 ID 全部缓存未命中时，部分请求被拒绝是预期，不再沿用“所有请求都成功”的旧断言。
- 当前容量 5、速率 10 的桶可能使 Semaphore 容量 10 很少被占满；测试两种机制时需隔离变量。
- 验证排队时取消、查询报错后名额释放、正常缓存命中，以及 Redis 恢复后的写入与命中。
- 保留第 17.8 节的 singleflight 边界：Do 等待者不会因自己的 ctx 自动退出，共享执行仍绑定执行者 ctx；Semaphore 的取消处理没有解决这两项问题。
- 补充分阶段耗时、请求成功/限流/超时计数与回源并发峰值；初始化失败应 t.Fatal 或明确 t.Skip，避免打印后 return 导致假通过。

第 18—20 节依据 2026-11-11 检查的源码及本阶段用户提供的日志整理。本次仅更新笔记，没有修改 Go 代码，没有重新运行 Redis/PostgreSQL 集成测试；历史 PASS 不代表新加入的限流逻辑已完成验证。
