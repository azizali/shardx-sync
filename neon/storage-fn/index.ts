// ShardX Sync storage gateway.
// Neon Object Storage only accepts project-level S3 keys, so the desktop app never holds them.
// Instead it calls this function with the user's Neon Auth JWT and gets short-lived presigned URLs.
import { createRemoteJWKSet, jwtVerify } from 'jose';
import { S3Client, GetObjectCommand, PutObjectCommand, ListObjectsV2Command, DeleteObjectsCommand } from '@aws-sdk/client-s3';
import { getSignedUrl } from '@aws-sdk/s3-request-presigner';

const BUCKET = 'profiles';
const URL_TTL_SECONDS = 15 * 60;
const MAX_ITEMS = 500;
// "<profile uuid>/<version folder>/<file>" or "<profile uuid>/<version folder>"
const KEY_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\/[A-Za-z0-9._-]{1,64}(\/[A-Za-z0-9._-]{1,64})?$/;
const PREFIX_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\/([A-Za-z0-9._-]{1,64}\/)?$/;

const jwks = createRemoteJWKSet(new URL(process.env.NEON_AUTH_JWKS_URL!));
const issuer = new URL(process.env.NEON_AUTH_BASE_URL!).origin;
const s3 = new S3Client({
  region: process.env.AWS_REGION,
  endpoint: process.env.AWS_ENDPOINT_URL_S3,
  credentials: { accessKeyId: process.env.AWS_ACCESS_KEY_ID!, secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY! },
  forcePathStyle: true,
});

const json = (body: unknown, status = 200) => Response.json(body, { status });
const fail = (status: number, error: string) => json({ error }, status);

type SignItem = { op: 'get' | 'put'; key: string };

async function handle(path: string, body: any): Promise<Response> {
  if (path === '/sign') {
    const items: SignItem[] = Array.isArray(body?.items) ? body.items : [];
    if (!items.length || items.length > MAX_ITEMS) return fail(400, 'items must have 1-500 entries');
    for (const it of items) {
      if ((it.op !== 'get' && it.op !== 'put') || typeof it.key !== 'string' || !KEY_RE.test(it.key)) return fail(400, `bad item: ${JSON.stringify(it)}`);
    }
    const urls = await Promise.all(items.map((it) => getSignedUrl(
      s3,
      it.op === 'put'
        ? new PutObjectCommand({ Bucket: BUCKET, Key: it.key, ContentType: 'application/octet-stream' })
        : new GetObjectCommand({ Bucket: BUCKET, Key: it.key }),
      { expiresIn: URL_TTL_SECONDS },
    )));
    return json({ urls, expiresIn: URL_TTL_SECONDS });
  }
  if (path === '/list') {
    const prefix = String(body?.prefix || '');
    if (!PREFIX_RE.test(prefix)) return fail(400, 'bad prefix');
    const keys: string[] = [];
    let token: string | undefined;
    do {
      const r = await s3.send(new ListObjectsV2Command({ Bucket: BUCKET, Prefix: prefix, ContinuationToken: token }));
      for (const o of r.Contents || []) if (o.Key) keys.push(o.Key);
      token = r.IsTruncated ? r.NextContinuationToken : undefined;
    } while (token && keys.length < 10000);
    return json({ keys });
  }
  if (path === '/delete') {
    const keys: string[] = Array.isArray(body?.keys) ? body.keys : [];
    if (!keys.length || keys.length > 1000 || keys.some((k) => typeof k !== 'string' || !KEY_RE.test(k))) return fail(400, 'bad keys');
    await s3.send(new DeleteObjectsCommand({ Bucket: BUCKET, Delete: { Objects: keys.map((Key) => ({ Key })), Quiet: true } }));
    return json({ deleted: keys.length });
  }
  return fail(404, 'not found');
}

export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    if (request.method === 'GET' && url.pathname === '/health') return json({ ok: true });
    if (request.method !== 'POST') return fail(405, 'POST only');
    const auth = request.headers.get('authorization') || '';
    if (!auth.toLowerCase().startsWith('bearer ')) return fail(401, 'missing token');
    try {
      await jwtVerify(auth.slice(7), jwks, { issuer });
    } catch {
      return fail(401, 'invalid or expired token');
    }
    let body: any = null;
    try { body = await request.json(); } catch { return fail(400, 'invalid JSON'); }
    try {
      return await handle(url.pathname, body);
    } catch (e: any) {
      console.error('storage error', e?.name, e?.message);
      return fail(502, `storage error: ${e?.name || 'unknown'}`);
    }
  },
};
