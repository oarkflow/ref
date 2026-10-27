import unittest

from migrate import convert, form_fields


class MigrationTest(unittest.TestCase):
    def test_nested_fields_and_required_validation(self):
        form = {"schema": {"components": [{"type": "card", "children": [
            {"type": "row", "children": [
                {"type": "text-input", "name": "applicant-name", "label": "Applicant", "validation": [{"type": "required"}]},
                {"type": "select", "name": "decision", "options": [{"value": "approve"}, {"value": "reject"}]},
            ]}
        ]}]}}
        result = "\n".join(form_fields(form, set()))
        self.assertIn('input "applicant_name"', result)
        self.assertIn('required true', result)
        self.assertIn('options ["approve", "reject"]', result)

    def test_stage_roles_and_group_references(self):
        data = {
            "pipeline": {"name": "Permit", "access_config": {"external": {"enabled": True}}},
            "roles": [{"id": "r1", "key": "reviewer"}],
            "forms": [{"key": "request", "fields": [{"key": "name", "type": "text-input"}]}],
            "stages": [{"key": "submit", "order": 1, "form_groups": [{"key": "details", "forms": ["request"]}], "allowed_role_ids": ["r1"]}],
        }
        bcl, item = convert("permit", data, "fixture")
        self.assertEqual(item["stages"], 1)
        self.assertIn('roles ["reviewer"]', bcl)
        self.assertIn('forms ["request"]', bcl)
        self.assertIn("public true", bcl)

    def test_stage_sla_pickup_and_escalation(self):
        data = {
            "pipeline": {"name": "Permit"},
            "roles": [{"id": "r1", "key": "reviewer"}, {"id": "r2", "key": "district_officer"}],
            "forms": [{"key": "request", "fields": [{"key": "name", "type": "text-input"}]}],
            "stages": [{"key": "review", "order": 1, "form_groups": [{"key": "d", "forms": ["request"]}],
                        "allowed_role_ids": ["r1"], "escalation_chain": ["district_officer"],
                        "sla_config": {"duration_minutes": 480, "pickup_timeout_minutes": 120, "breach_action": "notify_and_escalate"}}],
        }
        bcl, _ = convert("permit", data, "fixture")
        self.assertIn('duration "480m"', bcl)
        self.assertIn('pickup_timeout "120m"', bcl)
        self.assertIn('escalate "level_1"', bcl)
        self.assertIn('assign_roles ["district_officer"]', bcl)


if __name__ == "__main__":
    unittest.main()
