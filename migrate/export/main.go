// export 工具：读取旧链 kvmvcc store.db，导出最终全量状态。
//
// 用法：
//
//	export -store <旧节点datadir>/kvmvcc/store.db -out dump.gob
//
// 原理：kvmvcc 存储所有历史版本的变更键（.-mvcc-.d.<key>.<版本>），
// 逻辑键的最新版本即最终状态。遍历该前缀，逐键保留最大版本值。
package main

import (
	"encoding/gob"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/33cn/chain33/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
	"github.com/yccproject/ycc/migrate"
)

func main() {
	storePath := flag.String("store", "", "旧节点 kvmvcc store.db 路径（datadir/kvmvcc/store.db）")
	out := flag.String("out", "dump.gob", "导出文件")
	flag.Parse()
	if *storePath == "" {
		log.Fatal("需要 -store 参数")
	}

	db, err := leveldb.OpenFile(*storePath, &opt.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("打开 store.db 失败: %v", err)
	}
	defer db.Close()

	dump := &migrate.Dump{}
	latest := make(map[string][]byte) // 逻辑键 -> 最终值
	maxVersion := int64(-1)

	iter := db.NewIterator(util.BytesPrefix([]byte(".-mvcc-.d.")), nil)
	count := 0
	for iter.Next() {
		logical, ver, ok := migrate.ParseMVCCKey(iter.Key())
		if !ok {
			continue
		}
		if ver > maxVersion {
			maxVersion = ver
		}
		// 升序扫描：同逻辑键后出现的版本号更大（20位补零字典序==数值序），
		// 因此直接覆盖，最终保留最大版本值。
		v := make([]byte, len(iter.Value()))
		copy(v, iter.Value())
		latest[string(logical)] = v
		count++
		if count%5000000 == 0 {
			log.Printf("扫描中... %d 条变更，当前 maxVersion=%d", count, maxVersion)
		}
	}
	if err := iter.Error(); err != nil {
		log.Fatalf("迭代错误: %v", err)
	}
	iter.Release()
	log.Printf("共扫描 %d 条变更，maxVersion(≈旧链高度)=%d，逻辑键数=%d", count, maxVersion, len(latest))

	// 读版本元信息：最大版本对应的 stateHash
	if maxVersion >= 0 {
		metaKey := fmt.Sprintf("%sversion.%020d", migrate.MetaPrefix(), maxVersion)
		sh, err := db.Get([]byte(metaKey), nil)
		if err == nil {
			dump.StateHash = sh
		}
		dump.Height = maxVersion
	}
	dump.KVs = make([]*types.KeyValue, 0, len(latest))
	for k, v := range latest {
		dump.KVs = append(dump.KVs, &types.KeyValue{Key: []byte(k), Value: v})
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("创建输出文件失败: %v", err)
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(dump); err != nil {
		log.Fatalf("写入 dump 失败: %v", err)
	}
	log.Printf("导出完成: %s（%d 键，高度 %d，stateHash %x）", *out, len(dump.KVs), dump.Height, dump.StateHash)
}
