package agentsvc

import (
	"context"
	"testing"
	"time"

	"filesync/internal/model"
	"filesync/internal/store"
)

func newAgentServiceFixture(t *testing.T) (*AgentSvc, *store.DB) {
	t.Helper()
	db, err := store.New(t.TempDir() + "/files.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db, nil, ""), db
}

func addCompletedFile(t *testing.T, db *store.DB, id, name, space, owner string) {
	t.Helper()
	now := time.Now()
	err := db.CreateFile(&model.FileRecord{
		ID: id, Filename: name, Size: 1, Hash: id,
		StoragePath: "local:" + id, StorageType: "local", Status: "completed",
		Owner: owner, SpaceID: space, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMoveReportsMatchedFilesAndUpdatesOnlyRequestedSpace(t *testing.T) {
	svc, db := newAgentServiceFixture(t)
	addCompletedFile(t, db, "a", "docs/readme.md", "space-a", "alice")
	addCompletedFile(t, db, "b", "docs/other.md", "space-b", "alice")

	moved, err := svc.Move(context.Background(), "docs/", "archive/", "space-a", "alice", "user")
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("moved = %d, want 1", moved)
	}

	if _, err := db.FindFileByName("space-a", "archive/readme.md", "alice"); err != nil {
		t.Fatalf("moved file not found at destination: %v", err)
	}
	if _, err := db.FindFileByName("space-b", "docs/other.md", "alice"); err != nil {
		t.Fatalf("file in another space was changed: %v", err)
	}
}

func TestMoveReportsZeroWhenSourceDoesNotExist(t *testing.T) {
	svc, db := newAgentServiceFixture(t)
	addCompletedFile(t, db, "a", "notes/readme.md", "space-a", "alice")

	moved, err := svc.Move(context.Background(), "docs/", "archive/", "space-a", "alice", "user")
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 {
		t.Fatalf("moved = %d, want 0", moved)
	}
}

func TestDeletePathHonorsSpaceSandbox(t *testing.T) {
	svc, db := newAgentServiceFixture(t)
	addCompletedFile(t, db, "a", "docs/readme.md", "space-a", "alice")
	addCompletedFile(t, db, "b", "docs/other.md", "space-b", "alice")

	deleted, err := svc.Delete(context.Background(), []string{"docs/readme.md"}, nil, "space-a", "alice", "user")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := db.GetFile("b"); err != nil {
		t.Fatalf("file in another space was deleted: %v", err)
	}
}
