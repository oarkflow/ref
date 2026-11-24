-- Reference data of the hospital group, one table per set. facility_id is the
-- workspace that owns a row: "himal" (the group) rows are visible to every
-- facility, a facility's own rows only to it. Written to be re-run safely.
-- Stands in for a hospital's own tables; any database.sql dialect works.

CREATE TABLE IF NOT EXISTS tbl_specialties (facility_id TEXT NOT NULL, code TEXT NOT NULL, label TEXT NOT NULL, sort INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (facility_id, code));
CREATE TABLE IF NOT EXISTS tbl_provider_types (facility_id TEXT NOT NULL, code TEXT NOT NULL, label TEXT NOT NULL, sort INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (facility_id, code));
CREATE TABLE IF NOT EXISTS tbl_facility_types (facility_id TEXT NOT NULL, code TEXT NOT NULL, label TEXT NOT NULL, sort INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (facility_id, code));
CREATE TABLE IF NOT EXISTS tbl_cpt_codes (facility_id TEXT NOT NULL, code TEXT NOT NULL, label TEXT NOT NULL, sort INTEGER NOT NULL DEFAULT 0, specialties TEXT NOT NULL DEFAULT '', PRIMARY KEY (facility_id, code));
CREATE TABLE IF NOT EXISTS tbl_providers (facility_id TEXT NOT NULL, code TEXT NOT NULL, label TEXT NOT NULL, sort INTEGER NOT NULL DEFAULT 0, specialties TEXT NOT NULL DEFAULT '', provider_type TEXT NOT NULL DEFAULT 'physician', PRIMARY KEY (facility_id, code));
CREATE TABLE IF NOT EXISTS tbl_nurses (facility_id TEXT NOT NULL, code TEXT NOT NULL, label TEXT NOT NULL, sort INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (facility_id, code));
CREATE TABLE IF NOT EXISTS tbl_locations (facility_id TEXT NOT NULL, code TEXT NOT NULL, label TEXT NOT NULL, sort INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (facility_id, code));

-- ---- shared by the whole group -------------------------------------------
INSERT OR IGNORE INTO tbl_provider_types (facility_id, code, label, sort) VALUES
  ('himal', 'physician', 'Physician (attending)', 1),
  ('himal', 'resident', 'Resident', 2),
  ('himal', 'billing', 'Billing provider', 3),
  ('himal', 'secondary', 'Secondary provider', 4);

INSERT OR IGNORE INTO tbl_cpt_codes (facility_id, code, label, sort, specialties) VALUES
  ('himal', '99213', '99213 - Office visit, established patient (level 3)', 1, 'GM|CARD|ORTHO'),
  ('himal', '99214', '99214 - Office visit, established patient (level 4)', 2, 'GM|CARD|ORTHO'),
  ('himal', '93000', '93000 - Electrocardiogram, complete', 3, 'CARD|EM'),
  ('himal', '99283', '99283 - Emergency department visit (level 3)', 4, 'EM'),
  ('himal', '99285', '99285 - Emergency department visit (level 5)', 5, 'EM'),
  ('himal', '73030', '73030 - X-ray, shoulder', 6, 'ORTHO');

-- ---- City General Hospital (every specialty and claim type) ----------------
INSERT OR IGNORE INTO tbl_specialties (facility_id, code, label, sort) VALUES
  ('city', 'GM', 'General Medicine', 1), ('city', 'CARD', 'Cardiology', 2),
  ('city', 'EM', 'Emergency Medicine', 3), ('city', 'ORTHO', 'Orthopedics', 4);
INSERT OR IGNORE INTO tbl_facility_types (facility_id, code, label, sort) VALUES
  ('city', 'ED_PROFEE', 'ED - professional fee', 1), ('city', 'ED_FACILITY', 'ED - facility', 2),
  ('city', 'IP_PROFEE', 'Inpatient - professional fee', 3), ('city', 'IP_FACILITY', 'Inpatient - facility', 4),
  ('city', 'OP_PROFEE', 'Outpatient - professional fee', 5), ('city', 'OP_FACILITY', 'Outpatient - facility', 6),
  ('city', 'OBS', 'Observation', 7);
INSERT OR IGNORE INTO tbl_cpt_codes (facility_id, code, label, sort, specialties) VALUES
  ('city', '93306', '93306 - Echocardiography with Doppler', 7, 'CARD');
INSERT OR IGNORE INTO tbl_providers (facility_id, code, label, sort, specialties, provider_type) VALUES
  ('city', 'dr-sharma', 'Dr. Anita Sharma', 1, 'CARD', 'physician'),
  ('city', 'dr-koirala', 'Dr. Rajan Koirala', 2, 'GM', 'physician'),
  ('city', 'dr-rai', 'Dr. Sunita Rai', 3, 'EM', 'physician'),
  ('city', 'dr-lama', 'Dr. Tenzing Lama (resident)', 4, 'CARD|EM', 'resident'),
  ('city', 'dr-karki', 'Dr. Asha Karki (PA)', 5, 'EM|GM', 'secondary'),
  ('city', 'bill-himal', 'Himal Billing Services', 6, '', 'billing');
INSERT OR IGNORE INTO tbl_nurses (facility_id, code, label, sort) VALUES
  ('city', 'rn-gurung', 'Nurse Maya Gurung', 1), ('city', 'rn-tamang', 'Nurse Bikash Tamang', 2),
  ('city', 'rn-magar', 'Nurse Pooja Magar', 3), ('city', 'rn-limbu', 'Nurse Dipak Limbu', 4);
INSERT OR IGNORE INTO tbl_locations (facility_id, code, label, sort) VALUES
  ('city', 'er-1', 'ER Bay 1', 1), ('city', 'er-2', 'ER Bay 2', 2),
  ('city', 'card-3a', 'Cardiology Ward 3A', 3), ('city', 'icu', 'Intensive Care Unit', 4),
  ('city_er', 'er-triage', 'ER Triage', 0);

-- ---- Riverside Clinic (no emergency care: outpatient claim types only) ------
INSERT OR IGNORE INTO tbl_specialties (facility_id, code, label, sort) VALUES
  ('river', 'GM', 'General Medicine', 1), ('river', 'CARD', 'Cardiology', 2), ('river', 'ORTHO', 'Orthopedics', 4);
INSERT OR IGNORE INTO tbl_facility_types (facility_id, code, label, sort) VALUES
  ('river', 'OP_PROFEE', 'Outpatient - professional fee', 5), ('river', 'OP_FACILITY', 'Outpatient - facility', 6);
INSERT OR IGNORE INTO tbl_providers (facility_id, code, label, sort, specialties, provider_type) VALUES
  ('river', 'dr-adhikari', 'Dr. Prakash Adhikari', 1, 'GM', 'physician'),
  ('river', 'dr-shrestha', 'Dr. Mina Shrestha', 2, 'ORTHO|GM', 'physician'),
  ('river', 'bill-river', 'Riverside Billing Office', 3, '', 'billing');
INSERT OR IGNORE INTO tbl_nurses (facility_id, code, label, sort) VALUES
  ('river', 'rn-thapa', 'Nurse Kiran Thapa', 1), ('river', 'rn-basnet', 'Nurse Sita Basnet', 2);
INSERT OR IGNORE INTO tbl_locations (facility_id, code, label, sort) VALUES
  ('river', 'room-1', 'Clinic Room 1', 1), ('river', 'proc', 'Procedure Room', 2), ('river', 'xray', 'X-ray Suite', 3);
