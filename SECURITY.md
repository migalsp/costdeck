# Security Policy

## Supported versions

Security fixes go into the latest minor release, published as a new patch release of the
operator image, the Helm chart and `install.yaml`. Older minor releases receive no fixes:
upgrade to the latest release to get them.

| Version | Supported |
| ------- | --------- |
| 1.6.x   | Yes       |
| < 1.6   | No        |

## Reporting a vulnerability

Report vulnerabilities privately through GitHub:
<https://github.com/migalsp/costdeck/security/advisories/new>. Do not open a public issue,
pull request or discussion for them.

Include what you can of:

- the Cost Deck version and how it is installed (Helm chart, `install.yaml`);
- the component involved: the operator, the REST API, the dashboard, the MCP server, the
  Webex bot or the Helm chart;
- steps to reproduce, and what an attacker gains.

You get an acknowledgement within five working days. Once the report is confirmed, the fix
is released as a patch release and published as a GitHub security advisory that credits you,
unless you ask not to be named. A report that is not accepted gets an answer that says why.
