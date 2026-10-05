package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

type specimenView struct {
	ID            string `json:"id"`
	CatalogNumber string `json:"catalog_number"`
	Status        string `json:"status"`
}

type planView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
	Status     string `json:"status"`
}

type planSnapshot struct {
	Plan     planView `json:"plan"`
	Progress []struct {
		Percent int `json:"percent"`
	} `json:"progress"`
}

type releaseView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
}

type archiveView struct {
	ID            string `json:"id"`
	PlanID        string `json:"plan_id"`
	ReleaseID     string `json:"release_id"`
	ContentDigest string `json:"content_digest"`
	Progress      []struct {
		Percent int `json:"percent"`
	} `json:"progress"`
}

type errorEnvelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Reference string `json:"reference"`
}

type listEnvelope struct {
	Items []json.RawMessage `json:"items"`
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to check")
	flag.Parse()

	if err := run(*workflow); err != nil {
		fmt.Fprintln(os.Stderr, "workflow check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("workflow check passed: %s\n", *workflow)
}

func run(workflow string) error {
	// Local checks run without waiting out the recent-query retention window.
	service := station.New(
		repository.NewMemory(),
		station.WithArchiveRetention(0),
	)
	handler := httpapi.NewRouter(service)
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	switch workflow {
	case "register-specimen":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if specimen.Status != "registered" {
			return fmt.Errorf("expected registered status, got %s", specimen.Status)
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != specimen.ID {
			return errors.New("registered specimen cannot be read back")
		}
		return nil

	case "approve-prep-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		var approved planView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
			http.StatusOK,
			&approved,
		); err != nil {
			return err
		}
		if approved.Status != "approved" {
			return fmt.Errorf("expected approved plan, got %s", approved.Status)
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "preparing" {
			return fmt.Errorf("expected preparing specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "log-preparation":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 35, "矩阵剥离"); err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.Status != "completed" || len(snapshot.Progress) != 2 {
			return errors.New("plan did not complete with exactly two progress entries")
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "stabilized" {
			return fmt.Errorf("expected stabilized specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "release-for-study":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var release releaseView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/release",
			contracts.ReleaseRequest{
				Purpose:    "比较解剖研究",
				Recipient:  "地层研究组",
				ApprovedBy: "keeper-zhou",
			},
			http.StatusCreated,
			&release,
		); err != nil {
			return err
		}
		if release.SpecimenID != specimen.ID {
			return errors.New("release record references the wrong specimen")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("expected available specimen, got %s", fetched.Status)
		}
		return nil

	case "archive-completed-plan":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		if _, err := requestResponse(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/release",
			contracts.ReleaseRequest{
				Purpose:    "比较解剖研究",
				Recipient:  "地层研究组",
				ApprovedBy: "keeper-zhou",
			},
			http.StatusCreated,
		); err != nil {
			return err
		}

		// An unsettled release cannot block here: archiving before release must
		// be rejected with release_unsettled, proven on a second specimen.
		if err := expectArchiveRejectedBeforeRelease(client, server.URL); err != nil {
			return err
		}

		var archive archiveView
		if raw, err := requestResponse(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/archive",
			contracts.ArchivePlanRequest{
				ArchivedBy:     "keeper-zhou",
				AuditReference: "AUDIT-2026-0014",
			},
			http.StatusCreated,
		); err != nil {
			return err
		} else if err := json.Unmarshal(raw, &archive); err != nil {
			return err
		}
		if archive.PlanID != plan.ID || archive.ReleaseID == "" || archive.ContentDigest == "" {
			return fmt.Errorf("archive does not preserve references: %+v", archive)
		}

		// Repeat submission must be idempotent: 200 with the same record.
		raw, err := requestResponse(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/archive",
			contracts.ArchivePlanRequest{
				ArchivedBy:     "someone-else",
				AuditReference: "AUDIT-2026-9999",
			},
			http.StatusOK,
		)
		if err != nil {
			return err
		}
		var repeated archiveView
		if err := json.Unmarshal(raw, &repeated); err != nil {
			return err
		}
		if repeated.ID != archive.ID || repeated.ContentDigest != archive.ContentDigest {
			return errors.New("repeated archival produced a second result")
		}

		// The plan is out of the online read set, with a locatable 404.
		body, err := requestResponse(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusNotFound)
		if err != nil {
			return err
		}
		var gone errorEnvelope
		if err := json.Unmarshal(body, &gone); err != nil {
			return err
		}
		if gone.Code != "plan_archived" || gone.Reference != "/plan-archives/"+archive.ID {
			return fmt.Errorf("archived read not locatable: %+v", gone)
		}

		// The surviving fact is readable by both locators.
		if _, err := requestResponse(client, http.MethodGet, server.URL+"/plans/"+plan.ID+"/archive", nil, http.StatusOK); err != nil {
			return err
		}
		if _, err := requestResponse(client, http.MethodGet, server.URL+"/plan-archives/"+archive.ID, nil, http.StatusOK); err != nil {
			return err
		}

		// Online scope shrinks; archive listing and specimen state are preserved.
		var plans listEnvelope
		if raw, err := requestResponse(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID+"/plans", nil, http.StatusOK); err != nil {
			return err
		} else if err := json.Unmarshal(raw, &plans); err != nil || len(plans.Items) != 0 {
			return fmt.Errorf("online plan set still contains archived plan: %s", string(raw))
		}
		var archives listEnvelope
		if raw, err := requestResponse(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID+"/archives", nil, http.StatusOK); err != nil {
			return err
		} else if err := json.Unmarshal(raw, &archives); err != nil || len(archives.Items) != 1 {
			return fmt.Errorf("archive listing must keep exactly one record: %s", string(raw))
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("archival must not rewrite specimen status, got %s", fetched.Status)
		}
		return nil

	case "archive-cancelled-plan":
		// Only drafts can be cancelled; the approved plan from setup cannot.
		_, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if _, err := requestResponse(
			client, http.MethodPost, server.URL+"/plans/"+plan.ID+"/cancel",
			contracts.CancelPlanRequest{CancelledBy: "curator-lin"}, http.StatusOK,
		); err == nil {
			return errors.New("approved plan must not be cancellable")
		}

		// A fresh specimen + draft that is cancelled and then archived.
		specimen2, err := registerSpecimenWith(client, server.URL, "PPS-2026-002")
		if err != nil {
			return err
		}
		var draft planView
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/specimens/"+specimen2.ID+"/plans",
			contracts.CreatePlanRequest{
				Objective:      "暴露主要关节面",
				Method:         "手工显微剥离",
				TargetExposure: 80,
			},
			http.StatusCreated, &draft,
		); err != nil {
			return err
		}
		var cancelled planView
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+draft.ID+"/cancel",
			contracts.CancelPlanRequest{CancelledBy: "curator-lin"}, http.StatusOK, &cancelled,
		); err != nil {
			return err
		}
		if cancelled.Status != "cancelled" {
			return fmt.Errorf("expected cancelled plan, got %s", cancelled.Status)
		}
		var archive archiveView
		if err := requestJSON(
			client, http.MethodPost, server.URL+"/plans/"+draft.ID+"/archive",
			contracts.ArchivePlanRequest{ArchivedBy: "keeper-zhou", AuditReference: "AUD-2026-02"},
			http.StatusCreated, &archive,
		); err != nil {
			return err
		}
		if archive.ReleaseID != "" || len(archive.Progress) != 0 {
			return fmt.Errorf("cancelled plan archive must not carry release/progress references: %+v", archive)
		}
		if _, err := requestResponse(client, http.MethodGet, server.URL+"/plans/"+draft.ID, nil, http.StatusNotFound); err != nil {
			return err
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen2.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "planned" {
			return fmt.Errorf("cancelled-plan archival must not rewrite specimen status, got %s", fetched.Status)
		}
		return nil
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	return registerSpecimenWith(client, baseURL, "PPS-2026-001")
}

func registerSpecimenWith(client *http.Client, baseURL, catalog string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: catalog,
			FieldLabel:    "北坡-07",
			TaxonHint:     "大型脊椎动物",
			MatrixKind:    "粉砂岩",
			JacketNotes:   "夹克边角完好",
		},
		http.StatusCreated,
		&specimen,
	)
	return specimen, err
}

func createPlan(client *http.Client, baseURL, specimenID string) (planView, error) {
	var plan planView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/plans",
		contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			Adhesive:       "可逆树脂",
			TargetExposure: 80,
		},
		http.StatusCreated,
		&plan,
	)
	return plan, err
}

func completedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	specimen, err := registerSpecimen(client, baseURL)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	var approved planView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func recordProgress(client *http.Client, baseURL, planID string, percent int, stage string) error {
	var ignored struct{}
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{
			Stage:      stage,
			Percent:    percent,
			Note:       "本地链路检查",
			RecordedBy: "preparator-chen",
		},
		http.StatusCreated,
		&ignored,
	)
}

func requestJSON(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) error {
	raw, err := requestResponse(client, method, url, body, wantStatus)
	if err != nil {
		return err
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response for %s %s: %w", method, url, err)
	}
	return nil
}

// requestResponse performs a request like requestJSON but returns the raw body,
// allowing checks to inspect error envelopes or decode afterwards.
func requestResponse(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
) ([]byte, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != wantStatus {
		return nil, fmt.Errorf("request %s %s returned %d, want %d: %s", method, url, response.StatusCode, wantStatus, string(raw))
	}
	return raw, nil
}

// expectArchiveRejectedBeforeRelease builds a second completed plan that is
// still stabilized without a release and proves archival is blocked until the
// release reference settles, while the online plan stays intact.
func expectArchiveRejectedBeforeRelease(client *http.Client, baseURL string) error {
	specimen, err := registerSpecimenWith(client, baseURL, "PPS-2026-050")
	if err != nil {
		return err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return err
	}
	var approved planView
	if err := requestJSON(
		client, http.MethodPost, baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"}, http.StatusOK, &approved,
	); err != nil {
		return err
	}
	if err := recordProgress(client, baseURL, plan.ID, 100, "表面加固"); err != nil {
		return err
	}
	raw, err := requestResponse(
		client, http.MethodPost, baseURL+"/plans/"+plan.ID+"/archive",
		contracts.ArchivePlanRequest{ArchivedBy: "keeper-zhou", AuditReference: "AUDIT-2026-0077"},
		http.StatusConflict,
	)
	if err != nil {
		return err
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Code != "release_unsettled" {
		return fmt.Errorf("expected release_unsettled, got %s", envelope.Code)
	}
	// The rejected submission must not move the plan out of the online set.
	var snapshot planSnapshot
	if err := requestJSON(client, http.MethodGet, baseURL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
		return err
	}
	if snapshot.Plan.Status != "completed" {
		return fmt.Errorf("online plan changed after rejected archival: %s", snapshot.Plan.Status)
	}
	return nil
}
