import { attachDiagramCanvas } from './diagram-canvas.js';
const main = document.querySelector('main');
const title = document.querySelector('#page-title');
const theme = document.querySelector('#theme');
const media = matchMedia('(prefers-color-scheme: dark)');
const names = { brand: '品牌', tokens: '设计 Token', components: '组件状态', pages: '页面预览', architecture: '架构与流程' };
let data, selectedDiagram, selectedPage, selectedPreviewConfig = 'light-1', activeCanvas;
const escape = value => String(value).replace(/[&<>"']/g, c => ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' }[c]));
function sourceLinks(paths) {
  return `<div class="source-list">${paths.map(path => `<a href="${data.sourceIndex[path].url}" target="_blank" rel="noopener">${escape(path)}</a>`).join('')}</div>`;
}
function applyTheme() {
  document.documentElement.dataset.theme = theme.value === 'system' ? (media.matches ? 'dark' : 'light') : theme.value;
  if (location.hash === '#tokens') renderTokens();
}
theme.addEventListener('change', applyTheme);
media.addEventListener('change', applyTheme);

function renderBrand() {
  main.innerHTML = `
    <div class="brand-stage"><img class="mark-image" src="brand/mark.svg" width="96" height="96" alt="MiMac 品牌标志"><div><h2>MiMac</h2><p>遥控器与语音工具之间的桥。</p><p class="muted">黑白、克制、直接。</p></div></div>
    <div class="asset-grid">
      <div class="asset"><div class="asset-preview"><img class="mark-image" src="brand/mark.svg" width="48" height="48" alt="矢量标志"></div><h3>标志</h3><span>SVG，来自品牌源文件</span><a href="brand/mark.svg" download>下载 SVG</a></div>
      <div class="asset"><div class="asset-preview"><img src="brand/app-icon.png" width="80" height="80" alt="MiMac 应用图标"></div><h3>应用图标</h3><span>同源 PNG，Icon Composer 原稿保留</span><a href="brand/app-icon.png" download>下载 PNG</a></div>
      <div class="asset"><div class="asset-preview"><img class="mark-image" src="brand/tray.png" width="22" height="22" alt="菜单栏图标"></div><h3>菜单栏</h3><span>模板图标，运行时随系统外观</span><a href="brand/tray.png" download>下载 PNG</a></div>
    </div>
    <section class="section"><h2>使用规范</h2><dl class="facts"><dt>品牌色</dt><dd>主文字色，浅色近黑，深色反转。</dd><dt>应用图标</dt><dd>保持白底黑标，不添加描边、投影或其他强调色。</dd><dt>状态例外</dt><dd>遥控器真正就绪时使用系统绿色，状态必须同时有文字。</dd><dt>源文件</dt><dd><code>Assets/Brand/MiMac.icon</code></dd></dl>${sourceLinks(['DESIGN.md','Assets/Brand/brand.go'])}</section>`;
}
function renderTokens() {
  const palette = data.palettes[document.documentElement.dataset.theme];
  const roles = { Background:'页面', Text:'主文字', TextMuted:'次文字', Surface:'表面', SurfaceHover:'悬停', SurfacePressed:'按下', Border:'边框', Accent:'操作', AccentHover:'操作悬停', AccentPressed:'操作按下', AccentText:'操作文字', Separator:'分隔线', Disabled:'禁用文字', Selection:'选中', Focus:'焦点', Inverse:'反色表面', InverseText:'反色文字', Scrollbar:'滚动条', Success:'成功文字', Warning:'提醒文字', Danger:'错误文字' };
  main.innerHTML = `<p class="intro muted">执行值由 <code>cmd/mimac/design.go</code> 自动导出。修改源码后重新构建，工作台不维护另一套数值。</p>
    <section><div class="section-heading"><h2>颜色</h2><p>语义角色随外观切换</p></div><div class="swatches">${Object.entries(roles).filter(([key])=>palette[key]).map(([key,label])=>`<div class="swatch"><div class="swatch-surface" style="background:${escape(palette[key])}"></div><strong>${label}</strong><code>${key} ${palette[key]}</code></div>`).join('')}</div></section>
    <section class="section"><div class="section-heading"><h2>尺寸、间距与字重</h2><label>筛选 <input class="filter" id="token-filter" type="search" placeholder="如 spacing、control、window" aria-label="筛选 Token"></label></div><table class="token-table"><thead><tr><th>Token</th><th>值</th><th>比例</th></tr></thead><tbody id="token-rows"></tbody></table></section>${sourceLinks(['cmd/mimac/design.go','DESIGN.md'])}`;
  const filter = document.querySelector('#token-filter');
  const renderRows = () => {
    const query = filter.value.trim().toLowerCase();
    const values = Object.entries(data.tokens).filter(([key])=>key.toLowerCase().includes(query));
    document.querySelector('#token-rows').innerHTML = values.map(([key,value])=>`<tr><td><code>${key}</code></td><td>${value}${['mappingListRatio','mappingReferenceRows','sidebarSymbolWeight'].includes(key)?'':' pt'}</td><td><div class="measure" style="width:${Math.max(1,Math.min(value,184))}px" aria-hidden="true"></div></td></tr>`).join('') || '<tr><td colspan="3">没有匹配项</td></tr>';
  };
  filter.addEventListener('input', renderRows); renderRows();
}
function renderComponents() {
  main.innerHTML = `<p class="intro muted">布局样本使用项目 token。原生菜单、玻璃材质和状态动效仍以 MiMac 实际窗口为准。</p>
    <section><div class="section-heading"><h2>操作控件</h2><p>5pt 圆角，0.5pt 边框</p></div><div class="sample-area samples"><button>启用遥控器</button><button class="primary">使用此工具</button><button disabled>尚不可用</button><label>语音工具 <select id="tool-select"><option value="doubao">豆包输入法</option><option value="apple">Apple 语音识别</option></select></label></div></section>
    <section class="section"><div class="section-heading"><h2>两列工具选项</h2><p>选择与上方控件共用一个值</p></div><div class="tool-columns"><div class="tool-option"><label><input type="radio" name="tool" value="doubao" checked>豆包输入法</label><span class="choice-label">当前工具</span></div><div class="tool-option"><label><input type="radio" name="tool" value="apple">Apple 语音识别</label><span class="choice-label"></span></div></div><p class="native-note">仅切换本页样本，不改变应用设置，不启动录音。</p>${sourceLinks(['cmd/mimac/settings_voice.go'])}</section>
    <section class="section"><div class="section-heading"><h2>遥控器状态</h2><p>配置指引没有状态点</p></div><div class="sample-area"><div class="sample-row"><span>未启用</span><button>启用遥控器</button></div><div class="sample-row"><span class="status">请启用语音插件</span><button>停用遥控器</button></div><div class="sample-row"><span class="status">请选择语音工具</span><button>停用遥控器</button></div><div class="sample-row"><span>已启用</span><span class="muted">原生系统绿点与光晕</span></div></div>${sourceLinks(['cmd/mimac/remote.go','internal/platform/readyindicator/native/ReadyIndicator.m'])}</section>
    <section class="section"><h2>小米基准布局</h2><dl class="facts"><dt>窗口</dt><dd>${data.tokens.windowWidth} × ${data.tokens.windowHeight} pt 内容区域，最小尺寸保持一致。</dd><dt>列宽</dt><dd>模型 : 映射 = 1 : ${data.tokens.mappingListRatio}</dd><dt>固定锚点</dt><dd>${data.tokens.mappingReferenceRows} 行，底部操作中心与 TV 行对齐。</dd><dt>文字</dt><dd>小、标准、大共用同一套字体测量与行高计算。</dd></dl>${sourceLinks(['cmd/mimac/settings_mapping.go','cmd/mimac/design.go'])}</section>`;
  const select = document.querySelector('#tool-select');
  const choose = value => {
    select.value = value;
    document.querySelectorAll('input[name="tool"]').forEach(input=>{ input.checked = input.value===value; input.closest('.tool-option').querySelector('.choice-label').textContent=input.checked?'当前工具':''; });
  };
  select.addEventListener('change',()=>choose(select.value));
  document.querySelectorAll('input[name="tool"]').forEach(input=>input.addEventListener('change',()=>choose(input.value)));
}
function renderPages() {
  const previews = data.nativePreviews;
  if (!previews) {
    main.innerHTML = `<p>尚未采集原生页面。</p><pre>script/capture_workbench.swift --bundle /absolute/path/MiMac.app
./script/workbench.sh --build</pre>`;
    return;
  }
  const configurations = previews.matrix?.configurations || [];
  const hasLatest = Array.isArray(previews.pages) && previews.pages.length > 0 && !previews.isMatrixFallback;
  const selectedConfig = configurations.find(item => item.key === selectedPreviewConfig);
  if (selectedPreviewConfig !== 'latest' && !selectedConfig) selectedPreviewConfig = hasLatest ? 'latest' : configurations[0]?.key;
  if (selectedPreviewConfig === 'latest' && !hasLatest && configurations.length > 0) selectedPreviewConfig = configurations[0].key;
  const matrixConfig = configurations.find(item => item.key === selectedPreviewConfig);
  const capture = selectedPreviewConfig === 'latest' ? previews : matrixConfig?.capture;
  if (!capture?.pages?.length) {
    main.innerHTML = `<p>尚未采集原生页面。</p><pre>script/capture_workbench.swift --bundle /absolute/path/MiMac.app
./script/workbench.sh --build</pre>`;
    return;
  }
  selectedPage ||= capture.pages[0].id;
  const page = capture.pages.find(page => page.id === selectedPage) || capture.pages[0];
  const configurationLabel = selectedPreviewConfig === 'latest' ? '单次采集' : matrixConfig?.label || selectedPreviewConfig;
  const date = new Date(page.capturedAt).toLocaleString('zh-CN', { hour12: false });
  const configSelector = configurations.length ? `<label class="preview-config">截图配置<select id="native-preview-config" aria-label="选择截图配置">${configurations.map(item => `<option value="${escape(item.key)}" ${item.key === selectedPreviewConfig ? 'selected' : ''}>${escape(item.label)}</option>`).join('')}${hasLatest ? `<option value="latest" ${selectedPreviewConfig === 'latest' ? 'selected' : ''}>单次采集</option>` : ''}</select></label>` : '';
  main.innerHTML = `<div class="preview-controls">${configSelector}<div class="preview-navigation" role="group" aria-label="选择原生页面">${capture.pages.map(item => `<button data-preview="${escape(item.id)}" aria-pressed="${item.id === page.id}">${escape(item.title)}</button>`).join('')}</div></div>
    <figure class="native-preview"><a href="${escape(page.url)}" target="_blank" rel="noopener" aria-label="打开${escape(configurationLabel)} ${escape(page.title)}原始截图"><img src="${escape(page.url)}" width="${page.width}" height="${page.height}" alt="MiMac ${escape(configurationLabel)} ${escape(page.title)}原生窗口，${escape(page.state)}"></a><figcaption><span>${escape(configurationLabel)}，${escape(page.title)}，${escape(page.state)}</span><a href="${escape(page.url)}" download>下载原图</a></figcaption></figure>
    <details><summary>采集信息</summary><p class="preview-note">静态截图只记录采集时的页面，不代表权限、连接、错误或语音动态状态已验收。</p><dl class="facts"><dt>截图配置</dt><dd>${escape(configurationLabel)}</dd><dt>采集时间</dt><dd>${escape(date)}</dd>${capture.app.pid ? `<dt>采集进程 PID</dt><dd>${capture.app.pid}</dd>` : ''}<dt>窗口</dt><dd>${capture.window.width} × ${capture.window.height} pt</dd><dt>图片</dt><dd>${page.width} × ${page.height} px</dd><dt>磁盘程序 SHA256</dt><dd><code>${escape(capture.app.executableSHA256)}</code></dd></dl>${sourceLinks(page.sourcePaths)}</details>`;
  const configSelect = document.querySelector('#native-preview-config');
  configSelect?.addEventListener('change', () => { selectedPreviewConfig = configSelect.value; renderPages(); });
  document.querySelectorAll('[data-preview]').forEach(button => button.addEventListener('click', () => { selectedPage = button.dataset.preview; renderPages(); }));
}
function renderArchitecture() {
  selectedDiagram ||= data.diagrams[0].id;
  main.innerHTML = `<p class="intro muted">MyGo 宿主，最小通用核心，独立设备与语音适配器。Siri 材质作为可选显示适配器，仅在已验证的系统版本加载。</p><div class="diagram-list"><div class="diagram-nav" role="group" aria-label="选择流程图">${data.diagrams.map(d=>`<button data-diagram="${escape(d.id)}" aria-pressed="false">${escape(d.title)}</button>`).join('')}</div><section class="diagram-panel" id="diagram-panel" aria-live="polite"></section></div>`;
  document.querySelectorAll('[data-diagram]').forEach(button=>button.addEventListener('click',()=>{selectedDiagram=button.dataset.diagram;renderDiagram();}));
  renderDiagram();
}
function renderDiagram() {
  activeCanvas?.destroy();
  activeCanvas = null;
  const d = data.diagrams.find(item=>item.id===selectedDiagram);
  document.querySelectorAll('[data-diagram]').forEach(button=>button.setAttribute('aria-pressed',String(button.dataset.diagram===selectedDiagram)));
  document.querySelector('#diagram-panel').innerHTML = `<div class="toolbar"><h2>${escape(d.title)}</h2><div class="zoom-actions"><button id="zoom-out" aria-label="缩小图表">−</button><output id="zoom-value" class="zoom-value" aria-label="缩放比例"></output><button id="zoom-fit">适应视图</button><button id="zoom-reset">100%</button><button id="zoom-in" aria-label="放大图表">＋</button><button id="download-svg">导出 SVG</button></div></div><p class="diagram-summary">${escape(d.summary)}</p><div class="diagram-viewport pele" tabindex="0" role="region" aria-label="${escape(d.title)}，拖拽平移，滚轮或捏合缩放">${d.svg}</div>${sourceLinks(d.sourcePaths)}<details><summary>Mermaid 源码</summary><pre>${escape(d.source)}</pre><button id="download-source">下载源码</button></details>`;
  const viewport = document.querySelector('.diagram-viewport');
  const svg = viewport.querySelector('svg');
  const zoomOut = document.querySelector('#zoom-out');
  const zoomIn = document.querySelector('#zoom-in');
  activeCanvas = attachDiagramCanvas(viewport, svg, {
    width: d.width, height: d.height,
    onScaleChange: value => {
      document.querySelector('#zoom-value').value = `${Math.round(value * 100)}%`;
      zoomOut.disabled = value <= 0.1;
      zoomIn.disabled = value >= 4;
    },
  });
  zoomIn.onclick = activeCanvas.zoomIn;
  zoomOut.onclick = activeCanvas.zoomOut;
  document.querySelector('#zoom-fit').onclick = activeCanvas.fit;
  document.querySelector('#zoom-reset').onclick = activeCanvas.reset;
  document.querySelector('#download-source').onclick=()=>download(`${d.id}.mmd`,d.source,'text/plain');
  document.querySelector('#download-svg').onclick=()=>{
    const copy=svg.cloneNode(true), computed=getComputedStyle(svg);
    const keys=['bg','fg','muted','line','surface','surface-alt','border','accent','font','radius'];
    // Keep Pele's internal aliases; replacing the style attribute would break
    // every var(--_fg)/var(--_s) reference in a standalone export.
    for (const key of keys) copy.style.setProperty(`--pele-${key}`,computed.getPropertyValue(`--pele-${key}`));
    copy.style.removeProperty('width');
    copy.style.maxWidth='none';
    copy.setAttribute('width',String(d.width));copy.setAttribute('height',String(d.height));
    const background=document.createElementNS('http://www.w3.org/2000/svg','rect');
    background.setAttribute('width','100%');background.setAttribute('height','100%');
    background.setAttribute('fill',computed.getPropertyValue('--pele-bg').trim());
    copy.insertBefore(background,copy.firstChild);
    download(`${d.id}.svg`,new XMLSerializer().serializeToString(copy),'image/svg+xml');
  };
}
function download(name,content,type) {
  const url=URL.createObjectURL(new Blob([content],{type}));
  const link=document.createElement('a');link.href=url;link.download=name;link.click();
  setTimeout(()=>URL.revokeObjectURL(url),1000);
}
function route() {
  activeCanvas?.destroy();
  activeCanvas = null;
  const key=location.hash.slice(1)||'brand';
  if (!(key in names)) { location.hash='brand'; return; }
  title.textContent=names[key];document.title=`${names[key]} | MiMac 工作台`;
  document.querySelectorAll('nav a').forEach(a=>a.setAttribute('aria-current',a.hash===`#${key}`?'page':'false'));
  ({brand:renderBrand,tokens:renderTokens,components:renderComponents,pages:renderPages,architecture:renderArchitecture})[key]();
}
try {
  const response=await fetch('data.json');if(!response.ok)throw new Error('项目数据不可用');data=await response.json();
  document.querySelector('#revision').textContent=`源码 ${data.revision}`;
  document.querySelector('#built-at').textContent=`构建于 ${new Date(data.builtAt).toLocaleString('zh-CN',{hour12:false})}`;
  addEventListener('hashchange',route);route();
} catch(error) { main.innerHTML=`<p class="error">工作台加载失败：${escape(error.message)}。请运行 script/workbench.sh 重新构建。</p>`; }
