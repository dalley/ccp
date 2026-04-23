#!/usr/bin/env node
// postinstall script: downloads the platform-specific ccp binary from the
// GitHub Release matching this package's version. Small and dependency-free
// on purpose so there's no supply-chain surface area.
//
// Skips the download when running inside CI checkouts where a local binary
// is already present (e.g. developer uses `npm link` against a dev build).

const fs = require("fs");
const os = require("os");
const path = require("path");
const https = require("https");
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

const key = `${os.platform()}-${os.arch()}`;
const suffix = PLATFORM[key];
if (!suffix) {
  console.error(`ccp: unsupported platform ${key}. Supported: ${Object.keys(PLATFORM).join(", ")}.`);
  process.exit(1);
}

const binDir = path.join(__dirname, "bin");
const binPath = path.join(binDir, "ccp");
if (fs.existsSync(binPath)) {
  // Already installed (e.g. developer using `npm link`).
  process.exit(0);
}
fs.mkdirSync(binDir, { recursive: true });

const url = `https://github.com/dalley/ccp/releases/download/v${version}/ccp_${version}_${suffix}.tar.gz`;

console.log(`ccp: downloading ${url}`);

function follow(url, depth, done) {
  if (depth <= 0) return done(new Error("too many redirects"));
  https.get(url, (res) => {
    if ([301, 302, 303, 307, 308].includes(res.statusCode)) {
      return follow(res.headers.location, depth - 1, done);
    }
    if (res.statusCode !== 200) {
      return done(new Error(`HTTP ${res.statusCode} from ${url}`));
    }
    done(null, res);
  }).on("error", done);
}

follow(url, 5, (err, res) => {
  if (err) {
    console.error(`ccp: download failed: ${err.message}`);
    process.exit(1);
  }
  const chunks = [];
  res.on("data", (c) => chunks.push(c));
  res.on("end", () => {
    try {
      const gz = Buffer.concat(chunks);
      const tar = zlib.gunzipSync(gz);
      // Minimal tar extractor: find the `ccp` entry and write it to binPath.
      extractBinary(tar, "ccp", binPath);
      fs.chmodSync(binPath, 0o755);
      try { execFileSync(binPath, ["version"], { stdio: "ignore" }); } catch (_) {}
      console.log(`ccp: installed ${binPath}`);
    } catch (e) {
      console.error(`ccp: extract failed: ${e.message}`);
      process.exit(1);
    }
  });
});

// Walks a USTAR-format tar buffer and writes the named regular file out.
// Good enough for goreleaser's archives; we don't need a full tar lib.
function extractBinary(buf, name, out) {
  for (let offset = 0; offset + 512 <= buf.length; ) {
    const header = buf.slice(offset, offset + 512);
    if (header[0] === 0) break; // end of archive

    const rawName = header.slice(0, 100).toString("utf8").replace(/\0.*$/, "");
    const size = parseInt(header.slice(124, 136).toString("utf8").trim(), 8) || 0;
    const type = String.fromCharCode(header[156]);

    const dataStart = offset + 512;
    const dataEnd = dataStart + size;

    if (type === "0" && path.basename(rawName) === name) {
      fs.writeFileSync(out, buf.slice(dataStart, dataEnd));
      return;
    }
    // Round size up to 512-byte block.
    offset = dataStart + Math.ceil(size / 512) * 512;
  }
  throw new Error(`${name} not found in archive`);
}
