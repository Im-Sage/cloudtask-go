# 引入 CI/CD：配置管理与问题笔记

记录日期：2026-11-12。

后续补充：2026-11-13，第 14～23 节记录迁移管理、备份恢复、日志、健康检查和优雅退出。前 13 节保留当时的排查过程；当前实现进度以第 23 节为准。

本次主要完成 CI/CD 前的配置管理准备：将连接地址、连接池参数、超时等从启动代码中提取，通过环境变量加载到 Config，再传给各组件。本文记录本次改动和排查过程，不表示 Docker 镜像构建、CI 流水线或自动部署已经完成。

## 1. Config 结构与职责

项目使用一个总 Config，按组件分组：

```go
type Config struct {
    Http           HttConfig
    Postgres       PostgresConfig
    Redis          RedisConfig
    StartupTimeout time.Duration
}
```

当前代码中的命名是 `Http`、`HttConfig`；后续可以统一为 Go 中常见的 `HTTP`、`HTTPConfig`，但这不是本次加载逻辑完善的必要条件。

| 分组 | 保存的参数 |
| --- | --- |
| Http | 监听地址、读取请求头超时、写响应超时、空闲连接超时 |
| Postgres | DSN、最大/最小连接数、Ping 超时 |
| Redis | 地址、用户名、密码、数据库编号、Ping 超时 |
| StartupTimeout | 整个启动阶段的时间预算 |

Config 保存参数；连接池、Redis 客户端、Context 等运行时对象在初始化阶段创建。`main` 负责加载配置和组装依赖，各层只接收需要的配置或依赖。

## 2. Load 的工作流程与本次完善

```text
可选地加载 .env 到进程环境变量
    → 读取环境变量并使用默认值
    → 将字符串转换为整数或时间
    → 组装 Config
    → Validate 校验
    → 返回 Config 或 error
```

本次补齐了 HTTP 写入/空闲超时、PostgreSQL 最小连接数与 Ping 超时、Redis Ping 超时等字段，并统一了时间解析错误提示。Redis DB 的默认值由 1 调整为 0，避免省略配置时意外切换数据库。

当前 `Load()` 的读取约定如下。默认值是代码中的缺省值，可能与 `.env.example` 中的示例值不同。

| 环境变量 | Config 字段 | 未设置或值为空时 |
| --- | --- | --- |
| HTTP_ADDR | Http.Addr | `:8080` |
| HTTP_READ_HEADER_TIMEOUT | Http.ReadHeaderTimeout | `5s` |
| HTTP_WRITE_TIMEOUT | Http.WriteTimeout | `10s` |
| HTTP_IDLE_TIMEOUT | Http.IdleTimeout | `60s` |
| DATABASE_URL | Postgres.DSN | 必填，无默认值 |
| PG_MAX_CONNS | Postgres.MaxConns | `4` |
| PG_MIN_CONNS | Postgres.MinConns | `0` |
| PG_PING_TIMEOUT | Postgres.PingTimeout | `5s` |
| REDIS_ADDR | Redis.Addr | `127.0.0.1:6379` |
| REDIS_USERNAME | Redis.Username | `default` |
| REDIS_PASSWORD | Redis.Password | 空字符串，当前 Validate 未强制校验 |
| REDIS_DB | Redis.DB | `0` |
| REDIS_PING_TIMEOUT | Redis.PingTimeout | `3s` |
| STARTUP_TIMEOUT | StartupTimeout | `15s` |

注意：变量名由项目自己约定，拼写必须与读取代码一致。曾将 `REDIS_ADDR` 写成 `REIDS_ADDR`，会导致正确的变量名读不到值。

## 3. 字符串如何转成配置字段

### 3.1 os.Getenv 只有一个返回值

```go
raw := os.Getenv("PG_MAX_CONNS")
```

返回值始终是字符串，不返回 error。若需要区分“变量未设置”和“变量已设置但为空”，使用 `os.LookupEnv`。当前默认值逻辑把这两种情况都当作缺省。

### 3.2 MaxConns、MinConns 使用 int32

```go
n, err := strconv.ParseInt(raw, 10, 32)
if err != nil {
    return Config{}, fmt.Errorf("PG_MAX_CONNS 必须是 int32 范围内的整数: %w", err)
}
postgresStru.MaxConns = int32(n)
```

`10` 表示十进制，`32` 表示检查 int32 的取值范围；ParseInt 的返回类型仍然是 int64，所以赋值时还需要转换。不能忽略解析错误后直接强制转换。

### 3.3 Redis DB 使用 int

```go
db, err := strconv.Atoi(raw)
if err != nil {
    return Config{}, fmt.Errorf("REDIS_DB 必须是整数: %w", err)
}
redisStru.DB = db
```

转换解决的是“格式是否合法”；值是否合理还需要校验。例如 `-1` 能转成整数，但不能作为这里允许的 Redis DB 配置。

### 3.4 超时使用 time.Duration

```go
startupTimeout, err := time.ParseDuration("15s")
if err != nil {
    return Config{}, fmt.Errorf("STARTUP_TIMEOUT 格式错误: %w", err)
}
cfg.StartupTimeout = startupTimeout
```

支持 `500ms`、`15s`、`1m30s` 等格式。普通数字 `15` 没有单位，会解析失败。当前通过 `durationFromEnv` 统一读取、设置默认值和包装错误。

## 4. 参数校验放在哪里

配置校验放在 `config` 包，由 `Load()` 在返回前调用 `cfg.Validate()`。

当前已有的检查包括：

- DATABASE_URL 不能为空。
- PG_MAX_CONNS 必须大于 0。
- PG_MIN_CONNS 必须在 0 到 PG_MAX_CONNS 之间。
- REDIS_DB 不能为负数。
- StartupTimeout 必须大于 0。

类型解析与取值校验是两个步骤。配置合法也不等于依赖可用：密码是否正确、数据库或 Redis 是否能连接，要由初始化阶段的连接操作和 Ping 检查。

其他超时目前会检查时间格式，但 Validate 尚未逐一限制它们的正负值；后续应根据是否允许 0 表示禁用等语义补齐规则。

## 5. .env、环境变量与部署的关系

程序最终通过 `os.Getenv` 读取进程环境变量；`.env` 是本地开发时填充这些变量的辅助文件。

```go
if err := godotenv.Load("day9/.env"); err != nil {
    if !errors.Is(err, os.ErrNotExist) {
        return Config{}, fmt.Errorf("加载 .env 失败: %w", err)
    }
}
```

- 文件不存在：允许继续，因为部署环境可能直接提供了环境变量。
- 文件存在但解析或读取失败：返回错误，避免静默忽略配置问题。
- 必填配置最终仍为空：Validate 返回错误，不能因为允许缺文件就跳过必填检查。

`godotenv.Load` 默认不会覆盖已存在的环境变量，已有变量即使是空字符串也会影响文件加载。测试需要明确设置或清除相关变量。

部署时可以通过容器配置或 CI/CD 平台的环境变量、Secrets 提供实际值，不需要将真实 `.env` 打进镜像。Compose 使用 `.env` 做变量替换，也不意味着所有变量会自动注入应用容器；还需通过 `environment` 或 `env_file` 等显式传入。

## 6. 工作目录与相对路径：本次反复遇到的问题

工作目录是程序解析相对路径时的起点，不一定是 main.go 或 config.go 所在的目录。调用 config 包里的函数不会自动切换目录。

从模块根目录执行：

```powershell
# 当前目录：cloudtask-go
go run ./day9/cmd/server
```

程序工作目录仍然是 `cloudtask-go`，此时加载文件应使用 `day9/.env`。

但执行测试时：

```powershell
go test ./day9/internal/config -run '^TestEnvConfig$' -v -count=1
```

Go 默认在被测试包目录 `day9/internal/config` 运行测试。

| 程序工作目录 | 指向 day9/.env 的相对路径 |
| --- | --- |
| cloudtask-go | `day9/.env` |
| cloudtask-go/day9 | `.env` |
| cloudtask-go/day9/cmd/server | `../../.env` |
| cloudtask-go/day9/internal/config | `../../.env` |

可以用 `os.Getwd()` 查看工作目录，不要只看终端位置来判断测试里的路径。

`godotenv.Load` 接收文件路径，`t.Chdir` 接收目录路径。`godotenv.Load("../../..")` 指向目录，不能用来加载 .env。

### 当前文件状态与待修正事项

整理时，`Load()` 已改为读取 `day9/.env`，但 `prepareEnv` 仍把测试工作目录切换到临时的 `day9/internal/config`。这时实际查找的是：

```text
临时目录/day9/internal/config/day9/.env
```

临时文件实际位于 `临时目录/day9/.env`，两者不匹配。缺文件被允许忽略后，DATABASE_URL 仍为空。

本次重新运行配置包测试，结果为：

- TestEnvConfig：失败，错误为 `DATABASE_URL 不能为空`。
- TestLoadDefaults：通过。
- TestLoadInvalidValues：通过。

推荐保持服务从模块根目录启动、Load 使用 `day9/.env`，并让 `prepareEnv` 在构造临时目录后调用 `t.Chdir(root)`，使测试使用同样的目录约定。不要在测试辅助函数中直接调用 `t.Chdir("../../..")` 回到真实项目目录，否则会破坏临时文件隔离。本文仅记录这一问题和修正方式，未修改业务代码或测试代码。

## 7. 测试命令和“报错却 PASS”的原因

错误命令：

```powershell
go test -run /day9/internal/config/ '^TestEnvConfig$' -v -count=1
```

`-run` 后面应紧跟测试名称的正则表达式。上述命令把包路径当成测试匹配表达式，又把 `^TestEnvConfig$` 当成包路径，所以报 `malformed import path`。

正确命令：

```powershell
go test ./day9/internal/config -run '^TestEnvConfig$' -v -count=1
```

`./day9/internal/config` 是包路径；`-run` 选择测试；`-v` 显示详细信息；`-count=1` 避免使用测试结果缓存。

以下代码只输出日志，不会让测试失败：

```go
if err != nil {
    log.Printf("配置文件加载失败：%v", err)
    return
}
```

测试应使用：

```go
if err != nil {
    t.Fatalf("配置加载失败：%v", err)
}
```

本次已将原来的密码输出改成配置断言，增加完整配置、默认值和错误输入用例。测试使用临时 .env 和虚构凭据，不依赖真实密码。测试通过仅表示相应行为被验证，不代表数据库已连接成功。

## 8. fmt.Errorf、log.Fatalf 和错误包装

| 写法 | 行为 | 适用位置 |
| --- | --- | --- |
| fmt.Errorf | 创建并返回一个 error，本身不打印、不退出 | 配置、数据库等包向上报告错误 |
| log.Printf | 打印日志，继续执行 | 允许继续运行的错误或提示 |
| log.Fatalf | 打印日志，然后调用 os.Exit(1) | 程序入口决定结束进程 |
| t.Fatalf | 标记测试失败，停止当前测试 goroutine | 测试无法继续执行时 |

```go
return Config{}, fmt.Errorf("加载 .env 失败: %w", err)
```

这里返回的是新的外层错误，其中包装原始 err。`%w` 使调用方仍可通过 `errors.Is`、`errors.As` 检查错误链。

`errors.New` 只接收一个字符串，不支持格式化参数。将 `err.Error()` 拼接到字符串会保留显示文字，却失去原始错误关联。`fmt.Errorf` 中的 `%v` 也只格式化信息，不建立错误包装关系。

`log.Fatalf` 退出时不会执行已登记的 defer。若需要先关闭连接池等资源，可让主要逻辑放在返回 error 的 `run()` 中，由 run 返回时先执行 defer，再在 main 决定退出。

## 9. 配置如何真正传入 pgx 和 Redis

`PG_MAX_CONNS` 是自定义环境变量名，pgx 不会自动识别它。需要完成整条传递链：

```text
PG_MAX_CONNS=3
    → Load 读取并转成 int32
    → cfg.Postgres.MaxConns
    → poolConfig.MaxConns
    → pgxpool.NewWithConfig 创建连接池
```

数据库初始化函数接收结构体实例：

```go
pool, err := database.NewDBPool(ctx, cfg.Postgres)
```

`cfg.Postgres.DSN` 是 string，不符合当前函数参数类型；`config.PostgresConfig` 是类型名，也不是可直接传入的配置实例。

在 NewDBPool 内部解析和赋值：

```go
poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
if err != nil {
    return nil, fmt.Errorf("解析数据库配置失败: %w", err)
}
poolConfig.MaxConns = cfg.MaxConns
poolConfig.MinConns = cfg.MinConns
pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
```

整理时的实际 database.go 已设置 MaxConns，但尚未设置 MinConns。虽然 Load 已读取 PG_MIN_CONNS，仍需要补上对应赋值才能生效。

Redis 同理：将 cfg.Redis 的地址、用户名、密码、DB 传给 `redis.Options`，然后创建客户端。

## 10. 启动超时、Ping 超时与 HTTP 超时

`context.WithTimeout` 的参数顺序是：

```go
context.WithTimeout(父上下文, 超时时间)
```

启动阶段的父上下文：

```go
ctx, cancel := context.WithTimeout(context.Background(), cfg.StartupTimeout)
defer cancel()
```

PostgreSQL Ping 的子上下文：

```go
pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
defer cancel()
```

不能把 cfg.Postgres.PingTimeout 放在第一个参数位置，它是 Duration，不是 Context。

若启动总预算为 15s，Ping 自己的预算为 5s，Ping 的有效截止时间取父子上下文中更早者。父上下文只剩 2s 时，Ping 不能再等待完整 5s。

STARTUP_TIMEOUT 约束使用该 Context 的启动操作，不是每个 HTTP 请求的超时，也不会自动在 15 秒后关闭 HTTP 服务。Context 超时发出取消信号，操作需要使用它才能响应取消。

整理时，NewDBPool 内部已有 PingTimeout，而 main 外面还套了一层固定 5s 的 pgCtx。这样实际截止时间还受外层 5s 限制；建议由 NewDBPool 管理 Ping 子上下文，main 直接传入启动 ctx，避免重复限制。

HTTP 配置还需传给 http.Server 才会生效：

```go
server := &http.Server{
    Addr:              cfg.Http.Addr,
    Handler:           router,
    ReadHeaderTimeout: cfg.Http.ReadHeaderTimeout,
    WriteTimeout:      cfg.Http.WriteTimeout,
    IdleTimeout:       cfg.Http.IdleTimeout,
}
err := server.ListenAndServe()
```

当前 main 仍调用 `http.ListenAndServe(addr, router)`，因此三个 HTTP 超时字段虽然已经加载，还没有接入服务。这些字段分别限制请求头读取、响应写入、保持连接时的空闲等待，不等于统一的业务请求处理期限。

## 11. .env.example 的内容与用途

`.env.example` 是可以提交 Git 的配置模板，列出变量名、示例值和说明，凭据使用占位值。

```dotenv
DATABASE_URL=postgres://app:replace_me@localhost:5432/cloudtask?sslmode=disable
REDIS_PASSWORD=replace_me
```

在 day9 目录首次准备本地配置时：

```powershell
# 已有 .env 时不要重复复制覆盖
Copy-Item .env.example .env
```

再填写真实密码。Load 不会自动读取 .env.example。修改配置文件也不会改变已有 PostgreSQL 数据卷中的数据库密码，连接串必须与实际凭据一致。

## 12. .dockerignore 与 .gitignore 的区别

| 文件 | 控制范围 |
| --- | --- |
| .gitignore | Git 忽略哪些未跟踪文件 |
| .dockerignore | 镜像构建上下文中排除哪些文件 |

从 cloudtask-go 执行：

```powershell
docker build -t cloudtask .
```

末尾的 `.` 指定构建上下文。通常在该上下文根目录放置 .dockerignore；它与终端是否在 Dockerfile 所在目录是两回事。

适用于本项目的示例：

```dockerignore
.git
.vscode
.env
.env.*
!.env.example
*.log
*.test
coverage.out
server.exe
*.zip
```

`!` 表示例外，这里保留 .env.example。`.gitignore` 忽略 .env 并不意味着 Docker 也会忽略它，两者需要分别设置。排除构建上下文中的 .env，不会阻止 Compose 在宿主机读取 .env，也不会阻止容器通过环境变量接收配置。

本节为用途说明和建议示例，不代表已创建 Dockerfile 或 .dockerignore。

## 13. 下一步接入 CI/CD 前的检查

- 统一 Load 和测试的工作目录约定，先修复当前 TestEnvConfig 的文件路径不匹配。
- 将 PG_MIN_CONNS 和 HTTP 超时配置接入实际初始化代码。
- 删除或调整 main 中重复的 PostgreSQL 固定超时。
- 单元测试使用临时配置或显式环境变量，不读取开发机真实密码。
- 流水线中运行测试和构建时，不输出完整 Config、DSN 或密码；真实凭据由运行环境提供。
- Docker 构建排除真实 .env，运行时再注入配置。

本次笔记记录了当前配置包测试的实际结果；没有重新验证整个项目的数据库、Redis 集成测试或自动部署流程。

## 14. 第一版 PostgreSQL 迁移与初始化职责

当前迁移目录：

```text
day9/db/migrations/
├── 000001_init.up.sql
└── 000001_init.down.sql
```

`000001` 是版本号，`init` 是描述，up 和 down 是同一次结构变更的两个方向。

| 文件 | 作用 | 数据影响 |
| --- | --- | --- |
| 000001_init.up.sql | 创建 tasks、task_events、约束和索引 | 从零创建空表，不恢复历史业务数据 |
| 000001_init.down.sql | 先删除 task_events，再删除 tasks | 删除两张表及其数据 |

当前 up 已从旧初始化 Schema 整理而来，使用普通 CREATE TABLE / CREATE INDEX，并用 BEGIN、COMMIT 包裹。down 同样使用事务，并按外键依赖的反方向删除表。

重复执行 migrate up 不应重新建表，是因为工具记录并跳过已应用的版本，不是因为 CREATE TABLE IF NOT EXISTS。普通 CREATE 能帮助暴露“表已存在但未纳入迁移管理”等问题。

Compose 已移除 `./db/init:/docker-entrypoint-initdb.d:ro` 挂载，保留 postgres_data。此后职责为：PostgreSQL 镜像准备数据库和用户，migrate 管理业务 Schema，Go 服务使用已准备好的结构。旧初始化 SQL 留作历史参考，避免两套机制同时建业务表。

已经应用的迁移不要随意修改；新增结构变更使用 000002 等后续版本。

## 15. 独立临时迁移库的意义与验收

临时迁移库是专门验证迁移 SQL 的独立数据库，不能与保存开发数据的 cloudtask 混用。建议使用独立 PostgreSQL 容器，隔离端口和存储：

| 项目 | 开发库 | 临时迁移库示例 |
| --- | --- | --- |
| 数据库名 | cloudtask | cloudtask_migration_test |
| 宿主机端口 | 5432 | 15432 |
| 容器名 | 以 docker ps 为准 | cloudtask-migration-check |
| 存储 | 现有 postgres_data，需保留 | 临时存储，可丢弃 |
| 主要用途 | 日常开发 | 从零迁移、重复 up、down/up 测试 |

临时库不会因为创建 SQL 文件或安装 migrate 自动出现，需要单独创建。

下面是可重复采用的验收方式，不表示整理笔记时执行了这些操作。命令默认从 cloudtask-go 运行；临时凭据只用于这个独立测试容器：

```powershell
docker run --detach --rm --name cloudtask-migration-check `
  --publish 127.0.0.1:15432:5432 `
  --env POSTGRES_USER=migration_test `
  --env POSTGRES_PASSWORD=migration_test_only `
  --env POSTGRES_DB=cloudtask_migration_test `
  --tmpfs /var/lib/postgresql/data `
  postgres:17

docker exec cloudtask-migration-check `
  pg_isready -U migration_test -d cloudtask_migration_test
```

等待数据库就绪后：

```powershell
$MigrationTestUrl = 'postgres://migration_test:migration_test_only@localhost:15432/cloudtask_migration_test?sslmode=disable'
migrate -path ./day9/db/migrations -database "$MigrationTestUrl" up
migrate -path ./day9/db/migrations -database "$MigrationTestUrl" version
```

验收步骤：

1. 空库 up：确认两张业务表、所有约束和索引都存在，版本为 1，dirty 为 false。
2. 写入测试标记记录，再次 up：预期 no change，标记记录仍在。
3. 仅在临时库执行 down 1：确认两张业务表消失。
4. 再次 up：确认完整空表结构恢复；这不会恢复被 down 删除的记录。
5. 验收后可停止这个使用 --rm 的临时容器，现有开发数据卷不受影响。

不要为了测试第一版迁移执行删除开发数据卷的命令，也不要把开发库连接串传给上述回滚测试。

## 16. migrate 命令冲突与路径错误

### 16.1 调用了 Tomcat 的 migrate.bat

曾执行数据库迁移，却出现 TOMCAT、SERVLET、Jakarta 等帮助信息。原因是系统存在两个同名工具：

```text
Tomcat/bin/migrate.bat             Java/Jakarta 迁移工具
C:/Users/Lenovo/go/bin/migrate.exe golang-migrate
```

用 PowerShell 确认实际解析顺序：

```powershell
Get-Command migrate -All | Select-Object CommandType, Name, Source
```

Path 中应加入 `C:\Users\Lenovo\go\bin`，而不是 `C:\Users\Lenovo\go`；Path 不会递归搜索子目录。Go 编译器所在的安装目录也不一定是 go install 安装工具的目录。

```powershell
go env GOBIN GOPATH
$env:Path -split ';'
```

GOBIN 非空时使用该目录，否则使用 GOPATH 下的 bin。需要让 Go 工具目录在当前进程的实际 Path 中排在 Tomcat 前面。调整后重新启动终端；使用编辑器内置终端时，可能需要重启整个编辑器。系统和用户 Path 合并后的顺序也要检查。

不需要删除 Tomcat 工具。临时可用 migrate.exe 的完整路径执行；项目和 CI 应固定迁移工具版本与调用方式。

### 16.2 -path 与执行目录不匹配

在 `cloudtask-go/day9/db` 执行时：

```powershell
migrate -path ./migrations -database "$VerifiedDevUrl" version
```

在 `cloudtask-go` 执行时：

```powershell
migrate -path ./day9/db/migrations -database "$VerifiedDevUrl" version
```

曾在 day9/db 下仍传入 ./day9/db/migrations，导致 failed to open source。它表示找不到迁移文件，不能理解为数据库 SQL 已经执行失败。

连接串协议应写 `postgres://`，不要写成 `postgres\://`。migrate CLI 不会自动调用项目的 config.Load，需要显式传入连接串。

## 17. 现有开发库如何建立基线

已有 tasks 和 task_events 的数据库不能当作空库直接执行第一版 up。正确顺序：备份并验证可恢复 → 检查版本表 → 核对实际结构 → 确认一致后登记版本。

进入正确数据库后检查：

```sql
SELECT current_database(), current_user;
SELECT to_regclass('public.schema_migrations');
```

若版本表已经存在，先读取 version、dirty，调查既有状态；不要直接 force 覆盖。若不存在，再对照临时迁移库生成的结构：

```text
\d+ public.tasks
\d+ public.task_events
```

核对字段类型、NULL、默认值、Identity、主外键、级联删除、CHECK、version 字段和两个索引。两边业务记录数和序列当前值不必相同，但仅表名相同也不足以建立基线。存在差异时先制定补齐方案，不用 force 掩盖差异。

确认备份可用且结构等价后，针对已核验的开发库执行一次：

```powershell
# 当前目录为 cloudtask-go/day9/db
# VerifiedDevUrl 从本地安全配置提供，不把真实凭据写入笔记或提交
migrate -path ./migrations -database "$VerifiedDevUrl" force 1
migrate -path ./migrations -database "$VerifiedDevUrl" version
```

本次对话中，已反馈开发库不存在版本表、完成结构对比，并成功执行 force 1。该状态来自操作反馈，本次整理没有重新连接开发库核验。

force 1 只创建或更新版本记录，将版本设为 1、清除 dirty 标记，不执行 up/down，也不验证结构。因此原有数据没有丢失是正常的。

如果目录只有版本 1，再执行 up 应报告 no change。这只表示没有待应用的版本，不表示工具检查过表结构。将来新增版本 2，up 才会继续应用新的升级 SQL。

## 18. .dump 备份、迁移与数据恢复

| 操作或文件 | 作用 |
| --- | --- |
| up | 按迁移版本创建或修改结构 |
| down | 执行对应回滚 SQL，可能删除数据 |
| force | 只设置版本状态，不恢复结构或数据 |
| pg_dump 生成的 .dump | 保存备份时的数据库结构、数据和序列状态等 |
| pg_restore | 将自定义格式备份恢复到数据库 |

备份示例，在容器内生成文件后复制到宿主机，避免经过终端文本重定向：

```powershell
# DevDbContainer 替换为 docker ps 确认的开发库容器名
docker exec "$DevDbContainer" pg_dump `
  -U app -d cloudtask -Fc -f /tmp/cloudtask-before-baseline.dump

New-Item -ItemType Directory -Force ./backups
docker cp "${DevDbContainer}:/tmp/cloudtask-before-baseline.dump" `
  ./backups/cloudtask-before-baseline.dump
```

备份文件可能含业务数据，应记录保存位置和生成时间，不提交 Git。备份是指定数据库的快照，不等于整个 PostgreSQL 实例或所有角色的备份。

如果 down 删除了数据，重新 up 只会得到空表。建议先恢复到独立的新数据库，避免覆盖仍有价值的数据：

```powershell
docker cp ./backups/cloudtask-before-baseline.dump `
  "${DevDbContainer}:/tmp/cloudtask-before-baseline.dump"

# 该数据库名必须尚未存在；已有恢复库时先调查，不直接删除
docker exec "$DevDbContainer" createdb -U app cloudtask_restore_check

docker exec "$DevDbContainer" pg_restore `
  -U app -d cloudtask_restore_check `
  --exit-on-error --single-transaction `
  /tmp/cloudtask-before-baseline.dump
```

不要提前对空恢复库执行迁移，也不需要对这个空库使用 --clean。恢复后检查表结构、记录数、抽样记录和业务接口，再决定切换应用连接。保留原库供排查。

备份若早于 force 1，恢复库可能没有版本表；若备份时有版本表，则可能连同旧版本状态一起恢复。必须检查恢复后的结构和迁移状态，确认后再决定是否建立基线。

备份之后新增的数据不在这份 dump 中。恢复备份不能自动找回这部分数据；时间点恢复需要预先配置相应的备份和 WAL 归档机制。

## 19. 结构化日志、main/run 与返回错误

当前入口已创建 slog JSON logger，并把启动主体移入 run：

```go
func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout,
        &slog.HandlerOptions{Level: slog.LevelInfo}))
    if err := run(logger); err != nil {
        logger.Error("服务退出", "error", err)
        os.Exit(1)
    }
}
```

slog 统一时间、级别、消息和键值字段，便于容器收集和查询。INFO 级别默认过滤 DEBUG，保留 INFO、WARN、ERROR。logger.Error 只记录日志，不会像 log.Fatalf 那样结束程序。

run 中必要依赖失败时返回错误：

```go
pool, err := database.NewDBPool(ctx, cfg.Postgres)
if err != nil {
    return fmt.Errorf("创建数据库连接池失败: %w", err)
}
defer pool.Close()
```

main 是同步调用 run 并等待返回，并非持续监听一个 err 变量。函数返回前执行已经登记的 defer；main 收到错误后再非零退出。不要在 run 内部 Fatal，否则会绕过 defer。

| run 的状态 | main 的行为 |
| --- | --- |
| 尚未返回 | main 等待 |
| 返回 nil | main 正常结束，退出码 0 |
| 返回 error | 记录错误，os.Exit(1) |

配置和 PostgreSQL 初始化失败阻止启动；Redis、布隆过滤器失败按现有回源策略记录 WARN 后继续。避免同一错误在每层重复记录，也不输出完整配置、密码或含凭据的 DSN。

## 20. 存活检查、就绪检查与共享连接池

当前实际路由是 `/healthz` 和 `/readySys`；前文 `/readyz` 是建议名称，尚未统一。

| 接口 | 当前职责 | 数据库不可用时 |
| --- | --- | --- |
| /healthz | 确认 HTTP 服务可以响应 | 仍可返回 200 |
| /readySys | 在 1s 超时内 Ping PostgreSQL | 返回 503 not_ready |

早期 healthzSys 每次请求加载配置、创建新连接池、重复 Ping，而且未关闭新连接池。现已改为由入口创建 pool，经 NewRouter 传入 HealthHandler，检查时只借用：

```go
ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
defer cancel()
if err := h.pool.Ping(ctx); err != nil {
    c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not_ready"})
    return
}
c.JSON(http.StatusOK, gin.H{"status": "ready"})
```

失败分支需要 return，避免继续写成功响应；函数末尾的成功分支不必再写 return。c.JSON 本身不会结束函数。

连接池由 run 管理，健康 Handler 不调用 pool.Close；只取消本次检查的 Context。原则是管理资源生命周期的一方负责关闭，借用方不关闭。

启动时 NewDBPool 成功返回的是 pool 和 nil。之后数据库崩溃，不会让这次已经结束的函数调用再次返回错误，也不会改变原来的 err。后续查询、写入或 Ping 才会各自产生新的错误。

数据库故障通常不会导致 ListenAndServe 返回，HTTP 仍在监听。健康检查失败只返回本次响应，不会自动关闭进程。单独访问接口也不会自动摘流量；后续需配置反向代理或 Kubernetes 等根据 readiness 结果调整流量。

## 21. HTTP Server、goroutine、channel 与优雅退出

当前已用显式 http.Server 接入 HTTP 超时，启动方式为：

```go
serverErr := make(chan error, 1)
go func() {
    serverErr <- server.ListenAndServe()
}()
```

创建 server 不会监听；调用 ListenAndServe 才开始监听。该 goroutine 启动一次，ListenAndServe 调用一次，但其内部持续 Accept 新连接。常见 HTTP/1.1 场景中，连接由另外的 goroutine 处理，一个保持连接可以依次承载多个请求；不是所有请求都在这个监听 goroutine 里执行。

通道传递的是 ListenAndServe 结束时的结果，不是每个请求的业务错误。容量为 1，使主流程正在执行 Shutdown、不接收通道时，监听 goroutine 也能发送一次结果并结束。

run 内 select 等待两个事件：

```go
select {
case err := <-serverErr:
    if err != nil && !errors.Is(err, http.ErrServerClosed) {
        return fmt.Errorf("HTTP 服务失败: %w", err)
    }
    return nil
case <-signalCtx.Done():
    // 使用新的退出超时 Context 执行 Shutdown
}
```

正常运行时两个 case 都未满足，select 阻塞等待，不是忙轮询。select 没有外层 for 时只选择并执行一个分支，不会执行完信号分支再回来执行另一个分支。

当前信号分支使用 NotifyContext 监听 os.Interrupt 和 SIGTERM，调用 stop 恢复信号默认行为，再用 Background 派生 10s 退出 Context 执行 Shutdown。不要使用已经取消的信号 Context 或早已超时的启动 Context。

关闭顺序：

```text
收到退出信号
  → Shutdown 停止监听并等待活动请求结束
  → 成功则 run 返回 nil
  → 执行 defer，关闭 Redis 和 PostgreSQL
  → main 正常结束
```

Shutdown 超时时，当前实现记录错误、调用 server.Close 强制关闭连接，再返回错误。Close 不保证所有 Handler goroutine 已经结束；若有脱离请求的后台任务或长期操作，还需单独管理取消和等待，不能认为已有绝对的进程退出时限。

Shutdown 返回前不要因 ListenAndServe 已返回就提前退出主流程。当前信号分支同步等待 Shutdown，符合这一点。后续若从其他 goroutine 引入 Shutdown，也需要明确等待它完成。

### ErrServerClosed 不是业务错误分类

| 事件 | 常见结果 | 是否意味着 HTTP 监听结束 |
| --- | --- | --- |
| 任务不存在 | 业务错误，响应 404 | 否 |
| 操作超时 | context.DeadlineExceeded 等 | 否 |
| 客户端取消 | context.Canceled 等 | 否 |
| PostgreSQL 故障 | 查询或 Ping 错误 | 否 |
| 端口占用 | ListenAndServe 返回监听错误 | 该次监听未能启动 |
| Shutdown / Close | ListenAndServe 返回 http.ErrServerClosed | 是 |
| Shutdown 等待超时 | Shutdown 自己返回 Context 超时错误 | 监听通常已关闭，活动请求可能仍未结束 |

ErrServerClosed 是特定错误值，不代表所有非 200 响应。第一个 case 异常时返回 error，正常关闭时返回 nil；两者都会结束 run，只是退出码不同。数据库故障后程序继续运行，是因为 run 仍在 select 等待，不是因为它返回 nil 后 main 继续常驻。

## 22. 两个 select 分支如何验收，以及常用检查命令

### 22.1 监听失败分支

先保持 PostgreSQL 正常，从模块根目录编译并启动：

```powershell
go build -o ./server.exe ./day9/cmd/server
./server.exe
```

在另一个终端从同一目录再启动一次。第二个进程初始化成功后，因相同监听地址端口被占用而返回错误，预期日志包含 HTTP 服务失败，进程退出后 `$LASTEXITCODE` 为 1。

注意：当前启动会构建并清理布隆过滤器，第二实例并非完全没有共享状态影响。最好使用独立验收环境；更精确的单元测试可用一个只占用端口的 Listener 制造冲突，避免启动第二套完整业务依赖。

### 22.2 信号退出分支

运行编译后的 server.exe，按一次 Ctrl+C。预期先出现收到退出信号，再出现 HTTP 服务已关闭，正常结束时退出码为 0。使用编译后的程序可减少 go run 包装进程对信号测试的影响。

日志“HTTP 服务已关闭”出现在 run 返回前，尚不代表连接池等 defer 已全部执行完毕。

### 22.3 活动请求与退出超时

可在隔离测试环境临时添加耗时路由。请求已开始后再按 Ctrl+C，短请求应在退出预算内完成，随后进程结束；超过退出预算的请求应触发 Shutdown 超时和强制关闭路径。

测试时需区分 HTTP WriteTimeout 与退出预算。当前示例均为 10s，若要观察 15s 请求的退出超时，需在测试环境调整 WriteTimeout，避免先被响应写入超时干扰。临时测试路由验收后删除。

这些是验收方法，本次仅整理笔记，没有实际执行端口冲突、停库、恢复或信号退出测试。

### 22.4 常用代码检查

以下命令从 cloudtask-go 目录执行：

```powershell
git status
git log -1 --oneline
go build ./day9/cmd/server
go test ./day9/... -count=1
```

| 命令 | 作用 | 不能证明什么 |
| --- | --- | --- |
| git status | 查看分支、修改、暂存及未跟踪文件 | 不表示代码正确 |
| git log -1 --oneline | 确认最新提交 | 不表示本地修改已经提交 |
| go build | 检查服务入口及依赖可编译，通常生成 server.exe | 不启动服务，不检查数据库连接 |
| go test ./day9/... -count=1 | 运行 day9 及子包测试，不使用测试结果缓存 | 只验证已有测试覆盖的行为 |

逐行执行时，前一条失败未必阻止下一条执行，需要分别检查退出码与输出。测试若依赖数据库、Redis，需要相应环境；无断言、只打印日志的测试无法可靠验收行为。上述命令不会自动提交或推送代码。

## 23. 本次核对后的实现进度与遗留项

截至 2026-11-13，本次只读检查代码确认：

- 第一版 up/down 已写入，Compose 已取消旧业务 Schema 的初始化挂载。
- main/run 已拆分，必要依赖失败会返回错误，main 统一设置失败退出码。
- HTTP 超时已接入 http.Server，存在错误通道、信号处理、Shutdown 和超时后 Close 的流程。
- /healthz 和 /readySys 已注册，readySys 复用连接池并限制 Ping 时间。

仍需完善或验证：

- config.Load 仍读取 day9/.env，测试 prepareEnv 仍切到临时 day9/internal/config；上次记录的临时路径不匹配仍存在。本次没有再次运行测试，不能声称已修复。
- PostgreSQL MinConns 是否实际接入应继续核对 database.go；仅加载字段不等于生效。
- 退出时尚未将共享就绪状态设为 false；readySys 当前仅检查数据库。
- ShutdownTimeout 尚未配置化，当前固定为 10s；健康检查超时也固定为 1s。
- 仍有 log.Printf，Gin 使用 gin.Default，尚未统一结构化请求日志。
- 清理构建失败的布隆 Key 时，判断 cleanupErr，却仍记录旧 err，应记录 cleanupErr。
- 注释中不能把监听错误描述为所有“程序崩溃”；未恢复的 panic 等进程级异常不一定通过 serverErr 返回。
- 标准 Shutdown 不处理所有后台任务和已劫持连接；后续若引入这类任务，需要单独设计生命周期。

开发库 force 1 成功来自本次对话的操作反馈；临时库完整验收、备份恢复演练、故障退出结果需要各自保存执行记录。本次笔记更新没有删除数据卷、执行迁移、恢复备份或修改业务代码。
