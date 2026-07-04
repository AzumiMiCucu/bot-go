// =============================================================================
// googleLens.js — Google Lens reverse image search (sumber gambar).
// Satu fungsi, NATIVE Node.js v25 (tanpa dependency: pakai fetch/FormData/Blob
// bawaan). Siap dipasang di API ps.azumi.dev.
//
//   const { googleLens } = require('./googleLens')   // CommonJS
//   // atau: import { googleLens } from './googleLens.js'  (ESM)
//
//   const hasil = await googleLens(bufferGambar, { nid: '<cookie NID Google>' })
//
// -----------------------------------------------------------------------------
// CARA KERJA (diverifikasi live 2026-07-04):
//   1) POST multipart gambar (field `encoded_image`) ke lens.google.com/v3/upload
//      → 303 redirect, header Location = URL hasil (www.google.com/search?...udm=26).
//      Bagian ini PASTI jalan (upload + baca Location sudah diverifikasi).
//   2) GET URL hasil dengan header browser penuh + cookie consent (+NID bila ada).
//   3) Parse blok `AF_initDataCallback([...])` (data hasil di-SSR sebagai array
//      bersarang) secara STRUKTUR-AGNOSTIK: kumpulkan tiap grup yang berisi URL
//      eksternal + judul + thumbnail. Fallback: anchor `ping="/url?"`.
//
// -----------------------------------------------------------------------------
// PENTING — BATASAN NYATA (baca ini):
//   • Google me-render hasil Lens via JavaScript. Server hanya mengirim HTML
//     berisi data (SSR) bila IP + cookie dianggap "browser sungguhan". IP VPS/
//     datacenter yang sering menembak sering dibalas SHELL JS kosong / 403.
//   • Cara paling ampuh menaikkan tingkat keberhasilan: kirim cookie `NID`
//     dari akun Google yang login (opsi `nid`). Tanpa itu, sering 0 hasil.
//   • Kalau scraping kosong, fungsi TETAP mengembalikan `resultUrl` — tautan
//     Google Lens yang valid & bisa dibuka manual. Jadikan itu fallback di bot.
// =============================================================================

const UPLOAD_BASE = 'https://lens.google.com/v3/upload';

// UA browser desktop yang ditiru dari capture asli.
const DEFAULT_UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 ' +
  '(KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36';

// Cookie consent default (melewati interstitial consent EU → Google mau SSR).
const CONSENT_COOKIE =
  'SOCS=CAISNQgDEitib3FfaWRlbnRpdHlmcm9udGVuZHVpc2VydmVyXzIwMjQwODI3LjA4X3AwGgJlbiADGgYIgLC_tgY; ' +
  'CONSENT=YES+cb.20220301-11-p0.en+FX+111';

/**
 * googleLens — cari sumber sebuah gambar via Google Lens.
 *
 * @param {Buffer|Uint8Array|ArrayBuffer|string} image
 *        Data gambar. Boleh Buffer/Uint8Array/ArrayBuffer, atau string base64
 *        (boleh diawali "data:image/...;base64,").
 * @param {object} [options]
 * @param {string} [options.nid]       Nilai cookie Google `NID` (SANGAT disarankan).
 * @param {string} [options.cookie]    Cookie mentah tambahan (dipakai apa adanya).
 * @param {string} [options.hl='id']   Bahasa hasil.
 * @param {number} [options.limit=20]  Maksimum hasil yang dikembalikan.
 * @param {number} [options.timeoutMs=30000] Timeout per request.
 * @param {string} [options.userAgent] Override User-Agent.
 * @returns {Promise<{ok:boolean, engine:string, resultUrl:string, count:number,
 *                     results:Array<{title:string, source:string, domain:string,
 *                     thumbnail:string}>, note?:string}>}
 */
async function googleLens(image, options = {}) {
  const {
    nid = '',
    cookie = '',
    hl = 'id',
    limit = 20,
    timeoutMs = 30000,
    userAgent = DEFAULT_UA,
  } = options;

  const bytes = toBytes(image);
  if (!bytes || bytes.length === 0) throw new Error('data gambar kosong');

  const cookieHeader = buildCookie(nid, cookie);

  // 1) UPLOAD → ambil Location (URL hasil).
  const resultUrl = await lensUpload(bytes, { userAgent, hl, cookieHeader, timeoutMs });

  // 2) FETCH halaman hasil.
  const html = await fetchWithTimeout(
    resultUrl,
    {
      headers: {
        'User-Agent': userAgent,
        Cookie: cookieHeader,
        Accept: 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
        'Accept-Language': `${hl}-ID,${hl};q=0.9,en;q=0.8`,
        Referer: 'https://lens.google.com/',
        'sec-fetch-site': 'same-origin',
        'sec-fetch-mode': 'navigate',
        'sec-fetch-dest': 'document',
        'Upgrade-Insecure-Requests': '1',
      },
    },
    timeoutMs
  ).then((r) => r.text());

  // 3) PARSE.
  let results = parseFromAF(html);
  if (results.length === 0) results = parseFromAnchors(html);

  results = dedupe(results).slice(0, limit);

  const jsShell = results.length === 0 && /enablejs|Please click|noscript/i.test(html);
  return {
    ok: results.length > 0,
    engine: 'google-lens',
    resultUrl,
    count: results.length,
    results,
    ...(results.length === 0 && {
      note: jsShell
        ? 'Google mengirim shell JS (tidak ter-SSR) — IP kemungkinan diblok/anti-bot. ' +
          'Coba kirim cookie NID akun login, atau pakai resultUrl sebagai fallback.'
        : 'Tidak ada hasil terparse. Pakai resultUrl sebagai fallback.',
    }),
  };
}

// ---------------------------------------------------------------------------
// Langkah 1: upload gambar, balikan URL hasil (Location dari 303).
// ---------------------------------------------------------------------------
async function lensUpload(bytes, { userAgent, hl, cookieHeader, timeoutMs }) {
  const fd = new FormData();
  fd.append('encoded_image', new Blob([bytes], { type: detectMime(bytes) }), 'image.jpg');

  // st = timestamp ms; vpw/vph = viewport (nilai wajar apa saja diterima).
  const st = Date.now();
  const url =
    `${UPLOAD_BASE}?ep=gsbubb&st=${st}&authuser=0&hl=${encodeURIComponent(hl)}` +
    `&vpw=980&vph=1873`;

  const resp = await fetchWithTimeout(
    url,
    {
      method: 'POST',
      body: fd,
      redirect: 'manual', // WAJIB: kita butuh header Location, bukan follow.
      headers: {
        'User-Agent': userAgent,
        Cookie: cookieHeader,
        Origin: 'https://www.google.com',
        Referer: 'https://www.google.com/',
        Accept: 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
        'Accept-Language': `${hl}-ID,${hl};q=0.9,en;q=0.8`,
      },
    },
    timeoutMs
  );

  // undici mengekspos Location pada redirect manual (status 3xx).
  const loc = resp.headers.get('location');
  if (!loc) {
    throw new Error(
      `Lens: tidak mendapat URL hasil (status ${resp.status}) — upload ditolak Google`
    );
  }
  return loc;
}

// ---------------------------------------------------------------------------
// Parser A (utama): blok AF_initDataCallback → data array bersarang → deep-walk.
// Struktur-agnostik: kumpulkan grup array yang punya URL eksternal + judul.
// ---------------------------------------------------------------------------
function parseFromAF(html) {
  const out = [];
  const marker = 'AF_initDataCallback(';
  let i = 0;
  while ((i = html.indexOf(marker, i)) !== -1) {
    const objStart = i + marker.length; // menunjuk ke '{'
    i = objStart;
    const dataIdx = html.indexOf('data:', objStart);
    if (dataIdx === -1) continue;
    const arrStart = html.indexOf('[', dataIdx);
    if (arrStart === -1) continue;
    const arrStr = extractBalanced(html, arrStart, '[', ']');
    if (!arrStr) continue;
    let parsed;
    try {
      parsed = JSON.parse(arrStr);
    } catch {
      continue; // bagian yang tak valid JSON kita lewati saja
    }
    walkForMatches(parsed, out);
  }
  return out;
}

// Telusuri rekursif; tiap array yang memuat URL eksternal → jadikan 1 kandidat.
function walkForMatches(node, out) {
  if (Array.isArray(node)) {
    const strings = node.filter((x) => typeof x === 'string');
    const urls = strings.filter(isExternalUrl);
    if (urls.length) {
      const source = urls.find((u) => !looksLikeImage(u)) || urls[0];
      const thumbnail =
        strings.find((s) => /gstatic\.com|encrypted-tbn|\.(jpg|jpeg|png|webp)(\?|$)/i.test(s)) || '';
      const title =
        strings.find(
          (s) => !isUrl(s) && s.length >= 4 && s.length <= 200 && /[a-zA-Z]/.test(s) && !isJunk(s)
        ) || '';
      if (source) out.push({ title, source, domain: domainOf(source), thumbnail });
    }
    for (const child of node) walkForMatches(child, out);
  } else if (node && typeof node === 'object') {
    for (const v of Object.values(node)) walkForMatches(v, out);
  }
}

// ---------------------------------------------------------------------------
// Parser B (fallback): anchor hasil klasik `<a href=".." ping="/url?..">`.
// ---------------------------------------------------------------------------
function parseFromAnchors(html) {
  const out = [];
  const re = /<a[^>]*\shref="(https?:\/\/[^"]+)"[^>]*\sping="\/url\?[^"]*"[^>]*>/gis;
  let m;
  while ((m = re.exec(html)) !== null) {
    const link = decodeHtml(m[1]);
    if (!isExternalUrl(link)) continue;
    // Judul & domain: cari heading/label di jendela ~1500 char setelah anchor.
    const win = html.slice(m.index, m.index + 1500);
    const titleM = win.match(/role="heading"[^>]*>\s*(?:<[^>]+>\s*)*([^<]{3,180})/i);
    out.push({
      title: titleM ? decodeHtml(titleM[1]).trim() : '',
      source: link,
      domain: domainOf(link),
      thumbnail: '',
    });
  }
  return out;
}

// ---------------------------------------------------------------------------
// Util.
// ---------------------------------------------------------------------------
function toBytes(image) {
  if (image == null) return null;
  if (Buffer.isBuffer(image)) return image;
  if (image instanceof Uint8Array) return Buffer.from(image);
  if (image instanceof ArrayBuffer) return Buffer.from(new Uint8Array(image));
  if (typeof image === 'string') {
    const b64 = image.replace(/^data:[^;]+;base64,/, '');
    return Buffer.from(b64, 'base64');
  }
  return null;
}

function buildCookie(nid, extra) {
  const parts = [CONSENT_COOKIE];
  if (nid) parts.push('NID=' + String(nid).replace(/^NID=/i, '').trim());
  if (extra) parts.push(extra);
  return parts.join('; ');
}

function detectMime(b) {
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8) return 'image/jpeg';
  if (b.length >= 8 && b[0] === 0x89 && b[1] === 0x50) return 'image/png';
  if (b.length >= 12 && b.toString('ascii', 0, 4) === 'RIFF' && b.toString('ascii', 8, 12) === 'WEBP')
    return 'image/webp';
  if (b.length >= 3 && b.toString('ascii', 0, 3) === 'GIF') return 'image/gif';
  return 'image/jpeg';
}

// Ekstrak substring dari `open` sampai `close` yang seimbang, hormati string JSON.
function extractBalanced(s, start, open, close) {
  let depth = 0;
  let inStr = false;
  let esc = false;
  for (let k = start; k < s.length; k++) {
    const c = s[k];
    if (inStr) {
      if (esc) esc = false;
      else if (c === '\\') esc = true;
      else if (c === '"') inStr = false;
      continue;
    }
    if (c === '"') inStr = true;
    else if (c === open) depth++;
    else if (c === close) {
      depth--;
      if (depth === 0) return s.slice(start, k + 1);
    }
  }
  return null;
}

function isUrl(s) {
  return typeof s === 'string' && /^https?:\/\//i.test(s);
}
function isExternalUrl(s) {
  if (!isUrl(s)) return false;
  return !/(^https?:\/\/(?:[a-z0-9-]+\.)*(google\.com|gstatic\.com|googleusercontent\.com|googleapis\.com|youtube\.com|schema\.org)(\/|$))/i.test(
    s
  );
}
function looksLikeImage(u) {
  return /\.(jpg|jpeg|png|webp|gif|svg)(\?|$)/i.test(u) || /gstatic\.com|encrypted-tbn/i.test(u);
}
function isJunk(s) {
  // Buang string non-judul: hash, key internal (ds:1), mime, base64 panjang, dsb.
  return (
    /^[A-Za-z0-9_+/=-]{25,}$/.test(s) ||
    /^(ds:|GRID|SDCH|data:|rgb|#[0-9a-f]{3,8})/i.test(s) ||
    /^[\d.,\s-]+$/.test(s)
  );
}
function domainOf(u) {
  try {
    return new URL(u).hostname.replace(/^www\./, '');
  } catch {
    return '';
  }
}
function decodeHtml(s) {
  return s
    .replace(/&amp;/g, '&')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&#x2f;/gi, '/');
}
function dedupe(list) {
  const seen = new Set();
  const out = [];
  for (const r of list) {
    if (!r.source) continue;
    const key = r.source.split('#')[0].split('?')[0];
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(r);
  }
  return out;
}

async function fetchWithTimeout(url, opts, timeoutMs) {
  const ac = new AbortController();
  const t = setTimeout(() => ac.abort(), timeoutMs);
  try {
    return await fetch(url, { ...opts, signal: ac.signal });
  } finally {
    clearTimeout(t);
  }
}

module.exports = { googleLens };
// ESM: hapus baris di atas, ganti dengan:  export { googleLens };
