# Go 任务项目：乐观锁、版本返回与并发冲突处理

## 1. SELECT 不需要 RETURNING

```go
err := r.pool.QueryRow(
    ctx,
    `SELECT id, title, description, status,
            created_at, updated_at, version
     FROM tasks
     WHERE id = $1`,
    id,
).Scan(
    &task.ID,
    &task.Title,
    &task.Description,
    &task.Status,
    &task.CreatedAt,
    &task.UpdatedAt,
    &task.Version,
)
```

SELECT 已经指定返回哪些字段，不需要额外写 RETURNING。

RETURNING 用于 INSERT、UPDATE、DELETE 等操作，返回受影响记录的字段：

```sql
UPDATE tasks
SET status = $1,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $2 AND version = $3
RETURNING id, status, version;
```

QueryRow 不要求 SQL 必须包含 RETURNING，它用于接收一行结果。没有匹配记录时，错误在 Scan 时返回，pgx 中为 pgx.ErrNoRows。

**Scan 的接收顺序必须与 SQL 返回字段的顺序一致。**

## 2. JSON 标签的正确格式

```go
type ChangeStatusRequest struct {
    Status  string `json:"status"`
    Version int64  `json:"version"`
}
```

标签格式是 `键:"值"`，外面使用反引号，值使用双引号。

以下写法不正确：

```go
Version int64 `json:version`
```

这类写法通常能通过编译，但标签不符合约定，go vet 会报告问题。

JSON 标签指定结构体字段与 JSON 字段名的对应关系：

| Go 字段 | JSON 字段 |
| --- | --- |
| Status | status |
| Version | version |

请求：

```json
{
  "status": "completed",
  "version": 1
}
```

解析后：

```go
req.Status  // "completed"
req.Version // 1
```

标签不会改变 Go 字段名或类型，代码中仍使用 req.Version。

## 3. 乐观锁的核心：条件更新

```sql
UPDATE tasks
SET status = $1,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $2 AND version = $3
RETURNING id, title, description, status,
          created_at, updated_at, version;
```

- $1：要修改成的状态。
- $2：任务 ID。
- $3：客户端提交的预期版本。
- 更新成功后，数据库将版本加 1。

例如，数据库当前版本是 1：

```text
请求携带 version=1
    ↓
WHERE id=目标任务 AND version=1 匹配成功
    ↓
更新状态，同时将 version 改为 2
```

后续请求仍携带版本 1，就无法匹配。

**真正保证并发安全的是 UPDATE 中的版本条件。**

前面单独查询版本并比较，可以提前发现冲突，但不能替代条件更新，因为查询和更新之间仍可能发生其他写入。

## 4. 新版本如何返回给客户端？

```go
.Scan(&task.Version)
```

这里只是把数据库返回值写入 Go 变量，不会自动发送给客户端。

完整链路：

```text
数据库 RETURNING
    ↓
Repository 接收并返回 task
    ↓
Service 返回 task
    ↓
Handler 将 task 写入 JSON
    ↓
客户端保存新的 version
```

各层方法应返回 `(model.Task, error)`。错误分支返回：

```go
return model.Task{}, err
```

成功分支应在事务提交成功后返回：

```go
if err := tx.Commit(ctx); err != nil {
    return model.Task{}, err
}

return task, nil
```

如果只查询并赋值了 id、status、version，直接返回完整 model.Task 会带上其他字段的零值。可以选择：

- 返回完整任务，并完整扫描全部字段。
- 定义只包含这三个字段的更新响应结构体。

版本字段应保持类型一致：

```go
Version int64 `json:"version"`
```

## 5. Handler 返回 JSON 的方式

c.JSON 接收两个参数：状态码和响应数据。

不能这样写：

```go
c.JSON(http.StatusOK, gin.H{"message": "更新成功"}, task)
```

应该把消息和任务放进同一个响应对象：

```go
c.JSON(http.StatusOK, gin.H{
    "message": "更新成功",
    "data":    task,
})
```

响应示例：

```json
{
  "message": "更新成功",
  "data": {
    "id": 10,
    "status": "completed",
    "version": 2
  }
}
```

客户端下次修改时，应携带返回的新版本。

## 6. 版本冲突映射为 HTTP 409

定义错误：

```go
var ErrVersionConflict = errors.New("task version conflict")
```

Repository 补充错误信息时，使用 %w 包装：

```go
return model.Task{}, fmt.Errorf(
    "任务 %d 更新失败：%w",
    id,
    ErrVersionConflict,
)
```

HTTP 层识别：

```go
if errors.Is(err, repository.ErrVersionConflict) {
    writeError(
        c,
        http.StatusConflict,
        "TASK_VERSION_CONFLICT",
        "任务版本冲突，请刷新后重试",
    )
    return
}
```

- HTTP 409 的常量是 http.StatusConflict。
- %v 只格式化错误文本，不保留供 errors.Is 检查的包装关系。
- 写入错误响应后应 return，避免继续执行成功响应。

## 7. 区分“任务不存在”和“版本冲突”

首次查询任务：

```sql
SELECT status, version
FROM tasks
WHERE id = $1;
```

此时遇到 pgx.ErrNoRows，可返回 ErrTaskNotFound。

随后执行条件更新：

```sql
UPDATE tasks
SET status = $1,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $2 AND version = $3
RETURNING version;
```

此时没有返回记录，可能是读取之后版本被其他请求修改，也可能是任务被删除。

在当前项目约定中，可将这种“更新前提不再成立”的情况映射为版本冲突：

```go
if errors.Is(err, pgx.ErrNoRows) {
    return model.Task{}, fmt.Errorf(
        "任务 %d：%w", id, ErrVersionConflict,
    )
}
```

不能把条件更新无匹配一律解释为“任务不存在”。

## 8. 事务处理的注意事项

开启事务后先检查错误，再使用 tx：

```go
tx, err := r.pool.Begin(ctx)
if err != nil {
    return model.Task{}, err
}
```

随后登记回滚兜底：

```go
defer func() {
    rollbackCtx, cancel := context.WithTimeout(
        context.Background(),
        3*time.Second,
    )
    defer cancel()

    _ = tx.Rollback(rollbackCtx)
}()
```

任务更新和事件插入必须使用同一个 tx：

```text
开始事务
    ↓
读取旧状态和版本
    ↓
条件更新任务，递增版本
    ↓
插入状态变更事件
    ↓
提交事务
    ↓
返回更新后的任务
```

如果写事件失败，任务更新也应回滚。不能在事务提交前就向客户端返回更新成功。

## 9. 实验 A：两个 Goroutine 更新同一任务

初始数据：

```text
status=pending
version=1
```

| 请求 | 目标状态 | 预期版本 |
| --- | --- | ---: |
| A | completed | 1 |
| B | in_progress | 1 |

可以使用两个 goroutine 发送 HTTP 请求，下面为同步结构示意，省略具体请求代码：

```go
var wg sync.WaitGroup
start := make(chan struct{})

wg.Add(2)

go func() {
    defer wg.Done()
    <-start
    // 发送请求 A
}()

go func() {
    defer wg.Done()
    <-start
    // 发送请求 B
}()

close(start)
wg.Wait()
```

- WaitGroup：等待两个请求结束，Wait 放在两个任务启动之后。
- start：共同启动信号，关闭后两个等待者都可以继续。
- Mutex：只有并发修改共享数据时才需要；通过 Channel 汇总结果时可以不使用。

如果主 goroutine 在 wg.Wait 后才读取结果 Channel，结果 Channel 应能容纳所有任务结果，或者另开消费者及时读取，否则可能阻塞任务结束。

**共同启动不代表两条 SQL 在数据库中精确同时执行。** 如果要强制两边都先读到旧版本，再开始更新，需要专门的服务端测试同步点。

在没有其他写入或错误干扰时，预期结果：

```text
一个请求：HTTP 200
另一个请求：HTTP 409
最终 version=2
新增一条状态变更事件
```

谁成功不确定。实验前应确认初始版本确实为 1；重复实验可新建测试任务。

## 10. 悲观锁与乐观锁的区别

| 方案 | 当前项目中的实现 | 发生竞争后的行为 |
| --- | --- | --- |
| 悲观锁 | SELECT ... FOR UPDATE | 后来的事务等待，再基于最新记录继续处理 |
| 乐观锁 | UPDATE ... WHERE version = expectedVersion | 旧版本无法匹配，返回冲突 |

乐观锁并不意味着数据库完全不加锁。执行 UPDATE 时仍可能等待行锁；在默认 Read Committed 隔离级别下，前一个事务提交后，数据库会重新检查更新条件。

悲观锁也不保证请求一定成功。等待后仍可能因业务规则、超时、约束等原因失败。

## 11. 两个请求都要成功，是否应自动重试？

**可以重读后重试，但必须先确定业务允许。**

例如：

```text
初始：pending，version=1

A 成功：completed，version=2
B 冲突后重新读取
B 重试成功：in_progress，version=3
```

两个请求都成功，但 B 覆盖了 A 的状态结果。

需要明确：

- 是否允许 completed → in_progress？
- B 是否愿意覆盖 A 刚刚提交的修改？
- 客户端提交版本 1，是否意味着只允许修改自己看到的那个版本？

对于用户在页面上修改任务的场景，通常保留：

```text
返回 409
    ↓
重新查询最新任务
    ↓
用户确认
    ↓
携带新版本再次提交
```

不要在服务端悄悄替换客户端的预期版本，改变请求原本的前提。

### 适合自动重试的情况

| 操作 | 重试前的要求 |
| --- | --- |
| 计数增加 | 基于最新值重新执行，也可直接使用原子 SQL |
| 库存扣减 | 重新检查库存和业务条件 |
| 后台计算更新 | 重新读取数据、重新计算 |
| 状态修改 | 明确允许的状态转换及覆盖规则 |

本次讨论时，项目只校验目标状态属于合法枚举，没有校验旧状态到新状态的转换规则。如果增加重试，需要同时考虑这部分业务限制。

## 12. 自动重试应重跑完整事务

正确流程：

```text
开启新事务
    ↓
读取最新状态和版本
    ↓
重新校验业务规则、计算更新内容
    ↓
条件更新
    ↓
写事件并提交
    ↓
冲突则结束本次事务，有限重试
```

不能只把版本加 1，也不能沿用旧的 old_status 写事件。

重试还需要：

- 次数上限，例如最多尝试 3 次。
- 短暂退避，必要时增加随机延迟。
- 遵守 ctx 的超时和取消。
- 区分可重试冲突与不可重试错误。
- 避免重复发送通知等外部副作用。

有限重试提高成功率，但不能保证所有请求最终都成功。

版本冲突与“提交时连接中断、无法确认是否成功”不同，后者不能盲目重试；涉及客户端重复请求时，应考虑幂等标识。

应用定义的 ErrVersionConflict 也不等同于 PostgreSQL 的 serialization_failure。两者应分别识别和制定重试策略。

## 13. 多个写入入口必须统一维护版本

如果悲观锁接口和乐观锁接口都能修改同一任务，**所有相关写入入口都应维护版本号**。

悲观锁接口也应类似这样更新：

```sql
UPDATE tasks
SET status = $1,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $2;
```

否则可能发生：

```text
客户端读取 version=1
    ↓
悲观锁接口修改状态，但版本仍为 1
    ↓
客户端使用旧 version=1 更新成功
```

这时，乐观锁无法识别中间发生过的修改。其他修改任务的接口也需要一并检查。

## 14. 实验检查清单

- 请求与模型的版本类型一致，使用 int64。
- UPDATE 包含版本条件，并成功后递增版本。
- RETURNING 与 Scan 的字段顺序一致。
- 版本冲突使用 %w 包装，HTTP 层返回 409。
- 更新任务和写事件处于同一个事务。
- 事务提交成功后才返回新版本。
- 两个旧版本请求竞争时，只有一个成功。
- 所有修改任务的入口都维护版本号。
- 是否自动重试，由业务语义决定。

## 参考文档

- [PostgreSQL 事务隔离](https://www.postgresql.org/docs/16/transaction-iso.html)
- [PostgreSQL 完整事务重试原则](https://www.postgresql.org/docs/18/mvcc-serialization-failure-handling.html)
