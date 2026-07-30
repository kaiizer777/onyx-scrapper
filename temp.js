
let currentPoll = null;
let currentRunId = null;
let currentRunType = null;
let selectedMode = 'research'; 


document.addEventListener('DOMContentLoaded', () => {
  const hash = window.location.hash;
  if (hash && hash.includes('/')) {
    const parts = hash.substring(1).split('/');
    if (parts.length === 2) {
      currentRunType = parts[0];
      currentRunId = parts[1];
      loadRun(currentRunType, currentRunId);
    } else {
      loadHistory();
    }
  } else {
    loadHistory();
  }
  
  const input = document.getElementById('query-input');
  input.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      submitQuery();
    }
  });
  
  
  document.addEventListener('click', e => {
    if (!e.target.closest('#mode-selector-btn')) {
      document.getElementById('mode-dropdown').classList.remove('show');
    }
  });
});

function toggleModeDropdown(e) {
  e.stopPropagation();
  document.getElementById('mode-dropdown').classList.toggle('show');
}

function setMode(mode) {
  selectedMode = mode;
  const label = mode === 'research' ? '🔍 Deep Research' : '⚡ Agent';
  document.getElementById('current-mode-label').textContent = label;
  document.getElementById('mode-dropdown').classList.remove('show');
  document.getElementById('query-input').focus();
}

function autoResize(el) {
  el.style.height = 'auto';
  el.style.height = (el.scrollHeight) + 'px';
}

function scrollToBottom() {
  const scroll = document.getElementById('thread-scroll');
  scroll.scrollTo({ top: scroll.scrollHeight, behavior: 'smooth' });
}

async function loadHistory() {
  try {
    const res = await fetch('/ui/history');
    const items = await res.json();
    const list = document.getElementById('history-list');
    list.innerHTML = '';
    
    if (items.length === 0) {
      list.innerHTML = '<div style="padding: 20px; text-align: center; color: var(--text-secondary); font-size: 13px;">No past runs</div>';
      
      
      if (!currentRunId) {
        document.getElementById('empty-state').style.display = 'flex';
      }
      return;
    }
    
    if (!currentRunId) {
       document.getElementById('empty-state').style.display = 'flex';
    }
    
    items.forEach(item => {
      const div = document.createElement('div');
      div.className = `history-item ${item.id === currentRunId && item.type === currentRunType ? 'active' : ''}`;
      div.onclick = () => loadRun(item.type, item.id);
      
      const date = new Date(item.started_at).toLocaleDateString([], { month:'short', day:'numeric', hour:'2-digit', minute:'2-digit' });
      div.innerHTML = `
        <div class="h-goal">${escapeHtml(item.goal)}</div>
        <div class="h-meta">
          <span class="badge ${item.status}">${item.status}</span>
          <span>${item.type}</span>
          <span style="margin-left:auto">${date}</span>
        </div>
      `;
      list.appendChild(div);
    });
  } catch (err) {
    console.error("Failed to load history", err);
  }
}

function startNew() {
  stopPolling();
  currentRunId = null;
  currentRunType = null;
  
  window.history.replaceState(null, null, ' ');
  
  const input = document.getElementById('query-input');
  input.value = '';
  autoResize(input);
  
  document.getElementById('empty-state').style.display = 'flex';
  
  
  const thread = document.getElementById('thread');
  Array.from(thread.children).forEach(c => {
    if (c.id !== 'empty-state') c.remove();
  });
  
  loadHistory(); 
  input.focus();
}

function setStreamingState(isStreaming) {
  const btnSend = document.getElementById('btn-send');
  const btnStop = document.getElementById('btn-stop');
  const input = document.getElementById('query-input');
  
  if (isStreaming) {
    btnSend.style.display = 'none';
    btnStop.style.display = 'flex';
    input.disabled = true;
  } else {
    btnSend.style.display = 'flex';
    btnStop.style.display = 'none';
    input.disabled = false;
    btnSend.disabled = false;
  }
}

async function stopRun() {
  if (!currentRunId || !currentRunType) return;
  const endpoint = currentRunType === 'agent' 
    ? `/agent/runs/${currentRunId}/cancel` 
    : `/deep-research/${currentRunId}/cancel`;
  
  
  setStreamingState(false);
  stopPolling();
  
  try {
    const res = await fetch(endpoint, { method: 'POST' });
    if (!res.ok) throw new Error(await res.text());
    
    
    setTimeout(() => {
      pollStatus();
    }, 500);
  } catch (err) {
    console.error("Failed to cancel run", err);
    
    pollStatus();
  }
}

async function submitQuery() {
  const input = document.getElementById('query-input');
  const query = input.value.trim();
  if (!query) return;
  
  const btn = document.getElementById('btn-send');
  setStreamingState(true);
  
  
  startNew(); 
  document.getElementById('empty-state').style.display = 'none';
  appendUserBubble(query);
  
  const type = selectedMode;
  currentRunType = type;
  
  try {
    const endpoint = type === 'agent' ? '/agent/async' : '/deep-research';
    const bodyPayload = type === 'agent' ? { goal: query } : { query: query };
    
    const res = await fetch(endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(bodyPayload)
    });
    
    if (!res.ok) throw new Error(await res.text());
    
    const data = await res.json();
    currentRunId = data.run_id;
    window.history.replaceState(null, null, `#${type}/${currentRunId}`);
    
    pollStatus();
    loadHistory(); 
    
  } catch (err) {
    const thread = document.getElementById('thread');
    const errBlock = document.createElement('div');
    errBlock.className = 'error-block';
    errBlock.textContent = "Failed to start run: " + err.message;
    thread.appendChild(errBlock);
    scrollToBottom();
  } finally {
    
    
    if (!currentRunId) {
      setStreamingState(false);
    }
  }
}

function startNew() {
  stopPolling();
  currentRunId = null;
  currentRunType = null;
  window.history.replaceState(null, null, '#');
  document.getElementById('query-input').value = '';
  document.getElementById('query-input').style.height = 'auto';
  
  const thread = document.getElementById('thread');
  Array.from(thread.children).forEach(c => {
    if (c.id !== 'empty-state') c.remove();
  });
  
  document.getElementById('empty-state').style.display = 'flex';
  
  document.querySelectorAll('.history-item').forEach(el => el.classList.remove('active'));
  document.getElementById('query-input').focus();
}

function loadRun(type, id) {
  stopPolling();
  currentRunType = type;
  currentRunId = id;
  
  window.history.replaceState(null, null, `#${type}/${id}`);
  
  document.getElementById('empty-state').style.display = 'none';
  
  const thread = document.getElementById('thread');
  Array.from(thread.children).forEach(c => {
    if (c.id !== 'empty-state') c.remove();
  });
  
  loadHistory(); 
  pollStatus();
}

function stopPolling() {
  if (currentPoll) {
    clearTimeout(currentPoll);
    currentPoll = null;
  }
}

function appendUserBubble(text) {
  const thread = document.getElementById('thread');
  const div = document.createElement('div');
  div.className = 'user-bubble';
  div.textContent = text;
  thread.appendChild(div);
  scrollToBottom();
}

async function pollStatus() {
  if (!currentRunId || !currentRunType) return;
  
  try {
    const endpoint = currentRunType === 'agent' 
      ? `/agent/runs/${currentRunId}` 
      : `/deep-research/${currentRunId}`;
      
    const res = await fetch(endpoint);
    if (!res.ok) throw new Error("Failed to fetch status");
    const data = await res.json();
    
    renderThread(data);
    
    const status = data.run.status;
    if (status !== 'completed' && status !== 'failed' && status !== 'max_steps_exceeded' && status !== 'cancelled') {
      setStreamingState(true);
      currentPoll = setTimeout(pollStatus, 1500);
    } else {
      setStreamingState(false);
      
      loadHistory();
    }
  } catch (err) {
    console.error("Polling error", err);
    currentPoll = setTimeout(pollStatus, 3000);
  }
}

function renderThread(data) {
  const thread = document.getElementById('thread');
  const run = data.run;
  
  if (!document.getElementById('user-bubble-' + run.id)) {
      const div = document.createElement('div');
      div.id = 'user-bubble-' + run.id;
      div.className = 'user-bubble';
      div.textContent = run.goal;
      
      const emptyState = document.getElementById('empty-state');
      if (emptyState) emptyState.style.display = 'none';
      
      thread.insertBefore(div, thread.firstChild);
  }
  
  let agentContainer = document.getElementById('agent-container-' + run.id);
  if (!agentContainer) {
    agentContainer = document.createElement('div');
    agentContainer.id = 'agent-container-' + run.id;
    agentContainer.className = 'agent-container';
    thread.appendChild(agentContainer);
  }
  
  let blocksContainer = document.getElementById('blocks-container-' + run.id);
  if (!blocksContainer) {
      blocksContainer = document.createElement('div');
      blocksContainer.id = 'blocks-container-' + run.id;
      blocksContainer.className = 'agent-blocks';
      agentContainer.appendChild(blocksContainer);
  }
  
  const expandedStates = new Set();
  Array.from(blocksContainer.children).forEach((c, i) => {
      if (c.classList.contains('expanded')) expandedStates.add(i);
  });
  
  blocksContainer.innerHTML = '';
  
  if (currentRunType === 'research') {
    const sqs = data.sub_questions || [];
    const findings = data.findings || [];
    
    sqs.forEach((sq, i) => {
      const block = document.createElement('div');
      block.className = 'step-block';
      if (expandedStates.has(i)) block.classList.add('expanded');
      
      const fds = findings.filter(f => f.subquestion_id === sq.id);
      
      let summary = `Decomposed sub-question ${i+1}: ${sq.question}`;
      if (sq.status === 'done') summary = `Researched: ${sq.question} (${fds.length} findings)`;
      
      let detailsHTML = `<div style="margin-bottom:8px"><strong>Status:</strong> ${sq.status}</div>`;
      if (fds.length > 0) {
        detailsHTML += `<ul>` + fds.map(f => `
          <li>
            ${escapeHtml(f.claim)}
            <br><a href="${escapeHtml(f.source_url)}" target="_blank" style="font-size:11px;color:var(--brand-primary)">${escapeHtml(f.source_url)}</a>
          </li>`).join('') + `</ul>`;
      }
      
      block.innerHTML = `
        <div class="step-summary" onclick="this.parentElement.classList.toggle('expanded')">
          <div class="step-icon">▶</div>
          <div class="step-title">${escapeHtml(summary)}</div>
        </div>
        <div class="step-details-wrapper">
          <div class="step-details-inner">
            <div class="step-details">${detailsHTML}</div>
          </div>
        </div>
      `;
      blocksContainer.appendChild(block);
    });
    
    let loader = document.getElementById('loader-' + run.id);
    if (run.status === 'running') {
        const txt = sqs.length === 0 ? "Initializing research plan..." : "Researching in progress...";
        if (!loader) {
            loader = document.createElement('div');
            loader.id = 'loader-' + run.id;
            loader.className = 'loading-indicator';
            loader.innerHTML = `<span>${txt}</span><div class="loading-dots"><span></span><span></span><span></span></div>`;
            agentContainer.appendChild(loader);
            scrollToBottom();
        } else {
            loader.querySelector('span').textContent = txt;
        }
    } else if (loader) {
        loader.remove();
    }
    
    if (run.report_md && !document.getElementById('report-' + run.id)) {
      const reportWrap = document.createElement('div');
      reportWrap.id = 'report-' + run.id;
      reportWrap.className = 'report-container';
      reportWrap.innerHTML = `
        <div class="report-header">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline><line x1="16" y1="13" x2="8" y2="13"></line><line x1="16" y1="17" x2="8" y2="17"></line><polyline points="10 9 9 9 8 9"></polyline></svg>
          Final Report
        </div>
        <div class="report-content">${marked.parse(run.report_md)}</div>
      `;
      agentContainer.appendChild(reportWrap);
      scrollToBottom();
    }
    
  } else if (currentRunType === 'agent') {
    const steps = data.steps || [];
    
    steps.forEach((step, i) => {
      const block = document.createElement('div');
      block.className = 'step-block';
      if (expandedStates.has(i)) block.classList.add('expanded');
      
      let summary = `Step ${step.step_number}: ${step.action}`;
      if (step.error) summary = `Error in ${step.action}`;
      
      let detailsHTML = `<pre>${escapeHtml(step.args_json)}</pre>`;
      if (step.result) detailsHTML += `<div style="margin-top:12px; font-family:var(--font-mono); font-size:12px;"><strong>Result:</strong><br>${escapeHtml(step.result)}</div>`;
      if (step.error) detailsHTML += `<div style="margin-top:12px; color:var(--status-err); font-family:var(--font-mono); font-size:12px;"><strong>Error:</strong><br>${escapeHtml(step.error)}</div>`;
      
      block.innerHTML = `
        <div class="step-summary" onclick="this.parentElement.classList.toggle('expanded')">
          <div class="step-icon">▶</div>
          <div class="step-title">${escapeHtml(summary)}</div>
        </div>
        <div class="step-details-wrapper">
          <div class="step-details-inner">
            <div class="step-details">${detailsHTML}</div>
          </div>
        </div>
      `;
      blocksContainer.appendChild(block);
    });
    
    let loader = document.getElementById('loader-' + run.id);
    if (run.status === 'running') {
        const txt = steps.length === 0 ? "Agent starting..." : "Agent executing...";
        if (!loader) {
            loader = document.createElement('div');
            loader.id = 'loader-' + run.id;
            loader.className = 'loading-indicator';
            loader.innerHTML = `<span>${txt}</span><div class="loading-dots"><span></span><span></span><span></span></div>`;
            agentContainer.appendChild(loader);
            scrollToBottom();
        } else {
            loader.querySelector('span').textContent = txt;
        }
    } else if (loader) {
        loader.remove();
    }
    
    if (run.status === 'completed' && run.result && !document.getElementById('report-' + run.id)) {
      const reportWrap = document.createElement('div');
      reportWrap.id = 'report-' + run.id;
      reportWrap.className = 'report-container';
      reportWrap.innerHTML = `
        <div class="report-header">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline><line x1="16" y1="13" x2="8" y2="13"></line><line x1="16" y1="17" x2="8" y2="17"></line><polyline points="10 9 9 9 8 9"></polyline></svg>
          Final Report
        </div>
        <div class="report-content">${marked.parse(run.result)}</div>
      `;
      agentContainer.appendChild(reportWrap);
      scrollToBottom();
    }
  }
  
  }
  
  if ((run.status === 'failed' || run.status === 'max_steps_exceeded' || run.status === 'cancelled') && !document.getElementById('error-' + run.id)) {
    const err = document.createElement('div');
    err.id = 'error-' + run.id;
    err.className = 'error-block';
    if (run.status === 'cancelled') {
        err.style.background = 'rgba(148, 163, 184, 0.1)';
        err.style.borderColor = 'rgba(148, 163, 184, 0.3)';
        err.style.color = '#94a3b8';
    }
    err.innerHTML = `<strong>Run finished with status: ${escapeHtml(run.status)}</strong><br>${escapeHtml(run.result || '')}`;
    agentContainer.appendChild(err);
    scrollToBottom();
  }
}

function escapeHtml(unsafe) {
  if (!unsafe) return '';
  return unsafe
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}
