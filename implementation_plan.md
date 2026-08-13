# W-Monitor Extension Plan: Advanced Metrics & Multi-Server Support

You asked if it is possible to extend W-Monitor to track advanced metrics (similar to Azure) and to monitor multiple servers simultaneously (like 5 HA application servers behind a load balancer).

**The short answer is YES**, but it requires evolving W-Monitor from a **local, standalone tool** into a **distributed monitoring system**. 

Here is a detailed breakdown of what needs to change and what we should add to achieve this.

## User Review Required

> [!CAUTION]
> Evolving W-Monitor to support these features will significantly increase the complexity of the codebase. It transitions the tool from a simple single-binary local monitor into a client-server architecture with external dependencies. 
> 
> Please review the proposed changes below and let me know which features you would like to prioritize building first.

## 1. Monitoring Multiple Servers Simultaneously

Currently, W-Monitor collects metrics and saves them to a local SQLite database, running a local dashboard on loopback only. 

To monitor 5 application servers, we need to introduce a **Client-Server (Agent/Hub) architecture**.

### Proposed Changes for Multi-Server:
1. **Agent Mode vs Hub Mode**: 
   - Add flags like `-agent` and `-hub`.
   - **Agents** run on the 5 application servers. Instead of serving a dashboard, they collect metrics and **push** them to the Hub over HTTP/HTTPS, or expose a `/metrics` endpoint for the Hub to **pull** (Prometheus style).
2. **Centralized Hub**:
   - The Hub runs on a separate management server (or one of the existing servers). It receives data from all agents, stores it centrally, and serves the dashboard.
3. **Database Schema Update**: 
   - We must add a `hostname` or `server_id` column to the `metrics` and `processes` tables in `storage/db.go` so the dashboard can filter and aggregate data per server.
4. **Dashboard Updates**:
   - Update the UI to include a "Server Selector" dropdown to view metrics for specific servers or an aggregated view of the cluster.

---

## 2. Going Beyond Basic Metrics (Azure Parity)

To get Application-level, Database, and Load Balancer metrics, we need to expand `collector/collector.go`. 

Here is how we can implement the metrics from your Azure list:

### A. App Service & Application Insights Metrics
*Metrics: Requests, response time, HTTP 4xx/5xx, dependencies, exceptions.*
* **How to add it:** System-level monitors (like `gopsutil`) cannot see inside your application. We have two options:
  1. **Log Parsing:** W-Monitor can be configured to tail your web server logs (e.g., NGINX, IIS, Apache) to calculate request rates and HTTP errors.
  2. **Metrics Endpoint (Recommended):** Add an integration where W-Monitor scrapes a `/metrics` endpoint from your application (similar to Prometheus), or build a lightweight SDK for your app to push custom metrics (like response times and exceptions) directly to W-Monitor.

### B. SQL Database Metrics
*Metrics: DTU/vCore utilization, CPU, storage, connections, deadlocks.*
* **How to add it:** We need to add a database polling module.
  - W-Monitor would take a database connection string in a config file.
  - It would use Go's `database/sql` to periodically run queries against system tables (e.g., `sys.dm_os_performance_counters` for SQL Server, or `pg_stat_database` for PostgreSQL) to fetch active connections, deadlocks, and query throughput.

### C. Load Balancer / Network Firewalls
*Metrics: Data processed, health probe status, connections, SNAT ports.*
* **How to add it:** 
  - If you are using a software load balancer (like HAProxy or NGINX), W-Monitor can query their built-in stats pages (e.g., HAProxy stats CSV, NGINX stub_status).
  - If you are using a cloud load balancer (which it sounds like you might be moving away from), we would have to query the cloud provider's API.

### D. AKS / Container Metrics
*Metrics: Node CPU/memory, pod count, container metrics.*
* **How to add it:** 
  - Integrate with the Docker API or Kubernetes internal metrics server. Go has excellent client libraries for Docker and Kubernetes to fetch container-specific CPU and memory usage.

## Open Questions

Before we start coding, I need to know your preferences:

> [!IMPORTANT]
> 1. **Architecture Preference:** Would you prefer the Agents to **push** data to the Hub, or should the Hub **pull/scrape** data from the Agents? (Push is usually easier for firewalls).
> 2. **Application Stack:** What language/framework are your 5 application servers running? (This helps me design how we collect App/HTTP metrics).
> 3. **Database:** Which database engine are you using (SQL Server, PostgreSQL, MySQL)?
> 4. **Prioritization:** Should we build the **Multi-Server (Agent/Hub) architecture** first, or add the **Database/App metric collectors** to the local version first?

Let me know how you'd like to proceed!
