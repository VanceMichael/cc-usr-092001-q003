package service

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/store"
)

// ---- 测试基件 ----

type fixture struct {
	svc                    *Service
	lead, memberA, memberB *domain.Institution
	evidence               *domain.Evidence
	source                 *domain.PrescriptionSource
	prep                   *domain.Preparation
	compV, procV, qcV      *domain.DocVersion
}

func admin() Actor { return Actor{ID: "admin-1", Dept: domain.DeptQuality} }

func actorAt(dept domain.Department, instID, id string) Actor {
	return Actor{ID: id, Dept: dept, InstitutionID: instID}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{svc: New(st)}
	f.lead = f.institution(t, "龙华医院", "lead")
	f.memberA = f.institution(t, "社区中心A", "member")
	f.memberB = f.institution(t, "社区中心B", "member")

	ev, _, err := f.svc.IngestEvidence(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"), EvidenceInput{
		EventID:        "EVT-SEED-1",
		SubjectRef:     "SUBJ-ANON-1",
		Category:       "lineage",
		Summary:        "某流派治疗湿阻证的用药经验摘要",
		ControlledRef:  "his://longhua/archive/0001",
		PayloadDigest:  domain.DigestOf([]byte("raw-material-kept-at-source")),
		OccurredAt:     "2025-12-01T09:00:00+08:00",
		SourceSequence: 1,
	})
	if err != nil {
		t.Fatalf("接入证据失败: %v", err)
	}
	f.evidence = ev

	f.source, err = f.svc.CreateSource(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"),
		"顾氏外科流派", "ORIGIN-ANON-7", "名老中医经验方", []string{ev.ID})
	if err != nil {
		t.Fatalf("登记来源失败: %v", err)
	}
	f.prep, err = f.svc.CreatePreparation(admin(), "祛湿合剂", f.source.ID)
	if err != nil {
		t.Fatalf("登记制剂失败: %v", err)
	}
	f.compV = f.approveVersion(t, f.prep.CompositionLineID,
		map[string]any{"items": []any{map[string]any{"herb": "茯苓", "dose": "15g"}}}, "")
	f.procV = f.approveVersion(t, f.prep.ProcessLineID,
		map[string]any{"steps": []any{"煎煮", "浓缩", "灌装"}}, "")
	f.qcV = f.approveVersion(t, f.prep.QCLineID,
		map[string]any{"limits": map[string]any{"pH": "5.0-7.0"}}, "2026-01-01T00:00:00+08:00")
	return f
}

func (f *fixture) institution(t *testing.T, name, kind string) *domain.Institution {
	t.Helper()
	inst, err := f.svc.CreateInstitution(admin(), name, kind)
	if err != nil {
		t.Fatalf("登记机构失败: %v", err)
	}
	return inst
}

func (f *fixture) approveVersion(t *testing.T, lineID string, payload map[string]any, eff string) *domain.DocVersion {
	t.Helper()
	v, err := f.svc.CreateVersion(actorAt(domain.DeptPharmacy, f.lead.ID, "phar-1"), lineID, payload, eff)
	if err != nil {
		t.Fatalf("创建版本失败: %v", err)
	}
	for _, dept := range domain.PromotionDepartments {
		if _, err := f.svc.SignOff(actorAt(dept, f.lead.ID, string(dept)+"-1"), v.ID, ""); err != nil {
			t.Fatalf("签署失败(%s): %v", dept, err)
		}
	}
	v, err = f.svc.Promote(actorAt(domain.DeptPharmacy, f.lead.ID, "phar-1"), v.ID)
	if err != nil {
		t.Fatalf("晋级失败: %v", err)
	}
	return v
}

func (f *fixture) produce(t *testing.T, quantity int, producedAt string) *domain.Batch {
	t.Helper()
	b, err := f.svc.ProduceBatch(actorAt(domain.DeptProduction, f.lead.ID, "prod-1"),
		f.prep.ID, quantity, producedAt)
	if err != nil {
		t.Fatalf("生产批次失败: %v", err)
	}
	return b
}

// ---- 版本晋级 ----

func TestPromoteRequiresAllIndependentSignOffs(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateVersion(actorAt(domain.DeptPharmacy, f.lead.ID, "phar-1"),
		f.prep.QCLineID, map[string]any{"limits": map[string]any{"pH": "4.5-7.0"}}, "2026-06-01T00:00:00+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if v.BaseVersionID != f.qcV.ID {
		t.Fatalf("新版本必须引用上一版作为基线，实际基线 %q", v.BaseVersionID)
	}
	if _, err := f.svc.Promote(admin(), v.ID); !domain.IsConflict(err) {
		t.Fatalf("零签署时晋级应失败，实际 %v", err)
	}
	if _, err := f.svc.SignOff(actorAt(domain.DeptClinical, f.lead.ID, "cli-1"), v.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Promote(admin(), v.ID); !domain.IsConflict(err) {
		t.Fatalf("仅临床签署时晋级应失败，实际 %v", err)
	}
	// 同一部门重复签署被拒绝。
	if _, err := f.svc.SignOff(actorAt(domain.DeptClinical, f.lead.ID, "cli-2"), v.ID, ""); !domain.IsConflict(err) {
		t.Fatalf("同部门重复签署应失败，实际 %v", err)
	}
	// 质量部门不参与晋级签署。
	if _, err := f.svc.SignOff(actorAt(domain.DeptQuality, f.lead.ID, "qa-1"), v.ID, ""); !domain.IsValidation(err) {
		t.Fatalf("非晋级部门签署应失败，实际 %v", err)
	}
	if _, err := f.svc.SignOff(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), v.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Promote(admin(), v.ID); !domain.IsConflict(err) {
		t.Fatalf("缺少生产部门签署时晋级应失败，实际 %v", err)
	}
	if _, err := f.svc.SignOff(actorAt(domain.DeptProduction, f.lead.ID, "pro-1"), v.ID, ""); err != nil {
		t.Fatal(err)
	}
	promoted, err := f.svc.Promote(admin(), v.ID)
	if err != nil {
		t.Fatalf("三部门签署齐全后晋级应成功: %v", err)
	}
	if promoted.Status != domain.VersionApproved {
		t.Fatalf("晋级后状态应为 approved，实际 %s", promoted.Status)
	}
	if _, err := f.svc.Promote(admin(), v.ID); !domain.IsConflict(err) {
		t.Fatalf("重复晋级应失败，实际 %v", err)
	}
	if _, err := f.svc.SignOff(actorAt(domain.DeptClinical, f.lead.ID, "cli-9"), v.ID, ""); !domain.IsConflict(err) {
		t.Fatalf("已晋级版本不应再接受签署，实际 %v", err)
	}
}

func TestConcurrentSignOffsNeverSkipMissingSteps(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateVersion(actorAt(domain.DeptPharmacy, f.lead.ID, "phar-1"),
		f.prep.ProcessLineID, map[string]any{"steps": []any{"煎煮", "浓缩", "灭菌", "灌装"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	// 每个部门两人同时签署：每部门只能成功一次。
	for _, dept := range domain.PromotionDepartments {
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(dept domain.Department, n int) {
				defer wg.Done()
				_, err := f.svc.SignOff(actorAt(dept, f.lead.ID, string(dept)+"-"+string(rune('a'+n))), v.ID, "")
				errs <- err
			}(dept, i)
		}
	}
	// 并发晋级尝试：在签署未齐前不得成功。
	promoteOK := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.svc.Promote(admin(), v.ID)
			promoteOK <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(promoteOK)

	signOK := 0
	for err := range errs {
		if err == nil {
			signOK++
		} else if !domain.IsConflict(err) {
			t.Fatalf("签署出现意外错误: %v", err)
		}
	}
	if signOK != 3 {
		t.Fatalf("三个部门应各签署成功一次，实际成功 %d 次", signOK)
	}
	promoted := 0
	for err := range promoteOK {
		if err == nil {
			promoted++
		} else if !domain.IsConflict(err) {
			t.Fatalf("晋级出现意外错误: %v", err)
		}
	}
	v, err = f.svc.GetVersion(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.SignOffs) != 3 {
		t.Fatalf("最终应有且仅有 3 个部门签署，实际 %d", len(v.SignOffs))
	}
	// 并发下要么恰有一次晋级成功（在签署齐全后），要么全部因缺签署失败；
	// 绝不允许出现“签署缺失却已晋级”的中间态。
	if promoted > 1 {
		t.Fatalf("至多一次晋级成功，实际 %d 次", promoted)
	}
	if v.Status == domain.VersionApproved && promoted != 1 {
		t.Fatal("状态已晋级但没有任何一次晋级调用成功，状态机不一致")
	}
	if v.Status == domain.VersionDraft {
		if _, err := f.svc.Promote(admin(), v.ID); err != nil {
			t.Fatalf("签署齐全后补晋级应成功: %v", err)
		}
	}
}

func TestVersionLineStaysLinear(t *testing.T) {
	f := newFixture(t)
	v2, err := f.svc.CreateVersion(actorAt(domain.DeptPharmacy, f.lead.ID, "phar-1"),
		f.prep.CompositionLineID, map[string]any{"items": []any{map[string]any{"herb": "茯苓", "dose": "20g"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateVersion(actorAt(domain.DeptPharmacy, f.lead.ID, "phar-2"),
		f.prep.CompositionLineID, map[string]any{"items": []any{}}, ""); !domain.IsConflict(err) {
		t.Fatalf("存在草稿时不应再开新版本，实际 %v", err)
	}
	for _, dept := range domain.PromotionDepartments {
		if _, err := f.svc.SignOff(actorAt(dept, f.lead.ID, string(dept)+"-1"), v2.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Promote(admin(), v2.ID); err != nil {
		t.Fatal(err)
	}
	v3, err := f.svc.CreateVersion(actorAt(domain.DeptPharmacy, f.lead.ID, "phar-1"),
		f.prep.CompositionLineID, map[string]any{"items": []any{map[string]any{"herb": "茯苓"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if v3.BaseVersionID != v2.ID || v3.Seq != 3 {
		t.Fatalf("v3 应以 v2 为基线且序号为 3，实际基线 %s 序号 %d", v3.BaseVersionID, v3.Seq)
	}
}

// ---- 标准生效日与不可追溯性 ----

func TestQCEffectiveDateSelectionAndNoRetroactiveRewrite(t *testing.T) {
	f := newFixture(t)
	b1 := f.produce(t, 100, "2026-03-01T10:00:00+08:00")
	if b1.QCVersionID != f.qcV.ID {
		t.Fatalf("3 月生产应引用质控首版，实际 %s", b1.QCVersionID)
	}
	// 新质控标准 7 月生效。
	qcV2 := f.approveVersion(t, f.prep.QCLineID,
		map[string]any{"limits": map[string]any{"pH": "5.5-6.5"}}, "2026-07-01T00:00:00+08:00")
	b2 := f.produce(t, 50, "2026-08-01T10:00:00+08:00")
	if b2.QCVersionID != qcV2.ID {
		t.Fatalf("8 月生产应引用新质控版，实际 %s", b2.QCVersionID)
	}
	// 新标准生效前的批次仍引用旧版。
	b3 := f.produce(t, 30, "2026-05-01T10:00:00+08:00")
	if b3.QCVersionID != f.qcV.ID {
		t.Fatalf("新标准生效日前生产的批次应引用旧质控版，实际 %s", b3.QCVersionID)
	}
	// 旧批次不被新规则追溯改写。
	report, err := f.svc.AuditBatch(b1.BatchNo)
	if err != nil {
		t.Fatal(err)
	}
	if report.QC.Version.ID != f.qcV.ID {
		t.Fatalf("旧批次的审计视图应仍指向质控首版，实际 %s", report.QC.Version.ID)
	}
	if len(report.QC.Chain) != 1 || report.QC.Chain[0] != f.qcV.ID {
		t.Fatalf("旧批次质控链应为 [%s]，实际 %v", f.qcV.ID, report.QC.Chain)
	}
}

func TestSuspendedVersionNotSelectableButHistoryKept(t *testing.T) {
	f := newFixture(t)
	qcV2 := f.approveVersion(t, f.prep.QCLineID,
		map[string]any{"limits": map[string]any{"pH": "5.5-6.5"}}, "2026-07-01T00:00:00+08:00")
	b1 := f.produce(t, 10, "2026-08-01T10:00:00+08:00")
	if b1.QCVersionID != qcV2.ID {
		t.Fatal("前置条件失败")
	}
	if _, err := f.svc.SuspendVersion(actorAt(domain.DeptQuality, f.lead.ID, "qa-1"), qcV2.ID, "pH 限度抽检异常"); err != nil {
		t.Fatal(err)
	}
	// 停用后新批次回退到仍有效的旧版。
	b2 := f.produce(t, 10, "2026-08-02T10:00:00+08:00")
	if b2.QCVersionID != f.qcV.ID {
		t.Fatalf("停用新版后应回退到旧质控版，实际 %s", b2.QCVersionID)
	}
	// 历史批次引用保持不动。
	report, err := f.svc.AuditBatch(b1.BatchNo)
	if err != nil {
		t.Fatal(err)
	}
	if report.QC.Version.ID != qcV2.ID {
		t.Fatalf("历史批次不应被停用动作改写，实际 %s", report.QC.Version.ID)
	}
	if len(report.Suspensions) != 1 || report.Suspensions[0].ObjectID != qcV2.ID {
		t.Fatalf("审计应列出对 %s 的停用记录，实际 %+v", qcV2.ID, report.Suspensions)
	}
	// 全部质控版本停用后不能再生产。
	if _, err := f.svc.SuspendVersion(actorAt(domain.DeptQuality, f.lead.ID, "qa-1"), f.qcV.ID, "全面复核"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ProduceBatch(actorAt(domain.DeptProduction, f.lead.ID, "prod-1"),
		f.prep.ID, 10, "2026-08-03T10:00:00+08:00"); !domain.IsConflict(err) {
		t.Fatalf("无可用质控版本时生产应失败，实际 %v", err)
	}
}

// ---- 批次拆分与分发 ----

func TestBatchSplitConservesQuantity(t *testing.T) {
	f := newFixture(t)
	b := f.produce(t, 100, "2026-03-01T10:00:00+08:00")
	children, err := f.svc.SplitBatch(actorAt(domain.DeptProduction, f.lead.ID, "prod-1"), b.ID, []int{40, 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("应拆出 2 个子批次，实际 %d", len(children))
	}
	sum := 0
	for _, c := range children {
		sum += c.Quantity
		if c.ParentBatchID != b.ID {
			t.Fatalf("子批次应引用父批次 %s，实际 %s", b.ID, c.ParentBatchID)
		}
		if c.CompositionVersionID != b.CompositionVersionID || c.ProcessVersionID != b.ProcessVersionID || c.QCVersionID != b.QCVersionID {
			t.Fatal("子批次必须继承父批次锁定的版本")
		}
	}
	if sum != 100 {
		t.Fatalf("拆分总量应守恒为 100，实际 %d", sum)
	}
	b, _ = f.svc.GetBatch(b.ID)
	if b.Remaining != 0 {
		t.Fatalf("父批次剩余应为 0，实际 %d", b.Remaining)
	}
	if _, err := f.svc.SplitBatch(actorAt(domain.DeptProduction, f.lead.ID, "prod-1"), b.ID, []int{1}); !domain.IsConflict(err) {
		t.Fatalf("剩余不足时拆分应失败，实际 %v", err)
	}
}

func TestDistributeRequiresApplicabilityAndStock(t *testing.T) {
	f := newFixture(t)
	b := f.produce(t, 50, "2026-03-01T10:00:00+08:00")
	if _, err := f.svc.Distribute(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), b.ID, f.memberA.ID, 10); !domain.IsConflict(err) {
		t.Fatalf("未授权机构接收分发应失败，实际 %v", err)
	}
	if _, err := f.svc.SetApplicability(admin(), f.prep.ID, f.memberA.ID, true); err != nil {
		t.Fatal(err)
	}
	d, err := f.svc.Distribute(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), b.ID, f.memberA.ID, 40)
	if err != nil {
		t.Fatal(err)
	}
	if d.AuthorizationID == "" {
		t.Fatal("分发必须生成授权记录")
	}
	if _, err := f.svc.Distribute(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), b.ID, f.memberA.ID, 11); !domain.IsConflict(err) {
		t.Fatalf("超量分发应失败，实际 %v", err)
	}
	if _, err := f.svc.Distribute(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), b.ID, f.memberA.ID, 10); err != nil {
		t.Fatal(err)
	}
	// 停用批次不得再分发。
	b2 := f.produce(t, 5, "2026-03-02T10:00:00+08:00")
	if _, err := f.svc.SuspendBatch(actorAt(domain.DeptQuality, f.lead.ID, "qa-1"), b2.ID, "冷链温度超标"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Distribute(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), b2.ID, f.memberA.ID, 1); !domain.IsConflict(err) {
		t.Fatalf("已停用批次分发应失败，实际 %v", err)
	}
}

// ---- 跨院撤回 ----

func TestRecallCoversAllReceivingInstitutions(t *testing.T) {
	f := newFixture(t)
	for _, inst := range []*domain.Institution{f.memberA, f.memberB} {
		if _, err := f.svc.SetApplicability(admin(), f.prep.ID, inst.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	parent := f.produce(t, 100, "2026-03-01T10:00:00+08:00")
	children, err := f.svc.SplitBatch(actorAt(domain.DeptProduction, f.lead.ID, "prod-1"), parent.ID, []int{40, 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Distribute(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), children[0].ID, f.memberA.ID, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Distribute(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), children[1].ID, f.memberB.ID, 60); err != nil {
		t.Fatal(err)
	}
	recall, err := f.svc.CreateRecall(actorAt(domain.DeptQuality, f.lead.ID, "qa-1"), parent.ID, "稳定性考察不合格")
	if err != nil {
		t.Fatal(err)
	}
	if len(recall.NoticeIDs) != 2 {
		t.Fatalf("撤回通知应覆盖全部 2 家领取机构，实际 %d", len(recall.NoticeIDs))
	}
	cov, err := f.svc.GetRecallCoverage(recall.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Complete || cov.Total != 2 || cov.Confirmed != 0 {
		t.Fatalf("初始覆盖应为 0/2，实际 %+v", cov)
	}
	// 非领取机构无通知可确认。
	if _, err := f.svc.ConfirmRecall(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), recall.ID); !domain.IsNotFound(err) {
		t.Fatalf("牵头院未领取批次，不应有撤回通知，实际 %v", err)
	}
	if _, err := f.svc.ConfirmRecall(actorAt(domain.DeptPharmacy, f.memberA.ID, "a-1"), recall.ID); err != nil {
		t.Fatal(err)
	}
	cov, _ = f.svc.GetRecallCoverage(recall.ID)
	if cov.Complete || cov.Confirmed != 1 || len(cov.Missing) != 1 || cov.Missing[0] != f.memberB.ID {
		t.Fatalf("确认 1 家后覆盖应为 1/2 缺 %s，实际 %+v", f.memberB.ID, cov)
	}
	if _, err := f.svc.ConfirmRecall(actorAt(domain.DeptPharmacy, f.memberA.ID, "a-2"), recall.ID); !domain.IsConflict(err) {
		t.Fatalf("重复确认应失败，实际 %v", err)
	}
	if _, err := f.svc.ConfirmRecall(actorAt(domain.DeptPharmacy, f.memberB.ID, "b-1"), recall.ID); err != nil {
		t.Fatal(err)
	}
	cov, _ = f.svc.GetRecallCoverage(recall.ID)
	if !cov.Complete || cov.Confirmed != 2 || len(cov.Missing) != 0 {
		t.Fatalf("全部确认后覆盖应完整，实际 %+v", cov)
	}
}

// ---- 科研用途与脱敏边界 ----

func TestResearchAuthorizationAndPrivacyBoundary(t *testing.T) {
	f := newFixture(t)
	req, err := f.svc.CreateResearchRequest(actorAt(domain.DeptClinical, f.memberA.ID, "a-1"),
		"真实世界疗效评价", []string{f.evidence.ID})
	if err != nil {
		t.Fatal(err)
	}
	// 申请方不能自行审批。
	if _, err := f.svc.ReviewResearchRequest(actorAt(domain.DeptClinical, f.memberA.ID, "a-2"), req.ID, true); !domain.IsConflict(err) {
		t.Fatalf("非所属机构审批应失败，实际 %v", err)
	}
	req, err = f.svc.ReviewResearchRequest(actorAt(domain.DeptClinical, f.lead.ID, "doc-9"), req.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != domain.ResearchApproved {
		t.Fatalf("审批后状态应为 approved，实际 %s", req.Status)
	}
	// 对外视图只含摘要与指纹，不含受控引用。
	view, err := f.svc.GetEvidenceView(f.evidence.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "his://") || strings.Contains(string(raw), "controlled") {
		t.Fatalf("对外视图不得泄露受控引用，实际 %s", raw)
	}
	// 审计视图中的证据同样只暴露摘要与指纹。
	b := f.produce(t, 10, "2026-03-01T10:00:00+08:00")
	report, _ := f.svc.AuditBatch(b.BatchNo)
	raw, _ = json.Marshal(report.Evidence)
	if strings.Contains(string(raw), "his://") {
		t.Fatal("审计视图不得泄露受控引用")
	}
}

func TestEvidenceIngestionRules(t *testing.T) {
	f := newFixture(t)
	in := EvidenceInput{
		EventID:        "EVT-X-1",
		SubjectRef:     "SUBJ-ANON-9",
		Category:       "clinical",
		Summary:        "辨证论治摘要",
		ControlledRef:  "emr://longhua/case/9",
		PayloadDigest:  domain.DigestOf([]byte("payload")),
		OccurredAt:     "2026-02-01T08:00:00+08:00",
		SourceSequence: 1,
	}
	ev, dup, err := f.svc.IngestEvidence(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"), in)
	if err != nil || dup {
		t.Fatalf("首次接入应成功且不重复: %v dup=%v", err, dup)
	}
	// 同一事件重复投递幂等。
	ev2, dup, err := f.svc.IngestEvidence(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"), in)
	if err != nil || !dup || ev2.ID != ev.ID {
		t.Fatalf("重复投递应幂等返回原证据: %v dup=%v", err, dup)
	}
	// 来源序号必须递增。
	in.EventID = "EVT-X-2"
	in.SourceSequence = 3
	if _, _, err := f.svc.IngestEvidence(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"), in); !domain.IsConflict(err) {
		t.Fatalf("跳号应被拒绝，实际 %v", err)
	}
	in.SourceSequence = 2
	if _, _, err := f.svc.IngestEvidence(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"), in); err != nil {
		t.Fatalf("序号连续后应成功: %v", err)
	}
	// 原始发生时间原样保留，不被到达时间覆盖。
	got, _ := f.svc.GetEvidenceView(ev.ID)
	if got.OccurredAt != "2026-02-01T08:00:00+08:00" {
		t.Fatalf("occurred_at 被改写为 %s", got.OccurredAt)
	}
	// 摘要夹带患者直标识被拒绝。
	bad := in
	bad.EventID = "EVT-X-3"
	bad.SubjectRef = "SUBJ-ANON-10"
	bad.SourceSequence = 1
	bad.Summary = `{"patient_name":"张某","note":"..."}`
	if _, _, err := f.svc.IngestEvidence(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"), bad); !domain.IsValidation(err) {
		t.Fatalf("夹带患者直标识应被拒绝，实际 %v", err)
	}
	// 受控引用必须是机构内部协议。
	bad.Summary = "正常摘要"
	bad.ControlledRef = "https://example.org/raw"
	if _, _, err := f.svc.IngestEvidence(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"), bad); !domain.IsValidation(err) {
		t.Fatalf("外部协议的受控引用应被拒绝，实际 %v", err)
	}
	// 指纹格式校验。
	bad.ControlledRef = "his://x/1"
	bad.PayloadDigest = "md5:abc"
	if _, _, err := f.svc.IngestEvidence(actorAt(domain.DeptClinical, f.lead.ID, "doc-1"), bad); !domain.IsValidation(err) {
		t.Fatalf("非法指纹应被拒绝，实际 %v", err)
	}
}

// ---- 审计追溯 ----

func TestAuditBatchTracesWholeChain(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.SetApplicability(admin(), f.prep.ID, f.memberA.ID, true); err != nil {
		t.Fatal(err)
	}
	b := f.produce(t, 80, "2026-03-01T10:00:00+08:00")
	if _, err := f.svc.Distribute(actorAt(domain.DeptPharmacy, f.lead.ID, "pha-1"), b.ID, f.memberA.ID, 30); err != nil {
		t.Fatal(err)
	}
	report, err := f.svc.AuditBatch(b.BatchNo)
	if err != nil {
		t.Fatal(err)
	}
	if report.Batch.ID != b.ID || report.Preparation.ID != f.prep.ID || report.Source.ID != f.source.ID {
		t.Fatal("审计应关联批次、制剂与来源")
	}
	if len(report.Evidence) != 1 || report.Evidence[0].ID != f.evidence.ID {
		t.Fatal("审计应列出依据的方证证据")
	}
	if report.Composition.Version.ID != f.compV.ID || report.Process.Version.ID != f.procV.ID || report.QC.Version.ID != f.qcV.ID {
		t.Fatal("审计应列出批次锁定的组方、工艺、质控版本")
	}
	if len(report.Composition.Version.SignOffs) != 3 {
		t.Fatal("审计应能看到版本的三方签署")
	}
	if len(report.Authorizations) != 1 || report.Authorizations[0].GranteeInstitution != f.memberA.ID {
		t.Fatal("审计应列出分发授权")
	}
	if len(report.Circulation) != 1 || report.Circulation[0].InstitutionID != f.memberA.ID || report.Circulation[0].Quantity != 30 {
		t.Fatalf("审计应给出当前流通范围，实际 %+v", report.Circulation)
	}
	if _, err := f.svc.AuditBatch("BN-UNKNOWN"); !domain.IsNotFound(err) {
		t.Fatalf("未知批号应返回不存在，实际 %v", err)
	}
}

func TestEventSequencePerSubject(t *testing.T) {
	f := newFixture(t)
	b := f.produce(t, 10, "2026-03-01T10:00:00+08:00")
	if _, err := f.svc.SuspendBatch(actorAt(domain.DeptQuality, f.lead.ID, "qa-1"), b.ID, "复核"); err != nil {
		t.Fatal(err)
	}
	events, err := f.svc.ListEvents(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("批次主体应有 2 条事件，实际 %d", len(events))
	}
	for i, ev := range events {
		if ev.SourceSequence != int64(i+1) {
			t.Fatalf("来源序号应自主体内递增，第 %d 条为 %d", i, ev.SourceSequence)
		}
		if ev.OccurredAt == "" || ev.RecordedAt == "" {
			t.Fatal("事件必须同时保留业务发生时间与接收时间")
		}
	}
}
