# clinical-record-normalizer-go

把多来源诊疗消息归一到统一的患者与就诊模型，覆盖确定性归一化、跨来源身份合并与
冲突溯源、本地码表版本化映射，以及可复现/幂等的批量导入（串行与并发结果一致）。

## 目录结构

```
cmd/normalizer/            本地样本导入 CLI
internal/domain/           统一模型：患者、就诊、诊断、带溯源的值
internal/ingest/           来源配置、消息信封解析
internal/normalize/        单条消息归一化 + 可区分拒绝原因
internal/terminology/      本地码表、版本注册、三态映射
internal/identity/         身份链接（并查集）与人口学冲突取舍
internal/pipeline/         串行/并发导入、报告、幂等 Store
samples/sources.json       来源优先级与时区配置
samples/messages/          本地样本消息（含各类畸形输入）
tables/diag/               diag-v1 / diag-v2 两个码表版本
docs/                      规则与版本处理说明
```

## 本地运行

需要 Go 1.25+。

```bash
# 并发导入（逐记录的"原始输入 / 归一化结果"打印到 stderr；JSON 报告输出到 stdout）
go run ./cmd/normalizer \
  -samples ./samples/messages \
  -sources ./samples/sources.json \
  -tables ./tables/diag \
  -diag-version diag-v1 -workers 8 -mode concurrent > report.json

# 串行导入，结果应与并发逐字节一致
go run ./cmd/normalizer -diag-version diag-v1 -mode serial > report-serial.json
diff report.json report-serial.json

# 切换批次基线码表版本（仅影响未在消息中显式声明版本的记录）
go run ./cmd/normalizer -diag-version diag-v2 -mode serial > report-v2.json
```

可选参数：`-batch-id`（批次 ID，重复批次为空操作回放）、`-mode serial|concurrent`、
`-workers`、`-samples/-sources/-tables`。

样本批处理（15 条）预期：接收 7 条、拒绝 8 条，合并出 3 名患者、7 次就诊。
拒绝原因覆盖：畸形 JSON、字段缺失、非法编码、非法性别、时间语义不明、未来出生日期、
未知来源、重复消息。码值上可见 `Z99.99 → unknown`、`I21.9@diag-v2 → deprecated`。

## 运行测试

```bash
go test ./...                       # 全部单测
go test -v ./internal/pipeline/     # 含原始输入与归一化结果日志
go test -race ./...                 # 并发竞态检测（需 CGO）
go vet ./...
gofmt -l .
```

关键测试：

- `normalize.TestRejectionReasonsAreDistinguishable`：各类畸形输入产生可区分原因，
  并在日志打印原始输入与归一化结果。
- `pipeline.TestConcurrentEqualsSerial`：并发（8 worker）与串行报告逐字节一致，
  且拒绝按原因计数正确。
- `pipeline.TestConcurrentImportIsStableAcrossRuns`：不同 worker 数、重复运行结果稳定。
- `pipeline.TestStoreIdempotentAndConcurrencySafe`：同批次并发 Apply 收敛一致、
  重复 Apply 幂等。
- `pipeline.TestUnknownAndDeprecatedCodingsSurviveInReport`：码表升级后重放历史批次，
  结论不变。
- `identity.*`：共享强标识合并、优先级/新近度冲突规则、不同人不误并、传递性链接。

## 规则文档

- [归一化与身份合并规则](docs/normalization-rules.md)
- [码表版本处理](docs/code-table-versioning.md)
