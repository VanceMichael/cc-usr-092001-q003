// Package httpapi 暴露证据链的 REST 接口。
//
// 操作人身份通过请求头传入：X-Actor-Id（操作人）、X-Actor-Dept
// （部门：clinical/pharmacy/production/quality）、X-Institution-Id
// （所属机构）。错误统一为 {"error":{"code","message"}}。
package httpapi

import (
	"encoding/json"
	"net/http"

	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/service"
)

// Server 是 HTTP 接入层。
type Server struct {
	svc *service.Service
	mux *http.ServeMux
}

// New 装配路由。
func New(svc *service.Service) *Server {
	s := &Server{svc: svc, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /health", s.health)
	s.mux.HandleFunc("POST /institutions", s.createInstitution)
	s.mux.HandleFunc("POST /evidence", s.ingestEvidence)
	s.mux.HandleFunc("GET /evidence/{id}", s.getEvidence)
	s.mux.HandleFunc("POST /sources", s.createSource)
	s.mux.HandleFunc("POST /preparations", s.createPreparation)
	s.mux.HandleFunc("GET /preparations/{id}", s.getPreparation)
	s.mux.HandleFunc("POST /preparations/{id}/applicability", s.setApplicability)
	s.mux.HandleFunc("POST /lines/{lineID}/versions", s.createVersion)
	s.mux.HandleFunc("GET /versions/{id}", s.getVersion)
	s.mux.HandleFunc("POST /versions/{id}/signoffs", s.signOff)
	s.mux.HandleFunc("POST /versions/{id}/promote", s.promote)
	s.mux.HandleFunc("POST /versions/{id}/suspend", s.suspendVersion)
	s.mux.HandleFunc("POST /batches", s.produceBatch)
	s.mux.HandleFunc("GET /batches/{id}", s.getBatch)
	s.mux.HandleFunc("POST /batches/{id}/split", s.splitBatch)
	s.mux.HandleFunc("POST /batches/{id}/distributions", s.distribute)
	s.mux.HandleFunc("POST /batches/{id}/suspend", s.suspendBatch)
	s.mux.HandleFunc("POST /recalls", s.createRecall)
	s.mux.HandleFunc("POST /recalls/{id}/confirm", s.confirmRecall)
	s.mux.HandleFunc("GET /recalls/{id}/coverage", s.recallCoverage)
	s.mux.HandleFunc("POST /research-requests", s.createResearch)
	s.mux.HandleFunc("GET /research-requests/{id}", s.getResearch)
	s.mux.HandleFunc("POST /research-requests/{id}/review", s.reviewResearch)
	s.mux.HandleFunc("GET /audit/batches/{batchNo}", s.auditBatch)
	s.mux.HandleFunc("GET /events", s.listEvents)
	return s
}

// Handler 返回根处理器。
func (s *Server) Handler() http.Handler { return s.mux }

func actorOf(r *http.Request) service.Actor {
	return service.Actor{
		ID:            r.Header.Get("X-Actor-Id"),
		Dept:          domain.Department(r.Header.Get("X-Actor-Dept")),
		InstitutionID: r.Header.Get("X-Institution-Id"),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := "internal"
	status := http.StatusInternalServerError
	switch {
	case domain.IsValidation(err):
		code, status = "invalid", http.StatusBadRequest
	case domain.IsNotFound(err):
		code, status = "not_found", http.StatusNotFound
	case domain.IsConflict(err):
		code, status = "conflict", http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return domain.Invalidf("请求体不是合法 JSON: %v", err)
	}
	return nil
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) createInstitution(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	inst, err := s.svc.CreateInstitution(actorOf(r), body.Name, body.Kind)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, inst)
}

func (s *Server) ingestEvidence(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventID        string `json:"event_id"`
		SubjectRef     string `json:"subject_ref"`
		Category       string `json:"category"`
		Summary        string `json:"summary"`
		ControlledRef  string `json:"controlled_ref"`
		PayloadDigest  string `json:"payload_digest"`
		OccurredAt     string `json:"occurred_at"`
		SourceSequence int64  `json:"source_sequence"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	ev, duplicated, err := s.svc.IngestEvidence(actorOf(r), service.EvidenceInput{
		EventID:        body.EventID,
		SubjectRef:     body.SubjectRef,
		Category:       body.Category,
		Summary:        body.Summary,
		ControlledRef:  body.ControlledRef,
		PayloadDigest:  body.PayloadDigest,
		OccurredAt:     body.OccurredAt,
		SourceSequence: body.SourceSequence,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusCreated
	if duplicated {
		status = http.StatusOK
	}
	writeJSON(w, status, ev.View())
}

func (s *Server) getEvidence(w http.ResponseWriter, r *http.Request) {
	view, err := s.svc.GetEvidenceView(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) createSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lineage     string   `json:"lineage"`
		OriginRef   string   `json:"origin_ref"`
		Note        string   `json:"note"`
		EvidenceIDs []string `json:"evidence_ids"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	src, err := s.svc.CreateSource(actorOf(r), body.Lineage, body.OriginRef, body.Note, body.EvidenceIDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, src)
}

func (s *Server) createPreparation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		SourceID string `json:"source_id"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	prep, err := s.svc.CreatePreparation(actorOf(r), body.Name, body.SourceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, prep)
}

func (s *Server) getPreparation(w http.ResponseWriter, r *http.Request) {
	prep, err := s.svc.GetPreparation(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prep)
}

func (s *Server) setApplicability(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InstitutionID string `json:"institution_id"`
		Action        string `json:"action"` // grant / revoke
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	grant := body.Action == "grant"
	if body.Action != "grant" && body.Action != "revoke" {
		writeErr(w, domain.Invalidf("action 必须是 grant 或 revoke"))
		return
	}
	prep, err := s.svc.SetApplicability(actorOf(r), r.PathValue("id"), body.InstitutionID, grant)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prep)
}

func (s *Server) createVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Payload       map[string]any `json:"payload"`
		EffectiveFrom string         `json:"effective_from"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	v, err := s.svc.CreateVersion(actorOf(r), r.PathValue("lineID"), body.Payload, body.EffectiveFrom)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.GetVersion(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) signOff(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Comment string `json:"comment"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	v, err := s.svc.SignOff(actorOf(r), r.PathValue("id"), body.Comment)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) promote(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Promote(actorOf(r), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) suspendVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	v, err := s.svc.SuspendVersion(actorOf(r), r.PathValue("id"), body.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) produceBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PreparationID string `json:"preparation_id"`
		Quantity      int    `json:"quantity"`
		ProducedAt    string `json:"produced_at"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	batch, err := s.svc.ProduceBatch(actorOf(r), body.PreparationID, body.Quantity, body.ProducedAt)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, batch)
}

func (s *Server) getBatch(w http.ResponseWriter, r *http.Request) {
	batch, err := s.svc.GetBatch(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, batch)
}

func (s *Server) splitBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Quantities []int `json:"quantities"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	children, err := s.svc.SplitBatch(actorOf(r), r.PathValue("id"), body.Quantities)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, children)
}

func (s *Server) distribute(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ToInstitution string `json:"to_institution"`
		Quantity      int    `json:"quantity"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	dist, err := s.svc.Distribute(actorOf(r), r.PathValue("id"), body.ToInstitution, body.Quantity)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dist)
}

func (s *Server) suspendBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	batch, err := s.svc.SuspendBatch(actorOf(r), r.PathValue("id"), body.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, batch)
}

func (s *Server) createRecall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BatchID string `json:"batch_id"`
		Reason  string `json:"reason"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	recall, err := s.svc.CreateRecall(actorOf(r), body.BatchID, body.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, recall)
}

func (s *Server) confirmRecall(w http.ResponseWriter, r *http.Request) {
	notice, err := s.svc.ConfirmRecall(actorOf(r), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notice)
}

func (s *Server) recallCoverage(w http.ResponseWriter, r *http.Request) {
	cov, err := s.svc.GetRecallCoverage(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cov)
}

func (s *Server) createResearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Purpose     string   `json:"purpose"`
		EvidenceIDs []string `json:"evidence_ids"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	req, err := s.svc.CreateResearchRequest(actorOf(r), body.Purpose, body.EvidenceIDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (s *Server) getResearch(w http.ResponseWriter, r *http.Request) {
	req, err := s.svc.GetResearchRequest(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) reviewResearch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Approve bool `json:"approve"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	req, err := s.svc.ReviewResearchRequest(actorOf(r), r.PathValue("id"), body.Approve)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) auditBatch(w http.ResponseWriter, r *http.Request) {
	report, err := s.svc.AuditBatch(r.PathValue("batchNo"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	subject := r.URL.Query().Get("subject_ref")
	if subject == "" {
		writeErr(w, domain.Invalidf("缺少查询参数 subject_ref"))
		return
	}
	events, err := s.svc.ListEvents(subject)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}
