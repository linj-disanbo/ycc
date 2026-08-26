// gendump 生成合成旧链状态 dump（测试用）。
// 用法: gendump -out synthetic.gob
// 输出里同时打印 测试私钥/地址，便于导入测试钱包。
package main

import (
	"encoding/gob"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/33cn/chain33/common"
	"github.com/33cn/chain33/common/address"
	"github.com/33cn/chain33/common/crypto"
	"github.com/33cn/chain33/system/address/eth"
	"github.com/33cn/chain33/types"
	pos33types "github.com/yccproject/ycc/plugin/dapp/pos33/types"
	"github.com/yccproject/ycc/migrate"
)

var coinPrecision = int64(100000000)

func execAddrOf(name string) string {
	a, err := address.GetExecAddress(name, eth.ID)
	if err != nil {
		panic(err)
	}
	return a
}

func main() {
	out := flag.String("out", "synthetic.gob", "输出 dump")
	miners := flag.Int("miners", 1, "被委托人（矿工）数量")
	consignorsPerMiner := flag.Int("consignorsPerMiner", 1, "每个矿工的委托人数量")
	stakePer := flag.Int64("stakePer", 10000000, "每个委托人质押（ycc 单位）")
	flag.Parse()

	pos33Exec := execAddrOf("pos33")
	ticketExec := execAddrOf("ticket")

	// 测试密钥（secp256k1）
	cr, err := crypto.Load(types.GetSignName("", types.SECP256K1), -1)
	if err != nil {
		log.Fatal(err)
	}
	privMiner, _ := cr.GenKey()
	privConsignor, _ := cr.GenKey()
	privUser, _ := cr.GenKey()
	addrMiner := address.PubKeyToAddr(eth.ID, privMiner.PubKey().Bytes())
	addrConsignor := address.PubKeyToAddr(eth.ID, privConsignor.PubKey().Bytes())
	addrUser := address.PubKeyToAddr(eth.ID, privUser.PubKey().Bytes())

	stake := *stakePer * coinPrecision
	price := int64(10000) * coinPrecision

	dump := &migrate.Dump{Height: 1000000, BlockTime: 1787557086, ChainID: 999}
	setAcc := func(k string, acc *types.Account) { dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte(k), Value: types.Encode(acc)}) }
	raw := func(k, v string) { dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte(k), Value: []byte(v)}) }

	setAcc("mavl-coins-ycc-"+addrMiner, &types.Account{Addr: addrMiner, Balance: 500 * coinPrecision})
	setAcc("mavl-coins-ycc-"+addrConsignor, &types.Account{Addr: addrConsignor, Balance: 100 * coinPrecision})
	setAcc("mavl-coins-ycc-"+addrUser, &types.Account{Addr: addrUser, Balance: 3000 * coinPrecision})
	setAcc("mavl-coins-ycc-"+pos33Exec, &types.Account{Addr: pos33Exec, Balance: 300 * coinPrecision})
	setAcc("mavl-coins-ycc-exec-"+pos33Exec+":"+addrConsignor, &types.Account{Addr: addrConsignor, Balance: 0, Frozen: stake})
	setAcc("mavl-coins-ycc-exec-"+pos33Exec+":"+addrMiner, &types.Account{Addr: addrMiner, Balance: 0, Frozen: 80 * coinPrecision})

	// 真实规模的多个矿工/委托人（确定性地址，不参与挖矿测试）
	totalStake := stake + 80*coinPrecision
	for m := 0; m < *miners; m++ {
		maddr := fmt.Sprintf("0x%040x", 1000+m)
		var cons []*pos33types.Consignor
		for c := 0; c < *consignorsPerMiner; c++ {
			caddr := fmt.Sprintf("0x%040x", 200000+1000*m+c)
			setAcc("mavl-coins-ycc-"+caddr, &types.Account{Addr: caddr, Balance: 50 * coinPrecision})
			setAcc("mavl-coins-ycc-exec-"+pos33Exec+":"+caddr, &types.Account{Addr: caddr, Balance: 0, Frozen: stake})
			cons = append(cons, &pos33types.Consignor{Address: caddr, Amount: stake})
			totalStake += stake
		}
		ce := &pos33types.Pos33Consignee{Address: maddr, FeePersent: 10, Amount: stake * int64(*consignorsPerMiner), Consignors: cons}
		dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte("mavl-pos33-consignee-" + string(address.FormatAddrKey(maddr))), Value: types.Encode(ce)})
		dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte("mavl-pos33-consignor-" + string(address.FormatAddrKey(cons[0].Address))), Value: types.Encode(&pos33types.Pos33Consignor{Address: cons[0].Address, Consignees: []*pos33types.Consignee{{Address: maddr, Amount: stake}}})})
		dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte("mavl-pos33-" + maddr), Value: types.Encode(&pos33types.Pos33DepositMsg{Maddr: maddr, Raddr: cons[0].Address, Count: stake / price})})
	}

	consignee := &pos33types.Pos33Consignee{
		Address:        addrMiner,
		FeePersent:     10,
		Amount:         stake,
		Consignors:     []*pos33types.Consignor{{Address: addrConsignor, Amount: stake, Reward: 50 * coinPrecision, RemainReward: 20 * coinPrecision}},
		FeeReward:      10 * coinPrecision,
		RemainFeeReward: 5 * coinPrecision,
	}
	dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte("mavl-pos33-consignee-" + string(address.FormatAddrKey(addrMiner))), Value: types.Encode(consignee)})
	consignor := &pos33types.Pos33Consignor{Address: addrConsignor, Consignees: []*pos33types.Consignee{{Address: addrMiner, Amount: stake}}}
	dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte("mavl-pos33-consignor-" + string(address.FormatAddrKey(addrConsignor))), Value: types.Encode(consignor)})
	deposit := &pos33types.Pos33DepositMsg{Maddr: addrMiner, Raddr: addrConsignor, Count: stake / price, Reward: 50 * coinPrecision}
	dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte("mavl-pos33-" + addrMiner), Value: types.Encode(deposit)})
	raw("mavl-pos33-bind-"+addrConsignor, addrMiner)
	raw("mavl-pos33-bls-0x6da47c11230e44adb384fed56554bfd55dde1750", addrMiner)
	raw("mavl-pos33-allcountid", "12345")
	raw("mavl-pos33-all-frozen", fmt.Sprintf("%d", totalStake))
	// 其他 dapp 状态（原样保留）
	raw("mavl-token-create-test", "abc")
	raw("mavl-evm-testkey", "evmstate")
	raw("mavl-trade-test", "trade")

	f, _ := os.Create(*out)
	defer f.Close()
	gob.NewEncoder(f).Encode(dump)
	log.Printf("合成 dump: %s（键数=%d）", *out, len(dump.KVs))
	log.Printf("pos33 exec=%s ticket exec=%s", pos33Exec, ticketExec)
	log.Printf("矿工 priv=%s addr=%s", hex.EncodeToString(privMiner.Bytes()), addrMiner)
	log.Printf("委托人 priv=%s addr=%s", hex.EncodeToString(privConsignor.Bytes()), addrConsignor)
	log.Printf("用户 priv=%s addr=%s", hex.EncodeToString(privUser.Bytes()), addrUser)
	_ = common.Sha256
	_ = price
}
