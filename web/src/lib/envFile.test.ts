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

  it("adds a branch's own API", () => {
    const creds = {
      project: { name: "pr-7", db_name: "shop_pr7" },
      connection: { pooled_url: "postgresql://a", session_url: "postgresql://b" },
      api: { ref: "k7f3", url: "https://k7f3.api.example", publishable_key: "pgd_pub_x", secret_key: "pgd_sec_y" },
    } as ProjectCredentials;
    const text = envFileContents(creds);
    expect(text).toContain('PGDOCK_API_URL="https://k7f3.api.example"');
    expect(text).toContain('PGDOCK_PUBLISHABLE_KEY="pgd_pub_x"');
    expect(text).toContain('PGDOCK_SECRET_KEY="pgd_sec_y"');
  });
});
