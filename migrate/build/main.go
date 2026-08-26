// build 工具：把转换后的状态写入新链 datadir（离线预载）。
//
// 用法：
//
//	build -datadir <新datadir> -state out/state.bin -local out/local.bin
//
// 写入内容：
//   1) <datadir>/kvmvcc/store.db   : 每个状态键写入 .-mvcc-.d.<key>.00000000000000000000（版本0）
//   2) <datadir>/blockchain.db     : 本地键（LODB-ticket-*，与区块同库）
// 新链节点首次启动：创世块无交易 → 空变更集 → kvmvcc 自动注册版本0 元信息（stateHash=空变更集哈希），
// 预载的版本0 数据即成为创世状态。之后 ticket 共识从高度 1 正常出块。
package main

import (
	"encoding/gob"
	"flag"
	"log"
	"os"
	"path"

	"github.com/33cn/chain33/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/yccproject/ycc/migrate"
)

func main() {
	datadir := flag.String("datadir", "datadir", "新链 datadir 目录（须为空或不存在）")
	stateFile := flag.String("state", "out/state.bin", "状态键文件")
	localFile := flag.String("local", "out/local.bin", "本地键文件")
	flag.Parse()

	storePath := path.Join(*datadir, "kvmvcc", "store.db")
	blockPath := path.Join(*datadir, "blockchain.db")
	if err := os.MkdirAll(path.Dir(storePath), 0o755); err != nil {
		log.Fatalf("创建目录失败: %v", err)
	}

	stateKVs := loadKVFile(*stateFile)
	localKVs := loadKVFile(*localFile)
	log.Printf("状态键 %d 个，本地键 %d 个", len(stateKVs), len(localKVs))

	// 1) store.db：版本0 数据键
	sdb, err := leveldb.OpenFile(storePath, nil)
	if err != nil {
		log.Fatalf("打开 store.db 失败: %v", err)
	}
	writeChunked(sdb, stateKVs, func(kv *types.KeyValue) []byte { return migrate.MVCCDataKey(kv.Key, 0) })
	if err := sdb.Close(); err != nil {
		log.Fatalf("关闭 store.db 失败: %v", err)
	}
	log.Printf("store.db 写入完成: %s", storePath)

	// 2) blockchain.db：LODB-* 本地键
	bdb, err := leveldb.OpenFile(blockPath, nil)
	if err != nil {
		log.Fatalf("打开 blockchain.db 失败: %v", err)
	}
	writeChunked(bdb, localKVs, func(kv *types.KeyValue) []byte { return kv.Key })
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
