package api

import (
	"bytes"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
)

func toAPIPayment(p store.Payment, orgName *string) gen.Payment {
	return gen.Payment{
		Id: p.ID, OrgId: p.OrgID, OrgName: orgName, Provider: p.Provider, Channel: p.Channel, ProviderRef: p.ProviderRef,
		AmountMinor: p.AmountMinor, FeeMinor: p.FeeMinor, RefundedMinor: p.RefundedMinor, Note: p.Note, ReceivedAt: p.ReceivedAt,
	}
}

func toAPIMethod(m store.PaymentMethod) gen.PaymentMethod {
	out := gen.PaymentMethod{Id: m.ID, Provider: m.Provider, Kind: gen.PaymentMethodKind(m.Kind), Brand: m.Brand, Last4: m.Last4,
		LimitMinor: m.LimitMinor, IsDefault: m.IsDefault}
	if m.ExpMonth != nil {
		v := int(*m.ExpMonth)
		out.ExpMonth = &v
	}
	if m.ExpYear != nil {
		v := int(*m.ExpYear)
		out.ExpYear = &v
	}
	return out
}

// StartCheckout implements POST /api/v1/orgs/{org}/billing/checkout.
func (s *Server) StartCheckout(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.StartCheckoutJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	c := billing.CheckoutStart{OrgID: org, Channel: string(req.Channel), Purpose: string(req.Purpose), InvoiceID: req.InvoiceId,
		RedirectURL: s.publicBase + "/org/billing?org=" + org.String()}
	if req.AmountMinor != nil {
		c.AmountMinor = *req.AmountMinor
	}
	if req.MandateLimitMinor != nil {
		c.MandateLimitMinor = *req.MandateLimitMinor
	}
	au := auditFrom(r.Context())
	au.set("channel", c.Channel)
	au.set("purpose", c.Purpose)
	in, err := bs.StartCheckout(r.Context(), c)
	if errors.Is(err, billing.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the payment provider is unavailable; try another way to pay")
		return
	}
	if err != nil {
		s.billingError(w, "checkout", err)
		return
	}
	au.set("reference", in.Reference)
	au.set("amount_minor", in.AmountMinor)
	writeJSON(w, http.StatusCreated, map[string]any{"reference": in.Reference, "checkout_url": in.CheckoutUrl, "amount_minor": in.AmountMinor})
}

// OrgVirtualAccount implements POST /api/v1/orgs/{org}/billing/virtual-account.
func (s *Server) OrgVirtualAccount(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	va, err := bs.EnsureVirtualAccount(r.Context(), org)
	if err != nil {
		if errors.Is(err, billing.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "unavailable", "bank transfer accounts can't be issued right now; try again later or pay by card")
			return
		}
		s.billingError(w, "virtual account", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.VirtualAccount{Provider: va.Provider, AccountNumber: va.AccountNumber, BankName: va.BankName, AccountName: va.AccountName})
}

// ListPaymentMethods implements GET /api/v1/orgs/{org}/billing/payment-methods.
func (s *Server) ListPaymentMethods(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	rows, err := store.New(s.db).ListPaymentMethods(r.Context(), org)
	if err != nil {
		s.internalError(w, "payment methods", err)
		return
	}
	out := struct {
		Items []gen.PaymentMethod `json:"items"`
	}{Items: []gen.PaymentMethod{}}
	for _, m := range rows {
		out.Items = append(out.Items, toAPIMethod(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// RemovePaymentMethod implements DELETE /api/v1/orgs/{org}/billing/payment-methods/{method_id}.
func (s *Server) RemovePaymentMethod(w http.ResponseWriter, r *http.Request, org gen.OrgID, id gen.MethodID) {
	q := store.New(s.db)
	m, err := q.GetOrgPaymentMethod(r.Context(), store.GetOrgPaymentMethodParams{ID: id, OrgID: org})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && m.Status != "active") {
		writeError(w, http.StatusNotFound, "not_found", "payment method not found")
		return
	}
	if err != nil {
		s.internalError(w, "payment method", err)
		return
	}
	auditFrom(r.Context()).target("payment_method", id.String())
	if m.Kind == "mandate" && m.ProviderRef != nil && s.billing != nil {
		if p, ok := s.billing.Provider(m.Provider); ok {
			if err := p.RevokeMandate(r.Context(), *m.ProviderRef); err != nil && !errors.Is(err, billing.ErrUnsupported) {
				s.billingError(w, "revoke mandate", err)
				return
			}
		}
	}
	if err := q.SetPaymentMethodStatus(r.Context(), store.SetPaymentMethodStatusParams{ID: id, Status: "removed"}); err != nil {
		s.internalError(w, "payment method", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetDefaultPaymentMethod implements POST /api/v1/orgs/{org}/billing/payment-methods/{method_id}/default.
func (s *Server) SetDefaultPaymentMethod(w http.ResponseWriter, r *http.Request, org gen.OrgID, id gen.MethodID) {
	q := store.New(s.db)
	if m, err := q.GetOrgPaymentMethod(r.Context(), store.GetOrgPaymentMethodParams{ID: id, OrgID: org}); err != nil || m.Status != "active" {
		writeError(w, http.StatusNotFound, "not_found", "payment method not found")
		return
	}
	auditFrom(r.Context()).target("payment_method", id.String())
	if err := q.SetDefaultPaymentMethod(r.Context(), store.SetDefaultPaymentMethodParams{ID: id, OrgID: org}); err != nil {
		s.internalError(w, "payment method", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetAutoTopup implements PUT /api/v1/orgs/{org}/billing/auto-topup.
func (s *Server) SetAutoTopup(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AutoTopup
	if !decodeJSON(w, r, &req) {
		return
	}
	au := auditFrom(r.Context())
	au.set("below_minor", req.BelowMinor)
	au.set("amount_minor", req.AmountMinor)
	if err := bs.SetAutoTopup(r.Context(), org, &billing.AutoTopup{BelowMinor: req.BelowMinor, AmountMinor: req.AmountMinor}); err != nil {
		s.billingError(w, "auto top-up", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ClearAutoTopup implements DELETE /api/v1/orgs/{org}/billing/auto-topup.
func (s *Server) ClearAutoTopup(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	if err := bs.SetAutoTopup(r.Context(), org, nil); err != nil {
		s.billingError(w, "auto top-up", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListOrgPayments implements GET /api/v1/orgs/{org}/billing/payments.
func (s *Server) ListOrgPayments(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	rows, err := store.New(s.db).ListOrgPayments(r.Context(), store.ListOrgPaymentsParams{OrgID: org, Lim: 200})
	if err != nil {
		s.internalError(w, "payments", err)
		return
	}
	out := gen.PaymentList{Items: []gen.Payment{}}
	for _, p := range rows {
		out.Items = append(out.Items, toAPIPayment(p, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetPaymentReceipt implements GET /api/v1/orgs/{org}/billing/payments/{payment_id}/receipt.
func (s *Server) GetPaymentReceipt(w http.ResponseWriter, r *http.Request, org gen.OrgID, id gen.PaymentID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	q := store.New(s.db)
	p, err := q.GetPayment(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.OrgID != org) {
		writeError(w, http.StatusNotFound, "not_found", "payment not found")
		return
	}
	if err != nil {
		s.internalError(w, "receipt", err)
		return
	}
	allocs, err := q.PaymentAllocations(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, "receipt", err)
		return
	}
	o, err := q.GetOrg(r.Context(), org)
	if err != nil {
		s.internalError(w, "receipt", err)
		return
	}
	set, err := bs.Settings(r.Context())
	if err != nil {
		s.internalError(w, "receipt", err)
		return
	}
	pdf, err := billing.ReceiptPDF(p, allocs, o.Name, set.Seller)
	if err != nil {
		s.internalError(w, "receipt", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="receipt-`+p.ReceivedAt.UTC().Format("20060102")+`-`+p.ID.String()[:8]+`.pdf"`)
	_, _ = w.Write(pdf)
}

func (s *Server) uploadWHT(w http.ResponseWriter, r *http.Request, org, invoice openapi_types.UUID, filename string) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	auditFrom(r.Context()).target("invoice", invoice.String())
	c, err := bs.UploadWHTCertificate(r.Context(), org, invoice, filename, http.MaxBytesReader(w, r.Body, billing.MaxDocumentBytes+1), userID(r.Context()))
	if err != nil {
		s.billingError(w, "WHT credit note", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": c.ID, "filename": c.Filename, "size_bytes": c.SizeBytes})
}

// UploadWhtCertificate implements POST /api/v1/orgs/{org}/billing/invoices/{invoice_id}/wht-certificate.
func (s *Server) UploadWhtCertificate(w http.ResponseWriter, r *http.Request, org gen.OrgID, id gen.InvoiceID, params gen.UploadWhtCertificateParams) {
	s.uploadWHT(w, r, org, id, params.Filename)
}

// ---- Admin --------------------------------------------------------------------

// AdminListPayments implements GET /api/v1/admin/payments.
func (s *Server) AdminListPayments(w http.ResponseWriter, r *http.Request, params gen.AdminListPaymentsParams) {
	to := time.Now().Add(time.Hour)
	from := to.AddDate(0, -3, 0)
	if params.From != nil {
		from = *params.From
	}
	if params.To != nil {
		to = *params.To
	}
	rows, err := store.New(s.db).ListPayments(r.Context(), store.ListPaymentsParams{Provider: params.Provider, FromTs: from, ToTs: to, Lim: 1000})
	if err != nil {
		s.internalError(w, "payments", err)
		return
	}
	out := gen.PaymentList{Items: []gen.Payment{}}
	for _, p := range rows {
		name := p.OrgName
		out.Items = append(out.Items, toAPIPayment(store.Payment{
			ID: p.ID, OrgID: p.OrgID, Provider: p.Provider, Channel: p.Channel, ProviderRef: p.ProviderRef, AmountMinor: p.AmountMinor,
			FeeMinor: p.FeeMinor, RefundedMinor: p.RefundedMinor, Note: p.Note, ReceivedAt: p.ReceivedAt,
		}, &name))
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminRecordPayment implements POST /api/v1/admin/payments.
func (s *Server) AdminRecordPayment(w http.ResponseWriter, r *http.Request) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AdminRecordPaymentJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	m := billing.ManualPayment{OrgID: req.OrgId, AmountMinor: req.AmountMinor, Reference: req.Reference, InvoiceID: req.InvoiceId, By: userID(r.Context())}
	if req.Note != nil {
		m.Note = *req.Note
	}
	if req.Topup != nil {
		m.Topup = *req.Topup
	}
	if req.ReceivedAt != nil {
		m.ReceivedAt = *req.ReceivedAt
	}
	if req.ProofKey != nil {
		m.ProofKey = *req.ProofKey
	}
	au := auditFrom(r.Context())
	au.target("org", req.OrgId.String())
	au.set("amount_minor", req.AmountMinor)
	au.set("reference", req.Reference)
	st, err := bs.RecordManual(r.Context(), m)
	if err != nil {
		s.billingError(w, "manual payment", err)
		return
	}
	if st.Duplicate {
		writeError(w, http.StatusConflict, "conflict", "a payment with this reference is already recorded")
		return
	}
	writeJSON(w, http.StatusCreated, toAPIPayment(st.Payment, nil))
}

// AdminUploadBillingDocument implements POST /api/v1/admin/billing/documents.
func (s *Server) AdminUploadBillingDocument(w http.ResponseWriter, r *http.Request, params gen.AdminUploadBillingDocumentParams) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	key, err := bs.StoreProof(r.Context(), params.OrgId, params.Filename, http.MaxBytesReader(w, r.Body, billing.MaxDocumentBytes+1))
	if err != nil {
		s.billingError(w, "document", err)
		return
	}
	auditFrom(r.Context()).target("org", params.OrgId.String())
	writeJSON(w, http.StatusCreated, map[string]string{"key": key})
}

// AdminRefundPayment implements POST /api/v1/admin/payments/{payment_id}/refund.
func (s *Server) AdminRefundPayment(w http.ResponseWriter, r *http.Request, id gen.PaymentID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AdminRefundPaymentJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	au := auditFrom(r.Context())
	au.target("payment", id.String())
	au.set("amount_minor", req.AmountMinor)
	au.set("reason", req.Reason)
	rf, err := bs.Refund(r.Context(), id, req.AmountMinor, req.Reason, userID(r.Context()))
	if err != nil && rf.ID == [16]byte{} {
		s.billingError(w, "refund", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": rf.ID, "status": rf.Status, "amount_minor": rf.AmountMinor})
}

// AdminListPaymentEvents implements GET /api/v1/admin/payment-events.
func (s *Server) AdminListPaymentEvents(w http.ResponseWriter, r *http.Request, params gen.AdminListPaymentEventsParams) {
	var outcome *string
	if params.Outcome != nil {
		o := string(*params.Outcome)
		outcome = &o
	}
	rows, err := store.New(s.db).ListPaymentEvents(r.Context(), store.ListPaymentEventsParams{Outcome: outcome, Lim: 500})
	if err != nil {
		s.internalError(w, "payment events", err)
		return
	}
	out := struct {
		Items []gen.PaymentEvent `json:"items"`
	}{Items: []gen.PaymentEvent{}}
	for _, e := range rows {
		out.Items = append(out.Items, gen.PaymentEvent{Id: e.ID, Provider: e.Provider, ProviderEventId: e.ProviderEventID, Kind: e.Kind,
			OrgId: e.OrgID, ProviderRef: e.ProviderRef, AmountMinor: e.AmountMinor, Outcome: e.Outcome, Error: e.Error, ReceivedAt: e.ReceivedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminAttributePaymentEvent implements POST /api/v1/admin/payment-events/{event_id}/attribute.
func (s *Server) AdminAttributePaymentEvent(w http.ResponseWriter, r *http.Request, id int64) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AdminAttributePaymentEventJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	au := auditFrom(r.Context())
	au.target("org", req.OrgId.String())
	au.set("event_id", id)
	st, err := bs.AttributeEvent(r.Context(), id, req.OrgId, userID(r.Context()))
	if err != nil {
		s.billingError(w, "attribute", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIPayment(st.Payment, nil))
}

// AdminOutstandingWht implements GET /api/v1/admin/wht.
func (s *Server) AdminOutstandingWht(w http.ResponseWriter, r *http.Request, params gen.AdminOutstandingWhtParams) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	if params.Format != nil && *params.Format == "csv" {
		var buf bytes.Buffer
		if err := bs.WHTReceivableCSV(r.Context(), &buf); err != nil {
			s.internalError(w, "WHT export", err)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="wht-receivable-`+time.Now().UTC().Format("20060102")+`.csv"`)
		_, _ = w.Write(buf.Bytes())
		return
	}
	rows, err := bs.OutstandingWHT(r.Context())
	if err != nil {
		s.internalError(w, "WHT", err)
		return
	}
	out := struct {
		Items []gen.OutstandingWht `json:"items"`
	}{Items: []gen.OutstandingWht{}}
	for _, row := range rows {
		age := 0
		if row.PaidAt != nil {
			age = int(time.Since(*row.PaidAt).Hours() / 24)
		}
		out.Items = append(out.Items, gen.OutstandingWht{InvoiceId: row.ID, Number: row.Number, OrgId: row.OrgID, OrgName: row.OrgName,
			Tin: row.Tin, WhtMinor: row.WhtDeductedMinor, PaidAt: row.PaidAt, AgeDays: age})
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminUploadWhtCertificate implements POST /api/v1/admin/invoices/{invoice_id}/wht-certificate.
func (s *Server) AdminUploadWhtCertificate(w http.ResponseWriter, r *http.Request, id gen.InvoiceID, params gen.AdminUploadWhtCertificateParams) {
	inv, err := store.New(s.db).GetInvoice(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "invoice not found")
		return
	}
	if err != nil {
		s.internalError(w, "invoice", err)
		return
	}
	s.uploadWHT(w, r, inv.OrgID, id, params.Filename)
}

func toAPIReconciliations(rs []billing.Reconciliation) (any, error) {
	items, err := convert[[]gen.Reconciliation](rs)
	if items == nil {
		items = []gen.Reconciliation{}
	}
	return map[string]any{"items": items}, err
}

// AdminLastReconciliation implements GET /api/v1/admin/reconciliation.
func (s *Server) AdminLastReconciliation(w http.ResponseWriter, r *http.Request) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	rs, err := bs.LastReconciliation(r.Context())
	if err != nil {
		s.internalError(w, "reconciliation", err)
		return
	}
	out, err := toAPIReconciliations(rs)
	if err != nil {
		s.internalError(w, "reconciliation", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminRunReconciliation implements POST /api/v1/admin/reconciliation.
func (s *Server) AdminRunReconciliation(w http.ResponseWriter, r *http.Request) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AdminRunReconciliationJSONBody
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from, to := today.AddDate(0, 0, -1), today
	if req.From != nil {
		from = *req.From
	}
	if req.To != nil {
		to = *req.To
	}
	auditFrom(r.Context()).skip = true
	rs, err := bs.Reconcile(r.Context(), from, to)
	if err != nil && len(rs) == 0 {
		s.billingError(w, "reconciliation", err)
		return
	}
	out, err := toAPIReconciliations(rs)
	if err != nil {
		s.internalError(w, "reconciliation", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminSetGrace implements PUT /api/v1/admin/orgs/{org}/billing/grace.
func (s *Server) AdminSetGrace(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.AdminSetGraceJSONBody
	if !decodeJSON(w, r, &req) {
		return
	}
	au := auditFrom(r.Context())
	au.target("org", org.String())
	au.set("until", req.Until)
	if err := bs.ExtendGrace(r.Context(), org, req.Until); err != nil {
		s.billingError(w, "grace", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
