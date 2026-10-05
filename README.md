# 古生物修复制备台

古生物修复制备台是一个使用 Go 编写的后端基线，用于记录化石标本、审批修复方案、回填制备进度，并在全部工序完成后办理研究借阅放行。

该服务只使用进程内仓储，便于在本机重复启动和检查。它不依赖数据库、远端接口或其他在线资源。

## 目录

- `cmd/paleo-prep-station`：HTTP 服务入口和监听参数。
- `cmd/workflowcheck`：通过真实 HTTP 处理器执行业务链路的本地验证命令（含两条归档链路）。
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
- `GET /plans/{id}`
- `POST /plans/{id}/approval`
- `POST /plans/{id}/cancel`
- `POST /plans/{id}/progress`
- `POST /plans/{id}/archive`（首次 201，重复提交幂等返回 200）
- `GET /plans/{id}/archive`
- `GET /plan-archives/{id}`
- `GET /specimens/{id}/archives`
- `POST /specimens/{id}/release`

## 方案归档

已完成方案在办结研究放行、已取消方案在保留取消记录后，且都离开最近查询保留期（默认三十天）并提供完整审计引用编号时，可以通过 `POST /plans/{id}/archive` 归档。归档把方案及其进度移入不可变记录（含 SHA-256 内容摘要），在线集合不再包含该方案：`GET /plans/{id}` 返回带 `reference` 的 `plan_archived` 404，归档本身可通过 `GET /plans/{id}/archive` 或 `GET /plan-archives/{id}` 定位。归档幂等，重复提交不会产生第二份有效结果；归档不会改写标本或放行记录的既有状态。

## 本地验证

每条业务链路都可以独立执行：

```bash
go run ./cmd/workflowcheck --workflow register-specimen
go run ./cmd/workflowcheck --workflow approve-prep-plan
go run ./cmd/workflowcheck --workflow log-preparation
go run ./cmd/workflowcheck --workflow release-for-study
go run ./cmd/workflowcheck --workflow archive-completed-plan
go run ./cmd/workflowcheck --workflow archive-cancelled-plan
```

这些检查会启动临时 HTTP 处理器并调用真实公共路由。`go test ./...` 覆盖归档前置条件、保留期、放行引用完整性、幂等归档、归档后读范围隔离和既有状态保持。

## 环境变量

项目不需要环境变量，也没有外部服务依赖。

## 测试边界

自动化测试位于各 `internal` 包内（领域不变量、站务编排与 HTTP 路由），生产级链路检查继续承担端到端烟雾验证。
