package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(st *State) error {
		AppendEvent(st, "SUBJ-1", "kind.a", "2026-01-01T00:00:00+08:00", "sha256:abc")
		AppendEvent(st, "SUBJ-1", "kind.b", "2026-01-02T00:00:00+08:00", "")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.View(func(st *State) error {
		if len(st.Events) != 2 {
			t.Fatalf("重开后应有 2 条事件，实际 %d", len(st.Events))
		}
		if st.Events[0].SourceSequence != 1 || st.Events[1].SourceSequence != 2 {
			t.Fatal("来源序号应在重开后保持")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 事件流文件逐行追加，且重开后不重复。
	data, err := os.ReadFile(path + ".events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	if lines != 2 {
		t.Fatalf("事件流应有 2 行，实际 %d", lines)
	}
	if err := reopened.Update(func(st *State) error {
		AppendEvent(st, "SUBJ-1", "kind.c", "2026-01-03T00:00:00+08:00", "")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path + ".events.jsonl")
	lines = strings.Count(strings.TrimSpace(string(data)), "\n") + 1
	if lines != 3 {
		t.Fatalf("追加后事件流应有 3 行，实际 %d", lines)
	}
}

func TestFailedUpdateLeavesNoTrace(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	wantErr := s.Update(func(st *State) error {
		AppendEvent(st, "SUBJ-2", "kind.x", "2026-01-01T00:00:00+08:00", "")
		return errBoom
	})
	if wantErr == nil {
		t.Fatal("应返回错误")
	}
	_ = s.View(func(st *State) error {
		if len(st.Events) != 0 {
			t.Fatal("失败的更新不得留下事件")
		}
		if st.SubjectSeq["SUBJ-2"] != 0 {
			t.Fatal("失败的更新不得推进来源序号")
		}
		return nil
	})
}

type boomError struct{}

func (boomError) Error() string { return "boom" }

var errBoom = boomError{}
