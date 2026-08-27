# ycc 链迁移 —— 全部产物位置与关系索引

> 2026-08-26 整理。目标：把代码/文档/工具/数据/环境全部对应起来，便于继续验证与部署。

## 0. 一图总览

```
【ycc 原始仓库】                     【交付物】                        【真实链服务器】
/home/linj/work/src/github.com/      /home/linj/work/src/github.com/    root@R1006-122-113-bak-bty-
  yccproject/ycc                     33cn/plugin/ycc-migrate/          ycc-old-tass:/mnt/ycc/
        │ 应用补丁                           │                              │
        ▼                                   ▼                              ▼
   ycc.go / plugin/...           01-ycc-code-changes.patch           dump.gob（旧链状态导出）
   （新链二进制源码）            02-migrate-tools/（工具源码）        out4/（转换结果）
        │                           static-binaries/（8 个静态二进制）  new-ycc/（新链 datadir）
        │                           DESIGN.md / RUNBOOK.md
        ▼
   [构建：go1.18 + /tmp/gomodcache]
        ▼
   ycc 节点二进制（含 ticket 共识 + powLimitBits=0x1e528e0c）
```

## 1. 交付物目录（主交付，本地）

`/home/linj/work/src/github.com/33cn/plugin/ycc-migrate/`

| 文件/目录 | 内容 | 说明 |
|---|---|---|
| `01-ycc-code-changes.patch` | ycc 仓库代码改动补丁 | **应用到 ycc 仓库即可得新链二进制源码**（含 ticket 共识注册、ycc.go 内嵌配置、maxTxNumber、powLimitBits=0x1e528e0c） |
| `02-migrate-tools/` | 7 个迁移工具源码 + lib.go | export/transform/build/verify/calib/gendump/pos33stat；**放入 ycc 仓库 migrate/ 目录后可用 go 构建** |
| `static-binaries/` | 8 个**静态链接**可执行文件 | 服务器上直接运行，无依赖（linux-amd64） |
| `ticket-fork-ticket.go` | ticket 共识 fork 版 | 需放入 ycc 仓库 `plugin/consensus/ticket/ticket.go`（补丁外的独立新增文件） |
| `DESIGN.md` | 完整设计文档 | 架构/转换规则/配置/验证结果（含 2026-08-26 真实链验证 + 3 个 bug 修复记录） |
| `RUNBOOK.md` | 真实迁移操作手册 | 五阶段执行步骤 + 上线验证清单 |
| `03-new-chain-config-sample.toml` | 新链配置样例 | 仅含"新增键"（chainID=1000、dbPath、RPC 等；共识等内嵌配置在 ycc.go） |
| `04/05/06-*` | 测试报告工件 | 合成数据转换报告 / summary / 压力测试 |
| `test-result/` | 真实链第 1 次转换（旧版） | 供对照，勿用于部署 |
| `test-result2/` | 真实链第 2 次（旧版 transform） | 同上 |
| `test-result3/` | 真实链第 3 次（修正版，票ID未修） | 同上 |
| `test-result4/` | **真实链最终版（修正版+票ID修复）** | report/summary/state.bin/local.bin/new-ycc(datadir) —— 用于部署 |
| `export-static-linux-amd64` | 冗余副本 | 与 static-binaries/ 内相同 |

## 2. 代码改动落点（ycc 仓库）

`/home/linj/work/src/github.com/yccproject/ycc/`（原始仓库，只读状态）

| 文件 | 改动 | 来源 |
|---|---|---|
| `ycc.go` | 内嵌配置：consensus=ticket、minerExecs=["ticket"]、[consensus.sub.ticket]、[mver.consensus.ticket]、[fork.sub.ticket]、maxTxNumber=30000、powLimitBits=0x1e528e0c | 01 patch |
| `plugin/consensus/init/init.go` | 注册 ycc fork 的 ticket 共识 | 01 patch |
| `plugin/dapp/init/init.go` | 注册 ticket 执行器（33cn/plugin 原版） | 01 patch |
| `plugin/consensus/ticket/ticket.go`（新增） | fork 版 ticket 共识：createMinerTx 用 `EncodeSignID(SECP256K1, eth.ID)`（eth 地址签名修复） | ticket-fork-ticket.go |
| `migrate/`（新增目录） | 工具源码（= 02-migrate-tools/ 内容），放在这里才能随 ycc 模块编译 | 02-migrate-tools/ |
| `go.mod` | 新增 goleveldb 依赖（工具用） | 01 patch |

> 工作副本：`/tmp/yccwork`（ycc 仓库的完整拷贝，所有改动先在这里完成并验证，01 patch 由此生成）。`/tmp/ycc-clean` / `/tmp/ycc-patch-test` 是补丁干净应用验证用的临时克隆。

## 3. 迁移数据流（输入输出关系）

```
旧链 datadir/kvmvcc/store.db
        │ export-static-linux-amd64（服务器上跑，读 leveldb 只读）
        ▼
dump.gob（293.3 万键，445MB；Height/StateHash/KVs）
        │ transform-static-linux-amd64 -in dump.gob -out out4/ -symbol ycc -ticketPrice 100000
        ▼
out4/state.bin（2,973,678 键）   out4/local.bin（41,127 键）   out4/report.txt   out4/summary.json
        │ build-static-linux-amd64 -datadir new-ycc -state ... -local ...
        ▼
new-ycc/kvmvcc/store.db（版本0 状态预载）  new-ycc/blockchain.db（LODB 票列表预载）
        │ 新链节点启动（ticket 共识，chainID=1000，powLimitBits=0x1e528e0c）
        ▼
新链高度 0 = 迁移状态 → 矿工导入私钥挖矿（5s 出块）
```

辅助：`pos33stat`（检查 pos33 资金分布）、`calib`（难度标定，真实链 40,645 票→0x1e528e0c）、`verify`（转换差异校验，0 违规）、`gendump`（合成测试数据）。

## 4. 构建环境（本地）

| 项 | 位置 | 说明 |
|---|---|---|
| Go 工具链 | `/home/linj/pkg/go1.18` | ycc 依赖 quic-go 要求 go≤1.18（go119.go 故意拒 1.19+） |
| module cache | `/tmp/gomodcache` | 从 `~/work/pkg/mod` 拷贝（沙箱只读），`GOMODCACHE` 指向它 |
| build cache | `/tmp/gocache18` | `GOCACHE` |
| 编译命令 | `CGO_ENABLED=0 GOROOT=/home/linj/pkg/go1.18 GOMODCACHE=/tmp/gomodcache GOCACHE=/tmp/gocache18 GOFLAGS=-mod=mod go build ...` | 静态链接需 CGO_ENABLED=0 |

## 5. 本地测试环境（验证用，可清理）

| 项 | 位置 | 状态 |
|---|---|---|
| 合成数据测试链 A | `/tmp/clean-test/`（datadir）+ `/tmp/ycc-test/`（配置） | 运行中（1000 票测试） |
| 双节点同步测试 B | `/tmp/node-b/` | 运行中 |
| 真实链状态测试链 | `/tmp/new-ycc.toml` + `/tmp/ycc-node-real` + test-result4/new-ycc | 运行中（真实 40,645 票状态） |
| 测试用节点二进制 | `/tmp/ycc-node`（1000票难度）/ `/tmp/ycc-node-real`（真实难度） | — |
| 工具动态版 | `/tmp/mig-*`、`/tmp/ycc-migrate-*-static` | 与交付物 static-binaries/ 同源 |

## 6. 真实链服务器（部署目标）

`root@R1006-122-113-bak-bty-ycc-old-tass:/mnt/ycc/`
- `dump.gob`（export 产物）、`out4/`（transform 最终结果）、`new-ycc/`（build 生成的 datadir）
- 静态二进制已部署（transform/verify/build/pos33stat 等）

## 7. 后续待办

1. **整理落位**：交付物是否移到 ycc 仓库 `migrate/`（正式化）或独立仓库？当前在 33cn/plugin 仓库下是临时位置
2. **新链二进制**：应用 01 patch + ticket-fork-ticket.go → 构建 ycc 节点 → 服务器部署
3. **新链配置定稿**：chainID 值（当前 1000）、收益参数（coinReward=75/coinDevFund=1）确认
4. **双节点新链同步验证**（真实状态）：两节点同 preload datadir 起链，确认同步
5. **2 张脏数据票**：上线后委托人 0x485703fc close 取回 20 万 ycc
6. **正式切换**：按 RUNBOOK 阶段四/五执行
