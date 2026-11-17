import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";
import { PassThrough } from "node:stream";
import test from "node:test";

const require = createRequire(import.meta.url);
const {
  YAOMU_WSL_IMAGE_URL,
  classifyWslListFailure,
  decodeWslOutput,
  downloadFixedWslImage,
  ensureYaomuWslImage,
  parseWslDefaultDistro,
  parseWslDistroNames,
} = require("../dist/wsl-image.js");

function createFakeChild() {
  const child = new EventEmitter();
  child.stdout = new PassThrough();
  child.stderr = new PassThrough();
  child.pid = 4242;
  child.exitCode = null;
  child.signalCode = null;
  child.kills = [];
  child.kill = (signal = "SIGTERM") => {
    child.kills.push(signal);
    return true;
  };
  child.finish = (code = 0, signal = null) => {
    if (child.exitCode !== null || child.signalCode !== null) return;
    child.exitCode = code;
    child.signalCode = signal;
    child.stdout.end();
    child.stderr.end();
    child.emit("exit", code, signal);
    child.emit("close", code, signal);
  };
  return child;
}

function createController(handlers = []) {
  const calls = [];
  const spawnImpl = (command, args, options) => {
    const child = createFakeChild();
    const call = { command, args, options, child };
    calls.push(call);
    const handler = handlers[calls.length - 1];
    if (handler) handler(call);
    return child;
  };
  return { spawnImpl, calls };
}

function complete(call, { code = 0, stdout = "", stderr = "" } = {}) {
  queueMicrotask(() => {
    if (stdout) call.child.stdout.write(stdout);
    if (stderr) call.child.stderr.write(stderr);
    call.child.finish(code);
  });
}

function stopOnKill(call) {
  call.child.kill = (signal = "SIGTERM") => {
    call.child.kills.push(signal);
    queueMicrotask(() => call.child.finish(null, signal));
    return true;
  };
}

async function tempDir() {
  return mkdtemp(path.join(tmpdir(), "yaomu-wsl-image-"));
}

test("image url is the confirmed https rootfs", () => {
  assert.equal(
    YAOMU_WSL_IMAGE_URL,
    "https://static-pro.guangdianyun.tv/static/common/yaomu-ubuntu.tar.gz",
  );
});

test("decode and parse wsl list output", () => {
  const utf16 = Buffer.concat([
    Buffer.from([0xff, 0xfe]),
    Buffer.from("Ubuntu\r\nyaomu\r\n", "utf16le"),
  ]);
  const text = decodeWslOutput(utf16);
  assert.deepEqual(parseWslDistroNames(text), ["Ubuntu", "yaomu"]);
  assert.equal(parseWslDefaultDistro("* Ubuntu\r\nyaomu\r\n"), "Ubuntu");
  assert.equal(parseWslDefaultDistro("  NAME STATE\n"), null);
  assert.equal(classifyWslListFailure("Wsl is not installed"), "unavailable");
  assert.equal(
    classifyWslListFailure("Windows Subsystem for Linux has no installed distributions."),
    "none",
  );
  assert.equal(classifyWslListFailure("temporary failure"), "unknown");
});

test("non-windows and distro override do not spawn", async () => {
  const dir = await tempDir();
  try {
    const skipped = await ensureYaomuWslImage({
      platform: "linux",
      userDataDir: dir,
      spawnImpl: () => {
        throw new Error("spawned");
      },
    });
    assert.deepEqual(skipped, { status: "skipped", reason: "not_windows" });

    const override = await ensureYaomuWslImage({
      platform: "win32",
      env: { YAOMU_WSL_DISTRO: "Ubuntu" },
      userDataDir: dir,
      spawnImpl: () => {
        throw new Error("spawned");
      },
    });
    assert.deepEqual(override, { status: "skipped", reason: "distro_override" });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("existing yaomu distro skips download", async () => {
  const dir = await tempDir();
  const { spawnImpl, calls } = createController([
    (call) => {
      const encoded = Buffer.concat([
        Buffer.from([0xff, 0xfe]),
        Buffer.from("Ubuntu\r\nyaomu\r\n", "utf16le"),
      ]);
      queueMicrotask(() => {
        call.child.stdout.write(encoded);
        call.child.finish(0);
      });
    },
  ]);
  try {
    const result = await ensureYaomuWslImage({
      platform: "win32",
      userDataDir: dir,
      spawnImpl,
      listTimeoutMs: 1000,
      downloadImpl: async () => {
        throw new Error("downloaded");
      },
    });
    assert.deepEqual(result, { status: "skipped", reason: "distro_present" });
    assert.equal(calls.length, 1);
    assert.deepEqual(calls[0].args, ["-l", "-q"]);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("missing distro downloads, imports, and restores the previous default", async () => {
  const dir = await tempDir();
  const { spawnImpl, calls } = createController([
    (call) => complete(call, { stdout: "Ubuntu\n" }),
    (call) => complete(call, { stdout: "  NAME STATE VERSION\n* Ubuntu Stopped 2\n" }),
    (call) => complete(call, { code: 0 }),
    (call) => complete(call, { stdout: "* yaomu Stopped 2\n" }),
    (call) => complete(call, { code: 0 }),
  ]);
  let downloaded = 0;
  try {
    const result = await ensureYaomuWslImage({
      platform: "win32",
      userDataDir: dir,
      spawnImpl,
      listTimeoutMs: 1000,
      importTimeoutMs: 1000,
      downloadImpl: async (destination) => {
        downloaded += 1;
        await writeFile(destination, "rootfs");
      },
    });
    assert.deepEqual(result, { status: "imported" });
    assert.equal(downloaded, 1);
    assert.deepEqual(calls.map((call) => call.args[0]), [
      "-l",
      "-l",
      "--import",
      "-l",
      "--set-default",
    ]);
    assert.deepEqual(calls[2].args, [
      "--import",
      "yaomu",
      path.join(dir, "wsl", "distros", "yaomu"),
      path.join(dir, "wsl", "cache", "yaomu-ubuntu.tar.gz"),
      "--version",
      "2",
    ]);
    assert.deepEqual(calls[4].args, ["--set-default", "Ubuntu"]);
    assert.equal(calls[2].options.shell, undefined);
    await assert.rejects(
      () => readFile(path.join(dir, "wsl", "cache", "yaomu-ubuntu.tar.gz")),
      (error) => error?.code === "ENOENT",
    );
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("no prior default does not call set-default", async () => {
  const dir = await tempDir();
  const { spawnImpl, calls } = createController([
    (call) => complete(call, {
      code: 1,
      stdout: "Windows Subsystem for Linux has no installed distributions.\n",
    }),
    (call) => complete(call, { code: 1, stdout: "no installed distributions\n" }),
    (call) => complete(call, { code: 0 }),
  ]);
  try {
    const result = await ensureYaomuWslImage({
      platform: "win32",
      userDataDir: dir,
      spawnImpl,
      listTimeoutMs: 1000,
      importTimeoutMs: 1000,
      downloadImpl: async (destination) => {
        await writeFile(destination, "rootfs");
      },
    });
    assert.equal(result.status, "imported");
    assert.equal(calls.some((call) => call.args[0] === "--set-default"), false);
    assert.equal(calls[2].args[0], "--import");
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("wsl unavailable and unknown list failure do not download", async () => {
  const dir = await tempDir();
  try {
    const unavailable = createController([
      (call) => complete(call, { code: 1, stdout: "The Windows Subsystem for Linux is not installed.\n" }),
    ]);
    const skipped = await ensureYaomuWslImage({
      platform: "win32",
      userDataDir: dir,
      spawnImpl: unavailable.spawnImpl,
      listTimeoutMs: 1000,
      downloadImpl: async () => {
        throw new Error("downloaded");
      },
    });
    assert.deepEqual(skipped, { status: "skipped", reason: "wsl_unavailable" });

    const unknown = createController([
      (call) => complete(call, { code: 1, stdout: "access denied\n" }),
    ]);
    const failed = await ensureYaomuWslImage({
      platform: "win32",
      userDataDir: dir,
      spawnImpl: unknown.spawnImpl,
      listTimeoutMs: 1000,
      downloadImpl: async () => {
        throw new Error("downloaded");
      },
    });
    assert.deepEqual(failed, { status: "failed", reason: "list_failed" });
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("completed archive is reused and a failed import keeps it", async () => {
  const dir = await tempDir();
  const archive = path.join(dir, "wsl", "cache", "yaomu-ubuntu.tar.gz");
  await writeFile(archive, "rootfs").catch(async () => {
    const { mkdir } = await import("node:fs/promises");
    await mkdir(path.dirname(archive), { recursive: true });
    await writeFile(archive, "rootfs");
  });
  const { spawnImpl, calls } = createController([
    (call) => complete(call, { stdout: "Ubuntu\n" }),
    (call) => complete(call, { stdout: "* Ubuntu Stopped 2\n" }),
    (call) => complete(call, { code: 1 }),
  ]);
  try {
    const result = await ensureYaomuWslImage({
      platform: "win32",
      userDataDir: dir,
      spawnImpl,
      listTimeoutMs: 1000,
      importTimeoutMs: 1000,
      downloadImpl: async () => {
        throw new Error("downloaded");
      },
    });
    assert.deepEqual(result, { status: "failed", reason: "import_failed" });
    assert.equal(await readFile(archive, "utf8"), "rootfs");
    assert.equal(calls.filter((call) => call.args[0] === "--import").length, 1);
    assert.equal(calls.some((call) => call.args[0] === "--set-default"), false);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test("cancel and unconfirmed import stop the owned wsl process", async () => {
  const dir = await tempDir();
  const aborted = new AbortController();
  const cancelSpawn = createController([
    (call) => {
      stopOnKill(call);
      aborted.abort();
    },
  ]);
  await assert.rejects(
    () => ensureYaomuWslImage({
      platform: "win32",
      userDataDir: dir,
      spawnImpl: cancelSpawn.spawnImpl,
      signal: aborted.signal,
      listTimeoutMs: 1000,
      stopGraceMs: 50,
      downloadImpl: async () => {
        throw new Error("downloaded");
      },
    }),
    (error) => error?.name === "AbortError",
  );
  assert.equal(cancelSpawn.calls[0].child.kills.length > 0, true);

  const stuck = createController([
    (call) => complete(call, { stdout: "Ubuntu\n" }),
    (call) => complete(call, { stdout: "* Ubuntu Stopped 2\n" }),
    (call) => {
      call.child.kill = (signal = "SIGTERM") => {
        call.child.kills.push(signal);
        return true;
      };
    },
  ]);
  await assert.rejects(
    () => ensureYaomuWslImage({
      platform: "win32",
      userDataDir: dir,
      spawnImpl: stuck.spawnImpl,
      listTimeoutMs: 30,
      importTimeoutMs: 30,
      stopGraceMs: 20,
      downloadImpl: async (destination) => {
        await writeFile(destination, "rootfs");
      },
    }),
    (error) => error?.name === "WslProbeCleanupError",
  );
  await rm(dir, { recursive: true, force: true });
});

test("fixed download writes https bytes, resumes, and rejects http redirects and short disk", async () => {
  const dir = await tempDir();
  const destination = path.join(dir, "yaomu-ubuntu.tar.gz");
  try {
    await downloadFixedWslImage({
      destination,
      freeBytesImpl: async () => 8 * 1024 * 1024 * 1024,
      requestImpl: async (request) => {
        assert.equal(request.url.protocol, "https:");
        const body = new PassThrough();
        queueMicrotask(() => body.end("rootfs"));
        return {
          statusCode: 200,
          headers: { "content-length": "6" },
          body,
          abort() {
            body.destroy();
          },
        };
      },
    });
    assert.equal(await readFile(destination, "utf8"), "rootfs");

    const partial = `${destination}.partial`;
    await writeFile(partial, "abc");
    await downloadFixedWslImage({
      destination,
      freeBytesImpl: async () => 8 * 1024 * 1024 * 1024,
      requestImpl: async (request) => {
        assert.equal(request.headers.Range, "bytes=3-");
        const body = new PassThrough();
        queueMicrotask(() => body.end("def"));
        return {
          statusCode: 206,
          headers: { "content-range": "bytes 3-5/6", "content-length": "3" },
          body,
          abort() {
            body.destroy();
          },
        };
      },
    });
    assert.equal(await readFile(destination, "utf8"), "abcdef");

    await assert.rejects(
      () => downloadFixedWslImage({
        destination: path.join(dir, "rejected.tar.gz"),
        requestImpl: async () => ({
          statusCode: 302,
          headers: { location: "http://evil.example/yaomu-ubuntu.tar.gz" },
          body: new PassThrough(),
          abort() {},
        }),
      }),
      (error) => error?.reason === "download",
    );

    await assert.rejects(
      () => downloadFixedWslImage({
        destination: path.join(dir, "disk.tar.gz"),
        freeBytesImpl: async () => 10,
        requestImpl: async () => {
          const body = new PassThrough();
          return {
            statusCode: 200,
            headers: { "content-length": "1000" },
            body,
            abort() {
              body.destroy();
            },
          };
        },
      }),
      (error) => error?.reason === "disk",
    );
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
