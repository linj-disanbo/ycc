// txscan：扫描旧链 datadir，导出新链「防重放」所需的本地库键（txhash 索引 + eth nonce）。
//
// 背景：新链沿用旧链 chainID/evmChainID，靠 chain33 现成的 txhash 去重索引（localdb
// TX:/STX:）阻止旧交易重放；本工具把旧链已上链交易的 hash 预先整理成可直接写入新链
// blockchain.db 的键值。
//
// 用法：
//
//	txscan -datadir <旧链 datadir> -out out/ [-filter-pos33=true] [-only-nonexpiring=false]
//	       [-max-height N] [-dump-tx] [-nonce-crosscheck warn|strict|off] [-tmpdir DIR]
//
// 输出（-out 目录）：
//
//	txhash.bin     : YCCKV 流式键值（TX:<hash>、STX:<hash[0:8]>、FLAG:FlagTxQuickIndex）
//	evmnonce.bin   : YCCKV 流式键值（LODB-evm-noncestate:<addr>）
//	manifest.json  : 快照高度、条数、集合哈希（可复算）、文件哈希
//	scan-report.txt: 统计与对账
//	txdetail.csv.gz: 可选（-dump-tx），txhash,nonce,address,expire,execer,height,index
//
// 实现要点（旧链可达 2 亿+ 笔交易，必须控内存）：
//   - 外部排序：扫描阶段把 (hash,height,index) 按 hash 首字节分流到 256 个临时桶文件；
//     输出阶段逐桶读入、桶内排序、流式写出。峰值内存 ≈ 单桶大小（几百 MB），不会 OOM。
//   - 输出为流式格式 YCCKV1（见 kio.go），边算哈希边写盘；`build` 支持读取该格式。
//   - 只读旧 datadir；同参数输出确定性一致（键序固定、manifest 不含时间戳）。
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/33cn/chain33/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
	"github.com/yccproject/ycc/migrate"

	// 地址驱动：eth（addressID=2），tx.From() 依赖
	_ "github.com/33cn/chain33/system/address/eth"
)

const (
	// TxHeight 型 expire 的窗口分布分桶步长（按旧高度）
	txHeightBucketStep = int64(6000000)
	bucketNum          = 256
	recSize            = 44 // 32 hash + 8 height + 4 index
)

type txRow struct {
	h      [32]byte
	height int64
	index  int32
}

type stats struct {
	total         int64
	txCount       int64
	pos33Total    int64
	pos33Skipped  int64
	expireSet     int64
	expireZero    int64
	expireSkipped int64
	expireTime    int64 // 时间戳型（unix 秒）
	expireHeight  int64 // TxHeight 型（expire > 1<<62）
	expireSmall   int64 // 旧式小高度（expire <= 1e9）
	byExecer      map[string]int64
	bySignTy      map[string]int64
	byAddr        map[string]struct{}
	ethTxByAddr   map[string]int64
	missingBody   int64
	blocksScanned int64
	lastHeight    int64
	maxHeight     int64
	layoutTable   int64
	layoutLegacy  int64
	amtOneCoins   int64 // 收录集合中「execer=coins 且 amount==1」的笔数（dust，可跳过）
	amtOneOther   int64 // 收录集合中「非 coins 且 amount==1」的笔数（如 evm，不跳过）
	amtZero       int64
	amtOther      int64
	amtOneByKind  map[string]int64
	dustSkipped   int64
	// TxHeight 型交易的创建高度分布：bucketIdx = H / txHeightBucketStep
	thAll     map[int64]int64
	thSkipped map[int64]int64 // 其中被 -skip-dust-amount 跳掉的
}

type manifest struct {
	Version          int              `json:"version"`
	Format           string           `json:"format"`
	SnapshotHeight   int64            `json:"snapshotHeight"`
	ScannedHeight    int64            `json:"scannedHeight"`
	FilterPos33      bool             `json:"filterPos33"`
	SkipTimeExpire   bool             `json:"skipTimestampExpire"`
	NeedIndexTxs     int64            `json:"needIndexTxs"` // expire==0 + TxHeight型 + 旧式小高度
	AmtOneCoinsTxs   int64            `json:"amtOneCoinsTxs"`
	AmtOneOtherTxs   int64            `json:"amtOneOtherTxs"`
	AmtZeroTxs       int64            `json:"amtZeroTxs"`
	AmtOneByKind     map[string]int64 `json:"amtOneByKind,omitempty"`
	DustSkipped      int64            `json:"dustSkipped"`
	TotalTxs         int64            `json:"totalTxs"`
	IndexedTxs       int64            `json:"indexedTxs"`
	Pos33Txs         int64            `json:"pos33Txs"`
	Pos33Skipped     int64            `json:"pos33Skipped"`
	ExpireSetTxs     int64            `json:"expireSetTxs"`
	ExpireZeroTxs    int64            `json:"expireZeroTxs"`
	ExpireTimeTxs    int64            `json:"expireTimeTxs"`
	ExpireHeightTxs  int64            `json:"expireHeightTxs"`
	ExpireSmallTxs   int64            `json:"expireSmallTxs"`
	ExpireSkipped    int64            `json:"expireSkipped"`
	AddrCount        int              `json:"addrCount"`
	BodyLayoutTable  int64            `json:"bodyLayoutTable"`
	BodyLayoutLegacy int64            `json:"bodyLayoutLegacy"`
	TxSetSha256      string           `json:"txSetSha256"`
	TxFileSha256     string           `json:"txFileSha256"`
	EvmNonceCount    int              `json:"evmNonceCount"`
	EvmNonceSha256   string           `json:"evmNonceSha256"`
	Notes            []string         `json:"notes,omitempty"`
}

func main() {
	datadir := flag.String("datadir", "", "旧链 datadir（须含 blockchain.db）")
	outDir := flag.String("out", "out", "输出目录")
	filterPos33 := flag.Bool("filter-pos33", true, "过滤 pos33 执行器交易（默认过滤）")
	skipTimestampExpire := flag.Bool("skip-timestamp-expire", true,
		"跳过「时间戳型」过期交易（这类在新链上永久失效，可安全跳过；默认 true）")
	maxHeight := flag.Int64("max-height", -1, "只扫描到该高度（-1 表示到链尾）")
	dumpTx := flag.Bool("dump-tx", false, "输出 txdetail.csv.gz 明细")
	progress := flag.Int64("progress", 20000000, "每处理 N 笔交易打印一次进度（默认 2000 万）")
	nonceCheck := flag.String("nonce-crosscheck", "warn", "eth nonce 交叉校验：strict|warn|off")
	tmpDir := flag.String("tmpdir", "", "外部排序临时目录（默认 <out>/tmp）")
	keepTmp := flag.Bool("keep-tmp", false, "保留临时桶文件（排查用）")
	skipDust := flag.Bool("skip-dust-amount", false, "跳过 amount==1（最小单位）的转账交易（默认 false；跳过会放弃这部分的重放拦截）")
	census := flag.Bool("census", false, "只输出 datadir 的 key 前缀普查（诊断）")
	probeHeight := flag.Int64("probe-height", 0, "诊断：打印该高度的 body 命中情况后退出")
	flag.Parse()

	if *datadir == "" {
		log.Fatal("必须指定 -datadir（旧链 datadir 目录）")
	}
	if *nonceCheck != "strict" && *nonceCheck != "warn" && *nonceCheck != "off" {
		log.Fatalf("-nonce-crosscheck 取值非法: %s", *nonceCheck)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("创建输出目录失败: %v", err)
	}

	dbPath := filepath.Join(*datadir, "blockchain.db")
	db, err := leveldb.OpenFile(dbPath, &opt.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("打开 %s 失败: %v（确认路径是旧节点 datadir，且节点已停止）", dbPath, err)
	}
	defer db.Close()

	if *census {
		printCensus(db, 60)
		return
	}
	if *probeHeight > 0 {
		probeHeightFn(db, *probeHeight)
		return
	}

	last, err := migrate.LoadLastHeight(db)
	if err != nil {
		log.Fatalf("读取链高度失败: %v", err)
	}
	log.Printf("旧链 datadir=%s 链高度=%d", dbPath, last)

	st := &stats{
		lastHeight:   last,
		maxHeight:    last,
		byExecer:     make(map[string]int64),
		bySignTy:     make(map[string]int64),
		byAddr:       make(map[string]struct{}),
		ethTxByAddr:  make(map[string]int64),
		amtOneByKind: make(map[string]int64),
		thAll:        make(map[int64]int64),
		thSkipped:    make(map[int64]int64),
	}
	if *maxHeight >= 0 && *maxHeight < last {
		st.maxHeight = *maxHeight
	}
	if *tmpDir == "" {
		*tmpDir = filepath.Join(*outDir, "tmp")
	}
	if err := os.MkdirAll(*tmpDir, 0o755); err != nil {
		log.Fatalf("创建临时目录失败: %v", err)
	}
	log.Printf("扫描范围: 1 .. %d（filter-pos33=%v, skip-timestamp-expire=%v, tmp=%s）",
		st.maxHeight, *filterPos33, *skipTimestampExpire, *tmpDir)

	// ---------- 阶段 A：扫描 → 按 hash 首字节分桶写临时文件 ----------
	buckets, err := openBuckets(*tmpDir)
	if err != nil {
		log.Fatalf("打开临时桶失败（磁盘空间/权限？）: %v", err)
	}
	defer closeBuckets(buckets)

	var dumpCsv *csv.Writer
	if *dumpTx {
		p := filepath.Join(*outDir, "txdetail.csv.gz")
		dumpFile, err := os.Create(p)
		if err != nil {
			log.Fatalf("创建 %s 失败: %v", p, err)
		}
		defer dumpFile.Close()
		dumpGz := gzip.NewWriter(dumpFile)
		defer dumpGz.Close()
		dumpCsv = csv.NewWriter(dumpGz)
		defer dumpCsv.Flush()
		_ = dumpCsv.Write([]string{"txhash", "nonce", "address", "expire", "execer", "height", "index"})
	}

	var rec [recSize]byte
	for h := int64(1); h <= st.maxHeight; h++ {
		txs, layout, err := migrate.LoadBlockTxs(db, h)
		if err != nil {
			st.missingBody++
			if st.missingBody <= 20 {
				log.Printf("[warn] 高度 %d 读取失败: %v", h, err)
			}
			continue
		}
		st.blocksScanned++
		if layout == "table" {
			st.layoutTable++
		} else {
			st.layoutLegacy++
		}
		for i, tx := range txs {
			st.total++
			execer := string(tx.Execer)
			sigTy := int32(0)
			if tx.Signature != nil {
				sigTy = tx.Signature.Ty
			}
			if migrate.IsPos33Execer(execer) {
				st.pos33Total++
				if *filterPos33 {
					st.pos33Skipped++
					continue
				}
			}
			// expire 语义（types/tx.go:610-628）：
			//   0            → 永不过期，必须靠索引拦
			//   > 1<<62      → TxHeight 型：只有 [H-200,H+600] 窗口可打包；新链迟早走到该高度 → 必须拦
			//   <= 1e9       → 旧式高度：到期条件 valid<=height，新链高度未到之前一直有效 → 必须拦
			//   其余          → 时间戳型：到期条件 valid<=blocktime，新链 blocktime 单调递增 → 永久失效
			if tx.Expire != 0 {
				st.expireSet++
				switch {
				case tx.Expire > types.TxHeightFlag:
					st.expireHeight++
					hOld := tx.Expire - types.TxHeightFlag
					st.thAll[hOld/txHeightBucketStep]++
				case tx.Expire <= types.ExpireBound:
					st.expireSmall++
				default:
					st.expireTime++
					if *skipTimestampExpire {
						st.expireSkipped++
						continue
					}
				}
			} else {
				st.expireZero++
			}
			// 金额统计（coins / evm 两类可解；其它执行器按“不适用”计）
			// dust 判定：**仅** execer=coins（含平行链 coins，按 realExec 归一）且 amount==1
			// 非 coins 的 amount==1（如 evm 合约调用）不计入 dust、不会被跳过。
			amount, kind := migrate.TxAmount(tx)
			if amount == 1 && migrate.IsCoinsExecer(tx.Execer) {
				if *skipDust {
					st.dustSkipped++
					if tx.Expire > types.TxHeightFlag {
						st.thSkipped[(tx.Expire-types.TxHeightFlag)/txHeightBucketStep]++
					}
					continue
				}
				st.amtOneCoins++
				if kind != "" {
					st.amtOneByKind[kind]++
				}
			} else if amount == 1 {
				st.amtOneOther++
			} else if amount == 0 {
				st.amtZero++
			} else {
				st.amtOther++
			}

			from := ""
			if tx.Signature != nil {
				from = strings.ToLower(tx.From())
			}
			if sigTy == migrate.EthSignTy && execer == "evm" && from != "" {
				st.ethTxByAddr[from]++
			}

			hash := tx.Hash()
			copy(rec[0:32], hash)
			binary.LittleEndian.PutUint64(rec[32:40], uint64(h))
			binary.LittleEndian.PutUint32(rec[40:44], uint32(i))
			if _, err := buckets[hash[0]&0xff].w.Write(rec[:]); err != nil {
				log.Fatalf("写临时桶失败（磁盘满？）: %v", err)
			}
			st.txCount++
			st.byExecer[execer]++
			st.bySignTy[strconv.Itoa(int(sigTy))]++
			if from != "" {
				st.byAddr[from] = struct{}{}
			}
			if dumpCsv != nil {
				_ = dumpCsv.Write([]string{
					hex.EncodeToString(hash), strconv.FormatInt(tx.Nonce, 10), from,
					strconv.FormatInt(tx.Expire, 10), execer, strconv.FormatInt(h, 10), strconv.Itoa(i),
				})
			}
		}
		if *progress > 0 && st.total > 0 && st.total%*progress < int64(len(txs)) {
			log.Printf("进度: 高度 %d/%d，交易 %d（收录 %d），RSS≈%s",
				h, st.maxHeight, st.total, st.txCount, memString())
		}
	}
	if dumpCsv != nil {
		dumpCsv.Flush()
	}
	for _, b := range buckets {
		if err := b.w.Flush(); err != nil {
			log.Fatalf("flush 临时桶失败: %v", err)
		}
	}
	log.Printf("阶段A 完成：交易 %d，收录 %d，缺失 body 高度 %d", st.total, st.txCount, st.missingBody)

	// ---------- 阶段 B：逐桶排序 → 流式写 txhash.bin ----------
	txFile := filepath.Join(*outDir, "txhash.bin")
	f, err := os.Create(txFile)
	if err != nil {
		log.Fatalf("创建 %s 失败: %v", txFile, err)
	}
	fileHasher := sha256.New()
	out := bufio.NewWriterSize(f, 1<<20)
	tee := io.MultiWriter(out, fileHasher)
	if err := migrate.WriteKVHeader(tee, st.txCount*2); err != nil {
		log.Fatalf("写头失败: %v", err)
	}
	setHasher := sha256.New()

	var lastShort [8]byte
	haveShort := false
	for b := 0; b < bucketNum; b++ {
		rows, err := readBucket(buckets, b)
		if err != nil {
			log.Fatalf("读桶 %d 失败: %v", b, err)
		}
		sort.Slice(rows, func(i, j int) bool { return bytes.Compare(rows[i].h[:], rows[j].h[:]) < 0 })
		for k := range rows {
			r := &rows[k]
			key := append([]byte(migrate.TxHashPrefix), r.h[:]...)
			val := encodeTxResult(r.height, r.index)
			if err := migrate.WriteKVRecord(tee, key, val); err != nil {
				log.Fatalf("写 %s 失败: %v", txFile, err)
			}
			setHasher.Write(key)
			setHasher.Write(val)

			var short [8]byte
			copy(short[:], r.h[:8])
			if !haveShort || short != lastShort {
				if err := migrate.WriteKVRecord(tee, append([]byte(migrate.TxShortPrefix), short[:]...), []byte("1")); err != nil {
					log.Fatalf("写 %s 失败: %v", txFile, err)
				}
				lastShort = short
				haveShort = true
			}
		}
		rows = nil
		if b%32 == 0 {
			log.Printf("阶段B 进度: 桶 %d/%d，RSS≈%s", b, bucketNum, memString())
		}
	}
	flagKV := types.FlagKV(types.FlagTxQuickIndex, 1)
	if err := migrate.WriteKVRecord(tee, flagKV.Key, flagKV.Value); err != nil {
		log.Fatalf("写 %s 失败: %v", txFile, err)
	}
	if err := out.Flush(); err != nil {
		log.Fatalf("flush %s 失败: %v", txFile, err)
	}
	if err := f.Close(); err != nil {
		log.Fatalf("关闭 %s 失败: %v", txFile, err)
	}

	// ---------- eth nonce ----------
	evmKVs, err := loadEvmNonces(db)
	if err != nil {
		log.Fatalf("读取 %s* 失败: %v", migrate.EvmNoncePrefix, err)
	}
	evmFile := filepath.Join(*outDir, "evmnonce.bin")
	evmHash, err := migrate.WriteKVFile(evmFile, evmKVs)
	if err != nil {
		log.Fatalf("写 %s 失败: %v", evmFile, err)
	}
	nonceReport, nonceBad := crossCheckNonces(evmKVs, st.ethTxByAddr)

	// ---------- manifest ----------
	mf := &manifest{
		Version:          1,
		Format:           migrate.KVFormatName,
		SnapshotHeight:   last,
		ScannedHeight:    st.maxHeight,
		FilterPos33:      *filterPos33,
		SkipTimeExpire:   *skipTimestampExpire,
		NeedIndexTxs:     st.expireZero + st.expireHeight + st.expireSmall,
		AmtOneCoinsTxs:   st.amtOneCoins,
		AmtOneOtherTxs:   st.amtOneOther,
		AmtZeroTxs:       st.amtZero,
		AmtOneByKind:     st.amtOneByKind,
		DustSkipped:      st.dustSkipped,
		TotalTxs:         st.total,
		IndexedTxs:       st.txCount,
		Pos33Txs:         st.pos33Total,
		Pos33Skipped:     st.pos33Skipped,
		ExpireSetTxs:     st.expireSet,
		ExpireZeroTxs:    st.expireZero,
		ExpireTimeTxs:    st.expireTime,
		ExpireHeightTxs:  st.expireHeight,
		ExpireSmallTxs:   st.expireSmall,
		ExpireSkipped:    st.expireSkipped,
		AddrCount:        len(st.byAddr),
		BodyLayoutTable:  st.layoutTable,
		BodyLayoutLegacy: st.layoutLegacy,
		TxSetSha256:      hex.EncodeToString(setHasher.Sum(nil)),
		TxFileSha256:     hex.EncodeToString(fileHasher.Sum(nil)),
		EvmNonceCount:    len(evmKVs),
		EvmNonceSha256:   evmHash,
	}
	if nonceBad > 0 {
		mf.Notes = append(mf.Notes, fmt.Sprintf("eth nonce 交叉校验有 %d 个地址不一致（见 scan-report.txt）", nonceBad))
	}
	if st.missingBody > 0 {
		mf.Notes = append(mf.Notes, fmt.Sprintf("%d 个高度 body 未命中（已跳过，交易未收录）", st.missingBody))
	}
	mfBytes, _ := json.MarshalIndent(mf, "", "  ")
	mfPath := filepath.Join(*outDir, "manifest.json")
	if err := os.WriteFile(mfPath, mfBytes, 0o644); err != nil {
		log.Fatalf("写 %s 失败: %v", mfPath, err)
	}

	report := buildReport(mf, st, nonceReport)
	rpPath := filepath.Join(*outDir, "scan-report.txt")
	if err := os.WriteFile(rpPath, []byte(report), 0o644); err != nil {
		log.Fatalf("写 %s 失败: %v", rpPath, err)
	}
	fmt.Print(report)
	log.Printf("输出完成: %s, %s, %s, %s", txFile, evmFile, mfPath, rpPath)

	if !*keepTmp {
		_ = os.RemoveAll(*tmpDir)
	} else {
		log.Printf("临时桶保留在 %s", *tmpDir)
	}
	if *nonceCheck == "strict" && nonceBad > 0 {
		log.Fatalf("eth nonce 交叉校验不一致（%d 个地址），已按 -nonce-crosscheck=strict 中止", nonceBad)
	}
}

// ---------- 分桶 ----------

type bucketFile struct {
	f *os.File
	w *bufio.Writer
	p string
}

func openBuckets(dir string) ([]*bucketFile, error) {
	bs := make([]*bucketFile, bucketNum)
	for i := 0; i < bucketNum; i++ {
		p := filepath.Join(dir, fmt.Sprintf("bkt-%03d.tmp", i))
		f, err := os.Create(p)
		if err != nil {
			return nil, err
		}
		bs[i] = &bucketFile{f: f, w: bufio.NewWriterSize(f, 1<<19), p: p}
	}
	return bs, nil
}

func closeBuckets(bs []*bucketFile) {
	for _, b := range bs {
		if b == nil || b.f == nil {
			continue
		}
		_ = b.w.Flush()
		_ = b.f.Close()
	}
}

func readBucket(bs []*bucketFile, i int) ([]txRow, error) {
	b := bs[i]
	if _, err := b.f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(bufio.NewReaderSize(b.f, 1<<19))
	if err != nil {
		return nil, err
	}
	n := len(data) / recSize
	rows := make([]txRow, n)
	for k := 0; k < n; k++ {
		off := k * recSize
		copy(rows[k].h[:], data[off:off+32])
		rows[k].height = int64(binary.LittleEndian.Uint64(data[off+32 : off+40]))
		rows[k].index = int32(binary.LittleEndian.Uint32(data[off+40 : off+44]))
	}
	return rows, nil
}

// ---------- 读取旧 datadir ----------

func encodeTxResult(height int64, index int32) []byte {
	return types.Encode(&types.TxResult{Height: height, Index: index})
}

func loadEvmNonces(db *leveldb.DB) ([]*types.KeyValue, error) {
	it := db.NewIterator(util.BytesPrefix([]byte(migrate.EvmNoncePrefix)), nil)
	defer it.Release()
	var kvs []*types.KeyValue
	for it.Next() {
		k := append([]byte(nil), it.Key()...)
		v := append([]byte(nil), it.Value()...)
		kvs = append(kvs, &types.KeyValue{Key: k, Value: v})
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	return kvs, nil
}

func crossCheckNonces(evmKVs []*types.KeyValue, ethTxByAddr map[string]int64) (string, int) {
	expected := make(map[string]int64, len(evmKVs))
	for _, kv := range evmKVs {
		addr := strings.ToLower(strings.TrimPrefix(string(kv.Key), migrate.EvmNoncePrefix))
		var n types.EvmAccountNonce
		if err := types.Decode(kv.Value, &n); err != nil {
			continue
		}
		expected[addr] = n.Nonce
	}
	addrs := make([]string, 0, len(expected)+len(ethTxByAddr))
	seen := make(map[string]bool, len(expected))
	for a := range expected {
		addrs = append(addrs, a)
		seen[a] = true
	}
	for a := range ethTxByAddr {
		if !seen[a] {
			addrs = append(addrs, a)
		}
	}
	sort.Strings(addrs)
	var sb strings.Builder
	bad := 0
	sb.WriteString("\n---- eth nonce 交叉校验（旧 localdb vs 扫描的 ty=8452&&execer=evm 计数）----\n")
	for _, a := range addrs {
		want := ethTxByAddr[a]
		got := expected[a]
		if got != want {
			bad++
			if bad <= 200 {
				fmt.Fprintf(&sb, "  不一致 addr=%s localdb=%d 扫描计数=%d\n", a, got, want)
			}
		}
	}
	if bad > 200 {
		sb.WriteString("  ... 超过 200 条已省略\n")
	}
	fmt.Fprintf(&sb, "  地址数=%d 不一致=%d\n", len(addrs), bad)
	return sb.String(), bad
}

func buildReport(mf *manifest, st *stats, nonceReport string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "================ ycc 新链防重放采集报告 ================\n")
	fmt.Fprintf(&sb, "旧链高度(快照)      : %d\n", mf.SnapshotHeight)
	fmt.Fprintf(&sb, "本次扫描上限        : %d（扫描区块 %d，扫描交易 %d）\n", mf.ScannedHeight, st.blocksScanned, mf.TotalTxs)
	fmt.Fprintf(&sb, "过滤 pos33          : %v（pos33 交易 %d 笔，过滤 %d 笔）\n", mf.FilterPos33, st.pos33Total, mf.Pos33Skipped)
	fmt.Fprintf(&sb, "跳过时间戳型过期    : %v（跳过 %d 笔；这类在新链上永久失效）\n", mf.SkipTimeExpire, mf.ExpireSkipped)
	fmt.Fprintf(&sb, "总交易数            : %d\n", mf.TotalTxs)
	fmt.Fprintf(&sb, "收录进索引          : %d（占比 %.2f%%）\n", mf.IndexedTxs, pct(mf.IndexedTxs, mf.TotalTxs))
	fmt.Fprintf(&sb, "  expire!=0         : %d（时间戳型 %d / TxHeight 型 %d / 旧式小高度 %d）\n",
		mf.ExpireSetTxs, mf.ExpireTimeTxs, mf.ExpireHeightTxs, mf.ExpireSmallTxs)
	fmt.Fprintf(&sb, "  expire==0         : %d\n", mf.ExpireZeroTxs)
	fmt.Fprintf(&sb, "需要索引的集合      : %d（= expire==0 %d + TxHeight型 %d + 旧式小高度 %d）\n",
		mf.NeedIndexTxs, mf.ExpireZeroTxs, mf.ExpireHeightTxs, mf.ExpireSmallTxs)
	fmt.Fprintf(&sb, "  说明：dust 判定口径 = execer 归一后为 coins 且 amount==1（evm 的 amount==1 不计）\n")
	fmt.Fprintf(&sb, "  说明：TxHeight 型只在旧高度窗口可打包，旧式小高度是「valid<=height」——\n")
	fmt.Fprintf(&sb, "        两者在新链高度增长后都可能重新变为可打包，必须靠索引拦；只有时间戳型永久失效。\n")
	fmt.Fprintf(&sb, "  其中 amount==1(coins) : %d（占收录 %.2f%%；dust/压测转账，可用 -skip-dust-amount 跳过）\n",
		mf.AmtOneCoinsTxs, pct(mf.AmtOneCoinsTxs, mf.IndexedTxs))
	fmt.Fprintf(&sb, "  其中 amount==1(非coins): %d（如 evm，不参与 dust 判定）\n", mf.AmtOneOtherTxs)
	fmt.Fprintf(&sb, "  其中 amount==0    : %d\n", mf.AmtZeroTxs)
	fmt.Fprintf(&sb, "  其中 amount>1     : %d\n", mf.IndexedTxs-mf.AmtOneCoinsTxs-mf.AmtOneOtherTxs-mf.AmtZeroTxs)
	if mf.DustSkipped > 0 {
		fmt.Fprintf(&sb, "  已跳过 dust       : %d 笔（-skip-dust-amount）\n", mf.DustSkipped)
	}
	fmt.Fprintf(&sb, "涉及地址数          : %d\n", mf.AddrCount)
	fmt.Fprintf(&sb, "Body 布局           : 表布局 %d 块 / 旧布局 %d 块\n", st.layoutTable, st.layoutLegacy)
	fmt.Fprintf(&sb, "缺失 Body 高度数    : %d\n", st.missingBody)
	fmt.Fprintf(&sb, "预估 txhash.bin     : %.2f GB（约 67 B/笔 × 2 键）\n", float64(mf.IndexedTxs)*67/1e9)
	fmt.Fprintf(&sb, "TX 键集合 sha256    : %s\n", mf.TxSetSha256)
	fmt.Fprintf(&sb, "txhash.bin sha256   : %s\n", mf.TxFileSha256)
	fmt.Fprintf(&sb, "eth nonce 键数      : %d（文件 sha256=%s）\n", mf.EvmNonceCount, mf.EvmNonceSha256)

	if len(st.thAll) > 0 {
		sb.WriteString("\n---- TxHeight 型交易的创建高度分布（= 这些交易在新链何时重新可打包）----\n")
		sb.WriteString("  旧高度区间(百万)  新链到达约(5s/块)  TxHeight总计   其中已被 dust 跳过\n")
		idxs := make([]int64, 0, len(st.thAll))
		for k := range st.thAll {
			idxs = append(idxs, k)
		}
		sort.Slice(idxs, func(i, j int) bool { return idxs[i] < idxs[j] })
		for _, k := range idxs {
			lo := k * txHeightBucketStep
			hi := lo + txHeightBucketStep
			days := float64(hi) * 5 / 86400
			fmt.Fprintf(&sb, "  %7.0f-%-7.0f %12.0f 天  %12d   %12d\n",
				float64(lo)/1e6, float64(hi)/1e6, days, st.thAll[k], st.thSkipped[k])
		}
	}

	sb.WriteString("\n---- 按执行器分布（收录的交易）----\n")
	writeSortedCounts(&sb, st.byExecer, 30)
	if len(mf.AmtOneByKind) > 0 {
		sb.WriteString("\n---- amount==1 按交易类型分布 ----\n")
		writeSortedCounts(&sb, mf.AmtOneByKind, 10)
	}
	sb.WriteString("\n---- 按签名类型分布（收录的交易）----\n")
	writeSortedCounts(&sb, st.bySignTy, 10)
	sb.WriteString(nonceReport)
	if len(mf.Notes) > 0 {
		sb.WriteString("\n---- 备注 ----\n")
		for _, n := range mf.Notes {
			fmt.Fprintf(&sb, "  * %s\n", n)
		}
	}
	sb.WriteString("=======================================================\n")
	return sb.String()
}

func writeSortedCounts(sb *strings.Builder, m map[string]int64, limit int) {
	type kv struct {
		k string
		v int64
	}
	list := make([]kv, 0, len(m))
	var sum int64
	for k, v := range m {
		list = append(list, kv{k, v})
		sum += v
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	for i, e := range list {
		if i >= limit {
			fmt.Fprintf(sb, "  ... 其余 %d 项省略\n", len(list)-limit)
			break
		}
		fmt.Fprintf(sb, "  %-40s %10d  (%.2f%%)\n", e.k, e.v, pct(e.v, sum))
	}
}

func pct(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

// ---------- 诊断 ----------

func printCensus(db *leveldb.DB, top int) {
	type ent struct {
		cnt    int64
		sample []byte
	}
	m := make(map[string]*ent)
	var total int64
	it := db.NewIterator(nil, nil)
	defer it.Release()
	for it.Next() {
		total++
		k := it.Key()
		p := prefixOf(k)
		e := m[p]
		if e == nil {
			e = &ent{sample: append([]byte(nil), k...)}
			m[p] = e
		}
		e.cnt++
	}
	if err := it.Error(); err != nil {
		log.Fatalf("遍历失败: %v", err)
	}
	type kv struct {
		p string
		e *ent
	}
	list := make([]kv, 0, len(m))
	for p, e := range m {
		list = append(list, kv{p, e})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].e.cnt > list[j].e.cnt })
	fmt.Printf("datadir key 总数=%d，前缀数=%d\n", total, len(list))
	for i, e := range list {
		if i >= top {
			fmt.Printf("... 其余 %d 个前缀省略\n", len(list)-top)
			break
		}
		fmt.Printf("  %-28q %12d  sample=%x\n", e.p, e.e.cnt, truncate(e.e.sample, 24))
	}
}

func prefixOf(k []byte) string {
	for i := 0; i < len(k) && i < 30; i++ {
		if k[i] == ':' {
			return string(k[:i+1])
		}
	}
	if idx := bytes.Index(k, []byte("-d-")); idx > 0 {
		return string(k[:idx+3])
	}
	if idx := bytes.Index(k, []byte("-m-")); idx > 0 {
		return string(k[:idx+3])
	}
	if len(k) > 16 {
		return string(k[:16])
	}
	return string(k)
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

func probeHeightFn(db *leveldb.DB, height int64) {
	hash, err := db.Get([]byte(migrate.HeightPrefix+strconv.FormatInt(height, 10)), nil)
	fmt.Printf("height=%d\n", height)
	if err != nil {
		fmt.Printf("  Height:<h>        : 未命中（%v）\n", err)
		return
	}
	fmt.Printf("  Height:<h>        : %x\n", hash)
	if v, err := db.Get(append([]byte(migrate.BodyPrefixLegacy), hash...), nil); err == nil {
		fmt.Printf("  Body:<hash>       : 命中，len=%d\n", len(v))
	} else {
		fmt.Printf("  Body:<hash>       : 未命中\n")
	}
	pk := append([]byte(migrate.TableBodyDataPrefix), append([]byte(fmt.Sprintf("%012d", height)), hash...)...)
	if v, err := db.Get(pk, nil); err == nil {
		fmt.Printf("  CHAIN-body (012d) : 命中，len=%d\n", len(v))
	} else {
		fmt.Printf("  CHAIN-body (012d) : 未命中\n")
	}
	ip := append([]byte(migrate.TableBodyHashPrefix), hash...)
	ip = append(ip, '-')
	it := db.NewIterator(util.BytesPrefix(ip), nil)
	defer it.Release()
	n := 0
	for it.Next() {
		fmt.Printf("  body hash 索引    : key=%x value(primary)=%x\n", it.Key(), truncate(it.Value(), 40))
		n++
	}
	if n == 0 {
		fmt.Printf("  body hash 索引    : 无\n")
	}
}

// memString 便于观察内存占用。
func memString() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return fmt.Sprintf("%.0f MB (heap %.0f MB)", float64(m.Sys)/1e6, float64(m.HeapAlloc)/1e6)
}
