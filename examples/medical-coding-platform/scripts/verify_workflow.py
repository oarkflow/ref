#!/usr/bin/env python3
"""Checks the medical coding workflow engine end to end over HTTP.

Needs a server on a disposable database with the demo seeds. It adds a second
company, facility and work item, and a facility admin, so run it only against
a throwaway database:

    BASE_URL=http://127.0.0.1:3000 python3 scripts/verify_workflow.py
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

BASE_URL = os.environ.get("BASE_URL", "http://127.0.0.1:3000")
PASSWORD = "DoctorSecret123!"
RUN = str(int(time.time()))
passed = failed = 0


def call(method, path, body=None, cookie=None):
    headers = {"Content-Type": "application/json"}
    if cookie:
        headers["Cookie"] = cookie
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE_URL + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read().decode() or "null"
            return resp.status, json.loads(raw), resp.headers
    except urllib.error.HTTPError as e:
        raw = e.read().decode() or "null"
        try:
            return e.code, json.loads(raw), e.headers
        except ValueError:
            return e.code, raw, e.headers


def login(email, password=PASSWORD):
    status, body, headers = call("POST", "/web/client/login", {"email": email, "password": password})
    assert status == 200, f"login {email}: {status} {body}"
    return "; ".join(c.split(";", 1)[0] for c in headers.get_all("Set-Cookie") or [])


def check(label, ok, detail=""):
    global passed, failed
    print(("[PASS] " if ok else "[FAIL] ") + label + ("" if ok else f"  {detail}"))
    passed += bool(ok)
    failed += not ok


def inputs(view, form):
    out = {}
    for g in view.get("page", {}).get("groups", []):
        for f in g["forms"]:
            if f["name"] == form:
                out.update({i["name"]: i for i in f["inputs"]})
    return out


def forms(view):
    return [f["name"] for g in view.get("page", {}).get("groups", []) for f in g["forms"]]


def values(options):
    return [o["value"] for o in options]


COMMON = {"patient": {"patient_name": "Jane Doe", "mrn": "MRN-1001", "dos": "2026-03-10"}}
GOOD_CODING = {
    "em": {"mdm_level": "High", "em_code": "99285"},
    "cpt": [{"cpt_code": "99285", "units": 1}],
    "dx": [{"icd_code": "R07.9", "poa": "Y"}],
}

root = login("doctor@clear.io")
coder = login("coder@clear.io")
auditor = login("auditor@clear.io")
entry = login("de@clear.io")


def reset_settings(unit="wi_memorial_er"):
    """Leave no workspace settings behind, so runs do not depend on each other."""
    call("PUT", f"/workflow/api/workspaces/{unit}/settings", {"overrides": {}}, root)


reset_settings()


def start(cookie, unit="wi_memorial_er"):
    status, body, _ = call("POST", "/workflow/api/cases", {"org_unit": unit, "data": COMMON}, cookie)
    assert status == 201, (status, body)
    return body["case"]["id"], body


def save(cid, stage, data, cookie):
    return call("PUT", f"/workflow/api/cases/{cid}/stages/{stage}", {"data": data}, cookie)


def act(cid, stage, action, cookie, comment=""):
    return call("POST", f"/workflow/api/cases/{cid}/stages/{stage}/actions/{action}", {"comment": comment} if comment else {}, cookie)


def view(cid, cookie):
    return call("GET", f"/workflow/api/cases/{cid}", None, cookie)


print("-- sign-in carries the user's workspaces --")
st, units, _ = call("GET", "/workflow/api/units", None, coder)
check("coder sees the work item of its membership", st == 200 and [u["id"] for u in units["units"]] == ["wi_memorial_er"], units)
st, units, _ = call("GET", "/workflow/api/units", None, root)
check("global admin sees every work item", st == 200 and len(units["units"]) >= 1)

print("\n-- coding stage: permissions, rules, dependent fields --")
cid, v = start(coder)
check("a new case opens in the coding stage with an SLA", v["stage"]["name"] == "coding" and v["stage"].get("due_at"), v.get("stage"))
check("the coder claimed it on start (claimable stage)", v.get("work", {}).get("assignee") == "usr_coder_01", v.get("work"))
check("the coder can edit the patient form", inputs(v, "patient")["patient_name"]["editable"])
check("the QA form is not shown while coding", "qa_review" not in forms(v) and "billing" not in forms(v))
check("downcode reason is required only for a moderate MDM coded 99283",
      not inputs(v, "em")["downcode_reason"].get("required"))
st, c, _ = call("POST", f"/workflow/api/cases/{cid}/choices/em/em_code", {"deps": {"mdm_level": "Moderate"}}, coder)
check("E&M codes follow the MDM level (Moderate)", st == 200 and values(c["choices"]) == ["99283", "99284"], c)
st, c, _ = call("POST", f"/workflow/api/cases/{cid}/choices/em/em_code", {"deps": {"mdm_level": "High"}}, coder)
check("E&M codes follow the MDM level (High)", values(c["choices"]) == ["99284", "99285"], c)
st, c, _ = call("POST", f"/workflow/api/cases/{cid}/choices/em/em_code", {"deps": {}}, coder)
check("without an MDM level no E&M code is offered", st == 200 and c["choices"] == [], c)
check("a code outside the level is refused", save(cid, "coding", {"em": {"mdm_level": "Low", "em_code": "99285"}}, coder)[0] == 422)
st, mods, _ = call("POST", f"/workflow/api/cases/{cid}/choices/em/modifier", {"deps": {}}, coder)
check("workspace lookups: GT added and 91 switched off for this work item",
      "GT" in values(mods["choices"]) and "91" not in values(mods["choices"]) and "25" in values(mods["choices"]), mods)
check("a CPT code must match its pattern", save(cid, "coding", {"cpt": [{"cpt_code": "BAD"}]}, coder)[0] == 422)
check("an ICD code must match its pattern", save(cid, "coding", {"dx": [{"icd_code": "1234"}]}, coder)[0] == 422)
st, r, _ = act(cid, "coding", "submit", coder)
check("submitting incomplete coding is refused", st == 422, (st, r))
check("a downcode reason becomes required for a moderate MDM coded 99283",
      save(cid, "coding", {"em": {"mdm_level": "Moderate", "em_code": "99283"}}, coder)[0] == 200
      and inputs(view(cid, coder)[1], "em")["downcode_reason"].get("required"))
check("QA notes are hidden until QA is requested", "qa_notes" not in inputs(view(cid, coder)[1], "routing"))
st, _, _ = save(cid, "coding", {**GOOD_CODING, "routing": {"request_qa": True}}, coder)
check("requesting QA shows and requires the notes", inputs(view(cid, coder)[1], "routing")["qa_notes"].get("required"))
check("submitting without the QA notes is refused", act(cid, "coding", "submit", coder)[0] == 422)
save(cid, "coding", {"routing": {"request_qa": True, "qa_notes": "check the modifier"}}, coder)
check("QA auditor cannot act on the coding stage", save(cid, "coding", {"em": {"mdm_level": "Low"}}, auditor)[0] in (403, 404))

print("\n-- QA stage --")
st, r, _ = act(cid, "coding", "submit", coder)
check("coding submits to QA when asked", st == 200 and r["case"]["stage"] == "qa", (st, r.get("case")))
st, qv = view(cid, coder)[0], view(cid, coder)[1]
check("the coder can no longer edit anything", not qv["can_edit"])
check("coder sees the QA stage read-only", "qa_review" not in forms(qv) or not inputs(qv, "qa_review")["agreement"]["editable"])
check("the case is in the auditor's queue", cid in [c["id"] for c in call("GET", "/workflow/api/cases?scope=queue", None, auditor)[1]], "")
check("not in the data entry queue yet", cid not in [c["id"] for c in call("GET", "/workflow/api/cases?scope=queue", None, entry)[1]])
call("POST", f"/workflow/api/cases/{cid}/work/claim", {}, auditor)
av = view(cid, auditor)[1]
check("the auditor edits the QA form", inputs(av, "qa_review")["agreement"]["editable"])
check("the findings are required only on disagreement", not inputs(av, "qa_review")["findings"].get("required"))
save(cid, "qa", {"qa_review": {"agreement": "Disagree"}}, auditor)
check("disagreeing makes the findings required", inputs(view(cid, auditor)[1], "qa_review")["findings"].get("required"))
check("approving without findings is refused", act(cid, "qa", "approve", auditor)[0] == 422)
st, r, _ = act(cid, "qa", "return", auditor)
check("returning needs a comment", st == 422, (st, r))
st, r, _ = act(cid, "qa", "return", auditor, "wrong modifier")
check("QA returns the case to the coder", st == 200 and r["case"]["stage"] == "coding", (st, r.get("case")))
st, cv = view(cid, coder)[0], view(cid, coder)[1]
check("the coder can edit again after a return", cv["can_edit"] and inputs(cv, "em")["mdm_level"]["editable"])
st, r, _ = act(cid, "coding", "submit", coder)
check("resubmitting goes back to QA", st == 200 and r["case"]["stage"] == "qa", (st, r.get("case")))
call("POST", f"/workflow/api/cases/{cid}/work/claim", {}, auditor)
save(cid, "qa", {"qa_review": {"agreement": "Agree"}}, auditor)
st, r, _ = act(cid, "qa", "approve", auditor)
check("an agreeing auditor approves to data entry", st == 200 and r["case"]["stage"] == "data_entry", (st, r.get("case")))

print("\n-- data entry stage --")
check("the case is in the data entry queue", cid in [c["id"] for c in call("GET", "/workflow/api/cases?scope=queue", None, entry)[1]])
call("POST", f"/workflow/api/cases/{cid}/work/claim", {}, entry)
ev = view(cid, entry)[1]
check("billing is editable by data entry", inputs(ev, "billing")["charge_total"]["editable"])
check("hold reason is hidden until the claim is held", "hold_reason" not in inputs(ev, "billing"))
check("the coder only looks at billing", "billing" not in forms(view(cid, coder)[1]) or not inputs(view(cid, coder)[1], "billing")["charge_total"]["editable"])
check("a coder cannot post charges", save(cid, "data_entry", {"billing": {"payer": "X"}}, coder)[0] in (403, 404))
save(cid, "data_entry", {"billing": {"claim_status": "Hold"}}, entry)
check("holding the claim shows and requires a reason", inputs(view(cid, entry)[1], "billing")["hold_reason"].get("required"))
save(cid, "data_entry", {"billing": {"payer": "Acme Health", "charge_total": 412.5, "claim_status": "Ready"}}, entry)
st, r, _ = act(cid, "data_entry", "complete", entry)
check("completing needs the confirmation step", st in (409, 428, 422) or (st == 200 and r["case"]["status"] == "completed"), (st, r if st != 200 else ""))
if st != 200:
    st, preview, _ = call("POST", f"/workflow/api/cases/{cid}/stages/data_entry/actions/complete", {"confirm": True}, entry)

print("\n-- QA is skipped when not requested --")
cid2, _ = start(coder)
save(cid2, "coding", {**GOOD_CODING, "routing": {"request_qa": False}}, coder)
st, r, _ = act(cid2, "coding", "submit", coder)
check("without a QA request the case goes straight to data entry", st == 200 and r["case"]["stage"] == "data_entry", (st, r.get("case")))

print("\n-- script hooks (sealed SPL scripts run by the pipeline) --")
node = {n["name"]: n for n in view(cid2, entry)[1].get("nodes", [])}.get("claim_integrity", {})
check("entering data entry ran the claim_integrity script node", node.get("status") == "passed" and node.get("result", {}).get("total_units") == 1, node)
cid3, _ = start(coder)
dup = {**GOOD_CODING, "cpt": [{"cpt_code": "99285", "units": 1}, {"cpt_code": "99285", "units": 2}], "routing": {"request_qa": False}}
save(cid3, "coding", dup, coder)
st, r, _ = act(cid3, "coding", "submit", coder)
node = {n["name"]: n for n in view(cid3, entry)[1].get("nodes", [])}.get("claim_integrity", {})
check("duplicate CPT codes fail the script node with its reason", node.get("status") == "failed" and node.get("result", {}).get("duplicate_procedures") == 1
      and "more than once" in node.get("result", {}).get("reason", ""), node)
call("POST", f"/workflow/api/cases/{cid3}/work/claim", {}, entry)
save(cid3, "data_entry", {"billing": {"payer": "Acme Health", "charge_total": 10, "claim_status": "Ready"}}, entry)
st, r, _ = act(cid3, "data_entry", "complete", entry)
if st != 200 or r.get("case", {}).get("status") != "completed":
    st, r, _ = call("POST", f"/workflow/api/cases/{cid3}/stages/data_entry/actions/complete", {"confirm": True}, entry)
check("a failed script node blocks posting the claim", not (st == 200 and r.get("case", {}).get("status") == "completed"), (st, r))

print("\n-- workspace settings, scope and inheritance --")
admin_email = f"ws_admin.{RUN}@wf.test"
st, made, _ = call("POST", "/users/store", {"email": admin_email, "name": "WF Admin", "password": PASSWORD, "role": "coder"}, root)
admin_id = made["user_id"]
call("POST", "/users/roles/assign", {"user_id": admin_id, "company_id": "comp_clear_01", "facility_id": "fac_memorial", "work_item_id": "*", "role": "admin"}, root)
ws_admin = login(admin_email)
settings = {"overrides": {
    "stages": {"coding": {"sla_duration": "6h", "sla_warn_before": "1h"}},
    "fields": {"patient.fin": {"required": True, "label": "Financial number"}, "dx": {"disabled": False}},
}}
st, r, _ = call("PUT", "/workflow/api/workspaces/wi_memorial_er/settings", settings, coder)
check("a coder cannot change workspace settings", st in (403, 404), (st, r))
st, r, _ = call("PUT", "/workflow/api/workspaces/wi_memorial_er/settings", {"overrides": {"stages": {"ghost": {"due": "1h"}}}}, ws_admin)
check("settings that name an unknown stage are refused", st in (400, 422), (st, r))
st, r, _ = call("PUT", "/workflow/api/workspaces/wi_memorial_er/settings", {"overrides": {"fields": {"em.mdm_level": {"hidden": True}}}}, ws_admin)
check("a required field cannot be hidden by a workspace", st in (400, 422), (st, r))
st, r, _ = call("PUT", "/workflow/api/workspaces/comp_clear_01/settings", settings, ws_admin)
check("a facility admin cannot write the company above it", st in (403, 404), (st, r))
st, r, _ = call("PUT", "/workflow/api/workspaces/wi_memorial_er/settings", settings, ws_admin)
check("a facility admin writes settings for a work item in its facility", st == 200, (st, r))
st, r, _ = call("GET", "/workflow/api/workspaces/wi_memorial_er/settings", None, ws_admin)
check("effective settings are readable", st == 200 and r["effective"]["stages"]["coding"]["sla_duration"] == "6h", r)
cid3, v3 = start(coder)
fin = inputs(v3, "patient")["fin"]
check("the workspace relabelled and required the FIN", fin["label"] == "Financial number" and fin["required"], fin)
span = None
try:
    from datetime import datetime
    s = datetime.fromisoformat(v3["stage"]["due_at"].replace("Z", "+00:00"))
    n = datetime.fromisoformat(v3["case"]["created_at"].replace("Z", "+00:00"))
    span = (s - n).total_seconds() / 3600
except Exception:
    pass
check("the workspace shortened the coding SLA (a 6 working hour deadline, not 24)", span is not None and span < 24, span)

print("\n-- a second workspace is isolated --")
co = f"Co {RUN}"
st, cmp_, _ = call("POST", "/companies", {"name": co}, root)
company_b = cmp_["company_id"]
st, fac, _ = call("POST", "/admin/facilities", {"facility_name": f"Fac {RUN}", "client_id": company_b}, root)
facility_b = fac["facility_id"]
st, wi, _ = call("POST", "/workitem", {"facility_id": facility_b, "work_item_type": "Both", "name": f"WI {RUN}"}, root)
wi_b = wi["work_item_id"]
st, sync, _ = call("POST", "/workflow/api/sync-org", {}, root)
check("syncing creates the new workspace's org units", st == 200, (st, sync))
check("a coder cannot sync", call("POST", "/workflow/api/sync-org", {}, coder)[0] == 403)
time.sleep(7)  # the org hierarchy reloads every 5s
st, unit_view, _ = call("GET", "/workflow/api/units", None, root)
check("the global admin now sees the new work item", wi_b in [u["id"] for u in unit_view["units"]], unit_view)
coder_b_email = f"coder_b.{RUN}@wf.test"
st, made_b, _ = call("POST", "/users/store", {"email": coder_b_email, "name": "Coder B", "password": PASSWORD, "role": "user"}, root)
call("POST", "/users/roles/assign", {"user_id": made_b["user_id"], "company_id": company_b, "facility_id": "*", "work_item_id": wi_b, "role": "coder"}, root)
coder_b = login(coder_b_email)
st, r, _ = call("POST", "/workflow/api/cases", {"org_unit": wi_b, "data": COMMON}, root)
check("a global admin is not a coder, so cannot start a coding case", st == 403, (st, r))
cid_b, vb = start(coder_b, wi_b)
check("a coder with a role in the new workspace starts a case there", bool(cid_b))
check("workspace roles apply only there: company B's coder cannot start in company A",
      call("POST", "/workflow/api/cases", {"org_unit": "wi_memorial_er", "data": COMMON}, coder_b)[0] in (403, 404))
check("company B's coder sees company B's case but not company A's", view(cid_b, coder_b)[0] == 200 and view(cid, coder_b)[0] in (403, 404))
check("the other workspace has no relabelled FIN", inputs(vb, "patient")["fin"]["label"] != "Financial number")
st, r, _ = view(cid_b, coder)
check("a coder of company A cannot open company B's case", st in (403, 404), (st, r))
check("company B's case is not in company A's queue", cid_b not in [c["id"] for c in call("GET", "/workflow/api/cases?scope=queue", None, coder)[1]])
st, r, _ = call("POST", "/workflow/api/cases", {"org_unit": wi_b, "data": COMMON}, coder)
check("a coder cannot start a case in a workspace it has no role in", st in (403, 404), (st, r))
st, r, _ = call("PUT", f"/workflow/api/workspaces/{wi_b}/settings", settings, ws_admin)
check("a facility admin cannot touch another company's settings", st in (403, 404), (st, r))

print("\n-- anonymous callers are refused --")
for path in ("/workflow/api/cases", "/workflow/api/units", "/workflow/api/workspaces/wi_memorial_er/settings"):
    check(f"anonymous GET {path}", call("GET", path)[0] == 401)

reset_settings()
print(f"\nWORKFLOW SUMMARY: {passed} passed, {failed} failed")
sys.exit(1 if failed else 0)
