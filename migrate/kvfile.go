// YCCKV1 —— 迁移工具链的流式键值文件格式（可处理几十亿字节，无需整块驻留内存）。
//
// 布局：
//
//	magic   : 6 字节 "YCCKV1"
//	count   : 8 字节小端“预估条数”（仅参考，读取以 EOF 为准）
//	records : 重复 { u32le keyLen | u32le valLen | key | val }
//	          valLen==0 表示删除该键（build 语义：空值跳过）
//
// 与旧的 gob([]*types.KeyValue) 并存：build 按 magic 自动识别。
package migrate

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/33cn/chain33/types"
)

const (
	// KVFormatName 格式名（写入 manifest）
	KVFormatName = "ycckv1"
	kvMagic      = "YCCKV1"
)

// WriteKVHeader 写 magic + 预估条数。
func WriteKVHeader(w io.Writer, hint int64) error {
	if _, err := w.Write([]byte(kvMagic)); err != nil {
		return err
	}
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(hint))
	_, err := w.Write(b[:])
	return err
}

// WriteKVRecord 写一条记录。
func WriteKVRecord(w io.Writer, key, val []byte) error {
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(key)))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(val)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	buf := make([]byte, 0, len(key)+len(val))
	buf = append(buf, key...)
	buf = append(buf, val...)
	_, err := w.Write(buf)
	return err
}

// WriteKVFile 把（小体量）键值按 key 升序写成 YCCKV 文件，返回文件 sha256。
func WriteKVFile(path string, kvs []*types.KeyValue) (string, error) {
	sort.Slice(kvs, func(i, j int) bool { return bytes.Compare(kvs[i].Key, kvs[j].Key) < 0 })
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	w := bufio.NewWriterSize(io.MultiWriter(f, h), 1<<20)
	if err := WriteKVHeader(w, int64(len(kvs))); err != nil {
		f.Close()
		return "", err
	}
	for _, kv := range kvs {
		if err := WriteKVRecord(w, kv.Key, kv.Value); err != nil {
			f.Close()
			return "", err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// IsKVStreamHead 判断文件头部是否为 YCCKV 格式（否则按 gob 处理）。
func IsKVStreamHead(head []byte) bool {
	return len(head) >= len(kvMagic) && string(head[:len(kvMagic)]) == kvMagic
}

// ReadKVRecords 流式读取 YCCKV 文件，逐条回调。
func ReadKVRecords(path string, fn func(key, val []byte) error) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	head := make([]byte, len(kvMagic))
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, fmt.Errorf("读 magic 失败: %w", err)
	}
	if string(head) != kvMagic {
		return 0, fmt.Errorf("不是 %s 格式（magic=%q）", KVFormatName, head)
	}
	var cnt [8]byte
	if _, err := io.ReadFull(r, cnt[:]); err != nil {
		return 0, fmt.Errorf("读 count 失败: %w", err)
	}
	var n int64
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return n, nil
			}
			if err == io.ErrUnexpectedEOF {
				return n, fmt.Errorf("记录头被截断（文件不完整？）")
			}
			return n, err
		}
		kl := int(binary.LittleEndian.Uint32(hdr[0:4]))
		vl := int(binary.LittleEndian.Uint32(hdr[4:8]))
		if kl <= 0 || kl > 1<<16 || vl > 1<<20 {
			return n, fmt.Errorf("记录长度异常 key=%d val=%d", kl, vl)
		}
		k := make([]byte, kl)
		if _, err := io.ReadFull(r, k); err != nil {
			return n, fmt.Errorf("读 key 失败: %w", err)
		}
		var v []byte
		if vl > 0 {
			v = make([]byte, vl)
			if _, err := io.ReadFull(r, v); err != nil {
				return n, fmt.Errorf("读 value 失败: %w", err)
			}
		}
		if err := fn(k, v); err != nil {
			return n, err
		}
		n++
	}
}
