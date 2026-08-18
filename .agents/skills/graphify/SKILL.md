---
name: graphify
description: >-
  Query the w-monitor knowledge graph built by graphify. Use this skill to look up
  nodes, edges, relationships, and communities in the codebase graph before answering
  questions about architecture, dependencies, or code structure. Saves tokens by
  returning structured graph context instead of reading raw files.
trigger: always_on
---

# graphify — w-monitor Knowledge Graph Skill

This skill gives you access to the graphify knowledge graph for the `w-monitor` project.
The graph was built from 18 Go source files and contains **101 nodes, 170 edges, 14 communities**.

## When to Use This Skill

Use graphify queries **before** answering any question about:
- Code architecture or package structure
- How components relate to each other (dependencies, calls, data flow)
- Where a specific function, struct, or type is defined
- What a package/module does
- Tracing data flow (e.g. "how does metric data get from collector to storage?")
- Finding all callers or implementations of an interface

## How to Query the Graph

Always run queries from the project root: `c:\Users\markv\Desktop\w-monitor`

### Basic query (BFS — broad context)
```powershell
graphify query "<your question here>"
```

### Targeted path query (between two nodes)
```powershell
graphify path "Collector" "DB"
```

### Explain a specific node
```powershell
graphify explain "Server"
```

### Deep search (DFS — trace a specific path)
```powershell
graphify query "<question>" --dfs
```

### Token-budgeted query
```powershell
graphify query "<question>" --budget 800
```

## Graph Summary

The w-monitor graph has these major communities:

| Community | Key Nodes |
|-----------|-----------|
| 0 | Server (HTTP layer) |
| 1 | Collector, .Run(), .collect(), collectTopProcesses(), UserTracker |
| 2 | DB, MetricRow, ProcessRow, .InsertMetric(), .QueryMetrics(), .InsertProcess() |
| 3 | Job (retention), retention.New() |
| 4 | CSVReport(), TextReport(), WriteCSV() (export) |
| 5 | main.go, program, .Start(), .Stop(), .run(), DataDir() |

## Workflow: Always Query First

**On every user question about this codebase:**

1. Identify the relevant nodes (packages, structs, functions) from the question
2. Run `graphify query "<question>"` to get graph context
3. Use the returned NODEs and EDGEs to give a precise answer with file/line references
4. Only read raw files if the graph context is insufficient

## Refreshing the Graph

If code changes, re-index with:
```powershell
cd c:\Users\markv\Desktop\w-monitor
graphify . --code-only --update
```

The `--update` flag uses SHA256 cache and only re-processes changed files.
