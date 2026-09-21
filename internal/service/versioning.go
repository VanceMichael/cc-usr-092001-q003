package service

import (
	"sort"

	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/store"
)

// CreateVersion 在版本线上新建草稿版本。新版本必须引用当前最新版本
// 作为基线（首版除外），且同一版本线任一时刻至多一个草稿，
// 保证多条版本线各自线性、不跳跃。
func (s *Service) CreateVersion(a Actor, lineID string, payload map[string]any, effectiveFrom string) (*domain.DocVersion, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, domain.Invalidf("payload 不能为空")
	}
	if effectiveFrom != "" {
		if err := domain.CheckTime(effectiveFrom, "effective_from"); err != nil {
			return nil, err
		}
	}
	var out *domain.DocVersion
	err := s.st.Update(func(st *store.State) error {
		line, ok := st.Lines[lineID]
		if !ok {
			return domain.NotFoundf("版本线 %s 不存在", lineID)
		}
		if line.Kind == domain.LineQC && effectiveFrom == "" {
			return domain.Invalidf("质控版本必须给出标准生效日 effective_from")
		}
		var baseID string
		var seq int
		for _, vid := range line.VersionIDs {
			v := st.Versions[vid]
			if v.Status == domain.VersionDraft {
				return domain.Conflictf("版本线 %s 已存在草稿 %s；请先晋级或废弃", lineID, v.ID)
			}
			baseID = v.ID
			seq = v.Seq
		}
		v := &domain.DocVersion{
			ID:            store.NextID(st, "VER"),
			LineID:        lineID,
			Seq:           seq + 1,
			BaseVersionID: baseID,
			Payload:       payload,
			EffectiveFrom: effectiveFrom,
			Status:        domain.VersionDraft,
			CreatedBy:     a.ID,
			CreatedAt:     now(),
		}
		st.Versions[v.ID] = v
		line.VersionIDs = append(line.VersionIDs, v.ID)
		store.AppendEvent(st, v.ID, "version.created", v.CreatedAt, "")
		out = v
		return nil
	})
	return out, err
}

// SignOff 记录一个部门对草稿版本的独立签署。每个晋级部门至多签署一次，
// 重复签署或越权部门都会被拒绝；签署与校验处于同一临界区。
func (s *Service) SignOff(a Actor, versionID, comment string) (*domain.DocVersion, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if err := domain.CheckDepartment(a.Dept); err != nil {
		return nil, err
	}
	if !domain.IsPromotionDepartment(a.Dept) {
		return nil, domain.Invalidf("部门 %s 不参与版本晋级签署", a.Dept)
	}
	var out *domain.DocVersion
	err := s.st.Update(func(st *store.State) error {
		v, ok := st.Versions[versionID]
		if !ok {
			return domain.NotFoundf("版本 %s 不存在", versionID)
		}
		if v.Status != domain.VersionDraft {
			return domain.Conflictf("版本 %s 当前状态为 %s，不能签署", versionID, v.Status)
		}
		for _, so := range v.SignOffs {
			if so.Department == a.Dept {
				return domain.Conflictf("部门 %s 已签署版本 %s，不能重复签署", a.Dept, versionID)
			}
		}
		v.SignOffs = append(v.SignOffs, domain.SignOff{
			Department: a.Dept,
			ActorID:    a.ID,
			SignedAt:   now(),
			Comment:    comment,
		})
		store.AppendEvent(st, versionID, "version.signed:"+string(a.Dept), now(), "")
		out = v
		return nil
	})
	return out, err
}

// Promote 晋级草稿版本。必须满足：
//  1. 版本处于草稿状态；
//  2. 基线引用未丢失（首版为空，其余必须指向线内上一版）；
//  3. 临床、药学、生产三个部门均已独立签署。
//
// 校验与状态翻转在同一临界区完成，并发签署不会越过缺失环节。
func (s *Service) Promote(a Actor, versionID string) (*domain.DocVersion, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	var out *domain.DocVersion
	err := s.st.Update(func(st *store.State) error {
		v, ok := st.Versions[versionID]
		if !ok {
			return domain.NotFoundf("版本 %s 不存在", versionID)
		}
		if v.Status != domain.VersionDraft {
			return domain.Conflictf("版本 %s 当前状态为 %s，不能晋级", versionID, v.Status)
		}
		line := st.Lines[v.LineID]
		if len(line.VersionIDs) == 0 || line.VersionIDs[len(line.VersionIDs)-1] != v.ID {
			return domain.Conflictf("版本 %s 不是版本线最新草稿，不能晋级", versionID)
		}
		if v.Seq > 1 {
			base, ok := st.Versions[v.BaseVersionID]
			if !ok {
				return domain.Conflictf("版本 %s 缺少对上一版的引用", versionID)
			}
			if base.LineID != v.LineID || base.Seq != v.Seq-1 {
				return domain.Conflictf("版本 %s 的基线 %s 不是线内上一版", versionID, v.BaseVersionID)
			}
		}
		signed := map[domain.Department]bool{}
		for _, so := range v.SignOffs {
			signed[so.Department] = true
		}
		var missing []string
		for _, dept := range domain.PromotionDepartments {
			if !signed[dept] {
				missing = append(missing, string(dept))
			}
		}
		if len(missing) > 0 {
			return domain.Conflictf("版本 %s 缺少部门签署: %v", versionID, missing)
		}
		v.Status = domain.VersionApproved
		v.PromotedBy = a.ID
		v.PromotedAt = now()
		store.AppendEvent(st, versionID, "version.promoted", v.PromotedAt, "")
		out = v
		return nil
	})
	return out, err
}

// SuspendVersion 紧急停用已晋级版本。停用后新版本不得再引用它，
// 但历史批次保留原引用，不被追溯改写。
func (s *Service) SuspendVersion(a Actor, versionID, reason string) (*domain.DocVersion, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if err := domain.CheckDepartment(a.Dept); err != nil {
		return nil, err
	}
	if err := domain.RequireNonEmpty(reason, "reason"); err != nil {
		return nil, err
	}
	var out *domain.DocVersion
	err := s.st.Update(func(st *store.State) error {
		v, ok := st.Versions[versionID]
		if !ok {
			return domain.NotFoundf("版本 %s 不存在", versionID)
		}
		if v.Status != domain.VersionApproved {
			return domain.Conflictf("版本 %s 当前状态为 %s，不能停用", versionID, v.Status)
		}
		v.Status = domain.VersionRejected
		v.SuspendedBy = a.ID
		v.SuspendedAt = now()
		v.SuspendReason = reason
		susID := store.NextID(st, "SUS")
		st.Suspensions[susID] = &domain.Suspension{
			ID:         susID,
			ObjectKind: "version",
			ObjectID:   versionID,
			Reason:     reason,
			CreatedBy:  a.ID,
			CreatedAt:  v.SuspendedAt,
		}
		store.AppendEvent(st, versionID, "version.suspended", v.SuspendedAt, "")
		out = v
		return nil
	})
	return out, err
}

// selectVersion 按生效日选择批次可引用的版本：
// 已晋级、未停用、生效日不晚于 asOf；生效日相同取序号较大者。
func selectVersion(st *store.State, lineID, asOf string) (*domain.DocVersion, error) {
	line, ok := st.Lines[lineID]
	if !ok {
		return nil, domain.NotFoundf("版本线 %s 不存在", lineID)
	}
	asOfTime, err := parseTime(asOf)
	if err != nil {
		return nil, domain.Invalidf("时间 %q 无法解析", asOf)
	}
	var candidates []*domain.DocVersion
	for _, vid := range line.VersionIDs {
		v := st.Versions[vid]
		if v.Status != domain.VersionApproved {
			continue
		}
		if v.EffectiveFrom != "" {
			eff, err := parseTime(v.EffectiveFrom)
			if err != nil || eff.After(asOfTime) {
				continue
			}
		}
		candidates = append(candidates, v)
	}
	if len(candidates) == 0 {
		return nil, domain.Conflictf("版本线 %s 在 %s 没有可用的已晋级版本", lineID, asOf)
	}
	sort.Slice(candidates, func(i, j int) bool {
		ei, _ := parseTime(candidates[i].EffectiveFrom)
		ej, _ := parseTime(candidates[j].EffectiveFrom)
		if !ei.Equal(ej) {
			return ei.After(ej)
		}
		return candidates[i].Seq > candidates[j].Seq
	})
	return candidates[0], nil
}

// GetVersion 读取版本。
func (s *Service) GetVersion(id string) (*domain.DocVersion, error) {
	var out *domain.DocVersion
	err := s.st.View(func(st *store.State) error {
		v, ok := st.Versions[id]
		if !ok {
			return domain.NotFoundf("版本 %s 不存在", id)
		}
		out = v
		return nil
	})
	return out, err
}
