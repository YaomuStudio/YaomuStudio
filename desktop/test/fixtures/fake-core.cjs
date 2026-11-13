// 受控假 Core：记录 startCore 实际传给子进程的环境，并提供 /health/live。
// 只回写断言所需的少量字段，不落盘完整环境变量。
const fs = require("node:fs");
const http = require("node:http");

const capturePath = process.env.YAOMU_TEST_CAPTURE_ENV;
if (!capturePath) {
  console.error("YAOMU_TEST_CAPTURE_ENV is required");
  process.exit(2);
}

fs.writeFileSync(
  capturePath,
  JSON.stringify({
    pid: process.pid,
    mockSlots: process.env.YAOMU_MOCK_MODEL_SLOTS ?? null,
    // Windows 环境变量名大小写不敏感：按不区分大小写的方式记录实际收到的键。
    mockSlotKeys: Object.keys(process.env).filter(
      (key) => key.toUpperCase() === "YAOMU_MOCK_MODEL_SLOTS",
    ),
    wslCommand: process.env.YAOMU_WSL_RUNNER_COMMAND ?? null,
    wslArgs: process.env.YAOMU_WSL_RUNNER_ARGS ?? null,
    dataDir: process.env.YAOMU_DATA_DIR ?? null,
    port: process.env.YAOMU_CORE_PORT ?? null,
    token: process.env.YAOMU_INSTANCE_TOKEN ?? null,
    parentOnly: process.env.YAOMU_TEST_PARENT_ONLY ?? null,
  }),
);

const port = Number(process.env.YAOMU_CORE_PORT);
const server = http.createServer((request, response) => {
  if (request.url === "/health/live") {
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ status: "ok" }));
    return;
  }
  response.writeHead(404);
  response.end();
});
server.listen(port, "127.0.0.1");
