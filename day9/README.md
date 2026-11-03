# CloudTask：任务管理 API

基于 Go、Gin、pgxpool 和 PostgreSQL 的学习项目，练习 HTTP 分层、SQL、事务、事件记录、连接池及并发更新。

## 文档导航

- [接口文档](docs/api.md)
- [架构图与事务流程](docs/architecture.md)
- [事务笔记](transaction-notes.md)
- [连接池与并发笔记](pool-bingfa.md)
- [执行计划与索引笔记](postgresql-explain-index-notes.md)
- [乐观锁与冲突处理笔记](optimistic-lock-concurrency-notes.md)
- [并发、连接、索引及 Mutex 笔记](concurrency-connections-index-mutex-notes.md)

## 已有功能与当前边界

- 创建、查询、列表分页、更新和删除任务。
- 更新任务和写入事件在同一事务中完成。
- 悲观锁状态更新：SELECT ... FOR UPDATE。
- 专用乐观锁接口：按 expected_version 条件更新，冲突返回 409，成功返回新版本。
- PostgreSQL 初始化脚本、任务与事件索引。
- 并发请求和连接池统计演示。

当前没有鉴权、事件查询 HTTP 接口、自动数据库迁移或完整业务集成测试。普通 PUT 未检查客户端版本；普通 PATCH 虽校验版本参数存在，但未比较它。正式验证乐观锁时使用 `/tasks/opm/:id`。

## 1. 环境准备

需要：

- Go：按模块 go.mod 使用 Go 1.25.7，或可兼容该模块的工具链。
- Docker Engine / Docker Desktop，包含 Docker Compose v2。
- 本机端口 5432、8080 可用。
- 本文命令使用 PowerShell；执行目录统一为模块根目录 `cloudtask-go`。

```powershell
go version
docker version
docker compose version
go mod download
```

若当前位于项目的父目录，先执行 `cd .\cloudtask-go`。

## 2. 数据库首次初始化

```powershell
docker compose -f day9/docker-compose.yml up -d db
docker compose -f day9/docker-compose.yml ps
docker compose -f day9/docker-compose.yml exec db pg_isready -U app -d cloudtask
```

若数据库尚未就绪，稍后再次执行 pg_isready，直到显示 accepting connections。启动异常时查看：

```powershell
docker compose -f day9/docker-compose.yml logs --tail 100 db
```

默认配置：

| 项目 | 值 |
| --- | --- |
| 镜像 | postgres:17 |
| 数据库 | cloudtask |
| 用户 | app |
| 本地开发密码 | dev_password_only_local |
| 主机端口 | localhost:5432 |
| 持久化 | Compose 命名卷 postgres_data，实际卷名可能带项目名前缀 |
| 初始化 SQL | day9/db/init/001_schema.sql |

Compose 将 `db/init` 挂载到容器 `/docker-entrypoint-initdb.d`，PostgreSQL 官方镜像只在数据目录为空的首次初始化时执行这里的脚本。

脚本创建 tasks、task_events、外键、CHECK 约束以及两个联合索引。tasks.version 为 BIGINT，默认 1；删除任务会级联删除关联事件。

检查实际表结构：

```powershell
docker compose -f day9/docker-compose.yml exec db psql -U app -d cloudtask -c '\d tasks'
docker compose -f day9/docker-compose.yml exec db psql -U app -d cloudtask -c '\d task_events'
```

## 3. 已有数据卷的处理

修改初始化 SQL、重启容器或重新 up，不会自动更新已有数据库；CREATE TABLE IF NOT EXISTS 也不会为已有表补列。

如果旧版 tasks 只是缺少 version，可以先备份，再在 psql 中针对性补列：

```powershell
docker compose -f day9/docker-compose.yml exec db psql -U app -d cloudtask
```

```sql
ALTER TABLE tasks
ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 1;

\d tasks
\q
```

这条 SQL 只补充缺失的版本列，不是完整迁移工具；其他字段、约束或索引差异需要逐项核对。不要为了补列删除已有数据卷。

## 4. 连接配置与启动

当前服务在 `cmd/server/main.go` 中直接设置连接串：

```text
postgres://app:dev_password_only_local@localhost:5432/cloudtask?sslmode=disable
```

目前没有从 .env 或 DATABASE_URL 读取服务连接配置。Compose 的 POSTGRES_PASSWORD 可以影响首次数据库初始化密码，但不会同步改写 Go 连接串；已有数据库也不会仅因该环境变量变化而修改密码。

默认配置适用于本地学习。如果调整数据库地址、端口或凭据，需要同步修改服务连接串；连接池演示 `internal/config/config.go` 也有独立连接串。

启动 API：

```powershell
go run ./day9/cmd/server
```

看到数据库连接成功和 `Starting server on :8080` 后，在另一终端验证：

```powershell
Invoke-RestMethod http://localhost:8080/tasks
```

没有 pending 任务时返回空数组。`go run ./day9` 不是当前 HTTP 服务入口，服务入口位于 cmd/server。

## 5. 从创建到版本冲突的完整演示

以下变量在同一个 PowerShell 终端中依次使用：

```powershell
$apiBase = 'http://localhost:8080'
$createBody = @{
    title = '版本保护实验'
    description = '验证任务更新与版本冲突'
} | ConvertTo-Json

$task = Invoke-RestMethod -Method Post -Uri "$apiBase/tasks" `
    -ContentType 'application/json; charset=utf-8' `
    -Body ([System.Text.Encoding]::UTF8.GetBytes($createBody))

$taskId = $task.id
$originalVersion = $task.version

Invoke-RestMethod "$apiBase/tasks/$taskId"

$updateBody = @{
    status = 'completed'
    expected_version = $originalVersion
} | ConvertTo-Json

$updated = Invoke-RestMethod -Method Patch -Uri "$apiBase/tasks/opm/$taskId" `
    -ContentType 'application/json' -Body $updateBody

$updated.data
```

新任务默认版本为 1，上述更新成功后版本为 2。继续提交同一份旧版本请求，应得到 HTTP 409：

```powershell
try {
    Invoke-RestMethod -Method Patch -Uri "$apiBase/tasks/opm/$taskId" `
        -ContentType 'application/json' -Body $updateBody
} catch {
    if ($null -ne $_.Exception.Response) {
        Write-Host 'HTTP 状态码：' ([int]$_.Exception.Response.StatusCode)
    }
    Write-Host $_.ErrorDetails.Message
}
```

缺少 expected_version、值为 0 或负数会因参数校验返回 400。冲突后应重新读取最新任务，再根据业务决定是否重提，不要盲目改版本覆盖别人。

## 6. 并发与连接池实验

并发请求示例入口：

```powershell
go run ./day9/cmd/optimistic-demo
```

**当前示例需要先调整后再运行：** main.go 中固定了 taskID，且请求结构体标签仍为 `json:"version"`。需换成新建测试任务的 ID，并将该标签改为 `json:"expected_version"`。Go 字段名 Version 可以保留，只改 JSON 标签即可。

确认两个请求都使用实际版本 1，预期一个 200、一个 409；最终版本为 2，新增一条 status_changed 事件。如果收到两个 400，优先检查客户端字段名；两个 409 则检查任务是否已经不是版本 1。

连接池统计示例：

```powershell
go run ./day9/internal/config
```

该示例独立连接数据库，执行并发 pg_sleep 查询并打印统计，不会启动 HTTP 服务。

## 7. 查看事件与执行计划

```powershell
docker compose -f day9/docker-compose.yml exec db psql -U app -d cloudtask
```

```sql
-- 将 10 替换成实验任务 ID。
SELECT id, status, version FROM tasks WHERE id = 10;
SELECT * FROM task_events WHERE task_id = 10 ORDER BY created_at DESC;

EXPLAIN (ANALYZE, BUFFERS)
SELECT id, title, status, created_at
FROM tasks
WHERE status = 'pending'
ORDER BY created_at DESC
LIMIT 20;
```

任务列表 HTTP 接口目前按 id DESC 排序；这里的 created_at 排序是数据库分析实验，不代表列表接口的实际 SQL。

## 8. 检查与测试

```powershell
go test ./day9/...
docker compose -f day9/docker-compose.yml config --quiet
```

当前 Repository、Service 的 `_test.go` 文件只有包声明或注释，没有实际测试用例；Handler 的 `task.test.go` 也不符合 Go 的 `_test.go` 测试文件命名。go test 成功主要表示包可编译，不表示乐观锁、回滚和 HTTP 行为已自动验证。

实际业务验证使用上述请求和数据库核对步骤；并发示例是人工观察工具，没有自动断言。

## 9. 停止服务与常见问题

API 终端按 Ctrl+C 停止。停止数据库但保留数据：

```powershell
docker compose -f day9/docker-compose.yml stop db
```

| 现象 | 优先检查 |
| --- | --- |
| 数据库连接被拒绝 | 容器、pg_isready、5432 端口及服务连接串 |
| 密码认证失败 | 已有数据卷中的真实密码与 Go 连接串是否一致 |
| version 列不存在 | 使用了旧数据卷，参照已有数据库补列步骤 |
| 8080 端口占用 | 是否已启动另一个服务，或调整 main.go 监听端口 |
| 乐观锁更新返回 400 | JSON、expected_version 字段、正整数版本及状态参数 |
| 乐观锁更新返回 409 | 版本已过期，需要重新查询任务 |
| PUT 响应版本为 0 | 当前 PUT 未扫描 version；重新 GET 获取真实版本 |

## 目录

```text
day9/
├── cmd/server/              HTTP 服务入口与依赖组装
├── cmd/optimistic-demo/     并发 HTTP 客户端实验
├── db/init/                首次初始化 SQL
├── docs/                   接口与架构文档
├── internal/handler/       路由、参数解析、响应和错误映射
├── internal/service/       业务校验
├── internal/repository/    SQL 与事务
├── internal/database/      连接池创建与 Ping
├── internal/model/         数据模型
├── internal/requestbody/   请求结构与校验标签
├── internal/config/        独立连接池实验入口
└── docker-compose.yml      本地 PostgreSQL 服务
```
