# 引入 Redis：从容器配置到任务缓存

> 阶段笔记，整理于 2026-11-07。以 day9 项目当前代码为基础；标为“建议”的示例不代表源码已经完成修改。

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
| 未命中返回 Task 零值、false、nil | 已实现 |
| 命中直接返回，未命中查询 PostgreSQL | 已实现 |
| 数据库错误正常返回 | 已实现 |
| 缓存回填失败只记录日志 | 已实现 |
| Service 用 nil 判断是否启用缓存 | 已实现 |
| Redis 读取异常时继续查询数据库 | 待完善：当前仍直接返回错误 |
| Redis 不可用时允许 HTTP 服务启动 | 待完善：main 中 Ping 失败仍退出 |
| 修改、删除任务后使缓存失效 | 待完善 |

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
      - 'exec redis-server --requirepass "$$REDIS_PASSWORD"'
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

此处缓存读取错误只记录日志，不直接返回。当前实际代码仍保留了 `if err != nil { return model.Task{}, err }`，需要调整后才支持 Redis 异常回源。

上述降级以请求本身仍然有效为前提。如果请求 ctx 已经超时或取消，用同一个 ctx 查询数据库也会失败。后续可以为缓存分配较短的子上下文，同时保留原始请求上下文用于数据库查询。

启动降级和运行时降级是两件事：main 中 Ping 失败就退出，会让 HTTP 服务无法启动；Service 能处理 nil 缓存，并不意味着入口已经提供了禁用缓存的配置开关。

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
- Redis 读取故障：请求仍有效时应回源；当前源码尚未满足。
- Redis 写入故障：已有数据库结果仍能返回。
- 缓存关闭：传入 nil 后仍能查询；需在入口提供配置方式。
- 启动时 Redis 不可用：按可选缓存策略继续启动；当前源码尚未满足。
- 请求超时或取消：应终止相关操作，不能用 Background 绕过请求取消。

### 9.4 后续改进

1. 先补齐运行时降级与启动降级，再验证 Redis 不可用的行为。
2. 更新、删除或修改任务状态成功后，删除对应详情缓存，避免旧数据一直保留到 TTL 到期。
3. 统一 key 函数的 int64 ID 类型，整理遗留练习代码和错误日志。
4. 为缓存操作设置合适的短超时与连接超时，避免故障缓存耗尽整个请求预算。
5. 并发更新与回填可能再次写入旧值，简单删除缓存并不保证强一致；后续再根据实际需求研究版本校验等方案。

本笔记整理了已有观察和源码状态，尚未执行上述完整故障验证。代码片段按用途分段展示，需要放入相应包并补齐导入。
