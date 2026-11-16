# 引入 CI/CD：配置管理与问题笔记

记录日期：2026-11-12。

后续补充：2026-11-13，第 14～23 节记录迁移管理、备份恢复、日志、健康检查和优雅退出。2026-11-16，第 24～34 节补充项目工程规范、Docker 多阶段构建、Compose 运行、交付及 Git 管理；第 35～41 节补充镜像瘦身、非 root 运行、Linux 权限及 docker run 环境变量引号问题。前文保留当时的排查过程；最新实现进度与待办以第 41 节为准。

最初主要完成 CI/CD 前的配置管理准备：将连接地址、连接池参数、超时等从启动代码中提取，通过环境变量加载到 Config，再传给各组件。近期已进一步完成 Docker 镜像构建与本机 Compose 运行；用户已反馈构建及 API 访问成功。这仍不代表 CI 流水线、镜像自动发布或远程自动部署已经完成。

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

## 24. 近期完善与项目工程规范

补充日期：2026-11-16。以下区分代码现状、用户操作反馈和后续建议，不将讲解过的方案直接记为已完成。

近期提交包括：

- `3bbac84`：完善数据库迁移、健康检查与优雅退出。
- `916a038`：完善 CloudTask 容器化部署与工程规范。
- `5b8030a`：停止跟踪本地编译产物和环境配置。

工程规范的目的，是让项目不依赖某个人电脑上的隐含条件，使其他人能理解、构建、运行和排查问题。

| 方面 | 当前约定或实现 | 仍需注意 |
| --- | --- | --- |
| 模块与目录 | 根目录保存 go.mod、go.sum；day9/cmd/server 为 API 入口，internal 按配置、数据库、缓存、处理器、服务、仓储等分包 | 容器中保持模块内的 day9 路径层级 |
| 配置管理 | Config 集中读取环境变量，提供默认值、类型转换和校验；.env.example 提供模板 | 真实 .env 与 .env.docker 不入库、不打进镜像；本机和容器地址分开 |
| 数据库结构 | 使用版本化 up/down 迁移；业务表不再依赖旧初始化目录自动创建 | 新环境启动 API 前先执行迁移；不要修改已应用的迁移 |
| 运行生命周期 | main/run 分工、错误向上返回、资源 defer 关闭、HTTP 超时与信号退出 | 不把“进程存在”当作“应用就绪” |
| 可观测性 | slog JSON 启动日志、/healthz、/readyz、Compose 日志 | 当前仍混用 log.Printf 与 Gin 默认日志；/readyz 主要检查数据库 |
| 构建与部署 | 固定 Go 1.26 系列，Docker 多阶段构建，Compose 管理三个服务 | 1.26 标签随补丁更新，并非不可变版本；严格复现时可进一步固定补丁或摘要 |
| 版本控制 | 提交源码、模块文件、部署配置、迁移和文档 | 不跟踪本地可执行文件、数据库备份、真实凭据；忽略规则需覆盖正确目录 |
| 交付文档 | 区分本机运行、容器运行、空库迁移和已有数据恢复 | 命令注明工作目录；在干净环境验证说明能否复现 |

这些约定为后续 CI/CD 提供基础，但当前仓库尚未发现实际的 GitHub Actions 工作流。

## 25. Docker 中的镜像、容器、网络与数据卷

### 25.1 镜像与容器

镜像是包含程序、文件和默认启动配置的只读模板；容器是根据镜像创建的实例，具有可写层、进程和通常隔离的网络环境。容器停止后仍存在，可以再次启动。

容器与虚拟机的关键区别是内核：容器共享运行环境的内核，虚拟机有自己的内核。Windows Docker Desktop 的 Linux 容器通常运行在 WSL 2 等提供的 Linux 环境中，并不是直接共享 Windows 内核。

### 25.2 服务名、容器名与服务发现

Compose 的 `db`、`redis`、`api` 是服务名。实际容器名通常类似 `day9-db-1`，可以用 `docker compose ps -a` 确认。

- `docker compose exec db sh`：Compose 根据服务名定位容器。
- `docker exec -it day9-db-1 sh`：Docker 根据实际容器名定位容器。
- 默认 Compose 网络中的容器通过内部 DNS，按服务名找到彼此的 IP。
- IP 可能在重建后改变，连接时应使用服务名；连接断开后应用仍需重新解析、重连。

服务发现就是“通过稳定名称找到服务地址”。它不会自动检查数据库业务表是否存在，也不会代替密码认证。

### 25.3 localhost 与端口映射

`localhost` 指当前网络环境自己，通常解析为 `127.0.0.1` 或 `::1`，不是服务名。

| 连接发起位置 | PostgreSQL 地址 | Redis 地址 |
| --- | --- | --- |
| Windows 本机 API | 127.0.0.1:5432 | 127.0.0.1:6379 |
| 同一 Compose 网络内的 API | db:5432 | redis:6379 |

例如 `127.0.0.1:16379:6379` 表示只在宿主机回环地址开放 16379，将流量转发到 Redis 容器的 6379。容器间仍访问 `redis:6379`。左右端口不必相同；右边必须匹配程序实际监听的端口，程序自身的监听端口也可以通过配置改变。

当前数据库与 Redis 的宿主机映射都已限制为 `127.0.0.1`。PostgreSQL 日志中的 `listening on 0.0.0.0` 描述容器内监听，不等于宿主机仍对所有网卡发布端口。

### 25.4 数据卷的持久化边界

```yaml
volumes:
  - postgres_data:/var/lib/postgresql/data
```

左边是 Docker 管理的命名卷，右边是 PostgreSQL 容器内的目录。默认实际卷名通常带项目名前缀，例如 `day9_postgres_data`；在 Docker Desktop → Volumes 可以查看内容。

| 操作 | 数据影响 |
| --- | --- |
| 停止后启动同一个容器 | 原容器可写层和命名卷都保留 |
| 删除并重建容器，挂载原卷 | 可以继续使用数据库文件 |
| docker compose down | 默认保留这里的命名卷 |
| docker compose down -v | 删除声明的数据卷，需避免误操作 |
| 换项目名或换电脑 | 不会自动找到或复制旧电脑的卷 |

持久化关键是卷独立于容器生命周期，而不只是“写到了磁盘”。数据库重启需要重新建立内存状态：PostgreSQL 按需读取数据库文件，异常退出后可能做 WAL 恢复；Redis 根据其持久化配置从 RDB/AOF 重建内存。Volume 保留文件，并不自动把尚未持久化的内存数据写入磁盘。

## 26. go run、本机 go build 与 Docker 编译

从模块根目录执行：

```powershell
# 编译后直接运行；通常使用临时可执行文件
go run ./day9/cmd/server

# 生成本机可执行文件，随后自行运行
go build -o cloudtask-local.exe ./day9/cmd/server
.\cloudtask-local.exe
```

两者都会编译。go run 不会在指定位置交付一个正式的可执行文件；go build 不会自动运行程序。修改源码后，旧 exe 不会更新，需要重新编译。

| 对比 | 本机编译 | Docker builder 编译 |
| --- | --- | --- |
| Go 工具链 | 本机安装的 Go | golang:1.26 提供 |
| 源码 | 电脑上的项目目录 | COPY 进入构建阶段的文件 |
| 目标系统 | 取决于当前 Go 环境，Windows 本机通常为 Windows | 当前显式指定 GOOS=linux |
| 输出 | 模块根目录下的 cloudtask-local.exe | builder 内部 /out/cloudtask-api |
| 用途 | Windows 本机运行 | 复制到 Linux 运行镜像 |

构建目标由 GOOS、GOARCH 等设置决定，不由 .exe 后缀决定。当前 Dockerfile 从源码编译，不需要先生成本地 exe。

编译器根据入口包、直接或间接 import 的代码、模块依赖及构建配置生成程序；不会因为源码里调用了 PostgreSQL 或 Redis，就在编译时执行连接操作。数据库地址、密码、表是否存在等通常在运行时才检查。构建步骤若另行执行集成测试、迁移或依赖数据库的代码生成，才可能需要真实依赖。

## 27. 当前 Dockerfile 的多阶段构建

当前文件位于 day9/Dockerfile，构建上下文应为模块根目录 cloudtask-go。去除教学注释后的主要内容：

```dockerfile
FROM golang:1.26 AS builder
WORKDIR /cloudtask

COPY go.mod go.sum ./
RUN GOPROXY=https://goproxy.cn,direct go mod download

COPY day9 /cloudtask/day9/
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/cloudtask-api ./day9/cmd/server

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/cloudtask-api /app/cloudtask-api
CMD ["/app/cloudtask-api"]
```

### 27.1 两个阶段都属于镜像构建

```text
源码 → builder：下载依赖、编译
                ↓ /out/cloudtask-api
       runtime：复制程序、记录启动命令
                ↓
       最终镜像 cloudtask-api:dev
                ↓ 创建并启动容器
       执行 /app/cloudtask-api，读取运行配置
```

不是要长期运行 builder、runtime 两个业务容器。Runtime 阶段也不再次编译：RUN 安装证书发生在构建时，CMD 的 API 启动命令发生在容器启动时。

默认构建最终阶段，只把最终阶段所需的内容放进运行镜像。编译器、依赖源码和缓存仍可能保留在本机构建缓存中，但不会自动进入最终镜像。所需的 Go 依赖代码通常已经链接进二进制，不是程序运行时完全没有依赖。

可以用两个 Dockerfile 分开构建，但需额外安排编译结果的导出和传递；单个多阶段文件通过 COPY --from 自动衔接。

### 27.2 指令、工作目录与路径

| 指令或路径 | 含义 |
| --- | --- |
| FROM ... AS builder | 开始阶段并命名，供后续引用 |
| WORKDIR /cloudtask | 设置当前阶段工作目录，不存在时创建 |
| RUN ... | 构建镜像时执行命令 |
| COPY go.mod go.sum ./ | 从构建上下文根目录复制到当前阶段的工作目录 |
| COPY day9 /cloudtask/day9/ | 复制 day9 的内容，保留模块内的 day9 层级 |
| COPY --from=builder ... | 从 builder 的文件系统复制产物，源路径从该阶段根目录解析 |
| CMD / ENTRYPOINT | 配置容器启动行为，构建时不启动 API |

`./` 是当前目录的写法，但参照点取决于位置：普通 COPY 的源相对于构建上下文；相对目标相对于 WORKDIR；RUN 中的相对路径通常相对于该命令的工作目录。WORKDIR 不改变 Docker 在宿主机构建上下文中寻找源文件的位置。

曾把工作目录改成 /cloudtask，却仍把 go.mod 放到 /cloudtask/day9，导致下载依赖的位置不匹配。当前 Module 文件位于 /cloudtask，入口位于 /cloudtask/day9/cmd/server，与模块导入路径一致。

### 27.3 编译变量和输出参数

```text
CGO_ENABLED=0       禁用 Go 与 C 代码交互的机制，不是简单地“不编译 C++”
GOOS=linux          目标操作系统为 Linux
-o /out/cloudtask-api    输出文件：cloudtask-api
./day9/cmd/server        输入入口包：server 目录
```

变量名是 CGO_ENABLED，最后有 D。支持纯 Go 构建的依赖通常可以借此避免系统 C 动态库依赖；必须使用 CGO 的依赖可能无法构建或缺少相应功能。CPU 架构另由 GOARCH 控制。前缀变量只作用于这条 RUN 命令，不等于用 ENV 持续设置后续步骤。

### 27.4 CMD、ENTRYPOINT 与信号

当前使用 CMD 的 JSON 数组形式即可启动服务，不必仅为学习 ENTRYPOINT 而修改。

- CMD 提供默认命令；docker run 镜像名 后的其他命令可替换它。
- exec 形式的 ENTRYPOINT 指定主程序；运行时传入的参数追加给它，CMD 可提供默认参数；更换主程序可使用 --entrypoint。
- JSON 数组形式直接执行程序，不额外经过 /bin/sh -c；参数边界明确，应用通常成为 PID 1，直接接收 SIGTERM，有利于当前 API 的优雅退出逻辑。
- shell 形式可能由 shell 占据 PID 1，影响信号转发。exec 形式也不会自动展开 $变量、管道或重定向。

## 28. Go Module、Docker Build Cache 与下载失败排查

go.mod 声明模块路径、Go 版本要求、依赖版本要求及 replace 等规则；go.sum 记录依赖模块内容的校验和。go.sum 不保存依赖源码，也不是严格的版本锁文件。两者都应提交 Git。

Docker 会按构建指令及相关输入查找可复用结果；COPY 涉及的文件改变，会影响该步骤及依赖它的后续步骤。因此先复制 Module 文件、下载依赖，再复制业务代码：

```text
只修改业务代码：Module COPY 与依赖下载通常复用缓存 → 重新复制源码和编译
修改 Module 文件：相关 COPY 缓存失效 → 重新下载依赖和编译
```

这不只是缓存下载文件，还可以直接跳过已命中的 RUN 步骤。远程内容更新不会自动使相同 RUN 指令的已有缓存失效。缓存影响效率，不保证首次构建不需要联网。

本次构建曾在 go mod download 失败：

```text
Get https://proxy.golang.org/...: dial tcp ...:443: connect: connection refused
```

已解析出地址，但到依赖服务器的 TCP 连接失败；尚未执行 go build，不能据此判断源码错误。Docker 镜像仓库、Go Module 代理和 Alpine 软件仓库是不同的下载来源，镜像下载成功不意味着依赖下载也能成功。

当前改为在下载指令前指定 GOPROXY=https://goproxy.cn,direct，之后用户反馈构建成功。GOPROXY 是 Go 模块代理，不是 Docker 镜像仓库，也不是通用 HTTP 代理。逗号分隔时通常仅在前一源返回 404/410 后回退；direct 不保证连接被拒绝时自动兜底。

本机 Go 配置和依赖缓存不会自动进入 builder。若其他环境仍失败，应根据新日志检查网络、Docker 构建代理或目标服务，而不是关闭依赖校验。当前配置没有禁用 GOSUMDB。

## 29. 构建、运行与验证命令

以下均从 cloudtask-go 目录执行；示例只说明操作步骤，不表示本次笔记整理重新执行了部署。

```powershell
# 读取指定文件和环境配置，仅校验，不启动容器
docker compose --env-file day9/.env -f day9/docker-compose.yml config --quiet

# 只验证 builder 阶段；要求能下载依赖
docker build --target builder -f day9/Dockerfile -t cloudtask-builder .

# 构建最终镜像
docker build -f day9/Dockerfile -t cloudtask-api:dev .

# 展开构建日志，便于排查
docker build --progress=plain -f day9/Dockerfile -t cloudtask-api:dev .
```

- -f 指定 Dockerfile；-t 指定镜像名和标签；最后的 . 指定构建上下文，不能省略。
- dev 是普通标签，不会自动启用开发模式；本地重新构建同标签不会自动更新已运行的容器。
- config --quiet 成功时通常无输出，失败时有错误；不代表端口空闲、数据库可达或应用已就绪。
- 当前 Compose 的 api 只有 image，没有 build；compose up 不会自动按 Dockerfile 构建 API，需要先 docker build。

依赖就绪、迁移完成、配置备妥后：

```powershell
docker compose -f day9/docker-compose.yml up -d db redis
docker compose -f day9/docker-compose.yml exec db pg_isready -U app -d cloudtask
# 确认数据库、Redis 就绪，并完成必要迁移后启动 API
docker compose -f day9/docker-compose.yml up -d api
docker compose -f day9/docker-compose.yml ps -a
docker compose -f day9/docker-compose.yml logs -f --tail 50 api
```

重新构建同标签镜像后，可以明确重建 API 容器以采用新镜像和配置：

```powershell
docker compose -f day9/docker-compose.yml up -d --force-recreate api
```

Ctrl+C 退出 logs -f 只停止查看日志。查看最近十分钟依赖日志可使用 `logs --since 10m db redis`，--tail 的行数按每个容器计算。读日志先看来源、时间、事件和上下文，再确认是否恢复；不要仅凭 FATAL 就认定数据库崩溃。

API 验证：

```powershell
curl.exe -i http://127.0.0.1:8080/healthz
curl.exe -i http://127.0.0.1:8080/readyz
curl.exe -i http://127.0.0.1:8080/tasks
```

构建成功只表示镜像完成；运行时还可能因配置、认证、数据库不可达、缺表、监听端口冲突等失败。当前 /readyz 的 Ping 通过也不能证明所有迁移已经应用，需要业务接口和结构检查进一步验证。

## 30. Compose 运行配置与敏感信息边界

当前 api 服务配置：

```yaml
  api:
    image: cloudtask-api:dev
    env_file:
      - .env.docker
    ports:
      - "127.0.0.1:8080:8080"
```

env_file 的路径相对于 Compose 文件，所以读取 day9/.env.docker。它将值放进容器环境变量，不等于把文件复制进镜像。config.Load 允许 day9/.env 不存在，再从环境读取，因此 /app 下无须准备这个文件。

首次从 .env 复制为 .env.docker 时，应确认目标文件不存在再操作，避免覆盖已有配置。保留实际凭据并改为容器地址：DATABASE_URL 中主机使用 db，REDIS_ADDR 使用 redis:6379，HTTP_ADDR 使用 :8080。敏感值不在本文展示。

必须区分两层配置：

| 配置位置 | 用途 |
| --- | --- |
| Compose 默认 .env 或命令行 --env-file | 为 YAML 中的 ${POSTGRES_PASSWORD}、${REDIS_PASSWORD} 等变量做替换 |
| api.env_file 或 api.environment | 显式提供给 API 容器的进程环境 |

仅填写 .env.docker 不会自动改变 db、redis 的配置。两侧实际凭据必须一致；修改 PostgreSQL 环境变量也不会自动重设已有卷中用户的密码。

当前 api 未配置 depends_on、healthcheck，db 也未配置 healthcheck；Redis 有 PING 健康检查。这意味着一次 up 全部服务不会自动等待数据库就绪。后续可补依赖健康条件和 API 就绪检查，但仅依赖启动顺序也不能替代运行时重连策略。

## 31. 迁移文件、数据搬迁与交付

### 31.1 API 镜像是否需要 migrations

当前启动代码未执行迁移，也未读取迁移目录；所以 Runtime 仅复制二进制，暂不需要复制 migrations。把 SQL 放进镜像不会自动执行。迁移工具若在宿主机或独立迁移容器运行，应在对应环境提供迁移文件。

### 31.2 新部署与数据搬迁是两件事

| 目的 | 处理方式 |
| --- | --- |
| 新用户从空库开始 | 启动数据库 → 执行版本化迁移 → 可选演示数据 → 启动 API |
| 换电脑保留现有数据 | 备份原数据库 → 传输备份 → 在新环境恢复 → 验证迁移版本和业务数据 → 启动 API |

Compose 会重建服务和网络，不会跨电脑传输 Volume。相同卷名也不代表两台电脑共享数据。

下面以 day9 目录、用户 app、数据库 cloudtask 为例。先在旧电脑备份：

```powershell
docker compose exec db pg_dump -U app -d cloudtask -Fc -f /tmp/cloudtask.dump
docker compose cp db:/tmp/cloudtask.dump ./cloudtask.dump
```

传输备份后，在新环境启动并等待数据库就绪，再向没有业务表的目标库恢复：

```powershell
docker compose cp ./cloudtask.dump db:/tmp/cloudtask.dump
docker compose exec db pg_restore -U app -d cloudtask --no-owner --no-privileges --exit-on-error /tmp/cloudtask.dump
```

这是恢复示例，不是本次实际演练。完整备份通常包含 Schema 和数据，避免先迁移创建同名表再直接重复恢复；恢复失败时需检查是否部分完成，不要直接把目标认定为可用。pg_dump 不备份整个集群的角色等全局对象，新环境应另行准备用户与权限。复制文件避免了旧版 PowerShell 重定向二进制时的编码问题。

### 31.3 交付材料

源码交付包括源码、Module 文件、Dockerfile、.dockerignore、Compose、.env.example、迁移和 README。镜像交付可通过镜像仓库，或离线导出：

```powershell
docker save -o cloudtask-api-dev.tar cloudtask-api:dev
# 接收方
docker load -i cloudtask-api-dev.tar
```

API 镜像归档不含 PostgreSQL、Redis 镜像，不含数据卷。完全离线时需另外准备依赖镜像；需要现有业务数据时另外提供备份及恢复说明。实际密码由接收方在运行时配置，不写入 API 镜像。

交付验收应使用独立项目名或隔离环境、空数据卷，按 README 从零走完构建、迁移、启动和接口检查，不以现有开发库恰好有表作为成功依据。

## 32. GitHub 压缩包变大与忽略规则

本次排查确认，提交 916a038 曾同时跟踪 server.exe、cloudtask-local.exe，每个 47,972,864 字节，约 45.75 MiB，未压缩合计约 91.5 MiB。它们是本地构建产物，是源码 ZIP 明显变大的主要来源；并不是 Docker 镜像或数据卷自动进入仓库。

5b8030a 已停止跟踪这两个文件和 day9/.env.docker。本地文件仍可保留，原提交历史仍含旧内容。

```powershell
# 仅在仍被跟踪时使用；--cached 保留本地文件
git rm --cached -- server.exe cloudtask-local.exe day9/.env.docker
```

当前有一个范围问题：*.exe 等规则被写在 day9/.gitignore，只作用于 day9 及其子目录，无法忽略仓库根目录的两个 exe。整理时 git status 仍将它们显示为未跟踪，后续 git add . 可能再次加入。

建议在 cloudtask-go/.gitignore 增加覆盖整个仓库的规则（这是建议，本次仅改笔记）：

```gitignore
*.exe
*.test
coverage.out
*.zip
*.dump
.env
.env.*
!.env.example
```

与 Git 规则分别管理的 .dockerignore，也可补 *.exe 等本地构建产物规则。现有 Dockerfile 只复制 day9 和 Module 文件，根目录 exe 不会被该 COPY 带入镜像，但不应依赖这一点代替明确的忽略规则。

停止跟踪后推送新提交，从最新提交下载源码 ZIP 会去掉这些文件；git clone 仍可能下载包含大文件的历史。若曾推送真实密码，应更换相关凭据，移出最新提交不能撤销泄露。历史重写是另一项操作，需单独规划，不作为清理源码 ZIP 的默认步骤。

## 33. 提交规范与 CI/CD 的下一步

提交描述应对应实际变更，可以使用以下类型：

- feat：新增容器化部署等能力。
- fix：修复配置路径、启动或测试问题。
- chore：清理编译产物、完善忽略规则等维护。
- docs：整理本文及使用说明。

部署与工程规范若在同次变更中，可以共同描述；不要把项目已有功能重新列为本次新增，也不要用“已验证”描述仅讲解过的方案。

接入流水线前应明确这些步骤各自的边界：

```text
代码检查、单元测试
    → 构建 API 镜像
    → 在独立数据库/Redis 环境执行迁移与集成验证
    → 发布带版本标识的镜像
    → 目标环境配置与迁移
    → 启动并验证就绪、业务接口
```

这是后续路线，不代表当前已实现。单元测试应隔离开发机配置；需要真实服务的测试应单独准备依赖，不将数据库密码写进仓库或构建日志。生产运行数据不能随 CI 测试环境清理。

## 34. 阶段状态、证据与后续待办（本轮完善前）

截至本次整理，结合文件检查和用户反馈：

| 项目 | 状态与依据 |
| --- | --- |
| Go 版本 | go.mod 声明 Go 1.26.0，builder 使用 golang:1.26 |
| Docker 多阶段 | 文件已实现；用户反馈镜像构建成功 |
| Compose API | 已添加 api，使用本地 cloudtask-api:dev；用户反馈 API 访问成功 |
| 配置隔离 | API 从 .env.docker 注入；镜像只复制程序，.dockerignore 排除真实环境文件 |
| 本机端口范围 | db、redis、api 的发布地址均为 127.0.0.1 |
| PostgreSQL MinConns | 当前 database.go 已赋值，更新第 23 节的待核对记录 |
| 就绪路由 | 当前 URL 是 /readyz，readySys 是 Go 方法名，修正前文将其混作 URL 的记录 |
| Git 清理 | 5b8030a 已停止跟踪 exe 与 .env.docker；根目录忽略范围仍待完善 |
| 自动化 CI/CD | 尚未发现实际工作流；本地构建和运行不等于自动化发布部署 |

仍需处理或单独验证：

- 根目录 .gitignore 缺失；当前本地 server.exe、cloudtask-local.exe、ZIP 仍显示为未跟踪，应防止再次提交。
- 当前 config.go、config_test.go 有用户尚未提交的修改；本次不改动它们。测试辅助函数仍 t.Chdir(dir) 到临时 day9/internal/config，而 Load 读取 day9/.env；完整配置用例的 REDIS_ADDR 输入为 redis:6380，期望却为 localhost:6380，也需要对齐。本次没有执行测试，不能声称已修复或通过。
- 根 README 仍提到 Go 1.25.7、首次启动自动执行旧 Schema，与当前 Go 1.26、独立迁移流程不一致，后续需同步。
- 当前只完成本机容器化，尚需新环境空卷迁移、备份恢复、镜像交付和优雅退出的独立验收记录。
- API/数据库健康检查及依赖就绪等待、结构化请求日志、ShutdownTimeout 配置化等仍是待办；第 23 节提到的布隆 Key 清理错误日志仍需修正。

本次只更新本 Markdown 笔记，没有修改业务代码、Dockerfile、Compose 或忽略规则，没有构建/启动容器、迁移数据库、生成或恢复备份，也没有提交和推送 Git。

参考：[Docker 多阶段构建](https://docs.docker.com/build/building/multi-stage/)、[Dockerfile 指令](https://docs.docker.com/reference/dockerfile/)、[构建缓存](https://docs.docker.com/build/cache/invalidation/)、[Compose 网络](https://docs.docker.com/compose/how-tos/networking/)、[Compose 环境变量注入](https://docs.docker.com/compose/how-tos/environment-variables/set-environment-variables/)、[数据卷](https://docs.docker.com/engine/storage/volumes/)、[Go CGO](https://pkg.go.dev/cmd/cgo)、[PostgreSQL pg_dump](https://www.postgresql.org/docs/17/app-pgdump.html)。

## 35. 基础镜像、多阶段构建与产物位置

补充日期：2026-11-16。本轮继续完善容器运行镜像，不代表已经接入自动化 CI/CD。

### 35.1 Alpine、scratch、Debian Slim 的角色

基础镜像提供程序运行需要的用户空间文件；Linux 内核由容器运行环境提供。Windows Docker Desktop 的 Linux 容器通常使用 WSL 2 或 Linux 虚拟机提供的内核。

| 选择 | 特点 | 使用时需要注意 |
| --- | --- | --- |
| Alpine | 轻量发行版，包含 Shell、BusyBox 和 apk，使用 musl | 部分依赖 glibc 的动态链接程序不能直接使用 |
| scratch | 空白构建起点，没有预置 Shell、包管理器和证书 | 必须自行提供程序及证书等所需文件 |
| Debian Slim | Debian 精简镜像，使用 glibc，保留基础工具和包管理能力 | 通常比 Alpine 大，仍需按需安装运行依赖 |

当前 API 使用 Alpine；Compose 中 redis:8-alpine 表示 Redis 自身基于 Alpine 打包。两个容器各有文件系统，不是共用一套 Alpine。生产镜像并非必须使用 Alpine，golang 镜像也能运行程序，但会携带运行时不需要的工具链等内容。

### 35.2 本机 exe 与 builder 产物不是同一个文件

| 路径 | 位置与来源 |
| --- | --- |
| cloudtask-local.exe | Windows 本机编译产物，不参与当前 Dockerfile 的编译 |
| /out/cloudtask-api | builder 阶段内部生成的 Linux 程序 |
| /app/cloudtask-api | runtime 阶段从 builder 复制得到的 Linux 程序 |

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/cloudtask-api ./day9/cmd/server
COPY --from=builder /out/cloudtask-api /app/cloudtask-api
```

这里的绝对路径位于镜像构建环境内部，不会自动导出到 Windows 项目目录。最终镜像从 Alpine 重新开始，只选择性复制二进制，因此不自动继承 builder 的 Go 编译器、源码目录或依赖缓存。Go 运行时通常已经链接进二进制，不等于需要安装 Go 编译器。二进制仍可能保留函数名、源码路径或通过 go:embed 嵌入的资源，不能声称其中完全没有源码相关信息。

## 36. 镜像大小检查、瘦身结果与构建缓存

### 36.1 三条检查命令

```powershell
docker image ls cloudtask-api
docker image inspect cloudtask-api:dev --format "{{.Size}}"
docker history --no-trunc cloudtask-api:dev
```

- image ls：查看本地镜像概况；U 表示有容器引用，不代表容器一定正在运行。
- image inspect：读取镜像详细配置；--format 使用模板提取字段，Size 单位为字节。
- history：查看最终镜像的构建历史，最新步骤在上；--no-trunc 不截断命令和 ID。它不是完整文件清单，需结合 Dockerfile 或容器内检查确认内容。

原镜像 history 中，复制程序约 47.7MB，Alpine 基础层约 9.08MB，安装证书的层约 1.22MB，WORKDIR 变化约 8.19kB。CMD 主要修改配置，显示 0B；底层 Alpine 的 CMD 被最终启动配置覆盖。历史中的 <missing> 是没有可显示的独立中间镜像 ID，不表示镜像损坏。

用户当前 Docker 输出区分 CONTENT SIZE 和 DISK USAGE：前者为内容存储大小，主要是压缩层数据；后者还涉及解压后的数据。containerd 可以同时保留压缩内容和解压数据，因此不能将 history 的层大小、inspect 的 Size 和磁盘占用视为同一口径。本次 inspect 返回 26309216，与 CONTENT SIZE 26.3MB 对应；不能将此关系推广到所有版本及存储后端。不同镜像共享层时，各行占用也不能简单相加作为独占空间。

### 36.2 编译参数与实际瘦身

当前 Dockerfile 已改为：

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cloudtask-api ./day9/cmd/server
```

环境变量放在 go build 前；-trimpath、-ldflags 是 go build 的参数，必须放在 go build 后。-trimpath 移除构建环境路径；-s、-w 省略符号表及 DWARF 调试信息，通常能减少体积，但降低二进制调试便利性，常规 panic 堆栈通常仍可显示函数和行号。

用户提供的镜像对比：

| 镜像 | ID | DISK USAGE | CONTENT SIZE |
| --- | --- | --- | --- |
| cloudtask-api:dev | 369357009b5b | 84.3MB | 26.3MB |
| cloudtask-2.3-opt1:latest | bfc81c8d9336 | 58.7MB | 15.2MB |

本地占用减少 25.6MB，约 30%；内容大小减少 11.1MB，约 42%。后续非 root 镜像中二进制大小为 33194146 字节，约 33.2MB / 31.7MiB。上述来自不同次输出，不应混作同一个镜像 ID 的测量。

### 36.3 Tag、构建上下文和缓存

在 cloudtask-go 目录构建示例：

```powershell
docker build --progress=plain -f day9/Dockerfile -t cloudtask-api:2.3.2-nonroot .
```

若在 day9 目录，则使用 -f Dockerfile，并将构建上下文设为 ..。否则 COPY go.mod、go.sum 或 day9 可能找不到源路径。此前曾询问“新 Tag 为什么构建失败”，但未提供当次完整命令与错误日志，不能把上下文问题认定为已证实的失败原因。

更换 Tag 本身不使缓存失效。同名 Tag 构建成功后可以指向新镜像，旧镜像不会被原地修改，现有容器也不会自动换镜像；重启旧容器不能完成更新。cloudtask-2.3-opt1:latest 是新仓库名加默认 latest 标签；cloudtask-api:2.3-opt1 才是保留仓库名、改变标签。

本轮改变 go build 指令会重新编译，前面未变化的 Module 下载步骤通常可复用。Dockerfile 位于 day9 内且 COPY day9 会复制它，因此修改 Dockerfile 也可能影响该 COPY 的缓存。构建输出中的 CACHED 表示复用成功。

Build Cache 可能保留 builder 的源码、依赖和旧构建结果，即使最终运行镜像很小，本地缓存仍可能很大。清理缓存不会缩小已生成的镜像；docker build --no-cache 只要求重新构建。apk add --no-cache 是 apk 不保留包索引缓存，与 Docker 构建缓存不是一回事。

## 37. 非 root 用户、UID/GID 与最小权限

API 监听 8080、连接 PostgreSQL/Redis 等操作不需要 root。非 root 用户能限制程序被利用后修改容器内文件的能力，但不是完整安全隔离方案；容器内 root 也不等同于 Windows 管理员。

当前运行阶段的关键配置：

```dockerfile
FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates
RUN addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app
WORKDIR /app
COPY --from=builder --chown=0:0 --chmod=0555 /out/cloudtask-api /app/cloudtask-api
USER 10001:10001
CMD ["/app/cloudtask-api"]
```

- addgroup -S -g 10001 app：创建 GID 10001 的系统用户组。
- adduser -S -D -H -u 10001 -G app app：创建 UID 10001、主组 app 的系统用户，不设置密码、不创建主目录。
- USER：切换后续默认运行身份；只创建用户而不设置 USER，不会自动让程序使用新身份。
- COPY 的 --chown=0:0 明确由 root:root 拥有，--chmod=0555 明确文件权限。

不指定编号时通常是自动选取可用 UID/GID，不是随机抽取；基础镜像或创建顺序变化可能改变结果。Linux 文件权限依赖数字 UID/GID，固定编号有利于保持卷目录及部署配置一致。10001 是项目约定，没有特殊权限含义，也不会自动解决挂载目录权限问题。

Config.User 输出 10001:10001 正常，因为镜像记录的是 USER 指令配置的值，不会自动转换成 app:app。用户名与编号通过镜像中的用户数据库对应。

## 38. 文件所有者、目录权限与验证结果

### 38.1 构建时放文件与运行时写文件的区别

app 用户与 /app 目录只是同名，没有自动归属关系。COPY 在构建时向镜像放入程序；运行时 app 只执行已存在的程序，不需要再次写入该文件。当前程序不需要往 /app 写文件，因而目录和二进制均由 root 拥有。以后若需要文件输出，应按需准备单独可写目录。

权限分为所有者、所属组、其他用户，Linux 根据访问者身份依次匹配一组，不会将三组权限相加。文件 root:root 中，第一个 root 是所有者用户，第二个 root 是所属用户组；app 既不是所有者也不属于 root 组，因此使用“其他用户”权限。

```text
r = 4，w = 2，x = 1
7 = 4 + 2 + 1 = rwx
5 = 4 + 0 + 1 = r-x
0555 → r-x r-x r-x
0755 → rwx r-x r-x
```

前导 0 表示未设置特殊权限位，0555 与通常说的 555 权限相同。普通文件的 r/w/x 是读取、修改、执行；目录的 r 是列出名字，x 是进入和访问内部条目，w 配合 x 允许创建、删除、重命名条目。

| 对象 | 所有者 | 权限 | app 的能力 |
| --- | --- | --- | --- |
| /app | root:root | 0755 | 可以列出、进入，但不能新增、删除或重命名条目 |
| /app/cloudtask-api | root:root | 0555 | 可以读取、执行，不能直接修改 |

文件不可写不代表一定不可删除：删除主要取决于父目录权限。因此需同时限制 /app 的写权限。0555 不意味着 root 永远不能修改文件，root 通常可以修改权限或绕过部分检查。

### 38.2 用户实际提供的验证结果

```powershell
docker image inspect cloudtask-api:2.3.2-nonroot --format "{{.Config.User}}"
docker run --rm --entrypoint /bin/sh cloudtask-api:2.3.2-nonroot -c "id; ls -ld /app; ls -l /app"
```

```text
10001:10001
uid=10001(app) gid=10001(app) groups=10001(app)
drwxr-xr-x    1 root root     4096 Nov 16 12:24 /app
total 32420
-r-xr-xr-x    1 root root 33194146 Nov 16 12:24 cloudtask-api
```

已确认非 root 身份、root 所有权、目录 0755 和二进制 0555。ls 中的 1 是硬链接数，不是文件数量；OverlayFS 等文件系统对目录链接数可能有特殊统计，不能据此推算子目录数量。4096 是目录自身大小，不是内容总大小；total 是条目磁盘块占用汇总，不是文件数量。日期是文件系统修改时间，不是容器启动时间。

- ls -ld /app：-d 表示检查目录本身。
- ls -l /app：列出目录内非隐藏条目的详细信息，并非专门识别二进制。
- ls -l /app/cloudtask-api：明确检查指定文件。
- 分号依次执行命令，前一条失败仍会继续；整段退出状态通常是最后一条命令的状态。

此前 command -v wget 输出 /usr/bin/wget，只能证明 PATH 中能找到命令，未证明网络畅通。--entrypoint /bin/sh 替换了默认启动方式，上述检查没有启动 Go API；--rm 在进程退出后删除临时容器，镜像保留。

## 39. Compose 网络与手动启动非 root 镜像

用户 docker network ls 显示 bridge、day9_default、host 和 none。NETWORK ID 是标识，DRIVER 是驱动，SCOPE local 指当前 Docker 主机范围，不代表公网范围。

| 网络 | 用途 |
| --- | --- |
| bridge | 未指定其他网络时普通 Linux 容器通常使用的默认桥接网络 |
| day9_default | Compose 项目创建的桥接网络，服务可通过 db、redis 等名称互相发现 |
| host | 共享 Docker 主机的网络命名空间；Docker Desktop 支持方式依赖平台及配置 |
| none / null | 通常只保留回环接口，不提供普通外部网络连接 |

网络存在不证明依赖容器已接入或就绪，可以通过 docker network inspect day9_default 检查成员。API 容器的 localhost 指向自身，连接其他容器应使用 db:5432 和 redis:6379。

在 cloudtask-go 目录手动启动的命令：

```powershell
docker run --rm `
  --name cloudtask-api-nonroot `
  --network day9_default `
  -p 127.0.0.1:18080:8080 `
  --env-file .\day9\.env.docker `
  cloudtask-api:2.3.2-nonroot
```

此命令启动镜像的默认 API 程序；18080 是 Windows 本机访问端口，8080 是容器监听端口。只发布到 127.0.0.1，供本机访问。--network 让手动启动的容器加入已有 Compose 网络，不负责启动数据库和 Redis。PowerShell 续行符是反引号，必须放在行尾，不应在其后添加空格。

## 40. 故障排查：docker run 的环境文件引号导致连接 Unix socket

### 40.1 现象与证据

上述手动启动失败，关键错误（省略日志时间等字段）：

```text
数据库加载或连接失败：Unable to ping database:
failed to connect to `user=app database=`:
/tmp/.s.PGSQL.5432 (/tmp): dial error:
dial unix /tmp/.s.PGSQL.5432: connect: no such file or directory
```

程序尝试连接 API 容器自身的 PostgreSQL Unix socket，而非预期的 db:5432。此错误本身不能证明数据库容器未启动，也不是 /app 权限不足造成的。

本轮检查发现：

1. config.Load 直接通过 os.Getenv("DATABASE_URL") 读取 DSN，未去除外层引号。
2. database.NewDBPool 将其传给 pgxpool.ParseConfig，再创建连接池并 Ping。
3. 本地 .env.docker 的 DATABASE_URL 带有外层双引号。
4. 使用同一镜像及 --env-file 启动临时 Shell，仅检查是否以双引号开头，实际输出为 yes，确认引号被保留。检查未打印连接字符串或密码，也未启动 API。

### 40.2 根因与处理方式

docker run --env-file 与 Compose 默认 env_file 的解析规则不同。Compose 会处理外层引号；docker run 会将这种外层引号保留在环境变量值中。DSN 因此不再以 postgres:// 直接开头，未按预期 URI 解析，导致主机、数据库名等配置异常，并回退到本地 Unix socket 连接。

错误中的 user=app 不能单独证明数据库用户解析正确：它也可能来自当前 Linux 用户 app；database= 为空是另一条异常线索。

修改建议如下，密码仅为占位符，不代表实际凭据：

```dotenv
# 修改前
DATABASE_URL="postgres://app:YOUR_PASSWORD@db:5432/cloudtask?sslmode=disable"

# 修改后：只去掉最外层双引号，保留实际连接信息
DATABASE_URL=postgres://app:YOUR_PASSWORD@db:5432/cloudtask?sslmode=disable
```

其他用于 docker run --env-file 的值也应检查是否含不期望的外层引号。不要盲目删除密码内部的引号或其他字符；URI 密码中的保留字符应按 URI 规则编码，也不能假设该文件支持 Compose 的变量插值。

环境文件是在启动时读取的，修改后重新创建容器即可，不需要重建镜像。本轮尚无去引号后成功启动及业务接口验证的记录，不能将“根因定位”写成“恢复已验证”。

参考：[Docker run](https://docs.docker.com/reference/cli/docker/container/run/)、[Compose env_file](https://docs.docker.com/reference/compose-file/services/#env_file)、[Docker CLI 引号行为记录](https://github.com/docker/cli/issues/4665)。

## 41. 本轮最新状态与后续验收

| 项目 | 当前结论与证据 |
| --- | --- |
| 编译瘦身 | 当前 Dockerfile 已有 -trimpath 和 -ldflags="-s -w"；用户提供了优化前后镜像大小对比 |
| 非 root 运行 | 当前 Dockerfile 使用 USER 10001:10001；用户的 id 输出已确认 app 身份 |
| 二进制权限 | 当前 COPY 明确 root:root、0555；用户 ls 输出已确认 |
| /app 权限 | 用户 ls 输出确认 root:root、0755，app 可访问不可写；当前 Dockerfile 依赖 WORKDIR 创建目录的默认权限 |
| 非 root API 启动 | 实际启动在数据库 Ping 阶段失败，尚未完成接口验收 |
| 环境文件故障 | 已确认 docker run 保留 DSN 外层双引号；已给出移除建议，未记录修改后恢复结果 |
| 原有 Compose | 历史上 API 访问成功不等于新非 root 镜像也已通过；原 api 镜像引用需另行核对更新 |
| 自动化 CI/CD | 本轮未新增流水线、镜像发布或远程部署 |

接下来应先按第 40 节调整运行环境文件，确认 db、redis 已在 day9_default 中运行，再重新创建非 root API 容器。启动成功后检查 /readyz 及实际业务接口，记录数据库和 Redis 访问是否正常；就绪检查不能替代迁移结构及业务验证。如改由 Compose 启动新镜像，应显式更新 api 的 image 引用并重建容器。

第 34 节关于 Git 忽略规则、配置测试、README、独立迁移/备份恢复验收等未完成事项仍保留，未因本轮镜像完善而自动完成。此次整理只编辑本文，保留用户已有的 Dockerfile 与业务文件修改；没有替用户更改环境文件、重新构建镜像、启动业务服务或执行数据库操作，没有提交和推送 Git。此前故障排查只额外运行过一个临时 Shell 容器验证引号行为。
