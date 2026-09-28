# 归一化与身份合并规则

本文件描述消息如何被归一成统一模型、记录何时被拒绝（拒绝原因可区分），以及跨来源
患者身份如何合并、冲突如何取舍。所有规则都是**确定性**的：同样的输入与码表版本，
无论串行还是并发、无论导入多少次，结果逐字节一致。

## 1. 处理流水线

```
原始 JSON
  → ingest.Parse        信封解析与形态校验
  → normalize.Normalize 字段校验 / 时间语义 / 术语映射（无跨消息状态）
  → identity.Resolve    身份链接 + 人口学冲突取舍（纯函数归并）
  → Report              患者 + 就诊 + 可区分的拒绝清单
```

并发模式（`RunConcurrent`）只把"单条解析与归一化"放进 worker 并行，去重、排序、
身份合并都在唯一的串行归约阶段完成。因此并发结果与串行结果完全一致。

## 2. 拒绝原因（不静默跳过任何记录）

每条无法接收的记录都进入报告 `rejections`，并保留**原始输入原文**、来源指针与原因：

| reason               | 含义                                                       |
|----------------------|------------------------------------------------------------|
| `malformed_json`     | 不是合法 JSON，或信封/ payload 形态错误（如 payload 非对象）|
| `missing_field`      | 必填字段缺失（`message_id`/`source_id`/`sent_at`/`payload`/`sex`/`birth_date`/本地患者号/就诊号/就诊类型/就诊开始时间）|
| `invalid_code`       | 编码形态非法（含非法字符、身份证号形态错误）、未知码表版本 |
| `invalid_sex`        | 性别值不在可识别集合                                       |
| `invalid_time`       | 时间串无法解析，或就诊结束早于开始                         |
| `ambiguous_time`     | 时间语义不明确（见下）                                     |
| `invalid_birth_date` | 出生日期不是严格 `YYYY-MM-DD` 或不是合法日历日             |
| `future_birth_date`  | 出生日期晚于当前日期                                       |
| `unknown_source`     | `source_id` 未在来源目录登记                               |
| `duplicate_message`  | 同一 `(source_id, message_id)` 在本批中再次出现             |

注意三类"编码问题"是**刻意区分**的：

1. 形态非法（如 `"bad code!"`）→ `invalid_code`，拒绝；
2. 形态合法但在钉选版本中查不到（如 `Z99.99`）→ **不拒绝**，映射为
   `coding.status=unknown`、概念为 `UNKNOWN`，原码保留，明显标记；
3. 编码存在但已废弃 → `coding.status=deprecated` 且 `replaced_by` 指向新码。

## 3. 时间语义

所有时间统一转为 UTC 存储。

- 带显式偏移或 `Z` 的 RFC3339：处处无歧义，直接采用。
- `YYYY-MM-DTHH:MM:SS+OFFSET` 之外的纯日期 `YYYY-MM-DD`：只表示"某一天"而非瞬时，
  判为 `ambiguous_time`。
- 无时区的墙上时间 `YYYY-MM-DD HH:MM:SS`：仅当来源在 `sources.json` 声明了
  `time_zone` 时，按该时区解释为瞬时；否则判为 `ambiguous_time`。
  （示例中 `partner-lab-c` 未声明时区，其墙上时间消息因此被拒绝。）

## 4. 字段归一化

- 性别：`M/MALE/男/1 → M`，`F/FEMALE/女/2 → F`，`U/UNKNOWN/未知/0 → U`。
- 就诊类型：`住院/IP/INPATIENT → inpatient`，`门诊/OP/OUTPATIENT → outpatient`，
  `急诊/ER/EMERGENCY → emergency`。
- 出生日期：严格 `YYYY-MM-DD`，非法日历日与未来日期分别拒绝。
- 各来源使用不同字段名（`patient_mrn/patient_id/card_id`、`sex/gender`、
  `birth_date/birthday`、`visit_id/visit_no/encounter_id`、
  `diagnoses/diag_codes/codes` 等），归一层按来源无关的统一语义读取。

## 5. 身份链接

链接键是**带命名空间的标识 `(system, value)`**：

- 每个来源的本地号放入 `MRN|<source_id>` 命名空间。因此不同来源恰好相同的 MRN
  字符串**不会**错误合并。
- 身份证号统一为 `ID_CARD` 系统（大写归一化、形态校验）。同一身份证跨来源出现即
  判为同一人。
- 链接具有传递性：A 与 B 共享一个标识、B 与 C 共享另一个标识，则 A/B/C 合并。
  实现为确定性并查集。

**不会**仅按姓名、出生日期等弱属性合并，避免错误合并。

## 6. 人口学冲突取舍

同一患者的 `sex`、`birth_date` 在不同来源/消息中矛盾时，按以下**全序**选唯一胜出值：

1. 来源优先级 `priority` 高者胜（`sources.json` 配置）；
2. 优先级相同时，消息 `sent_at`（已转 UTC）更晚者胜；
3. 仍相同时，按 `(source_id, message_id)` 字典序。

每个被淘汰的**不同取值都会**作为 `conflicts[]` 保留，记录字段名、胜出值（含来源与
接收时间）、失败值（含来源）以及规则名 `source_priority_then_recency_then_source_ref`，
做到取舍可追溯。患者的全部标识与来源指针也都保留。

## 7. 确定性 ID 与幂等

- 患者 ID：`PAT-` + sha256（该患者全部排序后的 `(system,value)` 链接键）前 12 字节。
- 就诊 ID：`ENC-` + sha256（`source_id|message_id|visit_id`）前 12 字节。

ID 只依赖内容，因此重复导入得到相同 ID。报告中的患者、就诊、标识符、诊断、冲突都
按稳定顺序输出；拒绝清单保持输入顺序，便于定位畸形记录。跨批次的 `Store.Apply`
以批次 ID 去重，重复批次是空操作回放，并在互斥锁内合并，保证并发安全与幂等。
