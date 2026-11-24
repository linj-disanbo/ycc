// Package migrate 的旧链读取辅助：读旧节点 datadir（leveldb）里的区块与交易，
// 供 txscan（索引采集）与 txpick（样本抽取）共用，避免两套实现的 key 布局漂移。
//
// 关键事实（详见 new-ycc-v2/DESIGN.md）：
//   - 区块 body 采用表布局（chain33 v1.68+ SaveBlock→saveBlockForTable）：
//     CHAIN-body-body-d-<%012d height><hash>，value = table.Row.Encode()
//     （8B 小端 primary 长度 + primary + Encode(BlockBody)）；
//     更早期实现用 Body:<hash>，作为兜底。
//   - height → hash 索引：Height:<十进制高度>。
//   - expire 有四种形态，只有时间戳型在新链上永久失效。
package migrate

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/33cn/chain33/system/crypto/secp256k1eth"
	dtypes "github.com/33cn/chain33/system/dapp/coins/types"
	"github.com/33cn/chain33/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// 旧链 localdb/区块 key 前缀
const (
	BodyPrefixLegacy     = "Body:" // 表结构引入前的旧布局
	HeightPrefix         = "Height:"
	LastHeightKey        = "blockLastHeight"
	TxHashPrefix         = "TX:"
	TxShortPrefix        = "STX:"
	EvmNoncePrefix       = "LODB-evm-noncestate:"
	TableBodyDataPrefix  = "CHAIN-body-body-d-"
	TableBodyHashPrefix  = "CHAIN-body-body-m-hash-"
	Pos33Execer          = "pos33"
	CoinsExecer          = "coins"
	EthSignTy      int32 = (2 << 12) | secp256k1eth.ID // EncodeSignID(secp256k1eth.ID, eth addressID)
)

// expire 形态分类（types/tx.go:610-628）
const (
	ExpireClassZero        = "zero"        // 0：永不过期
	ExpireClassTime        = "time"        // 时间戳型：valid<=blocktime，新链永久失效
	ExpireClassHeight      = "txheight"    // TxHeight 型：只在 [H-200,H+600] 可打包
	ExpireClassSmallHeight = "smallheight" // 旧式小高度：valid<=height 才过期
)

// ExpireClass 判定某笔交易的 expire 形态。
func ExpireClass(expire int64) string {
	switch {
	case expire == 0:
		return ExpireClassZero
	case expire > types.TxHeightFlag:
		return ExpireClassHeight
	case expire <= types.ExpireBound:
		return ExpireClassSmallHeight
	default:
		return ExpireClassTime
	}
}

// ExpireHeight 返回 TxHeight 型的创建高度（非该类型返回 0）。
func ExpireHeight(expire int64) int64 {
	if expire > types.TxHeightFlag {
		return expire - types.TxHeightFlag
	}
	return 0
}

// LoadLastHeight 读取旧链链高度（blockLastHeight）。
func LoadLastHeight(db *leveldb.DB) (int64, error) {
	b, err := db.Get([]byte(LastHeightKey), nil)
	if err != nil {
		return -1, fmt.Errorf("读 %s 失败: %w", LastHeightKey, err)
	}
	var h types.Int64
	if err := types.Decode(b, &h); err == nil {
		return h.Data, nil
	}
	var raw int64
	if _, err := fmt.Sscan(string(b), &raw); err != nil {
		return -1, fmt.Errorf("解析链高度失败")
	}
	return raw, nil
}

// LoadBlockTxs 读取某高度的交易列表，返回（交易列表，布局名 "table"/"legacy"，错误）。
func LoadBlockTxs(db *leveldb.DB, height int64) ([]*types.Transaction, string, error) {
	hash, err := db.Get([]byte(HeightPrefix+strconv.FormatInt(height, 10)), nil)
	if err != nil {
		return nil, "", fmt.Errorf("Height:%d 未找到", height)
	}
	if body, err := LoadBodyTable(db, height, hash); err == nil {
		return body.Txs, "table", nil
	}
	if bodyBytes, err := db.Get(append([]byte(BodyPrefixLegacy), hash...), nil); err == nil {
		var body types.BlockBody
		if err := types.Decode(bodyBytes, &body); err == nil {
			return body.Txs, "legacy", nil
		}
	}
	return nil, "", fmt.Errorf("block body 未命中 hash=%x", hash)
}

// LoadBodyTable 按 chain33 表布局读取 block body（含 hash 索引兜底）。
func LoadBodyTable(db *leveldb.DB, height int64, hash []byte) (*types.BlockBody, error) {
	primary := append([]byte(fmt.Sprintf("%012d", height)), hash...)
	if v, err := db.Get(append([]byte(TableBodyDataPrefix), primary...), nil); err == nil {
		return DecodeTableRow(v)
	}
	ip := append([]byte(TableBodyHashPrefix), hash...)
	ip = append(ip, '-')
	it := db.NewIterator(util.BytesPrefix(ip), nil)
	defer it.Release()
	if it.Next() {
		pk := append([]byte(nil), it.Value()...)
		if v, err := db.Get(append([]byte(TableBodyDataPrefix), pk...), nil); err == nil {
			return DecodeTableRow(v)
		}
	}
	return nil, fmt.Errorf("CHAIN-body 未命中")
}

// DecodeTableRow 解析 table.Row.Encode()：[8B LE len(primary)] + primary + Encode(Data)
func DecodeTableRow(v []byte) (*types.BlockBody, error) {
	if len(v) < 8 {
		return nil, fmt.Errorf("row 太短")
	}
	n := int64(binary.LittleEndian.Uint64(v[:8]))
	if n < 0 || int(n) > len(v)-8 {
		return nil, fmt.Errorf("row primary 长度非法: %d", n)
	}
	var body types.BlockBody
	if err := types.Decode(v[8+n:], &body); err != nil {
		return nil, fmt.Errorf("解析 BlockBody 失败: %w", err)
	}
	return &body, nil
}

// IsCoinsExecer 归一化后是否 coins（平行链 user.p.x.coins 也归一为 coins）。
func IsCoinsExecer(execer []byte) bool {
	return string(types.GetRealExecName(execer)) == CoinsExecer
}

// IsPos33Execer 是否 pos33 执行器（含平行链前缀形式）。
func IsPos33Execer(execer string) bool {
	return execer == Pos33Execer || len(execer) > len(Pos33Execer) &&
		execer[len(execer)-len(Pos33Execer)-1:] == "."+Pos33Execer
}

// TxAmount 解析交易金额（最小单位）。覆盖 coins 三类动作与 evm；
// 其它执行器返回 (0, "")，调用方不应把 0 当作真实金额判断。
func TxAmount(tx *types.Transaction) (int64, string) {
	real := string(types.GetRealExecName(tx.Execer))
	switch real {
	case CoinsExecer:
		var act dtypes.CoinsAction
		if err := types.Decode(tx.Payload, &act); err != nil {
			return 0, ""
		}
		switch v := act.Value.(type) {
		case *dtypes.CoinsAction_Transfer:
			if v.Transfer != nil {
				return v.Transfer.Amount, "coins.transfer"
			}
		case *dtypes.CoinsAction_TransferToExec:
			if v.TransferToExec != nil {
				return v.TransferToExec.Amount, "coins.transferToExec"
			}
		case *dtypes.CoinsAction_Withdraw:
			if v.Withdraw != nil {
				return v.Withdraw.Amount, "coins.withdraw"
			}
		}
	case "evm":
		var act types.EVMContractAction4Chain33
		if err := types.Decode(tx.Payload, &act); err == nil {
			return int64(act.Amount), "evm"
		}
	}
	return 0, ""
}

// IsDust 是否「coins 且 amount==1」的 dust 交易（与 txscan 的 -skip-dust-amount 口径一致）。
func IsDust(tx *types.Transaction) bool {
	if !IsCoinsExecer(tx.Execer) {
		return false
	}
	amount, _ := TxAmount(tx)
	return amount == 1
}
