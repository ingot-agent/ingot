# Security policy: Ingot Core

## Reporting a suspected vulnerability

Do not post exploit details, credentials, private prompts, state files or
sensitive reproduction data in a public issue or pull request.

**Reporting setup checked on 2026-09-22:** GitHub's API reported private
vulnerability reporting disabled for this repository. No dedicated security
email or guaranteed response schedule is published in this checkout.

Open the repository's [Security page](https://github.com/ingot-agent/ingot/security).
If maintainers have since enabled **Report a vulnerability**, use that private
form. Otherwise, open a [contact request](https://github.com/ingot-agent/ingot/issues/new?title=Private%20security%20reporting%20contact%20request)
containing only: “Please provide a private channel for a security report.”
Do not include affected code paths, reproduction steps or attachments in that
public coordination request. Wait for maintainers to establish a private
channel before sending technical details.

Once a private channel is available, include:

- `ingot version --json`, exact Image/Artifact IDs where relevant, the redacted recipe/lock, OS/architecture and Go version;
- the impact and the access/conditions needed to reproduce it;
- the smallest reproduction with synthetic data and credentials;
- expected versus observed behavior, and any suggested mitigation.

If the report spans repositories, name all affected modules in one private
report so maintainers can coordinate it. Ordinary non-security defects belong
in the public bug-report form.

## Scope and trust boundaries

Core owns module resolution, generated wiring, image verification/import/export,
managed Runtime state paths, process supervision and core installation/update.
Report path traversal, unintended file replacement, unsafe archive handling,
verification bypasses and process/state isolation defects here. Concrete model,
tool or browser behavior belongs in the [plugins repository](https://github.com/ingot-agent/plugins).

Selected plugins execute as ordinary code with the build/runtime account's
permissions. Building and startup-checking a composition can execute plugin
constructors; Core is not a sandbox for untrusted Go modules. Image digests
identify bytes and inputs, not whether an author is trustworthy. A Runtime home
isolates storage paths, not operating-system privileges.

## Version information and disclosure

Please report the exact affected versions, including older releases and source
checkouts. This repository does not currently publish an LTS or guaranteed
security-backport schedule. Fix availability and migration requirements must
be stated in the corresponding release notes; an unreleased branch fix should
not be described as available in an existing tag.

Use the established private channel to coordinate investigation and disclosure.
Do not assume this document guarantees a response deadline or authorizes
testing systems, accounts or data that you do not control.

## Maintainer release requirement

Before public release, enable **Private vulnerability reporting** in the GitHub
repository's security settings, verify the reporter-facing form with an
appropriate account, and update the dated setup statement above. Monitor the
chosen channel and document any support/response policy only after it has been
agreed. Adding this file or an issue-template link does not enable reporting
in GitHub settings.

