package event

import (
	_ "embed"
	"encoding/json"
	"sync"
)

// facesJSON 是 QQ 内置表情的"编号 -> 名称"映射表。
//
// 单独放在 faces.json 里、用 go:embed 打进二进制：数据与解析逻辑分离，便于核对与更新，
// 同时不依赖运行时存在外部文件。
//
//go:embed faces.json
var facesJSON []byte

// faceTable 是 faces.json 的结构。
type faceTable struct {
	Note   string            `json:"_note"`
	Source string            `json:"_source"`
	Usage  string            `json:"_usage"`
	Faces  map[string]string `json:"faces"`
}

var (
	faceOnce  sync.Once
	faceNames map[string]string
)

func loadFaces() {
	faceOnce.Do(func() {
		var t faceTable
		if err := json.Unmarshal(facesJSON, &t); err != nil {
			// 表坏掉不能让渲染崩：退化成"只有编号"。
			faceNames = map[string]string{}
			return
		}
		faceNames = t.Faces
	})
}

// FaceName 返回 QQ 内置表情的名称；表中没有该编号时返回 false。
func FaceName(id string) (string, bool) {
	loadFaces()
	name, ok := faceNames[id]
	return name, ok
}

// FaceCount 返回映射表条目数。
func FaceCount() int {
	loadFaces()
	return len(faceNames)
}
