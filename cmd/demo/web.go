package main

// indexHTML is the single-page UI, served at "/". Vanilla JS, no external
// resources, so the demo works fully offline.
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Laozi — Core Features Demo</title>
<style>
  :root { --bg:#0f1420; --card:#171e2e; --line:#2a3450; --ink:#e6edf6; --muted:#94a3b8;
          --warn:#f59e0b; --ok:#34d399; --info:#60a5fa; --bad:#f87171; --accent:#7dd3fc; }
  * { box-sizing:border-box; }
  body { margin:0; background:var(--bg); color:var(--ink); font:15px/1.5 ui-sans-serif,system-ui,Segoe UI,Roboto,sans-serif; }
  header { padding:24px 28px; border-bottom:1px solid var(--line); }
  h1 { margin:0; font-size:20px; }
  header p { margin:6px 0 0; color:var(--muted); }
  main { max-width:1100px; margin:0 auto; padding:22px; display:grid; gap:18px; grid-template-columns:1fr 1fr; }
  .card { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:18px; }
  .card.wide { grid-column:1 / -1; }
  h2 { margin:0 0 4px; font-size:16px; }
  .sub { color:var(--muted); font-size:13px; margin:0 0 14px; }
  label { display:block; font-size:12px; color:var(--muted); margin:8px 0 3px; }
  input, textarea, button { font:inherit; }
  input[type=number], input[type=text], textarea {
    width:100%; background:#0d1320; border:1px solid var(--line); color:var(--ink);
    border-radius:8px; padding:8px 10px; }
  textarea { min-height:64px; resize:vertical; font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:13px; }
  .row { display:flex; gap:10px; flex-wrap:wrap; }
  .row > div { flex:1; min-width:120px; }
  button { background:var(--accent); color:#062235; border:0; border-radius:8px; padding:9px 14px;
           font-weight:600; cursor:pointer; margin-top:12px; }
  button.ghost { background:transparent; color:var(--accent); border:1px solid var(--line); }
  button.danger { background:transparent; color:var(--bad); border:1px solid var(--line); }
  .chk { display:inline-flex; align-items:center; gap:7px; margin-top:12px; color:var(--muted); font-size:13px; }
  .out { margin-top:14px; display:grid; gap:10px; }
  .insight { border:1px solid var(--line); border-left-width:4px; border-radius:8px; padding:10px 12px; background:#0d1320; }
  .insight.warning { border-left-color:var(--warn); }
  .insight.success { border-left-color:var(--ok); }
  .insight.info    { border-left-color:var(--info); }
  .badge { font-size:11px; text-transform:uppercase; letter-spacing:.05em; font-weight:700; padding:2px 7px; border-radius:999px; }
  .badge.warning { background:rgba(245,158,11,.15); color:var(--warn); }
  .badge.success { background:rgba(52,211,153,.15); color:var(--ok); }
  .badge.info    { background:rgba(96,165,250,.15); color:var(--info); }
  .ref { color:var(--muted); font-size:12px; margin-top:4px; word-break:break-all; }
  .viol { margin-top:8px; font-size:12px; color:var(--muted); border-top:1px dashed var(--line); padding-top:6px; }
  .viol b { color:var(--accent); }
  .sql { background:#08101c; border:1px solid var(--line); border-radius:8px; padding:10px; font-family:ui-monospace,monospace; font-size:12.5px; white-space:pre-wrap; word-break:break-word; }
  .err { color:var(--bad); font-size:13px; }
  .ok  { color:var(--ok); font-size:13px; }
  .pill { display:inline-block; font-size:11px; color:var(--muted); border:1px solid var(--line); border-radius:999px; padding:1px 8px; margin-left:6px; }
  .samples button { background:transparent; color:var(--muted); border:1px solid var(--line); font-weight:400; font-size:12px; padding:4px 8px; margin:6px 6px 0 0; }
  code { background:#08101c; padding:1px 5px; border-radius:5px; font-size:12.5px; }
</style>
</head>
<body>
<header>
  <h1>Laozi — Core Features Demo</h1>
  <p>Runs offline with a deliberately-misbehaving demo model, so you can watch the enforcement layer correct it. Swap in a real client + <code>LAOZI_API_KEY</code> for production.</p>
</header>
<main>

  <section class="card">
    <h2>1 &middot; Analyze + Enforcement</h2>
    <p class="sub">The demo model claims everything is "success", cites a fake URL, and invents the number 999. Watch severity, citation, and number get enforced. Toggle strict mode to replace invented numbers in prose.</p>
    <div class="row">
      <div><label>steps</label><input id="m_steps" type="number" value="5200"></div>
      <div><label>fasting_glucose</label><input id="m_glucose" type="number" value="108"></div>
    </div>
    <div class="row">
      <div><label>systolic_bp</label><input id="m_sys" type="number" value="128"></div>
      <div><label>diastolic_bp</label><input id="m_dia" type="number" value="82"></div>
    </div>
    <label class="chk"><input id="strict" type="checkbox"> strict mode (replace invented numbers)</label>
    <button onclick="analyze()">Analyze</button>
    <div id="analyzeOut" class="out"></div>
  </section>

  <section class="card">
    <h2>2 &middot; Adaptive Query Classification</h2>
    <p class="sub">Free-form input is classified into a domain (LLM &rarr; keyword &rarr; default), then analysis is limited to that domain's categories.</p>
    <label>Ask or describe something</label>
    <input id="msg" type="text" value="my blood pressure has been high lately">
    <div class="samples">
      <button onclick="setMsg('how many steps should I walk')">steps</button>
      <button onclick="setMsg('my fasting glucose reading')">glucose</button>
      <button onclick="setMsg('systolic and diastolic readings')">bp</button>
      <button onclick="setMsg('what should I cook for dinner')">unrelated</button>
    </div>
    <button onclick="classify()">Classify &amp; analyze</button>
    <div id="classifyOut" class="out"></div>
  </section>

  <section class="card">
    <h2>3 &middot; DSL Test Parser</h2>
    <p class="sub">Validate a Laozi expression and see the compiled SQL, or the syntax/semantic errors.</p>
    <textarea id="expr">SUM(amount) WHERE(type = 'income') OVER(30 days)</textarea>
    <div class="samples">
      <button onclick="setExpr('COUNT(*) WHERE(type = \'CREDIT\') PERIOD(YTD)')">count YTD</button>
      <button onclick="setExpr('GINI(amount GROUP_BY(payee))')">gini</button>
      <button onclick="setExpr('CHANGE(revenue, 3 months)')">change</button>
      <button onclick="setExpr('SUM(amount')">broken</button>
      <button onclick="setExpr('GINI(amount)')">missing group_by</button>
    </div>
    <button onclick="checkDSL()">Check</button>
    <div id="dslOut" class="out"></div>
  </section>

  <section class="card">
    <h2>4 &middot; Human Draft &amp; Approval</h2>
    <p class="sub">Propose a category with a DSL expression. It is validated and held as a draft (with compiled SQL) until a human approves it — never auto-promoted.</p>
    <div class="row">
      <div><label>category id</label><input id="d_name" type="text" value="vitamin_d"></div>
      <div><label>metric</label><input id="d_metric" type="text" value="vitamin_d_level"></div>
    </div>
    <label>expression (DSL)</label>
    <input id="d_expr" type="text" value="AVG(vitamin_d) OVER(90 days)">
    <div class="row">
      <div><label>min</label><input id="d_min" type="number" value="30"></div>
      <div><label>max</label><input id="d_max" type="number" value="100"></div>
      <div><label>unit</label><input id="d_unit" type="text" value="ng/mL"></div>
    </div>
    <button onclick="propose()">Propose</button>
    <button class="ghost" onclick="loadDrafts()">Refresh pending</button>
    <div id="draftOut" class="out"></div>
  </section>

</main>
<script>
function esc(s){ return String(s).replace(/[&<>"]/g, function(c){ return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c]; }); }
function post(url, body){ return fetch(url,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)}).then(function(r){return r.json();}); }
function num(id){ return parseFloat(document.getElementById(id).value)||0; }
function val(id){ return document.getElementById(id).value; }
function setMsg(s){ document.getElementById("msg").value = s; }
function setExpr(s){ document.getElementById("expr").value = s; }

function renderInsight(ins){
  var sev = ins.severity || "info";
  var h = "<div class='insight "+sev+"'>";
  h += "<span class='badge "+sev+"'>"+esc(sev)+"</span> <b>"+esc(ins.category_id||"")+"</b>";
  h += "<div style='margin-top:6px'>"+esc(ins.text||"")+"</div>";
  if(ins.reference){ h += "<div class='ref'>ref: "+esc(ins.reference)+"</div>"; }
  if(ins.violations && ins.violations.length){
    h += "<div class='viol'><b>enforced ("+ins.violations.length+"):</b><br>";
    ins.violations.forEach(function(v){
      h += "&bull; "+esc(v.kind)+": "+esc(v.llm_value||"(n/a)");
      if(v.enforced){ h += " &rarr; "+esc(v.enforced); }
      h += "<br>";
    });
    h += "</div>";
  } else {
    h += "<div class='viol'>no corrections needed</div>";
  }
  h += "</div>";
  return h;
}

function analyze(){
  var metrics = { steps:num("m_steps"), fasting_glucose:num("m_glucose"), systolic_bp:num("m_sys"), diastolic_bp:num("m_dia") };
  post("/api/analyze", {metrics:metrics, strict:document.getElementById("strict").checked}).then(function(r){
    var el = document.getElementById("analyzeOut");
    if(r.error){ el.innerHTML = "<div class='err'>"+esc(r.error)+"</div>"; return; }
    el.innerHTML = (r.insights||[]).map(renderInsight).join("");
  });
}

function classify(){
  post("/api/classify", {message:val("msg")}).then(function(r){
    var el = document.getElementById("classifyOut");
    if(r.error){ el.innerHTML = "<div class='err'>"+esc(r.error)+"</div>"; return; }
    var c = r.classification;
    var h = "<div>domain <b>"+esc(c.domain)+"</b><span class='pill'>layer "+c.layer+" &middot; "+esc(c.reason)+"</span></div>";
    h += "<div class='sub' style='margin:6px 0'>limited to categories: "+esc((r.categories||[]).join(", ")||"(none)")+"</div>";
    h += (r.insights||[]).map(renderInsight).join("");
    el.innerHTML = h;
  });
}

function checkDSL(){
  post("/api/dsl", {expr:val("expr")}).then(function(r){
    var el = document.getElementById("dslOut");
    if(r.valid){ el.innerHTML = "<div class='ok'>valid &check;</div><div class='sql'>"+esc(r.sql)+"</div>"; }
    else { el.innerHTML = "<div class='err'>"+(r.errors||[]).map(esc).join("<br>")+"</div>"; }
  });
}

function renderDraft(d){
  var h = "<div class='insight'>";
  h += "<b>"+esc(d.id)+"</b> <span class='pill'>"+esc(d.status)+"</span> &mdash; "+esc(d.category?d.category.id:"");
  (d.expressions||[]).forEach(function(e){
    h += "<div style='margin-top:6px'>"+esc(e.metric)+": <code>"+esc(e.expression)+"</code></div>";
    if(e.valid){ h += "<div class='sql'>"+esc(e.sql)+"</div>"; }
    else { h += "<div class='err'>"+(e.errors||[]).map(esc).join("<br>")+"</div>"; }
  });
  if(d.status === "draft"){
    h += "<button onclick=\"approve('"+esc(d.id)+"')\">Approve</button> ";
    h += "<button class='danger' onclick=\"reject('"+esc(d.id)+"')\">Reject</button>";
  }
  h += "</div>";
  return h;
}

function propose(){
  post("/api/propose", {Name:val("d_name"), Metric:val("d_metric"), Expression:val("d_expr"),
    Min:num("d_min"), Max:num("d_max"), Unit:val("d_unit"), Source:"Demo", SourceURL:"https://demo.example/guide"}).then(function(r){
    var el = document.getElementById("draftOut");
    if(r.error){ el.innerHTML = "<div class='err'>"+esc(r.error)+"</div>"; return; }
    el.innerHTML = "<div class='ok'>proposed &mdash; pending approval</div>" + renderDraft(r.draft);
  });
}
function loadDrafts(){
  fetch("/api/drafts").then(function(r){return r.json();}).then(function(r){
    var el = document.getElementById("draftOut");
    var p = r.pending||[];
    el.innerHTML = p.length ? p.map(renderDraft).join("") : "<div class='sub'>no pending drafts</div>";
  });
}
function approve(id){ post("/api/approve",{ID:id}).then(function(r){
  document.getElementById("draftOut").innerHTML = r.error ? "<div class='err'>"+esc(r.error)+"</div>" : "<div class='ok'>approved &amp; registered for analysis &check;</div>";
}); }
function reject(id){ post("/api/reject",{ID:id, Reason:"rejected from demo"}).then(function(){ loadDrafts(); }); }

analyze();
</script>
</body>
</html>`
