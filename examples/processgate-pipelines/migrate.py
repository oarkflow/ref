#!/usr/bin/env python3
"""Convert ProcessGate flow bundles to standalone REF BCL examples.

The source tree is read only. Generated files are deterministic and require no
ProcessGate installation at runtime. Unsupported semantics are listed in the
manifest instead of silently pretending that they migrated.
"""

import argparse
import json
import re
from pathlib import Path


TYPES = {
    "text-input": "text", "textarea": "textarea", "number-input": "number",
    "date-picker": "date", "number": "number", "toggle": "boolean", "checkbox": "boolean",
    "file-upload": "file", "email-input": "email", "select": "select",
}
DECORATIVE = {"heading", "card", "row", "grid", "spacer", "html-block", "divider", "section", "paragraph"}
LAYOUTS = {"stacked", "tabbed", "wizard", "accordion", "grid"}
MODES = {"editable", "readonly", "summary", "hidden"}



def sla_block(minutes, config, stage, roles):
    """Render a stage SLA: deadline, pickup timeout and breach escalation."""
    lines = ["    sla {", f'      duration {q(str(minutes) + "m")}', '      on_breach "notify"']
    pickup = config.get("pickup_timeout_minutes")
    if isinstance(pickup, (float, int)) and 0 < pickup < minutes:
        lines += [f'      pickup_timeout {q(str(int(pickup)) + "m")}', '      on_pickup_timeout "notify"']
    if config.get("breach_action") == "notify_and_escalate":
        chain = [c for c in (stage.get("escalation_chain") or []) if c]
        step = max(minutes // 4, 1)
        for n, role in enumerate(chain, 1):
            name = roles.get(str(role), ident(role))
            lines += [f'      escalate {q("level_" + str(n))} {{', f'        after {q(str(step * n) + "m")}',
                      f'        assign_roles {arr([name])}', f'        notify {arr([name])}', "      }"]
        if not chain:
            lines += ['      escalate "manager" {', f'        after {q(str(step) + "m")}', '        notify ["manager"]', "      }"]
    lines.append("    }")
    return lines

def ident(value):
    name = re.sub(r"[^a-zA-Z0-9_]+", "_", str(value or "")).strip("_").lower()
    return ("n_" + name) if name[:1].isdigit() else (name or "unnamed")


def q(value):
    return json.dumps(str(value), ensure_ascii=False)


def arr(values):
    return "[" + ", ".join(q(v) for v in values) + "]"


def load(path):
    return json.loads(path.read_text())


def sources(root):
    bundles = {}
    for path in sorted((root / "bundles").rglob("*.import.json")):
        data = load(path)
        if isinstance(data.get("pipeline"), dict):
            slug = data["pipeline"].get("slug") or data["pipeline"].get("code")
            if slug:
                bundles[slug] = (data, str(path.relative_to(root)))
    # Export bundles fill gaps, but imports are the source of truth when both exist.
    for path in sorted((root / "bundles").rglob("*.export.json")):
        data = load(path)
        if isinstance(data.get("pipeline"), dict):
            slug = data["pipeline"].get("slug") or data["pipeline"].get("code")
            if slug and slug not in bundles:
                bundles[slug] = (data, str(path.relative_to(root)))
    for folder in sorted((root / "generated/government-ministry-pipelines").glob("*")):
        if not (folder / "pipeline.json").exists():
            continue
        data = {"pipeline": load(folder / "pipeline.json")}
        for key in ("forms", "stages", "pages", "snippets"):
            data[key] = [load(p) for p in sorted((folder / key).glob("*.json"))]
        data["roles"] = load(folder / "roles.json").get("roles", [])
        slug = data["pipeline"]["slug"]
        bundles[slug] = (data, str(folder.relative_to(root)))
    return bundles


def form_fields(form, warnings):
    roots = form.get("fields") or (form.get("schema") or {}).get("components") or []
    fields = []
    def visit(items):
        for field in items:
            if not isinstance(field, dict):
                continue
            fields.append(field)
            visit(field.get("children") or [])
    visit(roots)
    result, seen = [], set()
    for field in fields:
        typ = field.get("type")
        if typ in DECORATIVE:
            continue
        if typ not in TYPES:
            warnings.add("unmapped field widget: " + str(typ))
            continue
        name = ident(field.get("key") or field.get("name") or field.get("id"))
        if name in seen:
            warnings.add("duplicate field: " + name)
            continue
        seen.add(name)
        kind = TYPES[typ]
        options = []
        if kind == "select":
            for option in field.get("options") or []:
                value = option.get("value") if isinstance(option, dict) else option
                if value is not None:
                    options.append(str(value))
            if not options:
                warnings.add("select without options: " + name)
                kind = "text"
        required = bool(field.get("required")) or any(
            v.get("type") == "required" for v in field.get("validation", []) if isinstance(v, dict)
        )
        lines = [f'    input {q(name)} {{', f'      label {q(field.get("label") or field.get("label_en") or name)}', f'      kind {kind}']
        if required:
            lines.append("      required true")
        if options:
            lines.append("      options " + arr(options))
        if field.get("placeholder"):
            lines.append("      placeholder " + q(field["placeholder"]))
        if kind == "file":
            accept = field.get("accept") or field.get("file_types") or []
            if isinstance(accept, str):
                accept = [accept]
            if accept:
                lines.append("      accept " + arr(accept))
        lines.append("    }")
        result.extend(lines)
    return result


def refs(groups):
    result = []
    for group in groups or []:
        if isinstance(group, str):
            result.append((group, [], "editable"))
            continue
        if not isinstance(group, dict):
            continue
        forms = group.get("forms") or []
        names = []
        for form in forms:
            if isinstance(form, str):
                names.append(form)
            elif isinstance(form, dict):
                name = form.get("key") or form.get("formName") or form.get("formSlug") or form.get("name")
                if name:
                    names.append(name)
        if group.get("form"):
            names.append(group["form"])
        result.append((group.get("key") or group.get("group_key") or group.get("name") or "group", names, group.get("mode") or "editable"))
    return result


def convert(slug, data, source):
    p = data["pipeline"]
    warnings = set()
    name = ident(slug)
    forms = {}
    lines = [f'pipeline {q(name)} {{', f'  title {q(p.get("name") or slug)}']
    if p.get("description"):
        lines.append("  description " + q(p["description"]))
    for form in data.get("forms") or []:
        raw = form.get("key") or form.get("name") or form.get("slug")
        if not raw:
            continue
        fid = ident(raw)
        fields = form_fields(form, warnings)
        if not fields:
            warnings.add("form without migrated inputs: " + str(raw))
            continue
        if fid in forms:
            warnings.add("duplicate form: " + fid)
            continue
        forms[fid] = True
        lines += [f'  form {q(fid)} {{', f'    title {q(form.get("name") or raw)}'] + fields + ["  }"]
    roles = {}
    for role in data.get("roles") or []:
        if isinstance(role, dict):
            roles[str(role.get("id"))] = ident(role.get("slug") or role.get("key") or role.get("name"))
    stages = sorted(data.get("stages") or [], key=lambda s: s.get("stage_order") or s.get("order") or 0)
    external = (p.get("access_config") or {}).get("external") or {}
    if (external.get("registration") or {}).get("verification_method") not in (None, "", "none", "totp") and (external.get("registration") or {}).get("required"):
        warnings.add("source registration verification differs from authenticator-app TOTP")
    payment = external.get("payment") or {}
    if payment.get("enabled"):
        amount = payment.get("amount") or 0
        try:
            minor = round(float(amount) * 100)
        except (TypeError, ValueError):
            minor = 0
        lines += ['  payment {', '    currency ' + q(payment.get('currency') or 'NPR'), f'    amount {minor}', '    verifier_roles ["payment_officer", "admin"]', '  }']
        if minor == 0:
            warnings.add("source payment amount is zero; a trusted verifier must supply the assessed fee")
        if not payment.get("gateways"):
            warnings.add("source names no payment gateway; configure a provider verification intent or use manual officer verification")
    if (p.get("multi_date_enabled") or (p.get("flags") or {}).get("enable_multi_date")) and not any(s.get("allows_multi_dates") for s in data.get("stages") or []):
        warnings.add("pipeline enables multi-date but no stage declares schedule support")
    if (p.get("flags") or {}).get("enable_rfi"):
        lines.append("  allow_rfi true")
        warnings.add("RFI response payload is stored without a source form schema binding")
    notifications = p.get("notifications") or {}
    events = set()
    names_to_events = {
        "on_submit": "case.started", "submission": "case.started", "submitted": "case.started", "confirmation": "case.started",
        "on_rfi": "case.information_requested", "rfi": "case.information_requested",
        "on_approval": "case.completed", "approval": "case.completed", "approved": "case.completed",
        "on_rejection": "case.rejected", "rejected": "case.rejected", "rejection": "case.rejected",
        "on_issuance": "case.completed", "completion": "case.completed", "completed": "case.completed",
        "reminder": "sla.warning", "maturity_reminder": "sla.warning",
        "issued": "case.completed", "issuance": "case.completed",
        "status_change": "stage.completed", "assigned": "assigned",
    }
    if isinstance(notifications, dict):
        for key, enabled in notifications.items():
            if enabled is True and key in names_to_events:
                events.add((names_to_events[key], ""))
            elif enabled or (isinstance(enabled, list) and enabled):
                warnings.add("unmapped notification rule: " + str(key))
    elif isinstance(notifications, list):
        for key in notifications:
            if key in names_to_events:
                events.add((names_to_events[key], ""))
            elif key in ("verification", "review", "approval"):
                matched = [s for s in stages if s.get("requires_approval" if key == "approval" else "requires_verification")]
                for stage in matched:
                    events.add(("stage.completed", ident(stage.get("key") or stage.get("status_key") or stage.get("name"))))
                if not matched:
                    warnings.add("unmapped notification event: " + str(key))
            else:
                matched = [s for s in stages if key in (s.get("key"), s.get("status_key"), ident(s.get("name")))]
                if matched:
                    for stage in matched:
                        events.add(("stage.completed", ident(stage.get("key") or stage.get("status_key") or stage.get("name"))))
                else:
                    warnings.add("unmapped notification event: " + str(key))
    for event, stage in sorted(events):
        lines += [f'  notify {q(event)} {{', '    to ["applicant", "assignee"]', '    channels ["in_app"]', f'    subject {q("ProcessGate case {case.number}: " + event)}', f'    body {q("Case {case.number} has event " + event)}']
        if stage:
            lines.append('    stage ' + q(stage))
        lines.append('  }')
    if p.get("rules") or p.get("unified_rules"):
        warnings.add("ProcessGate expression rules need manual translation")
    for idx, stage in enumerate(stages):
        sid = ident(stage.get("key") or stage.get("status_key") or stage.get("name") or f"stage_{idx+1}")
        kind = "submission" if idx == 0 else "approval" if stage.get("requires_approval") else "review" if stage.get("requires_verification") else "processing"
        lines += [f'  stage {q(sid)} {{', f'    title {q(stage.get("name") or sid)}', f'    kind {kind}']
        if idx == 0 and external.get("enabled"):
            lines.append("    public true")
            if external.get("auth_required"):
                lines.append("    require_account true")
        if stage.get("allows_multi_dates") or stage.get("multi_date") or ((stage.get("config") or {}).get("flags") or {}).get("allows_multi_dates"):
            lines.append("    multi_date true")
        stage_roles = stage.get("allowed_role_ids") or []
        if not stage_roles:
            binding = stage.get("role_binding") or stage.get("owner_role")
            stage_roles = [binding] if binding else []
        names = [roles.get(str(r), ident(r)) for r in stage_roles if r]
        if names:
            lines.append("    roles " + arr(dict.fromkeys(names)))
        stage_flags = (stage.get("config") or {}).get("flags") or {}
        if stage_flags.get("requires_assignment") and idx > 0 and names:
            lines.append("    claimable true")
            lines.append("    assign_roles " + arr(dict.fromkeys(names)))
        sla_config = stage.get("sla_config") or {}
        minutes = stage.get("sla_minutes") or sla_config.get("duration_minutes")
        if isinstance(minutes, (float, int)) and minutes > 0:
            lines += sla_block(int(minutes), sla_config, stage, roles)
        groups = refs(stage.get("form_groups") or (stage.get("config") or {}).get("form_groups"))
        if groups and all(not names for _, names, _ in groups):
            mapping = {ident(g.get("key")): [ident(f) for f in g.get("forms") or []] for g in data.get("form_groups") or [] if isinstance(g, dict)}
            groups = [(key, mapping.get(ident(key), []), mode) for key, _, mode in groups]
        seen = set()
        blocks = []
        for key, raw_names, mode in groups:
            names = [ident(n) for n in raw_names if ident(n) in forms]
            if not names:
                warnings.add("stage group has no migrated forms: " + str(key))
                continue
            gid = ident(key)
            if gid in seen:
                gid += "_" + str(len(seen) + 1)
            seen.add(gid)
            blocks += [f'      group {q(gid)} {{', f'        title {q(key)}', "        forms " + arr(names)]
            if mode in MODES and mode != "editable":
                blocks.append("        mode " + mode)
            blocks.append("      }")
        if blocks:
            lines += ["    page {", f'      title {q(stage.get("name") or sid)}', "      layout wizard"] + blocks + ["    }"]
        stage_forms = list(dict.fromkeys(ident(n) for _, raw_names, _ in groups for n in raw_names if ident(n) in forms))
        if stage.get("requires_verification") and stage_forms:
            lines += ['    node "verification" {', '      title "Verify submitted forms"', '      kind review', '      forms ' + arr(stage_forms), '    }']
        elif stage.get("requires_verification"):
            warnings.add("verification stage has no migrated forms")
        if stage.get("requires_approval"):
            count = stage.get("approval_count") or 1
            if not isinstance(count, int) or count < 1:
                count = 1
            lines += ['    node "approval" {', '      title "Approve stage"', '      kind approval', f'      approvals {count}']
            if stage.get("prevent_self_approval"):
                lines.append('      distinct_from ["applicant"]')
            lines.append('    }')
        if stage.get("requires_approval"):
            warnings.add("ProcessGate approval identity and assignment policy needs manual review")
        if stage.get("requires_verification"):
            warnings.add("ProcessGate field verification scope needs manual review")
        if idx > 0 and stage.get("rollback_allowed"):
            lines += ['    action "rollback" {', '      label "Return to previous stage"', '      outcome return', '      return_to ' + q(ident(stages[idx - 1].get("key") or stages[idx - 1].get("status_key") or stages[idx - 1].get("name") or f"stage_{idx}")), '      comment_required true', '    }']
        if stage.get("rollback_allowed") and idx == 0:
            warnings.add("first stage rollback has no preceding stage")
        lines += [f'    action {q("submit" if idx == 0 else "complete")} {{', f'      label {q("Submit" if idx == 0 else "Complete stage")}', '      outcome advance', '    }', "  }"]
    lines.append("}")
    return "\n".join(lines) + "\n", {"name": name, "slug": slug, "source": source, "forms": len(forms), "stages": len(stages), "warnings": sorted(warnings)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path.home() / "Sites/processgate/docs/flows")
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parent)
    args = parser.parse_args()
    bundles = sources(args.source)
    if not bundles:
        parser.error("no ProcessGate pipeline bundles found")
    config = args.output / "config"
    config.mkdir(parents=True, exist_ok=True)
    manifest = []
    for slug, (data, source) in sorted(bundles.items()):
        content, item = convert(slug, data, source)
        (config / (item["name"] + ".bcl")).write_text(content)
        manifest.append(item)
    names = [item["name"] for item in manifest]
    resource = '\n'.join([
        'name "processgate_pipelines"', 'version "1"',
        'resource "database" {', '  kind "database.sql"', '  config {',
        '    driver "sqlite"', '    dsn env("PROCESSGATE_PIPELINES_DSN", "file:.data/processgate-pipelines.db?_pragma=busy_timeout(5000)")',
        '    migrations ["CREATE TABLE IF NOT EXISTS processgate_messages (id INTEGER PRIMARY KEY AUTOINCREMENT, tenant_id TEXT NOT NULL, user_id TEXT NOT NULL, channel TEXT NOT NULL, subject TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)"]',
        '  }', '}', 'resource "cases" {', '  kind "pipeline.cases"', '  config {',
        '    database "database"', '    pipelines ' + arr(names),
        '    storage "files"',
        '    allow_anonymous true',
        '    notify_channels { in_app "notifications.deliver" }',
        '    signing_secret env.required("PROCESSGATE_PIPELINES_SIGNING_SECRET")',
        '  }', '}',
        'resource "files" {', '  kind "storage.fs"', '  config {',
        '    dir env("PROCESSGATE_PIPELINES_FILES", ".data/files")',
        '  }', '}',
        'resource "jwt" {', '  kind "auth.jwt"', '  config {',
        '    secret env.required("PROCESSGATE_PIPELINES_JWT_SECRET")',
        '  }', '}',
        'resource "users" {', '  kind "identity.users"', '  config {',
        '    database "database"', '    token_issuer "jwt"',
        '    bootstrap {',
        '      email env.required("PROCESSGATE_PIPELINES_ADMIN_EMAIL")',
        '      password env.required("PROCESSGATE_PIPELINES_ADMIN_PASSWORD")',
        '      tenant "processgate"', '      roles ["admin"]',
        '    }', '  }', '}',
        'resource "limits" {', '  kind "ratelimit.memory"', '}',
        ''
    ])
    operations = {
        "catalog": ("pipeline.catalog", "pipelines", False, False),
        "describe": ("pipeline.describe", "pipeline", False, False),
        "start": ("pipeline.start", "case", True, True),
        "list": ("pipeline.list", "cases", False, False),
        "get": ("pipeline.get", "case", False, False),
        "view": ("pipeline.view", "view", False, False),
        "save": ("pipeline.save", "view", True, True),
        "act": ("pipeline.act", "view", True, True),
        "node": ("pipeline.node", "view", True, True),
        "work": ("pipeline.work", "view", True, True),
        "upload": ("pipeline.upload", "view", True, True),
        "file": ("pipeline.file", "file", False, False),
        "schedule_list": ("pipeline.schedule_list", "schedules", False, False),
        "schedule_create": ("pipeline.schedule_create", "view", True, True),
        "schedule_submit": ("pipeline.schedule_submit", "view", True, True),
        "payment_record": ("pipeline.payment_record", "view", True, True),
        "request_information": ("pipeline.request_information", "view", True, True),
        "submit_information": ("pipeline.submit_information", "view", True, True),
    }
    api = []
    api += [
        'intent "notifications.deliver" {', '  response "stored"', '  node "stored" {',
        '    uses "database.exec"', '    resource "database"', '    kind effect', '    requires [input]', '    provides [stored]',
        '    config {',
        '      statement "INSERT INTO processgate_messages (tenant_id, user_id, channel, subject, body) VALUES ($1, $2, $3, $4, $5)"',
        '      args ["input.tenant_id", "input.user", "input.channel", "input.subject", "input.body"]',
        '    }', '  }', '}', '',
        'intent "notifications.mine" {', '  response "messages"', '  node "messages" {',
        '    uses "database.crud"', '    resource "database"', '    provides [messages]',
        '    config {', '      operation "list"', '      table "processgate_messages"',
        '      columns ["id", "tenant_id", "user_id", "channel", "subject", "body", "created_at"]',
        '      tenant_column "tenant_id"', '      owner_column "user_id"', '      order_by "id"', '      descending true',
        '    }', '  }', '}', '',
        'route "notifications.mine" {', '  method GET', '  path "/api/notifications"',
        '  intent "notifications.mine"', '  auth "jwt"', '}', ''
    ]
    for name, action, output, action_resource, action_config in [
        ('register', 'identity.register_totp', 'registration', 'users', ['tenant "processgate"', 'role "applicant"', 'issuer "REF ProcessGate Pipelines"']),
        ('confirm', 'identity.confirm_totp', 'registration', 'users', []),
        ('login', 'identity.login', 'session', 'users', []),
        ('invite', 'identity.admin', 'invitation', 'users', ['operation "invite"']),
        ('accept_invite', 'identity.accept_invite', 'member', 'users', []),
    ]:
        api += [f'intent "auth.{name}" {{', f'  response "{output}"', f'  node "{output}" {{', f'    uses "{action}"', f'    resource "{action_resource}"', '    kind effect', '    requires [input]', f'    provides [{output}]']
        if action_config:
            api += ['    config {'] + ['      ' + line for line in action_config] + ['    }']
        api += ['  }', '}', '']
    for name, path, protected in [
        ('register', '/api/auth/register', False),
        ('confirm', '/api/auth/register/confirm', False),
        ('login', '/api/auth/login', False),
        ('invite', '/api/auth/invite', True),
        ('accept_invite', '/api/auth/invitations/accept', False),
    ]:
        api += [f'route "auth.{name}" {{', '  method POST', f'  path {q(path)}', f'  intent "auth.{name}"', '  cache_control "no-store"']
        if protected:
            api.append('  auth "jwt"')
        else:
            api.append('  rate_limit { limiter "limits" limit 10 window 1m }')
        api += ['}', '']
    for name, (action, result, effect, needs_input) in operations.items():
        api += [f'intent "cases.{name}" {{', f'  response "{result}"', f'  node "{result}" {{', f'    uses "{action}"', '    resource "cases"']
        if effect:
            api.append('    kind effect')
        if needs_input:
            api.append('    requires [input]')
        api += [f'    provides [{result}]', '  }', '}', '']
        if name == 'payment_record':
            api[-3:-3] = ['    config {', '      verify_intent env("PROCESSGATE_PAYMENT_VERIFY_INTENT", "")', '    }']
    routes = [
        ('catalog', 'GET', '/api/pipelines', False),
        ('describe', 'GET', '/api/pipelines/:pipeline', False),
        ('start', 'POST', '/api/pipelines/:pipeline/cases', True),
        ('list', 'GET', '/api/pipelines/:pipeline/cases', False),
        ('get', 'GET', '/api/pipelines/:pipeline/cases/:id', False),
        ('view', 'GET', '/api/pipelines/:pipeline/cases/:id/stages/:stage', False),
        ('save', 'PUT', '/api/pipelines/:pipeline/cases/:id/stages/:stage', False),
        ('act', 'POST', '/api/pipelines/:pipeline/cases/:id/stages/:stage/actions/:action', False),
        ('node', 'POST', '/api/pipelines/:pipeline/cases/:id/stages/:stage/nodes/:node/:verb', False),
        ('work', 'POST', '/api/pipelines/:pipeline/cases/:id/stages/:stage/work/:op', False),
        ('upload', 'POST', '/api/pipelines/:pipeline/cases/:id/stages/:stage/files/:path', False),
        ('file', 'GET', '/api/pipelines/:pipeline/cases/:id/files/:path', False),
        ('schedule_list', 'GET', '/api/pipelines/:pipeline/cases/:id/stages/:stage/schedules', False),
        ('schedule_create', 'POST', '/api/pipelines/:pipeline/cases/:id/stages/:stage/schedules', False),
        ('schedule_submit', 'POST', '/api/pipelines/:pipeline/cases/:id/stages/:stage/schedules/:schedule/submissions', False),
        ('payment_record', 'POST', '/api/pipelines/:pipeline/cases/:id/payments/verify', False),
        ('request_information', 'POST', '/api/pipelines/:pipeline/cases/:id/request-information', False),
        ('submit_information', 'POST', '/api/pipelines/:pipeline/cases/:id/submit-information', False),
    ]
    for name, method, path, created in routes:
        api += [f'route "{name}" {{', f'  method {method}', f'  path {q(path)}', f'  intent "cases.{name}"', '  auth "jwt"']
        if name in ('describe', 'start', 'get', 'view', 'save', 'act', 'upload', 'file', 'schedule_list', 'schedule_create', 'schedule_submit', 'request_information', 'submit_information'):
            api.append('  allow_anonymous true')
        if created:
            api.append('  status 201')
        if name == 'upload':
            api.append('  max_body_bytes 10485760')
        api += ['}', '']
    # SLA warnings, breaches, pickup timeouts and escalations are applied by
    # the sweep: on a schedule through the queue, and on demand for admins.
    api += [
        'static "app" {', '  prefix "/app"', '  root env("PROCESSGATE_PIPELINES_STATIC", "static")', '  index "index.html"', '}', '',
        'resource "bus" {', '  kind "queue.sql"', '  description "Durable queue so one replica fires each scheduled sweep"',
        '  config {', '    database "database"', '    table "processgate_jobs"', '    workers 1', '  }', '}', '',
        'intent "sla.sweep" {', '  response "sweep"', '  node "sweep" {', '    uses "pipeline.sweep"', '    resource "cases"',
        '    kind effect', '    provides [sweep]', '    config {', '      roles ["admin"]', '    }', '  }', '}', '',
        'route "sla.sweep" {', '  method POST', '  path "/api/admin/sla/sweep"', '  intent "sla.sweep"', '  auth "jwt"', '}', '',
        'schedule "sla_sweep" { every 1m queue "bus" intent "sla.sweep" }', '',
    ]
    (config / '01_api.bcl').write_text('\n'.join(api))
    (config / "00_resources.bcl").write_text(resource)
    (args.output / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    print(f"generated {len(manifest)} pipelines, {sum(x['stages'] for x in manifest)} stages, {sum(x['forms'] for x in manifest)} forms")


if __name__ == "__main__":
    main()
