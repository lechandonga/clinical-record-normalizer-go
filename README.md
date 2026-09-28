# 临床记录归一化服务（Go）

把不同来源（医院 A / 医院 B / 诊所……）的诊疗消息解析并归一到统一的
**患者（Patient）** 与 **就诊（Encounter）** 模型，使重复导入、信息冲突和
码表版本变化时的结果都**可预期、可复现、可追溯**。

## 模块结构

| 包 | 职责 |
| --- | --- |
| `model` | 统一模型：患者、就诊、概念映射、拒绝原因、导入报告 |
| `terminology` | 本地版本化码表：加载、版本切换、代码映射（active/deprecated/unknown） |
| `ingest` | 原始消息解析与归一化；按确定原因拒绝非法记录 |
| `identity` | 患者身份匹配、跨来源合并、人口学冲突取舍与留痕 |
| `registry` | 并发安全、幂等的批量导入入口；产出确定性报告与状态指纹 |
| `cmd/normalizer` | 加载本地样本并打印 JSON 报告的验证工具 |

样本位于：

- `samples/messages/*.json`：多来源消息，含重复消息与各类畸形记录；
- `samples/codesets/*.json`：两个码表（`diagnosis`、`encounter-type`）的
  `v2026` 与 `v2027` 版本，v2027 中存在废弃码与替换码。

## 本地验证

```bash
# 全部单元测试（含 -race 并发检查）
CGO_ENABLED=1 go test -race ./...

# 用 v2026 码表跑本地样本（固定时钟 => 输出完全可复现）
go run ./cmd/normalizer -version v2026 -now 2026-09-28T09:00:00Z

# 切换到 v2027：E11.9 变 deprecated，J20.9 从 unknown 变 active
go run ./cmd/normalizer -version v2027 -now 2026-09-28T09:00:00Z

# 指纹对比：同版本两次运行的 fingerprint 必须完全相同；
# v2026 与 v2027 的 fingerprint 必须不同
go run ./cmd/normalizer -version v2026 -now 2026-09-28T09:00:00Z | jq -r .report.fingerprint
go run ./cmd/normalizer -version v2027 -now 2026-09-28T09:00:00Z | jq -r .report.fingerprint
```

CLI 参数：`-codesets`（码表目录）、`-messages`（消息目录）、
`-version`（为所有码表固定版本）、`-now`（固定 RFC3339 时钟）。

## 规则摘要

归一化、身份合并、码表版本与并发/幂等的完整规则见
[`docs/rules.md`](docs/rules.md)。

- **不静默跳过任何记录**：无法归一的记录进入 `rejections`，按
  `reason` 区分原因，并保留原始报文体；`rejectedByReason` 给出按原因的计数。
- **确定性合并**：按 `(source, recordId)` 排序处理；同一批数据重复导入，
  报告与最终状态（`fingerprint`）完全一致；并发导入与串行结果一致。
- **身份**：MRN/HOSP 类标识按来源隔离（两家医院同号 MRN 不会错误合并），
  SSN/护照/国家健康 ID 等全局标识用于跨来源归并；硬矛盾（同标识指向不同
  出生日期/性别）以 `identity_conflict` 拒绝，绝不强行合并。
- **冲突取舍**：人口学矛盾以「最新 AssumedAt 优先，并列时按
  `(source, recordId)` 字典序决胜」处理，并在患者 `conflicts` 中留痕。
- **码表版本**：每批导入在开始时固定（pin）所使用的码表版本；之后加载
  v2027 不会改变按 v2026 归一的历史结论。
