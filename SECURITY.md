# Security Policy

Please report vulnerabilities privately via GitHub Security Advisories
("Report a vulnerability" on the repository's Security tab) rather than in public issues.
We aim to acknowledge reports within 3 working days.

Operational notes:
- Keep secrets out of config files; use `${ENV_VAR}` references.
- The state file contains Blnk ledger/balance IDs and Formance account addresses — treat it as internal data.
- `formance2blnk validate` redacts secrets when printing the configuration.
