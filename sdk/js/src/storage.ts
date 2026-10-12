import { PgdockError, type Result, fail, ok } from "./errors.js";
import { buildURL, json, send, type Transport } from "./http.js";

/** A stored file's metadata. */
export interface FileObject {
  id: string;
  bucket: string;
  path: string;
  version: string;
  size: number;
  mime_type: string;
  etag: string;
  owner?: string | null;
  user_metadata?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  [k: string]: unknown;
}

/** A listed name: a file or a folder. */
export interface FileEntry {
  name: string;
  path: string;
  folder: boolean;
  object?: FileObject | null;
  [k: string]: unknown;
}

export interface Bucket {
  id: string;
  public: boolean;
  file_size_limit?: number | null;
  allowed_mime_types?: string[];
  cache_seconds?: number;
  [k: string]: unknown;
}

export interface Transform {
  width?: number;
  height?: number;
  resize?: "cover" | "contain" | "fill";
  format?: "origin" | "webp" | "avif" | "jpeg" | "png";
  quality?: number;
}

export type Body =
  | Blob
  | ArrayBuffer
  | ArrayBufferView
  | ReadableStream
  | string;

function objectPath(bucket: string, path: string): string {
  return (
    encodeURIComponent(bucket) +
    "/" +
    path.split("/").map(encodeURIComponent).join("/")
  );
}

function transformQuery(
  t?: Transform,
): Record<string, string | number | undefined> {
  return {
    width: t?.width,
    height: t?.height,
    resize: t?.resize,
    format: t?.format,
    quality: t?.quality,
  };
}

/** One bucket's files: storage.bucket("avatars"). */
export class BucketClient {
  constructor(
    private t: Transport,
    readonly id: string,
  ) {}

  /** Uploads up to 50 MB (upsert overwrites). Bigger files: uploadLarge. */
  async upload(
    path: string,
    body: Body,
    o: {
      contentType?: string;
      upsert?: boolean;
      metadata?: Record<string, unknown>;
    } = {},
  ): Promise<Result<FileObject>> {
    const headers: Record<string, string> = {};
    const type =
      o.contentType ??
      (typeof Blob !== "undefined" && body instanceof Blob && body.type
        ? body.type
        : undefined);
    if (type) headers["Content-Type"] = type;
    if (o.upsert) headers["x-upsert"] = "true";
    if (o.metadata) headers["x-metadata"] = JSON.stringify(o.metadata);
    return json<FileObject>(
      this.t,
      "/storage/v1/object/" + objectPath(this.id, path),
      {
        method: "POST",
        raw: body as BodyInit,
        headers,
      },
    );
  }

  /** Downloads a file (as the caller's policies allow), or a byte range. */
  async download(
    path: string,
    o: { range?: string; transform?: Transform } = {},
  ): Promise<Result<Blob>> {
    const base = o.transform ? "/storage/v1/render/" : "/storage/v1/object/";
    const r = await send(this.t, base + objectPath(this.id, path), {
      query: o.transform ? transformQuery(o.transform) : undefined,
      headers: o.range ? { Range: o.range } : undefined,
    });
    if (r.error) return r;
    return ok(await r.data.blob());
  }

  async info(path: string): Promise<Result<FileObject>> {
    return json<FileObject>(
      this.t,
      "/storage/v1/object/info/" + objectPath(this.id, path),
    );
  }

  /** Files and folders under a prefix (folder/), a page at a time. */
  async list(
    o: { prefix?: string; cursor?: string; limit?: number } = {},
  ): Promise<Result<{ items: FileEntry[]; nextCursor: string | null }>> {
    const r = await json<{
      items?: FileEntry[];
      data?: FileEntry[];
      next_cursor?: string | null;
    }>(this.t, "/storage/v1/list/" + encodeURIComponent(this.id), {
      query: { prefix: o.prefix, cursor: o.cursor, limit: o.limit },
    });
    if (r.error) return r;
    return ok({
      items: r.data.items ?? r.data.data ?? [],
      nextCursor: r.data.next_cursor ?? null,
    });
  }

  /** Deletes files by path. */
  async remove(paths: string[]): Promise<Result<{ deleted: number }>> {
    const r = await json<FileObject[]>(
      this.t,
      "/storage/v1/object/" + encodeURIComponent(this.id),
      {
        method: "DELETE",
        body: { paths },
      },
    );
    return r.error ? r : ok({ deleted: r.data.length });
  }

  async move(
    from: string,
    to: string,
    o: { toBucket?: string } = {},
  ): Promise<Result<null>> {
    const r = await send(this.t, "/storage/v1/object/move", {
      body: { bucket: this.id, from, to, to_bucket: o.toBucket },
    });
    return r.error ? r : ok(null);
  }

  async copy(
    from: string,
    to: string,
    o: { toBucket?: string } = {},
  ): Promise<Result<null>> {
    const r = await send(this.t, "/storage/v1/object/copy", {
      body: { bucket: this.id, from, to, to_bucket: o.toBucket },
    });
    return r.error ? r : ok(null);
  }

  /** A public bucket's file URL (no request; anyone with it can download). */
  publicUrl(
    path: string,
    o: { transform?: Transform; download?: boolean | string } = {},
  ): string {
    if (o.transform) {
      return buildURL(
        this.t.url,
        "/storage/v1/render/" + objectPath(this.id, path),
        transformQuery(o.transform),
      );
    }
    const q: Record<string, string | undefined> = {};
    if (o.download) q["download"] = o.download === true ? "" : o.download;
    return buildURL(
      this.t.url,
      "/storage/v1/public/" + objectPath(this.id, path),
      q,
    );
  }

  /** A download URL that works without a session until it expires (1 hour by default). */
  async signedUrl(
    path: string,
    o: {
      expiresIn?: number;
      transform?: Transform;
      download?: boolean | string;
    } = {},
  ): Promise<Result<string>> {
    const r = await json<{
      signed_url?: string;
      signedURL?: string;
      url?: string;
    }>(this.t, "/storage/v1/object/sign/" + objectPath(this.id, path), {
      body: { expires_in: o.expiresIn ?? 3600, transform: o.transform },
    });
    if (r.error) return r;
    const u = r.data.signed_url ?? r.data.signedURL ?? r.data.url;
    if (!u)
      return fail(
        new PgdockError(200, "invalid_response", "no signed URL in the answer"),
      );
    const abs = u.startsWith("http")
      ? u
      : this.t.url.replace(/\/+$/, "") +
        (u.startsWith("/storage/") ? u : "/storage/v1" + u);
    if (!o.download) return ok(abs);
    const url = new URL(abs);
    url.searchParams.set("download", o.download === true ? "" : o.download);
    return ok(url.toString());
  }

  /** Signed URLs for several files. */
  async signedUrls(
    paths: string[],
    o: { expiresIn?: number } = {},
  ): Promise<Result<{ path: string; url: string | null; error?: string }[]>> {
    const r = await json<
      | {
          path: string;
          signed_url?: string;
          signedURL?: string;
          error?: string;
        }[]
      | { data: never[] }
    >(this.t, "/storage/v1/object/sign/" + encodeURIComponent(this.id), {
      body: { paths, expires_in: o.expiresIn ?? 3600 },
    });
    if (r.error) return r;
    const list = (Array.isArray(r.data) ? r.data : r.data.data) as {
      path: string;
      signed_url?: string;
      signedURL?: string;
      error?: string;
    }[];
    const base = this.t.url.replace(/\/+$/, "");
    return ok(
      list.map((x) => {
        const u = x.signed_url ?? x.signedURL;
        return {
          path: x.path,
          url: u
            ? u.startsWith("http")
              ? u
              : base + (u.startsWith("/storage/") ? u : "/storage/v1" + u)
            : null,
          error: x.error,
        };
      }),
    );
  }

  /** A URL a client without a session can upload one file to (2 hours). */
  async signedUploadUrl(
    path: string,
    o: { upsert?: boolean } = {},
  ): Promise<Result<{ url: string; token?: string }>> {
    const r = await json<{ url: string; token?: string }>(
      this.t,
      "/storage/v1/object/upload/sign/" + objectPath(this.id, path),
      {
        body: {},
        headers: o.upsert ? { "x-upsert": "true" } : undefined,
      },
    );
    if (r.error) return r;
    const u = r.data.url.startsWith("http")
      ? r.data.url
      : this.t.url.replace(/\/+$/, "") +
        (r.data.url.startsWith("/storage/")
          ? r.data.url
          : "/storage/v1" + r.data.url);
    return ok({ url: u, token: r.data.token });
  }

  /**
   * Uploads a large file straight to the object store in parts (up to the
   * plan's largest upload), a few at a time; onProgress gets bytes sent.
   */
  async uploadLarge(
    path: string,
    file: Blob,
    o: {
      contentType?: string;
      upsert?: boolean;
      concurrency?: number;
      onProgress?: (sent: number, total: number) => void;
    } = {},
  ): Promise<Result<FileObject>> {
    type Part = { number: number; size: number; url: string };
    const start = await json<{ id: string; part_size: number; parts: Part[] }>(
      this.t,
      "/storage/v1/upload/" + objectPath(this.id, path),
      {
        body: {
          size: file.size,
          mime_type: o.contentType ?? (file.type || "application/octet-stream"),
          upsert: o.upsert ?? false,
        },
      },
    );
    if (start.error) return start;
    const { id, part_size, parts } = start.data;
    let sent = 0;
    const queue = [...parts];
    const worker = async (): Promise<PgdockError | null> => {
      for (let p = queue.shift(); p; p = queue.shift()) {
        const from = (p.number - 1) * part_size;
        const res = await this.t
          .fetch(p.url, {
            method: "PUT",
            body: file.slice(from, from + p.size),
          })
          .catch((e: unknown) => e as Error);
        if (res instanceof Error || !res.ok) {
          return new PgdockError(
            res instanceof Error ? 0 : res.status,
            "part_failed",
            `part ${p.number} didn't upload; call uploadLarge's resume with ${id}`,
          );
        }
        sent += p.size;
        o.onProgress?.(sent, file.size);
      }
      return null;
    };
    const errs = await Promise.all(
      Array.from({ length: Math.max(1, o.concurrency ?? 4) }, worker),
    );
    const bad = errs.find((e) => e);
    if (bad) return fail(bad);
    return json<FileObject>(
      this.t,
      `/storage/v1/upload/${encodeURIComponent(id)}/complete`,
      { body: {} },
    );
  }
}

/** client.storage: buckets and files. */
export class StorageClient {
  constructor(private t: Transport) {}

  /** A bucket's files. */
  bucket(id: string): BucketClient {
    return new BucketClient(this.t, id);
  }

  // Buckets are managed with the secret key (servers, scripts).

  async listBuckets(): Promise<Result<Bucket[]>> {
    const r = await json<Bucket[] | { data: Bucket[] }>(
      this.t,
      "/storage/v1/bucket",
    );
    return r.error ? r : ok(Array.isArray(r.data) ? r.data : r.data.data);
  }

  async createBucket(
    id: string,
    o: {
      public?: boolean;
      fileSizeLimit?: number;
      allowedMimeTypes?: string[];
      cacheSeconds?: number;
    } = {},
  ): Promise<Result<Bucket>> {
    return json<Bucket>(this.t, "/storage/v1/bucket", {
      body: {
        id,
        public: o.public,
        file_size_limit: o.fileSizeLimit,
        allowed_mime_types: o.allowedMimeTypes,
        cache_seconds: o.cacheSeconds,
      },
    });
  }

  async updateBucket(
    id: string,
    o: {
      public?: boolean;
      fileSizeLimit?: number | null;
      allowedMimeTypes?: string[];
      cacheSeconds?: number;
    },
  ): Promise<Result<Bucket>> {
    return json<Bucket>(
      this.t,
      "/storage/v1/bucket/" + encodeURIComponent(id),
      {
        method: "PATCH",
        body: {
          public: o.public,
          file_size_limit: o.fileSizeLimit,
          allowed_mime_types: o.allowedMimeTypes,
          cache_seconds: o.cacheSeconds,
        },
      },
    );
  }

  async emptyBucket(id: string): Promise<Result<null>> {
    const r = await send(
      this.t,
      "/storage/v1/bucket/" + encodeURIComponent(id) + "/empty",
      { method: "POST" },
    );
    return r.error ? r : ok(null);
  }

  async deleteBucket(id: string): Promise<Result<null>> {
    const r = await send(
      this.t,
      "/storage/v1/bucket/" + encodeURIComponent(id),
      { method: "DELETE" },
    );
    return r.error ? r : ok(null);
  }
}
