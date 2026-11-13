// 受控慢启动假 Core：先写 PID，很久之后才提供 /health/live。
// 用于验证启动取消确实终止本模块自己的 child，而不是只 abort 一次。
const fs = require("node:fs");
const http = require("node:http");

const pidFile = process.env.YAOMU_TEST_PID_FILE;
if (pidFile) fs.writeFileSync(pidFile, String(process.pid));

const port = Number(process.env.YAOMU_CORE_PORT);
setTimeout(() => {
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
}, 60_000);
