// Reference verifier for spec/vectors, runnable with no packages:
//   node --test reference/verify-reference.test.mjs
// It is the executable statement of what every verifier (Go, browser, Python) must return.
// P1-6 moves these functions into web/verify/verify.js.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const VEC = join(dirname(fileURLToPath(import.meta.url)), "..", "spec", "vectors");
const enc = new TextEncoder();
const subtle = globalThis.crypto.subtle;

export function jcs(v) {
  if (v === null || typeof v !== "object") {
    if (typeof v === "number" && !Number.isFinite(v)) throw new Error("non-finite number");
    return JSON.stringify(v);
  }
  if (Array.isArray(v)) return "[" + v.map(jcs).join(",") + "]";
  return "{" + Object.keys(v).sort().map((k) => JSON.stringify(k) + ":" + jcs(v[k])).join(",") + "}";
}
const concat = (...a) => { const o = new Uint8Array(a.reduce((n, x) => n + x.length, 0)); let i = 0; for (const x of a) { o.set(x, i); i += x.length; } return o; };
export function pae(type, body) { const t = enc.encode(type); return concat(enc.encode(`DSSEv1 ${t.length} `), t, enc.encode(` ${body.length} `), body); }
export function derToRaw(der) {
  let i = 2; if (der[1] & 0x80) i += der[1] & 0x7f;
  const int = () => { i++; const len = der[i++]; let v = der.slice(i, i + len); i += len; while (v.length > 32 && v[0] === 0) v = v.slice(1); const o = new Uint8Array(32); o.set(v, 32 - v.length); return o; };
  const r = int(), s = int(); return concat(r, s);
}
const b64 = (s) => Uint8Array.from(Buffer.from(s, "base64"));
const hex = (b) => Buffer.from(b).toString("hex");
const unhex = (h) => Uint8Array.from(Buffer.from(h, "hex"));
const sha = async (b) => new Uint8Array(await subtle.digest("SHA-256", b));

export async function verifyMerkle(c) {
  const d = c.disclosed;
  let h = await sha(concat(new Uint8Array([0]), unhex(d.salt), enc.encode(d.trace_id)));
  for (const s of d.proof) { const x = unhex(s.hash); h = await sha(concat(new Uint8Array([1]), s.side === "L" ? x : h, s.side === "L" ? h : x)); }
  return hex(h) === c.trace_merkle_root;
}

export async function verifyEnvelope(env, jwk, prevEnv) {
  const payload = b64(env.payload);
  const key = await subtle.importKey("jwk", { kty: jwk.kty, crv: jwk.crv, x: jwk.x, y: jwk.y }, { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]);
  const signature = await subtle.verify({ name: "ECDSA", hash: "SHA-256" }, key, derToRaw(b64(env.signatures[0].sig)), pae(env.payloadType, payload));
  const st = JSON.parse(new TextDecoder().decode(payload));
  const subject_digest = hex(await sha(enc.encode(jcs(st.predicate)))) === st.subject[0].digest.sha256;
  const chain = prevEnv ? st.predicate.prev_pack_digest === "sha256:" + hex(await sha(b64(prevEnv.payload))) : null;
  const merkle = await verifyMerkle(st.predicate.commitments);
  return { signature, subject_digest, chain, merkle, verified: signature && subject_digest && merkle && chain !== false };
}

const manifest = JSON.parse(readFileSync(join(VEC, "manifest.json"), "utf8"));
const jwk = JSON.parse(readFileSync(join(VEC, manifest.jwks), "utf8")).keys[0];
const load = (f) => JSON.parse(readFileSync(join(VEC, f), "utf8"));

for (const c of manifest.cases) {
  test(`vector ${c.name}`, async () => {
    const got = await verifyEnvelope(load(c.file), jwk, c.prev ? load(c.prev) : null);
    assert.deepEqual(got, c.expect);
  });
}

test("pass payloads are already in canonical form", async () => {
  for (const c of manifest.cases.filter((x) => x.name.startsWith("pass"))) {
    const payload = Buffer.from(load(c.file).payload, "base64").toString("utf8");
    assert.equal(jcs(JSON.parse(payload)), payload);
  }
});

const jcsVectors = JSON.parse(readFileSync(join(VEC, "..", "jcs-vectors.json"), "utf8"));
for (const v of jcsVectors.cases) {
  test(`jcs ${v.name}`, () => assert.equal(jcs(JSON.parse(v.input)), v.expected));
}
