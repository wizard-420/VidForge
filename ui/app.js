// ==========================================
// YouTube Automation Studio — Frontend Logic
// ==========================================

const API = 'http://localhost:8000';

// Global state object — sent as POST /api/jobs body
const state = {
  raw_input: '',
  input_type: 'category',
  format: 'long',
  duration_min: 8,
  voiceover_mode: 'ai',
  voice_id: 'adam',
  video_mode: 'auto',
  video_style: 'stock',
  music_mode: 'auto',
  script_tone: 'dramatic',
  language: 'english',
  upload_schedule: 'immediate',
  caption_style: 'bold_white',
  auto_upload: false,
  clip_count: 6,
  image_count: 0
};

let currentJobId = null;
let ws = null;

// ---- Hint chips per input type ----
const hints = {
  category: ['Dark History', 'True Crime', 'Science Mysteries', 'Ancient Civilizations', 'Tech Scandals', 'Space Exploration'],
  topic: ['Fall of the Roman Empire', 'How WiFi Actually Works', 'The Dark Side of AI', 'Lost Cities Found by Satellites'],
  event: ['Titan submarine implosion', 'Solar storm nearly hit Earth in 2012', 'New high-temp superconductor discovery']
};

// ---- Page Navigation ----
function showPage(page) {
  document.querySelectorAll('[id^="page-"]').forEach(el => el.classList.add('hidden'));
  document.getElementById('page-' + page).classList.remove('hidden');
  document.querySelectorAll('.nav-item').forEach(el => el.classList.toggle('active', el.dataset.page === page));
  if (page === 'jobs') loadJobs();
  if (page === 'settings') loadSettings();
}

// ---- Input Type ----
function setInputType(type) {
  state.input_type = type;
  document.querySelectorAll('#input-tabs .tab').forEach((t, i) => {
    t.classList.toggle('active', ['category', 'topic', 'event'][i] === type);
  });
  const placeholders = {
    category: 'e.g. Dark History, True Crime, Space...',
    topic: 'e.g. How WiFi Actually Works...',
    event: 'Describe an event or story in detail...'
  };
  document.getElementById('raw-input').placeholder = placeholders[type];
  renderHints(type);
}

function renderHints(type) {
  const el = document.getElementById('hint-chips');
  el.innerHTML = hints[type].map(h =>
    `<span class="hint" onclick="document.getElementById('raw-input').value='${h}';state.raw_input='${h}'">${h}</span>`
  ).join('');
}

// ---- Format ----
function setFormat(fmt) {
  state.format = fmt;
  document.querySelectorAll('.format-card').forEach((c, i) => {
    c.classList.toggle('active', ['long', 'short', 'both'][i] === fmt);
  });
  document.getElementById('duration-slider').style.display = fmt === 'short' ? 'none' : '';
}

// ---- Voice ----
function setVoiceMode(mode) {
  state.voiceover_mode = mode;
  const btns = document.querySelector('#page-create .card:nth-child(4) .toggle-group').children;
  btns[0].classList.toggle('active', mode === 'ai');
  btns[1].classList.toggle('active', mode === 'manual');
  document.getElementById('voice-grid').style.display = mode === 'ai' ? '' : 'none';
}

function setVoice(id) {
  state.voice_id = id;
  document.querySelectorAll('.voice-card').forEach(c => {
    c.classList.toggle('active', c.onclick.toString().includes("'" + id + "'"));
  });
}

// ---- Video ----
function setVideoMode(mode) {
  state.video_mode = mode;
  const card = document.querySelector('#page-create .card:nth-child(5)');
  const btns = card.querySelector('.toggle-group').children;
  btns[0].classList.toggle('active', mode === 'auto');
  btns[1].classList.toggle('active', mode === 'manual');
  document.getElementById('style-chips').style.display = mode === 'auto' ? '' : 'none';
  document.getElementById('media-counts').style.display = mode === 'auto' ? '' : 'none';
}

function setVideoStyle(style) {
  state.video_style = style;
  document.querySelectorAll('#style-chips .hint').forEach(c => {
    c.classList.toggle('active', c.onclick.toString().includes("'" + style + "'"));
  });
  // Show/hide clip and image sliders based on style
  const clipGroup = document.getElementById('clip-count-group');
  const imgGroup = document.getElementById('image-count-group');
  if (style === 'stock') {
    clipGroup.style.display = '';
    imgGroup.style.display = 'none';
    state.image_count = 0;
  } else if (style === 'ai_images') {
    clipGroup.style.display = 'none';
    imgGroup.style.display = '';
    state.clip_count = 0;
    // Set default image count
    const imgSlider = imgGroup.querySelector('input');
    imgSlider.value = 6;
    state.image_count = 6;
    document.getElementById('img-val').textContent = '6';
  } else { // mixed
    clipGroup.style.display = '';
    imgGroup.style.display = '';
    // Set defaults for mixed
    const clipSlider = clipGroup.querySelector('input');
    const imgSlider = imgGroup.querySelector('input');
    clipSlider.value = 4; state.clip_count = 4;
    imgSlider.value = 2; state.image_count = 2;
    document.getElementById('clip-val').textContent = '4';
    document.getElementById('img-val').textContent = '2';
  }
}

// ---- Music ----
function setMusicMode(mode) {
  state.music_mode = mode;
  const card = document.querySelector('#page-create .card:nth-child(6)');
  const btns = card.querySelector('.toggle-group').children;
  btns[0].classList.toggle('active', mode === 'auto');
  btns[1].classList.toggle('active', mode === 'skip');
}

// ---- Tone ----
function setTone(tone) {
  state.script_tone = tone;
  document.querySelectorAll('.tone-chip').forEach(c => {
    c.classList.toggle('active', c.onclick.toString().includes("'" + tone + "'"));
  });
}

// ---- Create Job ----
async function createJob() {
  if (!state.raw_input.trim()) {
    alert('Please enter a topic, category, or event description.');
    return;
  }

  const btn = document.getElementById('btn-generate');
  btn.disabled = true;
  btn.textContent = '⏳ Starting pipeline...';

  try {
    const resp = await fetch(API + '/api/jobs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(state)
    });

    const data = await resp.json();
    if (!resp.ok) throw new Error(data.error || 'Failed to create job');

    currentJobId = data.job_id;
    showProgress(data.job_id);
    connectWebSocket(data.job_id);

  } catch (err) {
    alert('Error: ' + err.message);
    btn.disabled = false;
    btn.textContent = '🚀 Generate Video';
  }
}

// ---- Progress Tracking ----
function showProgress(jobId) {
  const panel = document.getElementById('progress-panel');
  panel.classList.add('visible');
  document.getElementById('progress-job-id').textContent = 'Job ID: ' + jobId;
  document.getElementById('approval-panel').style.display = 'none';

  // Reset all steps
  document.querySelectorAll('.pipeline-step').forEach(step => {
    step.className = 'pipeline-step waiting';
    step.querySelector('.step-msg').textContent = 'Waiting...';
  });
}

function updateStep(stage, status, message) {
  const steps = document.querySelectorAll('.pipeline-step');
  steps.forEach((step, i) => {
    const stepNum = i + 1;
    if (stepNum < stage) {
      step.className = 'pipeline-step done';
      if (step.querySelector('.step-msg').textContent === 'Waiting...') {
        step.querySelector('.step-msg').textContent = 'Completed';
      }
      step.querySelector('.step-icon').textContent = '✓';
    } else if (stepNum === stage) {
      step.className = 'pipeline-step ' + (status || 'running');
      step.querySelector('.step-msg').textContent = message || 'Processing...';
      if (status === 'done') step.querySelector('.step-icon').textContent = '✓';
    }
  });
}

// ---- WebSocket ----
function connectWebSocket(jobId) {
  if (ws) ws.close();
  ws = new WebSocket('ws://localhost:8000/ws/' + jobId);

  ws.onmessage = function(event) {
    const data = JSON.parse(event.data);
    if (data.status === 'completed') {
      updateStep(7, 'done', 'Pipeline completed! 🎉');
      document.getElementById('btn-generate').disabled = false;
      document.getElementById('btn-generate').textContent = '🚀 Generate Video';
      if (data.youtube_url) {
        const panel = document.getElementById('progress-panel');
        panel.querySelector('.card').insertAdjacentHTML('beforeend',
          `<div style="margin-top:16px;padding:12px;background:rgba(52,211,153,0.1);border-radius:8px;font-size:13px">
            ✅ Video ready: <a href="${data.youtube_url}" target="_blank" style="color:var(--accent)">${data.youtube_url}</a>
          </div>`);
      }
    } else if (data.status === 'failed') {
      updateStep(data.stage, 'failed', data.message);
      document.getElementById('btn-generate').disabled = false;
      document.getElementById('btn-generate').textContent = '🚀 Generate Video';
    } else if (data.status === 'pending_approval') {
      updateStep(7, 'running', data.message);
      document.getElementById('approval-panel').style.display = 'block';
      const videoEl = document.getElementById('preview-video');
      videoEl.src = `${API}/api/jobs/${jobId}/download`;
      videoEl.load();
    } else {
      updateStep(data.stage, data.progress_pct >= 100 ? 'done' : 'running', data.message);
    }
  };

  ws.onerror = function() {
    console.error('WebSocket error');
  };
}

async function approveJob() {
  if (!currentJobId) return;
  document.getElementById('approval-panel').style.display = 'none';
  try {
    await fetch(`${API}/api/jobs/${currentJobId}/approve`, { method: 'POST' });
  } catch (err) {
    alert('Failed to approve job: ' + err);
  }
}

async function rejectJob() {
  if (!currentJobId) return;
  if (!confirm('Are you sure you want to discard this video?')) return;
  
  document.getElementById('approval-panel').style.display = 'none';
  try {
    await fetch(`${API}/api/jobs/${currentJobId}`, { method: 'DELETE' });
    updateStep(7, 'failed', 'Video rejected and deleted by user');
    document.getElementById('btn-generate').disabled = false;
    document.getElementById('btn-generate').textContent = '🚀 Generate Video';
  } catch (err) {
    alert('Failed to reject job: ' + err);
  }
}

// ---- Jobs List ----
async function loadJobs() {
  try {
    const resp = await fetch(API + '/api/jobs');
    const jobs = await resp.json();
    const tbody = document.getElementById('jobs-tbody');

    if (!jobs || jobs.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--text-muted);padding:32px">No jobs yet</td></tr>';
      return;
    }

    tbody.innerHTML = jobs.map(j => `
      <tr>
        <td style="font-family:monospace;font-size:11px">${j.id.substring(0, 8)}</td>
        <td>${truncate(j.raw_input, 40)}</td>
        <td>${j.format}</td>
        <td><span class="status-badge ${j.status}">${j.status}</span></td>
        <td>${new Date(j.created_at).toLocaleString()}</td>
        <td>
          ${j.status === 'completed' ? `<a href="${API}/api/jobs/${j.id}/download" style="color:var(--accent);font-size:12px">⬇ Download</a>` : ''}
          ${j.status === 'failed' ? `<span onclick="retryJob('${j.id}')" style="color:var(--warning);cursor:pointer;font-size:12px">🔄 Retry</span>` : ''}
          ${j.status === 'pending_approval' ? `<span onclick="reviewJob('${j.id}')" style="color:var(--accent);cursor:pointer;font-size:12px">👀 Review</span>` : ''}
        </td>
      </tr>
    `).join('');
  } catch (err) {
    console.error('Failed to load jobs:', err);
  }
}

async function retryJob(id) {
  await fetch(API + '/api/jobs/' + id + '/retry', { method: 'POST' });
  showPage('create');
  showProgress(id);
  connectWebSocket(id);
}

function reviewJob(id) {
  currentJobId = id;
  showPage('create');
  showProgress(id);
  // Manually trigger the pending_approval UI state
  updateStep(7, 'running', 'Video rendered! Pending your approval before upload.');
  document.getElementById('approval-panel').style.display = 'block';
  const videoEl = document.getElementById('preview-video');
  videoEl.src = `${API}/api/jobs/${id}/download`;
  videoEl.load();
  connectWebSocket(id);
}

// ---- Settings ----
async function loadSettings() {
  try {
    // Load health status
    const statusResp = await fetch(API + '/api/status');
    const status = await statusResp.json();

    const keyList = document.getElementById('key-status-list');
    const keys = status.api_keys || {};
    const labels = { groq: 'Groq (Llama 3)', elevenlabs: 'ElevenLabs', pexels: 'Pexels', pixabay: 'Pixabay', openai: 'OpenAI (DALL-E)' };

    keyList.innerHTML = Object.entries(labels).map(([k, label]) =>
      `<div class="key-status"><div class="key-dot ${keys[k] ? 'set' : 'unset'}"></div>${label} — ${keys[k] ? '✅ Configured' : '❌ Not set'}</div>`
    ).join('');

    const sysEl = document.getElementById('system-status');
    sysEl.innerHTML = `
      <div class="key-status"><div class="key-dot ${status.ffmpeg ? 'set' : 'unset'}"></div>FFmpeg — ${status.ffmpeg ? '✅ Available' : '❌ Not found'}</div>
      <div class="key-status"><div class="key-dot ${status.whisper ? 'set' : 'unset'}"></div>Whisper — ${status.whisper ? '✅ Available' : '❌ Not found'}</div>
      <div style="margin-top:12px;font-size:12px;color:var(--text-muted)">Server version: ${status.version || '?'}</div>
    `;
  } catch (err) {
    document.getElementById('key-status-list').innerHTML = '<div style="color:var(--danger)">⚠️ Cannot connect to backend. Is the server running?</div>';
  }
}

// ---- Helpers ----
function truncate(str, len) {
  if (!str) return '';
  return str.length > len ? str.substring(0, len) + '...' : str;
}

// ---- Init ----
renderHints('category');
