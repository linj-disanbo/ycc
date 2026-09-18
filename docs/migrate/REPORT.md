# ycc 链"减少存储重跑"迁移项目 —— 完整汇报

> 版本：2026-08-26（2026-09-18 更新：参数定稿 + 路径同步）
> 状态：技术方案已定稿、参数已定稿，真实链备份数据全流程验证通过，待上线部署
> 目标读者：项目组（技术评审/决策）

---

## 目录
1. [项目背景与目标](#1-项目背景与目标)
2. [现状与技术原理](#2-现状与技术原理)
3. [迁移方案总体设计](#3-迁移方案总体设计)
4. [迁移流程与工具详解](#4-迁移流程与工具详解)
5. [过程中的关键发现与修复](#5-过程中的关键发现与修复)
6. [真实链备份数据验证结果](#6-真实链备份数据验证结果)
7. [新链配置](#7-新链配置)
8. [风险与注意事项](#8-风险与注意事项)
9. [部署步骤与后续计划](#9-部署步骤与后续计划)

---

## 1. 项目背景与目标

### 1.1 背景
- **ycc 链**于 2022-05 上线（genesisBlockTime=1652797628），采用 **pos33** 共识（BLS 抽签 + 委托质押），**约 1 秒出块**。
- 至 2026-08，链高度已达 **~1.47 亿**，存储持续膨胀，问题包括：
  - 区块历史：约 1.47 亿个区块的完整数据；
  - 状态存储（kvmvcc）：MVCC 版本链**保留每个高度的全部变更**（约 12 亿条变更记录），不裁剪。
- **pos33 共识已无团队维护**；ticket 共识（bityuan 链同款）有社区维护。

### 1.2 目标
1. **保存现有全部状态**：账户余额、pos33 质押/奖励、evm/token/trade 等所有 dapp 状态，迁移后完整保留；
2. **共识出块间隔 1s → 5s**；
3. **共识从 pos33 切换为 ticket**（bityuan 风格），含必要的状态迁移让 ticket 共识可运行；
4. **chainID 变更**：防重放攻击（新链只保留状态，旧交易不得在新链生效）。

### 1.3 核心思想：减少存储
新链**从创世重新运行，只导入最终状态、丢弃全部历史区块**。迁移后 datadir 只含：创世块 + 新产生的区块 + 当前状态（版本 0 一层），存储量从"历史全量 + 状态全版本"降为"状态单版本"，大幅减少。

---

## 2. 现状与技术原理

### 2.1 链现状（2026-08 实测）
| 项 | 值 |
|---|---|
| 高度 | 146,984,348（备份数据 143,872,889） |
| 出块间隔 | ~1.09 秒 |
| chainID | 999 |
| 币符号 | ycc（1e8 精度） |
| 共识 | pos33（BLS 抽签，委托质押） |
| 地址格式 | eth（0x…）为主，兼容 base58 |
| 状态存储 | kvmvcc（MVCC 版本链） |

### 2.2 pos33 共识与质押模型
- **委托质押**：委托人（consignor）把币委托给矿工（consignee），矿工凭质押出块，奖励按份额分配给委托人，矿工抽 fee。
- **票价**：`ticketPrice2 = 100,000 ycc/张`（配置 `UseEntrust=0`，自创世启用第二档票价）。
- **区块奖励**：每块由 pos33 执行器增发 `blockReward = 15 ycc` 到执行器主余额，再按记账阈值（≥1 ycc）转账给委托人/矿工。
- **pos33 状态键**：`mavl-pos33-consignee-*`（矿工及其委托人明细）、`mavl-pos33-consignor-*`、`mavl-pos33-*`（存款记录）、`mavl-pos33-bind/bls/allcountid/all-frozen`。

### 2.3 状态存储：kvmvcc 的 MVCC 模型（关键）
- 链上所有状态（余额、dapp 数据）以 **键值对** 形式存于 kvmvcc。
- **双重记账**（chain33 执行器资金模型，本项目的核心发现）：
  - 用户质押/存款（`TransferToExec`）：用户主余额 −x，**执行器主余额 +x（币实际托管处）**，执行器区域（`-exec-<addr>:<用户>`）**frozen/balance +x（负债明细）**。
  - **区域不是币**：区域只是"谁在某个执行器下有多少"的记账，币在执行器主余额。
- kvmvcc 存储键格式：`.-mvcc-.d.<逻辑键>.<20位版本号>`（版本号=区块高度），同一逻辑键在不同高度有多条记录，**最新版本即最终状态**。

### 2.4 ticket 共识（bityuan 风格）
- 持票挖矿（VRF 证明），票数越多挖矿概率越大；
- `[mver.consensus.ticket] targetTimePerBlock` 控制目标出块时间（难度自动调节）；
- 奖励：挖矿块奖励进**委托人（ReturnAddress）的 ticket 区域**；
- 票状态：Opened（可挖）→ Mined（已挖）→ Closed（关闭取回）。

### 2.5 重放防护
chain33 执行交易时校验 `tx.ChainID == cfg.GetChainID()`（ycc 链 minFee>0 恒生效）。新链改用**新的 chainID** 后，旧链的任何交易在新链都会因 chainID 不匹配被拒绝。

---

## 3. 迁移方案总体设计

### 3.1 思路：离线导出 → 状态转换 → 新链预载
```
旧链 kvmvcc/store.db
   │ ① export（只读遍历，取每键最新版本）
   ▼
dump.gob（全量最终状态快照：293.3 万键）
   │ ② transform（pos33→ticket 状态转换，总量守恒）
   ▼
state.bin（新链状态键） + local.bin（新链本地票列表） + report/summary（校验报告）
   │ ③ verify（差异校验：只改了预期键）
   ▼
   │ ④ build（写入新 datadir）
   ▼
new-ycc/kvmvcc/store.db（状态版本0 预载） + new-ycc/blockchain.db（LODB 票列表预载）
   │ ⑤ 新链节点启动（ticket 共识 + 新 chainID + 新 powLimitBits）
   ▼
高度 0 = 迁移状态 → 矿工导入私钥挖矿（5s 出块）
```

### 3.2 关键机制：新链创世即迁移状态（零框架改动）
- 新链创世块**无交易**：空变更集 → kvmvcc 自动注册"版本 0"（stateHash = 空变更集哈希，`0xdd0ad3…`）。
- 预载的版本 0 数据 = 创世状态（全部余额/票/dapp 状态）。
- ticket 共识从高度 1 正常出块，执行器按正常路径读写状态。
- 全程**不需要修改 chain33 框架**；仅 fork ycc 的 ticket 共识（1 处签名适配，见 §5.1）。

### 3.3 转换原则：总量守恒
迁移只"搬运"币，不增发、不销毁：
- pos33 执行器主余额的质押托管 → 按区域明细退还原告人；
- 质押 → 按票价折算成 ticket（票值托管进 ticket 执行器主余额）；
- 记账未发奖励 → 发放；
- 工具内置守恒校验（coins 主余额合计前后必须相等），不守恒即失败。

---

## 4. 迁移流程与工具详解

工具源码在 ycc 仓库 `migrate/` 目录，全部为 Go 编写、**静态链接**（`CGO_ENABLED=0`，linux-amd64，服务器零依赖）。编译脚本 `migrate/build.sh` 一键构建 8 个工具。

| 工具 | 作用 | 输入 → 输出 |
|---|---|---|
| **export** | 旧链状态导出 | 旧节点 `kvmvcc/store.db`（leveldb 只读）→ `dump.gob`（全量最终状态） |
| **transform** | pos33→ticket 状态转换（核心） | `dump.gob` → `state.bin`/`local.bin`/`report.txt`/`summary.json` |
| **verify** | 转换差异校验 | `dump.gob` + `state.bin` → 违规报告（0 违规才通过） |
| **build** | 新链 datadir 构建 | `state.bin`/`local.bin` → `new-ycc/`（store.db + blockchain.db） |
| **calib** | 挖矿难度标定 | 票数 + 目标出块时间 → `powLimitBits` |
| **pos33stat** | pos33 资金分布检查（诊断） | `dump.gob` → 执行器主余额/区域/奖励记账/异常地址 |
| **ticketdist** | 持票地址分布 Top N（诊断） | `dump.gob`/状态 → 地址/票数/委托矿工排名 |
| **gendump** | 合成测试数据（测试用） | 参数 → 模拟旧链 dump |
| **build.sh** | 一键静态编译以上 8 个工具 | — |

### 4.1 export —— 旧链状态导出
- **原理**：kvmvcc 存所有历史版本的变更键（`.-mvcc-.d.<键>.<版本>`）。遍历该前缀，同一逻辑键**后出现即更高版本**（20 位补零，字典序=数值序），覆盖保留最新值 = 最终状态。
- **输出 dump.gob**（gob 序列化）：
  ```go
  type Dump struct {
      Height    int64   // 导出锚定高度（= 最大版本号）
      StateHash []byte  // 该高度区块 stateHash（校验锚点）
      BlockTime int64   // 预留
      ChainID   int32   // 预留
      KVs []*types.KeyValue  // 全量最终状态：逻辑键 → 原始 proto 字节
  }
  ```
- 真实链实测：11.95 亿条变更 → 293.3 万键，445MB，约 3 分钟。

### 4.2 transform —— 状态转换（核心）
转换规则（严格总量守恒）：
1. **托管退还**：pos33 执行器**主余额**（质押托管的币）按区域明细（frozen+balance）退还质押人主余额；池不足按比例退并告警。
2. **记账奖励发放**：委托人 `RemainReward`、被委托人 `RemainFeeReward` → 主余额（从执行器主余额剩余扣）。
3. **质押折票**：每对（委托人,矿工）质押 S：票数 n = floor(S ÷ 票价)，票值 = n × 票价；主余额扣票值 → **ticket 执行器主余额 +票值（币托管）** + ticket 区域 frozen +票值（明细）；零头退回主余额。
4. **生成票状态**：每张票 `mavl-ticket-<矿工>:migrate:<全局序号>`（Status=Opened、IsGenesis=true、Price=票价）+ 本地票列表键（`LODB-ticket-tl:<矿工>:1:<票ID>`，钱包/共识挖矿依赖）。
5. **清理**：删除全部 `mavl-pos33-*` 键；其余状态（coins 主余额、evm/token/trade 等）原样保留。
6. **守恒校验**：coins 主余额合计（不含执行器区域）前后必须相等，否则失败。

### 4.3 verify —— 差异校验
逐键比对 dump.gob 与 state.bin，只允许：
- 删除：`mavl-pos33-*` 键 + pos33 区域币键；
- 修改：coins 主余额（托管退还/奖励/折票搬运）；
- 新增：`mavl-ticket-*` 票键、ticket 区域键、ticket 执行器主余额键。
其余任何键必须**逐字节一致**，违规即报错。真实链验证 0 违规。

### 4.4 build —— 新链 datadir 构建
把转换结果写入新节点 datadir：
- `kvmvcc/store.db`：每个状态键写入 `.-mvcc-.d.<键>.00000000000000000000`（版本 0 数据键，不写元信息——由节点启动时创世执行自动注册）；
- `blockchain.db`：LODB 本地票列表键（与区块同库）。
真实链实测：297.37 万状态键 + 4.11 万本地键。

### 4.5 calib —— 难度标定（解析公式）
ticket 挖矿概率被 compact 难度量化（currentdiff = CompactToBig(BigToCompact(sha256))，按哈希字节长度分档）。推导出**精确解析公式**：
```
P(成功) = Σ_{b<e} P(哈希字节长 b) + P(b=e) × clamp((mantissa−2^16)/(2^24−2^16), 0, 1)
出块时间 ≈ 1/(票数 × P)
```
按票数与目标 5s 反解 (指数, mantissa) → `powLimitBits`。真实链 40,645 票 → `0x1e528e0c`。之后难度每 `targetTimespan/targetTimePerBlock` 块自动调整收敛。

### 4.6 pos33stat —— 资金分布诊断
读 dump.gob 直接统计：pos33 执行器主余额（Bal/Frozen）、区域账户数/合计/分布、consignee 奖励记账（remainReward/remainFeeReward/feeReward）、状态键数量、异常地址检查。用于迁移前核对资金分布、发现数据异常。

### 4.7 gendump —— 合成测试数据
按参数生成模拟旧链 dump（账户、pos33 质押、奖励、其他 dapp 键），用于工具开发与全流程演练。

---

## 5. 过程中的关键发现与修复

真实链数据验证过程中，发现并修复了 **6 个问题**（前 2 个在合成验证阶段发现，后 4 个由真实数据暴露、经项目方质疑定位）：

### 5.1 ticket 共识 eth 地址签名（合成阶段）
- 现象：ticket 挖矿块执行报 `ErrNoBalance`。
- 根因：ticket 共识 `createMinerTx` 用 `tx.Sign(SECP256K1, priv)`，其 `tx.From()` 按签名类型映射为 **base58 地址**；ycc 链是 eth 地址账户体系，该地址余额为 0 → 矿工交易失败。
- 修复：fork ticket 共识，签名改为 `tx.Sign(EncodeSignID(SECP256K1, eth.ID))`（与 pos33 一致）。**需 fork 的原因**：ticket 共识原版面向 base58 链（bityuan），ycc 需适配。

### 5.2 maxTxNumber 缺失（合成阶段）
- 现象：同步节点收到区块报 `ErrManyTx`。
- 根因：`mver.consensus.maxTxNumber` 只在 ycc.toml（旧链运行配置），未进内嵌 ycc.go；新链同步节点若只带最小 toml，MaxTxNumber=0 → 任何含交易的区块被拒。
- 修复：`maxTxNumber=30000` 写入内嵌配置（随代码发布）。

### 5.3 质押托管在执行器主余额（真实数据，40 亿级）
- 现象：pos33 执行器主余额 ≈ 区域明细合计（均 41.25 亿 ycc，差仅 555 ycc）。
- 根因：chain33 执行器**双重记账**——用户质押经 `TransferToExec`：币进**执行器主余额**（资产），区域（frozen/balance）只是负债明细。原转换实现把区域明细当资产退回、把执行器主余额当"奖励池"留存 → **约 40 亿质押托管被错误锁死**。
- 修复：按区域明细从执行器**主余额**退还质押人；守恒口径改为 coins 主余额（**不含 -exec- 区域键**）。

### 5.4 FeeReward 重复发放（真实数据，1.22 亿）
- 现象：原实现"未领奖励"高达 1.22 亿 ycc。
- 根因：`FeeReward` 是**历史累计**（每次转账时从委托人奖励扣的 fee 记账，已随转账发过）；原实现把 `FeeReward + RemainFeeReward` 都当"未发"再次发放 → **凭空增发**。
- 修复：只发记账未发部分 `RemainReward`（委托人）+ `RemainFeeReward`（被委托人）。修正后奖励仅 280.19 ycc（真实未发）。

### 5.5 票 ID 冲突（真实数据，5,803 张票）
- 现象：verify 新增键 35,087 < 报告票数 40,645。
- 根因：票 ID 按（委托人,矿工）对从 0 编号（`矿工:migrate:%010d`）；真实链**同一矿工有 144 个委托人** → 各自生成相同 ID → state 键互相覆盖，**5,803 张票丢失**。
- 修复：票 ID 序号按**矿工全局累计**。修正后票键数 = 票数 = 40,645。

### 5.6 守恒口径（与 5.3 配套）
执行器区域是负债明细（币在主余额），若计入总流通量会**重复计数**。守恒校验统一为 **coins 主余额合计**（不含任何 `-exec-` 区域键），前后必须相等。

---

## 6. 真实链备份数据验证结果

备份 datadir（高度 143,872,889，约 38 天前的快照）完整跑通全流程：

| 环节 | 结果 |
|---|---|
| export | 11.95 亿条变更 → 293.3 万键，445MB，stateHash=716f8076... |
| transform | 票 **40,645** 张（票值 40.645 亿 ycc）、退款 83,000 ycc、奖励 280.19 ycc、pos33 留存 274.92 ycc、ticket 主余额 40.645 亿 |
| verify | **0 违规**；新增 40,884 = 票 40,645 + ticket 区域 238 + 主余额 1 |
| build | 状态键 2,973,678（= 原 2,933,430 − 删除 636 + 新增 40,884，精确对上） |
| 守恒（实测） | state.bin 主余额合计 **122.568447 亿 ycc == report 报告值，精确一致** |
| 起链验证 | 新链高度 0 正常；质押人 ticket 区域 = 折票额、主余额含退款；矿工 0xe887fa 名下 **14,121 张可挖票** |
| 出块机制 | 合成链实测 ~5s（ticket 共识 + 校准难度）；双节点同步一致 |

### 资金分布（迁移前，pos33 相关）
| 项 | 金额 | 说明 |
|---|---|---|
| pos33 执行器主余额 | 41.2458 亿 ycc | 质押托管（资产） |
| pos33 区域（294 账户） | 41.2458 亿（frozen 40.6458 + balance 0.6） | 质押/待提现明细（负债） |
| 记账奖励（未发） | 280.19 ycc | RemainReward + RemainFeeReward |

迁移后：质押全部变为票（40,645 张）+ 退款 83,000 ycc；票币托管在 ticket 执行器主余额；pos33 执行器仅剩增发净额 274.92 ycc。

---

## 7. 新链配置

### 7.1 内嵌配置（ycc.go，随代码发布）
```
ChainID = 1000（ycc.toml 可配，须 ≠999）   evmChainID = 4000
[consensus] name="ticket"  minerstart=true  minerExecs=["ticket"]
[mver.consensus] maxTxNumber=30000  powLimitBits=0x1e528e0c
[mver.consensus.ticket]
  coinReward=43  coinDevFund=26  ticketPrice=100000
  futureBlockTime=16  ticketFrozenTime=5  ticketWithdrawTime=10  ticketMinerWaitTime=2
  targetTimespan=2304  targetTimePerBlock=5  retargetAdjustmentFactor=4
[fork.sub.ticket] Enable=0  ForkTicketId=0  ForkTicketVrf=0
```

### 7.2 参数说明（2026-09-18 定稿）
| 参数 | 值 | 依据 |
|---|---|---|
| chainID | **1000（定稿）** | 防重放（≠999）；evmChainID 同步 4000 |
| ticketPrice | **100,000 ycc/张（定稿）** | 与 ycc pos33 一致（UseEntrust=0 → 全程第二档票价）；真实数据复核：40,645 张 × 100,000 = 40.645 亿 ycc |
| targetTimePerBlock | 5（定稿） | 目标 5s 出块 |
| powLimitBits | 0x1e528e0c | 按真实票数 40,645 解析公式标定（初始窗口），之后难度自调整 |
| coinReward | **43 ycc/块（定稿）** | 每块挖矿奖励（`miner.Reward = CoinReward + fee`，与共识校验一致）；进票的 ReturnAddress |
| coinDevFund | **26 ycc/块（定稿）** | 每块发展基金（`ForkTicketFundAddrV1=-1` → 恒启用：由 `ExecIssueCoins` 增发到 fundKeyAddr）。**必须写整数**：TOML 中写成 `26.0` 会解析为 float64，`MGInt` 走 `parseInt` 只认 int64 → 静默变 0 |
| 每块总增发 | **43 + 26 = 69 ycc / 5s = 13.8 ycc/s（≈4.35 亿/年）** | 与旧链 pos33（15 ycc/~1.09s = 13.8 ycc/s、4.34 亿/年）相近；43:26 ≈ bty 口径 **5:3**；推导见 `docs/migrate/REWARD.md` |

### 7.3 配置合并机制说明
chain33 的配置合并**不允许经 toml 覆盖内嵌标量**（"rewrite defalut key" 报错）。共识切换、maxTxNumber、powLimitBits 等**必须写入内嵌 ycc.go**，随二进制发布（与 ycc 现有约定一致："这部分配置随代码发布，不能修改"）。

---

## 8. 风险与注意事项

1. **2 张脏数据票**（链上历史数据）：1 处被委托人地址带全角逗号（20 万 ycc）、1 处地址拼接损坏（50 万 ycc）。票的 MinerAddress 无效不可挖，但委托人（ReturnAddress）有效，上线后可 close 取回。属链上既有数据问题，非迁移引入。
2. **pos33 执行器留存 274.92 ycc**：增发净额（历史区块奖励发放后的残余），留存于 pos33 执行器地址，pos33 退役后不可领取。金额极小，可接受。
3. **无历史区块**：新链只有状态，交易历史/地址索引查询为空（"只保留状态"的固有结果）。
4. **矿工收益模式变化**：ticket 模型下挖矿奖励进委托人（ReturnAddress）的 ticket 区域，矿工无直接块奖励分成（pos33 有 fee_persent）。属 ticket 固有设计（bityuan 同款），需项目组知悉。
5. **双地址格式**：链上存在 eth（0x…）与 base58 两种地址（3495 个 base58 账户），均正常保留。
6. **备份快照时效**：当前验证用的是 38 天前备份（高度 143,872,889）；正式迁移需用**最新同步**的 datadir 重新导出（后续余额变化才会包含）。

---

## 9. 部署步骤与后续计划

### 9.1 部署步骤（详见 RUNBOOK）
1. **状态导出**（旧节点服务器）：`export -store <datadir>/kvmvcc/store.db -out dump.gob`
2. **转换**：`transform -in dump.gob -out out/ -symbol ycc -ticketPrice 100000` → 检查 report（守恒 OK、票数、无复核告警）
3. **差异校验**：`verify -in dump.gob -state out/state.bin -symbol ycc`（0 违规）
4. **难度标定**：`calib -tickets <票数> -targetSec 5`（或直接用 transform 输出建议值）
5. **构建 datadir**：`build -datadir <新节点datadir> -state out/state.bin -local out/local.bin`
6. **部署新链**：全节点升级新二进制（ticket 共识 + 新配置）+ 全新 datadir；矿工导入旧私钥（addressID=2）→ 解锁（walletOrTicket=true）开始挖矿
7. **上线验证**：出块 ~5s、余额/票抽查与旧链一致、总量守恒、多节点同步一致、旧交易重放被拒

### 9.2 后续计划
- [x] 参数定稿（2026-09-18）：chainID=1000 / evmChainID=4000 / coinReward=43 / coinDevFund=26（5:3，总增发 69 ycc/块 ≈ 旧链）/ ticketPrice=100000（10 万 ycc/张）
- [ ] 正式迁移用最新 datadir 重新导出并全流程复验
- [ ] 双节点/多节点新链同步验证（真实状态）
- [ ] 脏数据票处理脚本（close）
- [ ] 上线切换（新旧链并行的回退预案已在 RUNBOOK）

---

## 附录 A：交付物清单（2026-09-18 路径同步）

| 交付物 | 位置 |
|---|---|
| 新链代码（ticket fork + 内嵌配置） | `~/work/src/chain/ycc-project/ycc/`（分支 `new-ycc`；补丁已应用并提交：7c3b751/3687ce4） |
| 工具源码 | ycc 仓库 `migrate/`（export/transform/build/verify/calib/gendump/pos33stat/ticketdist + build.sh） |
| 静态二进制 | `~/work/src/chain/ycc-project/ycc-testdata/static-binaries/`（8 个，linux-amd64 零依赖） |
| 设计文档 | ycc 仓库 `docs/migrate/DESIGN.md` |
| 操作手册 | ycc 仓库 `docs/migrate/RUNBOOK.md` |
| 收益参数定稿推导 | ycc 仓库 `docs/migrate/REWARD.md` |
| 真实链验证结果 | `~/work/src/chain/ycc-project/ycc-testdata/test-result4/`（report/summary/state.bin/local.bin/new-ycc datadir） |
| 索引 | ycc 仓库 `docs/migrate/00-INDEX.md` |
| 本汇报 | ycc 仓库 `docs/migrate/REPORT.md` |

## 附录 B：构建环境
- Go 1.18（ycc 依赖 quic-go 要求 go≤1.18）
- 静态编译：`CGO_ENABLED=0`；工具链路径可用环境变量覆盖（见 build.sh）
- 节点构建：`go build -o ycc-node .`（含 ticket 共识）
