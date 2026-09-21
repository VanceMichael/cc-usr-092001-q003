package service

import (
	"sort"

	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/store"
)

// VersionAudit 记录批次依据的某一版本及其完整基线链。
type VersionAudit struct {
	Version *domain.DocVersion `json:"version"`
	Chain   []string           `json:"chain"` // 从首版到本版的版本 ID
}

// CirculationEntry 是批次在某机构的流通数量。
type CirculationEntry struct {
	InstitutionID string `json:"institution_id"`
	Quantity      int    `json:"quantity"`
}

// BatchAudit 是审计视图：输入批号即可看到依据的方证、工艺与质控版本、
// 各次授权、当前可流通范围，以及撤回通知对领取机构的覆盖情况。
type BatchAudit struct {
	Batch          *domain.Batch              `json:"batch"`
	Preparation    *domain.Preparation        `json:"preparation"`
	Source         *domain.PrescriptionSource `json:"source"`
	Evidence       []domain.EvidenceView      `json:"evidence"`
	Composition    *VersionAudit              `json:"composition"`
	Process        *VersionAudit              `json:"process"`
	QC             *VersionAudit              `json:"qc"`
	Authorizations []domain.Authorization     `json:"authorizations"`
	Circulation    []CirculationEntry         `json:"circulation"`
	Recalls        []RecallCoverage           `json:"recalls"`
	Suspensions    []domain.Suspension        `json:"suspensions"`
}

// AuditBatch 按批号生成完整追溯报告。
func (s *Service) AuditBatch(batchNo string) (*BatchAudit, error) {
	var out *BatchAudit
	err := s.st.View(func(st *store.State) error {
		id, ok := st.BatchByNo[batchNo]
		if !ok {
			return domain.NotFoundf("批号 %s 不存在", batchNo)
		}
		batch := st.Batches[id]
		prep := st.Preparations[batch.PreparationID]
		src := st.Sources[prep.SourceID]

		report := &BatchAudit{Batch: batch, Preparation: prep, Source: src}

		for _, evID := range src.EvidenceIDs {
			if ev, ok := st.Evidences[evID]; ok {
				report.Evidence = append(report.Evidence, ev.View())
			}
		}

		var err error
		if report.Composition, err = versionAudit(st, batch.CompositionVersionID); err != nil {
			return err
		}
		if report.Process, err = versionAudit(st, batch.ProcessVersionID); err != nil {
			return err
		}
		if report.QC, err = versionAudit(st, batch.QCVersionID); err != nil {
			return err
		}

		// 批次族：本批次及其全部子孙，用于授权与流通范围。
		family := descendants(st, batch.ID)
		authSet := map[string]bool{}
		for _, d := range st.Distributions {
			if family[d.BatchID] {
				authSet[d.AuthorizationID] = true
			}
		}
		for _, auth := range st.Authorizations {
			if authSet[auth.ID] {
				report.Authorizations = append(report.Authorizations, *auth)
			}
		}
		sort.Slice(report.Authorizations, func(i, j int) bool {
			return report.Authorizations[i].ID < report.Authorizations[j].ID
		})

		circulation := map[string]int{}
		for _, d := range st.Distributions {
			if family[d.BatchID] {
				circulation[d.ToInstitution] += d.Quantity
			}
		}
		for instID, qty := range circulation {
			report.Circulation = append(report.Circulation, CirculationEntry{InstitutionID: instID, Quantity: qty})
		}
		sort.Slice(report.Circulation, func(i, j int) bool {
			return report.Circulation[i].InstitutionID < report.Circulation[j].InstitutionID
		})

		// 与本批次相关的撤回：目标是本批次、其祖先或其子孙。
		related := map[string]bool{}
		for id := range family {
			related[id] = true
		}
		for cur := batch; cur.ParentBatchID != ""; {
			parent := st.Batches[cur.ParentBatchID]
			if parent == nil {
				break
			}
			related[parent.ID] = true
			cur = parent
		}
		for _, recall := range st.Recalls {
			if !related[recall.BatchID] {
				continue
			}
			cov, err := recallCoverage(st, recall.ID)
			if err != nil {
				return err
			}
			report.Recalls = append(report.Recalls, *cov)
		}
		sort.Slice(report.Recalls, func(i, j int) bool { return report.Recalls[i].RecallID < report.Recalls[j].RecallID })

		versionSet := map[string]bool{
			batch.CompositionVersionID: true,
			batch.ProcessVersionID:     true,
			batch.QCVersionID:          true,
		}
		for _, sus := range st.Suspensions {
			if (sus.ObjectKind == "version" && versionSet[sus.ObjectID]) ||
				(sus.ObjectKind == "batch" && family[sus.ObjectID]) {
				report.Suspensions = append(report.Suspensions, *sus)
			}
		}
		sort.Slice(report.Suspensions, func(i, j int) bool { return report.Suspensions[i].ID < report.Suspensions[j].ID })

		out = report
		return nil
	})
	return out, err
}

// versionAudit 沿基线链回溯到首版。
func versionAudit(st *store.State, versionID string) (*VersionAudit, error) {
	v, ok := st.Versions[versionID]
	if !ok {
		return nil, domain.NotFoundf("版本 %s 不存在", versionID)
	}
	chain := []string{v.ID}
	for cur := v; cur.BaseVersionID != ""; {
		base, ok := st.Versions[cur.BaseVersionID]
		if !ok {
			break
		}
		chain = append([]string{base.ID}, chain...)
		cur = base
	}
	return &VersionAudit{Version: v, Chain: chain}, nil
}

// ListEvents 返回某主体的审计事件链，来源序号在主体内严格递增。
func (s *Service) ListEvents(subjectRef string) ([]domain.Event, error) {
	var out []domain.Event
	err := s.st.View(func(st *store.State) error {
		for _, ev := range st.Events {
			if ev.SubjectRef == subjectRef {
				out = append(out, ev)
			}
		}
		return nil
	})
	return out, err
}
