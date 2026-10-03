package history

import (
	"bufio"
	"encoding/json"
	"fmt"
)

// fileRecord 是 JSONL 历史文件的一行。
//
// 它一度定义在 File 适配器里，于是 ImportJSONL 被迫依赖 File；抽到本文件后，
// JSONL 只是**格式**：File（历史适配器）与 ImportJSONL（迁移路径）都只依赖它。
type fileRecord struct {
	Key  string `json:"key"`
	Item Item   `json:"item"`
}

// marshalRecord 把一行记录编码成 JSON（不含换行）。
func marshalRecord(rec fileRecord) ([]byte, error) {
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("encode history record: %w", err)
	}
	return line, nil
}

// writeRecord 把一行记录写入 writer（含换行）。
func writeRecord(w *bufio.Writer, rec fileRecord) error {
	line, err := marshalRecord(rec)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write history record: %w", err)
	}
	return nil
}

// readRecord 解码一行 JSONL；损坏的行由调用方决定跳过还是中止。
func readRecord(line []byte) (fileRecord, error) {
	var rec fileRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return fileRecord{}, err
	}
	return rec, nil
}
