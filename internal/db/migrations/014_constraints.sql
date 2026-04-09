-- Enforce foreign key on volume.machine_id via trigger
-- (SQLite cannot add FK constraints via ALTER TABLE)
CREATE TRIGGER IF NOT EXISTS fk_volume_machine_insert
BEFORE INSERT ON volume
WHEN NEW.machine_id IS NOT NULL
BEGIN
  SELECT RAISE(ABORT, 'foreign key violation: volume.machine_id references non-existent machine')
  WHERE NOT EXISTS (SELECT 1 FROM machine WHERE id = NEW.machine_id);
END;

CREATE TRIGGER IF NOT EXISTS fk_volume_machine_update
BEFORE UPDATE OF machine_id ON volume
WHEN NEW.machine_id IS NOT NULL
BEGIN
  SELECT RAISE(ABORT, 'foreign key violation: volume.machine_id references non-existent machine')
  WHERE NOT EXISTS (SELECT 1 FROM machine WHERE id = NEW.machine_id);
END;

-- Enforce valid protocol values on port_rule
CREATE TRIGGER IF NOT EXISTS chk_port_rule_protocol_insert
BEFORE INSERT ON port_rule
BEGIN
  SELECT RAISE(ABORT, 'protocol must be tcp or udp')
  WHERE NEW.protocol NOT IN ('tcp', 'udp');
END;

CREATE TRIGGER IF NOT EXISTS chk_port_rule_protocol_update
BEFORE UPDATE OF protocol ON port_rule
BEGIN
  SELECT RAISE(ABORT, 'protocol must be tcp or udp')
  WHERE NEW.protocol NOT IN ('tcp', 'udp');
END;
