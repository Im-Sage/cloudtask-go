# Go 数据库连接池与并发执行笔记

## 1. 创建连接池不等于连接数据库成功

```
pool, err := pgxpool.NewWithConfig(ctx, config)
```

这一步创建连接池，不能仅凭 `err == nil` 判断数据库可用。连接池会在后台建立连接，实际连通性应通过 `Ping` 检查：

```
pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
defer pingCancel()

if err := pool.Ping(pingCtx); err != nil {
    pool.Close()
    log.Fatalf("数据库连接失败：%v", err)
}
defer pool.Close()
```

数据库未启动时，`Ping` 会返回错误。3 秒是时间上限，如果连接立即被拒绝，就会提前返回。

## 2. `ctx` 与 `pingCtx`

```
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
```

`ctx` 提供共同的取消信号，可以传给数据库操作。调用 `cancel()` 后，使用它的操作可以感知取消。

```
pingCtx, pingCancel := context.WithTimeout(ctx, 3*time.Second)
defer pingCancel()
```

`pingCtx` 是 `ctx` 的子上下文，专门为这次 `Ping` 设置超时。

| 行为                | 影响                                       |
| ------------------- | ------------------------------------------ |
| 调用 `cancel()`     | 取消 `ctx`，也会取消它的子上下文 `pingCtx` |
| 调用 `pingCancel()` | 只取消 `pingCtx`，不会取消父上下文         |
| 3 秒到期            | `pingCtx` 超时，使用它的操作应结束         |

`defer pingCancel()` 用于及时释放相关资源，不必等到超时。

**取消上下文与关闭连接池是两个动作。** 释放连接池仍需调用 `pool.Close()`。

## 3. 连接池参数

```
config.MaxConns = 3
config.MinConns = 1
```

- `MaxConns`：池允许的最大连接数，不表示启动时立即创建这么多个连接。
- `MinConns`：连接池尝试维持的最低连接数，不保证构造函数返回时已经达到这个数量。

即使 `MinConns = 1`，也可能看到 2 个连接：后台补充连接和 `Ping` 获取连接可能同时发生，分别触发建连。

## 4. 连接池统计的含义

```
stats := pool.Stat()
```

`Stat()` 获取的是**调用这一刻的统计快照**，不会主动检查数据库，也不会随着后续操作自动更新。需要新数据时，应重新调用 `pool.Stat()`。

| 指标                | 含义                                                      |
| ------------------- | --------------------------------------------------------- |
| `TotalConns()`      | 当前总连接数，包含正在构建、空闲和已借出的连接            |
| `AcquiredConns()`   | 当前被借出、尚未归还的连接数                              |
| `IdleConns()`       | 当前空闲、可以借用的连接数                                |
| `AcquireCount()`    | 成功获取连接的累计次数                                    |
| `AcquireDuration()` | 成功获取连接累计耗费的时间，不包含获取之后执行 SQL 的时间 |

这些数据属于**当前这个连接池**，不会跨程序运行累积，也不是整个 PostgreSQL 服务的连接统计。

例如：

```
总连接数：2
已使用连接：0
空闲连接：2
累计获取次数：11
累计等待时间：27.4569ms
```

结合一次 `Ping` 和十次 `Exec`，可以理解为：

- 统计时有两个连接，都已空闲。
- 一共成功借用连接 11 次。
- 连接可以反复使用，所以借用 11 次不需要创建 11 个连接。
- 27.4569ms 是获取连接的累计耗时，不能当作十次 SQL 的执行总时间。

## 5. 归还连接与关闭连接池

**归还连接**表示本次操作结束，连接回到池里，可以继续复用。

**关闭连接池**表示释放连接池及其数据库连接。

`Ping` 会在内部获取连接，并在返回前归还：

```
Ping 获取连接
    ↓
检查数据库
    ↓
归还连接
    ↓
Ping 返回
```

因此：

```
pool.Ping(pingCtx)

defer pool.Close() // 登记退出时关闭，此刻不会执行

fmt.Println(pool.Stat().AcquiredConns()) // 此时可以是 0
```

连接池还没有关闭，不代表连接正在被使用。

手动借用连接时，可以更直观地看到变化：

```
conn, err := pool.Acquire(ctx)
if err != nil {
    log.Fatal(err)
}

fmt.Println(pool.Stat().AcquiredConns()) // 1，假设没有其他任务

conn.Release() // 归还连接

fmt.Println(pool.Stat().AcquiredConns()) // 0
```

## 6. `WaitGroup` 的作用

启动 goroutine 后，主 goroutine 会继续往下执行，不会自动等待它结束。

`sync.WaitGroup` 用于等待一组任务完成：

| 方法        | 作用                                          |
| ----------- | --------------------------------------------- |
| `wg.Add(1)` | 将未完成任务数加 1，应在启动 goroutine 前调用 |
| `wg.Done()` | 将未完成任务数减 1                            |
| `wg.Wait()` | 阻塞等待，直到未完成任务数变为 0              |

`Wait()` 本身不减少计数。

### 放在循环内：任务逐个执行

```
for i := 0; i < 10; i++ {
    wg.Add(1)

    go func() {
        defer wg.Done()
        pool.Exec(ctx, "select pg_sleep(0.2)")
    }()

    wg.Wait()
}
```

执行顺序：

```
启动第 1 个任务 → 等待完成
启动第 2 个任务 → 等待完成
……
启动第 10 个任务 → 等待完成
```

虽然使用了 goroutine，但每轮都等待当前任务完成，任务之间没有并发执行。

后续任务通常能直接复用空闲连接，因此累计获取连接的耗时可能很小。

### 放在循环外：启动全部任务，再统一等待

```
var wg sync.WaitGroup

for i := 0; i < 10; i++ {
    wg.Add(1)

    go func() {
        defer wg.Done()

        _, err := pool.Exec(ctx, "select pg_sleep(0.2)")
        if err != nil {
            log.Printf("select 执行失败：%v", err)
        }
    }()
}

wg.Wait()

stats := pool.Stat()
```

执行顺序：

```
启动 10 个任务
    ↓
等待所有任务完成
    ↓
获取统计
    ↓
main 返回，执行 defer pool.Close()
```

10 个 goroutine 可以同时发起操作，但如果 `MaxConns = 3`，最多同时有 3 个任务持有数据库连接，其余任务需要等待。

**在 `wg.Wait()` 后统计，已使用连接通常为 0，是因为任务已经全部完成，与执行速度快慢无关。**

## 7. 没有等待任务就退出的问题

如果启动任务后直接让 `main` 返回：

```
for i := 0; i < 10; i++ {
    go func() {
        pool.Exec(ctx, "select pg_sleep(0.2)")
    }()
}

// 没有等待
```

程序可能在任务完成前开始执行 `defer pool.Close()`。如果还有建连操作，就可能遇到：

```
lookup localhost: operation was canceled
```

它表示主机名解析操作被取消。结合这段代码，应首先检查是否在任务完成前关闭了连接池或取消了上下文。

另外：

```
log.Fatalf("执行失败：%v", err)
```

会立即终止**整个程序**，并跳过尚未执行的 `defer`。在并发练习中，可以先用 `log.Printf` 记录错误，让任务正常返回，并由 `defer wg.Done()` 通知等待方。