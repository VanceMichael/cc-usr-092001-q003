# 院内制剂传承证据链

本项目用于管理方证、制剂工艺与质量证据中的稳定事实和交换边界，支撑名医方证与院内制剂从单院经验走向医联体应用：分别管理匿名方证证据、处方来源、制剂组方、生产工艺、质控规格和适用机构，任何版本晋级都必须引用上一版并取得临床、药学、生产部门的独立签署。

## 目录

- `contracts/` 保存外部交换字段示例。
- `docs/` 说明领域对象、时间和标识约定。
- `internal/domain` 领域类型与校验；`internal/store` 持久化与事件流；
  `internal/service` 业务规则；`internal/httpapi` REST 接口。
- `cmd/server` 服务入口。

## 运行

执行 `make test` 检查基础行为，执行 `make migrate` 初始化本地数据目录，执行 `make run` 启动服务。默认监听 `8080` 端口，健康检查地址为 `/health`。

配置通过环境变量传入：`PORT`（默认 8080）、`DATABASE_PATH`（默认 `data/state.json`，事件流为同名 `.events.jsonl`）。敏感值和本地数据库文件不得提交到仓库。

## 接口概览

操作人身份经请求头传入：`X-Actor-Id`、`X-Actor-Dept`
（`clinical`/`pharmacy`/`production`/`quality`）、`X-Institution-Id`。

- `POST /institutions` 登记机构；`POST /evidence` 接入匿名证据（幂等，仅摘要+指纹）；
  `POST /sources`、`POST /preparations` 登记来源与制剂。
- `POST /lines/{id}/versions` 新建版本草稿；`POST /versions/{id}/signoffs`
  部门签署；`POST /versions/{id}/promote` 晋级；`POST /versions/{id}/suspend` 紧急停用。
- `POST /preparations/{id}/applicability` 维护适用机构。
- `POST /batches` 生产批次；`POST /batches/{id}/split` 拆分；
  `POST /batches/{id}/distributions` 分发；`POST /batches/{id}/suspend` 停用。
- `POST /recalls` 发起跨院撤回；`POST /recalls/{id}/confirm` 机构确认；
  `GET /recalls/{id}/coverage` 覆盖核查。
- `POST /research-requests` 科研用途申请；`POST /research-requests/{id}/review` 审批。
- `GET /audit/batches/{batchNo}` 批次全链审计；`GET /events?subject_ref=...` 事件链。

错误统一为 `{"error":{"code","message"}}`：`invalid`（400）、`not_found`（404）、`conflict`（409）。
