// build 工具：把转换后的状态写入新链 datadir（离线预载）。
//
// 用法（2026-11-25 新方案：创世状态为空 → 不写 store.db，不预载状态）：
//
//	build -datadir <新datadir> -local txhash.bin -local evmnonce.bin
//
// 写入内容：
//
//	<datadir>/blockchain.db : 本地键（防重放索引 TX:/STX:、eth nonce 等）
//
// 说明：新链创世状态为空——ycc 由创世票铸出、票由创世块生成，故本工具不再写 <datadir>/kvmvcc/store.db
// （store.db 由节点首次启动时自行创建）。这也让 `-state` 参数彻底移除。
//
// 新链节点首次启动：创世块无交易 → 空变更集 → kvmvcc 自动注册版本0 元信息（stateHash=空变更集哈希），
// 预载的版本0 数据即成为创世状态。之后 ticket 共识从高度 1 正常出块。
package main

import (
	"encoding/gob"
	"flag"
	"io"
	"log"
	"os"
	"path"
	"strings"

	"github.com/33cn/chain33/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/yccproject/ycc/migrate"
)

func main() {
	datadir := flag.String("datadir", "datadir", "新链 datadir 目录（须为空或不存在）")
	var localFiles strList
	flag.Var(&localFiles, "local", "本地键文件（可重复；支持 gob 与 YCCKV1 两种格式）")
	flag.Parse()

	blockPath := path.Join(*datadir, "blockchain.db")
	if err := os.MkdirAll(*datadir, 0o755); err != nil {
		log.Fatalf("创建目录失败: %v", err)
	}
	log.Printf("本地键文件 %d 个（不写 store.db：创世状态为空）", len(localFiles))

	// blockchain.db：本地键
	bdb, err := leveldb.OpenFile(blockPath, nil)
	if err != nil {
		log.Fatalf("打开 blockchain.db 失败: %v", err)
	}
	for _, p := range localFiles {
		n, err := writeLocalFile(bdb, p)
		if err != nil {
			log.Fatalf("写入本地键文件 %s 失败: %v", p, err)
		}
		log.Printf("本地键文件 %s：写入 %d 条", p, n)
	}
	if err := bdb.Close(); err != nil {
		log.Fatalf("关闭 blockchain.db 失败: %v", err)
	}
	log.Printf("blockchain.db 写入完成: %s", blockPath)
	log.Printf("完成：新 datadir 已就绪，可直接用新配置启动 ycc 节点")
}

// writeChunked 分批写入（大数据量时控制单批内存），空值键跳过
func writeChunked(db *leveldb.DB, kvs []*types.KeyValue, keyOf func(*types.KeyValue) []byte) {
	const chunk = 100000
	for start := 0; start < len(kvs); start += chunk {
		end := start + chunk
		if end > len(kvs) {
			end = len(kvs)
		}
		batch := new(leveldb.Batch)
		for _, kv := range kvs[start:end] {
			if len(kv.Value) == 0 {
				continue
			}
			batch.Put(keyOf(kv), kv.Value)
		}
		if err := db.Write(batch, &opt.WriteOptions{Sync: false}); err != nil {
			log.Fatalf("写 leveldb 失败: %v", err)
		}
	}
}

// strList 可重复的字符串参数
type strList []string

func (s *strList) String() string { return strings.Join(*s, ",") }

func (s *strList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// writeLocalFile 把本地键文件写入 blockchain.db；自动识别 gob / YCCKV1。
// YCCKV 走流式（用于几百万~上亿条的大索引），gob 沿用一次性加载。
func writeLocalFile(db *leveldb.DB, p string) (int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	head := make([]byte, 8)
	n, _ := io.ReadFull(f, head)
	f.Close()
	if migrate.IsKVStreamHead(head[:n]) {
		var written int64
		var batch = new(leveldb.Batch)
		var inBatch int64
		flush := func() error {
			if inBatch == 0 {
				return nil
			}
			err := db.Write(batch, &opt.WriteOptions{Sync: false})
			batch = new(leveldb.Batch)
			inBatch = 0
			return err
		}
		count, err := migrate.ReadKVRecords(p, func(k, v []byte) error {
			if len(v) == 0 {
				return nil
			}
			batch.Put(k, v)
			inBatch++
			written++
			if inBatch >= 100000 {
				return flush()
			}
			return nil
		})
		if err != nil {
			return written, err
		}
		if err := flush(); err != nil {
			return written, err
		}
		_ = count
		return written, nil
	}
	kvs := loadKVFile(p)
	writeChunked(db, kvs, func(kv *types.KeyValue) []byte { return kv.Key })
	return int64(len(kvs)), nil
}

func loadKVFile(p string) []*types.KeyValue {
	f, err := os.Open(p)
	if err != nil {
		log.Fatalf("打开 %s 失败: %v", p, err)
	}
	defer f.Close()
	var kvs []*types.KeyValue
	if err := gob.NewDecoder(f).Decode(&kvs); err != nil {
		log.Fatalf("解析 %s 失败: %v", p, err)
	}
	return kvs
}
