import { describe, expect, it } from "vitest";
import { extractKey } from "../components/BackupSetup";
import { backupIsStale } from "../pages/ProjectBackups";

describe("backups", () => {
  it("flags missing backups and ones older than 26 hours", () => {
    const now = Date.parse("2026-10-01T12:00:00Z");
    expect(backupIsStale(null, now)).toBe(true);
    expect(backupIsStale("2026-09-30T11:00:00Z", now)).toBe(false); // 25h
    expect(backupIsStale("2026-09-30T09:00:00Z", now)).toBe(true); // 27h
  });

  it("finds the key line in a downloaded key file", () => {
    const file = "# PGDock backup encryption key (fingerprint abc).\n# Store it offline.\npgdock-backup-key-v1:AAAA\n";
    expect(extractKey(file)).toBe("pgdock-backup-key-v1:AAAA");
    expect(extractKey("  pgdock-backup-key-v1:BBBB  ")).toBe("pgdock-backup-key-v1:BBBB");
  });
});
