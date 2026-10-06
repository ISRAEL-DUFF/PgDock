# Billing audit (M27)

V3 M27 asks for a billing audit before the Lagos launch: the ledger's
invariants, rounding, and VAT and WHT edge cases, reviewed with the
accountant. This is the engineering half: what was checked, what was
wrong and fixed, and the questions for the accountant. The accountant's
answers belong in the last section before launch.

## The invariants, checked continuously

`billing.Check` (Admin → Billing → Ledger check, `GET
/api/v1/admin/ledger/check`) used to check only that every transaction
balances. It now also checks that the ledger agrees with what it
records. Every billing test ends with it, so the whole suite runs with
these invariants:

| Invariant | What it catches |
| --- | --- |
| Every transaction balances; all debits equal all credits | A half-posted or hand-edited transaction |
| `invoice_ledger`: an issued invoice's transaction debits receivable (or credits credit balance) by its total and credits VAT payable by its VAT; a prepaid org's invoice was deducted in full | An invoice issued without its entries, or with different amounts |
| `invoice_arithmetic`: lines add up to the subtotal, VAT = round(subtotal × rate), total = subtotal + VAT | Amounts changed after rating, a rounding change |
| `invoice_allocations`: payments' allocations are within what the invoice records as paid; WHT deducted equals the allocations' WHT | A payment settling an invoice without a record of it |
| `invoice_outstanding`: payments and WHT never exceed the total; a paid invoice owes nothing; an open invoice owes something | Over-settlement, an open invoice dunning would chase for nothing |
| `credit_note_ledger`, `payment_ledger`: their transactions carry their amounts | A credit note or payment without its entries |
| `receivable`: each organisation's receivable equals what its open invoices still owe | Any drift between the ledger and the invoices |
| `account_sign`: no organisation's WHT receivable is negative | WHT evidenced twice |

## Findings

1. **A credit note on a partly paid invoice made the receivable
   negative** (fixed). The whole credit went to receivable even when less
   was owed; the excess now goes to the organisation's credit balance,
   where it pays the next invoice or can be refunded
   (`TestCreditNoteBeyondWhatIsOwed`).
2. **An invoice credited down to nothing stayed open** (fixed). It kept
   its `issued` or `partially_paid` status, so dunning counted the
   organisation as overdue and could have restricted or suspended it for
   a debt it didn't have. It is now settled (`paid`, or
   `paid_wht_pending` while deducted WHT awaits its certificate).
3. No other invariant broke across the existing scenarios: a month across
   a mid-month upgrade, partial, over- and duplicate payments, WHT
   shortfalls and certificates, refunds that reopen invoices, prepaid
   deductions and true-ups, billing mode switches, card retries, dunning,
   reconciliation, and both payment providers.

## Rounding and tax rules (as implemented)

- Amounts are integers in kobo. Unit prices and quantities are exact
  decimals (no floating point anywhere in rating).
- **Each invoice line** rounds once, half away from zero, from its exact
  quantity × unit price. Allowances are prorated by days, exactly.
- **VAT** (7.5% by default) is computed once on the invoice subtotal,
  rounded half away from zero; not per line.
- **WHT expected** (5% by default) is on the subtotal, excluding VAT, for
  organisations marked as deducting it. A transfer short by exactly the
  expected WHT on what is still owed is treated as WHT deducted, after
  any earlier part payments (`TestWHTAfterAPartialPayment`).
- **Credit notes** are entered before VAT; their VAT is at the invoice's
  rate, rounded on its own. The credits on an invoice, VAT included, can
  never exceed its total (`TestCreditNoteVATAndLimits`). Revenue is
  reversed across the invoice's product lines in proportion; the last
  line takes the rounding remainder.
- Prepaid deductions are daily, each rounded; the true-up at invoice
  issue brings the deductions to the invoice exactly.

## For the accountant

To answer before the Lagos launch. Record the answers here:

1. VAT once on the subtotal (not per line): acceptable for FIRS e-invoicing?
2. Credit-note VAT rounded per note: crediting an invoice in many small
   notes can move VAT by a kobo in total against crediting it at once.
   Acceptable, or should the last note true up the VAT?
3. WHT at 5% on the subtotal for all corporate customers: confirm the
   rate for services, and whether any customers (government agencies)
   use a different rate.
4. Credit beyond what is owed is held as credit balance and used on the
   next invoice, or refunded on request. Is it a liability to disclose
   at month end (the revenue dashboard shows it)?
5. Prepaid balances below zero after a true-up (usage beyond the funds)
   are owed by the customer: invoice them, or collect from the next
   top-up (the current behaviour)?
6. The monthly CSV exports (revenue, receivables by age, outstanding WHT,
   margins): enough for the books, or is a ledger export by account and
   month needed?

Answers: _pending_.
