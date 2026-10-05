# 古生物修复制备台

古生物修复制备台是一个使用 Go 编写的后端基线，用于记录化石标本、审批修复方案、回填制备进度，并在全部工序完成后办理研究借阅放行。

该服务只使用进程内仓储，便于在本机重复启动和检查。它不依赖数据库、远端接口或其他在线资源。

## 目录

- `cmd/paleo-prep-station`：HTTP 服务入口和监听参数。
- `cmd/workflowcheck`：通过真实 HTTP 处理器执行四条业务链路的本地验证命令。
- `internal/httpapi`：路由、请求解码、状态码映射和 JSON 输出。
- `internal/station`：跨领域对象的编排、事务边界和业务流程。
- `internal/domain`：标本、方案、进度和放行记录的状态与不变量。
- `internal/repository`：互斥保护的内存仓储和原子提交。
- `internal/contracts`：稳定错误模型和公共请求结构。

## 构建与运行

```bash
go build ./...
go run ./cmd/paleo-prep-station --addr 127.0.0.1:18133
```

服务启动后，`GET /healthz` 返回就绪状态。

## 主要接口

- `GET /specimens` 与 `POST /specimens`
- `GET /specimens/{id}`
- `POST /specimens/{id}/plans`
- `GET /specimens/{id}/plans`
- `POST /specimens/{id}/release`
- `GET /plans/{id}`
- `POST /plans/{id}/approval`
- `POST /plans/{id}/cancellation`
- `POST /plans/{id}/progress`
- `POST /plans/{id}/archive`
- `GET /archives/{id}`

## 方案归档

进入终态（`completed` 或 `cancelled`）且离开最近查询保留范围的方案，才允许通过
`POST /plans/{id}/archive` 归档。归档在同一个仓储事务内：

1. 校验方案为终态，并且自结束时刻起已经过保留期（默认 30 天，可用
   `--archive-retention` 调整）；
2. 已完成方案必须拥有完整、连续、单调且以 100% 收尾的进度轨迹，且其标本已经
   `available` 并存在永久放行记录；已取消方案必须没有任何进度记录引用；
3. 生成不可变归档：封存方案与进度的精确快照、归档人与时间，并对规范化快照计算
   SHA-256 摘要，作为可校验的存在事实；
4. 把方案及其进度原子移出在线集合，写入独立的归档集合。

归档之后：

- 在线读取范围不会被静默改写：`GET /plans/{id}` 返回 `404 plan_archived`，写操作
  返回 `409 plan_archived`，错误体的 `details.archive_id` 指回归档；
- `GET /specimens/{id}/plans` 的 `items` 只含在线方案，另以 `archived` 数组显式给出
  已移出方案的归档定位，方案不会无声消失；
- 归档可通过 `GET /archives/{id}` 永久读取，读取时会重新校验摘要；
- 重复归档返回 `409 plan_already_archived` 并指向既有归档，绝不会生成第二份有效结果；
- 归档失败不提交任何变更，方案状态、进度、标本状态与读取范围保持原样。

启动参数 `--archive-retention`（Go duration，如 `24h`、`720h`）控制方案结束后仍属
最近查询范围的时长。

## 本地验证

每条业务链路都可以独立执行：

```bash
go run ./cmd/workflowcheck --workflow register-specimen
go run ./cmd/workflowcheck --workflow approve-prep-plan
go run ./cmd/workflowcheck --workflow log-preparation
go run ./cmd/workflowcheck --workflow release-for-study
go run ./cmd/workflowcheck --workflow archive-completed-plan
go run ./cmd/workflowcheck --workflow archive-blocked-plan
go run ./cmd/workflowcheck --workflow archive-retention-window
```

这些检查会启动临时 HTTP 处理器并调用真实公共路由。

## 环境变量

项目不需要环境变量，也没有外部服务依赖。

## 测试边界

归档能力附带聚焦的 Go 单元测试（`internal/domain`、`internal/station`），覆盖摘要
封存与篡改检出、归档前置条件、重复归档单一结果、失败不产生副作用以及并发提交的
互斥；`cmd/workflowcheck` 的端到端链路检查继续作为公共路由烟雾路径。

```bash
go test ./...
```
