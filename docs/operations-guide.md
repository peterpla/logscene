# LogScene — Operations Guide

*Last updated: 06-07-2026 hh:mm*
*Stored in repo at `docs/operations-guide.md`*

This guide covers everything needed to run the LogScene business day-to-day: building
and releasing the app, generating and distributing license keys, managing revenue,
handling support, and keeping annual renewals on track. It assumes the app is already
built and shipping. For setting up the development environment, see
`docs/development-guide.md`.

---

## Overview of the Business

LogScene LLC (Texas single-member LLC, EIN assigned June 5, 2026) sells LogScene, a
Windows timelapse application, via Paddle (merchant of record). Customers purchase a
perpetual license and optionally an annual Upgrade Assurance subscription. Paddle
collects payment, handles all sales tax/VAT globally, and pays out net proceeds monthly.
The WordPress.com Business site at logscene.net hosts the public website, webhook
handler, and customer/license database. Email runs on Microsoft 365 Business Standard.

---

## Building and Releasing the App

### Regular build

```
make build
```

Produces `logscene.exe` with version and build date embedded from git tags.

### Tagging a release

Before building a release binary, tag the commit:

```
git tag v1.2.3
git push origin v1.2.3
```

Then `make build` — the embedded `Version` string will reflect the tag.

### Release types and customer impact

| Release type | Version example | Customer action required | New license key? |
|---|---|---|---|
| Patch | v1.0.1 | Download and install | No |
| Minor | v1.1.0 | Download and install | No |
| Major | v2.0.0 | Active Upgrade Assurance: key emailed automatically. Lapsed/never-subscribed: purchase required. | Yes |

### Major version release checklist

- [ ] Tag the release commit
- [ ] Build the installer (NSIS/WiX — see Phase 3)
- [ ] Sign the installer with the EV code signing certificate
- [ ] Generate a new batch of license keys for the new major version (see below)
- [ ] Upload the new key batch to WordPress
- [ ] Update the version manifest JSON (for auto-update notification)
- [ ] Email new keys to all active Upgrade Assurance subscribers (WordPress generates
      and sends; verify the send completed and check for bounces)
- [ ] Update the download link on logscene.net
- [ ] Post release notes

---

## License Key Generation

License keys are signed offline using the Ed25519 private key. The private key never
touches WordPress or any networked system.

### Private key location

The Ed25519 private key is stored on a USB drive in a fire-resistant safe. A backup
copy is stored off-site. Retrieve the drive before running the key generation tool.

*(The key generation command-line tool and exact procedure will be documented here
once the tooling is built in Phase 2. Placeholder sections below reflect the planned
workflow.)*

### Generating a key batch

```
logscene-keygen --tier individual-10 --major-version 1 --count 500 --out keys-individual-v1.json
logscene-keygen --tier commercial-10 --major-version 1 --count 200 --out keys-commercial10-v1.json
logscene-keygen --tier unlimited --major-version 1 --count 100 --out keys-unlimited-v1.json
```

Adjust `--count` based on current inventory levels and expected sales volume.

### Uploading a key batch to WordPress

*(Upload procedure to be documented once the WordPress admin endpoint is built.)*

The WordPress admin dashboard shows keys-issued and keys-remaining per tier. Check
this before and after upload to confirm the batch loaded correctly.

### Low inventory alert

When a tier's remaining key count falls below the alert threshold (to be set in
WordPress admin), an email alert fires. At that point:

1. Retrieve the USB drive
2. Generate a new batch
3. Upload to WordPress
4. Return the drive to secure storage

If a batch is exhausted before replenishment, new purchases for that tier are queued
and fulfilled manually. This should be rare if the alert threshold is set conservatively.

### Key revocation

Key revocation is not implemented. If the private key is ever compromised:

1. Release a new major version with a new signing key pair
2. Generate a new public key and embed it in the new binary
3. Existing keys from the compromised key cannot activate the new major version
4. Notify all customers; active Upgrade Assurance subscribers receive new keys
   automatically

---

## Paddle — Payment and Revenue

### How revenue flows

1. Customer purchases via Paddle checkout on logscene.net
2. Paddle collects payment and handles all sales tax/VAT
3. Paddle POSTs a webhook event to the WordPress REST API endpoint
4. WordPress records the customer, delivers a license key, and activates the
   Upgrade Assurance subscription if purchased
5. Paddle accumulates net proceeds (after 5% + $0.50/transaction fee)
6. On the 1st of each month, if the balance exceeds the payout threshold ($100 minimum),
   Paddle initiates a payout; funds arrive in the Mercury bank account by the 15th

### Paddle dashboard

Log in at https://vendors.paddle.com

Key areas:
- **Catalog** — products and prices (one-time licenses and Upgrade Assurance subscriptions)
- **Customers** — customer list, subscription status, transaction history
- **Finance → Payouts** — payout history, scheduled payouts, connected bank account
- **Finance → Reports** — transaction-level data for bookkeeping reconciliation
- **Developer Tools → Notifications** — webhook event log; retry failed events here
- **Developer Tools → Notifications → Simulate** — send test webhook events

### Reading a payout reconciliation report

Each payout includes a Statement, Reverse Invoice(s), and Remittance Advice, emailed
to the account admin. For bookkeeping, export transaction-level data from
Finance → Reports to see the Paddle fee on each transaction and end-customer details.

### Issuing a refund

*(Document refund process in Paddle dashboard once confirmed.)*

### Updating Paddle product catalog

When adding a new license tier or changing a price, update the Paddle catalog first,
then update the WordPress plugin to handle the new product/price IDs in webhook events.
Test end-to-end in the Paddle sandbox before going live.

---

## Banking — Mercury

**Account:** Mercury (mercury.com) — receives Paddle payouts via ACH.

### Bookkeeping workflow

Mercury does not connect directly to Quicken. Monthly reconciliation process:

1. Log in to Mercury; export the month's transactions as CSV
2. Convert CSV to QIF/QFX using ImportQIF (free, quicknperlwiz.com)
3. Import into Quicken Home & Business
4. Alternatively, enter transactions manually (low volume makes this feasible)

### Paying business expenses

Business expenses (WordPress.com, Microsoft 365, Northwest Registered Agent, EV cert,
domain renewals) are paid from the Mercury account. Keep receipts for CPA review.

---

## Support Inbox

Support email arrives at support@logscene.net (Microsoft 365 shared mailbox).

### Automated triage workflow

1. Zapier or Make monitors support@logscene.net
2. New email triggers an Anthropic API call with the email content and the FAQ
   knowledge base as context
3. A draft response is delivered for human review
4. Approved responses are sent; resolved tickets are summarized and added to the FAQ
5. The FAQ grows richer over time, improving future draft quality

Human-in-the-loop review is recommended indefinitely, especially for billing issues,
license key problems, and anything that could involve a refund.

### Common support scenarios

**Customer says their license key doesn't work:**
1. Ask for the exact error message the app displays
2. Check WordPress customer DB for their email — confirm the key was delivered and
   which tier/major version it covers
3. If the key is for a different major version than they installed, explain and
   provide the correct key or upgrade path
4. If the key appears valid but the app rejects it, escalate to a manual review

**Customer wants a refund:**
- Apply judgment; Paddle supports issuing refunds from the dashboard
- Document the refund in Quicken

**Customer asks about Upgrade Assurance / upgrade eligibility:**
- Check their subscription status in Paddle (active, lapsed, or never subscribed)
- Direct them to the appropriate upgrade path per the pricing page

---

## WordPress — Site and Plugin

### Accessing the site

- **Production:** logscene.net — WordPress.com Business dashboard at wordpress.com
- **Staging:** staging site provisioned at WordPress.com (URL in account dashboard)
- **Local dev:** http://logscene.local/ via Local by Flywheel

### Deploying plugin updates

```
make deploy-staging    # deploy to staging, smoke-test first
make deploy-wp         # deploy to production
```

Both commands use WinSCP to sync `wp-plugin/logscene/` to the WordPress SFTP server.
Credentials are in `WP_SFTP_PASSWORD` and `WP_STAGE_PASSWORD` environment variables.

### Verifying a plugin deployment

Log in to the WordPress dashboard → Plugins → confirm logscene plugin is active.
Check the PHP error log via SFTP or WP-CLI over SSH if anything appears broken.

### Secrets in wp-config.php

The Paddle webhook verification key is stored in `wp-config.php` as a defined
constant. Access `wp-config.php` via SFTP. Do not expose this file via the web
(WordPress prevents this by default).

---

## Microsoft 365 — Email

- **peter@logscene.net** — primary licensed mailbox (1 paid license)
- **info@logscene.net** — shared mailbox (no additional license required)
- **support@logscene.net** — shared mailbox (no additional license required)

Manage at https://admin.microsoft.com. The licensed account has full access to both
shared mailboxes.

---

## Annual Renewals Checklist

Run this check each year. Set calendar reminders.

| Item | Renewal timing | Where to renew |
|---|---|---|
| logscene.net domain | Annual; check expiry date at Namecheap | namecheap.com |
| logscene.com domain (if acquired) | Annual; check expiry date | namecheap.com |
| WordPress.com Business | Annual (or monthly — convert to annual to save) | wordpress.com |
| Microsoft 365 Business Standard | Annual | microsoft.com |
| Northwest Registered Agent | Annual (~$125) | northwestregisteredagent.com |
| EV Code Signing Certificate | Annual (~$300–500); **do not let this lapse** — a lapsed cert means the installer triggers Windows security warnings | DigiCert or Sectigo |
| Texas Franchise Tax "no tax due" report | Annual (due May 15) | Texas Comptroller online |

### EV code signing certificate note

The EV cert has a hard expiry. If it lapses, the installer will trigger Windows
SmartScreen warnings for new downloads until a new cert is obtained and the installer
is re-signed. Monitor the expiry date and renew at least 30 days early.

---

## Taxes and CPA

- LLC profits pass through to personal Form 1040; self-employment tax (15.3%) applies
- Quarterly estimated tax payments required once profitable — coordinate with CPA
- Texas Franchise Tax: file "no tax due" report annually (no payment owed at realistic
  revenue levels; threshold is $2.47M)
- Paddle handles all sales tax/VAT globally as merchant of record; no filing required
  from the LLC for Paddle-processed sales
- Provide CPA with Paddle payout statements and transaction-level reports each year

---

## Succession and Business Continuity

If someone is reading this after taking over the business:

- The Development Guide (`docs/development-guide.md`) covers the technical side
- The Ed25519 private key (for generating license key batches) is on a USB drive
  stored securely by the original owner — contact the estate or successor for access
- The EV code signing certificate credentials should also be on the same USB drive
  or documented with the estate
- All services (Paddle, WordPress, Mercury, Microsoft 365, Namecheap, Northwest
  Registered Agent) can be transferred by updating account ownership/credentials
- The GitHub repo (`github.com/peterpla/logscene`) contains the full source code,
  build system, and both guides

The business is structured to require only a few hours per week to maintain once
support automation is running and the product is stable.
