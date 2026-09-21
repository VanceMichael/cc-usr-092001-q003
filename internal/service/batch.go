package service

import (
	"fmt"

	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/store"
)

// ProduceBatch 按产出时刻可用的版本生产批次。批次一经产出即锁定
// 组方、工艺、质控三个版本，之后的版本晋级或停用不会追溯改写它。
func (s *Service) ProduceBatch(a Actor, preparationID string, quantity int, producedAt string) (*domain.Batch, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.InstitutionID == "" {
		return nil, domain.Invalidf("缺少生产机构（X-Institution-Id）")
	}
	if quantity <= 0 {
		return nil, domain.Invalidf("quantity 必须为正整数")
	}
	if err := domain.CheckTime(producedAt, "produced_at"); err != nil {
		return nil, err
	}
	var out *domain.Batch
	err := s.st.Update(func(st *store.State) error {
		prep, ok := st.Preparations[preparationID]
		if !ok {
			return domain.NotFoundf("制剂 %s 不存在", preparationID)
		}
		comp, err := selectVersion(st, prep.CompositionLineID, producedAt)
		if err != nil {
			return err
		}
		proc, err := selectVersion(st, prep.ProcessLineID, producedAt)
		if err != nil {
			return err
		}
		qc, err := selectVersion(st, prep.QCLineID, producedAt)
		if err != nil {
			return err
		}
		batch := &domain.Batch{
			ID:                   store.NextID(st, "BAT"),
			PreparationID:        preparationID,
			CompositionVersionID: comp.ID,
			ProcessVersionID:     proc.ID,
			QCVersionID:          qc.ID,
			ProducedAt:           producedAt,
			ProducedBy:           a.InstitutionID,
			Quantity:             quantity,
			Remaining:            quantity,
			Status:               domain.BatchActive,
		}
		batch.BatchNo = fmt.Sprintf("BN%s-%s", producedAt[:4], batch.ID[len("BAT-"):])
		st.Batches[batch.ID] = batch
		st.BatchByNo[batch.BatchNo] = batch.ID
		store.AppendEvent(st, batch.ID, "batch.produced", producedAt, "")
		out = batch
		return nil
	})
	return out, err
}

// SplitBatch 把父批次拆分为若干子批次。子批次继承父批次锁定的版本；
// 拆分数量之和不得超过父批次剩余量，数量守恒。
func (s *Service) SplitBatch(a Actor, batchID string, quantities []int) ([]*domain.Batch, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if len(quantities) == 0 {
		return nil, domain.Invalidf("拆分数量列表不能为空")
	}
	var out []*domain.Batch
	err := s.st.Update(func(st *store.State) error {
		parent, ok := st.Batches[batchID]
		if !ok {
			return domain.NotFoundf("批次 %s 不存在", batchID)
		}
		if parent.Status != domain.BatchActive {
			return domain.Conflictf("批次 %s 当前状态为 %s，不能拆分", batchID, parent.Status)
		}
		total := 0
		for _, q := range quantities {
			if q <= 0 {
				return domain.Invalidf("拆分数量必须为正整数")
			}
			total += q
		}
		if total > parent.Remaining {
			return domain.Conflictf("拆分总量 %d 超过批次 %s 剩余量 %d", total, batchID, parent.Remaining)
		}
		children := 0
		for _, b := range st.Batches {
			if b.ParentBatchID == batchID {
				children++
			}
		}
		parent.Remaining -= total
		for _, q := range quantities {
			children++
			child := &domain.Batch{
				ID:                   store.NextID(st, "BAT"),
				PreparationID:        parent.PreparationID,
				CompositionVersionID: parent.CompositionVersionID,
				ProcessVersionID:     parent.ProcessVersionID,
				QCVersionID:          parent.QCVersionID,
				ProducedAt:           parent.ProducedAt,
				ProducedBy:           parent.ProducedBy,
				Quantity:             q,
				Remaining:            q,
				ParentBatchID:        parent.ID,
				Status:               domain.BatchActive,
			}
			child.BatchNo = fmt.Sprintf("%s-S%d", parent.BatchNo, children)
			st.Batches[child.ID] = child
			st.BatchByNo[child.BatchNo] = child.ID
			store.AppendEvent(st, child.ID, "batch.split", now(), "")
			out = append(out, child)
		}
		return nil
	})
	return out, err
}

// Distribute 把批次数量分发到适用机构，并生成分发授权记录。
func (s *Service) Distribute(a Actor, batchID, toInstitution string, quantity int) (*domain.Distribution, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if quantity <= 0 {
		return nil, domain.Invalidf("quantity 必须为正整数")
	}
	var out *domain.Distribution
	err := s.st.Update(func(st *store.State) error {
		batch, ok := st.Batches[batchID]
		if !ok {
			return domain.NotFoundf("批次 %s 不存在", batchID)
		}
		if batch.Status != domain.BatchActive {
			return domain.Conflictf("批次 %s 当前状态为 %s，不能分发", batchID, batch.Status)
		}
		inst, ok := st.Institutions[toInstitution]
		if !ok {
			return domain.NotFoundf("机构 %s 不存在", toInstitution)
		}
		if inst.Status != domain.InstitutionActive {
			return domain.Conflictf("机构 %s 当前状态为 %s，不能接收分发", toInstitution, inst.Status)
		}
		prep := st.Preparations[batch.PreparationID]
		applicable := false
		for _, id := range prep.ApplicableInstitutions {
			if id == toInstitution {
				applicable = true
				break
			}
		}
		if !applicable {
			return domain.Conflictf("机构 %s 不在制剂 %s 的适用范围内", toInstitution, prep.ID)
		}
		if quantity > batch.Remaining {
			return domain.Conflictf("分发量 %d 超过批次 %s 剩余量 %d", quantity, batchID, batch.Remaining)
		}
		batch.Remaining -= quantity
		authID := store.NextID(st, "AUT")
		st.Authorizations[authID] = &domain.Authorization{
			ID:                 authID,
			GranteeInstitution: toInstitution,
			ObjectKind:         "batch",
			ObjectID:           batchID,
			Scope:              domain.ScopeDistribution,
			GrantedBy:          a.ID,
			CreatedAt:          now(),
		}
		dist := &domain.Distribution{
			ID:              store.NextID(st, "DST"),
			BatchID:         batchID,
			ToInstitution:   toInstitution,
			Quantity:        quantity,
			ShippedAt:       now(),
			AuthorizationID: authID,
		}
		st.Distributions[dist.ID] = dist
		store.AppendEvent(st, batchID, "batch.distributed", dist.ShippedAt, "")
		out = dist
		return nil
	})
	return out, err
}

// SuspendBatch 紧急停用批次：停止一切拆分与分发，历史记录保留。
func (s *Service) SuspendBatch(a Actor, batchID, reason string) (*domain.Batch, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if err := domain.CheckDepartment(a.Dept); err != nil {
		return nil, err
	}
	if err := domain.RequireNonEmpty(reason, "reason"); err != nil {
		return nil, err
	}
	var out *domain.Batch
	err := s.st.Update(func(st *store.State) error {
		batch, ok := st.Batches[batchID]
		if !ok {
			return domain.NotFoundf("批次 %s 不存在", batchID)
		}
		if batch.Status != domain.BatchActive {
			return domain.Conflictf("批次 %s 当前状态为 %s，不能停用", batchID, batch.Status)
		}
		batch.Status = domain.BatchSuspended
		batch.SuspendReason = reason
		susID := store.NextID(st, "SUS")
		st.Suspensions[susID] = &domain.Suspension{
			ID:         susID,
			ObjectKind: "batch",
			ObjectID:   batchID,
			Reason:     reason,
			CreatedBy:  a.ID,
			CreatedAt:  now(),
		}
		store.AppendEvent(st, batchID, "batch.suspended", now(), "")
		out = batch
		return nil
	})
	return out, err
}

// GetBatch 按 ID 读取批次。
func (s *Service) GetBatch(id string) (*domain.Batch, error) {
	var out *domain.Batch
	err := s.st.View(func(st *store.State) error {
		b, ok := st.Batches[id]
		if !ok {
			return domain.NotFoundf("批次 %s 不存在", id)
		}
		out = b
		return nil
	})
	return out, err
}
