package billing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"

	"github.com/israel-duff/pgdock/internal/store"
)

// InvoicePDF renders an invoice (V3 §3.6): both parties' details, the
// lines, subtotal, VAT and total in naira, expected WHT, and any credit
// notes. Amounts show as "NGN": the PDF core fonts have no naira sign.
func InvoicePDF(inv store.Invoice, lines []store.InvoiceLine, credits []store.CreditNote) ([]byte, error) {
	var seller Seller
	var billTo BillTo
	_ = json.Unmarshal(inv.Seller, &seller)
	_ = json.Unmarshal(inv.BillTo, &billTo)

	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("") // UTF-8 to the core fonts' cp1252
	pdf.SetTitle(tr("Invoice "+deref(inv.Number)), false)
	pdf.SetAutoPageBreak(true, 15)
	pdf.AddPage()
	pdf.SetMargins(15, 15, 15)

	title := "INVOICE"
	if inv.Status == StatusDraft {
		title = "DRAFT INVOICE"
	}
	pdf.SetFont("Helvetica", "B", 18)
	pdf.CellFormat(100, 10, title, "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	pdf.CellFormat(80, 10, tr(deref(inv.Number)), "", 1, "R", false, 0, "")

	// Seller and customer.
	y := pdf.GetY() + 2
	block := func(x float64, heading string, rows ...string) {
		pdf.SetXY(x, y)
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(85, 5, heading, "", 2, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		for _, r := range rows {
			if strings.TrimSpace(r) != "" {
				pdf.MultiCell(85, 4.5, tr(r), "", "L", false)
				pdf.SetX(x)
			}
		}
	}
	block(15, "From", seller.LegalName, seller.Address, label("TIN", seller.TIN), label("VAT registration", seller.VATNumber), seller.Email)
	sellerEnd := pdf.GetY()
	cust := billTo.OrgName
	if billTo.LegalName != nil {
		cust = *billTo.LegalName
	}
	block(110, "Bill to", cust, ptr(billTo.Address), label("TIN", ptr(billTo.TIN)))
	pdf.SetY(max(sellerEnd, pdf.GetY()) + 4)

	// Dates.
	pdf.SetFont("Helvetica", "", 9)
	period := inv.PeriodStart.Time.Format("2 Jan 2006") + " - " + inv.PeriodEnd.Time.Format("2 Jan 2006")
	facts := [][2]string{{"Usage period", period}}
	if inv.IssuedAt != nil {
		facts = append(facts, [2]string{"Issued", inv.IssuedAt.UTC().Format("2 Jan 2006")})
	}
	if inv.DueAt != nil && inv.TotalMinor > 0 {
		facts = append(facts, [2]string{"Due", inv.DueAt.UTC().Format("2 Jan 2006")})
	}
	facts = append(facts, [2]string{"Status", strings.ReplaceAll(inv.Status, "_", " ")})
	for _, f := range facts {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(30, 5, f[0], "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		pdf.CellFormat(100, 5, tr(f[1]), "", 1, "L", false, 0, "")
	}
	pdf.Ln(4)

	// Lines.
	pdf.SetFillColor(238, 238, 238)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.CellFormat(100, 7, "Description", "B", 0, "L", true, 0, "")
	pdf.CellFormat(25, 7, "Quantity", "B", 0, "R", true, 0, "")
	pdf.CellFormat(25, 7, "Unit (kobo)", "B", 0, "R", true, 0, "")
	pdf.CellFormat(30, 7, "Amount", "B", 1, "R", true, 0, "")
	pdf.SetFont("Helvetica", "", 8.5)
	for _, l := range lines {
		desc := tr(l.Description)
		h := 5.0 * float64(max(1, len(pdf.SplitLines([]byte(desc), 98))))
		x, yy := pdf.GetX(), pdf.GetY()
		if yy+h > 280 {
			pdf.AddPage()
			x, yy = pdf.GetX(), pdf.GetY()
		}
		pdf.MultiCell(100, 5, desc, "", "L", false)
		pdf.SetXY(x+100, yy)
		pdf.CellFormat(25, 5, DecFromNumeric(l.Quantity).Round2(), "", 0, "R", false, 0, "")
		pdf.CellFormat(25, 5, DecFromNumeric(l.UnitPriceMinor).Round2(), "", 0, "R", false, 0, "")
		pdf.CellFormat(30, 5, Naira(l.AmountMinor), "", 1, "R", false, 0, "")
		pdf.SetY(yy + h)
	}
	pdf.Ln(2)
	total := func(name, value string, bold bool) {
		style := ""
		if bold {
			style = "B"
		}
		pdf.SetFont("Helvetica", style, 9.5)
		pdf.CellFormat(150, 6, tr(name), "", 0, "R", false, 0, "")
		pdf.CellFormat(30, 6, value, "", 1, "R", false, 0, "")
	}
	total("Subtotal", Naira(inv.SubtotalMinor), false)
	total(fmt.Sprintf("VAT at %s%%", DecFromNumeric(inv.VatRate).Mul(DecInt(100)).String()), Naira(inv.VatMinor), false)
	total("Total", Naira(inv.TotalMinor), true)
	if inv.WhtExpectedMinor > 0 {
		total("Withholding tax you may deduct", Naira(inv.WhtExpectedMinor), false)
		total("Payable after WHT", Naira(inv.TotalMinor-inv.WhtExpectedMinor), false)
	}
	if len(credits) > 0 {
		pdf.Ln(4)
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(180, 6, "Credit notes", "B", 1, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 8.5)
		for _, c := range credits {
			pdf.CellFormat(150, 5, tr(fmt.Sprintf("%s (%s): %s", c.Number, c.IssuedAt.UTC().Format("2 Jan 2006"), c.Reason)), "", 0, "L", false, 0, "")
			pdf.CellFormat(30, 5, "-"+Naira(c.AmountMinor+c.VatMinor), "", 1, "R", false, 0, "")
		}
	}
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "I", 8)
	pdf.MultiCell(180, 4, tr("Amounts in Nigerian naira (NGN). Usage is billed in arrears for the period above; the plan fee is billed in advance for the following month."), "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func label(name, v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return name + ": " + v
}

func ptr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ReceiptPDF renders a payment receipt (V3 §3.6 "Receipts for each
// payment").
func ReceiptPDF(p store.Payment, allocations []store.PaymentAllocationsRow, orgName string, seller Seller) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.SetTitle("Receipt "+p.ID.String(), false)
	pdf.AddPage()
	pdf.SetMargins(15, 15, 15)
	pdf.SetFont("Helvetica", "B", 18)
	pdf.CellFormat(100, 10, "RECEIPT", "", 0, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.CellFormat(80, 10, tr(seller.LegalName), "", 1, "R", false, 0, "")
	pdf.Ln(4)
	rows := [][2]string{
		{"Received from", orgName},
		{"Amount", Naira(p.AmountMinor)},
		{"Date", p.ReceivedAt.UTC().Format("2 January 2006 15:04 UTC")},
		{"Method", strings.ReplaceAll(p.Channel, "_", " ") + " (" + p.Provider + ")"},
		{"Reference", p.ProviderRef},
	}
	for _, r := range rows {
		pdf.SetFont("Helvetica", "B", 10)
		pdf.CellFormat(40, 7, r[0], "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		pdf.CellFormat(140, 7, tr(r[1]), "", 1, "L", false, 0, "")
	}
	pdf.Ln(4)
	var applied int64
	if len(allocations) > 0 {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(180, 6, "Applied to", "B", 1, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		for _, a := range allocations {
			line := deref(a.Number)
			if a.WhtMinor > 0 {
				line += fmt.Sprintf(" (and %s of WHT deducted)", Naira(a.WhtMinor))
			}
			pdf.CellFormat(150, 6, tr(line), "", 0, "L", false, 0, "")
			pdf.CellFormat(30, 6, Naira(a.AmountMinor), "", 1, "R", false, 0, "")
			applied += a.AmountMinor
		}
	}
	if credit := p.AmountMinor - applied; credit > 0 {
		pdf.SetFont("Helvetica", "", 9)
		pdf.CellFormat(150, 6, "Added to the credit balance", "", 0, "L", false, 0, "")
		pdf.CellFormat(30, 6, Naira(credit), "", 1, "R", false, 0, "")
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
