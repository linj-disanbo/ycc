// transform 工具：pos33 → ticket 状态转换（总量守恒）。
//
// 用法：
//
//	transform -in dump.gob -out out/ [-ticketPrice 100000]
//
// 产出：
//	out/state.bin   : 新链 store 状态键（value 为最终值；需写入 kvmvcc 版本0 数据键）
//	out/local.bin   : 新链 localdb 键（LODB-ticket-*；需写入 blockchain.db）
//	out/report.txt  : 转换报告（总量守恒校验、票数、退款明细等）
package main

import (
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/33cn/chain33/common/address"
	"github.com/33cn/chain33/system/address/eth"
	"github.com/33cn/chain33/types"
	tickettypes "github.com/33cn/plugin/plugin/dapp/ticket/types"
	"github.com/yccproject/ycc/migrate"
	pos33types "github.com/yccproject/ycc/plugin/dapp/pos33/types"
)

const (
	coinSymbol   = "ycc"
	coinExec     = "coins"
	ticketExec   = "ticket"
	pos33Exec    = "pos33"
	pos33Prefix  = "mavl-pos33-"
	ticketPrefix = "mavl-ticket-"
	// 本地票列表键前缀（ticket 执行器 exec_local 写入格式）
	localTicketPrefix = "LODB-ticket-tl:"
	localBindPrefix   = "LODB-ticket-bind:"
	localMinerPrefix  = "LODB-ticket-miner:"
)

func main() {
	in := flag.String("in", "dump.gob", "导出文件")
	outDir := flag.String("out", "out", "输出目录")
	ticketPrice := flag.Int64("ticketPrice", 100000, "新链票面价（ycc 单位；pos33 UseEntrust=0 → 全程 ticketPrice2=100000）")
	createTime := flag.Int64("createTime", 0, "票创建时间（Unix 秒，0=使用 dump 的 BlockTime）")
	symbol := flag.String("symbol", "ycc", "币符号（如 ycc / AS）")
	flag.Parse()

	f, err := os.Open(*in)
	if err != nil {
		log.Fatalf("打开 dump 失败: %v", err)
	}
	var dump migrate.Dump
	if err := gob.NewDecoder(f).Decode(&dump); err != nil {
		log.Fatalf("解析 dump 失败: %v", err)
	}
	f.Close()
	log.Printf("dump: 高度=%d stateHash=%x 键数=%d", dump.Height, dump.StateHash, len(dump.KVs))
	if *createTime == 0 {
		*createTime = dump.BlockTime
	}
	if *createTime == 0 {
		*createTime = 1652797628 // 兜底：旧链创世时间
	}

	// 执行器地址（eth 驱动，与 ycc 线上一致）
	pos33ExecAddr := execAddrOf(pos33Exec)
	ticketExecAddr := execAddrOf(ticketExec)
	log.Printf("pos33 执行器地址=%s ticket 执行器地址=%s", pos33ExecAddr, ticketExecAddr)

	tr := &transformer{
		state:          make(map[string][]byte),
		ticketSeq:      make(map[string]int64),
		price:          *ticketPrice * 100000000, // ycc 单位 → 最小单位（1e8）
		createTime:     *createTime,
		pos33ExecAddr:  pos33ExecAddr,
		ticketExecAddr: ticketExecAddr,
		symbol:         *symbol,
	}
	tr.mainPrefix = "mavl-coins-" + tr.symbol + "-"
	tr.execPrefix = "mavl-coins-" + tr.symbol + "-exec-"
	for _, kv := range dump.KVs {
		tr.state[string(kv.Key)] = kv.Value
	}

	if err := tr.run(); err != nil {
		log.Fatalf("转换失败: %v", err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("创建输出目录失败: %v", err)
	}
	// 写 state.bin / local.bin（gob of []*types.KeyValue）
	if err := writeKVFile(*outDir+"/state.bin", tr.stateKVs); err != nil {
		log.Fatal(err)
	}
	if err := writeKVFile(*outDir+"/local.bin", tr.localKVs); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*outDir+"/report.txt", []byte(tr.report.String()), 0o644); err != nil {
		log.Fatal(err)
	}
	// 新链配置片段（ticket genesis 不需要：状态已含全部票）
	cfg := map[string]interface{}{
		"chainID":            1000, // 2026-09-18 定稿（≠999，防重放）
		"consensus":          "ticket",
		"minerExecs":         []string{"ticket"},
		"ticketPrice(ycc)":   *ticketPrice,
		"targetTimePerBlock": "5（5 秒出块）",
		"pos33ExecAddr":      pos33ExecAddr,
		"ticketExecAddr":     ticketExecAddr,
		"票数":                 tr.ticketCount,
		"票价值(最小单位)":          tr.ticketValue,
		"质押退款(最小单位)":         tr.refundTotal,
		"奖励发放(最小单位)":         tr.rewardTotal,
		"pos33执行器余额留存(最小单位)":      tr.pos33PoolLeft,
		"ticket执行器主余额(票值托管,最小单位)": tr.ticketValue,
		"建议powLimitBits":          recommendBits(tr.ticketCount),
	}
	cfgJSON, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(*outDir+"/summary.json", cfgJSON, 0o644)
	log.Printf("完成：票数=%d 票价值=%d 退款=%d 奖励=%d 输出目录=%s", tr.ticketCount, tr.ticketValue, tr.refundTotal, tr.rewardTotal, *outDir)
}

func execAddrOf(name string) string {
	addr, err := address.GetExecAddress(name, eth.ID)
	if err != nil {
		panic(err)
	}
	return addr
}

type transformer struct {
	state map[string][]byte // 逻辑键 -> 值（转换中就地修改）

	price          int64 // 票面价（最小单位）
	createTime     int64
	pos33ExecAddr  string
	ticketExecAddr string
	symbol         string
	mainPrefix     string
	execPrefix     string

	stateKVs []*types.KeyValue
	localKVs []*types.KeyValue

	ticketCount   int64
	ticketValue   int64
	refundTotal   int64
	rewardTotal   int64
	pos33PoolLeft int64
	// 票 ID 序号按矿工全局累计（同一矿工多个委托人时 ID 必须唯一，否则 state 键覆盖丢票）
	ticketSeq map[string]int64
	report    reportBuilder
}

func (tr *transformer) run() error {
	before := tr.totalSupply()
	// 1) 统计并移动 pos33 执行器区域全部币（质押+未发放奖励池）
	if err := tr.movePos33Region(); err != nil {
		return err
	}
	// 2) 发放未领奖励（委托人 remainReward、被委托人 fee 分成），从 pos33 执行器主余额扣减
	if err := tr.payRewards(); err != nil {
		return err
	}
	// 3) 质押折票
	if err := tr.stakeToTickets(); err != nil {
		return err
	}
	// 4) 删除全部 pos33 状态键
	tr.deletePos33Keys()
	// 5) 输出（排序保证确定性）
	tr.emit()
	after := tr.totalSupply()
	tr.report.addSummary(before, after, tr.ticketCount, tr.ticketValue, tr.refundTotal, tr.rewardTotal, tr.pos33PoolLeft)
	if before != after {
		return fmt.Errorf("总量守恒校验失败：before=%d after=%d", before, after)
	}
	return nil
}

// recommendBits 按票数与目标 5s 出块返回建议 powLimitBits 字符串
func recommendBits(tickets int64) string {
	if tickets <= 0 {
		return "0x1f00ffff（无票，无需挖矿）"
	}
	bits, _ := migrate.RecommendPowLimitBits(tickets, 5.0)
	return fmt.Sprintf("0x%08x", bits)
}

// totalSupply 统计 coins【主余额】之和（不含任何 -exec- 区域键）。
// 说明：执行器区域（mavl-coins-ycc-*-exec-<exec>:<addr>）是负债明细，币实际托管在对应执行器
// 【主余额】（TransferToExec 会计模型）；区域键的 balance/frozen 与主余额是同一笔币的双重记账，
// 若计入会造成重复计数。
func (tr *transformer) totalSupply() int64 {
	total := int64(0)
	for k, v := range tr.state {
		if len(k) > len(tr.mainPrefix) && k[:len(tr.mainPrefix)] == tr.mainPrefix && !strings.Contains(k, "-exec-") {
			acc := &types.Account{}
			if err := types.Decode(v, acc); err == nil {
				total += acc.Balance + acc.Frozen
			}
		}
	}
	return total
}

// parseMainKey 解析 "mavl-coins-ycc-<addr>" → addr
func (tr *transformer) parseMainKey(k string) (string, bool) {
	if len(k) <= len(tr.mainPrefix) {
		return "", false
	}
	if k[:len(tr.mainPrefix)] != tr.mainPrefix {
		return "", false
	}
	return k[len(tr.mainPrefix):], true
}

// parseExecKey 解析 "mavl-coins-ycc-exec-<execaddr>:<addr>" → (execaddr, addr)
func (tr *transformer) parseExecKey(k string) (string, string, bool) {
	if len(k) <= len(tr.execPrefix)+2 {
		return "", "", false
	}
	if k[:len(tr.execPrefix)] != tr.execPrefix {
		return "", "", false
	}
	rest := k[len(tr.execPrefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == ':' {
			return rest[:i], rest[i+1:], true
		}
	}
	return "", "", false
}

// accountValue 解码账户；不存在返回 nil
func (tr *transformer) accountValue(key string) *types.Account {
	v, ok := tr.state[key]
	if !ok {
		return nil
	}
	acc := &types.Account{}
	if err := types.Decode(v, acc); err != nil {
		return nil
	}
	return acc
}

func (tr *transformer) setAccount(key string, acc *types.Account) {
	if acc.Balance == 0 && acc.Frozen == 0 && acc.Currency == 0 {
		delete(tr.state, key)
		return
	}
	tr.state[key] = types.Encode(acc)
}

// movePos33Region 退还 pos33 质押托管：
// 币实际托管在 pos33 执行器【主余额】（TransferToExec 打入），区域键（frozen+balance）是负债明细。
// 按区域明细把托管币退回质押人主余额（从执行器主余额扣减），并删除区域键。
// 若执行器主余额不足以覆盖区域明细（异常），按比例退回并记录警告。
func (tr *transformer) movePos33Region() error {
	var keys []string
	for k := range tr.state {
		if ea, _, ok := tr.parseExecKey(k); ok && ea == tr.pos33ExecAddr {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	// 区域明细：持有人 -> 应得（frozen+balance）
	type claim struct {
		addr string
		amt  int64
	}
	var claims []claim
	var claimsTotal int64
	for _, k := range keys {
		_, addr, _ := tr.parseExecKey(k)
		acc := tr.accountValue(k)
		amt := int64(0)
		if acc != nil {
			amt = acc.Balance + acc.Frozen
		}
		if amt > 0 {
			claims = append(claims, claim{addr, amt})
			claimsTotal += amt
		}
		delete(tr.state, k)
	}
	if claimsTotal == 0 {
		return nil
	}
	// 执行器主余额（托管池）
	poolKey := tr.mainPrefix + tr.pos33ExecAddr
	pool := tr.accountValue(poolKey)
	poolBal := int64(0)
	if pool != nil {
		poolBal = pool.Balance + pool.Frozen
	}
	ratio := 1.0
	if poolBal < claimsTotal {
		ratio = float64(poolBal) / float64(claimsTotal)
		tr.report.flag = append(tr.report.flag,
			fmt.Sprintf("pos33 执行器主余额 %d 小于区域明细合计 %d，托管退回按比例 %.4f 执行", poolBal, claimsTotal, ratio))
	}
	var paid int64
	for _, c := range claims {
		amt := int64(float64(c.amt) * ratio)
		tr.creditMain(c.addr, amt)
		paid += amt
		tr.report.addLine("pos33托管退回", c.addr, amt, "质押托管退还（区域明细）")
	}
	// 执行器主余额扣减
	if pool != nil {
		pool.Balance = pool.Balance - paid
		if pool.Balance < 0 {
			pool.Balance = 0
		}
		pool.Frozen = 0
		tr.setAccount(poolKey, pool)
	}
	tr.report.addLine("pos33托管池", tr.pos33ExecAddr, paid, "已按区域明细退还质押人")
	return nil
}

// payRewards 发放记账未领奖励：委托人 RemainReward → 主余额；被委托人 RemainFeeReward → 主余额。
// 注意：FeeReward 是历史累计（已随转账发过），不在此发放，避免凭空增发。
// 从 pos33 执行器主余额（托管+增发净额）扣减（若不足则按可用发放）。
func (tr *transformer) payRewards() error {
	poolKey := tr.mainPrefix + tr.pos33ExecAddr
	pool := tr.accountValue(poolKey)
	poolBal := int64(0)
	if pool != nil {
		poolBal = pool.Balance + pool.Frozen
	}

	var credits []credit
	for k, v := range tr.state {
		if len(k) <= len(pos33Prefix)+len("consignee-") {
			continue
		}
		if k[:len(pos33Prefix)] != pos33Prefix || k[len(pos33Prefix):len(pos33Prefix)+len("consignee-")] != "consignee-" {
			continue
		}
		consignee := &pos33types.Pos33Consignee{}
		if err := types.Decode(v, consignee); err != nil {
			return fmt.Errorf("解码 consignee %s 失败: %v", k, err)
		}
		for _, cr := range consignee.Consignors {
			if cr.RemainReward > 0 {
				credits = append(credits, credit{addr: cr.Address, amount: cr.RemainReward, why: "委托人未领奖励(记账)"})
			}
		}
		// 只发 RemainFeeReward（待转 fee）；FeeReward 是历史累计（已随转账发过），重复发放会凭空增发
		if consignee.RemainFeeReward > 0 {
			credits = append(credits, credit{addr: consignee.Address, amount: consignee.RemainFeeReward, why: "被委托人fee待转(记账)"})
		}
	}
	total := int64(0)
	for _, c := range credits {
		total += c.amount
	}
	if total > poolBal {
		// 理论不应发生：池不足以覆盖记账奖励。保守处理：按记账发放，池归零，报告标记。
		tr.report.addLine("警告", "pos33池", poolBal, fmt.Sprintf("记账奖励 %d 超过池余额，按记账发放", total))
		tr.report.flag = append(tr.report.flag, "pos33 执行器主余额不足以覆盖未领奖励记账，需人工复核")
	}
	for _, c := range credits {
		tr.creditMain(c.addr, c.amount)
		tr.rewardTotal += c.amount
		tr.report.addLine("奖励", c.addr, c.amount, c.why)
	}
	// 池扣减后剩余
	left := poolBal - total
	if left < 0 {
		left = 0
	}
	tr.pos33PoolLeft = left
	if pool != nil {
		pool.Balance = left
		pool.Frozen = 0
		tr.setAccount(poolKey, pool)
	}
	return nil
}

type credit struct {
	addr   string
	amount int64
	why    string
}

func (tr *transformer) creditMain(addr string, amount int64) {
	if amount <= 0 {
		return
	}
	mainKey := tr.mainPrefix + addr
	acc := tr.accountValue(mainKey)
	if acc == nil {
		acc = &types.Account{Addr: addr}
	}
	acc.Balance += amount
	tr.setAccount(mainKey, acc)
}

// stakeToTickets 每个 (被委托人, 委托人) 的质押按票面价折票。
func (tr *transformer) stakeToTickets() error {
	var pairs []stakePair
	for k, v := range tr.state {
		if len(k) <= len(pos33Prefix)+len("consignee-") {
			continue
		}
		if k[:len(pos33Prefix)] != pos33Prefix || k[len(pos33Prefix):len(pos33Prefix)+len("consignee-")] != "consignee-" {
			continue
		}
		consignee := &pos33types.Pos33Consignee{}
		if err := types.Decode(v, consignee); err != nil {
			return fmt.Errorf("解码 consignee %s 失败: %v", k, err)
		}
		for _, cr := range consignee.Consignors {
			if cr.Amount <= 0 {
				continue
			}
			pairs = append(pairs, stakePair{miner: consignee.Address, consignor: cr.Address, stake: cr.Amount})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].miner != pairs[j].miner {
			return pairs[i].miner < pairs[j].miner
		}
		return pairs[i].consignor < pairs[j].consignor
	})
	for _, p := range pairs {
		if err := tr.convertPair(p); err != nil {
			return err
		}
	}
	return nil
}

type stakePair struct {
	miner     string
	consignor string
	stake     int64
}

func (tr *transformer) convertPair(p stakePair) error {
	n := p.stake / tr.price
	ticketValue := n * tr.price
	refund := p.stake - ticketValue
	if n <= 0 {
		// 质押不足一张票：全部退回主余额（已在 movePos33Region 完成），记退款
		tr.refundTotal += p.stake
		tr.report.addLine("退款(不足一张票)", p.consignor, p.stake, "委托给 "+p.miner)
		return nil
	}
	// 主余额扣回票面价值
	mainKey := tr.mainPrefix + p.consignor
	acc := tr.accountValue(mainKey)
	if acc == nil {
		return fmt.Errorf("consignor %s 主余额缺失（stake=%d n=%d）", p.consignor, p.stake, n)
	}
	acc.Balance -= ticketValue
	tr.setAccount(mainKey, acc)
	// ticket 执行器【主余额】+票值（票的币托管处，与正常 ticket 运行一致）
	ticketMainKey := tr.mainPrefix + tr.ticketExecAddr
	tmacc := tr.accountValue(ticketMainKey)
	if tmacc == nil {
		tmacc = &types.Account{Addr: tr.ticketExecAddr, Balance: 0}
	}
	tmacc.Balance += ticketValue
	tr.setAccount(ticketMainKey, tmacc)
	// ticket 执行器区域 frozen（负债明细）
	execKey := tr.execPrefix + tr.ticketExecAddr + ":" + p.consignor
	eacc := tr.accountValue(execKey)
	if eacc == nil {
		eacc = &types.Account{Addr: p.consignor, Frozen: 0}
	}
	eacc.Frozen += ticketValue
	tr.setAccount(execKey, eacc)
	// 生成票（ID 序号按矿工全局累计，避免同矿工多委托人冲突）
	prefix := p.miner + ":migrate:"
	seq := tr.ticketSeq[p.miner]
	for i := int64(0); i < n; i++ {
		id := prefix + fmt.Sprintf("%010d", seq+i)
		t := &tickettypes.Ticket{
			TicketId:      id,
			Status:        tickettypes.TicketOpened,
			IsGenesis:     true,
			MinerAddress:  p.miner,
			ReturnAddress: p.consignor,
			MinerValue:    0,
			CreateTime:    tr.createTime,
			Price:         tr.price,
		}
		tr.state[ticketPrefix+id] = types.Encode(t)
		// 本地票列表（miner 维度，状态=1 可挖）
		tr.localKVs = append(tr.localKVs, &types.KeyValue{
			Key:   []byte(fmt.Sprintf("%s%s:1:%s", localTicketPrefix, string(address.FormatAddrKey(p.miner)), id)),
			Value: []byte(id),
		})
	}
	// 绑定关系（便于委托人 force-close）
	tr.localKVs = append(tr.localKVs,
		&types.KeyValue{Key: []byte(fmt.Sprintf("%s%s", localBindPrefix, string(address.FormatAddrKey(p.consignor)))), Value: []byte(p.miner)},
		&types.KeyValue{Key: []byte(fmt.Sprintf("%s%s:%s", localMinerPrefix, string(address.FormatAddrKey(p.miner)), string(address.FormatAddrKey(p.consignor)))), Value: []byte(p.consignor)},
	)
	tr.ticketCount += n
	tr.ticketValue += ticketValue
	tr.refundTotal += refund
	tr.ticketSeq[p.miner] = seq + n
	tr.report.addLine("折票", p.consignor+"→"+p.miner, p.stake, fmt.Sprintf("票数=%d 票值=%d 退款=%d", n, ticketValue, refund))
	return nil
}

// deletePos33Keys 删除全部 mavl-pos33-* 键
func (tr *transformer) deletePos33Keys() {
	for k := range tr.state {
		if len(k) >= len(pos33Prefix) && k[:len(pos33Prefix)] == pos33Prefix {
			delete(tr.state, k)
		}
	}
}

func (tr *transformer) emit() {
	keys := make([]string, 0, len(tr.state))
	for k := range tr.state {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		tr.stateKVs = append(tr.stateKVs, &types.KeyValue{Key: []byte(k), Value: tr.state[k]})
	}
	sort.Slice(tr.localKVs, func(i, j int) bool { return string(tr.localKVs[i].Key) < string(tr.localKVs[j].Key) })
}

func writeKVFile(path string, kvs []*types.KeyValue) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(kvs)
}

// reportBuilder 简单文本报告
type reportBuilder struct {
	lines   []string
	flag    []string
	summary string
}

func (r *reportBuilder) addSummary(before, after, ticketCount, ticketValue, refund, reward, poolLeft int64) {
	r.summary = fmt.Sprintf(`
================================
总量守恒校验（主余额口径，不含执行器区域明细）:
  迁移前总流通量 = %d
  迁移后总流通量 = %d
  差额                                 = %d  (%s)
--------------------------------
  生成票数             = %d
  票价值(最小单位)      = %d
  质押退款(最小单位)     = %d
  未领奖励发放(最小单位) = %d
  pos33执行器留存(最小单位)= %d
`, before, after, before-after,
		map[bool]string{true: "OK 守恒", false: "✗ 不守恒！"}[before == after],
		ticketCount, ticketValue, refund, reward, poolLeft)
}

func (r *reportBuilder) addLine(cat, target string, amount int64, note string) {
	r.lines = append(r.lines, fmt.Sprintf("%-8s %-48s %20d  %s", cat, target, amount, note))
}

func (r *reportBuilder) String() string {
	s := "ycc pos33→ticket 迁移转换报告\n"
	s += "================================\n"
	for _, l := range r.lines {
		s += l + "\n"
	}
	if len(r.flag) > 0 {
		s += "⚠ 需人工复核：\n"
		for _, f := range r.flag {
			s += "  - " + f + "\n"
		}
	}
	s += r.summary
	return s
}
