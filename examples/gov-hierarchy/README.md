# gov-hierarchy

This example runs government services across a hierarchy: **country → state → district → municipality**.

- Officers are assigned to units through the `org_units` JWT claim. Each officer sees and acts on their own subtree only.
- The service catalogue is set nationally, then overridden per province, district, municipality and department. For example:
  - Kathmandu district disables death registration.
  - Kathmandu Metro adds parking permits.
  - Its revenue section relabels property tax.
- An application must use a service the unit actually offers. It is stored with the unit's materialised path, and the platform scopes every list to the caller's jurisdiction.

To run it:

```sh
export GOV_JWT_SECRET=$(openssl rand -hex 24)
go run ./examples/gov-hierarchy &

CLERK=$(go run ./examples/gov-hierarchy -token '{"sub":"clerk","org_units":"ktm-metro","department":"revenue"}')
ADMIN=$(go run ./examples/gov-hierarchy -token '{"sub":"root","roles":["admin"]}')

curl -H "Authorization: Bearer $CLERK" localhost:8095/api/org/scope
curl -H "Authorization: Bearer $CLERK" localhost:8095/api/lookups/services
curl -H "Authorization: Bearer $CLERK" -H 'Content-Type: application/json' \
     -d '{"org_unit_id":"ktm-metro","service":"parking","applicant":"Ram"}' localhost:8095/api/applications
curl -H "Authorization: Bearer $ADMIN" localhost:8095/api/org/tree
```

| Route | Purpose |
|---|---|
| `GET /api/org/scope` | The caller's assigned units |
| `GET /api/org/tree` | The jurisdiction tree below the caller |
| `GET /api/org/units/:id/children` | Direct children of a unit in scope |
| `POST /api/org/units` | Create or rename a unit inside the caller's scope |
| `GET /api/lookups/services[?org_unit=id]` | The effective service catalogue for a unit |
| `POST /api/lookups` | Override reference data at a unit the caller administers |
| `POST /api/applications` / `GET /api/applications` | Hierarchy-scoped records |

The tests are in `platform/org_e2e_test.go`, and the building blocks are described in [`docs/app-patterns.md`](../../docs/app-patterns.md).
