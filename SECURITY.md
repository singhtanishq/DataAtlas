# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.0.x   | ✅        |

## Reporting a Vulnerability

We take the security of DataAtlas seriously. DataAtlas connects to enterprise
data systems, so responsible disclosure matters.

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, report them privately via [GitHub Security Advisories](https://github.com/singhtanishq/DataAtlas/security/advisories/new)
or contact the maintainers directly.

Include as much of the following information as possible:

- Type of issue (e.g. authentication bypass, credential exposure, SQL injection)
- Step-by-step instructions to reproduce the issue
- Affected component(s) and version
- Impact of the issue, including how an attacker might exploit it

You can expect an initial response within 7 days. Please allow up to 90 days
for a fix before public disclosure.

## Security Design Principles

DataAtlas is built with the following security constraints:

- **Source credentials are never stored in plaintext.** They are encrypted at
  rest with authenticated encryption and are never returned by the API.
- **External data sources are accessed read-only.** Connectors use dedicated
  read-only accounts, connection timeouts, statement timeouts and result limits.
- **Strict tenant isolation.** Every organization-owned object is scoped to its
  organization and enforced at the API, service and query layers.
- **Metadata-first privacy.** Raw records from connected sources are not
  ingested or persisted by default; profiling uses configurable sampling and
  redaction.

See `docs/security/threat-model.md` for the current STRIDE-based analysis.
