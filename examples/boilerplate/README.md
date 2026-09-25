# REF Enterprise Auth & RBAC Boilerplate

A production-grade, high-performance web and API platform built with the **REF Platform**, **FastHTTP (`fh`)**, **Argon2id (RFC 9106)**, **Enterprise RBAC (`github.com/oarkflow/authz`)**, and **SPL Template Rendering (`github.com/oarkflow/spl` & `github.com/oarkflow/template`)**.

---

## 🌟 Architectural Highlights

1. **Zero Go Route Handlers (`main.go` is Pure Bootstrapping)**:
   - `./cmd/server/main.go` defines **zero routes** and **zero static file registrations** in Go.
   - 100% of routes, intents, resources, roles, and static file mounts are declared in modular BCL files located under `bcl/*.bcl`.
2. **Native Argon2id Password Hashing (RFC 9106 & OWASP Standards)**:
   - OWASP recommended parameters: Memory 64 MiB (`m=65536`), Iterations 3 (`t=3`), Parallelism 4 (`p=4`), 16-byte cryptographically secure salt, 32-byte key.
   - PHC string format (`$argon2id$v=19$m=65536,t=3,p=4$...`).
   - Constant-time dummy verification protects against timing-attack user enumeration.
   - Dual-hash verification with legacy Bcrypt fallback and seamless transparent upgrades.
3. **Enterprise RBAC with Inheritance (`github.com/oarkflow/authz`)**:
   - Multi-tier role inheritance matrix: `super_admin > admin > manager > user > guest`.
   - Wildcard permission evaluation (e.g. `users:*`, `reports:*`, `*`).
   - Guarded at both route level (`authz { roles [...] }`) and intent node level.
4. **SPL Template Engine (`github.com/oarkflow/spl` & `github.com/oarkflow/template`)**:
   - Modern server-side rendering (SSR) with template inheritance (`@extends`), named blocks (`@define`, `@block`), conditionals (`@if`, `@else`), and loops (`@for`).
   - Adheres strictly to SPL's `SecureMode` rules to eliminate XSS vulnerabilities.
   - Modern glassmorphism dark mode aesthetic with responsive layout and zero inline script tags.
5. **Dual Web & REST API Protocol Support**:
   - Web browser forms (`application/x-www-form-urlencoded`) are automatically translated into structured JSON inputs for REF intents, with HTTP 303 browser redirects and session cookie handling.
   - REST API clients (`application/json`) receive structured JSON responses and semantic HTTP status codes.
6. **High-Performance Structured Logging & Auditing (`github.com/oarkflow/zlog`)**:
   - Zero-allocation, high-throughput structured JSON/console logging with attributes.
   - Built-in audit logger capturing authentication successes/failures, administrative role mutations, and forensic traces.
7. **Real-Time Anomaly Detection & Threat Mitigation (`github.com/oarkflow/tcpguard`)**:
   - Intercepts requests to detect business and transport anomalies: brute-force velocity, credential stuffing, suspicious headers, and sensitive endpoint abuse.
   - Business anomaly rules: blocks unauthorized self-promotion to `super_admin` and prevents unauthorized primary admin account demotion.
8. **Observability: Prometheus, OpenTelemetry & Structured Logging (`ref/observer/*`)**:
   - `cmd/server/main.go` wires `promobserver.New`, `otelobserver.New`, and `slogobserver.New` into `platform.LoadOptions.Observers`, so every REF node execution, decision, effect commit, and intent completion compiled from BCL is observed with zero Go code inside any BCL-compiled node.
   - `/metrics` serves Prometheus text exposition (`promhttp.HandlerFor`), scoped to a dedicated `prometheus.Registry` rather than the global default.
   - The OTel observer is wired with a no-op tracer — wiring a real exporter (Jaeger, Tempo, an OTLP collector, …) is a deployment concern intentionally left unfabricated in a reference example; swap it for a real `sdktrace.TracerProvider` when deploying.
9. **Health Checks (`ref/health`)**:
   - A `*health.Registry` is attached via `platform.LoadOptions.HealthRegistry` and reachable through `p.Engine.Health()`.
   - `/livez` reports process liveness; `/readyz` additionally runs a readiness check that pings the primary `database` resource (`bcl/03_resources.bcl`) via `platform.Database.PingContext`, so a load balancer stops routing traffic here before requests that touch the database would fail.
   - fh's `Ctx` has no native `net/http.Handler` adapter, so `internal/web.WrapHTTPHandler` bridges `health.LivenessHandler`/`health.ReadinessHandler`/`promhttp.Handler` (all stdlib `http.Handler`s) onto `*fh.App` routes.
10. **Resilience — known limitation**: `ref/execution` enforces `capability.Resilience` (`WithTimeout`/`WithRetry`/`WithBulkhead`) on any `capability.Registration` built in Go, and the platform compiler's own node-level `timeout`/`retry` BCL fields (see `bcl/04_intents_auth.bcl`) already cover the common case for BCL-declared intents. Per-intent **bulkhead** concurrency limits, however, are a Go-level `capability.Option` with no BCL surface yet — BCL's declarative schema does not currently expose a way to attach one to a compiled intent node. Extending the BCL grammar for this was out of scope here; if you need it, attach `capability.WithBulkhead` to a `capability.Registration` you register directly against `p.Engine.Capabilities()` in Go, or open an issue against `platform/platform.go`'s node compiler. See `docs/runtime-execution-fabric.md` at the repo root for the execution model this sits on.
11. **Circuit Breaker — available, not wired**: none of this boilerplate's resources (`bcl/03_resources.bcl`) model a genuinely external, flaky dependency — `database.sql`, `cache.sql`, `queue.sql`, and `storage.fs` are all local/embedded — so nothing here plausibly needs one, and none is wired in to avoid a fabricated dependency. `capability.NewInMemoryCircuitBreakerCapability` (no extra infrastructure) and `capability.NewRedisCircuitBreakerCapability` (distributed, needs Redis) are both available in `github.com/oarkflow/ref/capability` for an application that does call out to a real external service:
    ```go
    reg, breaker := capability.NewInMemoryCircuitBreakerCapability(
        "payments-api",
        capability.DefaultInMemoryCircuitBreakerConfig(),
    )
    _ = p.Engine.Capabilities().Register(reg)
    // breaker.State("payments-api") feeds health.FromCircuitBreaker for /readyz.
    ```
12. **Configuration — `ref/config` available as an alternative**: `config/config.go` in this example is a small, working, env-var-driven `Config` loader. `github.com/oarkflow/ref/config`'s `Load[T]` offers layered sources (env/file/override), struct tags, a `Validate()` hook, and hot-reload, and is a reasonable drop-in for an application that outgrows a hand-rolled loader — but rewriting a working, low-risk loader just to "use everything" wasn't judged worth the churn here, so it was left as-is.

---

> [!TIP]
> 📘 **Looking for the deep dive?** See [ARCHITECTURE.md](file:///Users/sujit/Sites/ref/examples/boilerplate/ARCHITECTURE.md) for the complete end-to-end guide detailing how all components connect, the 5-phase request pipeline, DAG mechanics, and concrete execution walkthroughs for registration, login, and RBAC governance.

---

## 📁 Repository Structure

```
boilerplate/
├── ARCHITECTURE.md                 # Complete Architecture, Workflow & Execution Pipeline Guide
├── bcl/                            # 100% Declarative Application Configuration
│   ├── 01_app.bcl                  # Application identity, version, environment
│   ├── 02_roles.bcl                # RBAC roles, hierarchy, and permissions
│   ├── 03_resources.bcl            # Database, file sessions, session auth, authorizer, rules.engine
│   ├── 04_intents_auth.bcl         # Register, login, logout, forgot/reset password
│   ├── 05_intents_dashboard.bcl    # Dashboard overview, admin governance, reports, profile
│   ├── 06_routes_web.bcl           # HTML Web routes mapped to SPL templates
│   ├── 07_routes_api.bcl           # REST API JSON endpoints (/api/v1/auth/*)
│   └── 08_static.bcl               # Declarative static asset serving (/static)
├── cmd/
│   └── server/
│       └── main.go                 # Server entry point (BCL loader + zlog + tcpguard)
├── internal/
│   ├── auth/                       # Native Go auth domain service, hasher, & repository
│   │   ├── argon2id.go             # RFC 9106 Argon2id hasher with timing attack protection
│   │   ├── handler.go              # FastHTTP Web & API dual handlers
│   │   ├── middleware.go           # Session, role, and permission route middlewares
│   │   ├── repository.go           # Thread-safe user and session persistence
│   │   └── service.go              # Complete authentication lifecycle logic
│   ├── domain/
│   │   └── models.go               # Domain entities (User, Session, PasswordResetToken, Principal)
│   ├── rbac/
│   │   └── rbac.go                 # Enterprise RBAC authorizer backed by oarkflow/authz
│   ├── security/
│   │   ├── guard.go                # TCPGuard anomaly detection & business rule guards
│   │   └── guard_test.go           # Anomaly detection & privilege escalation tests
│   ├── telemetry/
│   │   ├── logger.go               # Structured logging & audit logger via oarkflow/zlog
│   │   └── logger_test.go          # Telemetry and logging tests
│   └── web/
│       ├── handlers.go             # Dashboard, admin portal, manager, and profile views
│       └── renderer.go             # SPL template engine adapter with default Globals
├── static/
│   ├── css/
│   │   └── app.css                 # Glassmorphic dark mode styling system
│   └── js/
│       └── app.js                  # Client-side interactions & password strength meter
├── templates/                      # SPL Templates
│   ├── layouts/
│   │   ├── base.html               # Main application layout with sticky glass navbar
│   │   └── auth.html               # Focused layout for login, register, and reset pages
│   ├── components/
│   │   ├── navbar.html             # Role-aware responsive navigation bar
│   │   ├── alert.html              # Dynamic error/success feedback alerts
│   │   └── footer.html             # Application branding footer
│   └── pages/
│       ├── auth/                   # login.html, register.html, forgot_password.html, reset_password.html
│       ├── dashboard/              # index.html, admin.html, manager.html, profile.html
│       └── errors/                 # 403.html (Access Denied), 404.html (Not Found)
└── boilerplate_test.go             # Comprehensive automated test suite
```

---

## 🌐 System Architecture & Topology

The boilerplate decouples declarative specifications from imperative infrastructure. The diagram below illustrates how all layers connect, from client traffic down to storage engines and template compilation:

```mermaid
flowchart TD
    subgraph Client_Layer["1. Client Tier"]
        Browser["Desktop & Mobile Browsers<br/>(HTML Forms, Cookies, DOM)"]
        APIClient["REST API Clients / Mobile Apps<br/>(JSON Payloads, Bearer / API Keys)"]
    end

    subgraph Transport_Layer["2. FastHTTP Engine (fh)"]
        FHEngine["FastHTTP Core Server"]
        FormTranslator["Form-to-JSON Adapter<br/>(x-www-form-urlencoded -> JSON facts)"]
        StaticServer["Static Assets Mount<br/>(/static -> boilerplate/static)"]
    end

    subgraph Platform_Layer["3. REF Platform Pipeline"]
        RouteRegistry["BCL Compiled Route Table"]
        SessionGuard["Session Extractor & Validator<br/>(cookie: ref_boilerplate_sid)"]
        RBACGate["Route-Level RBAC Gate<br/>(github.com/oarkflow/authz)"]
        Dispatcher["Intent Dispatcher & Execution Engine"]
    end

    subgraph DAG_Layer["4. Intent Execution DAG (Fact Graph)"]
        ValidationNode["validate.required<br/>Input structural checks"]
        CryptoNode["auth.password_hash / verify<br/>RFC 9106 Argon2id Hasher"]
        DatabaseNode["database.query<br/>modernc.org/sqlite (or pgx)"]
        SessionNode["auth.login / auth.logout<br/>Signed session mutation"]
        CollectorNode["collect<br/>Aggregates final response value"]
    end

    subgraph Presentation_Layer["5. Presentation & Response Tier"]
        SPLRenderer["SPL Template Engine (SSR)<br/>github.com/oarkflow/spl + template"]
        RedirectHandler["Browser 303 Redirect Engine<br/>(Maintains session cookie across views)"]
        JSONSerializer["REST JSON Serializer<br/>(RFC 7807 problem details)"]
    end

    Browser -->|HTTP GET/POST Forms| FHEngine
    APIClient -->|HTTP GET/POST JSON| FHEngine
    FHEngine -->|Static Assets| StaticServer
    FHEngine --> FormTranslator
    FormTranslator --> RouteRegistry
    RouteRegistry --> SessionGuard
    SessionGuard --> RBACGate
    RBACGate --> Dispatcher
    Dispatcher --> DAG_Layer
    DAG_Layer --> Presentation_Layer
    Presentation_Layer -->|HTML (text/html)| Browser
    Presentation_Layer -->|303 Redirect| Browser
    Presentation_Layer -->|JSON (application/json)| APIClient
```

---

## 🔁 Complete Request Lifecycle (Step-by-Step)

Every request traveling through the platform undergoes a predictable, deterministic sequence of phases:

```mermaid
sequenceDiagram
    autonumber
    actor User as Client (Browser / API)
    participant FH as FastHTTP Engine
    participant Guard as Session & RBAC Guards
    participant Engine as REF Intent Engine
    participant DB as SQLite / PostgreSQL
    participant SPL as SPL Template Engine

    User->>FH: HTTP Request (e.g. POST /login or GET /dashboard)
    alt Content-Type is form-urlencoded
        FH->>FH: Automatically parses query values into JSON fact map
    end

    FH->>Guard: Authenticate & Authorize Route
    Guard->>Guard: Read 'ref_boilerplate_sid' cookie (HMAC check)
    Guard->>Guard: Resolve Principal (Roles, ID, Metadata)
    Guard->>Guard: Evaluate RBAC Role Hierarchy (oarkflow/authz)
    
    alt RBAC Check Denied
        Guard-->>User: HTTP 403 Forbidden / 401 Unauthorized
    else RBAC Check Passed
        Guard->>Engine: Dispatch Intent (e.g. 'dashboard.index')
        Engine->>Engine: Build DAG nodes from BCL definition
        Engine->>DB: Execute SQL statements (e.g. select user, metrics)
        DB-->>Engine: Query Result Records
        Engine->>Engine: Compute facts and collect output
        Engine-->>FH: Intent Result Value Map
        
        alt Route has BCL 'template' declared
            FH->>SPL: Render template with Result Value Map + Globals
            SPL-->>FH: Rendered HTML (SSR, SecureMode validated)
            FH-->>User: HTTP 200 OK (text/html; charset=utf-8)
        else Route is Web Action with Redirect
            FH-->>User: HTTP 303 See Other (Location: /dashboard)
        else Route is REST API
            FH-->>User: HTTP 200/201 OK (application/json)
        end
    end
```

---

## ⚡ Workflow & Intent Execution Pipelines (Fact Graph DAGs)

In the REF Platform, business logic is composed of **Directed Acyclic Graphs (DAGs)**. Nodes consume facts (`requires`) and publish new facts (`provides`). Below are the primary execution pipelines implemented in `bcl/04_intents_auth.bcl` and `bcl/05_intents_dashboard.bcl`:

### 1. User Registration Pipeline (`auth.register`)

```mermaid
graph LR
    Input["input<br/>(email, name, password)"] --> NodeVal["1. validate<br/>(validate.required)"]
    Input --> NodeHash["2. hash-password<br/>(auth.password_hash)<br/>Argon2id 64MiB/3t/4p"]
    NodeVal --> NodeHash
    NodeHash --> NodeSQL["3. insert-user<br/>(database.query)<br/>INSERT INTO users RETURNING id, password_hash"]
    Input --> NodeSQL
    NodeSQL --> NodeLogin["4. login<br/>(auth.login)<br/>Creates signed session & principal"]
    Input --> NodeLogin
    NodeLogin --> NodeCollect["5. response<br/>(collect)<br/>Packages final payload"]
```

### 2. User Authentication Pipeline (`auth.login`)

```mermaid
graph LR
    Input["input<br/>(email, password)"] --> NodeVal["1. validate<br/>(validate.required)"]
    NodeVal --> NodeFind["2. find-user<br/>(database.query)<br/>SELECT * FROM users WHERE email=LOWER($1)"]
    Input --> NodeFind
    NodeFind --> NodeLogin["3. login<br/>(auth.login)<br/>RFC 9106 Argon2id Verify<br/>Constant-time dummy defense<br/>Issues session cookie"]
    Input --> NodeLogin
    NodeLogin --> NodeCollect["4. response<br/>(collect)<br/>Emits user profile & redirect hint"]
```

### 3. Protected Admin Governance Pipeline (`dashboard.admin`)

```mermaid
graph LR
    SessionCookie["Session Cookie<br/>ref_boilerplate_sid"] --> RouteGuard["Route RBAC Gate<br/>Requires admin or super_admin"]
    RouteGuard --> NodeAuth["1. user-id<br/>(auth.require_session)<br/>Extracts active caller ID"]
    NodeAuth --> NodeAdmin["2. load-admin<br/>(database.query)<br/>Loads caller profile"]
    NodeAdmin --> NodeUsers["3. load-all-users<br/>(database.query)<br/>Loads user table & role mappings"]
    NodeUsers --> NodeCollect["4. response<br/>(collect)<br/>Emits user list + roles matrix"]
    NodeCollect --> SPL["SPL Template Engine<br/>pages/dashboard/admin.html<br/>layouts/base.html"]
    SPL --> Browser["Rendered HTML View<br/>Interactive role switcher & table"]
```

---

## 🔍 Data Flow Walkthrough: A Live Login Request

To illustrate how everything connects in practice, here is the lifecycle of a user signing in via the web interface:

1. **User Submits Form**:
   - The browser issues an HTTP `POST /login` with form body:
     ```
     email=admin@example.com&password=Password123!&redirect=/dashboard
     ```
2. **FastHTTP Ingress**:
   - FastHTTP matches route `web.login_action` declared in `bcl/06_routes_web.bcl`.
   - The platform detects `application/x-www-form-urlencoded` and converts it into a JSON fact map:
     ```json
     { "email": "admin@example.com", "password": "Password123!", "redirect": "/dashboard" }
     ```
3. **Intent Invocation (`auth.login`)**:
   - `node "validate"` verifies that `email` and `password` are present.
   - `node "find-user"` runs parameterized SQL:
     ```sql
     SELECT id, email, name, password_hash, roles, status FROM users WHERE email = LOWER($1) LIMIT 1;
     ```
     Returning Alice Admin's record.
   - `node "login"` calls native `VerifyPassword(password, hash)`:
     - Confirms hash format `$argon2id$v=19$m=65536,t=3,p=4$...`.
     - Derives key with 4 threads and 64 MiB RAM.
     - Constant-time verification succeeds.
   - A cryptographic session is committed to `sessions` (`session.file` / disk).
4. **Session Cookie & Browser Redirection**:
   - The platform sets the signed `ref_boilerplate_sid` cookie:
     ```http
     Set-Cookie: ref_boilerplate_sid=v1.1f64f6...; Path=/; Max-Age=86400; HttpOnly; SameSite=Lax
     ```
   - Recognizing route `web.login_action`, the server responds with:
     ```http
     HTTP/1.1 303 See Other
     Location: /dashboard
     ```
5. **Dashboard Rendering (`web.dashboard`)**:
   - The browser automatically navigates to `GET /dashboard`, sending the new session cookie.
   - `route "web.dashboard"` validates the cookie via `auth "session_auth"`.
   - `authz.rbac` checks role membership against `["user", "manager", "admin", "super_admin"]` and permits access.
   - Intent `dashboard.index` loads the user record and system statistics.
   - The SPL template engine compiles `pages/dashboard/index.html` extending `layouts/base.html`.
   - The user is presented with the glassmorphic console showing real-time RBAC permissions, role badges, and shortcuts.

---

## ⚡ Quick Start

### 1. Run the Server

From the repository root:

```bash
# Start server on default port 8080 (or specify PORT)
PORT=8080 go run ./boilerplate/cmd/server
```

Open your browser and visit: `http://localhost:8080`

### 2. Pre-Seeded Demo Test Accounts

The SQLite database is pre-seeded with three demo accounts. All accounts share the password `Password123!`:

| Email | Role | Accessible Areas | Password |
| :--- | :--- | :--- | :--- |
| `admin@example.com` | `admin` | `/dashboard`, `/dashboard/admin`, `/dashboard/manager`, `/profile` | `Password123!` |
| `manager@example.com` | `manager` | `/dashboard`, `/dashboard/manager`, `/profile` | `Password123!` |
| `user@example.com` | `user` | `/dashboard`, `/profile` | `Password123!` |

*(Note: Clicking the demo buttons on the `/login` page automatically fills in these accounts for instant evaluation.)*

---

## 🧪 Running the Automated Test Suite

To run all unit, security, RBAC, SPL rendering, and BCL compilation tests:

```bash
# Test the boilerplate package
go test -v ./boilerplate/...

# Test the core REF platform
go test -v ./platform/...
```

All tests execute in sub-second times with zero external network dependencies:
- `TestArgon2idSecurity`: Validates RFC 9106 parameters, hash formats, positive/negative matching, and timing attack resistance.
- `TestAuthRegister` & `TestAuthLogin`: Tests registration, session cookie issuance, and credential verification.
- `TestForgotPasswordAndResetLifecycle`: Validates single-use token generation, expiration, and password updates.
- `TestRBACRoleHierarchyAndPermissions`: Verifies multi-level inheritance with `oarkflow/authz`.
- `TestRouteBasedAuthorization`: Verifies route authorization blocks.
- `TestSPLTemplateRendering`: Verifies template compilation, layouts, and data binding.
- `TestBCLLoadDirAndMount`: Verifies multi-file BCL parsing, compilation, and route mounting onto `fh.App`.

---

## 📖 Feature & Technical Deep Dive

### 1. Argon2id Password Cryptography ([platform/argon2id.go](file:///Users/sujit/Sites/ref/platform/argon2id.go))

Passwords are never stored in plain text or using weak single-round algorithms. The hasher uses **Argon2id**, the winner of the Password Hashing Competition (PHC) and standard of RFC 9106:

```
$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
```

- **Memory Cost (`m`)**: 64 MiB (65,536 KiB) prevents GPU and ASIC brute-force cracking.
- **Time Cost (`t`)**: 3 iterations guarantees minimum CPU time per hash.
- **Parallelism (`p`)**: 4 concurrent execution threads.
- **Timing Defense**: If an email is queried that does not exist in the database, `VerifyPassword` executes a dummy verification against a static hash using `subtle.ConstantTimeCompare`. An attacker cannot measure response latency to determine if an email is registered.

### 2. Enterprise RBAC Matrix ([bcl/02_roles.bcl](file:///Users/sujit/Sites/ref/boilerplate/bcl/02_roles.bcl))

Roles and permissions are governed by `github.com/oarkflow/authz`. Roles inherit permissions transitively:

```mermaid
graph TD
    SuperAdmin["super_admin (*)"] --> Admin["admin (admin:dashboard, users:*, roles:manage, audit:read)"]
    Admin --> Manager["manager (reports:read, reports:write, users:read)"]
    Manager --> User["user (dashboard:view, profile:view, profile:update)"]
    User --> Guest["guest"]
```

Because `admin` inherits `manager` and `manager` inherits `user`, an `admin` automatically possesses `reports:read`, `dashboard:view`, and `profile:view`.

### 3. Declarative Routes & Template Rendering ([bcl/06_routes_web.bcl](file:///Users/sujit/Sites/ref/boilerplate/bcl/06_routes_web.bcl))

Routes are declared with HTTP method, path, template path, layout path, and authz rules:

```bcl
route "web.admin" {
  method GET
  path "/dashboard/admin"
  intent "dashboard.admin"
  template "pages/dashboard/admin"
  layout "layouts/base"
  session "sessions"
  auth "session_auth"
  cache_control "private, no-store"
  authz {
    roles ["admin", "super_admin"]
    authorizer "authorization"
  }
}
```

When an unauthenticated request attempts to visit `/dashboard/admin`, the platform halts execution before the intent runs and returns HTTP 401. If the user is authenticated but holds only the `user` role, HTTP 403 Forbidden is returned.

### 4. Declarative Static Asset Serving ([bcl/08_static.bcl](file:///Users/sujit/Sites/ref/boilerplate/bcl/08_static.bcl))

Static files are served with compression and caching directives declared in BCL:

```bcl
static "assets" {
  prefix "/static"
  root "static"
  compress true
  max_age 24h
  cache_control "public, max-age=86400"
}
```

The server automatically maps `/static/*` requests to the `boilerplate/static/` directory on disk.

---

## 🛠️ How-To Guides

### How to Add a New Page & Route

1. **Create the Template**:
   Add a new file, e.g. `templates/pages/dashboard/analytics.html`:
   ```html
   @extends("layouts/base.html")

   @define("content") {
     <div class="glass-card">
       <h1>Real-Time Analytics</h1>
       <p>Telemetry and metrics overview.</p>
     </div>
   }
   ```

2. **Declare the Route in BCL**:
   Open `bcl/06_routes_web.bcl` and add:
   ```bcl
   route "web.analytics" {
     method GET
     path "/dashboard/analytics"
     template "pages/dashboard/analytics"
     layout "layouts/base"
     session "sessions"
     auth "session_auth"
     authz {
       roles ["manager", "admin", "super_admin"]
       authorizer "authorization"
     }
   }
   ```
   *No Go code modifications needed! The server mounts the route automatically.*

### How to Add a New RBAC Permission

1. Open `bcl/02_roles.bcl`.
2. Add the permission to the desired role:
   ```bcl
   role "manager" {
     description "Operations, metrics and reporting"
     permissions [
       "reports:read",
       "reports:write",
       "users:read",
       "analytics:export"  # <-- New permission
     ]
     inherits ["user"]
   }
   ```
3. Any gate requiring `analytics:export` will now allow `manager`, `admin`, and `super_admin`.

### How to Add a REST API Endpoint

1. Open `bcl/07_routes_api.bcl`.
2. Declare the API route:
   ```bcl
   route "api.reports" {
     method GET
     path "/api/v1/reports"
     intent "dashboard.manager"
     session "sessions"
     auth "session_auth"
     authz {
       roles ["manager", "admin", "super_admin"]
       authorizer "authorization"
     }
   }
   ```
   *Requests sending `Accept: application/json` receive clean JSON results.*

---

## 🚀 Production Deployment Checklist

When preparing to deploy this boilerplate into production environments:

1. **Environment Variables**:
   - `PORT`: Server listen port (e.g. `8080`).
   - `APP_ENV`: Set to `production` (disables template hot-reload and activates caching).
   - `SESSION_SECRET`: Set to a strong 32+ byte cryptographic random secret.
   - `DATABASE_URL`: Connection string for production database (e.g. `postgres://user:pass@host:5432/dbname?sslmode=require`).
2. **Switching from SQLite to PostgreSQL**:
   In `bcl/03_resources.bcl`, change the `driver` in `resource "database"` from `"sqlite"` to `"pgx"` or `"postgres"`.
3. **Switching Session Storage**:
   For multi-replica deployments behind a load balancer, change `kind "session.file"` to a distributed session backend such as Redis or SQL.
4. **HTTPS & Cookie Hardening**:
   In `bcl/03_resources.bcl`, ensure `secure true` is set on `resource "sessions"` so cookies are only transmitted over TLS (`__Host-` / `Secure`).

---

## 📡 API Endpoints Reference

| Method | Endpoint | Description | Auth Required |
| :--- | :--- | :--- | :--- |
| `GET` | `/login` | Sign-in web page | No |
| `POST` | `/login` | Authenticate user & issue session cookie | No |
| `GET` | `/register` | Account registration web page | No |
| `POST` | `/register` | Create account with Argon2id hash | No |
| `GET` | `/forgot-password` | Password recovery page | No |
| `POST` | `/forgot-password` | Request password reset token | No |
| `GET` | `/reset-password` | Password reset completion page | No |
| `POST` | `/reset-password` | Set new password using token | No |
| `POST` | `/logout` | Invalidate active session & clear cookie | Yes |
| `GET` | `/dashboard` | User dashboard overview | Yes (`user+`) |
| `GET` | `/dashboard/admin` | Admin governance & user management portal | Yes (`admin+`) |
| `POST` | `/dashboard/admin/role` | Modify user role assignment | Yes (`admin+`) |
| `GET` | `/dashboard/manager` | Operations audit & reporting | Yes (`manager+`) |
| `GET` | `/profile` | User profile & credential settings | Yes (`user+`) |
| `POST` | `/profile/password` | Update current password | Yes (`user+`) |
| `POST` | `/api/v1/auth/register` | REST API register endpoint (JSON) | No |
| `POST` | `/api/v1/auth/login` | REST API login endpoint (JSON) | No |
| `POST` | `/api/v1/auth/logout` | REST API logout endpoint (JSON) | Yes |
