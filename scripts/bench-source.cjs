// Minimal in-memory HTTP source used for tunnel throughput measurement.
// Serving from RAM keeps disk I/O out of the measurement. Deployed temporarily
// to the VPS (systemd-run) and removed after the bench; see docs/部署记录.md.
const http = require('node:http');

const size = Number(process.env.BENCH_SIZE || 32 * 1024 * 1024);
const port = Number(process.env.BENCH_PORT || 18080);
const host = process.env.BENCH_HOST || '127.0.0.1';
const body = Buffer.alloc(size, 0x5a);

http
  .createServer((_req, res) => {
    res.writeHead(200, {
      'content-length': body.length,
      'content-type': 'application/octet-stream',
    });
    res.end(body);
  })
  .listen(port, host, () => {
    console.log(`bench source listening on ${host}:${port} (${body.length} bytes)`);
  });
