# Legal documents

PGDock keeps two kinds of legal document (V3 §7.3).

**Each person accepts:** the terms of use, the privacy notice and the
acceptable use policy. They are published together as one version
(Platform settings → Terms). Everyone accepts a new version at their next
visit, before they can do anything else.

**Each organisation accepts:** the service level agreement (SLA), the data
processing agreement (DPA), and the organisation's own order form, if it
has one. Each is versioned. An organisation **owner** accepts the version
in effect on the organisation's behalf. PGDock records who accepted, when,
and from which IP address.

## The defaults are templates

The first start publishes the following as version 1:

- the terms, privacy notice and AUP (see the [terms template](terms-template.md));
- an SLA with the commitment from V3 §2.7 and the support response
  targets ([support](support.md));
- a DPA listing PGDock's sub-processors.

Have a lawyer review all of them, then publish the company's own versions
before charging anyone (see the launch gate in the [admin runbook](admin-runbook.md)).

## Publishing

- **SLA and DPA:** Platform → Legal documents → **Publish a new version**.
  The form starts from the current text.
- **Order form:** Platform → Organisations → the org → **Order form**.
  Use it for terms or prices agreed with that organisation. Only its
  owners see it.
- **Terms, privacy, AUP:** Platform settings → Terms.

A new version replaces the old one for new acceptances. Organisations that
accepted the old one see it as outstanding again until an owner accepts
the new one. Platform → Legal documents lists every version and the
organisations that accepted it.

## Accepting

- **Organisation → Legal:** shows each document, its version, and whether
  it has been accepted. Owners have an **Accept** button.
- **Changing plan:** while something is outstanding, the plan change
  offers to accept it at the same time. In the API this is
  `accept_legal: true` on `POST /orgs/{org}/billing/plan`, and only owners
  may set it.
- **Billing:** owners of a paid organisation see a banner there while
  something is outstanding.

Accepting is not enforced: a paid plan works without it. The record is
evidence of what was agreed, and the banner prompts owners to accept.

The API:

| Endpoint | Who | What |
| --- | --- | --- |
| `GET /api/v1/legal` | Anyone | The SLA and DPA in effect |
| `GET /api/v1/terms` | Anyone | The terms, privacy notice and AUP |
| `GET /api/v1/orgs/{org}/legal` | Members | The documents in effect for the organisation, with its acceptances |
| `POST /api/v1/orgs/{org}/legal/{document_id}/accept` | Owners | Accept a document. Only the version in effect can be accepted (409 otherwise). |
| `GET`, `POST /api/v1/admin/legal`; `GET /api/v1/admin/legal/{id}` | Platform admins | List versions, publish a version, see who accepted one |
| `POST /api/v1/admin/orgs/{org}/order-form` | Platform admins | Publish an organisation's order form |
