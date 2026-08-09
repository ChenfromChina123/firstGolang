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
	dirs, files, err := db.ListDir("", "", owner)
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
	studyDirs, _, err := db.ListDir("", "study/", owner)
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
	existing, err := db.ListFiles("", "aaa/", owner)
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

// TestMigrateAssignDefaultSpace 验证「现有数据兼容」数据迁移：
// 引入多空间前创建的历史文件 space_id=”，迁移后应归入所属用户的「我的空间」
// （space_id='default-<owner>'）；owner=” 的历史公共文件与已有自定义空间文件不受影响；
// 回收站文件一并迁移（恢复后仍在原空间）；迁移幂等（二次执行不再变更）。
func TestMigrateAssignDefaultSpace(t *testing.T) {
	tmpPath := t.TempDir() + "/test_migrate_space.db"
	db, err := New(tmpPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	mk := func(id, filename, owner, spaceID string, deleted bool) *model.FileRecord {
		rec := &model.FileRecord{
			ID: id, Filename: filename, Size: 10, Hash: "",
			StoragePath: "local:" + filename, StorageType: "local",
			ChunkSize: 512, TotalChunks: 0, Status: "completed",
			Owner: owner, SpaceID: spaceID, CreatedAt: now, UpdatedAt: now,
		}
		if deleted {
			rec.DeletedAt = &now
		}
		return rec
	}

	// 准备数据：
	// 1) alice 的历史文件（space_id=''，未删除）→ 应迁移为 default-alice
	// 2) alice 的回收站文件（space_id=''，已删除）→ 应迁移为 default-alice
	// 3) 历史公共文件（owner=''）→ 保持 ''
	// 4) alice 自定义空间文件（space_id='work-x'）→ 不受影响
	// 5) admin 文件（space_id=''）→ 应迁移为 default-admin
	for _, rec := range []*model.FileRecord{
		mk("id-alice-doc", "a/report.pdf", "alice", "", false),
		mk("id-alice-trash", "a/old.txt", "alice", "", true),
		mk("id-public", "pub.txt", "", "", false),
		mk("id-work", "w/work.txt", "alice", "work-x", false),
		mk("id-admin", "adm.txt", "admin", "", false),
	} {
		if err := db.CreateFile(rec); err != nil {
			t.Fatalf("CreateFile %s: %v", rec.ID, err)
		}
	}

	if err := db.migrateAssignDefaultSpace(); err != nil {
		t.Fatalf("migrateAssignDefaultSpace: %v", err)
	}

	checkSpace := func(id, want string) {
		t.Helper()
		f, err := db.GetFile(id)
		if err != nil {
			t.Fatalf("GetFile %s: %v", id, err)
		}
		if f.SpaceID != want {
			t.Errorf("file %s: space_id = %q, want %q", id, f.SpaceID, want)
		}
	}
	checkSpace("id-alice-doc", "default-alice")
	checkSpace("id-alice-trash", "default-alice")
	checkSpace("id-public", "")
	checkSpace("id-work", "work-x")
	checkSpace("id-admin", "default-admin")

	// 幂等性：二次执行不报错且结果不变
	if err := db.migrateAssignDefaultSpace(); err != nil {
		t.Fatalf("migrateAssignDefaultSpace (2nd): %v", err)
	}
	checkSpace("id-alice-doc", "default-alice")
}

// TestFindActiveSessionByHashSpaceIsolation 验证断点续传 session 查询的空间隔离：
// 修复前 FindActiveSessionByHash 仅按 file_hash+filename 查询，导致在不同空间上传同名同 hash
// 文件时会复用其他空间的 session（文件最终落入错误空间 / chunks 数量不匹配报错）。
// 修复后必须按 space_id 过滤：同空间能找到，跨空间返回 nil。
func TestFindActiveSessionByHashSpaceIsolation(t *testing.T) {
	tmpPath := t.TempDir() + "/test_session_space.db"
	db, err := New(tmpPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	now := time.Now()
	hash := "abc123"
	filename := "photo.png"

	// 默认空间（default-alice）和自定义空间（work-x）各有一个同 hash 同文件名的活跃 session
	s1 := &model.UploadSession{
		ID: "sess-default", Filename: filename, FileSize: 100, FileHash: hash,
		ChunkSize: 512, TotalChunks: 1, Status: "active", StorageType: "local",
		SpaceID: "default-alice", CreatedAt: now, UpdatedAt: now,
	}
	s2 := &model.UploadSession{
		ID: "sess-work", Filename: filename, FileSize: 100, FileHash: hash,
		ChunkSize: 512, TotalChunks: 1, Status: "active", StorageType: "local",
		SpaceID: "work-x", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}
	if err := db.CreateUploadSession(s1); err != nil {
		t.Fatalf("CreateUploadSession s1: %v", err)
	}
	if err := db.CreateUploadSession(s2); err != nil {
		t.Fatalf("CreateUploadSession s2: %v", err)
	}

	// 同空间能找到自己的 session
	got, err := db.FindActiveSessionByHash("work-x", hash, filename)
	if err != nil {
		t.Fatalf("FindActiveSessionByHash(work-x): %v", err)
	}
	if got == nil || got.ID != "sess-work" {
		t.Errorf("[根因未修复] work-x 空间未找到自己的 session, got=%+v", got)
	}

	// 跨空间不得复用：other-space 应返回 nil
	got2, err := db.FindActiveSessionByHash("other-space", hash, filename)
	if err != nil {
		t.Fatalf("FindActiveSessionByHash(other-space): %v", err)
	}
	if got2 != nil {
		t.Errorf("[根因未修复] 跨空间复用了 session %s, want nil", got2.ID)
	}

	// default-alice 能找到自己的（与 work-x 隔离）
	got3, err := db.FindActiveSessionByHash("default-alice", hash, filename)
	if err != nil {
		t.Fatalf("FindActiveSessionByHash(default-alice): %v", err)
	}
	if got3 == nil || got3.ID != "sess-default" {
		t.Errorf("default-alice 未找到自己的 session, got=%+v", got3)
	}

	// 已完成的 session 不参与续传
	if err := db.UpdateUploadSessionStatus("sess-default", "completed"); err != nil {
		t.Fatalf("UpdateUploadSessionStatus: %v", err)
	}
	got4, err := db.FindActiveSessionByHash("default-alice", hash, filename)
	if err != nil {
		t.Fatalf("FindActiveSessionByHash after completed: %v", err)
	}
	if got4 != nil {
		t.Errorf("completed session 仍被复用, got=%+v", got4)
	}
}
