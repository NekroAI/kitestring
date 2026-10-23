# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report privately through GitHub: go to the repository's **Security** tab and choose **Report a vulnerability**. Include a description, the affected component and version, and steps to reproduce. If you have a proof of concept, attach it to the report rather than publishing it.

We aim to acknowledge reports within 3 working days and to agree on a disclosure timeline with you. We will credit you in the release notes unless you prefer otherwise.

## Scope

The Hub is often reachable from the internet, so we are especially interested in:

- **Hub authentication** — bypassing the web password, passkeys or session cookies; using a revoked device token; brute-forcing past the login rate limit.
- **Injection into agents** — anything that lets someone other than the owner get text injected into an agent's context through hooks or MCP, or route a decision to the wrong session.
- **The device side** — anything that lets another local user or process talk to the daemon's socket, read device tokens, or make `ks hook` block or alter an agent.
- **Uploads and the web UI** — path traversal, stored XSS through requirements, comments, evidence or file names.
- **Install script and releases** — anything that lets a tampered build pass the SHA-256 check.

Out of scope: denial of service, issues that require an already compromised machine or Hub account, and missing hardening headers without a demonstrated impact.

## Supported versions

Kitestring is pre-1.0. Security fixes are released for the latest version only.
