// ==========================================
// YouTube Automation Studio — Frontend Logic
// ==========================================

const API = 'http://localhost:8000';

// Global state object — sent as POST /api/jobs body
const state = {
  raw_input: '',
  input_type: 'category',
  format: 'long',
  aspect_ratio: 'landscape',
  fit_mode: 'fill',
  output_quality: 'standard',
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
  clip_count: 0,
  image_count: 0,
  seconds_per_visual: 6,
  ai_image_percent: 0,
  music_url: '',
  music_start: 0,
  music_end: 0,
  pre_generated_script: null,
  manual_audio_base64: {},
  gcp_voice_name: '',
  gcp_language_code: ''
};

// Global variables for recording
let mediaRecorders = {};
let audioChunks = {};

let currentJobId = null;
let ws = null;

// ==========================================
// WIZARD NAVIGATION
// ==========================================
let currentWizardStep = 0;
const WIZARD_TOTAL_STEPS = 6;

const wizardStepLabels = ['Content', 'Format', 'Script', 'Visuals', 'Music', 'Finalize'];
const wizardNextLabels = ['Format', 'Script', 'Visuals', 'Music', 'Finalize'];

function wizardGoTo(step) {
  if (step < 0 || step >= WIZARD_TOTAL_STEPS) return;

  // Validate before going forward
  if (step > currentWizardStep) {
    const err = validateWizardStep(currentWizardStep);
    if (err) { alert(err); return; }
  }

  currentWizardStep = step;

  // Update panels
  document.querySelectorAll('.wizard-panel').forEach(p => p.classList.remove('active'));
  const target = document.querySelector(`.wizard-panel[data-panel="${step}"]`);
  if (target) target.classList.add('active');

  // Update stepper
  document.querySelectorAll('.wizard-step').forEach(s => {
    const idx = +s.dataset.wstep;
    s.classList.remove('active', 'done');
    if (idx < step) s.classList.add('done');
    else if (idx === step) s.classList.add('active');
  });

  document.querySelectorAll('.wizard-step-line').forEach((line, i) => {
    line.classList.toggle('done', i < step);
  });

  // On the final step, render the summary
  if (step === WIZARD_TOTAL_STEPS - 1) renderWizardSummary();

  // Scroll to top of main
  document.querySelector('.main').scrollTo({ top: 0, behavior: 'smooth' });
}

function wizardNext() { wizardGoTo(currentWizardStep + 1); }
function wizardBack() { wizardGoTo(currentWizardStep - 1); }

function wizardJumpTo(step) {
  if (step <= currentWizardStep || document.querySelector(`.wizard-step[data-wstep="${step}"]`).classList.contains('done')) {
    // Going backward or to a completed step — skip validation
    currentWizardStep = step;
    document.querySelectorAll('.wizard-panel').forEach(p => p.classList.remove('active'));
    const target = document.querySelector(`.wizard-panel[data-panel="${step}"]`);
    if (target) target.classList.add('active');

    document.querySelectorAll('.wizard-step').forEach(s => {
      const idx = +s.dataset.wstep;
      s.classList.remove('active', 'done');
      if (idx < step) s.classList.add('done');
      else if (idx === step) s.classList.add('active');
    });
    document.querySelectorAll('.wizard-step-line').forEach((line, i) => {
      line.classList.toggle('done', i < step);
    });

    if (step === WIZARD_TOTAL_STEPS - 1) renderWizardSummary();
    document.querySelector('.main').scrollTo({ top: 0, behavior: 'smooth' });
  }
}

function validateWizardStep(step) {
  switch (step) {
    case 0:
      if (!state.raw_input.trim()) return 'Please enter a topic, category, or event description.';
      break;
    case 2:
      if (!state.pre_generated_script) return 'Please generate and approve the script before proceeding.';
      if (state.voiceover_mode === 'manual') {
        const segs = state.pre_generated_script.segments;
        for (let i = 0; i < segs.length; i++) {
          if (!state.manual_audio_base64[segs[i].segment_id]) {
            return 'Please record audio for all segments. Missing: Segment ' + segs[i].segment_id + '.';
          }
        }
      }
      if (state.voiceover_mode === 'gcp_tts') {
        if (!state.gcp_language_code) return 'Please select a language for Google TTS.';
        if (!state.gcp_voice_name) return 'Please select a voice for Google TTS.';
        const voices = gcpVoicesCache[state.gcp_language_code] || [];
        const v = voices.find(x => x.name === state.gcp_voice_name);
        if (v && v.premium && !gcpServiceAccountConfigured) {
          return 'Premium voice "' + state.gcp_voice_name + '" requires a Google Cloud service account. Add GOOGLE_APPLICATION_CREDENTIALS_JSON to .env (and restart) or pick a non-premium voice.';
        }
      }
      break;
    case 4:
      if (state.music_mode === 'manual' && !state.music_url) {
        return 'Please search and select a music track, or switch to Auto Music / No Music.';
      }
      break;
  }
  return null;
}

function renderWizardSummary() {
  const el = document.getElementById('wizard-summary-content');
  if (!el) return;
  const items = [
    ['Input', state.raw_input ? (state.raw_input.length > 60 ? state.raw_input.substring(0, 60) + '...' : state.raw_input) : '—'],
    ['Type', state.input_type],
    ['Format', state.format + (state.format !== 'short' ? ' (' + state.duration_min + ' min)' : '') + ' • ' + (state.aspect_ratio || 'landscape') + ' • ' + (state.fit_mode || 'fill') + ' • ' + (state.output_quality || 'standard') + ' quality'],
    ['Voice', state.voiceover_mode === 'ai' ? 'AI — ' + state.voice_id : state.voiceover_mode === 'gcp_tts' ? 'Google TTS — ' + state.gcp_voice_name : state.voiceover_mode === 'none' ? '🎵 Music only (no narration)' : 'Manual recording'],
    ['Script', state.pre_generated_script ? 'Approved (' + state.pre_generated_script.segments.length + ' segments)' : 'Will be generated'],
    ['Visuals', `${state.video_style} • 1 / ${state.seconds_per_visual}s` + (state.video_mode === 'manual' ? ' (manual)' : '')],
    ['Music', state.music_mode + (state.music_mode === 'manual' && state.music_url ? ' (track selected)' : '')],
    ['Tone', state.script_tone],
    ['Language', state.language],
    ['Captions', state.caption_style],
    ['Upload', state.auto_upload ? 'Auto-upload' : 'Manual approval'],
  ];
  el.innerHTML = items.map(([k, v]) =>
    `<div class="summary-row"><span class="summary-label">${k}</span><span class="summary-value">${v}</span></div>`
  ).join('');
}

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

  // Auto-pick a sensible aspect ratio default for this format. The user can
  // still override it via the Aspect Ratio chips for any cross-platform use.
  if (fmt === 'short') {
    setAspectRatio('portrait');
  } else {
    setAspectRatio('landscape');
  }
  updateVisualsPreview();
}

// ---- Aspect Ratio ----
function setAspectRatio(aspect) {
  state.aspect_ratio = aspect;
  document.querySelectorAll('#aspect-chips .hint').forEach(c => {
    c.classList.toggle('active', c.dataset.aspect === aspect);
  });
}

// ---- Fit Mode ----
function setFitMode(mode) {
  state.fit_mode = mode;
  document.querySelectorAll('#fit-chips .hint').forEach(c => {
    c.classList.toggle('active', c.dataset.fit === mode);
  });
}

// ---- Output Quality ----
function setOutputQuality(quality) {
  state.output_quality = quality;
  document.querySelectorAll('#quality-chips .hint').forEach(c => {
    c.classList.toggle('active', c.dataset.quality === quality);
  });
}

// ---- Voice ----
function setVoiceMode(mode) {
  state.voiceover_mode = mode;
  const panel = document.querySelector('.wizard-panel[data-panel="2"]');
  const btns = panel.querySelector('.toggle-group').children;
  btns[0].classList.toggle('active', mode === 'ai');
  btns[1].classList.toggle('active', mode === 'gcp_tts');
  btns[2].classList.toggle('active', mode === 'manual');
  if (btns[3]) btns[3].classList.toggle('active', mode === 'none');

  document.getElementById('voice-grid').style.display = mode === 'ai' ? 'grid' : 'none';
  document.getElementById('gcp-tts-panel').style.display = mode === 'gcp_tts' ? 'block' : 'none';

  // Music-only info card
  const noVoicePanel = document.getElementById('no-voice-panel');
  if (noVoicePanel) noVoicePanel.style.display = mode === 'none' ? 'block' : 'none';

  // Hide manual-recording UI (segment recorders) when not in manual mode —
  // it's already only shown after script approval, but switching modes mid-flow
  // shouldn't leave stale recorders visible.
  const recPanel = document.getElementById('recording-script-container');
  if (recPanel && mode !== 'manual') recPanel.style.display = 'none';

  if (mode === 'gcp_tts' && !state.gcp_language_code) {
    state.gcp_language_code = 'en-US';
    loadGCPVoices('en-US');
  }
}

function setVoice(id) {
  state.voice_id = id;
  document.querySelectorAll('.voice-card').forEach(c => {
    c.classList.toggle('active', c.onclick.toString().includes("'" + id + "'"));
  });
}

// ---- Google Cloud TTS ----
let gcpVoicesCache = {};
let gcpServiceAccountConfigured = false; // toggled true when /api/gcp-tts/voices reports it

function classifyGCPVoice(name) {
  if (name.includes('Chirp3-HD')) return { label: 'Chirp 3 HD', premium: true };
  if (name.includes('Studio')) return { label: 'Studio', premium: true };
  if (name.includes('Neural2')) return { label: 'Neural2', premium: false };
  if (name.includes('Wavenet') || name.includes('WaveNet')) return { label: 'WaveNet', premium: false };
  if (name.includes('Chirp-HD') || name.includes('Chirp')) return { label: 'Chirp HD', premium: false };
  if (name.includes('News')) return { label: 'News', premium: false };
  if (name.includes('Casual')) return { label: 'Casual', premium: false };
  if (name.includes('Polyglot')) return { label: 'Polyglot', premium: false };
  return { label: 'Standard', premium: false };
}

async function loadGCPVoices(languageCode) {
  state.gcp_language_code = languageCode;
  state.gcp_voice_name = '';

  const voiceSelect = document.getElementById('gcp-voice-select');
  voiceSelect.innerHTML = '<option value="">Loading voices...</option>';
  document.getElementById('gcp-voice-info').style.display = 'none';

  if (gcpVoicesCache[languageCode]) {
    renderGCPVoiceOptions(gcpVoicesCache[languageCode]);
    return;
  }

  try {
    const resp = await fetch(API + '/api/gcp-tts/voices?language=' + encodeURIComponent(languageCode));
    if (!resp.ok) {
      const err = await resp.json();
      voiceSelect.innerHTML = '<option value="">Error: ' + (err.error || 'unavailable') + '</option>';
      return;
    }
    const data = await resp.json();
    gcpVoicesCache[languageCode] = data.voices || [];
    gcpServiceAccountConfigured = !!data.service_account_configured;
    renderGCPVoiceOptions(gcpVoicesCache[languageCode]);
  } catch (e) {
    voiceSelect.innerHTML = '<option value="">Failed to load voices</option>';
  }
}

function renderGCPVoiceOptions(voices) {
  const voiceSelect = document.getElementById('gcp-voice-select');
  if (!voices || voices.length === 0) {
    voiceSelect.innerHTML = '<option value="">No voices available</option>';
    return;
  }

  const genderIcon = { MALE: '♂', FEMALE: '♀', NEUTRAL: '⚬' };
  // Sort: premium voices first (they're the headline feature when SA is configured),
  // then alphabetical by name.
  const sorted = [...voices].sort((a, b) => {
    const ap = a.premium ? 0 : 1;
    const bp = b.premium ? 0 : 1;
    if (ap !== bp) return ap - bp;
    return a.name.localeCompare(b.name);
  });

  voiceSelect.innerHTML = '<option value="">— Select a voice —</option>' +
    sorted.map(v => {
      const icon = genderIcon[v.ssmlGender] || '';
      const cls = classifyGCPVoice(v.name);
      const star = v.premium ? '★ ' : '';
      return '<option value="' + v.name + '">' + star + icon + ' ' + v.name + ' (' + cls.label + ')</option>';
    }).join('');
}

function setGCPVoice(voiceName) {
  state.gcp_voice_name = voiceName;
  const infoEl = document.getElementById('gcp-voice-info');
  const warnEl = document.getElementById('gcp-premium-warning');

  if (!voiceName) {
    infoEl.style.display = 'none';
    if (warnEl) warnEl.classList.remove('show');
    return;
  }

  const voices = gcpVoicesCache[state.gcp_language_code] || [];
  const voice = voices.find(v => v.name === voiceName);
  if (voice) {
    const cls = classifyGCPVoice(voice.name);
    const premiumBadge = voice.premium
      ? '<span class="gcp-voice-badge gcp-voice-badge-premium" title="Premium voice — requires Google Cloud service account">★ Premium</span> '
      : '';
    infoEl.innerHTML = premiumBadge +
      '<span class="gcp-voice-badge">' + voice.ssmlGender + '</span> ' +
      '<span class="gcp-voice-badge">' + cls.label + '</span> ' +
      '<span class="gcp-voice-badge">' + voice.naturalSampleRateHertz + ' Hz</span>';
    infoEl.style.display = 'flex';

    // Premium voices only render when SA auth is configured server-side, but
    // belt-and-suspenders: if a stale cached selection is somehow premium and
    // SA is not configured, surface the warning so the user picks another.
    if (warnEl) {
      if (voice.premium && !gcpServiceAccountConfigured) {
        warnEl.classList.add('show');
      } else {
        warnEl.classList.remove('show');
      }
    }
  }

  renderGCPTTSPreview();
}

function renderGCPTTSPreview() {
  const container = document.getElementById('gcp-tts-preview-container');
  if (!state.pre_generated_script || !state.gcp_voice_name) {
    container.style.display = 'none';
    return;
  }

  const segments = state.pre_generated_script.segments;
  container.style.display = 'block';
  container.innerHTML = '<div class="gcp-preview-title">Preview Segments</div>' +
    segments.map(seg => {
      const truncText = seg.text.length > 80 ? seg.text.substring(0, 80) + '...' : seg.text;
      return '<div class="gcp-preview-row">' +
        '<span class="gcp-seg-label">Seg ' + seg.segment_id + '</span>' +
        '<span class="gcp-seg-text">' + truncText + '</span>' +
        '<button class="btn gcp-play-btn" onclick="previewGCPTTS(' + seg.segment_id + ')" id="gcp-play-' + seg.segment_id + '">▶ Play</button>' +
        '</div>';
    }).join('');
}

async function previewGCPTTS(segmentId) {
  if (!state.gcp_voice_name || !state.gcp_language_code) {
    alert('Please select a language and voice first.');
    return;
  }

  const segment = state.pre_generated_script.segments.find(s => s.segment_id === segmentId);
  if (!segment) return;

  const btn = document.getElementById('gcp-play-' + segmentId);
  const originalText = btn.textContent;
  btn.textContent = '⏳...';
  btn.disabled = true;

  try {
    const resp = await fetch(API + '/api/gcp-tts/synthesize', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        text: segment.text,
        voice_name: state.gcp_voice_name,
        language_code: state.gcp_language_code
      })
    });

    if (!resp.ok) {
      const err = await resp.json();
      alert('TTS Error: ' + (err.error || 'Failed to synthesize'));
      return;
    }

    const data = await resp.json();
    const audio = new Audio('data:audio/mpeg;base64,' + data.audio_base64);
    audio.play();
    btn.textContent = '🔊 Playing';
    audio.onended = () => { btn.textContent = originalText; btn.disabled = false; };
  } catch (e) {
    alert('Preview failed: ' + e.message);
  } finally {
    if (btn.textContent === '⏳...') {
      btn.textContent = originalText;
      btn.disabled = false;
    }
  }
}

// ---- Video ----
function setVideoMode(mode) {
  state.video_mode = mode;
  const panel = document.querySelector('.wizard-panel[data-panel="3"]');
  const btns = panel.querySelector('.toggle-group').children;
  btns[0].classList.toggle('active', mode === 'auto');
  btns[1].classList.toggle('active', mode === 'manual');
  document.getElementById('style-chips').style.display = mode === 'auto' ? '' : 'none';
  document.getElementById('media-counts').style.display = mode === 'auto' ? '' : 'none';
}

// ---- Visual pacing & AI mix ----
const PACING_PRESETS = {
  cinematic: 10,
  balanced: 6,
  energetic: 4,
};

function setPacing(preset) {
  document.querySelectorAll('#pacing-chips .hint').forEach(c => {
    c.classList.toggle('active', c.dataset.pacing === preset);
  });
  const customGroup = document.getElementById('pacing-custom-group');
  if (preset === 'custom') {
    customGroup.style.display = '';
    onPacingSliderChange(state.seconds_per_visual || 6);
  } else {
    customGroup.style.display = 'none';
    state.seconds_per_visual = PACING_PRESETS[preset];
    document.getElementById('pacing-slider').value = state.seconds_per_visual;
    document.getElementById('pacing-val').textContent = state.seconds_per_visual;
  }
  // Derive video_style from AI mix so backend defaults still make sense.
  syncVideoStyleFromMix();
  updateVisualsPreview();
}

function onPacingSliderChange(value) {
  state.seconds_per_visual = value;
  document.getElementById('pacing-val').textContent = value;
  updateVisualsPreview();
}

function onAIMixSliderChange(value) {
  state.ai_image_percent = value;
  document.getElementById('ai-mix-val').textContent = value;
  document.getElementById('stock-mix-val').textContent = 100 - value;
  syncVideoStyleFromMix();
  updateVisualsPreview();
}

function syncVideoStyleFromMix() {
  if (state.ai_image_percent >= 90) state.video_style = 'ai_images';
  else if (state.ai_image_percent <= 10) state.video_style = 'stock';
  else state.video_style = 'mixed';
}

function updateVisualsPreview() {
  const el = document.getElementById('visuals-preview');
  if (!el) return;
  const isShort = state.format === 'short';
  const estDurationSec = isShort ? 50 : (state.duration_min || 8) * 60;
  const sPerVisual = state.seconds_per_visual || 6;
  let total = Math.max(3, Math.ceil(estDurationSec / sPerVisual));
  const aiCount = Math.round(total * state.ai_image_percent / 100);
  const clipCount = total - aiCount;

  let mixSuffix = '';
  if (state.ai_image_percent === 0) mixSuffix = ` (${clipCount} stock clips)`;
  else if (state.ai_image_percent === 100) mixSuffix = ` (${aiCount} AI images)`;
  else mixSuffix = ` (${clipCount} clips + ${aiCount} AI images)`;

  const mins = isShort ? '~50s' : `${state.duration_min || 8}-min`;
  el.textContent = `≈ ${total} visuals across your ${mins} video — one new shot every ${sPerVisual} seconds.${mixSuffix}`;
}

// ---- Music ----
function setMusicMode(mode) {
  state.music_mode = mode;
  const panel = document.querySelector('.wizard-panel[data-panel="4"]');
  const btns = panel.querySelector('.toggle-group').children;
  btns[0].classList.toggle('active', mode === 'auto');
  btns[1].classList.toggle('active', mode === 'manual');
  btns[2].classList.toggle('active', mode === 'skip');
  document.getElementById('auto-music-genre').style.display = mode === 'auto' ? 'block' : 'none';
  document.getElementById('manual-music-section').style.display = mode === 'manual' ? 'block' : 'none';
}

async function searchJamendo() {
  const q = document.getElementById('jamendo-search').value.trim();
  const resDiv = document.getElementById('jamendo-results');
  if (!q) return;

  resDiv.innerHTML = '<div style="color:var(--text-muted);font-size:13px;">Searching...</div>';
  try {
    const resp = await fetch(`/api/music/jamendo/search?q=${encodeURIComponent(q)}`);
    const data = await resp.json();
    
    if (data.tracks && data.tracks.length > 0) {
      resDiv.innerHTML = data.tracks.map(t => `
        <div class="track-item">
          <div class="track-info">
            ${t.name} <span class="track-artist">${t.artist}</span>
          </div>
          <button class="btn btn-primary premium-btn" style="padding: 6px 12px; font-size: 11px;" onclick="selectJamendoTrack('${t.download_url}', '${t.stream_url}', '${t.name.replace(/'/g, "\\'")}', ${t.duration})">Select</button>
        </div>
      `).join('');
    } else {
      resDiv.innerHTML = '<div style="color:var(--text-muted);font-size:13px;">No tracks found.</div>';
    }
  } catch(e) {
    resDiv.innerHTML = '<div style="color:#ff6b6b;font-size:13px;">Error searching Jamendo.</div>';
  }
}

function selectJamendoTrack(downloadUrl, streamUrl, name, duration) {
  state.music_url = downloadUrl;
  state.music_start = 0;
  state.music_end = duration;

  document.getElementById('selected-track-name').textContent = name;
  const audio = document.getElementById('jamendo-audio');
  audio.src = streamUrl;
  
  const startSlider = document.getElementById('music-start-slider');
  const endSlider = document.getElementById('music-end-slider');
  
  startSlider.max = duration;
  startSlider.value = 0;
  
  endSlider.max = duration;
  endSlider.value = duration;
  
  document.getElementById('music-start-val').textContent = '0';
  document.getElementById('music-end-val').textContent = duration;
  
  document.getElementById('selected-track-ui').style.display = 'block';
}

function updateMusicCrop(changedId) {
  const startSlider = document.getElementById('music-start-slider');
  const endSlider = document.getElementById('music-end-slider');
  
  let start = parseInt(startSlider.value);
  let end = parseInt(endSlider.value);
  
  if (start >= end) {
    if (changedId === 'music-start-slider') start = end - 1;
    else end = start + 1;
    startSlider.value = start;
    endSlider.value = end;
  }
  
  state.music_start = start;
  state.music_end = end;
  document.getElementById('music-start-val').textContent = start;
  document.getElementById('music-end-val').textContent = end;
  
  const audio = document.getElementById('jamendo-audio');
  if (Math.abs(audio.currentTime - start) > 1) {
    audio.currentTime = start;
  }
}

// ---- Tone ----
const toneToMusicDesc = {
  dramatic:       'cinematic dramatic epic',
  suspenseful:    'suspense dark mysterious thriller',
  educational:    'calm ambient relaxing background',
  conversational: 'acoustic light happy positive',
  motivational:   'uplifting motivational energetic',
  humorous:       'fun playful comedy upbeat',
};

function setTone(tone) {
  state.script_tone = tone;
  document.querySelectorAll('.tone-chip').forEach(c => {
    c.classList.toggle('active', c.onclick.toString().includes("'" + tone + "'"));
  });
  const descEl = document.getElementById('auto-music-desc');
  if (descEl) {
    descEl.innerHTML = 'Auto music will match: <strong>' + (toneToMusicDesc[tone] || tone) + '</strong>';
  }
}

// ---- Create Job ----
async function createJob() {
  if (!state.raw_input.trim()) {
    alert('Please enter a topic, category, or event description.');
    return;
  }

  if (!state.pre_generated_script) {
    alert('Please generate and approve the script first before creating the video.');
    return;
  }

  // Validate manual recordings if applicable
  if (state.voiceover_mode === 'manual') {
    const segs = state.pre_generated_script.segments;
    for (let i = 0; i < segs.length; i++) {
      if (!state.manual_audio_base64[segs[i].segment_id]) {
        alert('Missing recording for Segment ' + segs[i].segment_id + '. Please record all segments.');
        return;
      }
    }
  }

  if (state.voiceover_mode === 'gcp_tts') {
    if (!state.gcp_voice_name || !state.gcp_language_code) {
      alert('Please select a language and voice for Google Cloud TTS.');
      return;
    }
    const voices = gcpVoicesCache[state.gcp_language_code] || [];
    const v = voices.find(x => x.name === state.gcp_voice_name);
    if (v && v.premium && !gcpServiceAccountConfigured) {
      alert('Premium voice "' + state.gcp_voice_name + '" requires a Google Cloud service account. Add GOOGLE_APPLICATION_CREDENTIALS_JSON to .env (and restart) or pick a non-premium voice.');
      return;
    }
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

// ---- Trim Controls ----
let videoDuration = 0;
let trimEndTime = 0;

function initTrimControls() {
  const video = document.getElementById('preview-video');
  video.addEventListener('loadedmetadata', function() {
    videoDuration = video.duration;
    trimEndTime = videoDuration;
    const slider = document.getElementById('trim-slider');
    slider.max = videoDuration;
    slider.value = videoDuration;
    slider.step = 0.1;
    document.getElementById('trim-max-label').textContent = formatTime(videoDuration);
    document.getElementById('trim-time-display').textContent = formatTime(videoDuration);
  });
}

function onTrimSliderChange(value) {
  trimEndTime = parseFloat(value);
  document.getElementById('trim-time-display').textContent = formatTime(trimEndTime);
}

function previewTrimPoint() {
  const video = document.getElementById('preview-video');
  // Seek to 2 seconds before the trim point to give context
  const seekTo = Math.max(0, trimEndTime - 2);
  video.currentTime = seekTo;
  video.play();

  // Pause at the trim point
  const checkPause = setInterval(() => {
    if (video.currentTime >= trimEndTime) {
      video.pause();
      clearInterval(checkPause);
    }
  }, 50);
}

async function applyTrim() {
  if (!currentJobId) return;
  if (trimEndTime <= 0) {
    alert('Please set a valid trim point.');
    return;
  }
  if (trimEndTime >= videoDuration - 0.5) {
    alert('Trim point is at or near the end of the video. No trimming needed.');
    return;
  }

  const btn = document.getElementById('btn-apply-trim');
  btn.textContent = '⏳ Trimming...';
  btn.disabled = true;

  try {
    const resp = await fetch(`${API}/api/jobs/${currentJobId}/trim`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ end_time: trimEndTime })
    });

    if (!resp.ok) {
      const err = await resp.json();
      alert('Trim failed: ' + (err.error || 'Unknown error'));
      return;
    }

    // Reload the video to show the trimmed version
    const video = document.getElementById('preview-video');
    const src = video.src;
    video.src = '';
    video.src = src + '?t=' + Date.now();
    video.load();

    btn.textContent = '✅ Trimmed!';
    setTimeout(() => {
      btn.textContent = '✂️ Apply Trim';
      btn.disabled = false;
    }, 2000);
  } catch (e) {
    alert('Trim request failed: ' + e.message);
  } finally {
    if (btn.textContent === '⏳ Trimming...') {
      btn.textContent = '✂️ Apply Trim';
      btn.disabled = false;
    }
  }
}

function formatTime(seconds) {
  const m = Math.floor(seconds / 60);
  const s = Math.floor(seconds % 60);
  const ms = Math.floor((seconds % 1) * 10);
  return m + ':' + String(s).padStart(2, '0') + '.' + ms;
}

// Initialize trim controls when the video loads
document.addEventListener('DOMContentLoaded', initTrimControls);
document.addEventListener('DOMContentLoaded', () => {
  if (typeof updateVisualsPreview === 'function') updateVisualsPreview();
});

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

// ==========================================
// SCRIPT EDITOR CHAT & VOICEOVER LOGIC
// ==========================================

let scriptChatHistory = [];
let currentDraftScript = null;

async function generateScript() {
  if (!state.raw_input.trim()) {
    alert('Please enter a topic, category, or event description first.');
    return;
  }

  const btn = document.getElementById('btn-gen-script');
  btn.disabled = true;
  btn.innerHTML = '<span class="loading-dots">Generating</span>';
  
  // Clear previous state
  scriptChatHistory = [];
  currentDraftScript = null;
  document.getElementById('script-chat-container').style.display = 'none';
  document.getElementById('recording-script-container').style.display = 'none';

  try {
    const res = await fetch(API + '/api/preview-script', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(state)
    });

    if (!res.ok) {
      const errData = await res.json().catch(() => ({}));
      throw new Error(errData.error || 'Failed to generate script');
    }
    const script = await res.json();

    // For 'short' format, segments live in short_version.segments; normalize to top-level
    if ((!script.segments || script.segments.length === 0) && script.short_version && script.short_version.segments) {
      script.segments = script.short_version.segments;
    }

    if (!script.segments || script.segments.length === 0) {
      throw new Error('Script was generated but contains no segments. Please try again.');
    }

    currentDraftScript = script;
    
    // Add initial AI message
    scriptChatHistory.push({
      role: 'ai',
      text: 'Here is your initial script draft based on your topic. You can review it below and ask me to make any changes (e.g. "make the intro more mysterious", "add a segment about X").',
      script: currentDraftScript
    });

    renderChat();
    document.getElementById('script-chat-container').style.display = 'block';

  } catch (err) {
    console.error(err);
    alert('Error generating script: ' + err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = '📝 Regenerate Script';
  }
}

function renderScriptPreview(script) {
  if (!script || !script.segments) return '';
  
  const segmentsHtml = script.segments.map(seg => `
    <div class="script-preview-segment">
      <div class="script-preview-header">
        <span class="script-preview-id">Segment ${seg.segment_id} (${seg.type})</span>
        <span class="script-preview-dur">~${seg.duration_sec}s</span>
      </div>
      <div class="script-preview-text">${seg.text}</div>
    </div>
  `).join('');
  
  return `<div class="script-preview-box">${segmentsHtml}</div>`;
}

function renderChat() {
  const historyDiv = document.getElementById('chat-history');
  
  historyDiv.innerHTML = scriptChatHistory.map(msg => `
    <div class="chat-message ${msg.role}">
      <div class="chat-sender">${msg.role === 'ai' ? '🤖 Script AI' : '👤 You'}</div>
      <div class="chat-bubble">
        ${msg.text}
        ${msg.script ? renderScriptPreview(msg.script) : ''}
      </div>
    </div>
  `).join('');
  
  // Scroll to bottom
  historyDiv.scrollTop = historyDiv.scrollHeight;
}

async function submitScriptRefinement() {
  const inputEl = document.getElementById('chat-input');
  const prompt = inputEl.value.trim();
  if (!prompt || !currentDraftScript) return;
  
  const btn = document.getElementById('btn-send-refinement');
  const approveBtn = document.getElementById('btn-approve-script');
  
  // Add user message to UI
  scriptChatHistory.push({ role: 'user', text: prompt });
  inputEl.value = '';
  renderChat();
  
  // Disable inputs while refining
  inputEl.disabled = true;
  btn.disabled = true;
  approveBtn.disabled = true;
  btn.innerHTML = '<span class="loading-dots"></span>';
  
  try {
    const payload = {
      current_script: currentDraftScript,
      user_prompt: prompt,
      raw_input: state.raw_input,
      format: state.format,
      duration_min: state.duration_min,
      script_tone: state.script_tone,
      language: state.language,
      clip_count: state.clip_count,
      image_count: state.image_count
    };

    const res = await fetch(API + '/api/refine-script', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });

    if (!res.ok) {
      const errData = await res.json().catch(() => ({}));
      throw new Error(errData.error || 'Failed to refine script');
    }
    
    const updatedScript = await res.json();
    currentDraftScript = updatedScript;
    
    // Add AI response
    scriptChatHistory.push({
      role: 'ai',
      text: 'I have updated the script based on your feedback.',
      script: currentDraftScript
    });
    
  } catch (err) {
    console.error(err);
    scriptChatHistory.push({
      role: 'ai',
      text: '⚠️ Error updating script: ' + err.message
    });
  } finally {
    renderChat();
    inputEl.disabled = false;
    btn.disabled = false;
    approveBtn.disabled = false;
    btn.textContent = 'Send';
    inputEl.focus();
  }
}

function approveScript() {
  if (!currentDraftScript) return;
  
  // Lock in the script
  state.pre_generated_script = currentDraftScript;
  
  // Hide chat container
  document.getElementById('script-chat-container').style.display = 'none';
  document.getElementById('btn-gen-script').style.display = 'none';
  
  if (state.voiceover_mode === 'manual') {
    renderRecordingControls();
  } else {
    const container = document.getElementById('recording-script-container');
    container.style.display = 'block';
    let modeLabel, modeDesc;
    if (state.voiceover_mode === 'gcp_tts') {
      modeLabel = 'Google Cloud TTS';
      modeDesc = `${modeLabel} will generate voiceover using the selected voice. You can now proceed to generate the video.`;
    } else if (state.voiceover_mode === 'none') {
      modeLabel = 'Music-only video';
      modeDesc = 'No narration will be generated. The script is locked in to plan visuals and segment timing. Pick a music track in the next step (or "No music" for a silent video).';
    } else {
      modeLabel = 'ElevenLabs AI';
      modeDesc = `${modeLabel} will generate voiceover using the selected voice. You can now proceed to generate the video.`;
    }
    container.innerHTML = `
      <div style="padding:16px; background: rgba(52,211,153,0.1); border: 1px solid rgba(52,211,153,0.2); border-radius: 12px; text-align: center;">
        <div style="font-size:24px; margin-bottom:8px;">✅</div>
        <div style="font-weight:600; color:var(--success); margin-bottom:4px;">Script Approved!</div>
        <div style="font-size:13px; color:var(--text-secondary);">${modeDesc}</div>
        <button class="btn btn-primary premium-btn" style="margin-top:12px; padding:6px 16px; font-size:12px;" onclick="resetScript()">Edit Script</button>
      </div>
    `;

    if (state.voiceover_mode === 'gcp_tts') {
      renderGCPTTSPreview();
    }
  }
}

function resetScript() {
  state.pre_generated_script = null;
  document.getElementById('recording-script-container').style.display = 'none';
  document.getElementById('btn-gen-script').style.display = 'block';
  document.getElementById('script-chat-container').style.display = 'block';
}

function renderRecordingControls() {
  const container = document.getElementById('recording-script-container');
  const script = state.pre_generated_script;
  
  const toneDirections = {
    dramatic:       '🎭 Deliver with dramatic weight — slow, intense pauses, build tension',
    educational:    '📚 Deliver clearly and calmly — like explaining to a curious friend',
    conversational: '💬 Deliver casually — relaxed, natural rhythm, like talking to a friend',
    suspenseful:    '😨 Deliver with rising tension — whisper-to-loud shifts, nervous energy',
    motivational:   '🔥 Deliver with conviction — powerful, uplifting, punchy emphasis',
    humorous:       '😂 Deliver with playful energy — comedic timing, light sarcasm allowed'
  };
  const direction = toneDirections[state.script_tone] || 'Deliver naturally';

  container.style.display = 'block';
  container.innerHTML = `
    <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:12px;">
      <div style="font-size:14px; font-weight:600; color:var(--success);">✅ Script Approved</div>
      <button class="btn btn-primary premium-btn" style="padding:4px 12px; font-size:11px;" onclick="resetScript()">Edit Script</button>
    </div>
    <div class="recording-direction" style="margin-bottom:16px; padding:12px 16px; background: rgba(124,92,252,0.1); border-left: 3px solid var(--accent); border-radius: 0 8px 8px 0;">
      <div style="font-size:11px; text-transform:uppercase; letter-spacing:1.5px; color:var(--accent); margin-bottom:4px; font-weight:700;">Delivery Direction</div>
      <div style="font-size:14px; color:#fff;">${direction}</div>
    </div>
  ` + script.segments.map(seg => `
    <div class="recording-segment" style="display:block; margin-bottom:14px; padding:16px; background: rgba(0,0,0,0.3); border:1px solid rgba(255,255,255,0.06); border-radius:12px;">
      <div style="display:flex; justify-content:space-between; align-items:center; margin-bottom:10px;">
        <strong style="color:var(--accent-2); font-size:13px;">Segment ${seg.segment_id}</strong>
        <span class="track-badge">${seg.duration_sec || '—'}s</span>
      </div>
      <p style="font-size:14px; line-height:1.7; margin-bottom:14px; color:#e0e0e0; font-family: 'Inter', sans-serif;">${seg.text}</p>
      <div style="display:flex; align-items:center; gap:8px; flex-wrap:wrap;">
        <button class="premium-btn" id="btn-rec-${seg.segment_id}" style="padding:7px 18px; font-size:12px; background:var(--danger); border:none; border-radius:8px; color:#fff; cursor:pointer;" onclick="startRecording(${seg.segment_id})">🔴 Record</button>
        <button class="premium-btn hidden" id="btn-stop-${seg.segment_id}" style="padding:7px 18px; font-size:12px; background:var(--accent); border:none; border-radius:8px; color:#fff; cursor:pointer;" onclick="stopRecording(${seg.segment_id})">⏹ Stop</button>
        <button class="premium-btn hidden" id="btn-play-${seg.segment_id}" style="padding:7px 18px; font-size:12px; background:var(--success); border:none; border-radius:8px; color:#fff; cursor:pointer;" onclick="playRecording(${seg.segment_id})">▶ Play</button>
        <span id="rec-status-${seg.segment_id}" style="color:var(--text-secondary); font-size:12px; display:flex; align-items:center; margin-left:4px;"></span>
      </div>
    </div>
  `).join('');
}

async function startRecording(segId) {
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const mediaRecorder = new MediaRecorder(stream);
    mediaRecorders[segId] = mediaRecorder;
    audioChunks[segId] = [];

    mediaRecorder.ondataavailable = e => {
      if (e.data.size > 0) audioChunks[segId].push(e.data);
    };

    mediaRecorder.onstop = () => {
      const audioBlob = new Blob(audioChunks[segId], { type: 'audio/webm' });
      const reader = new FileReader();
      reader.readAsDataURL(audioBlob);
      reader.onloadend = () => {
        state.manual_audio_base64[segId] = reader.result;
        document.getElementById('rec-status-' + segId).textContent = '✅ Recorded';
        document.getElementById('rec-status-' + segId).style.color = 'var(--success)';
      };
      // Release microphone
      stream.getTracks().forEach(track => track.stop());
    };

    mediaRecorder.start();

    document.getElementById('btn-rec-' + segId).classList.add('hidden');
    document.getElementById('btn-stop-' + segId).classList.remove('hidden');
    document.getElementById('btn-play-' + segId).classList.add('hidden');
    document.getElementById('rec-status-' + segId).textContent = '⏺ Recording...';
    document.getElementById('rec-status-' + segId).style.color = 'var(--danger)';

  } catch (err) {
    console.error(err);
    alert('Microphone access denied or unavailable.');
  }
}

function stopRecording(segId) {
  if (mediaRecorders[segId]) {
    mediaRecorders[segId].stop();
    document.getElementById('btn-stop-' + segId).classList.add('hidden');
    document.getElementById('btn-rec-' + segId).classList.remove('hidden');
    document.getElementById('btn-rec-' + segId).textContent = '🔴 Re-Record';
    document.getElementById('btn-play-' + segId).classList.remove('hidden');
  }
}

function playRecording(segId) {
  if (state.manual_audio_base64[segId]) {
    const audio = new Audio(state.manual_audio_base64[segId]);
    audio.play();
  }
}

// ---- Init ----
renderHints('category');

