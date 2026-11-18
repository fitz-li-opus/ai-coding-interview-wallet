#!/usr/bin/env python3
"""Render godog's cucumber JSON as one self-contained HTML file.

    BDD_FORMAT='pretty,cucumber:reports/run.json' go test ./...
    python3 tools/cucumber_html.py reports/run.json reports/run.html --tags @minimum

No network, no CDN, no npm: the output is one file that opens anywhere, which
is the only property a report needs that a terminal does not already have.
"""
import argparse
import datetime
import html
import json
import os

STATUS_ORDER = ["failed", "undefined", "pending", "skipped", "passed"]


def ns(d):
    """Nanoseconds to something a person reads."""
    if d is None:
        return ""
    if d < 1_000_000:
        return f"{d/1000:.0f}µs"
    if d < 1_000_000_000:
        return f"{d/1_000_000:.0f}ms"
    return f"{d/1_000_000_000:.2f}s"


def worst(statuses):
    for s in STATUS_ORDER:
        if s in statuses:
            return s
    return "passed"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("json_path", nargs="+",
                    help="one or more cucumber JSON files; several are merged, "
                         "which is how per-tag runs add up to the whole suite "
                         "without re-running it")
    ap.add_argument("html_path")
    ap.add_argument("--title", default="Credit Wallet · BDD")
    ap.add_argument("--tags", default="")
    ap.add_argument("--wall", default="", help="wall-clock time of the run, e.g. 40.3s")
    ap.add_argument("--concurrency", default="")
    args = ap.parse_args()

    # Merging, not concatenating: tags map one-to-one onto feature files here,
    # so the union of per-tag runs is the whole suite with every scenario
    # present exactly once. A scenario that appears twice (a scenario carrying
    # two tags, such as @minimum) is kept once, keyed by file and line.
    features = []
    by_uri = {}
    seen = set()
    for path in args.json_path:
        with open(path, encoding="utf-8") as fh:
            for f in json.load(fh):
                uri = f.get("uri", "")
                tgt = by_uri.get(uri)
                if tgt is None:
                    tgt = dict(f, elements=[])
                    by_uri[uri] = tgt
                    features.append(tgt)
                for el in f.get("elements", []):
                    key = (uri, el.get("line"), el.get("id"))
                    if key in seen:
                        continue
                    seen.add(key)
                    tgt["elements"].append(el)
    for f in features:
        f["elements"].sort(key=lambda el: el.get("line") or 0)
    features.sort(key=lambda f: f.get("uri", ""))

    scen_total = scen_pass = step_total = 0
    step_counts = {}
    step_time = 0
    slowest = []
    lifecycle = 0
    lifecycle_rows = []
    for f in features:
        for el in f.get("elements", []):
            scen_total += 1
            sts = []
            steps = el.get("steps", [])
            for idx, st in enumerate(steps):
                res = st.get("result", {}) or {}
                s = res.get("status", "undefined")
                sts.append(s)
                step_counts[s] = step_counts.get(s, 0) + 1
                step_total += 1
                d = res.get("duration") or 0
                step_time += d
                label = (st.get("keyword", "") + st.get("name", "")).strip()
                # godog's cucumber formatter bills Before/After hook time to the
                # adjacent step, and this suite's hooks start and remove three
                # containers. So a scenario's FIRST step carries stack startup
                # and its LAST step carries teardown, whatever those steps are.
                # Ranking them against real assertions would tell the reader
                # that a 0-row SQL query costs ten seconds.
                if idx == 0 or idx == len(steps) - 1:
                    lifecycle += d
                    lifecycle_rows.append((d, el.get("name", ""), label,
                                           "startup" if idx == 0 else "teardown"))
                else:
                    slowest.append((d, f.get("name", ""), el.get("name", ""), label))
            if worst(sts) == "passed":
                scen_pass += 1
    slowest.sort(reverse=True)
    lifecycle_rows.sort(reverse=True)
    verdict = "PASS" if scen_pass == scen_total and step_counts.get("failed", 0) == 0 else "FAIL"

    def tagchips(tags):
        return "".join(
            f'<span class="tag">{html.escape(t["name"])}</span>' for t in (tags or [])
        )

    rows = []
    for f in features:
        els = f.get("elements", [])
        f_sts = [((st.get("result") or {}).get("status", "undefined"))
                 for el in els for st in el.get("steps", [])]
        f_status = worst(f_sts)
        f_time = sum(((st.get("result") or {}).get("duration") or 0)
                     for el in els for st in el.get("steps", []))
        scen_html = []
        for el in els:
            sts = [((st.get("result") or {}).get("status", "undefined")) for st in el.get("steps", [])]
            e_status = worst(sts)
            e_time = sum(((st.get("result") or {}).get("duration") or 0) for st in el.get("steps", []))
            steps_html = []
            for st in el.get("steps", []):
                res = st.get("result", {}) or {}
                s = res.get("status", "undefined")
                loc = (st.get("match", {}) or {}).get("location", "")
                err = res.get("error_message", "")
                doc = (st.get("docString", {}) or {}).get("value", "")
                extra = ""
                if doc:
                    extra += f'<pre class="doc">{html.escape(doc)}</pre>'
                if err:
                    extra += f'<pre class="err">{html.escape(err)}</pre>'
                steps_html.append(
                    f'<li class="step {s}">'
                    f'<span class="dot"></span>'
                    f'<span class="txt"><b>{html.escape(st.get("keyword", "").strip())}</b> '
                    f'{html.escape(st.get("name", ""))}</span>'
                    f'<span class="loc">{html.escape(loc)}</span>'
                    f'<span class="dur">{ns(res.get("duration"))}</span>'
                    f'{extra}</li>'
                )
            scen_html.append(
                f'<details class="scenario {e_status}" open>'
                f'<summary><span class="badge {e_status}">{e_status}</span>'
                f'<span class="sname">{html.escape(el.get("name", ""))}</span>'
                f'{tagchips(el.get("tags"))}'
                f'<span class="dur">{ns(e_time)}</span></summary>'
                f'<ol class="steps">{"".join(steps_html)}</ol>'
                f'</details>'
            )
        rows.append(
            f'<section class="feature">'
            f'<h2><span class="badge {f_status}">{f_status}</span>'
            f'{html.escape(f.get("name", ""))}'
            f'<span class="dur">{ns(f_time)}</span></h2>'
            f'<p class="uri">{html.escape(os.path.basename(f.get("uri", "")))}'
            f' · {len(els)} scenario(s)</p>'
            f'{"".join(scen_html)}'
            f'</section>'
        )

    slow_rows = "".join(
        f"<tr><td>{ns(d)}</td><td>{html.escape(step)}</td>"
        f"<td class='muted'>{html.escape(sc)}</td></tr>"
        for d, _, sc, step in slowest[:10]
    )
    life_rows = "".join(
        f"<tr><td>{ns(d)}</td><td><span class='badge'>{ph}</span></td>"
        f"<td>{html.escape(step)}</td><td class='muted'>{html.escape(sc)}</td></tr>"
        for d, sc, step, ph in lifecycle_rows[:10]
    )
    chips = " ".join(
        f'<span class="count {s}">{n} {s}</span>'
        for s in STATUS_ORDER if (n := step_counts.get(s))
    )
    meta = [f"tags: <code>{html.escape(args.tags)}</code>" if args.tags else "tags: <code>(all)</code>"]
    if args.concurrency:
        meta.append(f"concurrency: <code>{html.escape(args.concurrency)}</code>")
    if args.wall:
        meta.append(f"wall clock: <code>{html.escape(args.wall)}</code>")
    meta.append("stack per scenario: <code>postgres + wallet + payment-provider</code>")
    stamp = datetime.datetime.now().astimezone().strftime("%Y-%m-%d %H:%M %Z")

    doc = f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(args.title)}</title>
<style>
:root {{
  --bg:#fbfbfa; --panel:#fff; --ink:#1a1a18; --muted:#6b6b66; --line:#e4e4df;
  --pass:#1d7a4c; --fail:#b4342a; --skip:#8a8a84; --warn:#9a6b10;
  --passbg:#e8f4ec; --failbg:#fbe9e7; --skipbg:#f0f0ed; --warnbg:#fbf1dd;
}}
@media (prefers-color-scheme: dark) {{ :root:not([data-theme="light"]) {{
  --bg:#141413; --panel:#1c1c1a; --ink:#f0efea; --muted:#9a9a93; --line:#2e2e2b;
  --pass:#6fcf97; --fail:#ff8a7a; --skip:#9a9a93; --warn:#e0b450;
  --passbg:#18291f; --failbg:#2b1917; --skipbg:#222220; --warnbg:#2a2214;
}} }}
:root[data-theme="dark"] {{
  --bg:#141413; --panel:#1c1c1a; --ink:#f0efea; --muted:#9a9a93; --line:#2e2e2b;
  --pass:#6fcf97; --fail:#ff8a7a; --skip:#9a9a93; --warn:#e0b450;
  --passbg:#18291f; --failbg:#2b1917; --skipbg:#222220; --warnbg:#2a2214;
}}
* {{ box-sizing:border-box; }}
body {{ margin:0; background:var(--bg); color:var(--ink);
  font:15px/1.55 ui-sans-serif,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",sans-serif; }}
.wrap {{ max-width:1000px; margin:0 auto; padding:32px 16px 64px; }}
header h1 {{ font-size:1.5rem; margin:0 0 4px; letter-spacing:-.01em; }}
.sub {{ color:var(--muted); font-size:.875rem; margin:0 0 20px; }}
.sub code {{ background:var(--skipbg); padding:1px 5px; border-radius:4px; font-size:.8rem; }}
.verdict {{ display:inline-block; font-weight:700; letter-spacing:.08em; font-size:.75rem;
  padding:4px 10px; border-radius:999px; margin-bottom:10px; }}
.verdict.PASS {{ background:var(--passbg); color:var(--pass); }}
.verdict.FAIL {{ background:var(--failbg); color:var(--fail); }}
.tiles {{ display:grid; grid-template-columns:repeat(auto-fit,minmax(150px,1fr)); gap:10px; margin:18px 0 26px; }}
.tile {{ background:var(--panel); border:1px solid var(--line); border-radius:10px; padding:14px 16px; }}
.tile .n {{ font-size:1.6rem; font-weight:650; font-variant-numeric:tabular-nums; }}
.tile .l {{ color:var(--muted); font-size:.75rem; text-transform:uppercase; letter-spacing:.06em; }}
.count {{ font-size:.75rem; padding:2px 8px; border-radius:999px; background:var(--skipbg); color:var(--muted); }}
.count.passed {{ background:var(--passbg); color:var(--pass); }}
.count.failed {{ background:var(--failbg); color:var(--fail); }}
.count.undefined,.count.pending {{ background:var(--warnbg); color:var(--warn); }}
.feature {{ background:var(--panel); border:1px solid var(--line); border-radius:12px;
  padding:16px 18px; margin-bottom:16px; }}
.feature h2 {{ font-size:1.05rem; margin:0 0 2px; display:flex; align-items:center; gap:10px; }}
.uri {{ color:var(--muted); font-size:.78rem; margin:0 0 12px; font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }}
.badge {{ font-size:.68rem; font-weight:700; letter-spacing:.06em; text-transform:uppercase;
  padding:3px 7px; border-radius:5px; background:var(--skipbg); color:var(--skip); }}
.badge.passed {{ background:var(--passbg); color:var(--pass); }}
.badge.failed {{ background:var(--failbg); color:var(--fail); }}
.badge.undefined,.badge.pending {{ background:var(--warnbg); color:var(--warn); }}
.dur {{ margin-left:auto; color:var(--muted); font-size:.78rem; font-variant-numeric:tabular-nums; }}
.scenario {{ border-top:1px solid var(--line); padding:10px 0 2px; }}
.scenario summary {{ cursor:pointer; display:flex; align-items:center; gap:9px; flex-wrap:wrap; }}
.scenario summary::-webkit-details-marker {{ display:none; }}
.sname {{ font-weight:550; }}
.tag {{ font-size:.7rem; color:var(--muted); border:1px solid var(--line);
  padding:1px 6px; border-radius:999px; font-family:ui-monospace,Menlo,monospace; }}
ol.steps {{ list-style:none; margin:10px 0 6px; padding:0 0 0 2px; }}
.step {{ display:flex; align-items:baseline; gap:9px; padding:3px 0; font-size:.86rem; flex-wrap:wrap; }}
.step .dot {{ width:7px; height:7px; border-radius:50%; background:var(--skip); flex:none; }}
.step.passed .dot {{ background:var(--pass); }}
.step.failed .dot {{ background:var(--fail); }}
.step.undefined .dot,.step.pending .dot {{ background:var(--warn); }}
.step .txt {{ flex:1 1 320px; }}
.step .loc {{ color:var(--muted); font-size:.72rem; font-family:ui-monospace,Menlo,monospace; }}
pre.doc,pre.err {{ flex-basis:100%; margin:6px 0 6px 16px; padding:9px 11px; border-radius:7px;
  background:var(--skipbg); font-size:.76rem; overflow-x:auto; }}
pre.err {{ background:var(--failbg); color:var(--fail); }}
table {{ width:100%; border-collapse:collapse; font-size:.84rem; }}
th,td {{ text-align:left; padding:6px 8px; border-bottom:1px solid var(--line); }}
th {{ color:var(--muted); font-size:.72rem; text-transform:uppercase; letter-spacing:.06em; font-weight:600; }}
td:first-child {{ font-variant-numeric:tabular-nums; white-space:nowrap; }}
.muted {{ color:var(--muted); }}
footer {{ margin-top:28px; color:var(--muted); font-size:.8rem; }}
footer code {{ background:var(--skipbg); padding:1px 5px; border-radius:4px; }}
</style></head><body><div class="wrap">
<header>
  <div class="verdict {verdict}">{verdict}</div>
  <h1>{html.escape(args.title)}</h1>
  <p class="sub">{" · ".join(meta)}<br>generated {html.escape(stamp)}</p>
</header>
<div class="tiles">
  <div class="tile"><div class="n">{scen_pass}/{scen_total}</div><div class="l">scenarios passed</div></div>
  <div class="tile"><div class="n">{step_counts.get("passed", 0)}/{step_total}</div><div class="l">steps passed</div></div>
  <div class="tile"><div class="n">{len(features)}</div><div class="l">feature files</div></div>
  <div class="tile"><div class="n">{ns(step_time - lifecycle)}</div><div class="l">time in assertions</div></div>
  <div class="tile"><div class="n">{ns(lifecycle)}</div><div class="l">container lifecycle</div></div>
</div>
<p>{chips}</p>
{"".join(rows)}
<section class="feature">
  <h2>Slowest assertions</h2>
  <p class="uri">each scenario's first and last step are excluded — see below for why</p>
  <table><thead><tr><th>time</th><th>step</th><th>scenario</th></tr></thead>
  <tbody>{slow_rows}</tbody></table>
</section>
<section class="feature">
  <h2>Container lifecycle</h2>
  <p class="uri">
    godog bills <code>Before</code>/<code>After</code> hook time to the adjacent step, and this
    suite's hooks start and remove three containers per scenario. A scenario's first step therefore
    carries stack startup and its last step carries teardown — whatever those steps happen to be.
    Counting them as assertion cost would claim a 0-row SQL query takes ten seconds.
  </p>
  <table><thead><tr><th>time</th><th>phase</th><th>billed to</th><th>scenario</th></tr></thead>
  <tbody>{life_rows}</tbody></table>
</section>
<footer>
  Reproduce: <code>cd wallet/test/bdd &amp;&amp; BDD_TAGS={html.escape(args.tags or "")} \
BDD_FORMAT='pretty,cucumber:../../../reports/minimum.json' go test ./...</code><br>
  Every scenario owns its own postgres, wallet and payment-provider containers; the provider is
  <code>services/payment-provider/provider.py</code> unmodified, configured only through its environment.
</footer>
</div></body></html>
"""
    with open(args.html_path, "w", encoding="utf-8") as fh:
        fh.write(doc)
    print(f"{args.html_path}: {verdict} — {scen_pass}/{scen_total} scenarios, "
          f"{step_counts.get('passed', 0)}/{step_total} steps")


if __name__ == "__main__":
    main()
