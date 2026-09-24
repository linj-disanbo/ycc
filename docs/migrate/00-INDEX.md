# ycc 链迁移 —— 全部产物位置与关系索引

> 2026-08-26 整理，**2026-09-18 路径同步**（交付物已从 `33cn/plugin/ycc-migrate/` 迁至 `~/work/src/chain/ycc-project/`，旧目录已删除）。
> 目标：把代码/文档/工具/数据/环境全部对应起来，便于继续验证与部署。

## 0. 一图总览

```
【ycc 新链仓库】                              【交付数据】                    【真实链服务器】
~/work/src/chain/ycc-project/ycc/            ~/work/src/chain/ycc-project/    root@R1006-122-113-bak-bty-
  分支 new-ycc                               ycc-testdata/                    ycc-old-tass:/mnt/ycc/
        │                                          │                              │
        │ migrate/（工具源码）                      │ static-binaries/（8 个）      │ dump.gob（旧链状态导出）
        │ docs/migrate/（本套文档）                 │ test-result4/（最终验证结果）  │ out4/（转换结果）
        │ plugin/consensus/ticket/（fork 共识）      │ 04/05/06-*（报告工件）        │ new-ycc/（新链 datadir）
        ▼                                          ▼                              ▼
   [构建：go1.18 + GOMODCACHE=/tmp/gomodcache]
        ▼
   ycc 节点二进制（ticket 共识 + powLimitBits=0x1e528e0c + chainID=1000）
```

## 1. 交付物目录（主交付，本地）

| 位置 | 内容 | 说明 |
|---|---|---|
| `~/work/src/chain/ycc-project/ycc/`（分支 `new-ycc`） | **ycc 新链仓库** | 新链代码 + 工具 + 文档，改动已提交（见 §2 提交列表） |
| └ `migrate/` | 8 个迁移工具源码 + `lib.go` + `build.sh` | export/transform/build/verify/calib/gendump/pos33stat/ticketdist；随 ycc 模块编译 |
| └ `docs/migrate/` | 本套文档 | 00-INDEX / DESIGN / REPORT / RUNBOOK（收益参数推导见仓库外 `new-ycc/REWARD.md`） |
| └ `plugin/consensus/ticket/ticket.go` | ticket 共识 fork 版 | createMinerTx 用 `EncodeSignID(SECP256K1, eth.ID)`（eth 地址签名修复） |
| └ `ycc.go` / `ycc.toml` | 内嵌配置 + 运行配置 | 共识/难度/maxTxNumber/收益参数内嵌；ChainID 在 ycc.toml |
| `~/work/src/chain/ycc-project/ycc-testdata/static-binaries/` | 8 个**静态链接**可执行文件 | 服务器上直接运行，无依赖（linux-amd64）：build/calib/export/gendump/pos33stat/**ticketdist**/transform/verify |
| `…/ycc-testdata/test-result4/` | **真实链最终版验证结果** | report.txt / summary.json / state.bin / local.bin / new-ycc(datadir) / new-ycc.tgz / top100-tickets.txt —— 用于部署 |
| `…/ycc-testdata/test-result{,2,3}/` | 真实链第 1/2/3 次转换（旧版） | 只有 report.txt + summary.json，供对照，勿用于部署 |
| `…/ycc-testdata/04-…txt` `05-…json` `06-…json` | 测试报告工件 | 合成数据转换报告 / summary / 压力测试 |
| `~/work/src/chain/ycc-project/old-ycc/` | 旧链接口参考资料 | GetPos33ConsigneeEntrust 查询样例、ycc-to-assetchain |
| `~/work/src/chain/ycc-project/new-ycc/REWARD.md` | 收益参数定稿推导 | 旧链 13.8 ycc/s → 新链 69 ycc/5s → bty 5:3 取整 43:26 |
| `~/work/src/chain/ycc-project/SESSION-SUMMARY.md` | 会话交接总结 | 项目目标/已完成/待办/关键路径 |
| `~/work/src/chain/ycc-project/TODO-new-ycc.md` | 新链任务清单 | 去 ticket fork（等 bityuan 新版）等 |

> **原 `33cn/plugin/ycc-migrate/` 已删除**，其内容去向：
> - `01-ycc-code-changes.patch` → 已应用到 ycc 仓库并提交（**不再单独保留**；需要时可 `git diff 25310e5..` 重新生成）；
> - `02-migrate-tools/` → ycc 仓库 `migrate/`；
> - `static-binaries/`、`test-result*/`、`04/05/06-*` → `ycc-testdata/`；
> - `DESIGN.md` / `RUNBOOK.md` / `REPORT.md` → ycc 仓库 `docs/migrate/`；
> - `03-new-chain-config-sample.toml`、`ticket-fork-ticket.go` → 已并入 ycc.go / `plugin/consensus/ticket/ticket.go`，不再单独保留。

## 2. 代码改动落点（ycc 仓库）

`~/work/src/chain/ycc-project/ycc/`（分支 `new-ycc`，基于 master 25310e5）

| 文件 | 改动 |
|---|---|
| `ycc.go` | 内嵌配置：consensus=ticket、minerExecs=["ticket"]、[consensus.sub.ticket]、[mver.consensus.ticket]、[fork.sub.ticket]、maxTxNumber=30000、powLimitBits=0x1e528e0c、coinReward=43、coinDevFund=26、ticketPrice=100000、targetTimePerBlock=5、evmChainID=4000 |
| `ycc.toml` | ChainID=1000 |
| `plugin/consensus/init/init.go` | 注册 ycc fork 的 ticket 共识 |
| `plugin/dapp/init/init.go` | 注册 ticket 执行器（33cn/plugin 原版） |
| `plugin/consensus/ticket/ticket.go`（新增） | fork 版 ticket 共识：createMinerTx 用 `EncodeSignID(SECP256K1, eth.ID)`（eth 地址签名修复） |
| `migrate/`（新增目录） | 迁移工具源码（export/transform/build/verify/calib/gendump/pos33stat/ticketdist + build.sh） |
| `go.mod` | 新增 goleveldb 依赖（工具用） |

提交（`git log --oneline`）：`7c3b751 引入 ticket` → `3687ce4 增加 migrate` → `d420144 add migrate build script` → `080bf54 add ycc migrate docs` → `376a656`/`903decb`（ignore bin）→ `89504e5 add ticketdist tool` → `954636c test chain id` → `cc25f1e add build ticketdist`

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

（本地落盘位置：`~/work/src/chain/ycc-project/ycc-testdata/test-result4/`）
```

辅助：`pos33stat`（检查 pos33 资金分布）、`calib`（难度标定，真实链 40,645 票→0x1e528e0c）、`verify`（转换差异校验，0 违规）、`gendump`（合成测试数据）、`ticketdist`（持票地址分布 Top N）。

**票价确认（2026-09-18）**：ticketPrice=100,000 ycc/张。真实数据复核：40,645 张 × 100,000 = **40.645 亿 ycc**，与 test-result4 的“票价值(最小单位) 406450000000000000”/1e8 精确一致。

## 4. 构建环境（本地）

| 项 | 位置 | 说明 |
|---|---|---|
| Go 工具链 | `/home/linj/pkg/go1.18` | ycc 依赖 quic-go 要求 go≤1.18（go119.go 故意拒 1.19+） |
| module cache | `/tmp/gomodcache` | 从 `~/work/pkg/mod` 拷贝（沙箱只读），`GOMODCACHE` 指向它 |
| build cache | `/tmp/gocache18` | `GOCACHE` |
| 编译命令 | `CGO_ENABLED=0 GOROOT=/home/linj/pkg/go1.18 GOMODCACHE=/tmp/gomodcache GOCACHE=/tmp/gocache18 GOFLAGS=-mod=mod go build ...` | 静态链接需 CGO_ENABLED=0 |
| 一键编译工具 | `migrate/build.sh [输出目录]` | 输出到 `ycc-testdata/static-binaries/` |

## 5. 本地测试环境（历史，已清理）

2026-08-24/26 验证用的临时目录（`/tmp/yccwork`、`/tmp/ycc-clean`、`/tmp/clean-test`、`/tmp/node-b`、`/tmp/ycc-node*`、`/tmp/mig-*`）**已随 /tmp 清理不存在**，仅 `/tmp/gomodcache`、`/tmp/gocache18` 保留。
复现验证：用 `ycc-testdata/static-binaries/` 里工具 + `test-result4/new-ycc` 预载 datadir 重新起链即可。

## 6. 真实链服务器（部署目标）

`root@R1006-122-113-bak-bty-ycc-old-tass:/mnt/ycc/`
- `dump.gob`（export 产物）、`out4/`（transform 最终结果）、`new-ycc/`（build 生成的 datadir）
- 静态二进制已部署（transform/verify/build/pos33stat/ticketdist 等）

## 7. 后续待办

1. ~~**参数定稿**：chainID/evmChainID/收益参数~~ ✅ 2026-09-18 定稿：chainID=1000、evmChainID=4000、coinReward=43、coinDevFund=26（5:3，总增发 69 ycc/块 ≈ 旧链 13.8 ycc/s，推导见 `~/work/src/chain/ycc-project/new-ycc/REWARD.md`）
2. **双节点新链同步验证**（真实状态）：两节点同 preload datadir 起链，确认同步
3. **正式迁移**：按 RUNBOOK 阶段四/五执行（用**最新 datadir**重新 export→transform→verify→calib→build→部署）
4. **2 张脏数据票**：上线后委托人 0x485703fc 等用 ticket close 取回（共 70 万 ycc）
5. **新链二进制**：用定稿配置重建 ycc 节点 → 服务器部署
