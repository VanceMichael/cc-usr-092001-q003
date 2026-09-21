// Package domain 定义院内制剂传承证据链的稳定事实类型与校验约定。
//
// 领域围绕四条版本线管理同一方药的不同侧面：
//   - 流派传承：处方来源（PrescriptionSource）与追加式方证证据（Evidence）
//   - 临床验证：追加式方证证据（Evidence，category=clinical 等）
//   - 制剂组方 / 生产工艺 / 质控规格：三条线性版本线（VersionLine + DocVersion）
//
// 含患者信息的原始材料不进入本系统：证据只保存受控引用（ControlledRef，
// 指向所属机构内部归档）与 sha256 摘要，外部主体只能获得授权摘要与可验证指纹。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// 部门标识。版本晋级要求三个部门各自独立签署。
type Department string

const (
	DeptClinical   Department = "clinical"   // 临床部门
	DeptPharmacy   Department = "pharmacy"   // 药学部门
	DeptProduction Department = "production" // 生产部门
	DeptQuality    Department = "quality"    // 质量部门（仅用于紧急停用等质量动作）
)

// PromotionDepartments 是版本晋级必需的三个独立签署部门。
var PromotionDepartments = []Department{DeptClinical, DeptPharmacy, DeptProduction}

// 版本线种类。
const (
	LineComposition = "composition" // 制剂组方
	LineProcess     = "process"     // 生产工艺
	LineQC          = "qc"          // 质控规格
)

// LineKinds 列出全部版本线种类。
var LineKinds = []string{LineComposition, LineProcess, LineQC}

// 版本状态。
const (
	VersionDraft    = "draft"     // 草稿：可签署、可晋级
	VersionApproved = "approved"  // 已晋级：可被新批次引用
	VersionRejected = "suspended" // 已紧急停用：不得被新批次引用，历史批次不受影响
)

// 批次状态。
const (
	BatchActive    = "active"
	BatchSuspended = "suspended"
	BatchRecalled  = "recalled"
)

// 机构状态。
const (
	InstitutionActive    = "active"
	InstitutionSuspended = "suspended"
)

// 科研申请状态。
const (
	ResearchPending  = "pending"
	ResearchApproved = "approved"
	ResearchRejected = "rejected"
)

// 授权范围。
const (
	ScopeDistribution = "distribution" // 批次分发授权
	ScopeResearch     = "research"     // 科研用途授权
	ScopeSummary      = "summary"      // 摘要调阅授权
)

// Institution 是医联体成员机构。
type Institution struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // lead（牵头）或 member（成员）
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// Evidence 是一条匿名方证证据。原始材料留在所属机构，
// 本系统只保存授权摘要、受控引用与 sha256 指纹。
type Evidence struct {
	ID                string `json:"id"`
	EventID           string `json:"event_id"`           // 来源事件标识，用于幂等接入
	SubjectRef        string `json:"subject_ref"`        // 匿名主体引用，不含真实身份
	Category          string `json:"category"`           // lineage / clinical / real_world 等
	OwningInstitution string `json:"owning_institution"` // 原始材料所属机构
	Summary           string `json:"summary"`            // 经授权的摘要
	ControlledRef     string `json:"-"`                  // 机构内部受控引用，绝不对外序列化
	PayloadDigest     string `json:"payload_digest"`     // sha256:<hex>，原始材料指纹
	OccurredAt        string `json:"occurred_at"`        // 原始发生时间，接收方不得覆盖
	SourceSequence    int64  `json:"source_sequence"`    // 同一主体内严格递增
	RecordedAt        string `json:"recorded_at"`
}

// EvidenceView 是证据的对外视图：只有摘要与指纹，不含受控引用。
type EvidenceView struct {
	ID                string `json:"id"`
	SubjectRef        string `json:"subject_ref"`
	Category          string `json:"category"`
	OwningInstitution string `json:"owning_institution"`
	Summary           string `json:"summary"`
	PayloadDigest     string `json:"payload_digest"`
	OccurredAt        string `json:"occurred_at"`
	SourceSequence    int64  `json:"source_sequence"`
}

// View 返回证据的对外视图。
func (e *Evidence) View() EvidenceView {
	return EvidenceView{
		ID:                e.ID,
		SubjectRef:        e.SubjectRef,
		Category:          e.Category,
		OwningInstitution: e.OwningInstitution,
		Summary:           e.Summary,
		PayloadDigest:     e.PayloadDigest,
		OccurredAt:        e.OccurredAt,
		SourceSequence:    e.SourceSequence,
	}
}

// PrescriptionSource 记录处方来源（流派传承线）。
type PrescriptionSource struct {
	ID            string   `json:"id"`
	Lineage       string   `json:"lineage"`    // 流派名称
	OriginRef     string   `json:"origin_ref"` // 匿名化的传承来源引用
	InstitutionID string   `json:"institution_id"`
	EvidenceIDs   []string `json:"evidence_ids"` // 支撑证据
	Note          string   `json:"note"`
	CreatedAt     string   `json:"created_at"`
}

// Preparation 是一个院内制剂，关联来源与三条版本线。
type Preparation struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	SourceID               string   `json:"source_id"`
	CompositionLineID      string   `json:"composition_line_id"`
	ProcessLineID          string   `json:"process_line_id"`
	QCLineID               string   `json:"qc_line_id"`
	ApplicableInstitutions []string `json:"applicable_institutions"` // 适用机构
	CreatedAt              string   `json:"created_at"`
}

// VersionLine 是一条线性版本线。任一时刻至多一个草稿版本；
// 新版本必须以当前最新版本为基线，保证链条不跳跃。
type VersionLine struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"` // composition / process / qc
	PreparationID string   `json:"preparation_id"`
	Title         string   `json:"title"`
	VersionIDs    []string `json:"version_ids"` // 按创建顺序
}

// DocVersion 是版本线上的一个版本。
type DocVersion struct {
	ID            string         `json:"id"`
	LineID        string         `json:"line_id"`
	Seq           int            `json:"seq"`             // 线内序号，从 1 开始
	BaseVersionID string         `json:"base_version_id"` // 上一版；首版为空
	Payload       map[string]any `json:"payload"`         // 组方明细 / 工艺步骤 / 质控限度
	EffectiveFrom string         `json:"effective_from"`  // 生效日（质控线必填，其余可空）
	Status        string         `json:"status"`
	SignOffs      []SignOff      `json:"sign_offs"`
	CreatedBy     string         `json:"created_by"`
	CreatedAt     string         `json:"created_at"`
	PromotedBy    string         `json:"promoted_by,omitempty"`
	PromotedAt    string         `json:"promoted_at,omitempty"`
	SuspendedBy   string         `json:"suspended_by,omitempty"`
	SuspendedAt   string         `json:"suspended_at,omitempty"`
	SuspendReason string         `json:"suspend_reason,omitempty"`
}

// SignOff 是一个部门对某版本的独立签署。
type SignOff struct {
	Department Department `json:"department"`
	ActorID    string     `json:"actor_id"`
	SignedAt   string     `json:"signed_at"`
	Comment    string     `json:"comment,omitempty"`
}

// Batch 是一个制剂批次。批次在产出时锁定所依据的三个版本，
// 之后版本线的新规则不会追溯改写该批次。
type Batch struct {
	ID                   string `json:"id"`
	BatchNo              string `json:"batch_no"`
	PreparationID        string `json:"preparation_id"`
	CompositionVersionID string `json:"composition_version_id"`
	ProcessVersionID     string `json:"process_version_id"`
	QCVersionID          string `json:"qc_version_id"`
	ProducedAt           string `json:"produced_at"`
	ProducedBy           string `json:"produced_by"` // 生产机构
	Quantity             int    `json:"quantity"`
	Remaining            int    `json:"remaining"` // 尚未拆分或分发的数量
	ParentBatchID        string `json:"parent_batch_id,omitempty"`
	Status               string `json:"status"`
	SuspendReason        string `json:"suspend_reason,omitempty"`
}

// Distribution 是一次批次分发，同时构成一条授权记录。
type Distribution struct {
	ID              string `json:"id"`
	BatchID         string `json:"batch_id"`
	ToInstitution   string `json:"to_institution"`
	Quantity        int    `json:"quantity"`
	ShippedAt       string `json:"shipped_at"`
	AuthorizationID string `json:"authorization_id"`
}

// Recall 是一次跨院撤回，覆盖目标批次及其全部子批次。
type Recall struct {
	ID        string   `json:"id"`
	BatchID   string   `json:"batch_id"`
	Reason    string   `json:"reason"`
	CreatedBy string   `json:"created_by"`
	CreatedAt string   `json:"created_at"`
	NoticeIDs []string `json:"notice_ids"`
}

// RecallNotice 是发给单一领取机构的撤回通知。
type RecallNotice struct {
	ID            string `json:"id"`
	RecallID      string `json:"recall_id"`
	InstitutionID string `json:"institution_id"`
	SentAt        string `json:"sent_at"`
	ConfirmedBy   string `json:"confirmed_by,omitempty"`
	ConfirmedAt   string `json:"confirmed_at,omitempty"`
}

// ResearchRequest 是科研用途申请。
type ResearchRequest struct {
	ID                   string   `json:"id"`
	ApplicantInstitution string   `json:"applicant_institution"`
	Purpose              string   `json:"purpose"`
	EvidenceIDs          []string `json:"evidence_ids"`
	Status               string   `json:"status"`
	CreatedAt            string   `json:"created_at"`
	ReviewedBy           string   `json:"reviewed_by,omitempty"`
	ReviewedAt           string   `json:"reviewed_at,omitempty"`
}

// Authorization 记录一次对外授权：外部主体只能凭授权获得摘要与指纹。
type Authorization struct {
	ID                 string `json:"id"`
	GranteeInstitution string `json:"grantee_institution"`
	ObjectKind         string `json:"object_kind"` // evidence / batch
	ObjectID           string `json:"object_id"`
	Scope              string `json:"scope"`
	GrantedBy          string `json:"granted_by"`
	CreatedAt          string `json:"created_at"`
}

// Suspension 是一次紧急停用记录。
type Suspension struct {
	ID         string `json:"id"`
	ObjectKind string `json:"object_kind"` // version / batch
	ObjectID   string `json:"object_id"`
	Reason     string `json:"reason"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
}

// Event 是写入事件日志的一条记录，形状与 contracts/ 示例一致。
type Event struct {
	EventID        string `json:"event_id"`
	SubjectRef     string `json:"subject_ref"`
	Kind           string `json:"kind"`
	OccurredAt     string `json:"occurred_at"` // 业务发生时间，不由到达时间覆盖
	SourceSequence int64  `json:"source_sequence"`
	PayloadDigest  string `json:"payload_digest"`
	RecordedAt     string `json:"recorded_at"`
}

// ---- 校验与工具 ----

// ValidationError 表示输入不满足领域约定。
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Invalidf 构造输入校验错误。
func Invalidf(format string, args ...any) *ValidationError {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) *ValidationError { return Invalidf(format, args...) }

// ConflictError 表示违反状态机或并发约束。
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Conflictf 构造状态冲突错误。
func Conflictf(format string, args ...any) *ConflictError {
	return &ConflictError{Msg: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) *ConflictError { return Conflictf(format, args...) }

// NotFoundError 表示引用对象不存在。
type NotFoundError struct{ Msg string }

func (e *NotFoundError) Error() string { return e.Msg }

// NotFoundf 构造对象不存在错误。
func NotFoundf(format string, args ...any) *NotFoundError {
	return &NotFoundError{Msg: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) *NotFoundError { return NotFoundf(format, args...) }

var digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// CheckDigest 校验 sha256 指纹格式。
func CheckDigest(d string) error {
	if !digestRE.MatchString(d) {
		return invalid("payload_digest 必须是 sha256:<64位小写十六进制>")
	}
	return nil
}

// DigestOf 计算载荷的 sha256 指纹。
func DigestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CheckTime 校验带偏移量的 ISO 8601 时间。
func CheckTime(value, field string) error {
	if value == "" {
		return invalid("%s 不能为空", field)
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return invalid("%s 必须是带偏移量的 ISO 8601 时间: %v", field, err)
	}
	return nil
}

// forbiddenPatientKeys 是不得出现在对外材料中的患者直标识字段。
var forbiddenPatientKeys = []string{
	"patient_name", "patient_id", "id_card", "id_number", "mrn",
	"medical_record_no", "phone", "address", "birth_date", "ssn",
}

// CheckNoPatientIdentifiers 拒绝在对外字段中携带患者直标识。
func CheckNoPatientIdentifiers(text, field string) error {
	lower := strings.ToLower(text)
	for _, key := range forbiddenPatientKeys {
		if strings.Contains(lower, key) {
			return invalid("%s 不得包含患者直标识字段 %q；含患者信息的原始材料不得离开所属机构", field, key)
		}
	}
	return nil
}

// internalRefSchemes 是受控引用允许的内部协议。
var internalRefSchemes = []string{"his://", "emr://", "lis://", "archive://"}

// CheckControlledRef 校验受控引用指向机构内部归档。
func CheckControlledRef(ref string) error {
	if ref == "" {
		return invalid("controlled_ref 不能为空：必须指向所属机构内部归档的原始材料")
	}
	for _, scheme := range internalRefSchemes {
		if strings.HasPrefix(ref, scheme) {
			return nil
		}
	}
	return invalid("controlled_ref 必须使用机构内部协议（%s）", strings.Join(internalRefSchemes, " "))
}

// CheckDepartment 校验部门标识。
func CheckDepartment(d Department) error {
	switch d {
	case DeptClinical, DeptPharmacy, DeptProduction, DeptQuality:
		return nil
	default:
		return invalid("未知部门 %q", d)
	}
}

// IsPromotionDepartment 报告部门是否可参与版本晋级签署。
func IsPromotionDepartment(d Department) bool {
	for _, dept := range PromotionDepartments {
		if d == dept {
			return true
		}
	}
	return false
}

// RequireNonEmpty 校验必填字符串。
func RequireNonEmpty(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return invalid("%s 不能为空", field)
	}
	return nil
}

// errors.Is 辅助：区分错误类别供 HTTP 层映射状态码。
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func IsConflict(err error) bool {
	var v *ConflictError
	return errors.As(err, &v)
}

func IsNotFound(err error) bool {
	var v *NotFoundError
	return errors.As(err, &v)
}
