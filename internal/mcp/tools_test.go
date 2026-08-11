package mcp

import (
	"reflect"
	"strings"
	"testing"
)

func TestFSDeleteInputUsesTokenSpace(t *testing.T) {
	if _, ok := reflect.TypeOf(fsDeleteInput{}).FieldByName("SpaceID"); ok {
		t.Fatal("fs_delete should use the token-bound space, not require space_id")
	}
}

func TestNormalizeSpaceUsesTokenBoundSpace(t *testing.T) {
	tc := &ToolContext{Username: "alice", Role: "user", SpaceID: "47a2efe5"}
	if got := normalizeSpace(tc, ""); got != tc.SpaceID {
		t.Fatalf("normalizeSpace() = %q, want token space %q", got, tc.SpaceID)
	}
}

func TestServerInstructionsLoadOnboardingOncePerSession(t *testing.T) {
	for _, want := range []string{
		"新会话入职规则",
		"whoami",
		"_index/mcp-onboarding.md",
		"同一 AI 会话只加载一次",
		"新的 AI 会话重新加载",
		"filesync:write",
		"fs_mkdir",
		"fs_write",
		"自动初始化必须幂等",
		"mode: file-manager",
		"不自动创建 docs/skills/projects",
		"mode: repository",
		"不得只凭目录存在",
	} {
		if !strings.Contains(serverInstructions, want) {
			t.Fatalf("server instructions missing %q", want)
		}
	}
}
