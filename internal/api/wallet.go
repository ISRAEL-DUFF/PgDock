package api

import (
	"errors"
	"net/http"

	"github.com/israel-duff/xenibase/internal/api/gen"
	"github.com/israel-duff/xenibase/internal/billing"
	"github.com/israel-duff/xenibase/internal/store"
)

func toAPIQuote(q billing.FXQuote) gen.FXQuote {
	return gen.FXQuote{From: q.From, To: q.To, MarketRate: q.Market.String(), EffectiveRate: q.Effective.String(), SpreadBps: q.SpreadBPS,
		FetchedAt: q.FetchedAt, QuotedAt: q.QuotedAt, Sources: &q.Sources, Derived: &q.Derived}
}

func toAPIConsent(c *billing.Consent) *gen.DebitConsent {
	if c == nil {
		return nil
	}
	return &gen.DebitConsent{At: c.At, By: c.By, Version: c.Version, Current: c.Current}
}

// toAPIAccount is a bank account as the API shows it: the account's name is
// the name, without any instruction the provider put in front of it.
func toAPIAccount(va store.VirtualAccount) gen.VirtualAccount {
	return gen.VirtualAccount{Provider: va.Provider, AccountNumber: va.AccountNumber, BankName: va.BankName, AccountName: billing.CleanAccountName(va.AccountName)}
}

func toAPIWallet(v billing.WalletView) gen.Wallet {
	out := gen.Wallet{Available: v.Available, LedgerMinor: v.Ledger, OwedMinor: v.Owed, Terms: billing.DebitTerms, TermsVersion: billing.DebitTermsVersion,
		Consent: toAPIConsent(v.Consent)}
	if v.WalletID != "" {
		out.WalletId = &v.WalletID
	}
	if v.Balance != nil {
		out.BalanceMinor, out.PendingMinor = &v.Balance.Available, &v.Balance.Pending
	}
	if v.BalanceError != "" {
		out.BalanceError = &v.BalanceError
	}
	if v.Account != nil {
		a := toAPIAccount(*v.Account)
		out.Account = &a
	}
	accounts := make([]gen.VirtualAccount, 0, len(v.Accounts))
	for _, va := range v.Accounts {
		accounts = append(accounts, toAPIAccount(va))
	}
	out.Accounts = &accounts
	if v.Rate != nil {
		fx := toAPIQuote(*v.Rate)
		out.Rate = &fx
	}
	return out
}

// GetOrgWallet implements GET /api/v1/orgs/{org}/billing/wallet.
func (s *Server) GetOrgWallet(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	v, err := bs.Wallet(r.Context(), org)
	if err != nil {
		s.billingError(w, "wallet", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIWallet(v))
}

// CreateOrgWallet implements POST /api/v1/orgs/{org}/billing/wallet.
func (s *Server) CreateOrgWallet(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.WalletSetup
	if !decodeJSON(w, r, &req) {
		return
	}
	in := billing.WalletSetup{BusinessName: req.BusinessName, Email: req.Email, BVN: req.Bvn}
	if req.Phone != nil {
		in.Phone = *req.Phone
	}
	if req.Tin != nil {
		in.TIN = *req.Tin
	}
	if req.CacNumber != nil {
		in.CACNumber = *req.CacNumber
	}
	// The BVN is never logged or audited: only that a wallet was opened.
	id, err := bs.EnsureWallet(r.Context(), org, in)
	if err != nil {
		if errors.Is(err, billing.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "unavailable", "the wallet provider is unavailable; try again shortly")
			return
		}
		s.billingError(w, "wallet", err)
		return
	}
	auditFrom(r.Context()).set("wallet", id)
	if _, err := bs.EnsureTransferAccount(r.Context(), org); err != nil {
		// The wallet exists; the account can be asked for again.
		s.log.Warn("bank account for a new wallet", "org", org, "err", err)
	}
	v, err := bs.Wallet(r.Context(), org)
	if err != nil {
		s.billingError(w, "wallet", err)
		return
	}
	writeJSON(w, http.StatusCreated, toAPIWallet(v))
}

// AcceptDebitTerms implements POST /api/v1/orgs/{org}/billing/debit-consent.
func (s *Server) AcceptDebitTerms(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	var req gen.DebitConsentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	auditFrom(r.Context()).set("terms_version", req.TermsVersion)
	c, err := bs.AcceptDebitTerms(r.Context(), org, userID(r.Context()), req.TermsVersion)
	if err != nil {
		s.billingError(w, "debit consent", err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIConsent(c))
}

// WithdrawDebitTerms implements DELETE /api/v1/orgs/{org}/billing/debit-consent.
func (s *Server) WithdrawDebitTerms(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	if err := bs.WithdrawDebitTerms(r.Context(), org); err != nil {
		s.billingError(w, "debit consent", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PayInvoiceFromWallet implements POST /api/v1/orgs/{org}/billing/invoices/{invoice_id}/pay-from-wallet.
func (s *Server) PayInvoiceFromWallet(w http.ResponseWriter, r *http.Request, org gen.OrgID, id gen.InvoiceID) {
	bs := s.billingSvc(w)
	if bs == nil {
		return
	}
	auditFrom(r.Context()).target("invoice", id.String())
	out, err := bs.PayInvoiceFromWallet(r.Context(), org, id)
	if err != nil {
		s.billingError(w, "pay from wallet", err)
		return
	}
	au := auditFrom(r.Context())
	au.set("succeeded", out.Succeeded)
	au.set("paid_minor", out.Paid)
	res := gen.WalletCharge{Succeeded: out.Succeeded, Skipped: out.Skipped, Unavailable: out.Unavailable, PaidMinor: out.Paid}
	if out.Message != "" {
		res.Message = &out.Message
	}
	writeJSON(w, http.StatusOK, res)
}
