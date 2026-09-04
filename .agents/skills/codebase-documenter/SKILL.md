---
name: codebase-documenter
description: >-
  Expert skill for generating and maintaining exhaustive, highly detailed technical documentation
  whenever changes, updates, or refactors occur in the W-Monitor codebase. Analyzes AST diffs,
  CLI flags, API endpoints, database schemas, and service configurations, keeping USER_GUIDE.md,
  ADMIN_GUIDE.md, and CHANGELOG.md synchronized with zero documentation drift.
---

# Codebase Documenter Skill

This skill provides an automated, rigorous protocol for documenting any update, feature addition, bug fix, or refactor in the **W-Monitor** codebase.

Whenever code changes occur in this repository, this skill MUST be executed to produce **exhaustive, specific, and accurate documentation** that leaves zero ambiguity.

---

## 1. When to Trigger This Skill

Execute this documentation protocol:
- After modifying Go source code (`main.go`, `collector/`, `server/`, `storage/`, `agent/`, `alerting/`, `export/`, `retention/`, `dashboard/`).
- After adding, modifying, or deprecating CLI flags or environment variables.
- After modifying database schemas, query logic, or migrations (SQLite or PostgreSQL).
- After updating installer or build scripts (`install.ps1`, `install.sh`, `install_user.ps1`, `build_release.ps1`, `render.yaml`).
- After updating UI components or endpoints in the dashboard (`dashboard/static/index.html`, `/api/*`).

---

## 2. Pre-Documentation Discovery & Impact Analysis

Before writing or editing any documentation, perform these discovery steps:

### Step 1: Detect Changed Files & AST Symbols
Identify every file touched using git diff or file inspection:
- Identify modified functions, structs, interfaces, and methods.
- Identify newly added constants, flags, or configuration variables.

### Step 2: Query Knowledge Graph (Graphify First)
Always query the graphify knowledge graph to discover affected dependents and communities:
```powershell
# From project root:
graphify query "what components depend on <ModifiedSymbol>?"
graphify explain "<ModifiedSymbol>"
```
*Take note of the node file:line coordinates returned (e.g., `server/server.go:L571`).*

### Step 3: Classify the Scope of Impact
Categorize the changes into the appropriate domains:
1. **User / Operator Facing:** Changes affecting CLI commands, default behavior, installation scripts, or dashboard UI.
2. **Admin / Fleet Architecture:** Changes affecting multi-tenant models, organization API keys, Render/cloud deployment, or Postgres storage.
3. **Internal / API Contract:** Changes affecting `/api/ingest`, `/api/servers`, `/api/metrics`, data structures, or spooling mechanisms.

---

## 3. Mandatory Documentation Targets

Every codebase update must be reflected in the relevant targets below:

### Target A: `CHANGELOG.md`
Maintain a granular, chronological ledger of updates. Each entry must follow this structure:

```markdown
## [YYYY-MM-DD] - <Short Title of Change>

### Summary
Concise explanation of the rationale, problem solved, and technical change.

### Affected Components
- `package/file.go:L##-L##`: Description of symbol change.
- `scripts/installer.ext`: Description of script change.

### Added / Modified CLI Flags & Environment Variables
| Flag / Env Var | Type | Default | Description |
|---|---|---|---|
| `-example-flag` | string | `""` | Details of behavior and precedence |

### API & Protocol Changes
- `ENDPOINT /path`: Description of payload, headers, or query parameters.

### Operational & Migration Notes
- Steps required by operators or existing deployments to adopt the update without downtime.
```

---

### Target B: `USER_GUIDE.md`
Update user-facing workflows whenever commands, installation flags, or dashboard capabilities change:
1. **Quick-Start Sections:** Ensure copy-pasteable PowerShell and Bash snippets match exact current flags.
2. **CLI Reference Table:** Ensure every flag registered in `main.go` has an accurate row with type, default, and purpose.
3. **Dashboard & Exports:** Update instructions for UI controls, range selectors, server filters, and export commands (`-assessment-report`, `-export-csv`).
4. **Service Management:** Keep `Get-Service`, `systemctl`, logs, and uninstall commands accurate.

---

### Target C: `ADMIN_GUIDE.md`
Update administrator and enterprise assessment documentation if the change touches:
1. **Cloud & Multi-Tenancy:** Render deployments, PostgreSQL connection strings, quotas, and tenant isolation.
2. **Key Management:** Client onboarding (`-add-client`), auditing (`-list-clients`), or revocation (`-revoke-client`).
3. **Fleet Rollout Automation:** GPO, Ansible, cloud-init scripts, and network firewall tables.

---

### Target D: Refresh Knowledge Graph
After committing code and documentation changes, re-index the knowledge graph to prevent knowledge drift:
```powershell
graphify . --code-only --update
```

---

## 4. Documentation Quality & Specificity Standards

To ensure documentation is authoritative and enterprise-grade, follow these rules:

1. **Exact File & Line Hyperlinks:**
   Always provide clickable Markdown links with line numbers:
   - Example: `[server/server.go:L543](file:///c:/Users/markmv/Desktop/Zeus/server/server.go#L543)`
   - Do NOT say "in the server file". Specify the exact struct, method, and line range.

2. **No Placeholders or Hand-Waving:**
   - Never write `<insert config here>` or `TODO`.
   - Provide concrete, syntactically valid commands (e.g. `.\install.ps1 -ApiKey "3qzmUw7d..."`).

3. **Complete Configuration Hierarchy:**
   When documenting settings, always state the precedence order:
   `CLI Flag` > `Environment Variable` > `config.env file` > `Baked-in default (-ldflags)` > `Built-in fallback`.

4. **Cross-Platform Symmetry:**
   Always document **both** Windows (PowerShell / Windows Services) and Linux (Bash / `systemd`) procedures side-by-side.

5. **Security Sensitivity:**
   - Emphasize credential isolation (e.g., locking `config.env` with Windows ACLs or Linux `chmod 600`).
   - Remind operators never to pass secrets via `-dsn` in multi-user environments where process lists (`ps` / Task Manager) are visible.
