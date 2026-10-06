package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestLegalDocuments is M23's legal done-when: the SLA and DPA are public
// and versioned; an organisation's owner accepts the versions in effect
// (and its own order form), on the Legal page or with a plan change; a new
// version needs accepting again; the platform admin sees who accepted.
func TestLegalDocuments(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	org := "/api/v1/orgs/" + e.OrgID.String()

	// The defaults, public.
	if code, body := e.GetText("/api/v1/legal", nil, false); code != http.StatusOK || !strings.Contains(body, "Service level agreement") || !strings.Contains(body, "Data processing agreement") {
		t.Fatalf("public documents: %d %.200s", code, body)
	}
	if code, body := e.GetText("/api/v1/terms", nil, false); code != http.StatusOK || !strings.Contains(body, "aup_md") {
		t.Errorf("the AUP with the terms: %d", code)
	}

	var ol gen.OrgLegal
	if code := e.Do("GET", org+"/legal", nil, &ol); code != http.StatusOK || !ol.Outstanding || len(ol.Items) != 2 {
		t.Fatalf("org documents: %d %+v", code, ol)
	}

	// An admin of the organisation reads them but can't accept.
	if code := e.Do("POST", org+"/members", map[string]string{"email": "ada@example.com", "role": "admin"}, nil); code != http.StatusCreated {
		t.Fatalf("invite admin: %d", code)
	}
	admin := e.AcceptInvitation(e.MailToken("ada@example.com", "invitation"), "ada@example.com")
	sla := ol.Items[0].Document
	if code := admin.Do("POST", org+"/legal/"+sla.Id.String()+"/accept", nil, nil); code != http.StatusForbidden {
		t.Errorf("an admin accepted: %d", code)
	}
	if code := e.Do("POST", org+"/legal/"+sla.Id.String()+"/accept", nil, &ol); code != http.StatusOK || !ol.Outstanding || ol.Items[0].AcceptedAt == nil {
		t.Fatalf("accept the SLA: %d %+v", code, ol)
	}

	// An order form for this organisation, accepted with the upgrade
	// along with the DPA.
	var of gen.LegalDocument
	if code := e.Do("POST", "/api/v1/admin/orgs/"+e.OrgID.String()+"/order-form", gen.OrderFormPublish{Title: "Order form", BodyMd: "Team plan, annual, ₦1,200,000."}, &of); code != http.StatusCreated || of.Version != 1 {
		t.Fatalf("order form: %d %+v", code, of)
	}
	other := e.CreateOrg("Other")
	if code := e.Do("POST", "/api/v1/orgs/"+other.String()+"/legal/"+of.Id.String()+"/accept", nil, nil); code != http.StatusNotFound {
		t.Errorf("another org accepted this org's order form: %d", code)
	}
	if code := admin.Do("POST", org+"/billing/plan", gen.PlanChangeRequest{Plan: "pro", AcceptLegal: ptr(true)}, nil); code != http.StatusForbidden {
		t.Errorf("a non-owner accepted with a plan change: %d", code)
	}
	if code := e.Do("POST", org+"/billing/plan", gen.PlanChangeRequest{Plan: "pro", AcceptLegal: ptr(true)}, nil); code != http.StatusOK {
		t.Fatalf("upgrade accepting: %d", code)
	}
	if code := e.Do("GET", org+"/legal", nil, &ol); code != http.StatusOK || ol.Outstanding || len(ol.Items) != 3 {
		t.Fatalf("after the upgrade: %d %+v", code, ol)
	}

	// A new SLA: outstanding again; the old version can't be accepted.
	var sla2 gen.LegalDocument
	if code := e.Do("POST", "/api/v1/admin/legal", gen.LegalPublish{Kind: gen.LegalPublishKindSla, Title: "Service level agreement", BodyMd: "# SLA\n\n99.95%."}, &sla2); code != http.StatusCreated || sla2.Version != 2 {
		t.Fatalf("publish: %d %+v", code, sla2)
	}
	if code := e.Do("GET", org+"/legal", nil, &ol); !ol.Outstanding {
		t.Errorf("a new version isn't outstanding: %d", code)
	}
	if code := e.Do("POST", "/api/v1/orgs/"+other.String()+"/legal/"+sla.Id.String()+"/accept", nil, nil); code != http.StatusConflict {
		t.Errorf("accepted a superseded version: %d", code)
	}
	var detail gen.LegalDocumentDetail
	if code := e.Do("GET", "/api/v1/admin/legal/"+sla.Id.String(), nil, &detail); code != http.StatusOK || len(detail.Acceptances) != 1 || detail.Acceptances[0].OrgId != e.OrgID {
		t.Fatalf("acceptances: %d %+v", code, detail)
	}
	var versions gen.LegalVersionList
	if code := e.Do("GET", "/api/v1/admin/legal", nil, &versions); code != http.StatusOK || len(versions.Items) != 3 {
		t.Errorf("versions: %d %+v", code, versions)
	}
	if code := admin.Do("POST", "/api/v1/admin/legal", gen.LegalPublish{Kind: gen.LegalPublishKindDpa, Title: "x", BodyMd: "y"}, nil); code != http.StatusForbidden {
		t.Errorf("a member published: %d", code)
	}

	// The revenue dashboard counts the upgrade.
	var rev gen.Revenue
	if code := e.Do("GET", "/api/v1/admin/revenue?months=3", nil, &rev); code != http.StatusOK || len(rev.Months) != 3 {
		t.Fatalf("revenue: %d %+v", code, rev)
	}
	if m := rev.Months[2]; m.MrrMinor <= 0 || m.PayingOrgs != 1 {
		t.Errorf("this month: %+v", m)
	}
	code, csv := e.GetText("/api/v1/admin/revenue?format=csv", nil, true)
	if code != http.StatusOK || !strings.HasPrefix(csv, "month,mrr_ngn") {
		t.Errorf("CSV: %d %.80q", code, csv)
	}
	if code := admin.Do("GET", "/api/v1/admin/revenue", nil, nil); code != http.StatusForbidden {
		t.Errorf("a member read revenue: %d", code)
	}
}
