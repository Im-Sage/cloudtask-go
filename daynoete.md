# CloudTask Go 学习项目

本仓库按 `dayN` 目录保存 Go 练习。当前的 `day9` 是基于 Go、Gin、pgxpool 和 PostgreSQL 的任务管理 API，包含 CRUD、事务事件记录、悲观锁及乐观锁实验；`day8` 是 Worker Pool 并发练习。

## day9：任务管理 API

- [运行与数据库初始化指南](day9/README.md)：环境准备、Docker Compose、已有数据库升级、启动及接口验证。
- [接口文档](day9/docs/api.md)：全部路由、参数、响应及版本冲突行为。
- [架构与事务流程图](day9/docs/architecture.md)：分层架构、数据库关系和并发更新时序。

从当前模块根目录运行：

```powershell
docker compose -f day9/docker-compose.yml up -d db
docker compose -f day9/docker-compose.yml exec db pg_isready -U app -d cloudtask
go run ./day9/cmd/server
```

第二条命令应显示数据库接受连接后再启动服务。首次启动会自动执行初始化 SQL；已有数据卷不会重新执行初始化脚本，具体处理见运行指南。

API 地址为 `http://localhost:8080`。独立终端中可访问：

```powershell
Invoke-RestMethod http://localhost:8080/tasks
```

当前只有 `PATCH /tasks/opm/:id` 检查版本冲突。普通 PUT 和悲观锁 PATCH 保留用于学习对比，不能将项目描述为“全部更新接口均已完成版本保护”。

## day8：Worker Pool

提交者通过 jobs Channel 发送任务，3 个 Worker 并发处理任务，再通过 results Channel 返回结果。以下保留 day8 的运行与测试说明。

## 环境与目录

- Go：使用 `go.mod` 声明的 Go 1.25.7。
- `day8/main.go`：创建 Channel、启动 Worker、提交任务、收集结果，并设置 800 毫秒超时。
- `day8/pool.go`：定义 `Job`、`Result` 和 Worker 的处理及取消逻辑。
- `day8/pool_test.go`：验证正常任务、模拟错误和 10 个任务的结果收集。

下面的命令都从仓库根目录 `cloudtask-go` 执行。如果 PowerShell 当前位于仓库的父目录，先进入该目录：

```powershell
cd .\cloudtask-go
```

## 运行 Worker Pool

```powershell
go run ./day8
```

程序尝试提交 10 个任务，但演示程序设置了 **800 毫秒超时**；除 Job 1 的模拟错误外，普通任务各模拟处理 500 毫秒。因此这次运行通常只会输出部分任务的结果，随后打印提交者或 Worker 收到取消信号的消息，并正常退出。各 Worker 并发运行，日志顺序和取消前完成的任务数可能变化。

Job 1 被特意设为失败，用来展示 `Result.Error` 的处理；它应只产生一份错误结果。

## 运行测试

```powershell
go test -v ./day8
```

现有测试覆盖：

- 一个正常任务返回正确结果。
- Job 1 返回错误结果。
- 3 个 Worker 处理 10 个任务，并收集每个任务的结果。

测试使用自己的 Context 期限，**不会沿用 `main.go` 的 800 毫秒超时**。目前尚无专门断言“处理中的任务因超时退出”或“手动调用 `cancel()` 后退出”的自动化测试；当前演示程序展示的是超时取消，手动取消需要另写测试或在代码中触发 `cancel()`。

## 检查数据竞争

```powershell
go test -race ./...
go run -race ./day8
```

第一条命令运行整个模块的现有测试并启用 Race Detector；第二条命令检查 `day8` 演示程序实际走到的超时路径。正常情况下不应出现 `WARNING: DATA RACE`。Race Detector 只能检查本次运行实际执行到的代码路径，一次通过不代表所有可能的并发路径都已验证。

## Channel 的关闭顺序

提交者退出时关闭 `jobs`。提交者和所有 Worker 都调用 `wg.Done()` 后，另一个 goroutine 才关闭 `results`。主 goroutine 持续用 `range results` 接收结果，直到结果通道关闭；这样 Worker 在发送无缓冲结果时始终有接收方。
