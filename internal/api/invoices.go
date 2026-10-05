package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
)

func toAPIInvoice(i store.Invoice, orgName *string) gen.Invoice {
	return gen.Invoice{
		Id: i.ID, OrgId: i.OrgID, OrgName: orgName, Number: i.Number,
		PeriodStart: openapi_types.Date{Time: i.PeriodStart.Time}, PeriodEnd: openapi_types.Date{Time: i.PeriodEnd.Time},
		Status: gen.InvoiceStatus(i.Status), Held: i.Held, HoldReason: i.HoldReason,
		SubtotalMinor: i.SubtotalMinor, VatMinor: i.VatMinor, TotalMinor: i.TotalMinor, WhtExpectedMinor: i.WhtExpectedMinor,
		PaidMinor: i.PaidMinor, WhtDeductedMinor: i.WhtDeductedMinor, WhtEvidencedAt: i.WhtEvidencedAt,
		VatRate: billing.DecFromNumeric(i.VatRate).String(), PriceBookVersion: int(i.PriceBookVersion),
		IssuedAt: i.IssuedAt, DueAt: i.DueAt, PaidAt: i.PaidAt, CreatedAt: i.CreatedAt,
	}
}

func toAPICreditNote(c store.CreditNote) gen.CreditNote {
	return gen.CreditNote{Id: c.ID, InvoiceId: c.InvoiceID, Number: c.Number, AmountMinor: c.AmountMinor, VatMinor: c.VatMinor, Reason: c.Reason, IssuedAt: c.IssuedAt}
}

func (s *Server) invoiceDetail(w http.ResponseWriter, r *http.Request, inv store.Invoice) {
	q := store.New(s.db)
	lines, err := q.InvoiceLines(r.Context(), inv.ID)
	if err != nil {
		s.internalError(w, "invoice lines", err)
		return
	}
	cns, err := q.InvoiceCreditNotes(r.Context(), inv.ID)
	if err != nil {
		s.internalError(w, "credit notes", err)
		return
	}
	out := gen.InvoiceDetail{Invoice: toAPIInvoice(inv, nil), Lines: []gen.InvoiceLine{}, CreditNotes: []gen.CreditNote{}}
	for _, l := range lines {
		pid := l.ProjectID
		out.Lines = append(out.Lines, gen.InvoiceLine{
			Kind: gen.InvoiceLineKind(l.Kind), Description: l.Description, ProjectId: pid, Metric: l.Metric,
			Quantity: billing.DecFromNumeric(l.Quantity).String(), UnitPrice: billing.DecFromNumeric(l.UnitPriceMinor).String(), Amount: l.AmountMinor,
		})
	}
	for _, c := range cns {
		out.CreditNotes = append(out.CreditNotes, toAPICreditNote(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) invoicePDF(w http.ResponseWriter, r *http.Request, inv store.Invoice) {
	q := store.New(s.db)
	lines, err := q.InvoiceLines(r.Context(), inv.ID)
	if err != nil {
		s.internalError(w, "invoice lines", err)
		return
	}
	cns, err := q.InvoiceCreditNotes(r.Context(), inv.ID)
	if err != nil {
		s.internalError(w, "credit notes", err)
		return
	}
	if inv.Status == billing.StatusDraft {
		// A draft shows what it will say: the details as they are now.
		if set, err := s.billing.Settings(r.Context()); err == nil {
			inv.Seller = mustMarshal(set.Seller)
		}
		if a, err := s.billing.Account(r.Context(), inv.OrgID); err == nil {
			o, _ := store.New(s.db).GetOrg(r.Context(), inv.OrgID)
			inv.BillTo = mustMarshal(billing.BillTo{OrgName: o.Name, LegalName: a.LegalName, Address: a.Address, TIN: a.Tin, VATRegistered: a.VatRegistered})
		}
	}
	pdf, err := billing.InvoicePDF(inv, lines, cns)
	if err != nil {
		s.internalError(w, "invoice PDF", err)
		return
	}
	name := "draft-" + inv.ID.String()
	if inv.Number != nil {
		name = *inv.Number
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, name))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// ListOrgInvoices implements GET /api/v1/orgs/{org}/billing/invoices.
func (s *Server) ListOrgInvoices(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	rows, err := store.New(s.db).ListOrgInvoices(r.Context(), store.ListOrgInvoicesParams{OrgID: org, Lim: 120})
	if err != nil {
		s.internalError(w, "invoices", err)
		return
	}
	out := gen.InvoiceList{Items: []gen.Invoice{}}
	for _, i := range rows {
		out.Items = append(out.Items, toAPIInvoice(i, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

// orgInvoice is an issued invoice of org; drafts are the admin's until issued.
func (s *Server) orgInvoice(w http.ResponseWriter, r *http.Request, org gen.OrgID, id gen.InvoiceID) (store.Invoice, bool) {
	inv, err := store.New(s.db).GetOrgInvoice(r.Context(), store.GetOrgInvoiceParams{ID: id, OrgID: org})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && inv.Status == billing.StatusDraft) {
		writeError(w, http.StatusNotFound, "not_found", "invoice not found")
		return inv, false
	}
	if err != nil {
		s.internalError(w, "invoice", err)
		return inv, false
	}
	return inv, true
}

// GetOrgInvoice implements GET /api/v1/orgs/{org}/billing/invoices/{invoice_id}.
func (s *Server) GetOrgInvoice(w http.ResponseWriter, r *http.Request, org gen.OrgID, id gen.InvoiceID) {
	if inv, ok := s.orgInvoice(w, r, org, id); ok {
		s.invoiceDetail(w, r, inv)
	}
}

// GetOrgInvoicePdf implements GET /api/v1/orgs/{org}/billing/invoices/{invoice_id}/pdf.
func (s *Server) GetOrgInvoicePdf(w http.ResponseWriter, r *http.Request, org gen.OrgID, id gen.InvoiceID) {
	if s.billingSvc(w) == nil {
		return
	}
	if inv, ok := s.orgInvoice(w, r, org, id); ok {
		s.invoicePDF(w, r, inv)
	}
}

// ---- Admin --------------------------------------------------------------------

func parsePeriod(w http.ResponseWriter, p string) (time.Time, bool) {
	t, err := time.Parse("2006-01", p)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "period is YYYY-MM")
		return t, false
	}
	return t, true
}

// AdminListInvoices implements GET /api/v1/admin/invoices.
func (s *Server) AdminListInvoices(w http.ResponseWriter, r *http.Request, params gen.AdminListInvoicesParams) {
	arg := store.ListInvoicesParams{Lim: 500}
	if params.Status != nil {
		st := string(*params.Status)
		arg.Status = &st
	}
	if params.Period != nil {
		t, ok := parsePeriod(w, *params.Period)
		if !ok {
			return
		}
		arg.PeriodStart = pgtype.Date{Time: t, Valid: true}
	}
	rows, err := store.New(s.db).ListInvoices(r.Context(), arg)
	if err != nil {
		s.internalError(w, "invoices", err)
		return
	}
	out := gen.InvoiceList{Items: []gen.Invoice{}}
	for _, row := range rows {
		name := row.OrgName
		out.Items = append(out.Items, toAPIInvoice(store.Invoice{
			ID: row.ID, OrgID: row.OrgID, Number: row.Number, PeriodStart: row.PeriodStart, PeriodEnd: row.PeriodEnd, Status: row.Status,
			Held: row.Held, HoldReason: row.HoldReason, SubtotalMinor: row.SubtotalMinor, VatMinor: row.VatMinor, TotalMinor: row.TotalMinor,
			WhtExpectedMinor: row.WhtExpectedMinor, VatRate: row.VatRate, PriceBookVersion: row.PriceBookVersion,
			IssuedAt: row.IssuedAt, DueAt: row.DueAt, PaidAt: row.PaidAt, CreatedAt: row.CreatedAt,
		}, &name))
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminDraftInvoices implements POST /api/v1/admin/invoices/draft.
func (s *Server) AdminDraftInvoices(w http.ResponseWriter, r *http.Request) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AdminDraftInvoicesJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	month, ok := parsePeriod(w, req.Period)
	if !ok {
		return
	}
	au := auditFrom(r.Context())
	au.set("period", req.Period)
	n := 0
	if req.OrgId != nil {
		au.target("org", req.OrgId.String())
		inv, err := bs.Draft(r.Context(), *req.OrgId, month)
		if err != nil {
			s.billingError(w, "draft", err)
			return
		}
		if inv != nil && inv.Status == billing.StatusDraft {
			n = 1
		}
	} else {
		var err error
		if n, err = bs.DraftAll(r.Context(), month); err != nil {
			s.billingError(w, "drafts", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"drafts": n})
}

func (s *Server) adminInvoice(w http.ResponseWriter, r *http.Request, id gen.InvoiceID) (store.Invoice, bool) {
	inv, err := store.New(s.db).GetInvoice(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "invoice not found")
		return inv, false
	}
	if err != nil {
		s.internalError(w, "invoice", err)
		return inv, false
	}
	return inv, true
}

// AdminGetInvoice implements GET /api/v1/admin/invoices/{invoice_id}.
func (s *Server) AdminGetInvoice(w http.ResponseWriter, r *http.Request, id gen.InvoiceID) {
	if inv, ok := s.adminInvoice(w, r, id); ok {
		s.invoiceDetail(w, r, inv)
	}
}

// AdminGetInvoicePdf implements GET /api/v1/admin/invoices/{invoice_id}/pdf.
func (s *Server) AdminGetInvoicePdf(w http.ResponseWriter, r *http.Request, id gen.InvoiceID) {
	if s.billingSvc(w) == nil {
		return
	}
	if inv, ok := s.adminInvoice(w, r, id); ok {
		s.invoicePDF(w, r, inv)
	}
}

// AdminHoldInvoice implements POST /api/v1/admin/invoices/{invoice_id}/hold.
func (s *Server) AdminHoldInvoice(w http.ResponseWriter, r *http.Request, id gen.InvoiceID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AdminHoldInvoiceJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	au := auditFrom(r.Context())
	au.target("invoice", id.String())
	au.set("held", req.Held)
	au.set("reason", req.Reason)
	inv, err := bs.Hold(r.Context(), id, req.Held, req.Reason)
	if err != nil {
		s.billingError(w, "hold", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIInvoice(inv, nil))
}

// AdminIssueInvoice implements POST /api/v1/admin/invoices/{invoice_id}/issue.
func (s *Server) AdminIssueInvoice(w http.ResponseWriter, r *http.Request, id gen.InvoiceID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	au := auditFrom(r.Context())
	au.target("invoice", id.String())
	inv, err := bs.Issue(r.Context(), id, userID(r.Context()))
	if err != nil {
		s.billingError(w, "issue", err)
		return
	}
	au.set("number", inv.Number)
	au.set("total_minor", inv.TotalMinor)
	writeJSON(w, http.StatusOK, toAPIInvoice(inv, nil))
}

// AdminCreateCreditNote implements POST /api/v1/admin/invoices/{invoice_id}/credit-notes.
func (s *Server) AdminCreateCreditNote(w http.ResponseWriter, r *http.Request, id gen.InvoiceID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AdminCreateCreditNoteJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	au := auditFrom(r.Context())
	au.target("invoice", id.String())
	au.set("amount_minor", req.AmountMinor)
	au.set("reason", req.Reason)
	cn, err := bs.CreditNote(r.Context(), id, req.AmountMinor, req.Reason, userID(r.Context()))
	if err != nil {
		s.billingError(w, "credit note", err)
		return
	}
	au.set("number", cn.Number)
	writeJSON(w, http.StatusCreated, toAPICreditNote(cn))
}

// AdminLedgerCheck implements GET /api/v1/admin/ledger/check.
func (s *Server) AdminLedgerCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := store.New(s.db)
	probs, err := billing.Check(ctx, s.db)
	if err != nil {
		s.internalError(w, "ledger check", err)
		return
	}
	t, err := q.LedgerTotals(ctx)
	if err != nil {
		s.internalError(w, "ledger check", err)
		return
	}
	accts, err := q.LedgerAccountTotals(ctx, nil)
	if err != nil {
		s.internalError(w, "ledger check", err)
		return
	}
	type problem struct {
		TxnID   openapi_types.UUID `json:"txn_id"`
		Debits  int64              `json:"debits_minor"`
		Credits int64              `json:"credits_minor"`
	}
	type account struct {
		Account string `json:"account"`
		Balance int64  `json:"balance_minor"`
	}
	raw := struct {
		Balanced     bool      `json:"balanced"`
		Transactions int       `json:"transactions"`
		Debits       int64     `json:"debits_minor"`
		Credits      int64     `json:"credits_minor"`
		Problems     []problem `json:"problems"`
		Accounts     []account `json:"accounts"`
	}{Balanced: len(probs) == 0, Transactions: int(t.Txns), Debits: t.Debits, Credits: t.Credits, Problems: []problem{}, Accounts: []account{}}
	for _, p := range probs {
		raw.Problems = append(raw.Problems, problem{p.Txn, p.Debits, p.Credits})
	}
	for _, a := range accts {
		raw.Accounts = append(raw.Accounts, account{a.Account, a.Balance})
	}
	out, err := convert[gen.LedgerCheck](raw)
	if err != nil {
		s.internalError(w, "ledger check", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PreviewPriceBook implements POST /api/v1/admin/price-books/{version}/preview.
func (s *Server) PreviewPriceBook(w http.ResponseWriter, r *http.Request, version gen.PriceBookVersion) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.PreviewPriceBookJSONBody
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	month := billing.MonthStart(time.Now()).AddDate(0, -1, 0)
	if req.Period != nil {
		var ok bool
		if month, ok = parsePeriod(w, *req.Period); !ok {
			return
		}
	}
	b, err := bs.GetBook(r.Context(), int32(version))
	if err != nil {
		s.billingError(w, "price book", err)
		return
	}
	rows, err := bs.PreviewPrices(r.Context(), b.Prices, month)
	if err != nil {
		s.billingError(w, "preview", err)
		return
	}
	type item struct {
		OrgID          openapi_types.UUID `json:"org_id"`
		OrgName        string             `json:"org_name"`
		Plan           string             `json:"plan"`
		CurrentMinor   int64              `json:"current_minor"`
		ProjectedMinor int64              `json:"projected_minor"`
	}
	out := struct {
		Period              string `json:"period"`
		CurrentTotalMinor   int64  `json:"current_total_minor"`
		ProjectedTotalMinor int64  `json:"projected_total_minor"`
		Items               []item `json:"items"`
	}{Period: month.Format("2006-01"), Items: []item{}}
	for _, p := range rows {
		out.Items = append(out.Items, item{p.OrgID, p.OrgName, p.Plan, p.Current, p.Projected})
		out.CurrentTotalMinor += p.Current
		out.ProjectedTotalMinor += p.Projected
	}
	writeJSON(w, http.StatusOK, out)
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
