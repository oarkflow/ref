import json
import os
import sqlite3
import urllib.request
import urllib.error

BASE_URL = "http://127.0.0.1:3000"

# Ensure test encounter is in 'OPEN' status for idempotent test runs
db_path = os.path.join(os.path.dirname(__file__), "..", ".data", "clear", "app.db")
if os.path.exists(db_path):
    with sqlite3.connect(db_path) as conn:
        conn.execute("UPDATE encounter_details SET encounter_status='OPEN', code_assigned=NULL WHERE encounter_id='enc_1002'")
        conn.execute("UPDATE encounters SET status='OPEN' WHERE encounter_id='enc_1002'")
        conn.commit()

def post(url, data, cookie=None):
    headers = {"Content-Type": "application/json"}
    if cookie:
        headers["Cookie"] = cookie
    req = urllib.request.Request(f"{BASE_URL}{url}", data=json.dumps(data).encode("utf-8"), headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            set_cookie = resp.headers.get("Set-Cookie")
            return resp.status, json.loads(resp.read().decode("utf-8")), set_cookie
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode("utf-8")), None

def get(url, cookie=None):
    headers = {}
    if cookie:
        headers["Cookie"] = cookie
    req = urllib.request.Request(f"{BASE_URL}{url}", headers=headers, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode("utf-8"))

print("1. Testing Argon2id Login (doctor@clear.io):")
status, res, cookie = post("/login", {"email": "doctor@clear.io", "password": "DoctorSecret123!"})
assert status == 200, f"Expected 200, got {status}: {res}"
assert res.get("principal", {}).get("email") == "doctor@clear.io"
assert cookie is not None
print("   [PASS] Logged in successfully, session cookie received.")

print("\n2. Testing Bcrypt Legacy Fallback Login (legacy_user@clear.io):")
status, res, legacy_cookie = post("/login", {"email": "legacy_user@clear.io", "password": "LegacySecret123!"})
assert status == 200, f"Expected 200, got {status}: {res}"
assert res.get("principal", {}).get("email") == "legacy_user@clear.io"
assert legacy_cookie is not None
print("   [PASS] Legacy Bcrypt login verified successfully.")

print("\n3. Testing Bad Credentials:")
status, res, _ = post("/login", {"email": "legacy_user@clear.io", "password": "WrongPassword!"})
assert status == 401, f"Expected 401, got {status}: {res}"
print("   [PASS] 401 Unauthorized returned as expected.")

print("\n4. Testing Role Management (assign & revoke):")
status, res, _ = post("/users/roles/assign", {
    "user_id": "usr_coder_01",
    "company_id": "comp_clear_01",
    "work_item_id": "wi_memorial_er",
    "role": "coder"
})
assert status == 200 and res.get("saved") == 1
status, res = get("/user/roles/usr_coder_01")
assert status == 200 and len(res.get("roles", [])) > 0
print("   [PASS] Assigned role verified in memberships.")

status, res, _ = post("/users/roles/revoke", {
    "user_id": "usr_coder_01",
    "role": "coder"
})
assert status == 200 and res.get("deleted") >= 1
print("   [PASS] Revoked role verified.")

print("\n5. Testing Chart Claim (start-coding):")
status, res, _ = post("/coding/wi_memorial_er/enc_1002/start-coding", {})
assert status == 200 and res.get("claimed") == 1
print("   [PASS] Encounter enc_1002 claimed successfully.")

print("\n6. Testing Suspend & Release Workflow:")
status, res, _ = post("/coding/wi_memorial_er/enc_1002/suspend", {
    "reason_id": 1,
    "suspend_reason": "Missing Medical Record",
    "suspend_note": "Awaiting pathology report",
    "event_dos": "2026-03-11"
})
assert status == 200 and res.get("event_saved") == 1
print("   [PASS] Encounter enc_1002 suspended with audit event.")

status, res, _ = post("/coding/wi_memorial_er/enc_1002/release-suspends", {})
assert status == 200 and res.get("header_updated") == 1
print("   [PASS] Encounter enc_1002 released back to active queue.")

print("\n7. Testing Coding Documentation Completion:")
status, res, _ = post("/coding/wi_memorial_er/enc_1002/start-coding", {})
assert status == 200 and res.get("claimed") == 1
status, res, _ = post("/coding/wi_memorial_er/enc_1002/save-coding-documentation", {
    "dos": "2026-03-11",
    "documentation": "Patient examined and discharged in stable condition.",
    "request_qa": False
})
assert status == 200 and res.get("header_updated") == 1 and res.get("next_status") == "COMPLETE"
print("   [PASS] Encounter enc_1002 saved and completed.")

print("\n8. Testing Queue Dashboard Metrics:")
status, res = get("/coding/facilities/info")
assert status == 200 and "summary_rows" in res
summary = res["summary_rows"][0]
print(f"   [PASS] Dashboard metrics: open={summary['open_count']}, in_progress={summary['in_progress_count']}, complete={summary['complete_count']}")

print("\nALL LIFECYCLE & SECURITY WORKFLOW TESTS PASSED!")
