package service

import (
	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/store"
)

// CreateInstitution 登记医联体成员机构。
func (s *Service) CreateInstitution(a Actor, name, kind string) (*domain.Institution, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if err := domain.RequireNonEmpty(name, "name"); err != nil {
		return nil, err
	}
	if kind != "lead" && kind != "member" {
		return nil, domain.Invalidf("kind 必须是 lead 或 member")
	}
	var out *domain.Institution
	err := s.st.Update(func(st *store.State) error {
		inst := &domain.Institution{
			ID:        store.NextID(st, "INS"),
			Name:      name,
			Kind:      kind,
			Status:    domain.InstitutionActive,
			CreatedAt: now(),
		}
		st.Institutions[inst.ID] = inst
		store.AppendEvent(st, inst.ID, "institution.created", inst.CreatedAt, "")
		out = inst
		return nil
	})
	return out, err
}

// EvidenceInput 是证据接入请求。原始材料不进入系统，
// 只接收授权摘要、受控引用与 sha256 指纹。
type EvidenceInput struct {
	EventID        string
	SubjectRef     string
	Category       string
	Summary        string
	ControlledRef  string
	PayloadDigest  string
	OccurredAt     string
	SourceSequence int64
}

// IngestEvidence 接入匿名方证证据。按 event_id 幂等；
// 来源序号在同一主体内必须严格递增；原始发生时间原样保留。
func (s *Service) IngestEvidence(a Actor, in EvidenceInput) (*domain.Evidence, bool, error) {
	if err := a.validate(); err != nil {
		return nil, false, err
	}
	if a.InstitutionID == "" {
		return nil, false, domain.Invalidf("缺少所属机构（X-Institution-Id）")
	}
	if err := domain.RequireNonEmpty(in.EventID, "event_id"); err != nil {
		return nil, false, err
	}
	if err := domain.RequireNonEmpty(in.SubjectRef, "subject_ref"); err != nil {
		return nil, false, err
	}
	if err := domain.RequireNonEmpty(in.Category, "category"); err != nil {
		return nil, false, err
	}
	if err := domain.RequireNonEmpty(in.Summary, "summary"); err != nil {
		return nil, false, err
	}
	if err := domain.CheckNoPatientIdentifiers(in.Summary, "summary"); err != nil {
		return nil, false, err
	}
	if err := domain.CheckNoPatientIdentifiers(in.SubjectRef, "subject_ref"); err != nil {
		return nil, false, err
	}
	if err := domain.CheckControlledRef(in.ControlledRef); err != nil {
		return nil, false, err
	}
	if err := domain.CheckDigest(in.PayloadDigest); err != nil {
		return nil, false, err
	}
	if err := domain.CheckTime(in.OccurredAt, "occurred_at"); err != nil {
		return nil, false, err
	}
	var out *domain.Evidence
	var duplicated bool
	err := s.st.Update(func(st *store.State) error {
		if id, ok := st.EvidenceEvents[in.EventID]; ok {
			out = st.Evidences[id]
			duplicated = true
			return nil
		}
		if _, ok := st.Institutions[a.InstitutionID]; !ok {
			return domain.NotFoundf("机构 %s 不存在", a.InstitutionID)
		}
		next := st.SubjectSeq["evidence:"+in.SubjectRef] + 1
		if in.SourceSequence != next {
			return domain.Conflictf("subject %s 的来源序号应为 %d，收到 %d；序号只在同一来源内递增",
				in.SubjectRef, next, in.SourceSequence)
		}
		ev := &domain.Evidence{
			ID:                store.NextID(st, "EV"),
			EventID:           in.EventID,
			SubjectRef:        in.SubjectRef,
			Category:          in.Category,
			OwningInstitution: a.InstitutionID,
			Summary:           in.Summary,
			ControlledRef:     in.ControlledRef,
			PayloadDigest:     in.PayloadDigest,
			OccurredAt:        in.OccurredAt,
			SourceSequence:    in.SourceSequence,
			RecordedAt:        now(),
		}
		st.Evidences[ev.ID] = ev
		st.EvidenceEvents[in.EventID] = ev.ID
		st.SubjectSeq["evidence:"+in.SubjectRef] = next
		store.AppendEvent(st, ev.ID, "evidence.ingested", in.OccurredAt, in.PayloadDigest)
		out = ev
		return nil
	})
	return out, duplicated, err
}

// GetEvidenceView 返回证据的对外视图：仅授权摘要与可验证指纹。
func (s *Service) GetEvidenceView(id string) (*domain.EvidenceView, error) {
	var out *domain.EvidenceView
	err := s.st.View(func(st *store.State) error {
		ev, ok := st.Evidences[id]
		if !ok {
			return domain.NotFoundf("证据 %s 不存在", id)
		}
		v := ev.View()
		out = &v
		return nil
	})
	return out, err
}

// CreateSource 登记处方来源（流派传承）。
func (s *Service) CreateSource(a Actor, lineage, originRef, note string, evidenceIDs []string) (*domain.PrescriptionSource, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.InstitutionID == "" {
		return nil, domain.Invalidf("缺少所属机构（X-Institution-Id）")
	}
	if err := domain.RequireNonEmpty(lineage, "lineage"); err != nil {
		return nil, err
	}
	if err := domain.RequireNonEmpty(originRef, "origin_ref"); err != nil {
		return nil, err
	}
	var out *domain.PrescriptionSource
	err := s.st.Update(func(st *store.State) error {
		for _, id := range evidenceIDs {
			if _, ok := st.Evidences[id]; !ok {
				return domain.NotFoundf("证据 %s 不存在", id)
			}
		}
		src := &domain.PrescriptionSource{
			ID:            store.NextID(st, "SRC"),
			Lineage:       lineage,
			OriginRef:     originRef,
			InstitutionID: a.InstitutionID,
			EvidenceIDs:   append([]string(nil), evidenceIDs...),
			Note:          note,
			CreatedAt:     now(),
		}
		st.Sources[src.ID] = src
		store.AppendEvent(st, src.ID, "source.created", src.CreatedAt, "")
		out = src
		return nil
	})
	return out, err
}

// CreatePreparation 登记院内制剂，并建立组方、工艺、质控三条版本线。
func (s *Service) CreatePreparation(a Actor, name, sourceID string) (*domain.Preparation, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if err := domain.RequireNonEmpty(name, "name"); err != nil {
		return nil, err
	}
	var out *domain.Preparation
	err := s.st.Update(func(st *store.State) error {
		if _, ok := st.Sources[sourceID]; !ok {
			return domain.NotFoundf("处方来源 %s 不存在", sourceID)
		}
		prep := &domain.Preparation{
			ID:        store.NextID(st, "PRP"),
			Name:      name,
			SourceID:  sourceID,
			CreatedAt: now(),
		}
		for _, kind := range domain.LineKinds {
			line := &domain.VersionLine{
				ID:            store.NextID(st, "LIN"),
				Kind:          kind,
				PreparationID: prep.ID,
				Title:         name + "/" + kind,
			}
			st.Lines[line.ID] = line
			switch kind {
			case domain.LineComposition:
				prep.CompositionLineID = line.ID
			case domain.LineProcess:
				prep.ProcessLineID = line.ID
			case domain.LineQC:
				prep.QCLineID = line.ID
			}
		}
		st.Preparations[prep.ID] = prep
		store.AppendEvent(st, prep.ID, "preparation.created", prep.CreatedAt, "")
		out = prep
		return nil
	})
	return out, err
}

// SetApplicability 授予或撤销制剂的适用机构。
func (s *Service) SetApplicability(a Actor, preparationID, institutionID string, grant bool) (*domain.Preparation, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	var out *domain.Preparation
	err := s.st.Update(func(st *store.State) error {
		prep, ok := st.Preparations[preparationID]
		if !ok {
			return domain.NotFoundf("制剂 %s 不存在", preparationID)
		}
		if _, ok := st.Institutions[institutionID]; !ok {
			return domain.NotFoundf("机构 %s 不存在", institutionID)
		}
		idx := -1
		for i, id := range prep.ApplicableInstitutions {
			if id == institutionID {
				idx = i
				break
			}
		}
		kind := "applicability.revoked"
		if grant {
			if idx >= 0 {
				return domain.Conflictf("机构 %s 已在制剂 %s 的适用范围内", institutionID, preparationID)
			}
			prep.ApplicableInstitutions = append(prep.ApplicableInstitutions, institutionID)
			kind = "applicability.granted"
		} else {
			if idx < 0 {
				return domain.Conflictf("机构 %s 不在制剂 %s 的适用范围内", institutionID, preparationID)
			}
			prep.ApplicableInstitutions = append(prep.ApplicableInstitutions[:idx], prep.ApplicableInstitutions[idx+1:]...)
		}
		store.AppendEvent(st, preparationID, kind, now(), "")
		out = prep
		return nil
	})
	return out, err
}

// GetPreparation 读取制剂。
func (s *Service) GetPreparation(id string) (*domain.Preparation, error) {
	var out *domain.Preparation
	err := s.st.View(func(st *store.State) error {
		prep, ok := st.Preparations[id]
		if !ok {
			return domain.NotFoundf("制剂 %s 不存在", id)
		}
		out = prep
		return nil
	})
	return out, err
}
