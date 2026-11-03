# CloudTask 接口文档

本文描述当前代码行为，包含用于对比实验的普通更新、悲观锁和乐观锁入口，不将计划功能视为已实现功能。

## 约定与路由

- 基础地址：`http://localhost:8080`。
- 请求体：JSON，设置 Content-Type: application/json。
- 当前没有登录、Token 或权限校验。
- 路径 id 必须是 int64 范围内的正整数。
- 状态枚举：pending、in_progress、completed。

| 方法 | 路径 | 功能 | 成功状态 |
| --- | --- | --- | --- |
| POST | /tasks | 创建任务 | 201 |
| GET | /tasks/:id | 查询任务 | 200 |
| GET | /tasks | 按状态分页查询 | 200 |
| PUT | /tasks/:id | 更新标题和状态 | 200 |
| PATCH | /tasks/:id | 悲观锁更新状态 | 200 |
| PATCH | /tasks/opm/:id | 乐观锁更新状态 | 200 |
| DELETE | /tasks/:id | 删除任务和关联事件 | 200 |

没有 task_events 的 HTTP 查询接口，事件暂通过 SQL 检查。

## 任务响应结构

创建、GET、列表中的任务，以及乐观锁成功响应的 data 使用以下结构：

```json
{
  "id": 10,
  "title": "学习 PostgreSQL",
  "description": "验证任务版本保护",
  "status": "pending",
  "version": 1,
  "created_at": "2026-11-03T10:00:00Z",
  "updated_at": "2026-11-03T10:00:00Z"
}
```

时间为 Go time.Time 的 JSON 字符串。示例仅展示格式；数据库列目前是 TIMESTAMP，不携带时区语义，不应凭示例的 Z 推断业务时间已经统一为 UTC。priority 虽存在于表中，但当前 API 模型不暴露它。

## 1. 创建任务

`POST /tasks`

```json
{
  "title": "学习 PostgreSQL",
  "description": "验证任务版本保护"
}
```

| 字段 | 校验 |
| --- | --- |
| title | 必填，最大 200；Service 拒绝纯空白标题 |
| description | 当前绑定要求非空字符串；不限制其最大长度 |

状态默认 pending，版本默认 1。请求中的 status 不用于创建状态。

成功：201，直接返回任务对象，不包装 data。不写创建事件。

失败：400 INVALID_REQUEST 或 INVALID_TITLE；数据库等内部错误为 500。

## 2. 查询单个任务

`GET /tasks/:id`

成功：200，返回任务对象，包含当前 version。

失败：非法 id 返回 400 INVALID_ID；不存在返回 404 TASK_NOT_FOUND。

## 3. 分页列表

`GET /tasks?status=pending&limit=10&offset=0`

| 参数 | 默认值 | 范围 |
| --- | --- | --- |
| status | pending | 三个合法状态之一 |
| limit | 10 | 整数 1～100 |
| offset | 0 | 大于等于 0 的整数 |

结果按 id DESC 排序，返回 JSON 数组；无结果为 `[]`。不返回总数或页码，也没有不按状态过滤的选项。

失败：400 INVALID_LIMIT、INVALID_OFFSET 或 INVALID_STATUS。offset=0 是合法值，当前错误文案“正整数”不准确。

## 4. 普通更新标题与状态

`PUT /tasks/:id`

```json
{
  "title": "新的标题",
  "status": "in_progress"
}
```

两个字段均必填；标题最大 200 且不能全为空白，状态必须属于枚举。description 不在该接口更新范围内。

成功：200，任务对象。事务内更新任务并插入 task_updated 事件，真实数据库版本加 1。

**当前限制：不接收或检查 expected_version；虽然递增了数据库版本，但 RETURNING/Scan 未包含 version，响应中的 version 为 Go 零值 0。需要再 GET 才能得到真实版本，不能用该响应版本继续乐观锁更新。**

失败：400 INVALID_REQUEST、INVALID_ID、INVALID_TITLE 或 INVALID_STATUS；404 TASK_NOT_FOUND；其他错误 500。

## 5. 悲观锁更新状态

`PATCH /tasks/:id`

```json
{
  "status": "in_progress",
  "expected_version": 1
}
```

expected_version 由于复用请求结构，绑定要求为正整数；**业务实际未比较它**，因此旧版本也可能更新成功。status 不在绑定层设置 required，但 Service 会拒绝不属于合法枚举的状态。

实现使用 SELECT ... FOR UPDATE 读取旧状态。若状态不同，更新状态、递增版本并写 status_changed 事件；若状态相同，提交无修改，不增版本、不写事件。

成功：200，仅返回：

```json
{"message":"更新成功"}
```

该接口不返回新版本，需要再 GET。失败包括 400 参数错误、404 任务不存在、500 内部错误；不会因版本过期返回 409。

## 6. 乐观锁更新状态

`PATCH /tasks/opm/:id`

```json
{
  "status": "completed",
  "expected_version": 1
}
```

| 字段 | 校验 |
| --- | --- |
| status | Service 校验为合法枚举 |
| expected_version | 必填、int64 正整数 |

事务流程：读取旧状态和版本，检查预期版本，通过 `WHERE id = ... AND version = ...` 原子更新，版本加 1，插入 status_changed 事件，提交后返回任务。

成功：200：

```json
{
  "message": "更新成功",
  "data": {
    "id": 10,
    "title": "学习 PostgreSQL",
    "description": "验证任务版本保护",
    "status": "completed",
    "version": 2,
    "created_at": "2026-11-03T10:00:00Z",
    "updated_at": "2026-11-03T10:01:00Z"
  }
}
```

旧版本：409，错误码包含空格，以下按当前实现保留：

```json
{
  "error": {
    "code": "TASK VERSION CONFLICT",
    "message": "任务状态冲突"
  }
}
```

缺少 expected_version 或小于等于 0：400 INVALID_REQUEST。请求只提交旧字段 version 也不满足绑定要求。

其他失败：400 INVALID_ID、INVALID_STATUS；404 TASK_NOT_FOUND；500 INTERNAL_ERROR。

当前即使新旧状态相同，乐观锁方法也会更新版本并写事件，与悲观锁入口不同。没有自动重试，没有状态转换规则校验，仅验证目标状态枚举。

### 并发保证与重试

两个请求针对同一初始版本更新，在无其他错误和写入干扰时，只有一个可以更新成功，另一个返回 409。失败事务不会保留任务修改或新增事件。

重新查询最新任务后，应由业务或用户决定是否再次提交；自动替换版本重试可能覆盖前一个请求的结果。

普通 PUT/PATCH 仍可绕过客户端版本检查，因此目前不能声称“全部更新入口都受乐观锁保护”。

## 7. 删除任务

`DELETE /tasks/:id`

不需要请求体或版本参数。成功 200：

```json
{"message":"删除成功"}
```

数据库外键 ON DELETE CASCADE 会删除该任务的全部事件。非法 id 为 400 INVALID_ID；不存在为 404 TASK_NOT_FOUND。当前删除也没有版本保护。

## 统一错误结构

```json
{
  "error": {
    "code": "TASK_NOT_FOUND",
    "message": "任务不存在"
  }
}
```

| HTTP 状态 | 典型场景 |
| --- | --- |
| 400 | JSON 无效、缺少必填字段、字段类型不符、ID 或状态非法 |
| 404 | 任务不存在 |
| 409 | 专用乐观锁接口检测到版本过期或条件更新无匹配 |
| 500 | 未分类的数据库、事务或内部错误 |

未知字段目前没有显式拒绝配置。成功响应也未统一封装：GET/POST/PUT 是任务对象，列表是数组，悲观锁更新和删除是 message，乐观锁是 message + data。接入时应按具体接口处理。
