# ycc 链"减少存储重跑"迁移 —— 真实环境操作手册（RUNBOOK）

> 前置：已完成设计验证（DESIGN.md）与本地端到端验证。本手册用于在真实 ycc 链上执行迁移。
> 原则：**总量守恒、可回退、分阶段验证**。执行前务必在测试环境完整演练一遍（见 §6）。

## 0. 角色与前置

- 需要：一台可访问**已同步旧节点 datadir** 的服务器（导出）、N 台新链节点服务器。
- 旧链信息（已确认）：chainID=999、CoinSymbol="ycc"、Title="pos33"、共识 pos33、存储 kvmvcc（datadir/kvmvcc/store.db）。
- 工具：migrate/（export/transform/build/calib），go1.18 构建（`GOROOT=<go1.18> GOMODCACHE=<cache> go build -o bin/ ./migrate/...`）。
- 新链代码：ycc 仓库应用 `01-ycc-code-changes.patch`（ticket fork + eth 签名修复 + 内嵌配置）。

## 1. 阶段一：状态导出（在旧节点服务器）

```bash
# 停旧节点写操作（或接受滚动状态：导出结果 = 导出时刻的最新状态）
# 记录当前高度与 stateHash 作锚点：
#   curl https://yuan.org/yccApi -d '{"jsonrpc":"2.0","method":"Chain33.GetLastHeader","params":[],"id":1}'
export -store <datadir>/kvmvcc/store.db -out dump.gob
# 输出：键数、maxVersion(≈高度)、stateHash
```

> 注意：真实链 ~1.47 亿版本、数亿条变更，导出需数十分钟~数小时；**内存**：export/transform 均在内存持有最终状态（主余额/pos33 记录/其他 dapp 键），按真实链规模估算 1~5GB，服务器内存建议 ≥16GB（状态 2~3 倍以上）；磁盘：store.db 为旧链状态库全量（数十 GB），确保导出盘空间足够。校验 dump 的 stateHash 与导出时刻 GetLastHeader 一致。

## 2. 阶段二：状态转换（任意机器，离线）

```bash
transform -in dump.gob -out out/ -symbol ycc -ticketPrice 100000
# 检查 out/report.txt：
#   - "总量守恒校验 ... OK 守恒"（必须）
#   - 票数/退款/奖励明细合理
#   - 无 "⚠ 需人工复核"（若有，见 §5）
# 检查 out/summary.json：票数、建议powLimitBits
```

**转换策略参数**（如与默认不同，需用户确认）：
- `-ticketPrice 100000`：票面价（用户明确沿用 ycc）。
- 质押→票：floor(S/10000) 张，零头退回委托人；未领奖励（委托人 remainReward / 被委托人 fee 分成）从 pos33 奖励池发放，池剩余留存 pos33 执行器地址。
- 所有余额/区域仅搬运不增减 → 总量守恒（工具强制校验，不守恒即失败）。

## 3. 阶段三：难度标定（离线）

```bash
calib -tickets <summary.json 里的票数> -targetSec 5
# 输出建议 powLimitBits（如 0x1f0a0000），写入 ycc.go 内嵌配置：
#   [mver.consensus] powLimitBits = "0x1f0a0000"
# 重建节点二进制。
```

> 说明：ticket 挖矿初始出块时间 ≈ 1/(票数×P)，P 由 powLimitBits 决定（compact 难度量化，需模拟标定）；此后难度每 460 块（targetTimespan/targetTimePerBlock）自动调整收敛到 5s。

## 4. 阶段四：构建新链 datadir 并部署

```bash
# 每台新节点：
build -datadir <新节点datadir> -state out/state.bin -local out/local.bin
# 生成：<datadir>/kvmvcc/store.db（状态版本0预载）、<datadir>/blockchain.db（LODB 票列表预载）
```

**新链配置**（03-new-chain-config-sample.toml 为测试样例；生产以 ycc.go 内嵌配置为准）：
- ChainID=1000（防重放，须 ≠999）、CoinSymbol="ycc"、[crypto.sub.secp256k1eth] evmChainID=4000
- [consensus] name="ticket"、minerExecs=["ticket"]（内嵌 ycc.go 已改）
- [consensus.sub.ticket]：genesisBlockTime=迁移时刻、无 genesis 票（状态预载）
- [mver.consensus.ticket]：coinReward=75、coinDevFund=1、ticketPrice=100000、targetTimePerBlock=5（5 秒出块）、其余按设计
- p2p：新网络（新 seeds/端口/频道，与旧链隔离）

**节点启动顺序**：
1. 先启动 1 个节点（全新 datadir），确认创世块生成（高度 0、无交易、stateHash=空变更集哈希）。
2. 用 RPC 校验余额（与旧链对照）与票（ticket.TicketList / TicketInfos）。
3. 其余节点以同二进制+同配置+同 datadir 预载加入（p2p 同步）。

**矿工准备**（每台矿工节点）：
```bash
# 钱包初始化（RPC 或 cli）：
Chain33.SaveSeed      {seed:"<12词助记词>", passwd:"<密码>"}
Chain33.UnLock        {passwd, timeout:0}                    # 先普通解锁
Chain33.ImportPrivkey {privkey:"<旧矿工私钥hex>", label:"miner", passwd, addressID:2}   # addressID=2=eth
Chain33.UnLock        {passwd, timeout:0, walletOrTicket:true}  # 解锁票 → 触发挖矿
# 验证：Chain33.GetWalletStatus → isAutoMining=true 或日志出现 "FindBlock"
```

## 5. 阶段五：上线后验证清单

| # | 检查项 | 方法 |
|---|--------|------|
| 1 | 出块正常、间隔 ~5s | `Chain33.GetLastHeader` 连续采样 blockTime 差 |
| 2 | 余额与旧链一致（抽样） | `Chain33.GetBalance` 对照旧链 RPC |
| 3 | 总量守恒 | 工具报告 + 上线后抽样 |
| 4 | 票可挖、奖励发放 | `ticket.TicketInfos`（status 1→2）、委托人 ticket 区域 frozen 增长 |
| 5 | pos33 状态已清理 | `Chain33.Query{execer:"pos33",...}` → ErrNotFound |
| 6 | 旧交易重放被拒 | 用旧链构造的交易在新区提交 → 应失败（chainID 校验） |
| 7 | 出块难度收敛 | 数个难度调整周期后 blockTime 稳定 ~5s |
| 8 | 多节点一致 | 各节点 GetLastHeader 高度与 stateHash 一致（已本地双节点验证） |

**回退预案**：旧链 datadir 原样保留，新链独立网络（新 chainID/新 p2p），迁移失败可随时回到旧链运行；新链无需与旧链共存协议。

## 6. 上线前演练（强烈建议）

在测试环境完整跑一遍：`gendump` 生成合成旧状态（或本地起 pos33 测试链）→ export → transform → build → 启动新链 → 走 §5 清单。本仓库已用该方法完成验证（DESIGN.md §6）。

## 7. 已知注意项

- 矿工无直接块奖励分成（ticket 奖励全进委托人 ticket 区域）——与 pos33 fee_persent 不同，属 ticket 模型固有设计，需用户知悉。
- 新链无历史区块：交易历史/地址索引查询为空（"只保留状态"的固有结果）。
- 旧钱包 DB 不可复用，需重新导入私钥（addressID=2=eth）。
- pos33 奖励池剩余（未发放的 maker/voter 份额等）留存 pos33 执行器地址，pos33 退役后不可领取——如需处理需另行治理决策。
