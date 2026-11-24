# 引入 CI/CD：配置管理与问题笔记

记录日期：2026-11-12。

最新补充（2026-11-24）：第 92 节更新了 v2.3.4 的镜像验收结果，第 93～96 节记录 Compose 部署、数据卷、回滚和环境变量模板。第 87～91 节记录 GHCR 发布流程与排错，第 81～86 节记录镜像构建与冒烟测试，第 73～80 节记录集成测试，第 54～72 节记录基础 CI、单元测试与 PR 排错。

后续补充：2026-11-13，第 14～23 节记录迁移管理、备份恢复、日志、健康检查和优雅退出。2026-11-16，第 24～34 节补充项目工程规范、Docker 多阶段构建、Compose 运行、交付及 Git 管理；第 35～41 节补充镜像瘦身、非 root 运行、Linux 权限及 docker run 环境变量引号问题。2026-11-17，第 42～47 节补充健康检查，第 48～53 节补充 API 容器安全配置、YAML 列表、Compose 构建、镜像标签与空间管理。前文保留当时的排查过程；最新配置核对结果与验收边界以第 53 节为准。

最初主要完成 CI/CD 前的配置管理准备：将连接地址、连接池参数、超时等从启动代码中提取，通过环境变量加载到 Config，再传给各组件。随后完成 Docker 镜像构建与本机 Compose 运行；截至 2026-11-18，已根据 GitHub 运行截图确认基础 CI Job 成功，尚不代表镜像自动发布或远程自动部署已经完成。

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

## 42. 健康检查阶段：目标与实际配置（2026-11-17）

本阶段为 API 镜像增加 HEALTHCHECK，为 PostgreSQL 增加 Compose 健康检查，并让 API 在启动时等待 PostgreSQL 健康。以下是本次整理时的实际文件状态，后文另列建议，避免将建议写成已经实施的改动。

### 42.1 API 镜像的健康检查

Dockerfile 的 runtime 阶段安装 ca-certificates 和 curl，且安装发生在 USER 10001:10001 之前：

```dockerfile
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates curl
```

当前使用阿里云 Alpine 镜像源，保留原仓库路径和版本。此调整与本阶段遇到的默认软件源访问失败有关，但不能仅凭构建成功证明此前网络异常的具体原因。

API 检查放在 Dockerfile 的启动 CMD 之前：

```dockerfile
USER 10001:10001

HEALTHCHECK --interval=30s --timeout=30s --start-period=5s --retries=3 CMD curl -fsS --max-time 2 http://127.0.0.1:8080/readyz || exit 1

CMD ["/app/cloudtask-api"]
```

注意：当前实际参数为 timeout=30s、start-period=5s；相邻注释写的是 start-period=2s，与实际配置不一致，仍待修正。curl 已将请求限制为 2 秒，Docker 的 timeout 可以考虑设为 3 秒；考虑 API 启动时还需初始化连接池、构建布隆过滤器，start-period 可先考虑 20 秒，再按实际启动耗时调整。以下仅为建议，本次整理没有修改 Dockerfile：

```dockerfile
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
    CMD curl -fsS --max-time 2 http://127.0.0.1:8080/readyz || exit 1
```

### 42.2 PostgreSQL 健康检查与 API 启动依赖

在 docker-compose.yml 的 db 下配置，与 environment、ports、volumes 同级：

```yaml
    healthcheck:
      test:
        - CMD-SHELL
        - 'pg_isready -h 127.0.0.1 -p 5432 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB" -t 2'
      interval: 5s
      timeout: 3s
      retries: 5
      start_period: 30s
```

API 使用包含健康检查的新镜像，并等待 db 健康：

```yaml
  api:
    image: cloudtask-api:2.3.3-health
    env_file:
      - .env.docker
    ports:
      - "127.0.0.1:8080:8080"
    depends_on:
      db:
        condition: service_healthy
```

## 43. 健康检查命令、网络与环境变量

### 43.1 curl 命令逐项解释

```sh
curl -fsS --max-time 2 http://127.0.0.1:8080/readyz || exit 1
```

| 部分 | 作用 |
| --- | --- |
| curl | 发起 HTTP 请求，默认使用 GET |
| -f | HTTP 400 及以上状态码使命令失败 |
| -s | 隐藏进度条与普通错误提示 |
| -S | 配合 -s，在失败时仍输出错误信息 |
| --max-time 2 | 整个请求最多运行 2 秒 |
| 127.0.0.1:8080/readyz | 访问当前 API 容器自身的就绪接口 |
| \|\| exit 1 | curl 失败时，统一返回退出码 1 |

HEALTHCHECK 后的 CMD 指定检查命令；Dockerfile 独立的 CMD 指定容器默认启动命令。不要把检查命令单独写成新的 Dockerfile CMD，否则会替换启动 API 的 CMD。

URL 应写为 http://，不需要在冒号后添加反斜杠。PowerShell 的 curl.exe -i 中，-i 表示同时显示 HTTP 响应头，和容器内健康检查中的 -f 作用不同。

### 43.2 /readyz 与 pg_isready 是两条检查路径

在 Windows 上执行：

```powershell
curl.exe -i http://127.0.0.1:8080/readyz
```

执行路径如下，不会调用 pg_isready：

```text
Windows curl.exe → 宿主机 8080 → 端口映射 → API 的 /readyz
    → pool.Ping(ctx) → PostgreSQL → HTTP 200 或 503
```

当前 readySys 复用启动时创建的连接池，给 Ping 设置 1 秒超时；成功返回 200/ready，失败返回 503/not_ready。/healthz 仅返回 HTTP 200，确认 HTTP 服务可以响应。/readyz 当前主要检查 PostgreSQL，不检查 Redis，也不检查业务表是否存在。

pg_isready 则由 Docker 按 db.healthcheck 定时触发，在 PostgreSQL 容器内部执行：

```text
Docker → db 容器的 /bin/sh -c → 展开环境变量 → pg_isready
    → 检查本容器 PostgreSQL 是否接受连接 → 返回退出码
```

pg_isready 由当前 PostgreSQL 官方镜像提供。参数 -U 指定用户名，-d 指定数据库，-h/-p 指定连接地址与端口，-t 2 表示连接检查最多等待 2 秒。

| pg_isready 退出码 | 含义 |
| --- | --- |
| 0 | 服务器正在接受连接 |
| 1 | 服务器正在拒绝连接，例如仍处于启动阶段 |
| 2 | 服务器没有响应 |
| 3 | 未发起检查，例如参数无效 |

pg_isready 不保证密码正确、业务表存在或业务 SQL 正常；用户名或数据库名错误也不一定影响它判断服务器正在接受连接。使用正确参数可避免无意义的连接失败日志。API 使用自己的连接配置执行 Ping，因此数据库 healthy 并不保证 API 一定 healthy。

### 43.3 为什么检查自身用 127.0.0.1，API 连接数据库用 db

每个容器都有自己的回环地址：

| 发起位置 | 访问地址 | 目标 |
| --- | --- | --- |
| PostgreSQL 容器内的 pg_isready | 127.0.0.1:5432 | 自己的 PostgreSQL |
| API 容器内的 curl | 127.0.0.1:8080 | 自己的 HTTP 服务 |
| API 的数据库连接 | db:5432 | Compose 网络中的 PostgreSQL 服务 |
| Windows curl.exe | 127.0.0.1:8080 | 宿主机发布端口，再映射至 API |

PostgreSQL 检查自身时，写 db 通常也能连通，但需要服务名解析并通过容器网络地址访问；127.0.0.1 更直接。API 容器里的 127.0.0.1 指向 API 自己，不能用它连接另一个数据库容器。

### 43.4 为什么 Compose 中使用 $$

```text
YAML 中 $$POSTGRES_USER
    → Compose 保留为字面量 $POSTGRES_USER
    → CMD-SHELL 启动的容器 shell 展开变量
    → 读取 db.environment 中的 POSTGRES_USER
```

直接写 $POSTGRES_USER 会让 Compose 尝试从宿主机环境或 .env 提前替换；YAML 外层单引号不能替代 $$。

在 day9 目录手动执行一次检查时，使用单个 $，因为命令直接交给容器 shell，不经过 Compose YAML 插值；PowerShell 外层单引号避免宿主机展开变量：

```powershell
docker compose exec db sh -c 'pg_isready -h 127.0.0.1 -p 5432 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t 2'
```

## 44. 阶段复习：状态、参数、依赖与恢复

### 44.1 Up 与 healthy 的区别

Up 表示容器正在运行、主进程尚未退出，是状态而非“启动”动作。healthy 表示配置的健康检查通过，能证明的范围取决于检查内容。Up (unhealthy) 完全可能：主进程还活着，但服务无法正常响应检查。

### 44.2 HEALTHCHECK 的工作原理与参数

Docker 定期在容器内部执行检查命令，根据命令退出结果及连续失败次数更新健康状态。初始状态是 starting；检查成功后变为 healthy；连续失败达到阈值后变为 unhealthy。unhealthy 后仍继续检查，后续一次成功即可恢复为 healthy。标准健康命令应返回 0 表示成功、1 表示失败，避免主动使用保留退出码 2；pg_isready 原生提供其自身的状态退出码。

| 参数 | 准确含义 |
| --- | --- |
| interval | 正常检查阶段，上次检查结束到下次检查开始的间隔 |
| timeout | 单次检查最长运行时间；超过后终止检查并记为失败 |
| retries | 标记 unhealthy 所需的连续失败次数，不是额外重试次数或总检查次数 |
| start_period | 启动宽限期，期间失败不计入连续失败次数 |

start_period 内仍会执行检查；一旦提前成功，后续失败就开始正常计数。成功会清零连续失败计数。例如 retries=3 时，“失败、失败、成功、失败”不会被视为连续失败三次。支持 start_interval 的版本还可单独指定宽限期内的检查间隔。

### 44.3 为什么 API 等待数据库健康

数据库容器启动并不代表 PostgreSQL 初始化结束、可以接受连接。当前 API 启动会创建连接池、Ping 数据库并读取数据，过早启动可能直接失败；等待数据库健康可减少启动时序问题，但不能替代数据库迁移。

| depends_on 条件 | 含义 |
| --- | --- |
| service_started | 依赖容器已启动，不等待健康检查通过 |
| service_healthy | 依赖容器的健康检查已通过 |

condition: service_healthy 是 Compose 内置的依赖条件名称，不需要定义同名函数。db.healthcheck 定义“如何检查”；Docker 执行检查并维护健康状态；Compose 根据该状态决定何时启动 API。未在 Compose 重复声明 API 的 healthcheck，也不影响 API 继承镜像中的检查。

### 44.4 unhealthy 是否自动重启；数据库宕机后能否保证恢复

普通 Docker/Compose 不会仅因 unhealthy 自动重启容器。健康检查负责记录状态；restart: unless-stopped 等重启策略主要在容器退出后决定是否重启。进程仍在运行但检查失败时，不会因此触发该重启策略。

depends_on 用于启动时的依赖等待，不会持续监控并在数据库每次故障时自动停止、等待和重启 API。数据库意外退出后，现有 db 的 unless-stopped 策略可以尝试重启数据库；API 能否恢复取决于连接池重新建连、超时及错误处理等机制。

```text
数据库故障 → API 数据库请求失败 → /readyz 返回 503
    → 连续检查失败，API 标记 unhealthy（主进程可能仍在运行）
数据库恢复 → 若连接池重新建连成功，/readyz 返回 200
    → 下次检查成功，API 恢复 healthy
```

这条路径是可能的恢复过程，不是本轮已完成的故障恢复测试。即使在 depends_on.db 下增加 restart: true，它主要响应显式 Compose 更新、重启依赖的操作，也不等于数据库意外退出并由运行时重启后会自动联动重启 API。健康状态本身也不自动保证流量被摘除。

## 45. Dockerfile 与 Compose 中健康检查的区别

| 对比 | Dockerfile HEALTHCHECK | Compose healthcheck |
| --- | --- | --- |
| 配置归属 | 镜像的默认配置 | 本次服务部署配置 |
| 生效范围 | 使用镜像创建的容器默认继承 | 使用该 Compose 创建的服务容器 |
| 修改后应用 | 重建镜像，再重建容器 | 重建对应服务容器；仅改检查配置无需重建镜像 |
| 适用场景 | 给自己维护的 API 提供通用默认检查 | 调整部署参数，或为 PostgreSQL、Redis 等第三方镜像配置检查 |

Compose 可以覆盖镜像的检查命令和参数，也可以用 healthcheck.disable: true 禁用。两者最终都在容器内部执行，不会同时成为两套独立检查；工具仍必须存在于镜像中，把 curl 命令移到 Compose 并不能省掉镜像内安装 curl 的步骤。

例如只调整 API 启动宽限期，其余继续继承镜像默认值：

```yaml
  api:
    image: cloudtask-api:2.3.3-health
    healthcheck:
      start_period: 60s
```

## 46. 本阶段故障排查记录

### 46.1 Compose 的 interval 缺少单位

曾写成 interval: 5，实际运行 docker compose config --quiet 报错：

```text
services.db.healthcheck.interval must be a string
```

已改为 interval: 5s。本次整理再次执行配置校验，退出码为 0。

### 46.2 docker build 未连接到 Docker 引擎

首次构建提示找不到 dockerDesktopLinuxEngine 命名管道，说明当时未连上 Docker Desktop 的 Linux 引擎。后一次日志已经进入构建流程，表明当时引擎连接问题已解决。

Windows 上使用 Docker Desktop 提供本地引擎时，需要先启动 Desktop 并等待引擎就绪；窗口可以最小化，但后台需运行。docker CLI 是客户端，构建由后台构建引擎完成；若连接远程引擎或 WSL 内独立运行的 Docker Engine，则不要求本机 Desktop 运行。docker info 能显示 Server 信息可用于检查连接状态。

### 46.3 Alpine 安装软件包时 I/O error

用户提供的第二次构建日志中，Go 编译已完成，失败步骤为 apk add --no-cache ca-certificates curl，最早的明确错误为：

```text
fetching https://dl-cdn.alpinelinux.org/alpine/v3.23/main/x86_64/APKINDEX.tar.gz: I/O error
```

可确定当时软件源索引下载失败，但日志不足以区分代理、DNS、网络波动或源站异常。后续依赖选择失败及 masked in: --no-network 不能单独证明用户手动禁用了 Docker 网络。镜像拉取成功也不能证明 apk 的目标域名可访问。

曾建议用独立容器缩小排查范围：

```powershell
docker run --rm alpine:3.23 sh -c 'apk add --no-cache ca-certificates curl'
```

普通容器测试与构建环境并非完全相同；若普通容器成功而构建持续失败，应继续比较构建代理和网络配置。当前 Dockerfile 已替换默认源为阿里云源，且新镜像现已存在；未保存该独立测试的执行结果，不能补写为已验证成功。

### 46.4 inspect 的 State.Health 为 null

当时实际检查发现：

| 对象 | 检查结果 |
| --- | --- |
| day9-api-1 | 使用旧 cloudtask-api:dev，Config.Healthcheck 与 State.Health 均为 null |
| cloudtask-api:2.3.3-health 镜像 | Config.Healthcheck 已包含 curl /readyz 命令 |

根因是新镜像已构建，但旧容器仍使用原镜像。null 表示没有启用健康检查，并非检查失败。仅重新构建镜像、移动标签或 docker restart 都不会让已有容器自动换用新镜像，需更新 Compose 的 image 并重建容器。当前文件与容器均已改为 2.3.3-health。

## 47. 常用命令与本轮验收边界

以下命令统一在 cloudtask-go 目录运行，避免混淆构建上下文和 Compose 文件位置：

```powershell
# 构建 API 镜像，最后的 . 为 cloudtask-go 构建上下文
docker build -f day9/Dockerfile -t cloudtask-api:2.3.3-health .

# 校验 Compose；无输出且退出码为 0 表示通过
docker compose -f day9/docker-compose.yml config --quiet

# 应用整个项目的配置，并按依赖条件启动
docker compose -f day9/docker-compose.yml up -d

# 若依赖已就绪，只重建 API；--no-deps 不负责启动或等待依赖
docker compose -f day9/docker-compose.yml up -d --no-deps --force-recreate api

# 查看容器状态与健康检查详情
docker compose -f day9/docker-compose.yml ps -a
docker inspect --format '{{json .State.Health}}' day9-api-1
docker inspect --format '{{json .State.Health}}' day9-db-1

# 检查镜像中是否包含健康检查
docker image inspect --format '{{json .Config.Healthcheck}}' cloudtask-api:2.3.3-health

# 从宿主机访问 API 的数据库就绪检查
curl.exe -i http://127.0.0.1:8080/readyz
```

PowerShell 不使用反斜杠作为续行符，命令末尾不要附加反斜杠。镜像标签需与 Compose 中的 image 一致。不要用 down -v 作为健康检查配置更新步骤，它会删除命名数据卷。

本次整理时通过只读命令取得以下状态快照：

| 对象 | 实际结果 | 能确认的范围 |
| --- | --- | --- |
| Compose 配置 | config --quiet 退出码 0 | 当前配置通过 Compose 校验 |
| day9-api-1 | image=cloudtask-api:2.3.3-health，running，healthy | API 容器使用新镜像，最近健康检查通过 |
| day9-db-1 | image=postgres:17，running，healthy | PostgreSQL 容器运行且检查通过 |
| day9-redis-1 | image=redis:8-alpine，exited，健康字段保留 unhealthy | Redis 当前已退出；健康字段不代表它仍在执行检查 |

因此可以确认 API 与 PostgreSQL 的健康检查已生效，不能据此宣称 Redis 或全部业务功能正常。本轮未调查 Redis 退出原因，也未执行数据库停机/恢复、业务表验证或完整业务回归；连接池故障恢复能力仍需单独验证。第 41 节为上一阶段历史快照，本节是本阶段新增状态，不抹去此前排错记录。

本次笔记整理仅修改本文，没有更改 Dockerfile、Compose、环境文件或业务代码，没有构建镜像、启停服务、执行数据库写入或提交推送 Git。

参考：[Docker HEALTHCHECK](https://docs.docker.com/reference/dockerfile/#healthcheck)、[Compose healthcheck](https://docs.docker.com/reference/compose-file/services/#healthcheck)、[Compose 启动依赖](https://docs.docker.com/compose/how-tos/startup-order/)、[pg_isready](https://www.postgresql.org/docs/17/app-pg-isready.html)、[构建与容器代理](https://docs.docker.com/engine/cli/proxy/)。

## 48. API 容器安全配置（2026-11-17）

本次在 `day9/docker-compose.yml` 的 `api` 服务下增加安全设置，保留原有 `env_file`、本机端口映射与数据库健康依赖。当前相关配置如下：

```yaml
  api:
    image: cloudtask-api:2.3.3-health
    build:
      context: ..
      dockerfile: day9/Dockerfile
    env_file:
      - .env.docker
    ports:
      - "127.0.0.1:8080:8080"
    depends_on:
      db:
        condition: service_healthy
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - "no-new-privileges:true"
    stop_grace_period: 20s
```

| 配置 | 作用 | 与本项目的关系 |
| --- | --- | --- |
| `read_only: true` | 将容器根文件系统设为只读 | API 日志写到标准输出，业务数据写入 PostgreSQL、Redis；当前未发现运行代码需要写本地文件 |
| `cap_drop: [ALL]` | 移除容器全部 Linux Capabilities | API 监听 8080、访问数据库和缓存不需要额外能力 |
| `security_opt` 中的 `no-new-privileges:true` | 阻止进程及其子进程通过执行程序获得新的权限 | 与 Dockerfile 的 `USER 10001:10001` 配合使用 |
| `stop_grace_period: 20s` | 停止时先发送终止信号，最多等待 20 秒后再强制结束 | 程序处理 SIGTERM，HTTP Shutdown 的超时为 10 秒，额外留出清理时间 |

只读根文件系统不等于数据库只读，也不会自动把额外挂载的卷设为只读。如果以后增加上传、落盘日志或临时文件功能，需要明确提供可写卷或 tmpfs；当前无需预先增加。

这次不引入自定义 seccomp、AppArmor 策略或额外容器安全工具。

### 为什么移除 Capabilities 有用

Linux Capabilities 将部分系统特权拆成独立能力，例如修改网络配置、挂载文件系统、改变文件属主等。普通 API 通常不需要这些能力，移除后可以减少程序误操作，以及被入侵后可利用的系统权限。

非 root 用户和移除能力分别约束不同的权限来源，可以同时使用。但这些配置不会阻止攻击者读取应用本来就能读取的环境变量，也不会阻止其使用应用已有的数据库凭据访问数据。

## 49. YAML 列表形式与本次格式错误

列表表示一个配置项下面有多个元素，即使只有一个元素，也仍然是列表。YAML 多行列表用短横线加空格表示元素：

```yaml
cap_drop:
  - ALL

security_opt:
  - "no-new-privileges:true"
```

下面的单行写法与上面等价：

```yaml
cap_drop: [ALL]
security_opt: ["no-new-privileges:true"]
```

原先曾写成：

```yaml
cap_drop: ALL
security_opt: no-new-privileges:true
```

这两个值都是字符串，而 Compose 要求这两个字段使用列表。当时实际校验报错：

```text
services.api.security_opt must be a array
```

`env_file`、`ports` 也是项目中已有的列表例子。列表内同级的短横线需要对齐，缩进使用空格，不使用 Tab。列表要求是字段的数据类型要求，不取决于目前填写了几个值。

## 50. 让 Compose 构建 API 镜像

只配置 `image` 时，Compose 知道使用哪个镜像，但不知道如何从本地源码构建。增加 `build` 后，将构建上下文与 Dockerfile 位置一并交给 Compose：

```yaml
build:
  context: ..
  dockerfile: day9/Dockerfile
```

路径分两步理解：

1. `context: ..` 相对于 Compose 文件所在的 `day9` 目录，指向模块根目录 `cloudtask-go`。
2. `dockerfile: day9/Dockerfile` 相对于构建上下文，指向已有 Dockerfile。

这样 Dockerfile 才能访问根目录的 `go.mod`、`go.sum` 和 `day9` 源码。根目录的 `.dockerignore` 会排除本地环境文件等不应进入构建上下文的内容。

统一在 `cloudtask-go` 目录运行：

```powershell
# 校验格式；无输出且退出码为 0 表示通过
docker compose -f day9/docker-compose.yml config --quiet

# 启动前构建，再应用服务配置
docker compose -f day9/docker-compose.yml up -d --build

# 查看容器状态及 API 日志
docker compose -f day9/docker-compose.yml ps
docker compose -f day9/docker-compose.yml logs --tail 100 api
```

`build` 是构建规则，`--build` 明确要求启动前执行构建。普通 `up -d` 不会自动监测源码变化；以后修改 Go 代码后，使用带 `--build` 的命令。构建会复用可用缓存，不等于每次从头下载、编译全部内容。

这仍是本地构建流程，不代表已经接入 CI/CD。以后可以由 CI 构建、测试并推送带版本或提交标识的镜像，部署环境再拉取运行。

## 51. 重新构建后，镜像、tag 与容器如何变化

`image: cloudtask-api:2.3.3-health` 同时指定本地构建产物的名称与标签。tag 是可移动的名字，并不是镜像内容永久不变的保证。

```text
构建前：cloudtask-api:2.3.3-health → 镜像 A
修改源码并重新构建后：cloudtask-api:2.3.3-health → 镜像 B
```

如果构建产生了不同的镜像，标签会指向新镜像。旧镜像不会立即自动删除；没有其他标签指向它时，可能显示为 `<none>`。若构建结果没有变化，也可能继续得到相同的镜像 ID，不能认为每次 `--build` 都一定产生不同镜像。

镜像标签更新不意味着已有容器立刻换用新镜像。`docker compose up -d --build` 包含后续应用步骤，检测到服务镜像变化后会重建对应容器。单独 `docker compose build api` 只构建镜像；`docker restart` 只重启原容器，都不能代替更新容器。

本地学习阶段可以复用同一个 tag；正式发布更适合使用独立版本号或提交标识，并约定不覆盖，以便追踪和回滚。

## 52. 多次构建会不会撑爆内存；如何管理空间

主要需要关注的是磁盘占用：镜像层和构建缓存保存在 Docker 的存储中。运行中的容器消耗内存，构建过程也会临时消耗内存，但旧镜像不会因为保存在磁盘上就一直占据同等大小的运行内存。

新旧镜像共享未变化的层，并非每次完整复制全部基础镜像。当前多阶段 Dockerfile 中，Go 编译器所在的构建阶段不会进入最终运行镜像，但其构建缓存仍可能占用磁盘。Compose 更新 API 时通常替换旧容器，不会每次构建就额外运行一个 API。

统一在 `cloudtask-go` 目录执行以下查看命令；清理操作按需手动执行：

```powershell
# 查看镜像、容器、卷和构建缓存的空间占用
docker system df

# 清理没有标签、且没有被容器引用的悬空镜像
docker image prune

# 清理未使用的构建缓存，之后首次重新构建可能变慢
docker builder prune
```

两条 prune 命令默认提示确认，不删除 PostgreSQL 命名数据卷；清理范围属于当前 Docker 引擎，可能涉及其他项目的未使用镜像或缓存，并非只针对本项目。日常不需要每次构建后都清理，空间不足时先查看占用再决定。不要把 `docker compose down -v` 当作镜像清理命令，它会删除项目命名数据卷。

## 53. 本轮核对结果与验收边界

2026-11-17 整理时，当前 Compose 已包含 API 的四项安全配置与 `build`，`cap_drop`、`security_opt` 均已改为列表，原有环境文件、端口映射和数据库健康依赖仍保留。

本轮实际执行 `docker compose -f day9/docker-compose.yml config --quiet`，无输出、退出码为 0，确认当前配置通过 Compose 校验。结合现有 Dockerfile 的非 root 用户、标准输出日志和 main.go 中的 SIGTERM/10 秒 Shutdown 逻辑，这些配置与当前设计相符。

配置校验不代表新配置已应用到运行中的容器。本轮仅整理本文并校验配置，没有执行镜像构建、容器重建、停机测试或清理操作；尚未验证只读根文件系统下的完整业务运行、安全选项的运行时生效情况，以及新构建镜像的健康状态。第 47 节中的容器状态属于之前的检查快照，不作为本轮运行结果。

后续应用时运行第 50 节的 `up -d --build`，再检查容器状态、日志和 `/readyz`，并验证核心业务。只修改 Compose 运行时安全配置时无需重建镜像，但需要重新创建 API 容器才能应用。

参考：[Compose 服务配置](https://docs.docker.com/reference/compose-file/services/)、[Compose 构建规则](https://docs.docker.com/reference/compose-file/build/)、[docker compose up](https://docs.docker.com/reference/cli/docker/compose/up/)、[Docker 磁盘占用](https://docs.docker.com/reference/cli/docker/system/df/)、[镜像清理](https://docs.docker.com/reference/cli/docker/image/prune/)、[构建缓存清理](https://docs.docker.com/reference/cli/docker/builder/prune/)。

## 54. 本阶段目标：为 CloudTask 引入基础 CI

本阶段在 `ci/github-actions` 分支上，为目标为 `main` 的 PR 和 `main` 上的 push 增加自动质量检查。现有分支已能承载这项工作，无需另建 `ci/actions-quality`。

本次范围：

- Workflow 名称为 `CloudTask Go CI`，Job 名称为 `Go quality checks`。
- 使用标准 GitHub 托管的 `ubuntu-latest` Runner。
- 先 checkout，再 setup-go；命令从包含 `go.mod` 的仓库根目录运行。
- 检查 `day9` 的格式、运行 `go vet`，编译 API 入口。
- 检查失败时让 Job 失败；只授予 `contents: read`，不配置真实数据库密码。

当前没有在工作流中执行完整测试、启动 API、构建或推送 Docker 镜像，也没有自动部署。

## 55. CI 与 CD：纠正“CI 不真正运行程序”的理解

CI（Continuous Integration，持续集成）围绕频繁提交和集成，通过自动构建、检查和测试尽早发现问题。它不限定必须在云端，也不限于部署前执行。

CI 可以实际运行程序：例如运行单元测试、启动 API、连接临时数据库并执行集成测试。本项目当前只配置静态检查和编译，是当前工作流的范围，不是 CI 本身的限制。

CD 常见的两种含义：

| 名称 | 含义 |
| --- | --- |
| Continuous Delivery：持续交付 | 自动完成验证和发布准备，正式部署可以保留人工批准 |
| Continuous Deployment：持续部署 | 改动通过验证后自动部署到生产环境 |

CloudTask 需要 CI，让每次 PR 接受同一套检查，减少遗漏格式问题、可疑代码和编译错误。CI 通过只代表已配置的检查通过，不保证全部业务正确，也不会自动禁止合并失败的 PR；强制检查通过后才能合并，需要额外设置分支规则。

## 56. Workflow、Job、Step、Runner 与 Action

| 概念 | 含义 | 本项目示例 |
| --- | --- | --- |
| Workflow | 由 YAML 定义的自动化流程，包含触发条件、权限及 Job | `CloudTask Go CI` |
| Job | 在 Runner 上执行的一组步骤 | `quality`，显示名为 `Go quality checks` |
| Step | Job 内的一个执行单元，可运行脚本或调用 Action | 格式检查、`go vet`、编译 |
| Runner | 接收并执行 Job 的运行机器及其代理程序 | `ubuntu-latest` |
| Action | 可以在步骤中调用的可复用工具 | `actions/checkout`、`actions/setup-go` |

同一 Job 内的步骤通常顺序执行。不同 Job 默认可以并行，通过 `needs` 建立依赖；Runner 执行的是 Job，不应把它理解成整个 Workflow 共用的唯一机器。GitHub 托管的普通 Job 通常各自使用新的环境。

```yaml
# 调用 Action，不是一条 Linux 命令
uses: actions/setup-go@v7

# 在当前 Runner 的 Shell 中执行命令
run: go vet ./day9/...
```

`actions/setup-go@v7` 表示引用 GitHub 上 `actions` 组织的 `setup-go` 仓库，使用 `v7` 版本标签。Action 也可以用提交 SHA 固定版本。

checkout 和 setup-go 是通过 Node.js 执行的 JavaScript Action，不局限于 Linux。自定义 Action 常见形式有 Composite、JavaScript、Docker；入口通常为 `action.yml` 或 `action.yaml`。公开仓库中的 Action 可以直接通过 `用户名/仓库名@版本` 引用，不必先上架 Marketplace。Workflow 是编排流程，Action 是流程中可复用的工具。

## 57. 开发分支、提交、推送和 PR 的完整关系

`ci/github-actions` 是一个完整分支名。`/` 是命名习惯，不代表 Git 中存在 `ci` 与 `github-actions` 两级父子分支，也不是必须使用的字符。

当前工作未完成时继续使用本分支；合并后有新的独立需求，再从更新后的 `main` 建分支，例如 `ci/add-integration-tests`，无需用 `github-actions2` 编号。

| 操作 | 作用 |
| --- | --- |
| `git add <路径>` | 将选定改动加入暂存区 |
| `git commit` | 将暂存内容保存到当前本地分支历史 |
| `git push` | 向远程上传已有提交，不自动上传未提交的文件 |
| 创建 PR | 请求将来源分支的改动合入目标分支，不会立即合并 |
| 合并 PR | 将改动正式合入目标分支 |

命令均从 `cloudtask-go` 运行，按实际改动选择文件，避免误提交压缩包和二进制产物：

```powershell
git branch --show-current
git status
git diff
git add .github/workflows/go-ci.yml
# 如有必要修正，再按路径添加相应 Go 文件和 .gitignore
git diff --cached
git commit -m "ci: add GitHub Actions workflow"
git push -u origin ci/github-actions
```

`origin` 是远程仓库别名；`-u` 为本地和远程分支建立上游跟踪关系，后续在该分支上通常直接 `git push`。分支名必须准确，`ci/github-action` 与 `ci/github-actions` 不同。

GitHub 创建 PR 时选择 base 为 `main`，compare/head 为 `ci/github-actions`。后续继续向这个开发分支 push，会更新同一个尚未合并的 PR，不需要重复创建。

补充 Git 命令理解：

- `git fetch origin`：获取远程提交并更新远程跟踪引用，不修改当前工作区或自动合并。
- `git switch main`：切换到本地 `main`；会按目标分支更新文件，遇到会覆盖的未提交改动通常拒绝切换。不要随意使用强制丢弃选项。
- `git pull --ff-only origin main`：可以更新本地分支和工作区；“只允许快进”不代表“不修改文件”。
- `git log -3 --oneline`：查看当前分支可追溯的最近 3 条提交，每条用短哈希和提交说明显示。
- 新建分支保留当前提交及工作区状态，但不等于备份未提交改动。

本次曾在 `main` 上提交测试调整。若类似情况再次出现，只有确认需要移动的是最新这一条、尚未推送到远程 main，并且新分支成功创建后，才可以在新分支上用 `git branch -f main HEAD~1` 回移本地 main 指针。这是特定条件下的处理方法，不是日常 CI 操作；已推送的 main 不应照搬回退。

## 58. push 与 pull_request 的触发条件

```yaml
on:
  push:
    branches:
      - main
  pull_request:
    branches:
      - main
```

- `push`：远程分支或标签被推送更新；本配置只选择 `main` 分支更新，PR 合并到 main 通常也会触发。
- `pull_request`：PR 创建、重新打开或来源分支新增提交等活动；这里筛选的是目标分支为 `main` 的 PR，并非要求来源分支叫 main。
- `pull_request` 不是 `git pull`。clone、fetch、pull 不会因此触发该 PR 事件。

按当前配置，只 push 开发分支但未创建 PR 时不会触发。创建目标为 main 的 PR 后运行检查，后续 push 更新该 PR 会再次检查。

## 59. 本轮工作流配置与关键参数

2026-11-18 本地文件核对版本如下，省略解释性注释。此版本已将 checkout 更新为 v7；不将此前截图中的成功运行视为这个更新版本的验证结果。

```yaml
name: CloudTask Go CI

on:
  push:
    branches:
      - main
  pull_request:
    branches:
      - main

permissions:
  contents: read

jobs:
  quality:
    name: Go quality checks
    runs-on: ubuntu-latest
    timeout-minutes: 15
    defaults:
      run:
        shell: bash
        working-directory: .
    steps:
      - name: Checkout Code
        uses: actions/checkout@v7
        with:
          persist-credentials: false
      - name: Set up Go
        uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - name: Check Go formatting
        run: |
          unformatted=$(gofmt -l ./day9)
          if [ -n "$unformatted" ]; then
            echo "以下文件需要执行 gofmt:"
            printf '%s\n' "$unformatted"
            exit 1
          fi
      - name: Run go vet
        run: go vet ./day9/...
      - name: Build 执行API
        run: go build -o "$RUNNER_TEMP/cloudtask-api" ./day9/cmd/server
```

文件位置必须是 `cloudtask-go/.github/workflows/go-ci.yml`，不能放在仓库父目录的 `.github` 下。工作目录 `.` 是 checkout 后包含 `go.mod` 的仓库根目录，不是 day9，也不是本机绝对路径。

checkout 准备源代码，setup-go 准备工具链。`go-version-file: go.mod` 让 CI 跟随项目声明，避免在 YAML 中另外随意维护版本；它不自动保证本地与 CI 的全部环境一致。本轮模块声明为 Go 1.26.0，早期 README 的 1.25.7 属于待统一说明。

`contents: read` 是 GITHUB_TOKEN 对 GitHub 资源的权限，不限制 Runner 本地文件写入；编译、缓存和下载依赖仍可进行。`persist-credentials: false` 避免为后续 Git 操作保留 checkout 认证凭据。当前检查不启动数据库或服务，因此无需真实数据库密码。

## 60. gofmt、go vet、go build 与失败传播

| 检查 | 能发现的问题 | 验证边界 |
| --- | --- | --- |
| `gofmt -l ./day9` | 文件格式与 Go 标准格式有差异 | 只列文件名，不验证业务逻辑 |
| `go vet ./day9/...` | 静态分析发现可疑用法 | 不证明全部运行路径正确 |
| `go build ... ./day9/cmd/server` | API 及其依赖的编译问题 | 不启动 API，不验证数据库或业务 |

`gofmt -l` 单纯发现格式差异时通常返回 0，因此仅执行命令可能出现“有输出但 CI 通过”。读取文件失败或语法错误则可能返回非零。

工作流中的 `$(...)` 捕获文件名，`[ -n "$unformatted" ]` 检查字符串非空，`exit 1` 让格式门禁失败。本地修复使用 `gofmt -w ./day9`，执行后检查差异再提交。曾误写为 `if [ -n "$unformatted"]; then`，右方括号前缺空格；该错误处于 if 条件中，不能假定 Shell 会自动让整个步骤失败，可能导致门禁失效。

本轮 go vet 曾报告两类问题：

1. 两个 task_cache.go 中共 4 处 `fmt.Errorf` 的返回值未使用。该函数创建 error，不自动打印；需要按调用约定 return、赋值或明确记录日志，不能机械改成统一处理方式。
2. TokenBucket_test.go 中共 5 处 `fmt.Println` 带 `%v`。Println 不解析占位符；可改用 `fmt.Printf("...%v\n", value)`、`fmt.Println("...", value)` 或测试中的 `t.Logf`。

当前 Bash 设置下，未被处理的命令失败通常使 Step 失败，后续普通 Step 跳过，Job 失败；但不等于整个 Workflow 立即终止：独立 Job 可继续，`if: always()` 步骤和相应 Post 收尾仍可能执行。`continue-on-error`、条件表达式等也会影响结果。质量门禁不应使用 `|| true` 隐藏失败。

本地编译示例：

```powershell
go vet ./day9/...
go build -o .\day9\cloudtask-ci.exe ./day9/cmd/server
Get-Item .\day9\cloudtask-ci.exe
```

go build 成功时通常没有输出。`-o` 指定的是输出文件，文件位于 day9，不是入口包目录；本次曾确认该 exe 已生成。Git 忽略规则可加入 `/day9/cloudtask-ci.exe`，保留本地文件但不提交。已暂存但尚未提交时，可用 `git restore --staged -- day9/cloudtask-ci.exe` 取消暂存；已被 Git 跟踪的文件不会仅靠 .gitignore 自动停止跟踪。

CI 中 `$RUNNER_TEMP/cloudtask-api` 是 Ubuntu Runner 的临时产物，不是 Windows exe，也没有自动上传成 Artifact。

## 61. 为什么暂不直接加入 go test ./...

原因不只是“测试没有全部写完”，而是部分测试还不能在干净环境中可靠、重复地运行：

- 部分测试连接真实 PostgreSQL、Redis，依赖本机配置。
- 查询固定任务 ID，临时空数据库未必有这些记录。
- 之前发现 TestRedisStop 在配置或数据库初始化失败时仅打印日志后 return，可能出现实际没验证却通过。
- 模块级 `./...` 会覆盖早期 dayN 练习，超出本阶段 day9 的检查范围。

后续先接入不依赖外部服务的单元测试，再分离集成测试，为它们创建临时服务、初始化结构、准备数据和清理资源。集成测试启用后，必要依赖初始化失败应明确使测试失败；若需要验证布隆过滤器，Redis 环境还应具备相应模块。

不需要等所有测试完善后才使用 CI，但不能把未经准备的测试全量运行或编译成功当作业务验证完成。

## 62. Checks 为 0 的排查与处理

PR 最初显示 `Checks 0`，没有任何运行记录。本地排查发现：

```text
?? .github/
```

这表示工作流目录未跟踪。尽管已有提交说明写着添加 CI，提交树中实际没有该文件，GitHub 无法运行本地未提交的工作流。

处理顺序：确认位于仓库内 `.github/workflows/` → 修复 Bash 空格问题 → git add → 核对 `git diff --cached` → commit → push。文件提交到现有 PR 的来源分支后，无需新建 PR，更新会触发检查。

排查时先区分“没有运行记录”和“已经运行但失败”。前者先看路径、是否进入提交、是否 push、触发条件；后者进入具体 Step 日志查看命令与错误。

## 63. 成功运行、Annotations 和两个 Post 步骤

用户提供的 GitHub 截图显示 `Go quality checks` 成功，耗时 49 秒，各步骤均显示成功状态：

| Step | 含义 |
| --- | --- |
| Set up job | 准备 Runner 环境和 Action |
| Checkout Code | 检出本次检查对应的代码 |
| Set up Go | 配置 Go 工具链和缓存恢复等 |
| Check Go formatting | 执行自定义格式检查脚本 |
| Run go vet | 执行静态分析 |
| Build 执行API | 编译 API，不启动 API |
| Post Set up Go | 条件满足时处理或保存 Go 缓存等收尾 |
| Post Checkout Code | 按配置清理 Git 认证或临时配置等 |
| Complete job | 完成 Job 收尾 |

Post 是 Action 注册的执行后收尾逻辑，不是 HTTP POST，也不是重复 checkout 或重新运行检查。通常逆序收尾：先 Checkout，再 Set up Go，最后 Post Set up Go、Post Checkout。具体缓存命中或保存情况需要展开日志，不能仅凭步骤名确定。

截图还有 1 warning 和 1 notice：

- Warning：checkout@v4 原本使用 Node.js 20，平台提示该运行时弃用并强制使用 Node.js 24。这是执行 Action 的运行时，与 Go API 的依赖无关，无需给 Go 项目安装 Node.js。本地工作流后来已改为 checkout@v7；该改动是否消除警告需要新一轮运行确认。
- Notice：ubuntu-latest 将迁移到 Ubuntu 26。latest 是随平台变化的标签，不是固定系统版本；本次提示没有使 Job 失败。

这次成功记录证明配置中的步骤报告成功，不证明 API 已实际运行、Docker 镜像已构建或所有测试通过。还应有意引入格式或编译错误，再修复并观察 CI 由失败恢复成功，验证门禁确实有效；本轮未确认已完成这个演练。

## 64. API 镜像与 Actions 费用问答

### 64.1 API 镜像与 Go 程序的关系

API 镜像将构建时的 Go API 二进制、Alpine 基础文件系统、证书和 curl，以及启动用户、健康检查和命令等配置打包。多阶段构建先用 Go 工具链编译，再把产物复制到运行阶段，最终运行镜像不包含完整 Go 编译工具链。

PostgreSQL、Redis 是 Compose 中另外的容器，不在 API 镜像里。修改本地源码不会更新已有镜像或容器，需要重新构建并更新容器。当前 CI 直接编译 API，不等于重新构建 Docker 镜像。

### 64.2 当前学习配置的费用边界

按 2026-11-18 会话查阅的 GitHub 官方计费说明：公开仓库使用标准 GitHub 托管 Runner 的计算时间免费；私有仓库先使用套餐包含的计算额度，超出后按费率计费。大型 Runner 是公开仓库免费规则的例外；自托管 Runner 的机器成本自行承担。

费用区分计算与存储：计算累计各 Job 的实际使用，失败和重跑也消耗资源；工件和缓存有不同额度及保留时间规则。当前没有上传 Artifact，临时编译文件不等于长期工件；setup-go 默认启用缓存。

具体套餐额度、费率及预算规则会变化，使用时查看官方说明和账户账单。上次会话中的价格示例不是固定长期承诺，也未核对账户实际账单或仓库可见性。

## 65. 本轮整理结果、待验证事项与参考

截至 2026-11-18：

- 已使用 `ci/github-actions` 分支提交改动，用户已创建目标为 main 的 PR。
- 已定位并处理“工作流未进入提交导致 Checks 0”的问题，后续用户截图显示基础 CI Job 成功。
- 本次整理时，本地工作流包含修正后的 Bash 条件和 checkout@v7、setup-go@v7；git status 显示工作流仍有未提交修改，不能视为该版本已经 push 或在 GitHub 验证通过。
- 本轮仅整理笔记、读取配置和核对 Git 差异，没有替用户提交、push、合并 PR，也没有运行新的 CI、测试或部署。

后续应核对差异并提交需要的 Action 升级，push 后观察最新提交的 Checks；展开日志确认 Go 版本、格式检查、vet、编译及警告状态。完成一次失败与修复演练后，再决定合并 PR，并继续建设可靠测试和 Docker 构建检查。

参考资料：

- [GitHub Actions 基本概念](https://docs.github.com/en/actions/get-started/understand-github-actions)
- [Workflow 语法、事件筛选、权限和步骤行为](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax)
- [Go 项目的构建和测试](https://docs.github.com/en/actions/tutorials/build-and-test-code/go)
- [actions/checkout](https://github.com/actions/checkout)
- [actions/setup-go](https://github.com/actions/setup-go)
- [gofmt 文档](https://pkg.go.dev/cmd/gofmt)
- [GitHub Actions 计费](https://docs.github.com/zh/billing/concepts/product-billing/github-actions)

## 66. Config 单元测试：先准备，再调用，最后断言

测试文件为 `day9/internal/config/config_test.go`。这组测试直接调用 Go 函数，使用虚构连接串，无需启动 PostgreSQL 或 Redis。断言是比较实际结果和预期结果，不符合就用 `t.Errorf` 或 `t.Fatalf` 报告失败。前者记录失败并继续，后者结束当前测试或子测试。

| 函数 | 作用 |
| --- | --- |
| `validConfig` | 创建合法配置，供每个校验场景独立修改，也提供默认值测试的预期结果 |
| `TestValidate` | 覆盖合法输入、空 DSN、连接数边界、负数 Redis DB、非正数启动超时 |
| `prepareLoadEnv` | 用 `t.Chdir(t.TempDir())` 避开本地 `day9/.env`，再用 `t.Setenv` 控制配置输入 |
| `TestLoadDefaults` | 验证非必需环境变量为空时使用默认值 |
| `TestLoadValidEnv` | 验证字符串输入能转换成目标字段，例如 `"1500ms"` 转成 `time.Duration` |
| `TestLoadInvalidEnv` | 验证无效时间、连接数溢出和不合法取值等会报错 |

表驱动测试把各场景放在切片里，共用执行和断言逻辑。字段 `change func(*Config)` 保存修改函数，`tt.change(&cfg)` 才执行修改。每个场景只改变需要验证的输入，避免先被其他校验条件拦住。

`t.Run(name, fn)` 创建具名子测试，能逐项看结果。曾经把循环直接写在 `TestValidate` 里，第一条正常配置通过后执行 `return`，导致后面的场景没跑。放到 `t.Run` 的匿名函数里后，`return` 只结束当前子测试。`t.Helper()` 则让测试框架报告错误位置时跳过辅助函数。工作目录和环境变量是进程级状态，因此使用 `t.Chdir`、`t.Setenv` 的测试及其祖先不要调用 `t.Parallel()`。

## 67. Token Bucket：用指定时间测试补充令牌

`TestTokenBu` 创建速率为每秒 10 个令牌、突发容量为 3 的桶，依次断言初始 3 个令牌可取、同一时刻再次取令牌被拒绝、模拟推进 150ms 后可以取到一个令牌。`AllowN(now.Add(...), n)` 将指定时间传给限流器，测试无需真的等待。

`if !limiter.AllowN(...) { t.Fatal(...) }` 断言应当放行；`if limiter.AllowN(...) { t.Fatal(...) }` 断言应当拒绝。`t.Fatal` 都位于实际结果偏离预期的分支里。这组测试验证令牌桶行为。业务构造器的容量是 5，且业务限流作用在缓存未命中后的数据库回源路径。

## 68. Singleflight：控制重叠窗口，验证一次回调

同一个 `Group` 中，同 key 且执行时间重叠的调用共享一次工作。回调结束后再次请求同 key，可以重新执行。`Shared` 表示这次结果是否有多个调用者收到，首个调用者也可能得到 `true`。

原 `TestSingleflightSameKey` 用开始 Channel 放行 goroutine、用 WaitGroup 等待结束，并用 `Sleep(100ms)` 给后续调用注册留时间。连续 50 次未观察到失败，但开始信号只能保证调用可以继续，不能保证它们已经进入 `Do`。加长 Sleep 仍然依赖调度时机。

`TestSingleflightSameKeyDoChan` 用 `release` 控制完成时机。先注册 20 次同 key 调用，将每次返回的只接收 Channel 存入切片，再关闭 `release`。然后从每个 Channel 接收一次并检查数据、错误和 `Shared`，最后断言回调计数为 1。回调是模拟回源，真实数据库查询次数需另外在 Service 层验证。

曾把 `close(release)` 和结果收集放进注册循环，导致只注册了调用 0，它的结果未被共享。其余位置仍为 nil Channel，读取调用 1 时超时。修复后，注册和接收是两个独立循环。结果 Channel 由 `DoChan` 返回且不会被关闭，接收一次即可。

```powershell
go test -count=20 -run '^TestSingleflightSameKey$' ./day9/internal/service
go test -count=100 -timeout=60s -run '^TestSingleflightSameKeyDoChan$' ./day9/internal/service
```

`-count` 是重复执行测试的次数，和每轮测试里的调用数相互独立。`-run` 两端的锚点用于精确匹配测试名。

## 69. Build Tags 与手动集成测试

`//go:build` 放在文件头，与 `package` 之间留一个空行。`// go:build` 在斜杠后多了空格，Go 会把它当普通注释。标签控制整个文件是否参与构建。启用标签时，符合条件的无标签测试也会参与。

在提交 `00d20c7` 中，`task_test.go` 和 `Redis_stop_situation_test.go` 使用 `//go:build integration`。整理笔记时，这两个文件的本地未提交版本已改为 `//go:build integration && manual`，因此本地版本需要同时传入 `-tags=integration,manual`。

| 用例 | PostgreSQL | Redis |
| --- | --- | --- |
| `TestConcurrentCacheMiss` | 需要任务 ID 12 | 必须可用 |
| `TestRandomTTL` | 需要任务 ID 11–61 | 本机 `127.0.0.1:6379` 必须可用 |
| `TestRedisStop` | 需要任务 ID 11–61 | 允许不可用，以观察回源 |

必要的配置或连接失败用 `t.Fatalf` 结束测试，避免只打印日志并返回造成假通过。工作 goroutine 中可用 `t.Errorf` 再返回。`TestRedisStop` 目前允许 Redis 连接失败，但没有主动制造故障，因此在 Redis 正常时通过无法证明故障回源路径已执行。

```powershell
go test -count=1 ./day9/...
go test '-tags=integration,manual' ./day9/internal/service -list '^Test(ConcurrentCacheMiss|RandomTTL|RedisStop)$'
```

第二条只编译并列出用例，实际运行前需要准备测试依赖和数据。

## 70. CI 新增单元测试与 Race Detector

```yaml
- name: Run Go unit tests
  run: go test -count=1 ./day9/...

- name: Run Go tests with race detector
  run: go test -race -count=1 ./day9/...
```

`./day9/` 只选根包，可能显示 `[no test files]` 并成功退出；`./day9/...` 会覆盖子包。两个步骤都没有启用集成测试标签。竞态检测会在测试执行路径上找出未同步的并发读写，发现时报告 `WARNING: DATA RACE` 并让步骤失败。

`setup-go@v7` 默认启用依赖和构建缓存。项目未设置 `cache-dependency-path`，缓存键使用根目录 `go.mod` 的哈希作为一部分。在相同配置的前后两次 CI 运行中，看 `Set up Go` 是否恢复缓存，若未命中则看成功运行的 `Post Set up Go` 是否保存。用日志确认缓存命中，运行时长只能作为参考。测试命令的 `-count=1` 关闭测试结果复用，仍可使用依赖和编译缓存。

## 71. 从 push 到 Checks：按 PR 状态和提交号排查

`git add` 暂存文件当时的内容，`git commit` 将暂存内容记录到本地分支，`git push` 上传已有提交。若暂存后又编辑同一文件，需要再次暂存才能提交最新版本。VS Code 的 GitHub Actions 扩展可以检查 YAML 和查看日志，实际运行由 GitHub Runner 执行。

工作流只监听 `main` 的 push 和目标为 `main` 的 PR。PR #1 合并后，再向 `ci/github-actions` 推送 `15bae2c`，当时没有打开的 PR 接收该提交，因此需要新建 PR #2。PR #2 保持打开时，推送后续提交会触发该 PR 的 `synchronize` 事件。同一分支是否首次推送、这次是否修改工作流 YAML，都不是这里的判断条件。

PR #2 的第 #3 次运行因 `TokenBucket_test.go` 未格式化在 gofmt 步骤失败，后续检查被跳过。将文件格式化并提交为 `00d20c7` 后，已核对远程分支和 PR #2 的 head 均指向这个提交，用户随后确认新运行成功。旧运行的详情页仍显示当时的失败记录。排查时返回 PR 的 Checks 或仓库 Actions 列表，按最新提交号找对应运行。

## 72. 合并前确认改动范围和验证范围

- 最新 PR 检查应对应当前提交。用户已确认格式化修复后的新运行成功，缓存是否命中仍需按第 70 节独立查看日志。
- 本地运行 `go test -count=1 ./...` 时，`day9` 通过；全仓库测试因已有的 `leetcode/alpha_diff_position.go` 缺少 `package` 声明而失败。当前 CI 使用 `./day9/...`，所以该问题可以单独处理。
- CI 分支差异还包含新增的 `leetcode/3无重复字符的最长子串.cpp`。它与上面的 Go 报错是两件事。合并前在 `Files changed` 确认是否有意一并提交；若误带，移除后再查看新的 Checks。

参考：[Go 构建约束](https://pkg.go.dev/cmd/go#hdr-Build_constraints)、[Singleflight](https://pkg.go.dev/golang.org/x/sync/singleflight)、[setup-go 缓存说明](https://github.com/actions/setup-go#caching-dependency-files-and-build-outputs)、[GitHub Actions 触发事件](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request)。

## 73. PostgreSQL 集成测试：验证真实 SQL 和事务

测试位于 `internal/repository/task_test.go`，通过 `TEST_DATABASE_URL` 接入独立测试库。辅助函数 `newRepository(t)` 读取连接串，用 `config.PostgresConfig{...}` 构造配置，并复用 `database.NewDBPool` 创建测试自己的连接池。`Postgres` 是 `Config` 的字段名，它的类型是 `config.PostgresConfig`。连接或准备数据失败应立即用 `t.Fatalf` 结束测试。

每个用例创建自己的任务，使用 PostgreSQL 返回的 ID。检查事件时，可以用 `pool.QueryRow` 按 `task_id` 和 `event_type` 直接查 `task_events`。SQL 占位符写 `$1`、`$2`，曾误写为 `&2`，编译 Go 测试时发现不了这个 SQL 错误。

| 用例 | 预期行为 |
| --- | --- |
| `TestCreateAndGetTask` | 能按创建返回的 ID 查回任务，标题正确，初始状态为 `pending`，版本为 1 |
| `TestChangeStatusWritesEvent` | 状态更新，版本加 1，新增一条 `status_changed` 事件 |
| `TestChangeStatusFailRollback` | 事件写入失败，任务状态、版本、更新时间保持原样，事件数为 0 |
| `TestChangeStatusOptimisticConflict` | 用当前版本成功更新一次，再用旧版本更新时返回 `ErrVersionConflict`，第一次结果和事件都保留 |

回滚用例采用一次更新。先创建任务作为基线，再给测试库的 `task_events` 加一条仅拒绝本次任务 `status_changed` 的命名 `CHECK` 约束。用合法状态调用 `ChangeStatus`，让事务先执行任务更新，再在事件插入处失败。通过 `errors.As` 取到 `*pgconn.PgError`，核对 SQLSTATE `23514` 和约束名，随后查询数据库验证回滚。测试结束时移除约束、删除本次任务并关闭连接池。清理用独立超时 Context，且应在资源取得后尽早注册。

两次更新适合乐观锁冲突用例。第一次调用的 `expectedVersion` 传任务的 `Version` 字段，第二次复用更新前的旧版本，然后用 `errors.Is` 匹配错误。结果与第一次提交后的状态比较，事件数应保持为 1。回滚用例无需先成功更新一次，否则已有事件可能让新增约束时的校验先失败。

## 74. Redis 集成测试：区分 Miss、空值命中和任务命中

`internal/cache/task_cache_integration_test.go` 用 `newRedis(t)` 读取 `TEST_REDIS_ADDR` 及可选的 `TEST_REDIS_USERNAME`、`TEST_REDIS_PASSWORD`。客户端创建后用 `Ping` 确认可达和认证有效，通过 `t.Cleanup` 关闭连接。

| 场景 | 操作和断言 |
| --- | --- |
| 缓存 Miss | 先清掉测试 key，再调用 `GetResult`，断言无错误且 `Hit=false` |
| 写入后读回 | 在测试中构造 `model.Task`，用同一个任务 ID 写入和读取，检查命中及任务字段 |
| 空值缓存 | `SetNotFound` 后读取，断言 `Hit=true`、`Exists=false` |
| 删除缓存 | 先写入并确认命中，调用 `DeleteTask` 后确认 Miss |
| TTL 范围 | 调用 `SetEntry` 后用 Redis `PTTL` 读取实际剩余时间，确认大于 0 且在预期范围内 |

`GetResult` 应先判断错误，再看 `Hit` 和 `Exists`。Miss 只说明缓存里没有答案。空值命中表示缓存已记录“任务不存在”。正值命中返回任务。TTL 写入配置是 `[10s, 70s)`，剩余时间在读回前会减少，测试下限要考虑这段耗时与毫秒精度。一次读回只验证 TTL 范围。共享测试 Redis 时，各用例应使用独立的 key，写入后清理。

## 75. Service 集成测试：观察 Cache Aside 的实际行为

`TestTaskService_CacheAside` 位于 `internal/service/task_integration_test.go`，同时依赖 PostgreSQL 和 Redis。准备阶段可通过 Repository 创建任务和清空缓存，待测的读取与更新通过 `TaskService` 完成。

1. 确认初始 Miss，调用 `TaskService.GetTaskByID`，核对返回值和 Redis 回填。
2. 只在数据库中改动测试任务的标题，保留 Redis 的原值。再次经 Service 查询，返回旧标题即可区分缓存命中和查询数据库。
3. 调用 `TaskService.UpdateTask`，确认旧缓存消失。再次通过 Service 读取，核对新标题、状态、版本及 Redis 回填。

曾在测试里直接调用 Repository 查询和更新，并用 `SetEntry` 手工写缓存。这样的做法会替 Service 完成待验证的工作。直接读 Redis 验证结果、用 SQL 构造可区分的测试数据，属于测试准备和断言。清理阶段只删除本用例创建的任务和缓存 key。

## 76. 布隆过滤器与 Cache Aside 的关系

启动时从 PostgreSQL 读取任务 ID，用 `BF.RESERVE` 创建过滤器，再用 `BF.MADD` 分批装入。全部成功后才标记可信。创建任务时，数据库写入成功后用 `BF.ADD` 加入新 ID。加入失败时将过滤器标为不可信，后续查询仍可走普通缓存与数据库路径。

查询先看 `BF.EXISTS`。只有返回否定、无错误且过滤器可信时，Service 才直接返回 `ErrTaskNotFound`。肯定结果表示可能存在，继续检查任务详情缓存。Cache Aside 用例构造 Service 时传入 `nil`，因此“布隆过滤器不可用”是预期日志，本用例只验证缓存路径。

## 77. 本地 Docker 测试环境与 Windows 端口排查

在 `cloudtask-go` 根目录用 PowerShell 执行。测试程序连接宿主机的 `55432` 和 `16379`，容器内部仍使用 PostgreSQL 的 `5432` 和 Redis 的 `6379`。

```powershell
docker run --rm -d --name cloudtask-test-pg -e POSTGRES_USER=ct_test -e POSTGRES_PASSWORD=only_for_tests -e POSTGRES_DB=cloudtask_test -p 127.0.0.1:55432:5432 postgres:17
docker run --rm -d --name cloudtask-test-redis -p 127.0.0.1:16379:6379 redis:8-alpine

docker exec cloudtask-test-pg pg_isready -h 127.0.0.1 -U ct_test -d cloudtask_test
docker exec cloudtask-test-redis redis-cli ping

Get-Content -Raw ./day9/db/migrations/000001_init.up.sql |
    docker exec -i -e PGPASSWORD=only_for_tests cloudtask-test-pg psql -h 127.0.0.1 -U ct_test -d cloudtask_test -v ON_ERROR_STOP=1

$env:TEST_DATABASE_URL = 'postgres://ct_test:only_for_tests@127.0.0.1:55432/cloudtask_test?sslmode=disable'
$env:TEST_REDIS_ADDR = '127.0.0.1:16379'
$env:TEST_REDIS_USERNAME = ''
$env:TEST_REDIS_PASSWORD = ''
```

确认 PostgreSQL 接受连接且 Redis 返回 `PONG` 后执行迁移。每个新建 PostgreSQL 容器执行一次迁移，并在设置环境变量的同一个 PowerShell 窗口运行测试。本地端口、用户名和密码可以与 CI 不同，各环境的变量只需对应各自的容器。

曾尝试将 Redis 映射到 `56379`，Docker 创建容器后在绑定端口时失败。用 `netsh interface ipv4 show excludedportrange protocol=tcp` 确认该端口落在 Windows 保留的 `56328–56427` 范围。改用 `16379`，同时更新 `TEST_REDIS_ADDR`。测完可用 `docker stop cloudtask-test-pg cloudtask-test-redis` 停止测试服务，`--rm` 会在停止后删除容器。

## 78. 测试标签与编辑器排查

`//go:build integration` 放在文件顶部，并在其后留空行。`// go:build integration` 只是普通注释，拼成 `integation` 则无法匹配命令中的标签。普通 `go test` 排除集成用例，`-tags=integration` 同时纳入普通与集成用例，旧的 `integration && manual` 用例需要再开启 `manual`。

VS Code 里的 Go 补全和 Ctrl+点击依赖 `gopls`。在包含 `go.mod` 的工作区打开代码，按需把 `"gopls": {"buildFlags": ["-tags=integration"]}` 加入工作区设置，再重启语言服务器。终端可用 `go test -tags=integration -run '^$' ./day9/internal/repository` 检查编译，命令不会执行测试。多行函数参数末尾缺逗号、`t.cleanup` 大小写错误等语法或类型问题也要分别处理。

工作流 YAML 的补全由 GitHub Actions 扩展提供。在 `.github/workflows/go-ci.yml` 中检查右下角语言模式是否为 `GitHub Actions Workflow`，用 Ctrl+Space 在 YAML 字段位置触发建议。扩展补全工作流字段和表达式，Bash 命令由 Shell 执行。

## 79. GitHub Actions 集成测试 Job

`go-ci.yml` 新增与 `quality` 同级的 `integration`。它使用 `needs: quality`，格式检查、单元测试及竞态检测成功后才运行。集成 Job 自己 checkout 和 setup-go，并通过 `services` 启动带健康检查的 PostgreSQL 17 和 Redis 8。当前映射到 Runner 的 `5432`、`6379`，使用临时测试账号。

- **Initialize PostgreSQL schema** 通过 `job.services.postgres.id` 定位容器，用 `docker exec -i` 在容器里运行 `psql`。Runner 从仓库读取迁移 SQL 并通过标准输入传入，`ON_ERROR_STOP=1` 让 SQL 出错时立即失败。
- **Run Go integration tests** 在 Runner 上设置 `TEST_DATABASE_URL`、`TEST_REDIS_ADDR`，带 `integration` 标签执行下节命令。这里的环回地址指 Runner，因此要匹配服务端口映射。

## 80. 本地验证记录与开发心得

2026-11-21 的本地终端日志显示，以下命令选中的 **13 个测试全部通过**。其中 Repository 4 个、Redis Cache 5 个、Service 4 个（包含 3 个普通测试和 1 个 Cache Aside 集成测试）。

```powershell
go test -v -count=1 -tags=integration ./day9/internal/repository ./day9/internal/cache ./day9/internal/service
```

Cache Aside 日志出现两次数据库回源，分别对应初始 Miss 和更新使缓存失效后的再次查询。两次回源之间的读取命中旧缓存。以上是本地真实服务运行记录。GitHub Actions 上的集成 Job 仍需在推送后查看建表与测试日志。

开发时将测试与行为一起交付。新增查询先测读回，加入事务再测成功提交和关键回滚，加入乐观锁时再测旧版本冲突。测试先准备数据，再调用待测入口，最后比较预期和实际。预期发生的业务错误也是通过条件，只有观察到的结果偏离预期才用 `t.Errorf` 或 `t.Fatalf` 报告失败。

## 81. 把 CloudTask 镜像构建加入 CI（2026-11-21）

提交 `0eaf8e0` 在 [go-ci.yml](../.github/workflows/go-ci.yml) 中新增了 `docker-build` Job。它声明 `needs: [quality, integration]`，等 Go 质量检查和集成测试都通过后，再验证仓库中的 [Dockerfile](Dockerfile) 能否把这次提交打包成镜像。

**为什么还要写一次 `runs-on` 和 checkout？** GitHub 托管的 `ubuntu-latest` 按 Job 分配临时 Runner。即使三个 Job 选了同一个标签，也是各自拿到一台干净的机器。`needs` 建立执行依赖，前一个 Job 的工作目录不会自动传过来，所以 `docker-build` 要重新检出源码。集成测试的 PostgreSQL、Redis 服务容器也随其 Job 结束而清理。同一 Job 内的各个 Step 则共享工作目录和本地 Docker 镜像，后面的 Step 可以直接使用刚构建的 `cloudtask-api:ci`。

| Step | 在这一步看什么 |
| --- | --- |
| Checkout code | 取得本次构建的源码、模块文件和 Dockerfile。 |
| Check Docker environment | `docker version` 能输出客户端和服务端信息，说明 Runner 可以连接 Docker 引擎。 |
| Build image | 从仓库根目录构建，并给本地镜像打上 `cloudtask-api:ci` 标签。 |
| Inspect image | 打印镜像配置，预期看到用户 `10001:10001`、工作目录 `/app` 和启动命令 `["/app/cloudtask-api"]`。这一步供查看，命令本身没有比较字段值。 |
| Container smoke test | 真正启动一个临时容器，用 `test` 断言实际 UID、GID、程序文件类型和执行权限。 |

## 82. 构建上下文与 COPY 的两个路径

在包含 `go.mod` 的 `cloudtask-go` 根目录执行下面这条命令，本地和 CI 使用相同的路径关系。

```powershell
docker build -f day9/Dockerfile -t cloudtask-api:ci .
```

`-f day9/Dockerfile` 选择构建指令文件，末尾的 `.` 选择构建上下文。对于这次本地目录构建，可以把上下文理解为从当前目录提供给构建器的一组文件，并受 `.dockerignore` 的排除规则约束。普通 `COPY` 的源路径从上下文根目录查找，目标路径在当前镜像阶段内解析。

| Dockerfile 中的例子 | 源在哪里 | 目标在哪里 |
| --- | --- | --- |
| `COPY go.mod go.sum ./` | 仓库根目录的两个文件 | `builder` 的 `WORKDIR /cloudtask`，得到 `/cloudtask/go.mod` 和 `/cloudtask/go.sum` |
| `COPY day9 /cloudtask/day9/` | 上下文中的 `day9` 目录内容 | `builder` 中的绝对路径 `/cloudtask/day9/` |
| `COPY --from=builder … /out/cloudtask-api /app/cloudtask-api` | `builder` 文件系统中的编译产物 | `runtime` 文件系统中的 `/app/cloudtask-api` |

如果先 `cd day9` 再执行 `docker build .`，上下文就缩小到了 `day9`，根目录的 `go.mod` 和 `go.sum` 随之落到上下文之外。这解释了为什么命令要同时保留 `-f day9/Dockerfile` 和最后的 `.`。

## 83. 两个构建阶段与 Go 编译参数

第一阶段从 `golang:1.26` 开始，下载模块、复制源码，然后把 API 编译为 `/out/cloudtask-api`。第二阶段从 `alpine:3.23` 重新开始，只从前一阶段复制这一个程序，再配上运行时需要的证书和健康检查用的 `curl`。最终镜像省去了 Go 工具链、源码和模块下载缓存，这才是多阶段构建能够减小体积的原因。

| 编译设置 | 在 CloudTask 中的作用 |
| --- | --- |
| `CGO_ENABLED=0` | 编译时禁用 cgo。当前项目可以用纯 Go 构建，便于把二进制复制到精简的运行镜像中。若以后加入必须通过 cgo 调用 C 库的依赖，需要重新评估。 |
| `GOOS=linux` | 指定目标操作系统为 Linux，对应最终的 Alpine 容器。目标 CPU 架构由 `GOARCH` 控制。 |

## 84. 读懂构建缓存与跨 Runner 复用

**看日志怎么判断？** BuildKit 的某一步出现 `CACHED`，表示复用了该步已有的结果。在本地给上述构建命令加上 `--progress=plain`，连续执行两次，就可以逐步对照。Docker 会匹配指令、前序构建结果和该步输入。对 `COPY` 来说，被复制文件的内容或权限变化都可能让缓存失效。

假设基础镜像相同、缓存仍在，当前 Dockerfile 的指令顺序会带来两个典型结果。

- **只改 `day9` 下的一行 Go 代码。** 模块文件没变，前面的 `go mod download` 可以命中缓存。从 `COPY day9 …` 开始，复制源码和随后的编译重新执行。
- **修改 `go.mod` 的依赖版本。** 前面的 `COPY go.mod go.sum ./` 输入就变了，依赖下载和后续编译都需要重跑。

**Runner 用完即毁，缓存还能保留吗？** 可以把缓存放到 Runner 外面。第 81 节的 `docker-build` 直接执行 `docker build`，这个 Job 没有配置跨次构建的 Docker 缓存。`setup-go` 管理的 Go 缓存和 Docker 镜像构建缓存各有各的用途。

后续新增的 `publish` 已采用 Buildx，在 `docker/build-push-action` 中用 `cache-to: type=gha,mode=max` 导出构建缓存，再用 `cache-from: type=gha` 读取。前面的 `docker-build` 仍用原命令在自己的 Runner 上构建和验证。若改用 Buildx 并要在同一 Job 用本地 `docker run` 验证单平台镜像，可以设置 `load: true`。[Docker 官方缓存示例](https://docs.docker.com/build/ci/github-actions/cache/)

## 85. 构建成功、冒烟测试通过和 API 就绪分别说明什么

`docker build` 成功说明编译和镜像组装完成。`CMD ["/app/cloudtask-api"]` 在构建时写入镜像配置，真正执行要等到容器按默认命令启动。

本次 smoke test 使用 `docker run --rm --entrypoint /bin/sh`，由容器内的 Shell 依次执行检查。`id` 和 `ls -l` 打印诊断信息，`test` 检查 UID、GID 均为 `10001`，且 `/app/cloudtask-api` 是普通文件并具有执行权限。`/bin/sh -ec` 会在这里任一断言失败时以非零状态退出，CI Step 随之失败。`--rm` 在退出后清理容器。

**为什么检查非 root？** CloudTask 只需要读取并执行程序。镜像将程序设为 `root:root` 所有、权限 `0555`，再用 `USER 10001:10001` 运行。应用用户可以启动程序，但不能直接改写该文件。以后若误删 `USER` 指令，UID 断言就能发现默认用户变成了 root。

**这次测试覆盖到哪一层？** 它验证了容器用户和文件权限。因为启动的是 Shell，API 进程、数据库连接和 HTTP 请求都没有在这一步运行。要验证服务就绪，需要提供启动配置与测试依赖，按默认命令启动 API，再请求 `/readyz`。

## 86. 健康检查与镜像发布

| 概念 | 用 CloudTask 理解 |
| --- | --- |
| Docker `HEALTHCHECK` | Docker 按镜像里的指令，在容器内用 `curl` 访问 `127.0.0.1:8080/readyz`，根据结果记录容器健康状态，可通过 `docker inspect` 查看。 |
| Kubernetes `readinessProbe` | 将来部署时，可以在 Pod 配置中显式指定同一个 `/readyz`。探测持续失败达到阈值时，Pod 变为未就绪，常规 Service 暂停向它分发新流量；就绪恢复后再接入。单独就绪探测失败不会重启容器。 |

**为什么 PR 的构建只标记 `cloudtask-api:ci`？** `docker build -t` 给本地镜像命名，方便后续检查。生产仓库的 `latest` 是一个可移动的标签。若每个 PR 都向它推送，最后完成的构建就可能让 `latest` 指向尚未合并的代码。这个 Job 只做验证。第 89 节的发布 Job 使用分支、版本和提交 SHA 标签，配置为跳过 `latest`。

## 87. 从本地镜像到 GHCR Package

![Docker 架构示意图：Client 发出 build、pull、run 请求，Host 上的 Daemon 管理镜像和容器，并从 Registry 拉取镜像。](docs/images/docker-client-host-registry.png)

从上往下读图：在 PowerShell 输入命令的是 Docker Client，处理请求的是 Docker Host 上的 Daemon。蓝线 `build` 对应 CI 根据 Dockerfile 构建本地镜像 `cloudtask-api:ci`；绿线 `pull` 对应验收时从 GHCR 拉取 `cloudtask-api:2.3.4`；红线 `run` 对应冒烟测试用镜像创建并启动容器。发布 Job 的 `push` 与绿线的镜像传输方向相反：构建结果从 Runner 上传到 GHCR。

以 `ghcr.io/im-sage/cloudtask-api:2.3.4` 为例，`ghcr.io` 是提供存储和分发服务的 Registry，`im-sage/cloudtask-api` 是其中的镜像仓库，对应 GitHub Packages 页面的一个容器 Package，`2.3.4` 是选择镜像版本的标签。源代码仓库 `Im-Sage/cloudtask-go` 保存 Go 代码、Dockerfile 和 workflow。把 Package 与源码仓库关联，方便从镜像找到源码，并管理仓库工作流对包的访问。

| 操作 | 去哪里看结果 |
| --- | --- |
| `docker context show` 输出 `desktop-linux` | Windows 上的 Docker CLI 选中了 Docker Desktop 的 Linux 引擎。`docker version` 的 Client 和 Server 分别描述客户端与引擎，Go version 指编译相应程序时用的 Go 版本。 |
| `docker image inspect cloudtask-api:2.3.3-health` | 读取当前引擎保存的本地镜像元数据。检查 ID、OS、架构和启动容器做冒烟测试分属不同步骤，后者见第 85 节。 |
| `docker tag` 给镜像换一个名字 | 曾把 `ghcr.io` 拼成 `ghrc.io`。用正确名称再打标签后，本地两个名字可以对应同一个 IMAGE ID；旧标签仍在。 |
| `docker push ghcr.io/im-sage/cloudtask-api:版本` | 把指定本地标签指向的镜像发布到 GHCR。同一 Package 下可以有多个版本，多个标签也可以指向同一份内容。 |

[本地 Compose](docker-compose.yml) 的 API 保留 `build.context: ..` 和 `dockerfile: day9/Dockerfile`，可以从本地源码构建。`docker compose up --build` 明确要求启动前构建。Actions 的 `integration.services` 自行启动 PostgreSQL 和 Redis，镜像构建步骤直接使用 Dockerfile；发布成功也不会自动更新正在运行的 Compose 容器。

## 88. 哪些事件检查代码，哪些事件发布镜像

工作流在 `on` 中选择事件，在 Job 的 `if` 中决定是否发布。`github.event_name` 表示事件类型；对 push，`github.ref` 表示本次更新的完整 Git 引用。当前发布条件和事件规则可在 [go-ci.yml](../.github/workflows/go-ci.yml) 对照。

| 操作 | GitHub 收到什么 | 预期行为 |
| --- | --- | --- |
| 推送 `cd/ghcr-publish`，尚无开放 PR | 开发分支 push | 没有命中 `push.branches: [main]`。 |
| 创建或更新以 `main` 为目标的 PR | `pull_request`，打开的 PR 使用类似 `refs/pull/6/merge` 的引用 | 运行 CI，跳过 `publish`。此处 `branches: [main]` 匹配 PR 的目标分支。 |
| 直接推送到 `main`，或在 GitHub 合并 PR 至 `main` | `push`，`refs/heads/main` | 前置检查通过后发布 `:main` 和提交 SHA 标签。 |
| 推送 Git 标签 `v2.3.4` | `push`，`refs/tags/v2.3.4` | 前置检查通过后发布版本标签和提交 SHA 标签。仅在本地创建标签不会触发。 |

```yaml
if: github.event_name == 'push' && (github.ref == 'refs/heads/main' || startsWith(github.ref, 'refs/tags/v'))
```

一次运行只有一个 `github.ref`，所以分支和标签之间用 `||`。即使它们指向同一提交，推送分支和推送标签也是两次不同的引用更新。这个条件写在 Job 层，控制整个 `publish`。它允许任意匹配的版本标签；本次通过显式选择 `origin/main` 来确定发布哪份源码。

## 89. 发布 Job 的五个步骤与标签规则

`publish` 使用 `ubuntu-latest`，依赖 `quality`、`integration` 和 `docker-build`。顶层只给 `contents: read`；发布 Job 自己显式声明 `contents: read` 和 `packages: write`。Job 一旦声明自己的权限表，未列出的权限设为 `none`。`packages` 要写在 `permissions` 下，直接写在 `docker-build` 下会导致工作流校验失败。

| Step | 在本项目里做了什么 |
| --- | --- |
| Checkout Code | 在发布 Runner 上检出触发事件对应的源码。`persist-credentials: false` 使后续 Git 命令无法复用 checkout 留下的凭据；稍后可独立登录 GHCR。 |
| Login to GHCR | 用 `github.actor` 和本次 Job 的 `GITHUB_TOKEN` 登录 `ghcr.io`。本地 PowerShell 的 Docker 登录状态不会带到 Runner。 |
| Set up Docker Buildx | 准备构建器。前面 `docker-build` 使用 Docker 自带的默认构建器，在自己的 Runner 运行 `docker run` 冒烟检查。`needs` 只传递执行依赖，发布 Job 会从同一提交重新构建。 |
| Extract Docker Metadata | `images` 提供基础名 `ghcr.io/im-sage/cloudtask-api`。这一步根据 Git 事件算出完整镜像标签，并生成源码 revision 等标签元数据。`id: meta` 供下一步读取输出。 |
| Build and Push | 从仓库根目录 `.` 取上下文，用 `day9/Dockerfile` 构建 `linux/amd64` 镜像。读取 `steps.meta.outputs.tags` 和 `steps.meta.outputs.labels`，通过 `push: true` 上传。 |

曾把 `cache-from` 和 `cache-to` 放在 setup 步骤。两者都是构建时参数，应放在 `docker/build-push-action` 的 `with` 下。提交 `11b9f02` 将缓存、目标平台和以下标签规则一起落地。缓存是否命中看构建日志中的 `CACHED`。

| 规则 | 以提交 `fae7a8d` 为例的输出 |
| --- | --- |
| `type=ref,event=branch` | main 推送生成 `main`。 |
| `type=ref,event=tag` | Git 标签 `v2.3.4` 生成同名镜像标签 `v2.3.4`。 |
| `type=semver,pattern={{version}}` | 同一个 Git 标签额外生成 `2.3.4`。 |
| `type=sha,prefix=sha-,format=short` | 两种发布事件均生成 `sha-fae7a8d`。 |

本项目显式设置 `flavor: latest=false`。发布时使用版本号便于选版和回滚，SHA 标签方便按提交查找；需要固定镜像内容时记录 Registry 返回的 digest，用 `镜像名@sha256:…` 引用。[Docker 镜像标签规则](https://github.com/docker/metadata-action#tags-input)

## 90. 两次权限问题与一次 Go 语法错误

**本地登录返回 `denied`。** `Read-Host "请输入 GitHub PAT" -AsSecureString` 的引号里是提示语，提示出现后才粘贴令牌。把令牌写在提示语里会明文显示；一旦暴露，应在 GitHub 撤销并换新。更简单的手动验证是在 PowerShell 运行 `docker login ghcr.io --username Im-Sage`，在 `Password:` 处输入有 `write:packages` 权限的 PAT (classic)。这次交互登录观察到了 `Login Succeeded`。此前管道登录失败的具体原因未确定；即使更新了 `$secureToken`，已转换出的 `$plainToken` 也需要重新赋值。个人令牌无需写入 workflow。[GHCR 认证说明](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#authenticating-to-the-container-registry)

**Actions 推送 `:main` 返回 `permission_denied: read_package`。** 先在失败运行的 `Set up job → GITHUB_TOKEN Permissions` 核对实际的 `Packages: write`，再到 `cloudtask-api → Package settings → Manage Actions access` 核对 `Im-Sage/cloudtask-go` 是否有 Write。此前若用个人令牌手动创建过同名包，个人有权推送并不自动授予仓库 workflow 写权限。若仅调整了 Package 权限，可重跑失败的 Job；给 workflow 重复添加 read 权限无法解决包侧授权。实际授权变更和重跑结果需以 GitHub 记录为准。[GitHub Package 权限](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility#ensuring-workflow-access-to-your-package)

**PR #5 在格式检查阶段连续报五处语法错误。** 第一条指向 `TestCreateAndGetTask`，最后一条是 `expected '}', found 'EOF'`。根因在前一个函数 `newRepository`：`pgxpool.ParseConfig` 的 `if err != nil` 少了结束花括号，后续四个测试函数都被当成还在函数体内。修复提交 `f3fa1b2` 同时把预期库名从 `cloudtask-test` 改为工作流实际创建的 `cloudtask_test`。[Repository 测试辅助函数](internal/repository/task_test.go)

`TEST_DATABASE_URL` 在 CI 的集成测试 Step 启动 `go test` 前由 `env` 注入。本地则在运行测试的 PowerShell 会话中赋值（见第 77 节）。Repository 测试先检查空串，再用 `parsed.ConnConfig.Database` 比较库名，匹配后才建连接池。复查语法和格式可在仓库根目录运行 `gofmt -l ./day9`。

## 91. 本地 main、origin/main 和 GitHub 的 main

在 `cd/ghcr-publish` 上执行 `git push -u origin cd/ghcr-publish` 后，本地分支跟踪远端同名分支。后续普通 `git push` 输出 `cd/ghcr-publish -> cd/ghcr-publish`；PR 合并后才会更新远端 main。以下三个书签的位置按本次 `git fetch origin --tags` 前后整理。

| 位置 | PR #6 在 GitHub 合并后、本地 fetch 前 | fetch 后 |
| --- | --- | --- |
| GitHub 上的 `main` | `fae7a8d` | `fae7a8d` |
| 电脑上的 `origin/main`（上次同步看到的位置） | `49364ae` | **`fae7a8d`** |
| 电脑上的 `main`（可切换并开发的分支） | `49364ae` | `49364ae` |

终端里的 `49364ae..fae7a8d  main -> origin/main` 表示远端信息已更新到本地记录。fetch 不会切换工作分支。此时用 `origin/main` 指定发布目标即可；若还想更新本地 main，可以切到 main 后执行 `git merge --ff-only origin/main`。

## 92. 正式版本 v2.3.4 的发布与验收

Git 标签固定这次选中的源码提交，GHCR 镜像标签用于拉取构建产物。两者通过第 89 节的 metadata 规则对应。下面按仓库根目录下的 PowerShell 操作编排。

**选定提交。** 先运行 `git fetch origin --tags` 和 `git log -1 --oneline origin/main`。本次选中 PR #6 的合并提交 `fae7a8d`。本地现已有附注标签 `v2.3.4`，可以直接运行 `git rev-list -n 1 v2.3.4` 核对。下列第一条是当时这类发布使用的创建命令，已有标签时跳过。

```powershell
git tag -a v2.3.4 origin/main -m "Release v2.3.4"
git push origin refs/tags/v2.3.4
```

**检查远端。** `git ls-remote --tags origin refs/tags/v2.3.4` 能查到远端标签是否存在。随后在 Actions 中选择由 `v2.3.4` 推送触发的运行，核对四个 Job 及 Build and Push 日志中的完整镜像名和 digest。即使 main 推送与标签推送使用同一个 SHA，两次运行生成的镜像标签也不同。

**拉取并比对。** 发布成功后执行下列命令。Git 的完整提交号应与镜像的 `org.opencontainers.image.revision` 标签相同；`docker pull` 返回的 digest 应与版本发布日志中的镜像摘要相符。

```powershell
$image = 'ghcr.io/im-sage/cloudtask-api:v2.3.4'
docker pull $image
$info = docker image inspect $image | ConvertFrom-Json
$info[0].Config.Labels.'org.opencontainers.image.revision'
git rev-list -n 1 v2.3.4
```

**11 月 24 日验收结果。** 本机镜像 `ghcr.io/im-sage/cloudtask-api:v2.3.4` 的 revision 与 `git rev-list -n 1 v2.3.4` 都输出 `fae7a8d3c30993cb1c49e1ebc07912064f4c64d1`。GHCR 的同一版本页面列出 `v2.3.4`、`2.3.4`、`sha-fae7a8d` 三个标签。至此已确认镜像发布到了 GHCR，且本地镜像记录的源码提交与发布标签一致。

| 遇到的问题 | 应该比较哪两个值 |
| --- | --- |
| 镜像由哪份源码构建？ | 镜像的 `org.opencontainers.image.revision` 与 Git 标签指向的提交 SHA。页面上的 `sha-fae7a8d` 是用该提交号前 7 位生成的镜像标签。 |
| GHCR 页面为什么显示另一个 SHA？ | 页面上的 `Digest: sha256:14baf56f…` 标识镜像清单或索引的内容。可执行 `docker buildx imagetools inspect $image`，将输出顶部的完整 `Digest` 与页面摘要比较。 |

## 93. Compose 部署覆盖与配置检查

[基础配置](docker-compose.yml) 定义 API、PostgreSQL 和 Redis，本地开发时 API 可以用仓库里的 Dockerfile 构建。[部署覆盖文件](docker-compose.deploy.yml) 只改 API 的两个字段。两个文件合并后，API 使用 GHCR 中选定的版本，数据库、端口、健康检查和数据卷等设置沿用基础配置。

```yaml
services:
  api:
    image: ghcr.io/im-sage/cloudtask-api:${IMAGE_TAG:?必须设置 IMAGE_TAG}
    build: !reset null
```

`!reset null` 清除基础配置中的 `build`。`${IMAGE_TAG:?...}` 要求版本值非空，避免忘记选版本时继续部署。在 **cloudtask-go 仓库根目录的 PowerShell** 中，把三组选项保存成数组，后续命令就能使用同一份配置：

```powershell
$dc = @(
    '--env-file', 'day9/.env.docker',
    '-f', 'day9/docker-compose.yml',
    '-f', 'day9/docker-compose.deploy.yml'
)
docker compose @dc config --images
```

赋值只创建数组。`@dc` 在调用命令时展开参数，Compose 按两个 `-f` 的顺序合并配置。`--env-file` 提供 YAML 插值所需的变量；基础配置中 `api.env_file` 则把文件中的变量传给 API 容器。这两处恰好用了同一个文件。

本次实际运行 `config --images` 列出 `ghcr.io/im-sage/cloudtask-api:v2.3.4`、`postgres:17` 和 `redis:8-alpine`。这验证了部署覆盖和版本选择。该命令只解析配置；查看正在运行的版本要用 `docker compose @dc ps`。

## 94. 拉取镜像、启动容器与验证服务

| 操作顺序 | 命令 | 检查什么 |
| --- | --- | --- |
| 1. 检查本机 GHCR 凭据 | `docker login ghcr.io` | 显示使用已保存凭据且随后出现 `Login Succeeded`，说明该凭据有效。若提示重新输入，可用 GitHub 用户名和 PAT (classic) 登录；拉取私有镜像需要相应的包读取权限。公开镜像允许匿名拉取。 |
| 2. 拉取选定版本 | `docker compose @dc pull api` | `api` 是服务名。Docker 检查其 GHCR 镜像并下载缺少的层；正在运行的容器继续使用原版本。 |
| 3. 应用部署配置 | `docker compose @dc up -d` | 启动所定义的服务，并按需要重建容器。数据库和 Redis 已就绪、只想更新 API 时可用 `docker compose @dc up -d --no-deps api`。 |
| 4. 核对运行结果 | `docker compose @dc ps` 和 `curl.exe -f http://127.0.0.1:8080/readyz` | 在 `ps` 中看 API 镜像和容器状态，随后验证就绪接口；涉及业务改动时再验证相应请求。 |

遇到“已改版本但仍运行旧容器”，依次检查上述三个位置：`config --images` 中的目标版本、本机已经拉取的镜像、`ps` 中正在运行的容器。这里记录的是部署验收方法；第 92 节的源码核对不能代替 HTTP 就绪检查。

## 95. PostgreSQL 的表为何保留，API 如何回滚

部署时没有手动执行迁移，却仍能看到原有表。本次检查到数据库容器 `day9-db-1` 将命名卷 **`day9_postgres_data`** 挂载至 **`/var/lib/postgresql/data`**。表、索引和数据都在这个卷上。覆盖文件只调整 API，即使数据库容器重建，挂回原卷也能读到旧表。可以用下面两条只读命令分别确认挂载和表结构：

```powershell
docker inspect (docker compose @dc ps -q db) --format '{{json .Mounts}}'
docker compose @dc exec db psql -U app -d cloudtask -c '\d tasks'
```

**判断迁移是否生效，要看本次迁移预期改变的对象。** 例如升级脚本为 `tasks` 增加一列，就核对列名和类型；看到 `tasks` 本身只能说明这张表已经存在。当前部署配置没有执行迁移的步骤。

**生产回滚可以由人决定、由流水线执行。** 小规模部署也可以由人手动切回旧版本；支持渐进式发布的平台可以根据健康检查或错误率阈值自动停止放量、切回稳定版。当前 Compose 配置没有自动切换版本的规则。

CloudTask 的手动回滚方法：将 `day9/.env.docker` 中的 `IMAGE_TAG` 改成 GHCR 已发布、验证过的旧标签，执行 `config --images` 确认目标，再按第 94 节拉取和更新 API，检查 `/readyz` 及受影响的业务请求。若当前 PowerShell 设置了 `$env:IMAGE_TAG`，它会优先于文件值。**回滚 API 保留数据库当前结构**，若升级已删除旧版所需的列，切回旧镜像仍可能报错，应先核对旧版与现有 schema 的兼容性。本轮没有记录实际回滚演练结果。

## 96. 分开维护宿主机与 Docker 的环境变量模板

宿主机运行 Go 程序时，`.env.example` 用 `localhost` 或 `127.0.0.1` 访问映射出来的端口。Compose 网络里，API 应使用服务名 `db:5432` 和 `redis:6379`；部署覆盖文件还需要 `IMAGE_TAG`。因此单独维护 `.env.docker.example`，能让新环境按正确的地址和版本启动。模板只放占位值，实际凭据写入本机的 `.env.docker`。

**本次文件状态。** 本地已有 `.env.docker.example`，其中有 `DATABASE_URL`、`REDIS_ADDR`、`REDIS_PASSWORD` 和 `IMAGE_TAG`；尚未显式列出 `POSTGRES_PASSWORD`。首次初始化新库时，建议在模板中写出这个变量，并让连接串使用相应密码。已有数据卷的数据库角色密码不会因为修改环境文件而自动更新。

当前 `day9/.gitignore` 的 `.env.*` 同时忽略实际配置和 Docker 模板，只单独放行了 `.env.example`。若要让同学克隆后能拿到 Docker 模板，可在忽略规则后增加 `!.env.docker.example`。用 `git check-ignore -v day9/.env.docker.example day9/.env.docker` 核对规则，再用 `git status --short --untracked-files=all -- day9/.env.docker.example` 确认可提交的是示例文件。
