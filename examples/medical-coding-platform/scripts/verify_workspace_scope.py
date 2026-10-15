#!/usr/bin/env python3
"""Checks workspace-scoped administration: company, facility and work item admins.

Run against a server started on a disposable database (it adds test companies,
facilities, work items and users to DB_FILE):

    DB_FILE=/path/to/app.db BASE_URL=http://127.0.0.1:3000 python3 scripts/verify_workspace_scope.py
"""
import json
import os
import sqlite3
import sys
import time
import urllib.error
import urllib.request

BASE_URL = os.environ.get("BASE_URL", "http://127.0.0.1:3000")
DB_FILE = os.environ.get("DB_FILE")
if not DB_FILE or not os.path.exists(DB_FILE):
    sys.exit("set DB_FILE to the SQLite file the server is using")

PASSWORD = "ScopeTest123!"
RUN = str(int(time.time()))  # keeps reruns from colliding on unique emails


def call(method, path, body=None, cookie=None):
    headers = {"Content-Type": "application/json"}
    if cookie:
        headers["Cookie"] = cookie
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE_URL + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            raw = resp.read().decode() or "null"
            return resp.status, json.loads(raw), resp.headers
    except urllib.error.HTTPError as e:
        raw = e.read().decode() or "null"
        try:
            return e.code, json.loads(raw), e.headers
        except ValueError:
            return e.code, raw, e.headers


def login(email, password):
    status, _, headers = call("POST", "/web/client/login", {"email": email, "password": password})
    assert status == 200, f"login {email} -> {status}"
    return "; ".join(c.split(";", 1)[0] for c in headers.get_all("Set-Cookie") or [])


passed = failed = 0


def check(label, ok):
    global passed, failed
    print(("[PASS] " if ok else "[FAIL] ") + label)
    passed += ok
    failed += not ok


# --- workspace fixture: two companies, three facilities, four work items -----
db = sqlite3.connect(DB_FILE)
db.execute("INSERT OR IGNORE INTO companies (id, name, slug, status) VALUES ('cmp_b', 'Company B', 'company-b', 'active')")
for fid, client in (("fac_a2", "comp_clear_01"), ("fac_b1", "cmp_b")):
    db.execute("INSERT OR IGNORE INTO facilities (facility_id, facility_name, client_id, status) VALUES (?, ?, ?, 'active')", (fid, fid, client))
for wid, fac in (("wi_a1b", "fac_memorial"), ("wi_a2", "fac_a2"), ("wi_b1", "fac_b1")):
    db.execute("INSERT OR IGNORE INTO work_items (work_item_id, facility_id, work_item_type, name, status) VALUES (?, ?, 'Both', ?, 'active')", (wid, fac, wid))
db.commit()
db.close()
# Layout: company A = fac_memorial {wi_memorial_er, wi_a1b} + fac_a2 {wi_a2}; company B = fac_b1 {wi_b1}

root = login("doctor@clear.io", "DoctorSecret123!")


def make_user(tag):
    email = f"{tag}.{RUN}@scope.test"
    status, res, _ = call("POST", "/users/store", {"email": email, "name": tag, "password": PASSWORD, "role": "coder"}, root)
    assert status == 200, (status, res)
    return res["user_id"] if "user_id" in res else res["response"]["user_id"], email


def grant(user_id, company, facility="*", work_item="*", role="admin", cookie=root):
    return call("POST", "/users/roles/assign", {"user_id": user_id, "company_id": company, "facility_id": facility, "work_item_id": work_item, "role": role}, cookie)


company_admin, company_admin_email = make_user("company_admin")
facility_admin, facility_admin_email = make_user("facility_admin")
wi_admin, wi_admin_email = make_user("wi_admin")
plain, plain_email = make_user("plain_coder")
member_a, _ = make_user("member_a")
member_b, _ = make_user("member_b")

assert grant(company_admin, "comp_clear_01")[0] == 200
assert grant(facility_admin, "comp_clear_01", facility="fac_memorial")[0] == 200
assert grant(wi_admin, "comp_clear_01", work_item="wi_memorial_er")[0] == 200
assert grant(plain, "comp_clear_01", work_item="wi_memorial_er", role="coder")[0] == 200
assert grant(member_a, "comp_clear_01", work_item="wi_a2", role="coder")[0] == 200
assert grant(member_b, "cmp_b", work_item="wi_b1", role="coder")[0] == 200

ca, fa, wa, pl = (login(e, PASSWORD) for e in (company_admin_email, facility_admin_email, wi_admin_email, plain_email))

print("\n-- company admin (company A, every facility) --")
st, res, _ = call("GET", "/users", cookie=ca)
ids = {u["id"] for u in res["users"]}
check("lists members of company A", {member_a, wi_admin, plain} <= ids)
check("does not list company B members", member_b not in ids)
check("does not list global admins who have no company A membership", "usr_admin_01" not in ids)
check("assign on a company A work item allowed", grant(member_a, "comp_clear_01", work_item="wi_a2", role="qa", cookie=ca)[0] == 200)
check("assign facility-wide in company A allowed", grant(member_a, "comp_clear_01", facility="fac_a2", role="manager", cookie=ca)[0] == 200)
check("assign in company B refused", grant(member_a, "cmp_b", work_item="wi_b1", cookie=ca)[0] == 403)
check("company A claimed with a company B work item refused", grant(member_a, "comp_clear_01", work_item="wi_b1", cookie=ca)[0] == 403)
check("granting super_admin refused", grant(member_a, "comp_clear_01", role="super_admin", cookie=ca)[0] == 403)
check("read a company B user refused", call("GET", f"/users/{member_b}", cookie=ca)[0] == 403)
check("read a company A user allowed", call("GET", f"/users/{member_a}", cookie=ca)[0] == 200)
check("update a company A user allowed", call("PUT", f"/users/{member_a}", {"name": "Member A2"}, ca)[0] == 200)
check("update a company B user refused", call("PUT", f"/users/{member_b}", {"name": "x"}, ca)[0] == 403)
check("update the global admin refused", call("PUT", "/users/usr_admin_01", {"name": "x"}, ca)[0] == 403)
check("create a company refused", call("POST", "/companies", {"name": "Rogue"}, ca)[0] == 403)
check("create a role refused", call("POST", "/roles", {"name": "x"}, ca)[0] == 403)
st, res, _ = call("GET", "/companies", cookie=ca)
check("lists only company A", [c["id"] for c in res["companies"]] == ["comp_clear_01"])
check("company B detail refused", call("GET", "/companies/cmp_b", cookie=ca)[0] == 403)
check("company A detail allowed", call("GET", "/companies/comp_clear_01", cookie=ca)[0] == 200)
st, res, _ = call("POST", "/users/store", {"email": f"made_by_scoped.{RUN}@scope.test", "name": "m", "password": PASSWORD, "role": "admin"}, ca)
check("scoped admin creating a user cannot make a global admin", st == 200 and call("GET", "/users", cookie=root)[1] is not None)
db = sqlite3.connect(DB_FILE)
row = db.execute("SELECT roles FROM users WHERE email = ?", (f"made_by_scoped.{RUN}@scope.test",)).fetchone()
db.close()
check("that user got the base role, not admin", row is not None and row[0] == "user")

print("\n-- facility admin (fac_memorial only) --")
check("assign on a work item in the facility allowed", grant(plain, "comp_clear_01", work_item="wi_a1b", role="qa", cookie=fa)[0] == 200)
check("assign on a work item in another facility refused", grant(plain, "comp_clear_01", work_item="wi_a2", role="qa", cookie=fa)[0] == 403)
check("assign company-wide refused", grant(plain, "comp_clear_01", role="qa", cookie=fa)[0] == 403)
check("assign facility-wide on another facility refused", grant(plain, "comp_clear_01", facility="fac_a2", role="qa", cookie=fa)[0] == 403)
ids = {u["id"] for u in call("GET", "/users", cookie=fa)[1]["users"]}
check("lists members of its facility but not other facilities", plain in ids and member_a not in ids)

print("\n-- work item admin (wi_memorial_er only) --")
check("assign on its work item allowed", grant(plain, "comp_clear_01", work_item="wi_memorial_er", role="suspend_manager", cookie=wa)[0] == 200)
check("assign on a sibling work item refused", grant(plain, "comp_clear_01", work_item="wi_a1b", role="qa", cookie=wa)[0] == 403)
check("assign facility-wide refused", grant(plain, "comp_clear_01", facility="fac_memorial", role="qa", cookie=wa)[0] == 403)

print("\n-- revoke stays inside the caller's scope --")
assert grant(member_a, "comp_clear_01", work_item="wi_a2", role="qa", cookie=root)[0] == 200
assert grant(member_a, "cmp_b", work_item="wi_b1", role="qa", cookie=root)[0] == 200
call("POST", "/users/roles/revoke", {"user_id": member_a, "role": "qa"}, ca)
db = sqlite3.connect(DB_FILE)
left = {r[0] for r in db.execute("SELECT company_id FROM memberships WHERE user_id = ? AND role = 'qa'", (member_a,))}
db.close()
check("company admin revoke removed only company A rows", left == {"cmp_b"})

print("\n-- ordinary member --")
check("cannot list users", call("GET", "/users", cookie=pl)[0] == 403)
check("cannot assign roles", grant(plain, "comp_clear_01", work_item="wi_memorial_er", role="qa", cookie=pl)[0] == 403)
check("cannot list roles", call("GET", "/roles", cookie=pl)[0] == 403)
check("cannot list companies", call("GET", "/companies", cookie=pl)[0] == 403)

print("\n-- global admin keeps full access --")
check("lists every user", {member_a, member_b, plain} <= {u["id"] for u in call("GET", "/users", cookie=root)[1]["users"]})
check("can assign in any company", grant(member_b, "cmp_b", work_item="wi_b1", role="qa", cookie=root)[0] == 200)
check("can create a company", call("POST", "/companies", {"name": f"Co {RUN}"}, root)[0] == 200)


# --- master data writes ------------------------------------------------------
print("\n-- master data: facilities --")
def post(path, body, cookie):
    return call("POST", path, body, cookie)[0]

check("company admin creates a facility in its company", post("/admin/facilities", {"facility_name": "New A", "client_id": "comp_clear_01"}, ca) == 200)
check("company admin cannot create a facility in company B", post("/admin/facilities", {"facility_name": "Rogue", "client_id": "cmp_b"}, ca) == 403)
check("facility admin cannot create a facility (needs company-wide)", post("/admin/facilities", {"facility_name": "x", "client_id": "comp_clear_01"}, fa) == 403)
check("work item admin cannot create a facility", post("/admin/facilities", {"facility_name": "x", "client_id": "comp_clear_01"}, wa) == 403)
check("ordinary member cannot create a facility", post("/admin/facilities", {"facility_name": "x", "client_id": "comp_clear_01"}, pl) == 403)
check("global admin creates a facility anywhere", post("/admin/facilities", {"facility_name": "B2", "client_id": "cmp_b"}, root) == 200)

print("\n-- master data: work items --")
def wi(fac, cookie, name="wi"):
    return post("/workitem", {"facility_id": fac, "work_item_type": "Both", "name": name}, cookie)
check("company admin creates a work item in any facility of its company", wi("fac_a2", ca) == 200)
check("company admin cannot create one in company B", wi("fac_b1", ca) == 403)
check("facility admin creates one in its facility", wi("fac_memorial", fa) == 200)
check("facility admin cannot create one in a sibling facility", wi("fac_a2", fa) == 403)
check("work item admin cannot create work items", wi("fac_memorial", wa) == 403)
check("unknown facility refused for a scoped admin", wi("fac_missing", ca) == 403)
check("ordinary member cannot create a work item", wi("fac_memorial", pl) == 403)

print("\n-- master data: work item settings --")
def setting(wid, cookie):
    return post(f"/workitem/{wid}/settings", {"key": "k" + RUN, "value": "v"}, cookie)
check("work item admin updates its own work item", setting("wi_memorial_er", wa) == 200)
check("work item admin cannot update a sibling", setting("wi_a1b", wa) == 403)
check("facility admin updates a work item in its facility", setting("wi_a1b", fa) == 200)
check("facility admin cannot update another facility's work item", setting("wi_a2", fa) == 403)
check("company admin cannot update company B", setting("wi_b1", ca) == 403)
check("ordinary member cannot update settings", setting("wi_memorial_er", pl) == 403)

print("\n-- master data: chargemaster --")
def charge(wid, cookie, code="99213"):
    return post("/charge-code/save", {"work_item_id": wid, "cpt_hcpcs_code": code, "description": "visit"}, cookie)
check("work item admin saves a charge code for its work item", charge("wi_memorial_er", wa, "A1001") == 200)
check("work item admin cannot save for a sibling work item", charge("wi_a1b", wa, "A1002") == 403)
check("company admin cannot save for company B", charge("wi_b1", ca, "A1003") == 403)
check("ordinary member cannot save a charge code", charge("wi_memorial_er", pl, "A1004") == 403)
assert charge("wi_a1b", root, "R1001") == 200 and charge("wi_b1", root, "R1002") == 200
db = sqlite3.connect(DB_FILE)
rows = {r[0]: r[1] for r in db.execute("SELECT work_item_id, id FROM charge_master WHERE cpt_hcpcs_code IN ('A1001','R1001','R1002')")}
db.close()
own, sibling, other_company = rows["wi_memorial_er"], rows["wi_a1b"], rows["wi_b1"]
upd = {"description": "changed", "charge_amount": 10, "effective_date": "2026-01-01", "profee_type": "", "facility_type": ""}
check("work item admin updates its own charge code", post(f"/charge-code/{own}/update", upd, wa) == 200)
check("work item admin cannot update a sibling's charge code", post(f"/charge-code/{sibling}/update", upd, wa) == 403)
check("company admin cannot update company B's charge code", post(f"/charge-code/{other_company}/update", upd, ca) == 403)
check("unknown charge code refused for a scoped admin", post("/charge-code/999999/update", upd, wa) == 403)

def status_of(cid):
    db = sqlite3.connect(DB_FILE)
    st = db.execute("SELECT status FROM charge_master WHERE id = ?", (cid,)).fetchone()[0]
    db.close()
    return st
check("bulk delete containing an out-of-scope code is refused", post("/charge-code/bulk-delete", {"ids": [own, sibling]}, wa) == 403)
check("a refused bulk delete changes nothing", status_of(own) == "active" and status_of(sibling) == "active")
check("bulk delete inside scope succeeds", post("/charge-code/bulk-delete", {"ids": [own]}, wa) == 200)
check("and actually deactivates the code", status_of(own) == "inactive")
check("global admin bulk deletes anywhere", post("/charge-code/bulk-delete", {"ids": [other_company]}, root) == 200 and status_of(other_company) == "inactive")

print("\n-- master data: providers --")
def provider(cookie, npi, work_item=""):
    body = {"first_name": "Pat", "last_name": "Provider", "npi": npi, "provider_type": "MD"}
    if work_item:
        body["work_item_id"] = work_item
        body["client_ref"] = "ref-1"
    return post("/provider/save", body, cookie)
check("ordinary member cannot add a provider", provider(pl, "1000000001") == 403)
check("work item admin adds an unlinked provider", provider(wa, "1000000002") == 200)
check("work item admin adds a provider linked to its work item", provider(wa, "1000000003", "wi_memorial_er") == 200)
db = sqlite3.connect(DB_FILE)
linked = db.execute("SELECT COUNT(*) FROM provider_wi WHERE work_item_id = 'wi_memorial_er'").fetchone()[0]
db.close()
check("the provider link was created", linked >= 1)
check("work item admin cannot link a provider to a sibling work item", provider(wa, "1000000004", "wi_a1b") == 403)
check("global admin adds a provider", provider(root, "1000000005") == 200)

print("\n-- master data: shared lookups stay global-admin only --")
check("scoped admin cannot add a work item type", post("/workitem/types", {"work_item_description": "Custom"}, ca) == 403)
check("scoped admin cannot add a CPT code", post("/cpt-code/create", {"cpt_code": "Z9999", "short_description": "x"}, ca) == 403)
check("global admin can add a work item type", post("/workitem/types", {"work_item_description": f"Type {RUN}"}, root) == 200)
check("global admin can add a CPT code", post("/cpt-code/create", {"cpt_code": "Z" + RUN[-4:], "short_description": "x"}, root) == 200)

print(f"\nSCOPE SUMMARY: {passed} passed, {failed} failed")
sys.exit(1 if failed else 0)
