package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

type httpErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Reference string `json:"reference"`
}

type httpResponse struct {
	status   int
	location string
	raw      []byte
	object   map[string]any
	apiError httpErrorBody
}

func newArchiveHTTPFixture(t *testing.T) *httptest.Server {
	t.Helper()
	clock := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	service := station.New(
		repository.NewMemory(),
		station.WithClock(func() time.Time { return clock }),
		station.WithArchiveRetention(0),
	)
	server := httptest.NewServer(NewRouter(service))
	t.Cleanup(server.Close)
	return server
}

func sendJSON(t *testing.T, method, url string, body any) httpResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	result := httpResponse{status: response.StatusCode, location: response.Header.Get("Location"), raw: raw}
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) == nil {
		if _, isError := envelope["code"]; isError {
			_ = json.Unmarshal(raw, &result.apiError)
		} else {
			result.object = envelope
		}
	}
	return result
}

func expectStatus(t *testing.T, result httpResponse, want int) httpResponse {
	t.Helper()
	if result.status != want {
		t.Fatalf("expected status %d, got %d: %s", want, result.status, string(result.raw))
	}
	return result
}

func postObject(t *testing.T, url string, body any) map[string]any {
	t.Helper()
	return expectStatus(t, sendJSON(t, http.MethodPost, url, body), http.StatusCreated).object
}

func setupReleasedPlan(t *testing.T, baseURL string) (string, string) {
	t.Helper()
	specimen := postObject(t, baseURL+"/specimens", contracts.RegisterSpecimenRequest{
		CatalogNumber: "PPS-2026-201",
		FieldLabel:    "北坡-07",
		MatrixKind:    "粉砂岩",
	})
	plan := postObject(t, baseURL+"/specimens/"+specimen["id"].(string)+"/plans", contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面",
		Method:         "手工显微剥离",
		TargetExposure: 80,
	})
	planID := plan["id"].(string)
	expectStatus(t, sendJSON(t, http.MethodPost, baseURL+"/plans/"+planID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"}), http.StatusOK)
	expectStatus(t, sendJSON(t, http.MethodPost, baseURL+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{Stage: "表面加固", Percent: 100, RecordedBy: "preparator-chen"}), http.StatusCreated)
	expectStatus(t, sendJSON(t, http.MethodPost, baseURL+"/specimens/"+specimen["id"].(string)+"/release",
		contracts.ReleaseRequest{Purpose: "比较解剖研究", Recipient: "地层研究组", ApprovedBy: "keeper-zhou"}), http.StatusCreated)
	return specimen["id"].(string), planID
}

func listItems(t *testing.T, result httpResponse) []map[string]any {
	t.Helper()
	var envelope struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(result.raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Items
}

func TestArchivePlanHTTPFlow(t *testing.T) {
	base := newArchiveHTTPFixture(t).URL
	specimenID, planID := setupReleasedPlan(t, base)

	created := expectStatus(t, sendJSON(t, http.MethodPost, base+"/plans/"+planID+"/archive",
		contracts.ArchivePlanRequest{ArchivedBy: "keeper-zhou", AuditReference: "AUDIT-2026-0014"}), http.StatusCreated)
	if !strings.HasPrefix(created.location, "/plan-archives/archive-") {
		t.Fatalf("archive location missing or wrong: %q", created.location)
	}
	archiveID := created.object["id"].(string)

	// Repeat archival is idempotent: 200, same record, no new location.
	repeated := sendJSON(t, http.MethodPost, base+"/plans/"+planID+"/archive",
		contracts.ArchivePlanRequest{ArchivedBy: "other", AuditReference: "AUDIT-2026-9999"})
	expectStatus(t, repeated, http.StatusOK)
	if repeated.location != "" || repeated.object["id"] != archiveID ||
		repeated.object["audit_reference"] != "AUDIT-2026-0014" {
		t.Fatalf("idempotent archive must return the original record: %s", string(repeated.raw))
	}

	// Online read is an explicit, locatable 404 — not a silent rewrite.
	online := sendJSON(t, http.MethodGet, base+"/plans/"+planID, nil)
	if online.status != http.StatusNotFound || online.apiError.Code != "plan_archived" ||
		online.apiError.Reference != "/plan-archives/"+archiveID {
		t.Fatalf("archived plan read expected locatable plan_archived 404, got %d %+v", online.status, online.apiError)
	}

	// Both archive locators expose the surviving fact.
	expectStatus(t, sendJSON(t, http.MethodGet, base+"/plans/"+planID+"/archive", nil), http.StatusOK)
	expectStatus(t, sendJSON(t, http.MethodGet, base+"/plan-archives/"+archiveID, nil), http.StatusOK)

	plans := listItems(t, expectStatus(t, sendJSON(t, http.MethodGet, base+"/specimens/"+specimenID+"/plans", nil), http.StatusOK))
	if len(plans) != 0 {
		t.Fatalf("archived plan must leave the online set, got %v", plans)
	}
	archives := listItems(t, expectStatus(t, sendJSON(t, http.MethodGet, base+"/specimens/"+specimenID+"/archives", nil), http.StatusOK))
	if len(archives) != 1 || archives[0]["id"] != archiveID {
		t.Fatalf("archive listing must keep the archived plan, got %v", archives)
	}

	// Further writes against the archived plan fail with the same error.
	progress := sendJSON(t, http.MethodPost, base+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{Stage: "x", Percent: 100, RecordedBy: "y"})
	if progress.status != http.StatusNotFound || progress.apiError.Code != "plan_archived" {
		t.Fatalf("progress on archived plan expected plan_archived 404, got %d %+v", progress.status, progress.apiError)
	}
}

func TestArchiveUnsettledReleaseHTTPConflict(t *testing.T) {
	base := newArchiveHTTPFixture(t).URL
	specimen := postObject(t, base+"/specimens", contracts.RegisterSpecimenRequest{
		CatalogNumber: "PPS-2026-202",
		FieldLabel:    "北坡-07",
		MatrixKind:    "粉砂岩",
	})
	plan := postObject(t, base+"/specimens/"+specimen["id"].(string)+"/plans", contracts.CreatePlanRequest{
		Objective: "暴露主要关节面", Method: "手工显微剥离", TargetExposure: 80,
	})
	planID := plan["id"].(string)
	expectStatus(t, sendJSON(t, http.MethodPost, base+"/plans/"+planID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"}), http.StatusOK)
	expectStatus(t, sendJSON(t, http.MethodPost, base+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{Stage: "表面加固", Percent: 100, RecordedBy: "preparator-chen"}), http.StatusCreated)

	result := sendJSON(t, http.MethodPost, base+"/plans/"+planID+"/archive",
		contracts.ArchivePlanRequest{ArchivedBy: "keeper-zhou", AuditReference: "AUDIT-2026-0014"})
	if result.status != http.StatusConflict || result.apiError.Code != "release_unsettled" {
		t.Fatalf("expected release_unsettled 409, got %d %+v", result.status, result.apiError)
	}
}

func TestCancelThenArchiveHTTPFlow(t *testing.T) {
	base := newArchiveHTTPFixture(t).URL
	specimen := postObject(t, base+"/specimens", contracts.RegisterSpecimenRequest{
		CatalogNumber: "PPS-2026-203",
		FieldLabel:    "北坡-07",
		MatrixKind:    "粉砂岩",
	})
	plan := postObject(t, base+"/specimens/"+specimen["id"].(string)+"/plans", contracts.CreatePlanRequest{
		Objective: "暴露主要关节面", Method: "手工显微剥离", TargetExposure: 80,
	})
	planID := plan["id"].(string)
	cancelled := expectStatus(t, sendJSON(t, http.MethodPost, base+"/plans/"+planID+"/cancel",
		contracts.CancelPlanRequest{CancelledBy: "curator-lin"}), http.StatusOK)
	if cancelled.object["status"] != "cancelled" {
		t.Fatalf("expected cancelled status, got %v", cancelled.object["status"])
	}

	expectStatus(t, sendJSON(t, http.MethodPost, base+"/plans/"+planID+"/archive",
		contracts.ArchivePlanRequest{ArchivedBy: "keeper-zhou", AuditReference: "AUD-2026-01"}), http.StatusCreated)
	online := sendJSON(t, http.MethodGet, base+"/plans/"+planID, nil)
	if online.status != http.StatusNotFound || online.apiError.Code != "plan_archived" {
		t.Fatalf("archived cancelled plan expected plan_archived 404, got %d %+v", online.status, online.apiError)
	}
}
