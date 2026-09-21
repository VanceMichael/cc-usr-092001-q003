package service

import (
	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/store"
)

// CreateResearchRequest 提交科研用途申请。申请引用的证据必须同属一个
// 所属机构，由该机构审批；审批通过后申请方仅获得授权摘要与指纹。
func (s *Service) CreateResearchRequest(a Actor, purpose string, evidenceIDs []string) (*domain.ResearchRequest, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.InstitutionID == "" {
		return nil, domain.Invalidf("缺少申请机构（X-Institution-Id）")
	}
	if err := domain.RequireNonEmpty(purpose, "purpose"); err != nil {
		return nil, err
	}
	if len(evidenceIDs) == 0 {
		return nil, domain.Invalidf("evidence_ids 不能为空")
	}
	var out *domain.ResearchRequest
	err := s.st.Update(func(st *store.State) error {
		if _, ok := st.Institutions[a.InstitutionID]; !ok {
			return domain.NotFoundf("机构 %s 不存在", a.InstitutionID)
		}
		owner := ""
		for _, id := range evidenceIDs {
			ev, ok := st.Evidences[id]
			if !ok {
				return domain.NotFoundf("证据 %s 不存在", id)
			}
			if owner == "" {
				owner = ev.OwningInstitution
			} else if ev.OwningInstitution != owner {
				return domain.Invalidf("证据 %s 属于机构 %s，与 %s 不同；请按所属机构分别申请", id, ev.OwningInstitution, owner)
			}
		}
		req := &domain.ResearchRequest{
			ID:                   store.NextID(st, "RES"),
			ApplicantInstitution: a.InstitutionID,
			Purpose:              purpose,
			EvidenceIDs:          append([]string(nil), evidenceIDs...),
			Status:               domain.ResearchPending,
			CreatedAt:            now(),
		}
		st.Research[req.ID] = req
		store.AppendEvent(st, req.ID, "research.requested", req.CreatedAt, "")
		out = req
		return nil
	})
	return out, err
}

// ReviewResearchRequest 审批科研申请。审批人必须来自证据所属机构；
// 批准后为每条证据生成科研授权，申请方只能调阅摘要与指纹。
func (s *Service) ReviewResearchRequest(a Actor, requestID string, approve bool) (*domain.ResearchRequest, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.InstitutionID == "" {
		return nil, domain.Invalidf("缺少审批机构（X-Institution-Id）")
	}
	var out *domain.ResearchRequest
	err := s.st.Update(func(st *store.State) error {
		req, ok := st.Research[requestID]
		if !ok {
			return domain.NotFoundf("科研申请 %s 不存在", requestID)
		}
		if req.Status != domain.ResearchPending {
			return domain.Conflictf("科研申请 %s 当前状态为 %s，不能审批", requestID, req.Status)
		}
		for _, id := range req.EvidenceIDs {
			ev := st.Evidences[id]
			if ev.OwningInstitution != a.InstitutionID {
				return domain.Conflictf("证据 %s 属于机构 %s，审批人机构 %s 无权审批", id, ev.OwningInstitution, a.InstitutionID)
			}
		}
		req.ReviewedBy = a.ID
		req.ReviewedAt = now()
		if approve {
			req.Status = domain.ResearchApproved
			for _, id := range req.EvidenceIDs {
				authID := store.NextID(st, "AUT")
				st.Authorizations[authID] = &domain.Authorization{
					ID:                 authID,
					GranteeInstitution: req.ApplicantInstitution,
					ObjectKind:         "evidence",
					ObjectID:           id,
					Scope:              domain.ScopeResearch,
					GrantedBy:          a.ID,
					CreatedAt:          req.ReviewedAt,
				}
			}
		} else {
			req.Status = domain.ResearchRejected
		}
		store.AppendEvent(st, requestID, "research.reviewed", req.ReviewedAt, "")
		out = req
		return nil
	})
	return out, err
}

// GetResearchRequest 读取科研申请。
func (s *Service) GetResearchRequest(id string) (*domain.ResearchRequest, error) {
	var out *domain.ResearchRequest
	err := s.st.View(func(st *store.State) error {
		req, ok := st.Research[id]
		if !ok {
			return domain.NotFoundf("科研申请 %s 不存在", id)
		}
		out = req
		return nil
	})
	return out, err
}
