package store

import (
	"testing"
	"time"

	"filesync/internal/model"
)

// TestListDirEmptyKeepDirectory 验证「新建文件夹不显示」根因修复：
// 空目录（仅含 .keep 占位文件）必须在 ListDir 子目录列表中显示，且文件数计数排除 .keep。
// 同时验证直接文件列表不含 .keep，以及 Mkdir 存在性检查（ListFiles）仍能查到 .keep。
func TestListDirEmptyKeepDirectory(t *testing.T) {
	tmpPath := t.TempDir() + "/test_listdir.db"
	db, err := New(tmpPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	owner := "testuser"

	mk := func(id, filename string, size int64) *model.FileRecord {
		return &model.FileRecord{
			ID: id, Filename: filename, Size: size, Hash: "",
			StoragePath: "local:" + filename, StorageType: "local",
			ChunkSize: 512, TotalChunks: 0, Status: "completed",
			Owner: owner, CreatedAt: now, UpdatedAt: now,
		}
	}

	// 场景1：空目录 aaa（仅 .keep）
	if err := db.CreateFile(mk("id-aaa-keep", "aaa/.keep", 0)); err != nil {
		t.Fatalf("CreateFile aaa/.keep: %v", err)
	}
	// 场景2：非空目录 bbb（.keep + 1 个普通文件）
	if err := db.CreateFile(mk("id-bbb-keep", "bbb/.keep", 0)); err != nil {
		t.Fatalf("CreateFile bbb/.keep: %v", err)
	}
	if err := db.CreateFile(mk("id-bbb-file", "bbb/file.txt", 10)); err != nil {
		t.Fatalf("CreateFile bbb/file.txt: %v", err)
	}
	// 场景3：二级空目录 study/ccc（仅 .keep）
	if err := db.CreateFile(mk("id-ccc-keep", "study/ccc/.keep", 0)); err != nil {
		t.Fatalf("CreateFile study/ccc/.keep: %v", err)
	}

	// === 根目录列表 ===
	dirs, files, err := db.ListDir("", owner)
	if err != nil {
		t.Fatalf("ListDir root: %v", err)
	}

	foundAAA, foundBBB := false, false
	for _, d := range dirs {
		switch d.Name {
		case "aaa":
			foundAAA = true
			if d.Count != 0 {
				t.Errorf("aaa count=%d, want 0 (only .keep)", d.Count)
			}
		case "bbb":
			foundBBB = true
			if d.Count != 1 {
				t.Errorf("bbb count=%d, want 1 (one non-keep file)", d.Count)
			}
		}
	}
	if !foundAAA {
		t.Errorf("[根因未修复] 空目录 aaa 未显示在根目录列表中")
	}
	if !foundBBB {
		t.Errorf("非空目录 bbb 未显示在根目录列表中")
	}

	// === 直接文件列表不含 .keep ===
	for _, f := range files {
		if isKeep(f.Filename) {
			t.Errorf(".keep 出现在直接文件列表: %s", f.Filename)
		}
	}

	// === 二级空目录 study/ccc 必须显示 ===
	studyDirs, _, err := db.ListDir("study/", owner)
	if err != nil {
		t.Fatalf("ListDir study/: %v", err)
	}
	foundCCC := false
	for _, d := range studyDirs {
		if d.Name == "ccc" {
			foundCCC = true
			if d.Count != 0 {
				t.Errorf("ccc count=%d, want 0", d.Count)
			}
		}
	}
	if !foundCCC {
		t.Errorf("[根因未修复] 二级空目录 study/ccc 未显示")
	}

	// === Mkdir 存在性检查：ListFiles 仍能查到 .keep（第二次创建提示已存在的依据）===
	existing, err := db.ListFiles("aaa/", owner)
	if err != nil {
		t.Fatalf("ListFiles aaa/: %v", err)
	}
	if len(existing) == 0 {
		t.Errorf("ListFiles(aaa/) 查不到 .keep，Mkdir 存在性检查会失效")
	}
}

// isKeep 判断文件名是否为 .keep 占位文件。
func isKeep(filename string) bool {
	if len(filename) < 5 {
		return false
	}
	return filename[len(filename)-5:] == ".keep"
}
