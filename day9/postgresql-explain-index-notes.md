# PostgreSQL 执行计划、索引与缓存笔记

本文结合 tasks、task_events 的实际查询，记录 `EXPLAIN (ANALYZE, BUFFERS)` 的阅读方法，以及索引扫描、回表、排序和缓存的关系。

## 1. EXPLAIN (ANALYZE, BUFFERS) 的作用

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, title, status, created_at
FROM tasks
WHERE status = 'pending'
ORDER BY created_at DESC
LIMIT 20 OFFSET 0;
```

| 部分 | 作用 |
| --- | --- |
| EXPLAIN | 显示优化器选择的执行计划，包括节点、预估成本和预估行数 |
| ANALYZE | 实际执行 SQL，测量节点耗时、输出行数和执行次数 |
| BUFFERS | 显示共享缓冲区等数据页的访问情况 |

只有 `EXPLAIN` 时，不实际执行查询；增加 `ANALYZE` 后，会真正执行。对 UPDATE、DELETE、INSERT 使用时，修改也会发生。

这里的 ANALYZE 选项与下面的独立命令不同：

```sql
ANALYZE tasks;
```

独立的 ANALYZE 更新优化器使用的统计信息，例如数据规模和字段值分布；它不只是“更新数据量”，也不负责像 VACUUM 那样清理可回收的旧版本。

## 2. 从一份执行计划入手

```text
Limit  (cost=0.14..8.16 rows=1 width=492)
       (actual time=0.876..0.878 rows=4 loops=1)
  Buffers: shared read=2
  -> Index Scan using idx_task_status_created_at on tasks
       (cost=0.14..8.16 rows=1 width=492)
       (actual time=0.874..0.876 rows=4 loops=1)
       Index Cond: ((status)::text = 'pending'::text)
       Buffers: shared read=2
Planning:
  Buffers: shared hit=66 read=6
Planning Time: 5.797 ms
Execution Time: 0.892 ms
```

按数据流从下往上理解：

```text
Index Scan：通过索引定位 pending 记录，按所需顺序读取并回表
    ↓
Limit：最多向上输出 20 行
```

实际执行采用按需拉取的方式：上层 Limit 拿够记录后，可以停止向下层请求数据，并非下层必须先读取全部匹配记录。

本次实际只有 4 条符合条件的可见记录，因此最终返回 4 行。LIMIT 20 表示最多返回 20 行，不保证一定有 20 行；OFFSET 0 表示不跳过记录。

### 2.1 预估信息：cost、rows、width

```text
cost=0.14..8.16 rows=1 width=492
```

| 字段 | 含义 |
| --- | --- |
| 0.14 | 预估启动成本，即开始输出首行前的成本 |
| 8.16 | 预估节点完整执行的总成本，已经包含启动成本 |
| rows=1 | 预估该节点每次执行输出 1 行 |
| width=492 | 预估输出行平均宽度为 492 字节 |

**cost 是优化器比较方案使用的成本单位，不是毫秒。** width 也是估算值，不是本次实际测量的行大小。

### 2.2 实际信息：actual time、rows、loops

```text
actual time=0.874..0.876 rows=4 loops=1
```

| 字段 | 含义 |
| --- | --- |
| 0.874 | 该节点开始执行到输出首行约耗时 0.874 ms |
| 0.876 | 该节点完成本次执行约耗时 0.876 ms |
| rows=4 | 实际输出 4 行 |
| loops=1 | 节点执行 1 次 |

loops 大于 1 时，节点实际时间和行数通常按每次执行的平均值展示。父节点的耗时包含调用子节点的时间，不能把整棵计划中各节点的耗时直接相加。

本次预估 1 行，实际 4 行，有偏差，但绝对数量很小，不能单凭这一点断定存在严重性能问题。

### 2.3 Index Cond 与 Filter

```text
Index Cond: ((status)::text = 'pending'::text)
```

说明 status 条件参与了索引查找。`::text` 是类型转换的显示形式，本例中没有阻止索引使用。

如果计划出现 `Filter`，通常表示扫描得到候选记录后还需要执行额外过滤；应结合 `Rows Removed by Filter` 判断丢弃了多少记录。

## 3. BUFFERS：数据页访问与缓存

| 指标 | 含义 |
| --- | --- |
| shared hit | 所需页已经在 PostgreSQL shared buffers 中，直接命中 |
| shared read | 所需页需要读入 PostgreSQL shared buffers |

注意：

- 这些数字表示数据页访问，不是记录条数，也不保证是不同页面的数量。
- 默认页大小通常为 8 KiB；read=2 约对应 16 KiB 的页读取量。
- shared read 可能由操作系统文件缓存提供数据，不等于一定发生物理磁盘读取。
- 父节点包含子节点的缓冲区统计，不能重复累加。
- 执行阶段访问的页可能来自索引和表；不能仅凭 read=2 就确定两页分别是什么。

原计划中 Limit 和 Index Scan 都显示 read=2，执行阶段合计仍是 2，不是 4。

## 4. 规划阶段与执行阶段

```text
Planning:
  Buffers: shared hit=66 read=6
Planning Time: 5.797 ms
Execution Time: 0.892 ms
```

**Planning Time 是实际测量的规划耗时，不是预估执行时间。** 规划阶段需要根据表、索引、统计信息等生成并选择计划，可能访问系统目录及其数据页，因此也会产生缓冲区访问。

Execution Time 是执行器阶段的实际耗时。它不等于应用端完整请求耗时，不包含应用获取连接、网络往返等所有开销；EXPLAIN ANALYZE 自身的测量也有开销。

### 为什么相同 SQL 多跑几次会更快？

重复执行后，相关数据页、索引页、系统目录页以及后端内部的部分元数据可能已经缓存，因此规划或执行阶段都可能变快。

需要区分：

- **页缓存命中**：减少重复读取页面的工作。
- **执行计划复用**：是否复用取决于预备语句、客户端协议及其他设置，不能仅凭 SQL 相同就认定计划被复用。

因此，“充分预热后 Planning Time 明显下降”是合理观察，但不能把全部变化都归因于 shared buffers，更不能认定第二次就跳过了规划。

## 5. 索引与回表

假设索引键只有 `(status, created_at)`，查询却需要 id、title 等字段：

```sql
SELECT id, title, status, created_at
FROM tasks
WHERE status = 'pending'
ORDER BY created_at DESC
LIMIT 20;
```

普通 Index Scan 会根据索引中的行位置访问表记录，也就是“回表”。回表已包含在 Index Scan 节点中，不需要额外显示一个回表节点。

PostgreSQL 的普通二级索引不会自动附带主键 id 的值。

覆盖索引示例：

```sql
-- 用于理解，不必仅为消除少量回表就直接创建。
CREATE INDEX idx_tasks_status_created_cover
ON tasks (status, created_at DESC)
INCLUDE (id, title);
```

INCLUDE 字段作为附带数据存储，不作为索引查找和排序的键。上述索引可以覆盖所列四个字段，但不一定覆盖 SELECT *。

覆盖查询只是 Index Only Scan 的前提之一。PostgreSQL 还要确认事务可见性；必要时仍会访问表。Index Only Scan 中的 `Heap Fetches: 0` 表示本次没有为了检查记录可见性而读取表元组。

## 6. 批量构造测试数据

```sql
INSERT INTO tasks (title, description, status)
SELECT
    'benchmark-' || g,
    '',
    CASE
        WHEN g % 3 = 0 THEN 'pending'
        WHEN g % 3 = 1 THEN 'in_progress'
        ELSE 'completed'
    END
FROM generate_series(1, 20000) AS g;

ANALYZE tasks;
```

- generate_series 生成 1 到 20000，包含两端。
- `||` 拼接标题，`%` 取余，CASE 根据余数分配状态。
- 新增 pending 6666 条，in_progress 6667 条，completed 6667 条。
- 空字符串 `''` 不等于 NULL。
- 未列出的字段按表默认值和约束处理。
- 这是追加操作；重复执行会再次尝试插入，存在唯一约束时可能失败。

如果 created_at 使用 DEFAULT now()，同一事务中生成的这些记录可能具有相同时间。需要稳定分页顺序时，可使用 `ORDER BY created_at DESC, id DESC`，并考虑与之匹配的索引；仅有时间字段不能保证同一时间内的行顺序固定。

## 7. 实例一：查询全部 pending 任务

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM tasks WHERE status = 'pending';
```

核心结果：

```text
Bitmap Heap Scan: 预估 6670 行，实际 6670 行
  Heap Blocks: exact=225
  Buffers: shared hit=233
  -> Bitmap Index Scan: 实际 6670 个候选位置
       Buffers: shared hit=8
Execution Time: 1.260 ms
```

执行过程：

```text
通过索引收集符合条件的记录位置
    ↓
形成位图
    ↓
按表的物理数据页顺序集中回表
```

要取的记录较多，优化器认为按页集中回表比沿索引逐条分散读取更划算。Bitmap Index Scan 使用的仍是已有索引，不代表创建了另一种持久化“位图索引”。

`exact=225` 表示访问了 225 个表页，位图保留了精确记录位置；不代表返回了 225 行。该计划中 8 次索引页命中加 225 次表页命中，共 233 次命中。

`Recheck Cond` 的出现不代表索引失效或一定发生了大量误匹配。位图还可能有仅保留页级信息的 lossy 块；本例没有显示 lossy 块。

6666 条新增 pending 加原有 4 条，共 6670 条，与结果吻合。预估行数也等于实际行数，说明本次估算准确。

## 8. 实例二：查询最新 20 条 pending 任务

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM tasks
WHERE status = 'pending'
ORDER BY created_at DESC
LIMIT 20;
```

核心结果：

```text
Limit: 实际 20 行
  -> Index Scan: 预估完整输出 6670 行，实际只输出 20 行
Buffers: shared hit=4
Execution Time: 0.042 ms
```

没有 Sort，说明索引扫描已提供需要的顺序。Limit 拿够 20 行就停止，避免读取全部 6670 条。

因此第二条比第一条访问的页面少、执行更快。但两条 SQL 返回数量不同，不能据此认定 Index Scan 永远优于位图扫描。

## 9. 实例三：查询任务事件

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT * FROM task_events
WHERE task_id = 10
ORDER BY created_at DESC;
```

核心结果：

```text
Sort: 实际 0 行，quicksort，Memory: 25kB
  -> Bitmap Heap Scan: 实际 0 行
       Heap Blocks: exact=1
       -> Bitmap Index Scan: 实际 4 个候选位置
Execution Time: 0.030 ms
```

### 9.1 为什么有索引仍然排序？

位图回表按物理页顺序访问，会丢失索引原本的排序顺序，因此需要单独 Sort。

优化器预估只有 2 行，认为少量结果排序的成本可以接受。是否有其他可提供排序的索引路径，需要结合索引实际定义确认，不能只看索引名称。

即使输出 0 行，排序节点也可能完成初始化并显示少量内存开销；25kB 不代表实际排序了很多记录。

### 9.2 为什么索引有 4 个候选位置，最终却没有记录？

索引候选位置不等于当前事务可见的记录。回表还要进行 MVCC 可见性判断。

一种可能是删除或更新后的旧索引项尚未清理，也可能涉及当前快照不可见的其他事务记录版本。仅凭计划不能确定具体原因，更不能直接断定索引损坏。

可以先核对：

```sql
SELECT count(*) FROM task_events WHERE task_id = 10;
```

如果事件表近期频繁修改，可以在事务块外执行维护后重新观察：

```sql
VACUUM (ANALYZE) task_events;
```

仍然活跃的事务等因素可能限制旧版本清理。`ANALYZE tasks` 只处理 tasks 的统计信息，不会更新 task_events 的统计信息，也不会清理其旧索引项。

## 10. 阅读计划时的检查顺序

1. 看节点：Seq Scan、Index Scan、Bitmap Heap Scan、Sort、Limit 等。
2. 沿数据流看：底层如何读取，上层如何过滤、排序和限制数量。
3. 对比预估 rows 和 actual rows，注意 loops。
4. 看是否回表、是否额外排序、是否能提前停止。
5. 看 Buffers，区分命中与读取，避免父子节点重复累加。
6. 分开看 Planning Time 与 Execution Time。
7. 用有代表性的数据量和缓存条件比较，避免只凭一次耗时下结论。

## 参考文档

- [PostgreSQL：Using EXPLAIN](https://www.postgresql.org/docs/current/using-explain.html)
- [PostgreSQL：EXPLAIN 参数说明](https://www.postgresql.org/docs/current/sql-explain.html)
- [PostgreSQL：位图扫描与索引组合](https://www.postgresql.org/docs/17/indexes-bitmap-scans.html)
- [PostgreSQL：Index-Only Scans and Covering Indexes](https://www.postgresql.org/docs/18/indexes-index-only-scans.html)
