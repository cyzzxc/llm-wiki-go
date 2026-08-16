package web

// tplSource is the full template set: layout blocks ("head"/"foot") plus
// one template per route. html/template autoescape applies everywhere;
// the only template.HTML value is the goldmark-rendered page body and the
// pre-escaped search excerpts.
const tplSource = `
{{define "head"}}<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · {{.Wiki}}</title>
<style>
:root{--bg:#faf7f2;--fg:#2d2a26;--accent:#b4632c;--muted:#8a8178;--pill-bg:#f0e9df;--code-bg:#f3efe8;--border:#e5ddd0}
@media (prefers-color-scheme:dark){:root{--bg:#211e1b;--fg:#e8e2d9;--accent:#d99a5b;--muted:#9a9188;--pill-bg:#2d2925;--code-bg:#2a2723;--border:#3a352f}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.8 "Songti SC","Noto Serif CJK SC",Georgia,serif}
a{color:var(--accent);text-decoration:none}
a:hover{text-decoration:underline}
main{max-width:42rem;margin:0 auto;padding:0 1rem 3rem}
.top{position:sticky;top:0;z-index:9;background:var(--bg);border-bottom:1px solid var(--border)}
.bar{max-width:42rem;margin:0 auto;padding:.5rem 1rem;display:flex;align-items:center;gap:.9rem;flex-wrap:wrap;font-family:"PingFang SC","Noto Sans CJK SC",system-ui,sans-serif;font-size:.9rem}
.site{font-weight:700;color:var(--fg)}
.bar input{margin-left:auto;border:1px solid var(--border);border-radius:6px;background:var(--bg);color:var(--fg);padding:.25rem .6rem;width:11rem;font-size:.85rem;font-family:inherit}
.bar input:focus{outline:1px solid var(--accent)}
h1,h2,h3{font-family:"PingFang SC","Noto Sans CJK SC",system-ui,sans-serif;line-height:1.35}
h1{font-size:1.7rem;margin:1.4rem 0 .6rem}
h2{font-size:1.2rem;margin:2rem 0 .5rem;border-bottom:1px solid var(--border);padding-bottom:.3rem}
.hero p{color:var(--muted);margin:.2rem 0 1rem}
.pills{display:flex;flex-wrap:wrap;gap:.5rem;margin:1rem 0 1.5rem}
.pill{background:var(--pill-bg);border-radius:999px;padding:.15rem .75rem;font-size:.85rem;font-family:"PingFang SC","Noto Sans CJK SC",system-ui,sans-serif}
.pill b{opacity:.65;font-weight:600}
.rows{list-style:none;margin:0;padding:0}
.rows li{display:flex;align-items:baseline;gap:.75rem;padding:.45rem 0;border-bottom:1px dashed var(--border);flex-wrap:wrap}
.rows .t{flex:1;min-width:12rem}
.rows .date{color:var(--muted);font-size:.85rem;white-space:nowrap}
.rows .excerpt{flex-basis:100%;margin:.15rem 0 0;color:var(--muted);font-size:.92rem}
.tag-label{font-size:.75rem;color:var(--muted);border:1px solid var(--border);border-radius:4px;padding:0 .35rem;white-space:nowrap}
.badges{display:flex;gap:.5rem;align-items:center;margin:.5rem 0;flex-wrap:wrap}
.badge{font-size:.78rem;border-radius:4px;padding:.05rem .5rem;font-family:"PingFang SC","Noto Sans CJK SC",system-ui,sans-serif}
.badge.type{border:1px solid var(--accent);color:var(--accent)}
.badge.status{border:1px solid var(--border);color:var(--muted)}
.conf-dot{display:inline-block;width:.7rem;height:.7rem;border-radius:50%;background:var(--accent)}
.tags{margin:.2rem 0}
.tag{color:var(--muted);font-size:.85rem;margin-right:.4rem}
.foot{max-width:42rem;margin:0 auto;padding:1rem;border-top:1px solid var(--border);color:var(--muted);font-size:.85rem;display:flex;justify-content:space-between;gap:1rem;font-family:"PingFang SC","Noto Sans CJK SC",system-ui,sans-serif}
.muted{color:var(--muted)}
.two{display:grid;grid-template-columns:1fr 1fr;gap:2rem}
@media (max-width:640px){.two{grid-template-columns:1fr}}
.md pre{background:var(--code-bg);padding:.8rem 1rem;border-radius:6px;overflow-x:auto;font:.875rem/1.6 ui-monospace,Menlo,monospace}
.md code{font-family:ui-monospace,Menlo,monospace;font-size:.9em;background:var(--code-bg);padding:.05em .3em;border-radius:3px}
.md pre code{background:none;padding:0}
.md blockquote{border-left:3px solid var(--border);margin:1rem 0;padding:.2rem 1rem;color:var(--muted)}
.md table{border-collapse:collapse;width:100%;font-size:.9rem}
.md th,.md td{border:1px solid var(--border);padding:.3rem .6rem;text-align:left}
.md th{background:var(--pill-bg)}
.md img{max-width:100%}
.md hr{border:none;border-top:1px solid var(--border)}
mark{background:var(--accent);color:var(--bg);border-radius:2px;padding:0 .1em}
.score{color:var(--muted);font-size:.78rem}
.searchpage{display:flex;gap:.5rem;margin:1rem 0}
.searchpage input{flex:1;border:1px solid var(--border);border-radius:6px;background:var(--bg);color:var(--fg);padding:.4rem .7rem;font:inherit}
.searchpage select,.searchpage button{border:1px solid var(--border);border-radius:6px;background:var(--bg);color:var(--fg);padding:.4rem .7rem;font:inherit}
.searchpage button{color:var(--accent);border-color:var(--accent)}
.graph{background:var(--code-bg);padding:1rem;border-radius:6px;overflow-x:auto;font:.8rem/1.5 ui-monospace,Menlo,monospace;white-space:pre-wrap}
.backlinks{margin-top:2.5rem}
.note{color:var(--muted);font-size:.9rem}
</style>
</head>
<body>
<header class="top">
<nav class="bar">
<a class="site" href="/">{{.Wiki}}</a>
{{range .NavTypes}}<a href="/list/{{.Name}}">{{.Label}}</a>{{end}}
<a href="/graph">Graph</a>
<form action="/search" method="get">
<input type="search" name="q" id="q" placeholder="搜索 (/)" value="{{.Query}}">
</form>
</nav>
</header>
<main>
{{end}}

{{define "foot"}}
</main>
<footer class="foot">
<span>{{.Wiki}} · powered by llm-wiki</span>
<span><a href="/feed.xml">RSS</a></span>
</footer>
<script>document.addEventListener('keydown',function(e){if(e.key==='/'&&!/INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)){e.preventDefault();var q=document.getElementById('q');if(q)q.focus()}})</script>
</body>
</html>{{end}}

{{define "home"}}
{{template "head" .}}
<section class="hero">
<h1>{{.Wiki}}</h1>
<p>{{.Stats.Pages}} 页 · 平均连接 {{printf "%.1f" .Stats.AvgConnections}} · 图密度 {{printf "%.3f" .Stats.GraphDensity}}</p>
</section>
<nav class="pills">
{{range .NavTypes}}<a class="pill" href="/list/{{.Name}}">{{.Label}} <b>{{.Count}}</b></a>{{end}}
</nav>
<div class="two">
<section>
<h2>Recently tended</h2>
<ul class="rows">
{{range .Recent}}<li><a class="t" href="/p/{{.Slug}}">{{if .Title}}{{.Title}}{{else}}{{.Slug}}{{end}}</a>{{if .Type}}<span class="tag-label">{{.Type}}</span>{{end}}<span class="date">{{.LastUpdated}}</span></li>{{end}}
</ul>
</section>
<section>
<h2>Activity</h2>
<ul class="rows">
{{range .Activity}}<li><span class="t">{{.Message}}</span><span class="date">{{.Date}}</span></li>{{end}}
</ul>
</section>
</div>
{{template "foot" .}}
{{end}}

{{define "page"}}
{{template "head" .}}
<article>
<h1>{{.Title}}</h1>
<p class="badges">
{{if .Type}}<span class="badge type">{{.Type}}</span>{{end}}
{{if .Status}}<span class="badge status">{{.Status}}</span>{{end}}
{{if .LastUpdated}}<span class="badge status">{{.LastUpdated}}</span>{{end}}
{{if .Confidence}}<span class="conf-dot" style="opacity:{{.ConfDot}}"></span><span class="badge status">confidence {{.Confidence}}</span>{{end}}
</p>
{{if .Tags}}<p class="tags">{{range .Tags}}<span class="tag">#{{.}}</span>{{end}}</p>{{end}}
{{if .Summary}}<p class="note">{{.Summary}}</p>{{end}}
<div class="md">
{{.HTML}}
</div>
</article>
{{if .Backlinks}}
<section class="backlinks">
<h2>反向链接</h2>
<ul class="rows">
{{range .Backlinks}}<li><a class="t" href="/p/{{.slug}}">{{if .title}}{{.title}}{{else}}{{.slug}}{{end}}</a></li>{{end}}
</ul>
</section>
{{end}}
{{template "foot" .}}
{{end}}

{{define "search"}}
{{template "head" .}}
<h1>搜索</h1>
<form class="searchpage" action="/search" method="get">
<input type="search" name="q" value="{{.Q}}" placeholder="关键词…">
<select name="mode">
<option value="keyword"{{if eq .Mode "keyword"}} selected{{end}}>关键词</option>
<option value="hybrid"{{if eq .Mode "hybrid"}} selected{{end}}>混合</option>
<option value="semantic"{{if eq .Mode "semantic"}} selected{{end}}>语义</option>
</select>
<button type="submit">搜索</button>
</form>
{{if .Fallback}}<p class="note">语义检索未配置（[embedding] 缺失），已回退关键词检索。</p>
{{else if .ModeNote}}<p class="note">语义/混合模式包含一次嵌入网关往返。</p>{{end}}
{{if .Err}}<p class="note">{{.Err}}</p>{{end}}
{{if .Q}}{{if not .Results}}<p class="note">无结果。</p>{{end}}{{end}}
<ul class="rows">
{{range .Results}}<li>
<a class="t" href="/p/{{.Ref.Slug}}">{{if .Ref.Title}}{{.Ref.Title}}{{else}}{{.Ref.Slug}}{{end}}</a>
<span class="score">{{printf "%.2f" .Ref.Score}}</span>
{{if .ExcerptHTML}}<p class="excerpt">{{.ExcerptHTML}}</p>{{end}}
</li>{{end}}
</ul>
{{template "foot" .}}
{{end}}

{{define "list"}}
{{template "head" .}}
<h1>{{.Label}}</h1>
<p class="note">{{.Total}} 页</p>
{{range .Groups}}
<section>
<h2>{{.Label}}</h2>
<ul class="rows">
{{range .Pages}}<li><a class="t" href="/p/{{.Slug}}">{{if .Title}}{{.Title}}{{else}}{{.Slug}}{{end}}</a>{{if .Type}}<span class="tag-label">{{.Type}}</span>{{end}}{{if .Summary}}<p class="excerpt">{{.Summary}}</p>{{end}}</li>{{end}}
</ul>
</section>
{{end}}
{{template "foot" .}}
{{end}}

{{define "graph"}}
{{template "head" .}}
<h1>概念图</h1>
<p class="note">{{.Nodes}} 节点 · {{.Edges}} 边 · <a href="/graph.mmd">下载 .mmd</a> · <a href="/graph.dot">下载 .dot</a></p>
<pre class="graph">{{.LLMS}}</pre>
{{template "foot" .}}
{{end}}

{{define "msg"}}
{{template "head" .}}
<h1>{{.Title}}</h1>
<p class="note">{{.Message}}</p>
<p><a href="/">← 返回首页</a></p>
{{template "foot" .}}
{{end}}
`
