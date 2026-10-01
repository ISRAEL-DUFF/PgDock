import { describe, expect, it } from "vitest";
import type { ProjectCredentials } from "../api/client";
import { envFileContents } from "./envFile";

describe("envFileContents", () => {
  it("writes both connection strings as quoted variables", () => {
    const creds = {
      project: { name: "My Blog", db_name: "my_blog_k2f9" },
      connection: {
        pooled_url: "postgresql://u:p@db.example.com:6543/my_blog_k2f9?sslmode=require",
        session_url: "postgresql://u:p@db.example.com:5432/my_blog_k2f9?sslmode=require",
      },
    } as ProjectCredentials;
    const text = envFileContents(creds);
    expect(text).toContain('DATABASE_URL="postgresql://u:p@db.example.com:6543/my_blog_k2f9?sslmode=require"');
    expect(text).toContain('DATABASE_SESSION_URL="postgresql://u:p@db.example.com:5432/my_blog_k2f9?sslmode=require"');
    expect(text.startsWith("# PGDock project: My Blog (my_blog_k2f9)")).toBe(true);
    expect(text.endsWith("\n")).toBe(true);
  });
});
