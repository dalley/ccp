#!/usr/bin/env node
// postinstall script: downloads the platform-specific ccp binary from the
// GitHub Release matching this package's version. Small and dependency-free
// on purpose so there's no supply-chain surface area.
//
// Security hardening:
//   * Redirects are only followed to github.com / *.githubusercontent.com.
//   * The archive SHA-256 is verified against checksums.txt from the same
//     release; a mismatch aborts without writing the binary.
//   * Overall deadline + socket timeout bound the download so a stalled
//     CDN cannot hang `npm install` indefinitely.
//   * Download size is capped to prevent a hostile response from pinning
//     the process with unbounded heap growth.
//
// Skips the download when running inside CI checkouts where a local binary
// is already present (e.g. developer uses `npm link` against a dev build).

const fs = require("fs");
const os = require("os");
const path = require("path");
const https = require("https");
const crypto = require("crypto");
const { execFileSync } = require("child_process");
const zlib = require("zlib");

const pkg = require("./package.json");
const version = pkg.version;

// Maps Node's os.platform()/os.arch() to the asset suffix goreleaser emits.
const PLATFORM = {
  "darwin-arm64": "darwin_arm64",
  "darwin-x64":   "darwin_amd64",
  "linux-arm64":  "linux_arm64",
  "linux-x64":    "linux_amd64",
};

// Allowed redirect hosts. GitHub Releases redirect through objects.githubusercontent.com
// and sometimes github-releases.githubusercontent.com; pin to github.com and any
// *.githubusercontent.com child.
const ALLOWED_HOSTS = [
  /^github\.com$/,
  /^[a-z0-9-]+\.githubusercontent\.com$/,
];

// Safety bounds.
const MAX_DOWNLOAD_BYTES = 64 * 1024 * 1024; // 64 MiB — generous for a Go CLI tarball.
const SOCKET_TIMEOUT_MS = 30_000;
const OVERALL_DEADLINE_MS = 120_000;

const key = `${os.platform()}-${os.arch()}`;
const suffix = PLATFORM[key];
if (!suffix) {
  console.error(`ccp: unsupported platform ${key}. Supported: ${Object.keys(PLATFORM).join(", ")}.`);
  process.exit(1);
}

const binDir = path.join(__dirname, "bin");
// Write the native binary to a sibling filename. The shim script that ships
// in the tarball IS bin/ccp, so naming the native binary "ccp" would make
// existsSync below trip on our own shim and skip the download permanently.
const binPath = path.join(binDir, "ccp-bin");
if (fs.existsSync(binPath)) {
  // Already installed (e.g. developer using `npm link`).
  process.exit(0);
}
fs.mkdirSync(binDir, { recursive: true });

const releaseBase = `https://github.com/dalley/ccp/releases/download/v${version}`;
const archiveName = `ccp_${version}_${suffix}.tar.gz`;
const archiveURL = `${releaseBase}/${archiveName}`;
const checksumsURL = `${releaseBase}/checksums.txt`;

async function main() {
  const deadline = Date.now() + OVERALL_DEADLINE_MS;

  console.log(`ccp: fetching checksums.txt`);
  const checksumsBuf = await download(checksumsURL, deadline, 1024 * 1024); // 1 MiB cap
  const expected = parseChecksums(checksumsBuf.toString("utf8"))[archiveName];
  if (!expected) {
    throw new Error(`${archiveName} missing from checksums.txt`);
  }

  console.log(`ccp: downloading ${archiveURL}`);
  const gz = await download(archiveURL, deadline, MAX_DOWNLOAD_BYTES);

  const got = crypto.createHash("sha256").update(gz).digest("hex");
  if (got !== expected) {
    throw new Error(`SHA-256 mismatch for ${archiveName}: expected ${expected}, got ${got}`);
  }

  const tar = zlib.gunzipSync(gz);
  extractBinary(tar, "ccp", binPath);
  fs.chmodSync(binPath, 0o755);
  // Smoke-test the binary so an obviously broken download fails loudly
  // during install rather than silently at first use.
  execFileSync(binPath, ["version"], { stdio: "ignore" });
  console.log(`ccp: installed ${binPath}`);
}

main().catch((err) => {
  console.error(`ccp: install failed: ${err.message}`);
  try { fs.unlinkSync(binPath); } catch (_) {}
  process.exit(1);
});

// download fetches url and returns a Buffer. Enforces host allow-listing on
// every redirect, a per-socket timeout, an overall deadline, and a size cap.
function download(url, deadline, maxBytes) {
  return new Promise((resolve, reject) => {
    follow(url, 5, (err, res) => {
      if (err) return reject(err);

      let received = 0;
      const chunks = [];
      res.on("data", (c) => {
        received += c.length;
        if (received > maxBytes) {
          res.destroy(new Error(`response exceeds ${maxBytes} bytes`));
          return;
        }
        if (Date.now() > deadline) {
          res.destroy(new Error("overall download deadline exceeded"));
          return;
        }
        chunks.push(c);
      });
      res.on("end", () => resolve(Buffer.concat(chunks)));
      res.on("error", reject);
    });

    function follow(u, depth, done) {
      if (depth <= 0) return done(new Error("too many redirects"));
      const parsed = new URL(u);
      if (!ALLOWED_HOSTS.some((re) => re.test(parsed.hostname))) {
        return done(new Error(`refusing redirect to disallowed host ${parsed.hostname}`));
      }
      const req = https.get(u, (res) => {
        if ([301, 302, 303, 307, 308].includes(res.statusCode)) {
          // Drain so the socket is returned to the agent, then follow.
          res.resume();
          return follow(res.headers.location, depth - 1, done);
        }
        if (res.statusCode !== 200) {
          return done(new Error(`HTTP ${res.statusCode} from ${u}`));
        }
        done(null, res);
      });
      req.setTimeout(SOCKET_TIMEOUT_MS, () => {
        req.destroy(new Error(`socket idle for ${SOCKET_TIMEOUT_MS}ms`));
      });
      req.on("error", done);
    }
  });
}

// parseChecksums turns goreleaser's `<sha256>  <filename>\n` format into a map.
function parseChecksums(text) {
  const out = {};
  for (const line of text.split(/\r?\n/)) {
    const m = line.match(/^([a-f0-9]{64})\s+(.+)$/);
    if (m) out[m[2]] = m[1];
  }
  return out;
}

// Walks a USTAR-format tar buffer and writes the named regular file out.
// Good enough for goreleaser's archives; we don't need a full tar lib.
function extractBinary(buf, name, out) {
  for (let offset = 0; offset + 512 <= buf.length; ) {
    const header = buf.slice(offset, offset + 512);
    if (header[0] === 0) break; // end of archive

    const rawName = header.slice(0, 100).toString("utf8").replace(/\0.*$/, "");
    // Parse size octal; reject malformed fields rather than treating NaN
    // as 0 (which would produce a zero-byte step and an infinite loop).
    const sizeStr = header.slice(124, 136).toString("utf8").trim();
    const size = parseInt(sizeStr, 8);
    if (!Number.isFinite(size) || size < 0) {
      throw new Error(`malformed tar size field ${JSON.stringify(sizeStr)}`);
    }
    const type = String.fromCharCode(header[156]);

    const dataStart = offset + 512;
    const dataEnd = dataStart + size;
    if (dataEnd > buf.length) {
      throw new Error("tar entry extends past end of archive");
    }

    if (type === "0" && path.basename(rawName) === name) {
      fs.writeFileSync(out, buf.slice(dataStart, dataEnd));
      return;
    }
    // Round size up to 512-byte block.
    offset = dataStart + Math.ceil(size / 512) * 512;
  }
  throw new Error(`${name} not found in archive`);
}
