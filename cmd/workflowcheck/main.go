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
	ID          string `json:"id"`
	SpecimenID  string `json:"specimen_id"`
	Status      string `json:"status"`
	CancelledBy string `json:"cancelled_by"`
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
	ID            string   `json:"id"`
	PlanID        string   `json:"plan_id"`
	SpecimenID    string   `json:"specimen_id"`
	Plan          planView `json:"plan"`
	ProgressCount int      `json:"progress_count"`
	ReleaseID     string   `json:"release_id"`
	ArchivedBy    string   `json:"archived_by"`
	Digest        string   `json:"digest"`
	Progress      []struct {
		Sequence int `json:"sequence"`
		Percent  int `json:"percent"`
	} `json:"progress"`
}

type errorBodyView struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details"`
}

type planListingView struct {
	Items    []planView `json:"items"`
	Archived []struct {
		PlanID    string `json:"plan_id"`
		ArchiveID string `json:"archive_id"`
		Status    string `json:"status"`
		Digest    string `json:"digest"`
	} `json:"archived"`
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
	// Workflow checks run the full lifecycle in milliseconds, so the recent
	// query retention window is collapsed to zero here, except for the
	// dedicated retention-boundary workflow.
	retention := time.Duration(0)
	if workflow == "archive-retention-window" {
		retention = 24 * time.Hour
	}
	service := station.New(repository.NewMemory(), station.WithArchiveRetention(retention))
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
		specimen, plan, err := releasedSetup(client, server.URL)
		if err != nil {
			return err
		}
		var archived archiveView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/archive",
			contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"},
			http.StatusCreated,
			&archived,
		); err != nil {
			return err
		}
		if archived.PlanID != plan.ID || archived.SpecimenID != specimen.ID {
			return errors.New("archive does not bind the sealed plan and specimen")
		}
		if archived.Plan.Status != "completed" || archived.ProgressCount != 1 || archived.ReleaseID == "" {
			return errors.New("archive snapshot is missing the completed trail or release reference")
		}
		if archived.Digest == "" || len(archived.Digest) != 64 {
			return errors.New("archive must carry a sha-256 existence digest")
		}

		// A repeated submission conflicts and points at the existing archive;
		// it must not mint a second result.
		dupBody, err := expectError(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/archive",
			contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"},
			http.StatusConflict,
		)
		if err != nil {
			return err
		}
		if dupBody.Code != "plan_already_archived" || dupBody.Details["archive_id"] != archived.ID {
			return fmt.Errorf("duplicate archive returned %v", dupBody)
		}

		// The plan left the online collection: reads are an explicit, located
		// failure, never an empty snapshot, and writes are conflicts.
		goneBody, err := expectError(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusNotFound)
		if err != nil {
			return err
		}
		if goneBody.Code != "plan_archived" || goneBody.Details["archive_id"] != archived.ID {
			return fmt.Errorf("archived plan read returned %v", goneBody)
		}
		writeBody, err := expectError(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/progress",
			contracts.ProgressRequest{Stage: "补记", Percent: 100, RecordedBy: "preparator-chen"},
			http.StatusConflict,
		)
		if err != nil {
			return err
		}
		if writeBody.Code != "plan_archived" {
			return fmt.Errorf("progress write after archive returned %v", writeBody)
		}

		// The archive remains a stable, locatable existence fact: two reads
		// return byte-identical content, including the sealed digest.
		first, err := readRaw(client, http.MethodGet, server.URL+"/archives/"+archived.ID)
		if err != nil {
			return err
		}
		var refetch archiveView
		if err := json.Unmarshal(first, &refetch); err != nil {
			return fmt.Errorf("decode first archive read: %w", err)
		}
		if refetch.Digest != archived.Digest || refetch.ProgressCount != 1 {
			return errors.New("archive read back does not match the sealed fact")
		}
		again, err := readRaw(client, http.MethodGet, server.URL+"/archives/"+archived.ID)
		if err != nil {
			return err
		}
		if !bytes.Equal(first, again) {
			return errors.New("archive fact is not immutable across reads")
		}

		// The specimen's plan listing must not silently drop the sealed plan:
		// the online items exclude it while an explicit locator discloses it.
		var listing planListingView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID+"/plans", nil, http.StatusOK, &listing); err != nil {
			return err
		}
		if len(listing.Items) != 0 {
			return errors.New("sealed plan must leave the online plan items")
		}
		if len(listing.Archived) != 1 || listing.Archived[0].ArchiveID != archived.ID ||
			listing.Archived[0].PlanID != plan.ID || listing.Archived[0].Status != "completed" ||
			listing.Archived[0].Digest != archived.Digest {
			return fmt.Errorf("listing does not locate the sealed archive: %+v", listing.Archived)
		}
		return nil

	case "archive-blocked-plan":
		// A completed plan whose specimen is still stabilized (release
		// unfinished) cannot be archived, and the failure must be read-only.
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		blocked, err := expectError(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/archive",
			contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"},
			http.StatusConflict,
		)
		if err != nil {
			return err
		}
		if blocked.Code != "release_unfinished" {
			return fmt.Errorf("expected release_unfinished, got %s", blocked.Code)
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return fmt.Errorf("blocked archive must leave the plan readable: %w", err)
		}
		if snapshot.Plan.Status != "completed" {
			return errors.New("blocked archive silently changed plan status")
		}

		// A draft plan is not terminal and cannot be archived.
		draftSpecimen, err := registerSpecimenWith(client, server.URL, "PPS-2026-002")
		if err != nil {
			return err
		}
		draft, err := createPlan(client, server.URL, draftSpecimen.ID)
		if err != nil {
			return err
		}
		draftBody, err := expectError(
			client,
			http.MethodPost,
			server.URL+"/plans/"+draft.ID+"/archive",
			contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"},
			http.StatusConflict,
		)
		if err != nil {
			return err
		}
		if draftBody.Code != "plan_not_archivable" {
			return fmt.Errorf("expected plan_not_archivable, got %s", draftBody.Code)
		}

		// A cancelled draft can be archived immediately (no references).
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+draft.ID+"/cancellation",
			contracts.CancelPlanRequest{CancelledBy: "curator-lin"},
			http.StatusOK,
			&planView{},
		); err != nil {
			return err
		}
		var cancelledArchive archiveView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+draft.ID+"/archive",
			contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"},
			http.StatusCreated,
			&cancelledArchive,
		); err != nil {
			return err
		}
		if cancelledArchive.Plan.Status != "cancelled" || cancelledArchive.ProgressCount != 0 {
			return errors.New("cancelled archive must seal zero referenced progress")
		}

		var cancelledListing planListingView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+draftSpecimen.ID+"/plans", nil, http.StatusOK, &cancelledListing); err != nil {
			return err
		}
		if len(cancelledListing.Items) != 0 || len(cancelledListing.Archived) != 1 ||
			cancelledListing.Archived[0].ArchiveID != cancelledArchive.ID {
			return fmt.Errorf("cancelled plan listing mismatch: %+v", cancelledListing)
		}

		// Before any archiving, the stabilized specimen's listing carries the
		// completed plan online and no archive reference.
		var onlineListing planListingView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID+"/plans", nil, http.StatusOK, &onlineListing); err != nil {
			return err
		}
		if len(onlineListing.Items) != 1 || onlineListing.Items[0].ID != plan.ID || len(onlineListing.Archived) != 0 {
			return fmt.Errorf("failed archive must not alter listing scope: %+v", onlineListing)
		}

		// Unknown ids fail as not found, while the specimen stays untouched.
		if _, err := expectError(client, http.MethodGet, server.URL+"/archives/does-not-exist", nil, http.StatusNotFound); err != nil {
			return err
		}
		var still specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &still); err != nil {
			return err
		}
		if still.Status != "stabilized" {
			return fmt.Errorf("blocked archive changed specimen state to %s", still.Status)
		}
		return nil

	case "archive-retention-window":
		// With a 24h retention window, a just-finished plan stays in the recent
		// query scope and cannot leave the online collection yet.
		specimen, plan, err := releasedSetup(client, server.URL)
		if err != nil {
			return err
		}
		retained, err := expectError(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/archive",
			contracts.ArchivePlanRequest{ArchivedBy: "registrar-gao"},
			http.StatusConflict,
		)
		if err != nil {
			return err
		}
		if retained.Code != "plan_still_in_recent_scope" {
			return fmt.Errorf("expected plan_still_in_recent_scope, got %s", retained.Code)
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return fmt.Errorf("retained plan must stay readable online: %w", err)
		}
		if snapshot.Plan.Status != "completed" {
			return errors.New("retention refusal silently changed plan status")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("retention refusal changed specimen state to %s", fetched.Status)
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

// releasedSetup drives a specimen through plan approval, one-hundred-percent
// preparation and the study release, the reference state required to archive a
// completed plan.
func releasedSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	specimen, plan, err := completedPlanSetup(client, baseURL)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	if err := recordProgress(client, baseURL, plan.ID, 100, "表面加固"); err != nil {
		return specimenView{}, planView{}, err
	}
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimen.ID+"/release",
		contracts.ReleaseRequest{
			Purpose:    "比较解剖研究",
			Recipient:  "地层研究组",
			ApprovedBy: "keeper-zhou",
		},
		http.StatusCreated,
		&releaseView{},
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, plan, nil
}

func expectError(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
) (errorBodyView, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errorBodyView{}, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return errorBodyView{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return errorBodyView{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return errorBodyView{}, err
	}
	if response.StatusCode != wantStatus {
		return errorBodyView{}, fmt.Errorf("request %s %s returned %d: %s", method, url, response.StatusCode, string(raw))
	}
	var decoded errorBodyView
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errorBodyView{}, fmt.Errorf("decode error body for %s %s: %w", method, url, err)
	}
	return decoded, nil
}

func readRaw(client *http.Client, method, url string) ([]byte, error) {
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
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
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request %s %s returned %d", method, url, response.StatusCode)
	}
	return raw, nil
}

func requestJSON(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != wantStatus {
		return fmt.Errorf("request %s %s returned %d: %s", method, url, response.StatusCode, string(raw))
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response for %s %s: %w", method, url, err)
	}
	return nil
}
