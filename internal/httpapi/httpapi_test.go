package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/batch-092001-q003/internal/domain"
	"example.com/batch-092001-q003/internal/service"
	"example.com/batch-092001-q003/internal/store"
)

// TestEndToEndFlow 走通“证据→来源→制剂→版本晋级→批次→分发→撤回→审计”
// 的完整 HTTP 链路。
func TestEndToEndFlow(t *testing.T) {
	st, _ := store.Open("")
	h := New(service.New(st)).Handler()

	call := func(method, path string, body any, headers map[string]string) (int, map[string]any) {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, &buf)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		out := map[string]any{}
		if rec.Body.Len() > 0 {
			var decoded any
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("响应不是 JSON: %v: %s", err, rec.Body.String())
			}
			switch v := decoded.(type) {
			case map[string]any:
				out = v
			case []any:
				out = map[string]any{"items": v}
			}
		}
		return rec.Code, out
	}
	must := func(want, got int, body map[string]any) {
		t.Helper()
		if got != want {
			t.Fatalf("期望状态 %d，实际 %d: %v", want, got, body)
		}
	}
	adminH := map[string]string{"X-Actor-Id": "admin-1", "X-Actor-Dept": "quality"}

	// 机构。
	code, lead := call("POST", "/institutions", map[string]any{"name": "龙华医院", "kind": "lead"}, adminH)
	must(http.StatusCreated, code, lead)
	leadID := lead["id"].(string)
	code, member := call("POST", "/institutions", map[string]any{"name": "社区中心A", "kind": "member"}, adminH)
	must(http.StatusCreated, code, member)
	memberID := member["id"].(string)

	leadH := map[string]string{"X-Actor-Id": "doc-1", "X-Actor-Dept": "clinical", "X-Institution-Id": leadID}

	// 证据接入（脱敏：摘要 + 指纹 + 内部受控引用）。
	code, ev := call("POST", "/evidence", map[string]any{
		"event_id":        "EVT-HTTP-1",
		"subject_ref":     "SUBJ-ANON-HTTP",
		"category":        "lineage",
		"summary":         "流派经验摘要",
		"controlled_ref":  "his://longhua/archive/77",
		"payload_digest":  domain.DigestOf([]byte("raw")),
		"occurred_at":     "2026-01-15T09:00:00+08:00",
		"source_sequence": 1,
	}, leadH)
	must(http.StatusCreated, code, ev)
	evID := ev["id"].(string)
	if _, leaked := ev["ControlledRef"]; leaked {
		t.Fatal("对外响应不得包含受控引用")
	}

	// 来源与制剂。
	code, src := call("POST", "/sources", map[string]any{
		"lineage": "顾氏外科流派", "origin_ref": "ORIGIN-ANON-1", "evidence_ids": []string{evID},
	}, leadH)
	must(http.StatusCreated, code, src)
	code, prep := call("POST", "/preparations", map[string]any{"name": "祛湿合剂", "source_id": src["id"]}, adminH)
	must(http.StatusCreated, code, prep)
	prepID := prep["id"].(string)

	// 三条版本线各自晋级。
	for _, line := range []struct{ key, eff string }{
		{"composition_line_id", ""},
		{"process_line_id", ""},
		{"qc_line_id", "2026-01-01T00:00:00+08:00"},
	} {
		lineID := prep[line.key].(string)
		payload := map[string]any{"payload": map[string]any{"note": "v1"}}
		if line.eff != "" {
			payload["effective_from"] = line.eff
		}
		code, ver := call("POST", "/lines/"+lineID+"/versions", payload, leadH)
		must(http.StatusCreated, code, ver)
		verID := ver["id"].(string)
		// 少一方签署不能晋级。
		for _, dept := range []string{"clinical", "pharmacy"} {
			code, _ = call("POST", "/versions/"+verID+"/signoffs", map[string]any{},
				map[string]string{"X-Actor-Id": dept + "-1", "X-Actor-Dept": dept, "X-Institution-Id": leadID})
			must(http.StatusOK, code, nil)
		}
		code, _ = call("POST", "/versions/"+verID+"/promote", nil, adminH)
		must(http.StatusConflict, code, nil)
		code, _ = call("POST", "/versions/"+verID+"/signoffs", map[string]any{},
			map[string]string{"X-Actor-Id": "pro-1", "X-Actor-Dept": "production", "X-Institution-Id": leadID})
		must(http.StatusOK, code, nil)
		code, _ = call("POST", "/versions/"+verID+"/promote", nil, adminH)
		must(http.StatusOK, code, nil)
	}

	// 适用机构与生产。
	code, _ = call("POST", "/preparations/"+prepID+"/applicability",
		map[string]any{"institution_id": memberID, "action": "grant"}, adminH)
	must(http.StatusOK, code, nil)
	code, batch := call("POST", "/batches", map[string]any{
		"preparation_id": prepID, "quantity": 100, "produced_at": "2026-03-01T10:00:00+08:00",
	}, map[string]string{"X-Actor-Id": "pro-1", "X-Actor-Dept": "production", "X-Institution-Id": leadID})
	must(http.StatusCreated, code, batch)
	batchID := batch["id"].(string)
	batchNo := batch["batch_no"].(string)

	// 分发与撤回。
	code, _ = call("POST", "/batches/"+batchID+"/distributions",
		map[string]any{"to_institution": memberID, "quantity": 40}, leadH)
	must(http.StatusCreated, code, nil)
	code, recall := call("POST", "/recalls", map[string]any{"batch_id": batchID, "reason": "稳定性异常"}, adminH)
	must(http.StatusCreated, code, recall)
	recallID := recall["id"].(string)
	code, cov := call("GET", "/recalls/"+recallID+"/coverage", nil, nil)
	must(http.StatusOK, code, cov)
	if cov["complete"].(bool) {
		t.Fatal("未确认前撤回不应完整")
	}
	code, _ = call("POST", "/recalls/"+recallID+"/confirm", nil,
		map[string]string{"X-Actor-Id": "m-1", "X-Actor-Dept": "pharmacy", "X-Institution-Id": memberID})
	must(http.StatusOK, code, nil)
	code, cov = call("GET", "/recalls/"+recallID+"/coverage", nil, nil)
	must(http.StatusOK, code, cov)
	if !cov["complete"].(bool) {
		t.Fatalf("确认后撤回应完整: %v", cov)
	}

	// 审计：批号追溯全链。
	code, audit := call("GET", "/audit/batches/"+batchNo, nil, nil)
	must(http.StatusOK, code, audit)
	if audit["batch"].(map[string]any)["batch_no"] != batchNo {
		t.Fatal("审计应定位到批次")
	}
	if len(audit["evidence"].([]any)) != 1 {
		t.Fatal("审计应列出方证证据")
	}
	if len(audit["circulation"].([]any)) != 1 {
		t.Fatal("审计应给出流通范围")
	}
	if len(audit["recalls"].([]any)) != 1 {
		t.Fatal("审计应列出撤回覆盖")
	}

	// 事件链。
	code, events := call("GET", fmt.Sprintf("/events?subject_ref=%s", batchID), nil, nil)
	must(http.StatusOK, code, events)
	if len(events["items"].([]any)) == 0 {
		t.Fatal("批次主体应有审计事件")
	}
}
