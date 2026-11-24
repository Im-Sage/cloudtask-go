# 从 Go 语法到 CloudTask 交付

一次 `PATCH /tasks/opm/42` 会串起大半个项目。Gin 从 URL 取出任务 ID，从 JSON 取出新状态和旧版本；Repository 在 PostgreSQL 事务里更新任务并写事件；提交成功后，Service 删除 Redis 缓存。下次 GET 才会回源读到新值。读懂这一条链，要能回答三个问题：谁持有数据，谁负责等待，失败后谁来收尾。

要实际启动服务，使用[项目 README](../../README.md)。以下实验数字来自原笔记；命令、源码位置和默认行为按当前重构分支整理。

| 想复习的问题 | 阅读位置 |
| --- | --- |
| 为什么启动了 goroutine，程序却仍然卡住？ | [Go 数据与错误](#go-data)、[并发与取消](#concurrency) |
| 一次请求从哪里取得参数，又在哪里释放连接？ | [HTTP 与连接池](#request) |
| 如何证明更新没有写一半？两个人同时修改会怎样？ | [事务与版本](#transaction) |
| 为什么同样有索引，一条查询读 233 页，另一条只读 4 页？ | [执行计划](#explain) |
| 缓存挂了、热键过期、查不存在的 ID，各走哪条路？ | [Redis 读写路径](#cache) |
| 换目录、换容器、换版本以后，数据和测试怎么找？ | [配置与容器](#runtime)、[测试](#tests)、[交付与回滚](#delivery) |

<a id="go-data"></a>

## 一、从内存任务管理器理解 Go 的数据与错误

最早的练习把任务保存在 `map[int]*Task` 里。用 ID 找到任务后，指针使调用方能修改同一个实例。后来把数据放入 PostgreSQL，“找任务”仍是一个操作，关键变化是现在读取可能超时、可能查无此行，而且多个服务进程都能修改同一条记录。

| 当时写的语法 | 放进任务项目后如何理解 |
| --- | --- |
| `type TaskManager struct { ... }`，`func (t *Task) Complete()` | 结构体保存字段，带接收者的方法定义行为。指针接收者可以修改原任务。 |
| `NewTaskManager()` | 普通函数，集中把 map 初始化、把下一个 ID 设为 1。现在的 `NewTaskService` 同样组装依赖，并让所有请求共享同一个限流器。 |
| `make(map[int]*Task)` | 初始化可写的映射；向 nil map 写入会 panic。map 按需增长，若多个 goroutine 共享写入，还需要同步。 |
| `make([]*Task, 0, len(tasks))` | 长度从 0 开始，容量预留给 `append`。如果把长度也设为任务数再追加，前面就会多一段零值。 |
| `type TaskStore interface { ... }` | 方法签名匹配即可满足接口。早期内存练习用 Add、Get、Complete、Delete 表达能力；当前 Service 构造函数实际接收具体的 Repository 和 Cache 指针。 |
| `chan *Task` | 通道传递的是指针的副本。两个接收地址相同的 goroutine 仍可能修改同一块数据，需要明确谁能写。 |

**处理错误时把决定权留给调用者。** Repository 可用 `fmt.Errorf("读取任务 %d: %w", id, err)` 补充上下文，上层用 `errors.Is` 识别原错误并映射为 HTTP 状态。正常 `return err` 会执行已登记的 `defer`。`log.Fatalf` 会调用 `os.Exit(1)`，进程直接退出。当前入口把需要关闭的池和客户端放在 `run` 中，用 `defer` 清理；错误返回到 `main` 后才记录退出日志。[OneNote 第 1、3 页](../../go_grammer.pdf) · [服务入口](../../cmd/server/main.go)

<a id="concurrency"></a>

## 二、并发练习里最值得保留的三次卡住

**第一次：任务启动了，主程序却先结束。** 去掉 `go`，调用在当前 goroutine 顺序执行。加上 `go`，多个任务的 Sleep 或 I/O 等待可以重叠，输出顺序也会改变。先 `wg.Add(1)` 再启动 goroutine，内部 `defer wg.Done()`，启动完所有任务后才 `wg.Wait()`。把 Wait 放在启动循环里，每轮都会等当前任务完成。把 Add 改为 2 却只 Done 一次，计数永远不能归零。WaitGroup 只统计显式登记的工作。

**第二次：消费者退出，生产者却一直等。** 无缓冲通道的发送需要接收者。原实验在处理第二个任务时超时，消费者已经退出，生产者仍卡在 `jobs <- job`；函数没有返回，`defer close(jobs)` 和 `wg.Done()` 都执行不到。让下面两个动作竞争，发送方就有退出路径：

```go
select {
case jobs <- job:
    // 已被接收，可以继续提交下一项
case <-ctx.Done():
    return
}
```

`close(jobs)` 表示发送方已经交付完全部任务，缓冲区的剩余值还能继续读取。读完后 `v, ok := <-jobs` 的 `ok` 为 false，`range jobs` 结束。`cancel()` 通知整个调用链可以放弃尚未完成的工作；接收、处理、发送结果这些可能阻塞的地方都要能观察取消。若在循环的 `select` 加一个无条件打印的 `default`，没有任务时也会持续消耗 CPU。

**第三次：所有 Worker 都有结果，却没有人收。** 历史练习用 3 个 Worker 处理最多 10 个 Job。若 main 先 Wait 再读取无缓冲的 results，Worker 会卡在发结果，无法 Done。正确的关闭次序是：提交者发送完关闭 jobs；Worker 读尽或收到取消后返回；单独的 goroutine 等所有结果发送者结束，再关闭 results。这样 main 从一开始就能 `range results`。演示中普通任务约 500 ms，全局期限为 800 ms，Job 1 还故意报错。测试有自己的期限，不能把演示的一次输出顺序写成断言。[OneNote 第 1–3 页](../../go_grammer.pdf) · [历史练习记录](../../daynoete.md)

**等待结束与保护数据要各做一次。** 在计数实验中，两个 goroutine 都可能读到 5，再分别写回 6。`counter++` 的读、改、写需要由同一把 Mutex 保护；最后还要等工作结束再读总数。10 个 goroutine 各加 1000 次，此时才应得到 10000。这个办法只保护当前进程内遵守同一把锁的代码。两个 API 实例更新同一行时，协调点必须放在共享的数据库中。[并发与 Mutex 实验](concurrency-connections-index-mutex-notes.md)

<a id="request"></a>

## 三、跟踪一次 HTTP 请求和它借到的数据库连接

| 请求中的信息 | 例子 | 代码从哪里取 |
| --- | --- | --- |
| 路径参数 | `/tasks/42` | `c.Param("id")` 后转为正整数 |
| 查询参数 | `/tasks?status=pending&limit=5` | `c.DefaultQuery`，转换并校验范围 |
| 请求头与 JSON | `Content-Type`、`title`、`expected_version` | `c.GetHeader`、`c.ShouldBindJSON(&req)` |
| 客户端断开或请求取消 | 用户关闭连接 | `c.Request.Context()` 往下传到 Service、Repository 和外部调用 |

Gin 为当前请求准备 `*gin.Context`，Handler 用 `c.JSON` 写状态码、Content-Type 和序列化的响应体。传给数据库的是标准库的 `context.Context`。启动时用的 Context 有启动期限，把它长期保存在 Service 里，会让后续请求继承一个早已结束的期限。[OneNote 第 6 页](../../go_grammer.pdf) · [Handler](../../internal/handler/task_handler.go)

**收到 100 个请求，需要多少连接？** 同一请求的 JSON 解析、缓存命中及写 HTTP 响应都可能不占用 PostgreSQL 连接。多个请求可以轮流借一条连接。反过来，一笔事务即使在等行锁，也会继续占用连接；扩大池子可能只会增加等同一行的请求数。

- 启动时创建 `pgxpool.Pool` 并用带期限的 `Ping` 检验连通性。连接池由进程共享，关闭服务时再 `Close`。
- 查询多行要遍历 `rows.Next()`、检查 `rows.Err()` 并关闭结果集。显式 Acquire 的连接要 Release。事务从开始到提交或回滚使用同一连接。
- 原实验做一次 Ping、十次 Exec，随后快照为总连接 2、借出 0、空闲 2、累计借用 11 次、累计获取耗时 27.4569 ms。这里的 11 是复用次数；最后一项衡量获取连接的时间。要看变化，在两段负载之后分别调用 `pool.Stat()`。
- 慢请求先分段看：连接是否借不到，拿到后 SQL 是否慢，SQL 是否在等别人的锁。调高 `PG_MAX_CONNS` 只能直接改变第一段可用的并发名额。[连接池实验](pool-bingfa.md) · [数据库初始化](../../internal/database/database.go)

<a id="transaction"></a>

## 四、用两张表证明一次更新完整发生

`tasks` 保存当前状态，`task_events` 用外键关联任务，并可在 JSONB 的 `details` 中记录旧状态和新状态。这里的业务要求是“状态改变，并且有相应事件”。单独看 HTTP 200、UPDATE 的影响行数，或看到事件 ID 增长，都不足以验证两步一起成功。

| 写入口 | 数据库如何处理 | 客户端能观察什么 |
| --- | --- | --- |
| 普通 PUT | 在事务中更新标题、状态和版本，`RETURNING` 扫描新值，再写 `task_updated` | 返回任务和新版本；写条件只按 ID |
| 普通 PATCH | `SELECT ... FOR UPDATE` 锁住目标行，取得真实旧状态；状态改变才更新版本、写 `status_changed` | 两个写者依次获得行锁。相同状态直接提交；共享请求结构要求提供正版本，但此路径不比较客户端版本 |
| 乐观锁 PATCH | 先查旧状态与版本，再执行带 ID 和版本条件的 UPDATE；同一事务写事件 | 一个旧版本只允许一次匹配更新。客户端能区分成功、404 和 409 |

**为什么先查版本还不够？** A 和 B 可以同时读到 1。A 在两次读取之后先提交，B 的本地变量仍是 1。真正阻止 B 覆盖 A 的是下面 UPDATE 的 WHERE 子句（简化示意）：

```sql
UPDATE tasks
SET status = $1, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $2 AND version = $3
RETURNING id, status, version;
```

`Exec` 适合只需命令结果的 SQL；需要新版本时用 `QueryRow(...).Scan(...)` 读取 `RETURNING`。普通 SELECT 已返回结果行。两个客户端拿同一版本时，一个可成功到 2，另一个收到 409；先读最新值，再判断原本的修改意图是否仍成立。业务允许重试时，用新事务重新读取并重算决定。只把旧请求里的版本加一再重发，会把冲突检测的意义抵消。[乐观锁与重试](optimistic-lock-concurrency-notes.md) · [Repository 实现](../../internal/repository/task_repository.go)

**如何验证回滚？** 在独立测试库里先记下任务状态、版本和事件数，再让状态更新成功、事件插入因约束失败。事务内每条语句都要由同一个 `tx` 执行；任一语句出错都退出并回滚，还要检查 `Commit` 自身的错误。重新查询后应看到旧状态和旧版本，事件数不变。序列已分配的 ID 可能出现空档。遇到 `current transaction is aborted`，往前找失败的第一条 SQL。项目把该场景做成了 `TestChangeStatusFailRollback`。[事务实验](transaction-notes.md) · [仓储集成测试](../../internal/repository/task_test.go)

<a id="explain"></a>

## 五、读一份执行计划，再决定索引是否有用

在测试库对比“所有 pending”与“最新 20 条 pending”。索引 `(status, created_at DESC)` 的顺序与第二条查询匹配。项目的 HTTP 列表实际使用 `ORDER BY id DESC`；要优化接口，需要对它实际发出的 SQL 再取一次计划。

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM tasks WHERE status = 'pending';

EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM tasks WHERE status = 'pending'
ORDER BY created_at DESC LIMIT 20;
```

| 原笔记中的观察 | 怎样解释 |
| --- | --- |
| 小表查询估计 1 行、实际 4 行，`shared read=2`；规划 5.797 ms、执行 0.892 ms | `cost` 使用优化器成本单位。`actual time` 才是实测毫秒。`read` 指页面被读入 PostgreSQL 共享缓冲区，操作系统仍可能有缓存。重复运行时规划阶段也可能受益于预热。 |
| 加入实验数据后，全查返回 6670 行，Bitmap Index Scan 配合 Bitmap Heap Scan，`shared hit=233` | 索引先给出候选位置，位图让回表集中到物理页。样本中是 8 次索引页命中和 225 次表页命中。 |
| 带排序和 LIMIT 的查询返回 20 行，Index Scan，`shared hit=4` | 索引直接给出所需顺序，拿够 20 行即可停止。两条 SQL 完成的工作量不同。 |
| 事件查询出现 4 个索引候选，最终返回 0 行，并有 Sort | 回表还需检查当前事务快照的可见性。位图回表也会丢失索引顺序。仅凭这份计划无法断定具体哪些旧版本尚未清理。 |

阅读顺序：先看预期和实际行数差多少，再看 `loops`、`Index Cond` 和过滤掉的行，最后看排序、回表和缓冲页。索引命中很多行或 OFFSET 很深时，访问代价仍可能很大。加了 ANALYZE 会实际执行语句，以上对照选择只读 SELECT。[OneNote 第 7 页](../../go_grammer.pdf) · [执行计划原始样本](postgresql-explain-index-notes.md)

<a id="cache"></a>

## 六、按 ID 查任务时，Redis 如何参与决定

PostgreSQL 保存任务事实，Redis 保存可丢弃的查询结果。当前详情键形如 `cloudtask:task:42:v2`。这里的 v2 表示缓存值的编码版本；任务本身还有独立的 `version`，供并发更新校验。

1. **先查布隆过滤器。** 启动时从库里取 ID，建立带 UUID 的过滤器并分批加入。构建成功才标为可信。查询返回“可能存在”时继续；可信且明确未加入时才提前返回 404。创建任务后增量加入失败会撤销可信标记，避免把已经写进库的任务误拦。旧构建键清理按单实例设计，多实例共用时要重新设计生命周期。
2. **再读缓存。** 未找到 key 表示未知，应继续；命中 `{"exists":false}` 表示数据库此前确认不存在，返回 404；命中有效任务则返回 200。网络错误、认证错误或反序列化失败走回源路径。若仅判断 Hit，把空值当作零值 Task 发回，就会错误返回 200。
3. **把同时到达的同 ID 回源合并。** `singleflight.Group.Do` 让同进程、同键且时间重叠的调用共享一次回调。回调开头再查一次缓存，因为从第一次 miss 到进入回调之间，其他请求可能已填好。不同键仍可并行。
4. **在真正查库前控制负载。** 当前 Service 的令牌桶速率为每秒 10、突发容量 5；共享的带缓冲 channel 同时最多放行 10 个回源任务。一个控制速率，一个控制在途数量，数据库连接池再限制可借连接数。当前详情 Handler 会把限流错误走通用 500 路径。读缓存失败降级也受这些限制。
5. **按查库的真实结果回填。** 成功时写任务；确认查无记录时写空值；数据库故障不应缓存成“不存在”。当前正、负缓存的 TTL 都在 `[10s, 70s)` 随机取值，用于分散过期。Redis 写失败时仍保留本次数据库查询结果。

| 现场现象 | 应先检查什么 |
| --- | --- |
| 反复访问不存在的 ID 都查到库 | 区分 miss、空值命中及布隆过滤器的可信状态。穿透涉及不存在的数据。 |
| 热门任务一过期，同时打到库 | 看 singleflight 回调实际执行几次。击穿涉及一个热键的并发重建；多个不同键集中到期则可用 TTL 抖动缓解。 |
| 修改已成功，短时间仍读到旧值 | 更新或删除先写数据库，再失效详情缓存。失效失败或并发旧读回填都可能留下短暂旧值；TTL 限制其存活时间。 |
| Redis 超时以后连数据库也立即失败 | 检查是否把已超时的缓存子 Context 传给数据库。当前读取缓存有短预算，查库承接请求 Context。写后删除用 `WithoutCancel` 脱离请求取消，再设 100 ms 收尾期限。 |

一个可观察的实验是先 GET 真实任务两次，让第二次命中缓存，再用 PATCH 改状态，再次 GET 核对新版本。要证明“同键回源合并”，得让多个请求在第一次回调完成前重叠，并统计回调或数据库查询次数。连续发两次 GET 只能观察缓存复用。[Redis 实验与故障记录](引入redis.md) · [缓存编码和 TTL](../../internal/cache/task_cache.go) · [Service 读取路径](../../internal/service/task_service.go)
