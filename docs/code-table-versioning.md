# 本地码表与版本处理

诊断码表位于 `tables/diag/*.json`，随仓库一起版本化。本工程内置两个版本：

- `diag-v1`：`I10/I21.9/E11.9/J00/LC-OLD` 均为 `active`。
- `diag-v2`：新增 `I21.3`、`C34.9`；`I21.9`、`LC-OLD` 置为 `deprecated` 并通过
  `replaced_by` 指向新码（`I21.3`、`C34.9`）。

## 版本如何被钉选

- 每条消息可在信封 `version`（或 payload `diag_version`）显式声明它应当使用的
  诊断码表版本。
- 消息未声明时，使用批次基线版本（CLI 的 `-diag-version`，即 `Batch.DiagVersion`）。
- 版本在**导入那一刻**解析并写入每条 `coding.table_version`，之后与数据一起保存。

## 三态映射

| 情形                                   | 结果                                                          |
|----------------------------------------|---------------------------------------------------------------|
| 钉选版本中存在且 active                | `status=active`，返回归一概念                                 |
| 钉选版本中存在但 deprecated            | `status=deprecated`，概念仍返回（保留历史含义），带 replaced_by |
| 形态合法但钉选版本中无此码             | `status=unknown`，概念置为 `UNKNOWN`，原码保留并明显标记       |
| 形态非法                               | 记录被拒绝，原因 `invalid_code`                               |
| 请求的版本在注册中心不存在             | 记录被拒绝，原因 `invalid_code`（detail 指明未知版本）          |

## 为什么历史结论不会被码表更新悄悄改变

- 映射永远依据 `coding.table_version` 这一"当时钉选"的版本求值，注册中心后续加载
  新版本不会修改任何已有 `Coding`。
- 示例：`I21.9` 在 v1 为 active。某条显式声明 v1 的历史消息，即使在加载 v2 后
  重新跑同一批次，结论仍是 active；而显式声明 v2 的消息得到 deprecated 标记。
  同一次批次运行里，历史的 v1 记录与新的 v2 记录可以并存、各自带版本痕迹。
- 测试 `TestUnknownAndDeprecatedCodingsSurviveInReport` 以逐字节报告比对验证：
  加载 v2 后重放 v1 批次，结果与首次完全一致。

## 新增版本

新增一个 `tables/diag/diag-v3.json` 并在其中声明 `system/version/entries` 即可被
CLI 自动加载（按文件名排序读取）。默认版本是首个加载的版本，可通过
`Registry.SetDefault` 改变。重复的版本号或重复编码在加载时即报错，杜绝歧义表。
