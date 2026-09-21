package service

import (
	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/store"
)

// descendants 返回批次及其全部子孙批次的 ID 集合。
func descendants(st *store.State, batchID string) map[string]bool {
	set := map[string]bool{batchID: true}
	changed := true
	for changed {
		changed = false
		for _, b := range st.Batches {
			if b.ParentBatchID != "" && set[b.ParentBatchID] && !set[b.ID] {
				set[b.ID] = true
				changed = true
			}
		}
	}
	return set
}

// CreateRecall 发起跨院撤回：覆盖目标批次及其全部子批次，
// 向所有领取过这些批次的机构逐一发出撤回通知。
func (s *Service) CreateRecall(a Actor, batchID, reason string) (*domain.Recall, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if err := domain.RequireNonEmpty(reason, "reason"); err != nil {
		return nil, err
	}
	var out *domain.Recall
	err := s.st.Update(func(st *store.State) error {
		if _, ok := st.Batches[batchID]; !ok {
			return domain.NotFoundf("批次 %s 不存在", batchID)
		}
		set := descendants(st, batchID)
		recipients := map[string]bool{}
		for _, d := range st.Distributions {
			if set[d.BatchID] {
				recipients[d.ToInstitution] = true
			}
		}
		if len(recipients) == 0 {
			return domain.Conflictf("批次 %s 及其子批次尚未分发到任何机构，无需撤回", batchID)
		}
		recall := &domain.Recall{
			ID:        store.NextID(st, "RCL"),
			BatchID:   batchID,
			Reason:    reason,
			CreatedBy: a.ID,
			CreatedAt: now(),
		}
		for instID := range recipients {
			notice := &domain.RecallNotice{
				ID:            store.NextID(st, "NTC"),
				RecallID:      recall.ID,
				InstitutionID: instID,
				SentAt:        now(),
			}
			st.Notices[notice.ID] = notice
			recall.NoticeIDs = append(recall.NoticeIDs, notice.ID)
		}
		st.Recalls[recall.ID] = recall
		for id := range set {
			if b := st.Batches[id]; b.Status == domain.BatchActive {
				b.Status = domain.BatchRecalled
			}
		}
		store.AppendEvent(st, batchID, "batch.recalled", recall.CreatedAt, "")
		out = recall
		return nil
	})
	return out, err
}

// ConfirmRecall 领取机构确认撤回通知。
func (s *Service) ConfirmRecall(a Actor, recallID string) (*domain.RecallNotice, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.InstitutionID == "" {
		return nil, domain.Invalidf("缺少机构（X-Institution-Id）")
	}
	var out *domain.RecallNotice
	err := s.st.Update(func(st *store.State) error {
		recall, ok := st.Recalls[recallID]
		if !ok {
			return domain.NotFoundf("撤回 %s 不存在", recallID)
		}
		var notice *domain.RecallNotice
		for _, nid := range recall.NoticeIDs {
			n := st.Notices[nid]
			if n.InstitutionID == a.InstitutionID {
				notice = n
				break
			}
		}
		if notice == nil {
			return domain.NotFoundf("撤回 %s 没有发往机构 %s 的通知", recallID, a.InstitutionID)
		}
		if notice.ConfirmedAt != "" {
			return domain.Conflictf("机构 %s 已确认撤回通知 %s", a.InstitutionID, notice.ID)
		}
		notice.ConfirmedBy = a.ID
		notice.ConfirmedAt = now()
		store.AppendEvent(st, recall.BatchID, "recall.confirmed:"+a.InstitutionID, notice.ConfirmedAt, "")
		out = notice
		return nil
	})
	return out, err
}

// RecallCoverage 是撤回覆盖情况：是否覆盖全部领取机构。
type RecallCoverage struct {
	RecallID  string   `json:"recall_id"`
	BatchID   string   `json:"batch_id"`
	Total     int      `json:"total"`
	Confirmed int      `json:"confirmed"`
	Missing   []string `json:"missing"`
	Complete  bool     `json:"complete"`
}

// GetRecallCoverage 计算撤回通知对领取机构的覆盖情况。
func (s *Service) GetRecallCoverage(recallID string) (*RecallCoverage, error) {
	var out *RecallCoverage
	err := s.st.View(func(st *store.State) error {
		cov, err := recallCoverage(st, recallID)
		if err != nil {
			return err
		}
		out = cov
		return nil
	})
	return out, err
}

func recallCoverage(st *store.State, recallID string) (*RecallCoverage, error) {
	recall, ok := st.Recalls[recallID]
	if !ok {
		return nil, domain.NotFoundf("撤回 %s 不存在", recallID)
	}
	cov := &RecallCoverage{RecallID: recall.ID, BatchID: recall.BatchID}
	for _, nid := range recall.NoticeIDs {
		n := st.Notices[nid]
		cov.Total++
		if n.ConfirmedAt != "" {
			cov.Confirmed++
		} else {
			cov.Missing = append(cov.Missing, n.InstitutionID)
		}
	}
	cov.Complete = cov.Total > 0 && cov.Confirmed == cov.Total
	return cov, nil
}
