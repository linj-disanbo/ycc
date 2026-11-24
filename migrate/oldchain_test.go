package migrate

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/33cn/chain33/system/crypto/secp256k1eth"
	dtypes "github.com/33cn/chain33/system/dapp/coins/types"
	"github.com/33cn/chain33/types"
	"github.com/syndtr/goleveldb/leveldb"
)

func TestExpireClass(t *testing.T) {
	cases := []struct {
		expire int64
		want   string
	}{
		{0, ExpireClassZero},
		{1670386893, ExpireClassTime},
		{types.TxHeightFlag + 5782578, ExpireClassHeight},
		{5782578, ExpireClassSmallHeight},
	}
	for _, c := range cases {
		if got := ExpireClass(c.expire); got != c.want {
			t.Errorf("ExpireClass(%d)=%s want %s", c.expire, got, c.want)
		}
	}
	if h := ExpireHeight(types.TxHeightFlag + 100); h != 100 {
		t.Errorf("ExpireHeight=%d want 100", h)
	}
}

func TestIsPos33Execer(t *testing.T) {
	if !IsPos33Execer("pos33") || !IsPos33Execer("user.p.x.pos33") {
		t.Error("pos33 判定失败")
	}
	if IsPos33Execer("pos33v2") || IsPos33Execer("coins") {
		t.Error("pos33 判定误报")
	}
}

func mkCoinsTx(t *testing.T, amount int64) *types.Transaction {
	t.Helper()
	priv, err := secp256k1eth.Driver{}.GenKey()
	if err != nil {
		t.Fatal(err)
	}
	p := types.Encode(&dtypes.CoinsAction{Value: &dtypes.CoinsAction_Transfer{
		Transfer: &types.AssetsTransfer{Amount: amount, To: "0x1111111111111111111111111111111111111111"}}})
	tx := &types.Transaction{Execer: []byte("coins"), Payload: p, Fee: 1e6, ChainID: 999}
	tx.Sign((2<<12)|secp256k1eth.ID, priv)
	return tx
}

func TestTxAmountAndDust(t *testing.T) {
	if amt, kind := TxAmount(mkCoinsTx(t, 1)); amt != 1 || kind != "coins.transfer" {
		t.Errorf("TxAmount=%d/%s want 1/coins.transfer", amt, kind)
	}
	if !IsDust(mkCoinsTx(t, 1)) {
		t.Error("coins amount==1 应判为 dust")
	}
	if IsDust(mkCoinsTx(t, 100)) {
		t.Error("coins amount==100 不应判为 dust")
	}
	// evm amount==1 不算 dust
	evmTx := &types.Transaction{Execer: []byte("evm"),
		Payload: types.Encode(&types.EVMContractAction4Chain33{Amount: 1}), Fee: 1e6, ChainID: 999}
	if IsDust(evmTx) {
		t.Error("evm amount==1 不应判为 dust")
	}
}

func TestLoadBlockTxsTableLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blockchain.db")
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tx := mkCoinsTx(t, 7)
	hash := sha256.Sum256([]byte("block1"))
	body := &types.BlockBody{Txs: []*types.Transaction{tx}, Hash: hash[:]}
	primary := append([]byte(fmt.Sprintf("%012d", 1)), hash[:]...)
	row := make([]byte, 8)
	binary.LittleEndian.PutUint64(row, uint64(len(primary)))
	row = append(row, primary...)
	row = append(row, types.Encode(body)...)
	if err := db.Put(append([]byte(TableBodyDataPrefix), primary...), row, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte(HeightPrefix+"1"), hash[:], nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte(LastHeightKey), types.Encode(&types.Int64{Data: 1}), nil); err != nil {
		t.Fatal(err)
	}

	if h, err := LoadLastHeight(db); err != nil || h != 1 {
		t.Fatalf("LoadLastHeight=%d,%v want 1", h, err)
	}
	txs, layout, err := LoadBlockTxs(db, 1)
	if err != nil {
		t.Fatalf("LoadBlockTxs err: %v", err)
	}
	if layout != "table" {
		t.Errorf("layout=%s want table", layout)
	}
	if len(txs) != 1 || txs[0].Nonce != tx.Nonce {
		t.Errorf("读到的交易不符")
	}
	if amt, _ := TxAmount(txs[0]); amt != 7 {
		t.Errorf("金额=%d want 7", amt)
	}
}
