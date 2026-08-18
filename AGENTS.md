# w-monitor Agent Rules

## Graphify Knowledge Graph — Always Query First

This workspace has a graphify knowledge graph at `graphify-out/graph.json`.
The graph covers all 18 Go source files: **101 nodes, 170 edges, 14 communities**.

**On EVERY question about code, architecture, dependencies, or how things work:**

1. Run `graphify query "<question>"` from `c:\Users\markv\Desktop\w-monitor`
2. Use the returned node/edge data as primary context for your answer
3. Include file+line references from graph nodes in your response (e.g. `collector/collector.go:L27`)
4. Only fall back to reading raw files if the graph output is insufficient

This saves ~71x tokens vs reading raw files directly.

### Query examples

```powershell
# From c:\Users\markv\Desktop\w-monitor:
graphify query "how does data flow from collector to storage?"
graphify query "what HTTP routes does the server expose?"
graphify path "Collector" "DB"
graphify explain "Job"
```

### Packages in this project

| Package | Key types |
|---------|-----------|
| `collector` | `Collector`, `UserTracker` |
| `storage` | `DB`, `MetricRow`, `ProcessRow` |
| `server` | `Server` |
| `retention` | `Job` |
| `export` | `CSVReport()`, `TextReport()` |
| `dashboard` | `Register()` |
| `main` | `program` (service wrapper) |

### Re-index after code changes

```powershell
graphify . --code-only --update
```
