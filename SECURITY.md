# Security Policy

## Supported versions

Lao Zi is pre-1.0; security fixes land on the latest minor release and `main`.
Pin a tagged release for production use.

| Version | Supported |
|---------|-----------|
| latest `0.x` minor | yes |
| older `0.x` | no |

## Reporting a vulnerability

Please report suspected vulnerabilities privately rather than opening a public
issue. Use GitHub's **"Report a vulnerability"** (Security → Advisories) on the
repository, or email the maintainer listed in the repository profile.

Include: affected version/commit, a description, and a minimal reproduction.
We aim to acknowledge within 5 business days and to agree on a disclosure
timeline once the report is validated. Please allow time for a fix before any
public disclosure.

## Scope

This project deterministically computes severities, citations, and numeric
goals and enforces them over LLM output. Reports of particular interest:

- ways to make a fabricated or unverifiable value reach a user as if traceable;
- ways to bypass severity/citation enforcement or the audit trail;
- injection in compiled DSL/SQL output or the Postgres integration;
- tampering that the audit hash chain fails to detect.
