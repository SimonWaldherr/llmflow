package llmgateway

const adminUIHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>llmgateway control plane</title>
  <style>
    body { font-family: system-ui, sans-serif; margin: 20px; background:#0b1020; color:#e5e7eb; }
    h1,h2 { margin: 0 0 8px; }
    .grid { display:grid; grid-template-columns: repeat(auto-fit,minmax(340px,1fr)); gap:16px; }
    .card { border:1px solid #374151; border-radius:10px; padding:12px; background:#111827; }
    textarea,input,button { width:100%; box-sizing:border-box; padding:8px; border-radius:8px; border:1px solid #4b5563; background:#0f172a; color:#f9fafb; }
    button { cursor:pointer; margin-top:8px; background:#1d4ed8; border:none; }
    button.secondary { background:#374151; }
    pre { max-height:260px; overflow:auto; white-space:pre-wrap; word-break:break-word; background:#0b1224; padding:8px; border-radius:8px; }
    .muted { color:#9ca3af; font-size:12px; }
  </style>
</head>
<body>
  <h1>llmgateway</h1>
  <div class="muted">Control Plane (Config, Routing, Metrics, Decisions)</div>

  <div class="grid" style="margin-top:16px">
    <section class="card">
      <h2>Runtime Metrics</h2>
      <pre id="metrics">loading...</pre>
    </section>

    <section class="card">
      <h2>Backends Health</h2>
      <pre id="backends">loading...</pre>
    </section>

    <section class="card">
      <h2>Route Dry-Run</h2>
      <input id="dryPath" placeholder="/v1/chat/completions" value="/v1/chat/completions"/>
      <input id="dryModel" placeholder="model (optional)"/>
      <textarea id="dryPrompt" rows="5" placeholder="prompt text"></textarea>
      <button onclick="dryRun()">Test Routing</button>
      <pre id="dryOut"></pre>
    </section>

    <section class="card">
      <h2>Config Editor</h2>
      <textarea id="cfg" rows="12"></textarea>
      <button class="secondary" onclick="validateCfg()">Validate</button>
      <button onclick="applyCfg()">Apply (Hot Reload)</button>
      <pre id="cfgOut"></pre>
    </section>

    <section class="card">
      <h2>Recent Decisions</h2>
      <pre id="decisions">loading...</pre>
    </section>

    <section class="card">
      <h2>Snapshots / Audit</h2>
      <pre id="snapshots"></pre>
      <pre id="audit"></pre>
    </section>
  </div>

<script>
const token = localStorage.getItem('llmgateway_token') || '';
const headers = token ? {'Authorization':'Bearer '+token, 'Content-Type':'application/json'} : {'Content-Type':'application/json'};

async function jget(url){ const r = await fetch(url,{headers}); return await r.json(); }
async function jpost(url, body){ const r = await fetch(url,{method:'POST',headers,body:JSON.stringify(body)}); return await r.json(); }

async function refresh(){
  try {
    const [m,b,d,s,a] = await Promise.all([
      jget('/admin/metrics'),
      jget('/admin/backends'),
      jget('/admin/decisions?limit=20'),
      jget('/admin/snapshots'),
      jget('/admin/audit')
    ]);
    document.getElementById('metrics').textContent = JSON.stringify(m,null,2);
    document.getElementById('backends').textContent = JSON.stringify(b,null,2);
    document.getElementById('decisions').textContent = JSON.stringify(d,null,2);
    document.getElementById('snapshots').textContent = JSON.stringify(s,null,2);
    document.getElementById('audit').textContent = JSON.stringify(a,null,2);
  } catch (e) {
    document.getElementById('metrics').textContent = 'Error: '+e;
  }
}

async function loadConfig(){
  const cfg = await jget('/admin/config');
  document.getElementById('cfg').value = cfg.yaml || '';
}

async function validateCfg(){
  const yaml = document.getElementById('cfg').value;
  const res = await jpost('/admin/config/validate',{yaml});
  document.getElementById('cfgOut').textContent = JSON.stringify(res,null,2);
}

async function applyCfg(){
  const yaml = document.getElementById('cfg').value;
  const res = await jpost('/admin/config/apply',{yaml,source:'gui'});
  document.getElementById('cfgOut').textContent = JSON.stringify(res,null,2);
  await refresh();
}

async function dryRun(){
  const body = {
    path: document.getElementById('dryPath').value,
    model: document.getElementById('dryModel').value,
    prompt: document.getElementById('dryPrompt').value
  };
  const res = await jpost('/admin/route/dry-run', body);
  document.getElementById('dryOut').textContent = JSON.stringify(res,null,2);
}

refresh();
loadConfig();
setInterval(refresh, 5000);

const es = new EventSource('/admin/events' + (token ? ('?token='+encodeURIComponent(token)) : ''));
es.onmessage = () => refresh();
</script>
</body>
</html>`
