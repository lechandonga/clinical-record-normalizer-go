# 归一化、身份合并与码表版本规则

本文档固定系统的取舍规则。任何规则变更都应先改本文档，再改代码与样本。

## 1. 输入与解析

- 输入记录由 `(source, recordId)` 唯一标识；幂等键为 `source + "/" + recordId`。
- 目前支持 JSON。每条消息必须是**恰好一个** JSON 对象；空体、非法 JSON、
  体中出现第二个 JSON 值都属于 `unreadable_payload`。
- 解析与校验分为两层：
  1. 结构层（`ingest.Parse`）：必填字段是否存在；
  2. 值域层（`ingest.Normalize`）：编码系统、编码语法、时间语义、码表查找。
- 消息体可自带 `source` / `recordId`，比文件名更优先。

### 1.1 拒绝原因（互斥、可计数）

| reason | 触发条件 |
| --- | --- |
| `unreadable_payload` | 空体、非 JSON、格式不支持、多个 JSON 值 |
| `missing_field` | 必填结构字段缺失或为空（recordId、患者标识及 system/value、birthDate、gender、encounter.start/end、typeSystem/typeCode、诊断的 system/code 等） |
| `invalid_code` | 编码系统不属于该码表；编码语法不合法（如 ICD-10 的 `XX!.9`）；码表版本未加载 |
| `invalid_time` | 时间字面量无法解析；就诊结束早于开始 |
| `ambiguous_time` | 就诊边界只给日期不给时分（当日内时长无法确定）；时间点在未来（语义尚未确立） |
| `invalid_birth_date` | 不是严格的 `YYYY-MM-DD`；超出合理区间（早于 1900 或晚于当前） |
| `invalid_gender` | 无法识别的性别取值 |
| `identity_conflict` | 同一身份标识指向可证明不同的两个人（见 §3） |

拒绝记录不会进入患者/就诊状态；每条拒绝都保留 `field`、`detail` 与原始
报文 `rawInput`，单测日志里会同时打印原始输入与归一化结果。

### 1.2 字段归一

- **性别**：`male/m/1`、`female/f/2`、`other/o/3`、`unknown/u/4`
  分别归一为 `male/female/other/unknown`；无法识别 → `invalid_gender`。
  `unknown` 是合法值，不参与身份硬冲突（见 §3）。
- **出生日期**：只接受日精度 `YYYY-MM-DD`；输出统一为该格式。
- **就诊时间**：必须精确到分钟或秒；仅日期会被判为 `ambiguous_time`。
- 每个归一后的人口学字段都是 `AttributedValue`：值 + 来源（source/recordId）
  + `assumedAt`（源系统断言该事实的时间）+ `receivedAt`（本系统收到时间）。

## 2. 码表与术语映射

- 码表按 `(name, version)` 不可变注册。**重复注册同一 `(name, version)`
  会被拒绝**，已注册对象还会做防御性拷贝，调用方无法篡改历史。
- 每个码表声明自己的编码 `system`（如 diagnosis=`ICD-10`，
  encounter-type=`LOCAL-ENC`）。消息里的 `typeSystem` / 诊断 `system`
  必须与码表声明一致，否则 `invalid_code`。
- 编码语法校验（`ICD-10` / `LOCAL-DX` / `LOCAL-ENC` 各有正则）只判定
  「长得合不合法」。语法合法但码表中查不到的编码**不会被拒绝**，而是
  保留在结果中并标记为 `unknown`——数据没有丢失，状态显式可见。
- 映射状态有三种：
  - `active`：该版本中有效；
  - `deprecated`：该版本中已废弃；结果同时给出 `canonicalCode`（替换码）；
  - `unknown`：该版本中不存在。
- **版本固定（pin）**：每批导入开始时，快照当前活动版本（
  diagnosis 与 encounter-type 各一个），该批所有记录都按这个版本映射，
  并把版本令牌（如 `diagnosis:v2026/encounter-type:v2026`）写进每条记录与
  报告。之后 `SetActiveVersion` 切到 v2027，只影响**之后新导入**的批次。
  已归一记录是不可变的，因此 v2026 的历史结论在加载 v2027 后仍可逐字节
  复现（见端到端测试 `TestEndToEndV2026AndV2027DifferButEachStable`）。
- 第一个加载的版本自动成为活动版本；切换活动版本只能指向已加载版本。

样本中的版本差异：v2027 中 `E11.9` 被废弃并指向替换码 `E11.90`，
`Z00.00` 被废弃，新增 `J20.9`；encounter-type 新增 `URGENT`。

## 3. 身份合并

### 3.1 匹配键

- `MRN`、`HOSP` 这类**按机构编号**的标识按来源隔离：
  `src:<source>|<SYSTEM>|<value>`。因此 hospitalA 与 hospitalB 的同号
  MRN-001 不会仅凭号码合并（样本 B-2002 因此正确触发 `identity_conflict`）。
- `SSN`、`PASSPORT`、`NHID` 等**全局权威**标识跨来源通用：
  `g:<SYSTEM>|<value>`。
- 无法确认发证机构的未知系统按来源隔离处理——宁可拆成两人，也不冒错误
  合并的风险。
- 一条记录的全部匹配键排序后生成稳定的患者 ID（SHA-256 截断，`pat-` 前缀），
  与到达顺序无关；合并多个既有患者时，字典序最小的 ID 作为存活 ID。
- 就诊 ID 由 `(source, recordId)` 确定性派生（`enc-` 前缀）。

### 3.2 合并规则

- 新记录的任一匹配键命中既有患者即归属；命中多个既有患者时把它们合并。
- 患者保留标识并集、来源并集，以及所有冲突记录。
- **硬冲突（拒绝合并）**：同一匹配键两侧出生日期都非空且不同，或性别都
  非 `unknown` 且不同。该记录以 `identity_conflict` 拒绝且不改动状态。
  这保证「不把一个人拆成多个，也不错误合并」。

### 3.3 人口学冲突取舍（可追溯）

姓名/性别/出生日期出现矛盾时，按固定全序选值：

1. `assumedAt` 更晚（源系统更新的断言）者胜；
2. `assumedAt` 相同（含都缺省）时，`(source, recordId)` 字典序更大者胜；
3. 空值永不覆盖已有值。

每次取舍都会在患者 `conflicts` 中记录字段、被选值及来源、被弃值及来源
和取舍理由。样本中 hospitalB 的 B-2001 用更晚的 assumedAt 更新了 Zhang Wei
的姓名，冲突留痕可在患者对象中看到。

## 4. 并发、幂等与确定性

- `Registry.ImportBatch` 用单许可通道串行化整个批次：并发调用等价于按
  某种顺序串行调用，不会出现身份合并的中间态竞争。
- 每批在内部按 `(source, recordId)` 稳定排序后处理，因此结果与到达顺序、
  goroutine 调度无关。
- 幂等：已进入状态的 `(source, recordId)` 不会二次吸收；同一文件批次
  内部的重复消息也只生效一次。报告中的 accepted/rejected 计数反映该批
  消息本身，状态计数（患者数、就诊数）反映去重后的累积结果。
- `fingerprint` 是对「全部患者 + 全部就诊」规范化 JSON 的 SHA-256：
  - 同批数据重复导入 → fingerprint 不变；
  - 并发导入与串行导入 → fingerprint 相同（测试
    `TestConcurrentImportEqualsSerialAndIdempotent`，`-race` 下验证）；
  - 不同码表版本得出的结论不同 → fingerprint 不同；同版本重跑 → 相同。
- 为保证测试与本地样本可复现，时钟可注入（`Registry.WithClock` /
  CLI `-now`）；缺省 `receivedAt` 与未来时间判断使用该时钟。

## 5. 本地验证方法

```bash
CGO_ENABLED=1 go test -race ./...       # 全量单测（含竞态检测）
go test -v ./ingest/                    # 查看原始输入/归一结果日志
go run ./cmd/normalizer -version v2026  # 样本端到端 JSON 报告
```

样本覆盖：跨来源合并（SSN）、同号 MRN 不合并、人口学冲突留痕、硬冲突
拒绝、重复导入、未知/废弃码、非法编码系统、非法编码语法、日期级时间
（歧义）、倒置时间区间、畸形 JSON 等。
