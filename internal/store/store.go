// Package store 提供证据链的持久化状态存储。
//
// 所有写操作通过 Update 在单把互斥锁内完成“检查并落库”，
// 因此并发签署、并发晋级等场景不会越过缺失环节：
// 校验与状态翻转处于同一临界区。状态以 JSON 快照原子写入，
// 同时把事件追加到 jsonl 审计流。
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"example.com/batch-092001-q003/internal/domain"
)

// State 是全部聚合的内存视图。
type State struct {
	Seq            map[string]int64                      `json:"seq"`         // 各类 ID 序号
	SubjectSeq     map[string]int64                      `json:"subject_seq"` // 每个来源主体的递增序号
	Institutions   map[string]*domain.Institution        `json:"institutions"`
	Evidences      map[string]*domain.Evidence           `json:"evidences"`
	EvidenceEvents map[string]string                     `json:"evidence_events"` // event_id -> evidence_id，幂等接入
	Sources        map[string]*domain.PrescriptionSource `json:"sources"`
	Preparations   map[string]*domain.Preparation        `json:"preparations"`
	Lines          map[string]*domain.VersionLine        `json:"lines"`
	Versions       map[string]*domain.DocVersion         `json:"versions"`
	Batches        map[string]*domain.Batch              `json:"batches"`
	BatchByNo      map[string]string                     `json:"batch_by_no"`
	Distributions  map[string]*domain.Distribution       `json:"distributions"`
	Recalls        map[string]*domain.Recall             `json:"recalls"`
	Notices        map[string]*domain.RecallNotice       `json:"notices"`
	Research       map[string]*domain.ResearchRequest    `json:"research"`
	Authorizations map[string]*domain.Authorization      `json:"authorizations"`
	Suspensions    map[string]*domain.Suspension         `json:"suspensions"`
	Events         []domain.Event                        `json:"events"`
}

// NewState 返回空状态。
func NewState() *State {
	return &State{
		Seq:            map[string]int64{},
		SubjectSeq:     map[string]int64{},
		Institutions:   map[string]*domain.Institution{},
		Evidences:      map[string]*domain.Evidence{},
		EvidenceEvents: map[string]string{},
		Sources:        map[string]*domain.PrescriptionSource{},
		Preparations:   map[string]*domain.Preparation{},
		Lines:          map[string]*domain.VersionLine{},
		Versions:       map[string]*domain.DocVersion{},
		Batches:        map[string]*domain.Batch{},
		BatchByNo:      map[string]string{},
		Distributions:  map[string]*domain.Distribution{},
		Recalls:        map[string]*domain.Recall{},
		Notices:        map[string]*domain.RecallNotice{},
		Research:       map[string]*domain.ResearchRequest{},
		Authorizations: map[string]*domain.Authorization{},
		Suspensions:    map[string]*domain.Suspension{},
	}
}

// Store 持有状态并负责持久化。path 为空时仅驻留内存（测试用）。
type Store struct {
	mu        sync.Mutex
	state     *State
	path      string
	eventPath string
}

// Open 打开（或创建）位于 path 的存储。
func Open(path string) (*Store, error) {
	s := &Store{state: NewState(), path: path}
	if path == "" {
		return s, nil
	}
	s.eventPath = path + ".events.jsonl"
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, s.state); err != nil {
			return nil, fmt.Errorf("解析状态快照失败: %w", err)
		}
		// 以快照为准重建事件流，避免重复。
		if err := s.rewriteEvents(); err != nil {
			return nil, err
		}
	case os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return s, nil
}

// Update 在临界区内执行变更：fn 返回错误则状态整体回滚，
// 内存与磁盘均不留痕迹（含 ID 与来源序号计数器）。
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	before := len(s.state.Events)
	if err := fn(s.state); err != nil {
		restored := &State{}
		if uerr := json.Unmarshal(backup, restored); uerr != nil {
			return fmt.Errorf("回滚状态失败: %w（原始错误: %v）", uerr, err)
		}
		s.state = restored
		return err
	}
	return s.persistLocked(before)
}

// View 在临界区内执行只读访问。
func (s *Store) View(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.state)
}

func (s *Store) persistLocked(fromEvent int) error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if fromEvent < len(s.state.Events) {
		f, err := os.OpenFile(s.eventPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		for _, ev := range s.state.Events[fromEvent:] {
			if err := enc.Encode(ev); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) rewriteEvents() error {
	if s.eventPath == "" {
		return nil
	}
	f, err := os.OpenFile(s.eventPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range s.state.Events {
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	return nil
}

// NextID 分配带前缀的单调 ID，如 EV-000001。
func NextID(st *State, prefix string) string {
	st.Seq[prefix]++
	return fmt.Sprintf("%s-%06d", prefix, st.Seq[prefix])
}

// AppendEvent 追加一条审计事件；来源序号在同一主体内严格递增。
func AppendEvent(st *State, subjectRef, kind, occurredAt, digest string) domain.Event {
	st.SubjectSeq[subjectRef]++
	ev := domain.Event{
		EventID:        NextID(st, "EVT"),
		SubjectRef:     subjectRef,
		Kind:           kind,
		OccurredAt:     occurredAt,
		SourceSequence: st.SubjectSeq[subjectRef],
		PayloadDigest:  digest,
		RecordedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	st.Events = append(st.Events, ev)
	return ev
}
