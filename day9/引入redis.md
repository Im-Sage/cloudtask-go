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
10. 更新后的缓存失效与一致性
11. Redis 故障降级复盘
12. VS Code 断点调试与工作目录
13. 空值缓存：缓存穿透防护与修复复盘

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
