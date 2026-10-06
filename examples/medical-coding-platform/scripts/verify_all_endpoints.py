#!/usr/bin/env python3
import json
import urllib.request
import urllib.error

BASE_URL = "http://127.0.0.1:3000"

endpoints = [
    # Auth
    ("GET", "/login"),
    ("GET", "/web/client/login"),
    ("GET", "/me"),
    ("GET", "/web/client/me"),
    ("POST", "/user/info", {"id": "usr_admin_01"}),
    ("POST", "/web/client/user/info", {"id": "usr_admin_01"}),
    ("GET", "/user/roles/usr_coder_01"),
    ("GET", "/web/client/user/roles/usr_coder_01"),
    
    # Master / Facilities
    ("GET", "/facilities"),
    ("GET", "/web/client/facilities"),
    ("GET", "/facility/fac_memorial/workitems"),
    ("GET", "/web/client/facility/fac_memorial/workitems"),
    ("GET", "/coding/facilities/info"),
    ("GET", "/web/client/coding/facilities/info"),
    ("GET", "/workitem"),
    ("GET", "/web/client/workitem"),
    ("GET", "/workitem/types"),
    ("GET", "/web/client/workitem/types"),
    ("GET", "/workitem/wi_memorial_er/settings"),
    ("GET", "/web/client/workitem/wi_memorial_er/settings"),
    ("GET", "/workitem/wi_memorial_er/description"),
    ("GET", "/web/client/workitem/wi_memorial_er/description"),
    ("GET", "/workitem/comp_clear_01/workitems"),
    ("GET", "/web/client/workitem/comp_clear_01/workitems"),
    ("GET", "/coding/workitem/wi_memorial_er"),
    ("GET", "/web/client/coding/workitem/wi_memorial_er"),
    
    # Encounters & Queues
    ("GET", "/coding/wi_memorial_er/open-list"),
    ("GET", "/web/client/coding/wi_memorial_er/open-list"),
    ("GET", "/coding/wi_memorial_er/in-progress-list"),
    ("GET", "/web/client/coding/wi_memorial_er/in-progress-list"),
    ("GET", "/coding/wi_memorial_er/completed-list"),
    ("GET", "/web/client/coding/wi_memorial_er/completed-list"),
    ("GET", "/coding/in-progress-count"),
    ("GET", "/web/client/coding/in-progress-count"),
    ("POST", "/coding/wi_memorial_er/search", {"mrn": "MRN-1001"}),
    ("POST", "/web/client/coding/wi_memorial_er/search", {"mrn": "MRN-1001"}),
    ("GET", "/coding/wi_memorial_er/enc_1002/patient-header"),
    ("GET", "/web/client/coding/wi_memorial_er/enc_1002/patient-header"),
    ("GET", "/coding/wi_memorial_er/enc_1002/documentation"),
    ("GET", "/web/client/coding/wi_memorial_er/enc_1002/documentation"),
    ("GET", "/coding/wi_memorial_er/enc_1002/summary"),
    ("GET", "/web/client/coding/wi_memorial_er/enc_1002/summary"),
    ("POST", "/coding/wi_memorial_er/2026-03-01/pqrs-measures", {}),
    ("POST", "/web/client/coding/wi_memorial_er/2026-03-01/pqrs-measures", {}),
    ("POST", "/coding/wi_memorial_er/enc_1002/pqrs-codes", {}),
    ("POST", "/web/client/coding/wi_memorial_er/enc_1002/pqrs-codes", {}),
    ("POST", "/coding/wi_memorial_er/enc_1002/pqrs-codes-by-id", {}),
    ("POST", "/web/client/coding/wi_memorial_er/enc_1002/pqrs-codes-by-id", {}),
    
    # QA Queues
    ("GET", "/coding/wi_memorial_er/qa-list"),
    ("GET", "/web/client/coding/wi_memorial_er/qa-list"),
    ("GET", "/coding/wi_memorial_er/qa-in-progress-list"),
    ("GET", "/web/client/coding/wi_memorial_er/qa-in-progress-list"),
    
    # DE Queues
    ("GET", "/coding/wi_memorial_er/de-list"),
    ("GET", "/web/client/coding/wi_memorial_er/de-list"),
    ("GET", "/coding/wi_memorial_er/de-in-progress-list"),
    ("GET", "/web/client/coding/wi_memorial_er/de-in-progress-list"),
    ("GET", "/coding/de-in-progress-count"),
    ("GET", "/web/client/coding/de-in-progress-count"),
    
    # Suspend Queues
    ("GET", "/coding/suspend-reasons"),
    ("GET", "/web/client/coding/suspend-reasons"),
    ("GET", "/coding/wi_memorial_er/suspended-list"),
    ("GET", "/web/client/coding/wi_memorial_er/suspended-list"),
    ("GET", "/coding/wi_memorial_er/enc_1002/suspends"),
    ("GET", "/web/client/coding/wi_memorial_er/enc_1002/suspends"),
    
    # Chargemaster & Providers & CPT
    ("GET", "/facility/fac_memorial/providers"),
    ("GET", "/web/client/facility/fac_memorial/providers"),
    ("GET", "/provider"),
    ("GET", "/web/client/provider"),
    ("GET", "/provider/prv_house/info"),
    ("GET", "/web/client/provider/prv_house/info"),
    ("GET", "/charge-code"),
    ("GET", "/web/client/charge-code"),
    ("GET", "/cpt-code"),
    ("GET", "/web/client/cpt-code"),
    ("GET", "/cpt-code/99285"),
    ("GET", "/web/client/cpt-code/99285"),
    ("GET", "/cpts"),
    ("GET", "/web/client/cpts"),
    ("GET", "/metadata/rules"),
    ("GET", "/web/client/metadata/rules"),
    ("GET", "/background/service"),
    ("GET", "/web/client/background/service"),
    
    # Admin & Roles & Users
    ("GET", "/users"),
    ("GET", "/web/client/users"),
    ("GET", "/users/usr_admin_01"),
    ("GET", "/web/client/users/usr_admin_01"),
    ("GET", "/users/usr_admin_01/permissions"),
    ("GET", "/web/client/users/usr_admin_01/permissions"),
    ("GET", "/roles"),
    ("GET", "/web/client/roles"),
    ("GET", "/companies"),
    ("GET", "/web/client/companies"),
    ("GET", "/companies/comp_clear_01"),
    ("GET", "/web/client/companies/comp_clear_01"),
    ("GET", "/coders"),
    ("GET", "/web/client/coders"),
]

passed = 0
failed = 0

for item in endpoints:
    method = item[0]
    path = item[1]
    body = item[2] if len(item) > 2 else None
    
    url = f"{BASE_URL}{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode("utf-8") if body else None
    
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            status = resp.status
            content = resp.read()
            if status == 200:
                print(f"[PASS] {method:4s} {path:55s} -> HTTP {status}")
                passed += 1
            else:
                print(f"[WARN] {method:4s} {path:55s} -> HTTP {status}")
                passed += 1
    except urllib.error.HTTPError as e:
        print(f"[FAIL] {method:4s} {path:55s} -> HTTP {e.code}: {e.read().decode('utf-8')[:100]}")
        failed += 1
    except Exception as e:
        print(f"[FAIL] {method:4s} {path:55s} -> {str(e)}")
        failed += 1

print("\n" + "="*70)
print(f"VERIFICATION SUMMARY: {passed} PASSED, {failed} FAILED (TOTAL: {len(endpoints)})")
print("="*70)
if failed > 0:
    exit(1)
