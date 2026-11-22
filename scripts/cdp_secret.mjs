// 通过 CDP 从运行中的 OrcaTerm 读取系统密钥存储里的 refresh token。
// 用法：node cdp_secret.mjs [port]
// 输出：{"ok":true,"refresh_token":"..."} 或 {"ok":false,"error":"..."}
const port = process.argv[2] || process.env.CDP_PORT || 9333;

async function main() {
  let list;
  try {
    list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  } catch (e) {
    return { ok: false, error: `无法连接 CDP ${port}: ${e.message}` };
  }
  const pages = list.filter((t) => t.type === 'page' && !String(t.url).includes('dynamic-island'));
  const page = pages.find((p) => String(p.url).startsWith('http://tauri.localhost')) || pages[0];
  if (!page) return { ok: false, error: '没有找到 OrcaTerm 页面' };

  return await new Promise((resolve) => {
    const ws = new WebSocket(page.webSocketDebuggerUrl);
    let id = 0;
    const timer = setTimeout(() => {
      try { ws.close(); } catch {}
      resolve({ ok: false, error: 'CDP 超时' });
    }, 15000);

    ws.addEventListener('open', () => {
      const expr = "window.__TAURI_INTERNALS__.invoke('secret_get',{key:'oauth_refresh_token_v2'})"
        + ".then(v=>JSON.stringify({ok:true,refresh_token:v}))"
        + ".catch(e=>JSON.stringify({ok:false,error:String(e)}))";
      ws.send(JSON.stringify({
        id: ++id,
        method: 'Runtime.evaluate',
        params: { expression: expr, awaitPromise: true, returnByValue: true },
      }));
    });

    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data);
      if (!msg.id) return;
      clearTimeout(timer);
      try { ws.close(); } catch {}
      const val = msg.result?.result?.value;
      if (val) {
        try { resolve(JSON.parse(val)); } catch { resolve({ ok: false, error: '返回值不是 JSON: ' + val }); }
      } else {
        resolve({ ok: false, error: JSON.stringify(msg.error || msg.result || {}) });
      }
    });

    ws.addEventListener('error', (e) => {
      clearTimeout(timer);
      resolve({ ok: false, error: 'WebSocket 错误: ' + (e.message || 'unknown') });
    });
  });
}

const out = await main();
console.log(JSON.stringify(out));
process.exit(out.ok ? 0 : 1);
