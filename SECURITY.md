# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in this library, please report it using GitHub's private vulnerability reporting feature rather than a public issue:

1. Navigate to the [Security tab](https://github.com/jh125486/pdf/security) of this repository
2. Click "Report a vulnerability" or "Advisories" → "New draft security advisory"
3. Provide details about the vulnerability (description, a minimal reproducing PDF if possible, affected versions)

This mechanism ensures your report is reviewed privately by the maintainer before any public disclosure.

## Scope

### In Scope
Security vulnerabilities in this library's own parsing, decryption, and text-extraction code. Given this fork exists specifically to harden against malformed and hostile PDF input (see [GO-2026-6115](https://pkg.go.dev/vuln/GO-2026-6115) / CVE-2026-56867 in `README.md`), reports involving crashes, hangs, or unbounded resource use from a crafted PDF are especially welcome.

### Out of Scope
The legacy RC4/MD5 encryption support in `initEncrypt`/`cryptKey` implements the PDF Standard Security Handler exactly as specified (PDF 32000-1:2008 §7.6.3.3) so that existing encrypted PDFs using it can still be *decrypted*. RC4 and MD5 being weak by modern standards is a property of that file format, not a fixable implementation choice here — reports about this specific, spec-mandated usage won't be actionable without breaking the ability to open real-world encrypted PDFs.

## Response Timeline

This is a solo-maintained fork. Security issues will be reviewed and addressed on a best-effort basis without a guaranteed SLA.

## Disclaimer

This library is provided as-is. While security is taken seriously, this project makes no guarantees about the completeness of security reviews or the absence of vulnerabilities.
