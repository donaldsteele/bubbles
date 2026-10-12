# Security policy

## Reporting a vulnerability

Please do not report security problems in public issues, pull requests or chat.

Use GitHub's private vulnerability reporting: open the repository's
**Security** tab and choose **Report a vulnerability**. Include:

- the bubbles version (`bubbles version`) or the commit,
- the operating system and how bubbles was run (binary, service, Docker),
- steps to reproduce, and the impact you expect,
- whether you have shared the details anywhere else.

Do not send message contents, tokens, passwords, CA keys or backup archives in
the report. A description with synthetic data is enough.

## What to expect

- We acknowledge the report as soon as the maintainers see it, confirm whether it
  is a vulnerability, and agree on a fix and a disclosure date with the reporter.
- We credit the reporter if they want credit.
- Fixes ship in a new tagged release with a note in the release notes.

## Supported versions

Security fixes go to the latest tagged release. Older releases do not receive
fixes; upgrade to the newest release. Pre-release tags (for example `v1.2.0-rc1`)
are not supported once a stable release exists.

## Scope

In scope: the bubbles server, the web app, the desktop tray app, the Go SDK
(`pkg/bubblesclient`), the Docker image and the release workflow.

Out of scope: the security of the host operating system, a compromised server
machine, social engineering of administrators, and denial of service from the
local network that does not exploit a bug. See
[docs/security.md](docs/security.md) for the threat model and known limits.
