# CloudTask Go

CloudTask 是一个用 Go、Gin、PostgreSQL 和 Redis 实现的任务管理 API。创建一个任务后，可以按 ID 查询、按状态分页，或带着上次读到的版本修改状态。项目用同一套任务数据演示三个问题：并发写入如何检测冲突，状态变更如何留下事件，缓存失效时怎样保护数据库。

[综合学习笔记](docs/notes/cloudtask-handbook.md) · [接口示例](docs/api.md) · [架构与事务流程图](docs/architecture.md)

## 在本地启动

以下 PowerShell 命令在仓库根目录 `cloudtask-go` 执行。容器方式需要 Docker 和支持 `up --wait` 的 Compose v2，默认占用本机回环地址的 5432、6379 和 8080 端口。只有在宿主机上编译、测试或运行 Go 服务时，才需要安装与 [go.mod](go.mod) 匹配的 Go 工具链（模块声明为 1.26.0）。

### 1. 准备容器环境

```powershell
if (!(Test-Path .env.docker)) { Copy-Item .env.docker.example .env.docker }
```

打开 `.env.docker`，替换占位密码。让 `POSTGRES_PASSWORD` 与 `DATABASE_URL` 中的密码一致，容器内地址保留为 `db:5432` 和 `redis:6379`。已有数据卷时，使用库中现有的角色密码。下面的 `--env-file` 为 Compose 展开变量，配置中的 `api.env_file` 则把变量传入 API 容器。[容器配置模板](.env.docker.example) · [Compose 配置](docker-compose.yml)

```powershell
docker compose --env-file .env.docker config --quiet
docker compose --env-file .env.docker up -d --wait db redis
docker compose --env-file .env.docker exec db psql -U app -d cloudtask -c '\dt'
```

### 2. 首次为空库建表

如果上一步显示没有关系表，在**新建的空库**执行一次基线迁移。它会在一个事务中创建 `tasks`、`task_events`、约束和索引。当前 Compose 只把命名卷挂到 PostgreSQL 的数据目录，启动服务不会自动执行仓库里的 SQL。[基线迁移](db/migrations/000001_init.up.sql)

```powershell
Get-Content -Raw -Encoding UTF8 .\db\migrations\000001_init.up.sql | docker compose --env-file .env.docker exec -T db psql -v ON_ERROR_STOP=1 -U app -d cloudtask
```

已有业务表时跳过这条命令。可以用 `\d tasks` 和 `\d task_events` 对照基线检查列、约束和索引。如果重构后意外看到空库，先检查 Compose 项目名和挂载的卷；目录改名后，默认卷名可能随项目名改变，旧数据可能仍在原卷里。

### 3. 启动 API 并验证

```powershell
docker compose --env-file .env.docker up -d --build api
docker compose --env-file .env.docker ps
$api = 'http://127.0.0.1:8080'
Invoke-RestMethod "$api/healthz"
Invoke-RestMethod "$api/readyz"
Invoke-RestMethod "$api/tasks?status=pending&limit=5&offset=0"
```

`/healthz` 返回 `{"status":"ok"}` 表示 HTTP 服务能响应。`/readyz` 会 Ping PostgreSQL，成功返回 `{"status":"ready"}`，失败返回 503。最后一个请求验证应用能查询任务表；空库返回空数组。如果刚启动就连接失败，可以先看 `docker compose --env-file .env.docker logs --tail 60 api db redis`。

**在宿主机调试 Go 服务**时，只启动上面的 `db` 和 `redis` 并完成建表，把 `.env.example` 复制为 `.env`（已有文件可直接使用）。填写与容器相同的数据库和 Redis 凭据，主机改用 `localhost:5432` 和 `127.0.0.1:6379`。如果容器版 API 已占用 8080，先执行 `docker compose --env-file .env.docker stop api`。随后在根目录运行 `go run ./cmd/server`，按 Ctrl+C 退出。[宿主机配置模板](.env.example)

## 从创建任务到观察版本冲突

在同一个 PowerShell 终端运行。使用创建响应里的 ID 和版本，这个实验可以在已有数据的开发库里重复做。

```powershell
$api = 'http://127.0.0.1:8080'
$body = @{ title = 'Version check'; description = 'Try one stale update' } | ConvertTo-Json
$task = Invoke-RestMethod -Method Post -Uri "$api/tasks" -ContentType 'application/json' -Body $body
$task | Select-Object id, status, version

$change = @{ status = 'completed'; expected_version = $task.version } | ConvertTo-Json
$uri = "$api/tasks/opm/$($task.id)"
$updated = Invoke-RestMethod -Method Patch -Uri $uri -ContentType 'application/json' -Body $change
$updated.data | Select-Object id, status, version
```

新任务应从 `pending / 1` 变为 `completed / 2`。用同一份 `$change` 再发一次，会得到 **409 Conflict**。重新 GET 可以看到胜出的版本。在数据库中还可以核对这次变更的旧状态和新状态。

```powershell
try {
    Invoke-RestMethod -Method Patch -Uri $uri -ContentType 'application/json' -Body $change
} catch {
    [int]$_.Exception.Response.StatusCode
}
Invoke-RestMethod "$api/tasks/$($task.id)"
docker compose --env-file .env.docker exec db psql -U app -d cloudtask -c "SELECT event_type, details FROM task_events WHERE task_id = $($task.id) ORDER BY id"
```

| 入口 | 当前行为 |
| --- | --- |
| `POST /tasks`、`GET /tasks/:id`、`DELETE /tasks/:id` | 创建、查详情、删除；删除任务时数据库级联删除关联事件 |
| `GET /tasks` | 默认 `pending`，按 ID 倒序；`limit` 默认 10、范围 1–100，`offset` 从 0 开始 |
| `PUT /tasks/:id` | 更新标题和状态，递增版本，并写入 `task_updated` 事件 |
| `PATCH /tasks/:id` | 加行锁后修改状态；请求体要求正数 `expected_version`，此入口只校验其格式 |
| `PATCH /tasks/opm/:id` | 按客户端版本条件更新；成功返回 `data.version`，版本冲突返回 409 |

支持的状态为 `pending`、`in_progress` 和 `completed`。当前 HTTP 路由没有鉴权，事件通过 SQL 查询。字段与错误映射可对照 [请求结构](internal/requestbody/task_request.go) 和 [Handler](internal/handler/task_handler.go)。

## 构建与测试

```powershell
go build ./cmd/server
go test ./...
docker build -t cloudtask-api:refactor .
```

在 Windows 上，第一条命令默认在根目录生成 `server.exe`。第二条运行默认测试。缓存和仓储包的测试文件带有 `//go:build integration`，因此默认输出中的 `[no test files]` 是预期结果。第三条构建 Linux 镜像，服务启动和数据库访问可以用上面的 HTTP 请求另行验证。

要运行集成测试，准备独立的 PostgreSQL 测试库 **`cloudtask_test`**，在该空库执行同一份基线迁移，并准备专用的 Redis 测试实例。在当前终端设置 `TEST_DATABASE_URL` 和 `TEST_REDIS_ADDR`；如 Redis 启用了认证，还需设置 `TEST_REDIS_USERNAME`、`TEST_REDIS_PASSWORD`。测试会写入任务、事件和缓存键。

```powershell
go test -v -count=1 -tags=integration ./internal/repository ./internal/cache ./internal/service
```

面向 `main` 的 PR 会运行格式检查、`go vet`、编译、默认测试、竞态检测、带 PostgreSQL/Redis 的集成测试，以及镜像构建和冒烟检查。检查通过后，推送到 `main` 或推送 `v*` 标签才进入 GHCR 发布步骤。需要使用已发布镜像或切换版本时，见综合笔记的“交付与回滚”。[工作流](.github/workflows/go-ci.yml) · [部署覆盖文件](docker-compose.deploy.yml)

## 源码导航

| 路径 | 从这里看什么 |
| --- | --- |
| [cmd/server](cmd/server/main.go) | 加载配置、组装依赖、构建过滤器、监听 HTTP 与优雅退出 |
| [internal/handler](internal/handler/router.go) → [service](internal/service/task_service.go) → [repository](internal/repository/task_repository.go) | 从路由跟到参数校验、缓存编排、SQL 和事务 |
| [internal/cache](internal/cache)、[internal/database](internal/database)、[internal/config](internal/config) | Redis 读写与布隆过滤器、pgx 连接池、配置加载及校验 |
| [db/migrations](db/migrations) | 数据库基线及对应的回退 SQL |
| [examples](examples) | 缓存和并发客户端的学习示例。乐观锁样例有固定 ID 和旧 JSON 字段，复现实验可直接使用上面的 PowerShell 请求 |
| [docs/notes](docs/notes) | 综合笔记和各阶段的实验记录。早期按天组织的练习代码可在 `archive/learning-history` 分支查看 |

结束实验时，执行 `docker compose --env-file .env.docker stop` 可以停止服务并保留数据库卷。
