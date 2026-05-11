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
  music_file_base64: '',
  music_start: 0,
  music_end: 0,
  music_preset: 'peaceful_aesthetic',
  music_prompt: '',
  music_provider: 'auto',
  music_ambience: [],
  ai_music_audio_base64: '',
  ai_music_start: 0,
  ai_music_end: 0,
  pre_generated_script: null,
  manual_audio_base64: {},
  gcp_voice_name: '',
  gcp_language_code: '',
  skip_visual_review: false
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
      if (state.music_mode === 'manual' && !state.music_url && !state.music_file_base64) {
        return 'Please pick a Jamendo track, paste a direct URL, or upload an audio file (or switch to Auto Music / No Music).';
      }
      break;
  }
  return null;
}

function musicSummary() {
  const m = state.music_mode;
  if (m === 'auto') return 'auto';
  if (m === 'skip') return 'no music';
  if (m === 'manual') {
    if (state.music_file_base64) return 'manual (uploaded file)';
    if (state.music_url) return 'manual (track selected)';
    return 'manual';
  }
  if (m === 'ai_generated') {
    const presetLabel = state.music_preset && state.music_preset !== 'custom' ? state.music_preset.replace(/_/g, ' ') : 'custom prompt';
    const ambience = state.music_ambience.length > 0 ? ' + ' + state.music_ambience.join(', ') : '';
    return 'AI — ' + presetLabel + ambience;
  }
  return m;
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
    ['Music', musicSummary()],
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

  if (mode === 'gcp_tts') {
    // Either show the empty hint or kick off a recommendation fetch.
    maybeLoadVoiceRecommendation();
  } else {
    // Hide both panels when not on GCP TTS so they don't reappear with stale
    // data when the user toggles back later (the next open will re-fetch).
    const banner = document.getElementById('gcp-voice-rec-banner');
    if (banner) banner.style.display = 'none';
    const empty = document.getElementById('gcp-voice-rec-empty');
    if (empty) empty.style.display = 'none';
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

  // Recompute the voice recommendation against the new language (different
  // catalog → different ranked picks). The cache is keyed on language so
  // switching back doesn't re-fetch.
  if (state.voiceover_mode === 'gcp_tts') {
    maybeLoadVoiceRecommendation();
  }

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

// ---------------------------------------------------------------------------
// Voice recommendation (GCP TTS)
//
// Cache: keyed on (scriptHash + language). The recommendation depends on the
// current script + selected GCP language. When either changes (user refines
// the script, picks a different language), we re-fetch on the next open.
// ---------------------------------------------------------------------------

const voiceRecCache = {};       // key -> rec object
let voiceRecDismissed = false;  // user dismissed banner for this session

function currentScriptDocument() {
  // currentDraftScript holds the unsaved draft while the user iterates;
  // state.pre_generated_script is set after they click "Approve Script".
  // Either is a valid input to the recommender.
  return currentDraftScript || state.pre_generated_script || null;
}

function scriptHashShort(script) {
  if (!script) return '';
  // Cheap hash: concatenate ids + truncated text of the first few segments.
  // Avoids re-fetching on identical scripts but invalidates on edits.
  let s = (script.hook || '') + '|';
  (script.segments || []).slice(0, 4).forEach(seg => {
    s += seg.segment_id + ':' + (seg.text || '').slice(0, 60) + '|';
  });
  let h = 0;
  for (let i = 0; i < s.length; i++) h = ((h << 5) - h + s.charCodeAt(i)) | 0;
  return String(h);
}

function maybeLoadVoiceRecommendation() {
  const script = currentScriptDocument();
  const banner = document.getElementById('gcp-voice-rec-banner');
  const empty = document.getElementById('gcp-voice-rec-empty');
  if (!banner || !empty) return;

  if (!script) {
    banner.style.display = 'none';
    empty.style.display = 'block';
    return;
  }
  empty.style.display = 'none';
  if (voiceRecDismissed) {
    banner.style.display = 'none';
    return;
  }
  loadVoiceRecommendation(script);
}

async function loadVoiceRecommendation(script) {
  const lang = state.gcp_language_code || 'en-US';
  const cacheKey = scriptHashShort(script) + '|' + lang;
  const banner = document.getElementById('gcp-voice-rec-banner');
  if (!banner) return;

  if (voiceRecCache[cacheKey]) {
    renderVoiceRecommendation(voiceRecCache[cacheKey]);
    return;
  }

  // Show a loading skeleton so the panel isn't blank during the fetch.
  banner.style.display = 'block';
  document.getElementById('gcp-rec-character').textContent = 'Finding the right voice…';
  document.getElementById('gcp-rec-gender').textContent = '';
  document.getElementById('gcp-rec-voices').innerHTML = '<div class="rec-loading">Analysing your script…</div>';
  document.getElementById('gcp-rec-reason').textContent = '';
  document.getElementById('gcp-rec-note').style.display = 'none';

  try {
    const res = await fetch(API + '/api/tts/recommend-voice', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ script, tone: state.script_tone, language: lang }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      console.warn('Voice recommendation failed:', err.error || res.statusText);
      banner.style.display = 'none';
      return;
    }
    const rec = await res.json();
    voiceRecCache[cacheKey] = rec;
    renderVoiceRecommendation(rec);
  } catch (err) {
    console.warn('Voice recommendation failed:', err);
    banner.style.display = 'none';
  }
}

function renderVoiceRecommendation(rec) {
  if (!rec || !rec.voices || rec.voices.length === 0) {
    const banner = document.getElementById('gcp-voice-rec-banner');
    if (banner) banner.style.display = 'none';
    return;
  }
  document.getElementById('gcp-voice-rec-banner').style.display = 'block';
  document.getElementById('gcp-rec-character').textContent = rec.character || 'Recommended voice';
  document.getElementById('gcp-rec-gender').textContent = genderLabel(rec.gender_hint);

  const chipsEl = document.getElementById('gcp-rec-voices');
  chipsEl.innerHTML = rec.voices.map((v, i) => {
    const family = v.family ? `<span class="rec-chip-meta">${escapeHTML(v.family)}</span>` : '';
    const gender = v.gender ? `<span class="rec-chip-meta">${escapeHTML(genderLabel(v.gender))}</span>` : '';
    const star = i === 0 ? '<span class="rec-chip-star">★</span>' : '';
    return `<button type="button" class="rec-chip rec-chip-voice" onclick="applyVoiceRecommendation(${i})" title="${escapeAttr(v.reason || '')}">
      ${star}<span class="rec-chip-name">${escapeHTML(v.name)}</span>${gender}${family}
    </button>`;
  }).join('');

  document.getElementById('gcp-rec-reason').textContent = rec.reason || '';
  const noteEl = document.getElementById('gcp-rec-note');
  if (rec.note) {
    noteEl.textContent = rec.note;
    noteEl.style.display = 'block';
  } else {
    noteEl.style.display = 'none';
  }
}

function genderLabel(g) {
  if (!g) return '';
  const lower = String(g).toLowerCase();
  if (lower === 'male' || lower === 'female') return lower.charAt(0).toUpperCase() + lower.slice(1) + ' voice';
  if (lower === 'either') return 'Either gender';
  if (lower === 'neutral') return 'Neutral';
  return g;
}

function applyVoiceRecommendation(idx) {
  const lang = state.gcp_language_code || 'en-US';
  const cacheKey = scriptHashShort(currentScriptDocument()) + '|' + lang;
  const rec = voiceRecCache[cacheKey];
  if (!rec || !rec.voices || !rec.voices[idx]) return;
  const voiceName = rec.voices[idx].name;
  const select = document.getElementById('gcp-voice-select');
  if (select) {
    select.value = voiceName;
    if (select.value === voiceName) {
      setGCPVoice(voiceName);
    } else {
      // Voice catalog may not be loaded yet — apply once the dropdown is
      // populated. loadGCPVoices() resolves the cache synchronously when
      // a hit, otherwise we wait one tick and try again.
      setTimeout(() => {
        select.value = voiceName;
        if (select.value === voiceName) setGCPVoice(voiceName);
      }, 300);
    }
  }
  // Highlight the picked chip
  document.querySelectorAll('#gcp-rec-voices .rec-chip').forEach((c, i) => {
    c.classList.toggle('rec-chip-applied', i === idx);
  });
}

function dismissVoiceRecommendation() {
  voiceRecDismissed = true;
  const banner = document.getElementById('gcp-voice-rec-banner');
  if (banner) banner.style.display = 'none';
}

// ---------------------------------------------------------------------------
// Music recommendation (manual Jamendo search)
// ---------------------------------------------------------------------------

const musicRecCache = {};       // scriptHash + tone -> rec object
let musicRecDismissed = false;
let musicRecActiveQuery = '';   // currently-applied chip, for highlighting

function maybeLoadMusicRecommendation() {
  const script = currentScriptDocument();
  const banner = document.getElementById('music-rec-banner');
  const empty = document.getElementById('music-rec-empty');
  if (!banner || !empty) return;

  if (!script) {
    banner.style.display = 'none';
    empty.style.display = 'block';
    return;
  }
  empty.style.display = 'none';
  if (musicRecDismissed) {
    banner.style.display = 'none';
    return;
  }
  loadMusicRecommendation(script);
}

async function loadMusicRecommendation(script) {
  const cacheKey = scriptHashShort(script) + '|' + (state.script_tone || '');
  const banner = document.getElementById('music-rec-banner');
  if (!banner) return;

  if (musicRecCache[cacheKey]) {
    renderMusicRecommendation(musicRecCache[cacheKey], /*autoSearchFirst=*/false);
    return;
  }

  banner.style.display = 'block';
  document.getElementById('music-rec-mood-summary').textContent = 'Picking music ideas…';
  document.getElementById('music-rec-chips').innerHTML = '<div class="rec-loading">Analysing your script…</div>';
  const explainWrap = document.getElementById('music-rec-explain-wrap');
  if (explainWrap) explainWrap.style.display = 'none';

  try {
    const res = await fetch(API + '/api/music/recommend-search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ script, tone: state.script_tone }),
    });
    if (!res.ok) {
      console.warn('Music recommendation failed:', res.statusText);
      banner.style.display = 'none';
      return;
    }
    const rec = await res.json();
    musicRecCache[cacheKey] = rec;
    renderMusicRecommendation(rec, /*autoSearchFirst=*/true);
  } catch (err) {
    console.warn('Music recommendation failed:', err);
    banner.style.display = 'none';
  }
}

function renderMusicRecommendation(rec, autoSearchFirst) {
  if (!rec || !rec.queries || rec.queries.length === 0) {
    const banner = document.getElementById('music-rec-banner');
    if (banner) banner.style.display = 'none';
    return;
  }
  document.getElementById('music-rec-banner').style.display = 'block';

  // Mood summary line
  const dominant = rec.dominant_mood ? `<strong>${escapeHTML(rec.dominant_mood)}</strong>` : '';
  const secondary = rec.secondary_mood ? ` · also ${escapeHTML(rec.secondary_mood)}` : '';
  const variance = rec.mood_variance ? ' · <em>mixed moods</em>' : '';
  const tone = state.script_tone ? ` · tone: ${escapeHTML(state.script_tone)}` : '';
  document.getElementById('music-rec-mood-summary').innerHTML =
    dominant ? `Mood: ${dominant}${secondary}${variance}${tone}` : (tone || 'Suggested music searches');

  // Chips for each query
  const chipsEl = document.getElementById('music-rec-chips');
  chipsEl.innerHTML = rec.queries.map((q, i) => {
    const star = i === 0 ? '<span class="rec-chip-star">★</span>' : '';
    const active = q === musicRecActiveQuery ? ' rec-chip-applied' : '';
    return `<button type="button" class="rec-chip rec-chip-query${active}" onclick="applyMusicSearch(${i})">
      ${star}<span class="rec-chip-name">${escapeHTML(q)}</span>
    </button>`;
  }).join('');

  // Explanation (LLM-only)
  const explainWrap = document.getElementById('music-rec-explain-wrap');
  const reasonEl = document.getElementById('music-rec-reason');
  if (rec.explanation && rec.explanation.trim()) {
    reasonEl.textContent = rec.explanation;
    explainWrap.style.display = 'block';
  } else {
    explainWrap.style.display = 'none';
  }
  const avoidWrap = document.getElementById('music-rec-avoid-wrap');
  const avoidEl = document.getElementById('music-rec-avoid');
  if (rec.avoid && rec.avoid.length) {
    avoidEl.textContent = rec.avoid.join(', ');
    avoidWrap.style.display = 'block';
  } else {
    avoidWrap.style.display = 'none';
  }

  // Auto-run the top query so the results list is pre-populated.
  if (autoSearchFirst && rec.queries.length > 0) {
    applyMusicSearch(0);
  }
}

function applyMusicSearch(idx) {
  const cacheKey = scriptHashShort(currentScriptDocument()) + '|' + (state.script_tone || '');
  const rec = musicRecCache[cacheKey];
  if (!rec || !rec.queries || !rec.queries[idx]) return;
  const q = rec.queries[idx];
  musicRecActiveQuery = q;

  const input = document.getElementById('jamendo-search');
  if (input) input.value = q;
  // Highlight the applied chip
  document.querySelectorAll('#music-rec-chips .rec-chip').forEach((c, i) => {
    c.classList.toggle('rec-chip-applied', i === idx);
  });
  searchJamendo();
}

function dismissMusicRecommendation() {
  musicRecDismissed = true;
  const banner = document.getElementById('music-rec-banner');
  if (banner) banner.style.display = 'none';
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
  btns[1].classList.toggle('active', mode === 'ai_generated');
  btns[2].classList.toggle('active', mode === 'manual');
  btns[3].classList.toggle('active', mode === 'skip');

  document.getElementById('auto-music-genre').style.display = mode === 'auto' ? 'block' : 'none';
  document.getElementById('ai-music-section').style.display = mode === 'ai_generated' ? 'block' : 'none';
  document.getElementById('manual-music-section').style.display = mode === 'manual' ? 'block' : 'none';

  // Initialise the AI panel preview the first time it's shown.
  if (mode === 'ai_generated') updateAIMusicPromptPreview();

  // Manual music opens on the Jamendo source by default; surface the
  // recommendation banner immediately so the search box isn't a blank slate.
  if (mode === 'manual') {
    maybeLoadMusicRecommendation();
  } else {
    const banner = document.getElementById('music-rec-banner');
    if (banner) banner.style.display = 'none';
  }
}

// ---- AI Music ----
//
// Vibe presets are mirrored on the server (pipeline/music_ai.go); we keep the
// human-readable preview here so the user sees what the model will actually
// receive. Selecting a preset replaces both the prompt AND the ambience stack
// (so the result feels intentional). "custom" mode unlocks the textarea.
const aiMusicPresets = {
  peaceful_aesthetic: {
    prompt: 'Calm cinematic nature ambience, soft piano, airy vocal pad, light strings, distant reverb, peaceful sunrise mood, slow 70 BPM',
    ambience: ['birds', 'wind']
  },
  cinematic_drama: {
    prompt: 'Epic cinematic orchestral, deep cinematic drone, slow timpani, sweeping strings, emotional swell, hopeful but tense',
    ambience: []
  },
  lofi_study: {
    prompt: 'Chill lo-fi hip hop beat, vinyl crackle, soft Rhodes piano, mellow drums, jazzy chords, focused study mood, 80 BPM',
    ambience: ['vinyl']
  },
  sunrise_vlog: {
    prompt: 'Warm acoustic guitar, gentle finger-picked melody, soft strings, hopeful uplifting mood, morning vlog feel',
    ambience: ['birds']
  },
  asmr_calm: {
    prompt: 'Soft ambient drone, gentle synth pads, very slow evolving texture, sleep meditation mood, no drums',
    ambience: ['waves', 'rain']
  },
  tech_futuristic: {
    prompt: 'Modern electronic ambient, synth pads, soft glitch textures, motivational pulse, futuristic tech feel',
    ambience: []
  },
  mysterious: {
    prompt: 'Suspenseful dark ambient, low drones, distant whispered textures, eerie tension build, no melody',
    ambience: ['wind']
  },
  motivational: {
    prompt: 'High-energy uplifting orchestral, driving drums, anthemic strings, triumphant climax, motivational mood',
    ambience: []
  },
  custom: { prompt: '', ambience: [] }
};

function setMusicPreset(presetId) {
  state.music_preset = presetId;
  const preset = aiMusicPresets[presetId] || aiMusicPresets.custom;

  const promptInput = document.getElementById('ai-music-prompt');
  if (presetId === 'custom') {
    state.music_prompt = promptInput.value.trim();
  } else {
    state.music_prompt = '';
    promptInput.value = preset.prompt;
  }
  state.music_ambience = preset.ambience.slice();

  document.querySelectorAll('.vibe-preset-card').forEach(c => {
    c.classList.toggle('active', c.dataset.preset === presetId);
  });
  document.querySelectorAll('.ambience-chip').forEach(chip => {
    chip.classList.toggle('active', state.music_ambience.includes(chip.dataset.ambience));
  });

  resetAIMusicPreview();
  updateAIMusicPromptPreview();
}

function onAIMusicPromptInput(value) {
  state.music_prompt = (value || '').trim();
  // Free-typing implicitly switches the preset to "custom" so the user's
  // text is preserved when re-rendering the preview.
  if (state.music_preset !== 'custom') {
    state.music_preset = 'custom';
    document.querySelectorAll('.vibe-preset-card').forEach(c => {
      c.classList.toggle('active', c.dataset.preset === 'custom');
    });
  }
  resetAIMusicPreview();
  updateAIMusicPromptPreview();
}

function toggleAmbience(tag) {
  const idx = state.music_ambience.indexOf(tag);
  if (idx >= 0) state.music_ambience.splice(idx, 1);
  else state.music_ambience.push(tag);

  document.querySelectorAll('.ambience-chip').forEach(chip => {
    chip.classList.toggle('active', state.music_ambience.includes(chip.dataset.ambience));
  });
  resetAIMusicPreview();
  updateAIMusicPromptPreview();
}

function updateAIMusicPromptPreview() {
  const preset = aiMusicPresets[state.music_preset] || aiMusicPresets.custom;
  let prompt = state.music_prompt || preset.prompt || '(no prompt set)';
  if (state.music_ambience.length > 0) {
    prompt += ', with subtle ' + state.music_ambience.join(' and ') + ' atmosphere';
  }
  const el = document.getElementById('ai-music-prompt-value');
  if (el) el.textContent = prompt;
}

// ---- AI Music Preview / Regenerate ----
//
// Calls POST /api/music/ai/generate with the current preset + prompt + ambience
// + provider, gets MP3 bytes back as base64, plays them in the wizard, and
// stores them in state so the job submission reuses the pre-generated track
// instead of re-running the slow provider chain.

async function generateAIMusicPreview(isRegenerate) {
  const btn = document.getElementById('btn-ai-music-preview');
  const status = document.getElementById('ai-music-status');
  const panel = document.getElementById('ai-music-track-panel');

  // Build the same prompt the prompt-preview line shows (preset → custom → tone fallback).
  const preset = aiMusicPresets[state.music_preset] || aiMusicPresets.custom;
  const prompt = (state.music_prompt && state.music_prompt.trim()) || preset.prompt || '';

  const body = {
    preset: state.music_preset,
    prompt: prompt,
    provider: state.music_provider,
    script_tone: state.script_tone,
    ambience: state.music_ambience.slice(),
    duration_sec: 30
  };

  btn.disabled = true;
  btn.textContent = isRegenerate ? '🔄 Regenerating...' : '⏳ Generating (30-90s)...';
  if (status) status.textContent = 'Calling AI provider — this can take up to 90 seconds the first time.';

  try {
    const resp = await fetch(API + '/api/music/ai/generate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    });

    const data = await resp.json();
    if (!resp.ok) {
      throw new Error(data.error || ('HTTP ' + resp.status));
    }

    const dataUrl = 'data:audio/mpeg;base64,' + data.audio_base64;
    state.ai_music_audio_base64 = dataUrl;
    state.ai_music_start = 0;
    state.ai_music_end = data.duration_sec || 30;

    // Refresh the audio player. Re-set src on regenerate so the browser
    // discards the previous buffered track.
    const audio = document.getElementById('ai-music-audio');
    audio.pause();
    audio.src = dataUrl;
    audio.load();

    // Reset crop sliders to the new track's full length.
    const startSlider = document.getElementById('ai-music-start-slider');
    const endSlider = document.getElementById('ai-music-end-slider');
    const fullLen = state.ai_music_end;
    startSlider.max = fullLen;
    endSlider.max = fullLen;
    startSlider.value = 0;
    endSlider.value = fullLen;
    document.getElementById('ai-music-start-val').textContent = '0';
    document.getElementById('ai-music-end-val').textContent = String(fullLen);

    // Provider badge — tells the user which leg of the chain succeeded.
    const badge = document.getElementById('ai-music-provider-badge');
    if (badge) {
      const labels = {
        huggingface_musicgen: 'MusicGen',
        huggingface_stable_audio: 'Stable Audio',
        jamendo: 'Jamendo (fallback)'
      };
      badge.textContent = labels[data.provider_used] || data.provider_used || 'AI';
    }

    panel.style.display = 'block';
    btn.textContent = '🔁 Generate Another Preview';
    if (status) status.textContent = isRegenerate ? '✅ New variation ready.' : '✅ Preview ready — play it below.';
  } catch (err) {
    console.error('AI music preview failed:', err);
    if (status) status.textContent = '❌ ' + (err.message || 'Generation failed');
    btn.textContent = '🎵 Generate Preview';
  } finally {
    btn.disabled = false;
  }
}

function updateAIMusicCrop(changedId) {
  const startSlider = document.getElementById('ai-music-start-slider');
  const endSlider = document.getElementById('ai-music-end-slider');

  let start = parseInt(startSlider.value);
  let end = parseInt(endSlider.value);

  // Keep the two thumbs from crossing each other.
  if (start >= end) {
    if (changedId === 'ai-music-start-slider') start = end - 1;
    else end = start + 1;
    startSlider.value = start;
    endSlider.value = end;
  }

  state.ai_music_start = start;
  state.ai_music_end = end;
  document.getElementById('ai-music-start-val').textContent = String(start);
  document.getElementById('ai-music-end-val').textContent = String(end);

  const audio = document.getElementById('ai-music-audio');
  if (audio && Math.abs(audio.currentTime - start) > 1) {
    audio.currentTime = start;
  }
}

// Drop any cached preview when the user changes their preset/prompt/ambience
// so they don't accidentally submit a job with a stale track that no longer
// matches what they see in the prompt preview line. Called from the existing
// preset / ambience / prompt handlers below via resetAIMusicPreview().
function setAIMusicProvider(provider) {
  state.music_provider = provider;
  resetAIMusicPreview();
}

function resetAIMusicPreview() {
  state.ai_music_audio_base64 = '';
  state.ai_music_start = 0;
  state.ai_music_end = 0;
  const panel = document.getElementById('ai-music-track-panel');
  if (panel) panel.style.display = 'none';
  const btn = document.getElementById('btn-ai-music-preview');
  if (btn) btn.textContent = '🎵 Generate Preview';
  const status = document.getElementById('ai-music-status');
  if (status) status.textContent = '';
  const audio = document.getElementById('ai-music-audio');
  if (audio) {
    audio.pause();
    audio.removeAttribute('src');
    audio.load();
  }
}

// Manual mode supports three sources of audio: Jamendo search, a direct URL,
// or an uploaded local file. Switching sources clears any previously chosen
// track so the user can't accidentally send stale state to the backend.
function setManualMusicSource(source) {
  const tabs = document.querySelector('.manual-music-source-tabs').children;
  tabs[0].classList.toggle('active', source === 'jamendo');
  tabs[1].classList.toggle('active', source === 'url');
  tabs[2].classList.toggle('active', source === 'upload');

  document.getElementById('manual-source-jamendo').style.display = source === 'jamendo' ? 'block' : 'none';
  document.getElementById('manual-source-url').style.display = source === 'url' ? 'block' : 'none';
  document.getElementById('manual-source-upload').style.display = source === 'upload' ? 'block' : 'none';

  // Clear prior selection from a different source so we never submit two at once.
  resetSelectedTrack();

  // Show recommendations only on the Jamendo tab — URL/upload have nothing
  // we can pre-populate. Hide the banner otherwise so it doesn't linger.
  if (source === 'jamendo') {
    maybeLoadMusicRecommendation();
  } else {
    const banner = document.getElementById('music-rec-banner');
    if (banner) banner.style.display = 'none';
    const empty = document.getElementById('music-rec-empty');
    if (empty) empty.style.display = 'none';
  }
}

function resetSelectedTrack() {
  state.music_url = '';
  state.music_file_base64 = '';
  state.music_start = 0;
  state.music_end = 0;
  document.getElementById('selected-track-ui').style.display = 'none';
  const audio = document.getElementById('jamendo-audio');
  if (audio) {
    audio.pause();
    audio.removeAttribute('src');
    audio.load();
  }
  const fileInput = document.getElementById('manual-music-file');
  if (fileInput) fileInput.value = '';
  const urlInput = document.getElementById('manual-music-url');
  if (urlInput) urlInput.value = '';
  const uploadedInfo = document.getElementById('manual-uploaded-info');
  if (uploadedInfo) uploadedInfo.style.display = 'none';
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
  state.music_file_base64 = '';
  showSelectedTrack(name, streamUrl, duration);
}

// Direct URL input: every keystroke updates state. We use the URL itself as
// both the audio preview source and the backend download source, so the
// preview only works for CORS-friendly URLs — duration is best-effort.
function onManualUrlInput(value) {
  const url = (value || '').trim();
  state.music_file_base64 = '';
  if (!url) {
    state.music_url = '';
    document.getElementById('selected-track-ui').style.display = 'none';
    return;
  }
  state.music_url = url;

  const audio = document.getElementById('jamendo-audio');
  audio.src = url;
  document.getElementById('selected-track-name').textContent = url.split('/').pop() || url;

  audio.onloadedmetadata = () => {
    const duration = Math.floor(audio.duration) || 60;
    showSelectedTrack(document.getElementById('selected-track-name').textContent, url, duration);
  };
  audio.onerror = () => {
    // Preview failed (likely CORS) — still let the user proceed; backend will fetch directly.
    showSelectedTrack(document.getElementById('selected-track-name').textContent, url, 60);
  };
  document.getElementById('selected-track-ui').style.display = 'block';
}

// Local file upload: read into a data URL so we can both preview it locally
// and ship it to the backend in the JSON job payload.
function handleMusicFileUpload(input) {
  const file = input.files && input.files[0];
  if (!file) return;

  const reader = new FileReader();
  reader.onload = () => {
    const dataUrl = reader.result;
    state.music_file_base64 = dataUrl;
    state.music_url = '';

    const audio = document.getElementById('jamendo-audio');
    audio.src = dataUrl;
    audio.onloadedmetadata = () => {
      const duration = Math.floor(audio.duration) || 60;
      showSelectedTrack(file.name, dataUrl, duration);
    };
    audio.onerror = () => {
      showSelectedTrack(file.name, dataUrl, 60);
    };

    const sizeKb = (file.size / 1024).toFixed(0);
    const sizeStr = file.size > 1024 * 1024
      ? (file.size / (1024 * 1024)).toFixed(2) + ' MB'
      : sizeKb + ' KB';
    document.getElementById('manual-uploaded-name').textContent = file.name;
    document.getElementById('manual-uploaded-meta').textContent = (file.type || 'audio') + ' · ' + sizeStr;
    document.getElementById('manual-uploaded-info').style.display = 'block';
  };
  reader.onerror = () => {
    alert('Failed to read the audio file. Please try a different file.');
  };
  reader.readAsDataURL(file);
}

function clearUploadedMusic() {
  resetSelectedTrack();
}

// Common UI hook for "we now have a chosen track" — used by all three sources.
function showSelectedTrack(name, previewSrc, duration) {
  state.music_start = 0;
  state.music_end = duration;

  document.getElementById('selected-track-name').textContent = name;
  const audio = document.getElementById('jamendo-audio');
  if (audio.src !== previewSrc) audio.src = previewSrc;

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
    } else if (data.status === 'pending_visual_review') {
      // Pipeline paused after Stage 4 — show the per-clip review screen so
      // the user can preview each generated clip, regenerate the ones they
      // don't like, and optionally add Instagram-style text overlays.
      updateStep(4, 'done', data.message || 'All clips ready');
      openVisualReview(jobId);
    } else if (data.status === 'clip_regenerated') {
      // A single clip was just regenerated. Refresh that tile in place
      // (no full reload) by re-fetching the list quietly.
      reloadVisualReview();
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

// =====================================================================
//  Per-clip Visual Review (Stage 4 pause)
// =====================================================================
//
// When the pipeline finishes Stage 4 it pauses with status
// "pending_visual_review". The UI opens a grid of every generated visual
// so the user can preview, regenerate, or annotate each one before the
// video is rendered.

let visualReviewClips = [];          // last server snapshot of clip review items
let visualReviewEditingKey = null;   // which clip the overlay editor is targeting

async function openVisualReview(jobId) {
  if (jobId) currentJobId = jobId;
  document.getElementById('visual-review-panel').style.display = 'block';
  document.getElementById('visual-review-panel').scrollIntoView({behavior: 'smooth', block: 'nearest'});
  await reloadVisualReview();
}

async function reloadVisualReview() {
  if (!currentJobId) return;
  try {
    const res = await fetch(`${API}/api/jobs/${currentJobId}/clips`);
    if (!res.ok) {
      console.warn('Failed to load clips:', res.status);
      return;
    }
    const data = await res.json();
    visualReviewClips = data.clips || [];
    renderVisualReview();
  } catch (err) {
    console.error('reloadVisualReview', err);
  }
}

function renderVisualReview() {
  const grid = document.getElementById('visual-review-grid');
  const countEl = document.getElementById('visual-review-count');
  if (!grid || !countEl) return;

  countEl.textContent = visualReviewClips.length;
  if (!visualReviewClips.length) {
    grid.innerHTML = `<div class="visual-review-empty">No visuals generated yet. Click Refresh to retry.</div>`;
    return;
  }

  grid.innerHTML = visualReviewClips.map(item => {
    const isImage = item.source_type === 'image';
    const previewUrl = `${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(item.key)}/preview?t=${item.regen_count || 0}`;
    const segLabel = item.sub_index >= 0
      ? `Seg ${item.segment_id} · Visual ${item.sub_index + 1}`
      : `Seg ${item.segment_id}`;
    const overlayBadge = (item.overlay && item.overlay.text)
      ? `<span class="vr-badge vr-badge-overlay" title="${escapeHTML(item.overlay.text)}">✏️ ${escapeHTML(item.overlay.text.slice(0, 18))}${item.overlay.text.length > 18 ? '…' : ''}</span>`
      : '';
    const regenBadge = (item.regen_count > 0)
      ? `<span class="vr-badge vr-badge-regen">↻ ${item.regen_count}</span>`
      : '';
    const media = isImage
      ? `<img src="${previewUrl}" alt="visual" loading="lazy">`
      : `<video src="${previewUrl}" muted loop playsinline preload="metadata" onmouseenter="this.play()" onmouseleave="this.pause();this.currentTime=0"></video>`;

    return `
      <div class="vr-card" data-key="${escapeAttr(item.key)}">
        <div class="vr-card-media" onclick="openClipFullPreview('${escapeAttr(item.key)}')">
          ${media}
          <span class="vr-type-pill">${isImage ? '🖼 Image' : '🎬 Clip'}</span>
        </div>
        <div class="vr-card-body">
          <div class="vr-card-meta">
            <span class="vr-card-seg">${segLabel}</span>
            ${regenBadge}
            ${overlayBadge}
          </div>
          <div class="vr-card-narration" title="${escapeAttr(item.narration_text || '')}">
            ${escapeHTML((item.narration_text || '').slice(0, 100))}${(item.narration_text || '').length > 100 ? '…' : ''}
          </div>
          <input type="text" class="vr-query-input premium-input"
                 value="${escapeAttr(item.query || '')}"
                 data-key="${escapeAttr(item.key)}"
                 placeholder="Search query / AI prompt"
                 title="Edit the query then click Regenerate to use it">
          <div class="vr-card-actions">
            <button class="vr-btn vr-btn-regen" onclick="regenerateClip('${escapeAttr(item.key)}', false)" title="Same query, different result">🔄 Regen</button>
            <button class="vr-btn vr-btn-regen-edit" onclick="regenerateClip('${escapeAttr(item.key)}', true)" title="Use the edited query above">🔄✎ With query</button>
            <button class="vr-btn vr-btn-toggle" onclick="toggleClipType('${escapeAttr(item.key)}')" title="Switch between stock clip and AI image">${isImage ? '🎬 → Clip' : '🖼 → Image'}</button>
            <button class="vr-btn vr-btn-overlay" onclick="openOverlayEditor('${escapeAttr(item.key)}')">✏️ Text</button>
          </div>
        </div>
      </div>
    `;
  }).join('');
}

function escapeHTML(str) {
  const div = document.createElement('div');
  div.textContent = str || '';
  return div.innerHTML;
}
function escapeAttr(str) {
  return String(str || '').replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

async function regenerateClip(key, useEditedQuery) {
  if (!currentJobId) return;
  const card = document.querySelector(`.vr-card[data-key="${cssEscape(key)}"]`);
  if (card) card.classList.add('vr-card-loading');
  const body = {};
  if (useEditedQuery) {
    const input = card?.querySelector('.vr-query-input');
    if (input && input.value.trim()) body.query = input.value.trim();
  }
  try {
    const res = await fetch(`${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(key)}/regenerate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      alert('Regenerate failed: ' + (err.error || res.statusText));
      return;
    }
    // The WebSocket "clip_regenerated" event will trigger a reload, but we
    // also reload here in case the WS isn't connected.
    await reloadVisualReview();
  } catch (err) {
    alert('Regenerate failed: ' + err.message);
  } finally {
    if (card) card.classList.remove('vr-card-loading');
  }
}

async function toggleClipType(key) {
  const item = visualReviewClips.find(c => c.key === key);
  if (!item || !currentJobId) return;
  const newType = item.source_type === 'image' ? 'clip' : 'image';
  const card = document.querySelector(`.vr-card[data-key="${cssEscape(key)}"]`);
  if (card) card.classList.add('vr-card-loading');
  try {
    const input = card?.querySelector('.vr-query-input');
    const body = { source_type: newType };
    if (input && input.value.trim() && input.value.trim() !== item.query) {
      body.query = input.value.trim();
    }
    const res = await fetch(`${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(key)}/regenerate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      alert('Switch type failed: ' + (err.error || res.statusText));
      return;
    }
    await reloadVisualReview();
  } catch (err) {
    alert('Switch type failed: ' + err.message);
  } finally {
    if (card) card.classList.remove('vr-card-loading');
  }
}

function cssEscape(str) {
  // CSS.escape isn't on every old browser; this fallback handles the
  // chars that show up in our keys ("0", "1_2", etc.).
  if (window.CSS && CSS.escape) return CSS.escape(str);
  return String(str).replace(/[^a-zA-Z0-9_-]/g, '\\$&');
}

async function approveAllVisuals() {
  if (!currentJobId) return;
  const btn = document.getElementById('btn-approve-visuals');
  btn.disabled = true;
  btn.textContent = 'Resuming…';
  try {
    const res = await fetch(`${API}/api/jobs/${currentJobId}/clips/approve-all`, { method: 'POST' });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      alert('Failed to continue: ' + (err.error || res.statusText));
      btn.disabled = false;
      btn.textContent = '✅ Continue to Render →';
      return;
    }
    document.getElementById('visual-review-panel').style.display = 'none';
  } catch (err) {
    alert('Failed to continue: ' + err.message);
    btn.disabled = false;
    btn.textContent = '✅ Continue to Render →';
  }
}

// ---- Overlay editor ----
//
// Two responsibilities:
//   1) Persist overlay JSON to the backend (saveOverlay / clearOverlay).
//   2) Render a live HTML preview that mirrors the FFmpeg drawtext output
//      closely enough that the user can pick size/position/shadow without
//      a server round-trip. The "Render real preview" button triggers a
//      backend FFmpeg pass for pixel-accurate confirmation.

// CSS placement that mirrors the 9-cell position grid used by drawtext.
// Keep in sync with positionExpr() in pipeline/renderer.go.
const OVERLAY_POSITION_CSS = {
  'top-left':   { top: '5%',   left: '5%',   transform: 'none' },
  'top-center': { top: '5%',   left: '50%',  transform: 'translateX(-50%)' },
  'top-right':  { top: '5%',   right: '5%',  transform: 'none' },
  'mid-left':   { top: '50%',  left: '5%',   transform: 'translateY(-50%)' },
  'mid-center': { top: '50%',  left: '50%',  transform: 'translate(-50%, -50%)' },
  'mid-right':  { top: '50%',  right: '5%',  transform: 'translateY(-50%)' },
  'bot-left':   { bottom: '8%', left: '5%',  transform: 'none' },
  'bot-center': { bottom: '8%', left: '50%', transform: 'translateX(-50%)' },
  'bot-right':  { bottom: '8%', right: '5%', transform: 'none' },
};

// Shadow presets — must produce the same overlay JSON we send to the
// backend. The CSS variant uses textShadow values that match the visual
// of the FFmpeg drawtext layers.
const SHADOW_PRESETS = {
  none:   { ffmpeg: { shadow_color: '', shadow_x: 0,  shadow_y: 0, glow: false }, css: null },
  soft:   { ffmpeg: { shadow_color: 'black@0.6', shadow_x: 2, shadow_y: 2, glow: false }, css: { x: 2, y: 2, blur: 2, color: 'rgba(0,0,0,0.6)' } },
  hard:   { ffmpeg: { shadow_color: 'black@0.9', shadow_x: 3, shadow_y: 3, glow: false }, css: { x: 3, y: 3, blur: 0, color: 'rgba(0,0,0,0.9)' } },
  long:   { ffmpeg: { shadow_color: 'black@0.7', shadow_x: 6, shadow_y: 6, glow: false }, css: { x: 6, y: 6, blur: 4, color: 'rgba(0,0,0,0.7)' } },
  glow:   { ffmpeg: { shadow_color: 'white@0.7', shadow_x: 0, shadow_y: 0, glow: true  }, css: { x: 0, y: 0, blur: 10, color: 'rgba(255,255,255,0.7)' } },
  custom: { ffmpeg: null, css: null }, // resolved from the X/Y sliders + color
};

// Aspect ratio of the source video frame in pixels — used to scale the
// stored "source coordinate" font size into the preview's pixel space.
const ASPECT_DIMS = {
  landscape: { w: 1920, h: 1080 },
  portrait:  { w: 1080, h: 1920 },
  square:    { w: 1080, h: 1080 },
};

let overlayServerPreviewTs = 0; // cache-buster for the rendered preview file

function openOverlayEditor(key) {
  visualReviewEditingKey = key;
  const item = visualReviewClips.find(c => c.key === key);
  const overlay = (item && item.overlay) || {};

  document.getElementById('overlay-text').value = overlay.text || '';
  document.getElementById('overlay-size').value = overlay.font_size || 48;
  document.getElementById('overlay-size-val').textContent = overlay.font_size || 48;
  document.getElementById('overlay-color').value = colorNameToHex(overlay.font_color) || '#ffffff';
  document.getElementById('overlay-box').checked = !!overlay.box_color;
  document.getElementById('overlay-fade').checked = overlay.fade_in !== false;

  // Typography
  document.getElementById('overlay-font').value = overlay.font_family || 'Inter';
  document.getElementById('overlay-bold').checked = !!overlay.bold;
  document.getElementById('overlay-italic').checked = !!overlay.italic;

  // Shadow — figure out which preset the stored overlay matches, if any.
  const preset = matchShadowPreset(overlay);
  document.getElementById('overlay-shadow-preset').value = preset;
  document.getElementById('overlay-shadow-color').value =
    overlay.shadow_color ? ffmpegColorToHex(overlay.shadow_color) : '#000000';
  document.getElementById('overlay-shadow-x').value = overlay.shadow_x || 2;
  document.getElementById('overlay-shadow-x-val').textContent = overlay.shadow_x || 2;
  document.getElementById('overlay-shadow-y').value = overlay.shadow_y || 2;
  document.getElementById('overlay-shadow-y-val').textContent = overlay.shadow_y || 2;
  document.getElementById('overlay-shadow-custom').style.display = preset === 'custom' ? 'flex' : 'none';

  // Position grid
  document.querySelectorAll('#overlay-position-grid .overlay-pos-cell').forEach(c => c.classList.remove('active'));
  const pos = overlay.position || 'bot-center';
  const cell = document.querySelector(`#overlay-position-grid .overlay-pos-cell[data-pos="${pos}"]`);
  if (cell) cell.classList.add('active');

  // Point the preview at the same source as the card.
  const previewUrl = `${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(key)}/preview?t=${item ? item.regen_count || 0 : 0}`;
  const videoEl = document.getElementById('overlay-preview-video');
  const imgEl = document.getElementById('overlay-preview-image');
  if (item && item.source_type === 'image') {
    imgEl.src = previewUrl;
    imgEl.style.display = '';
    videoEl.style.display = 'none';
    try { videoEl.pause(); } catch (_) {}
    videoEl.removeAttribute('src');
  } else {
    videoEl.src = previewUrl;
    videoEl.style.display = '';
    imgEl.style.display = 'none';
    videoEl.play().catch(() => {}); // muted autoplay is allowed
  }

  // Match the preview frame's aspect to the actual output aspect.
  const dims = ASPECT_DIMS[state.aspect_ratio] || ASPECT_DIMS.landscape;
  const frame = document.getElementById('overlay-preview-frame');
  if (frame) frame.style.aspectRatio = `${dims.w} / ${dims.h}`;

  // Reset the server-preview state — any previously rendered file is for
  // a different clip/overlay combination.
  overlayServerPreviewTs = 0;

  document.getElementById('overlay-editor-backdrop').style.display = 'flex';
  // Run after layout so the preview frame has its final width.
  requestAnimationFrame(updateOverlayPreview);
}

function closeOverlayEditor(event) {
  if (event && event.target.id !== 'overlay-editor-backdrop') return;
  const modal = document.getElementById('overlay-editor-backdrop');
  if (modal) modal.style.display = 'none';
  // Stop the video so it doesn't keep playing behind the scenes.
  const videoEl = document.getElementById('overlay-preview-video');
  if (videoEl) { try { videoEl.pause(); } catch (_) {} videoEl.removeAttribute('src'); }
  visualReviewEditingKey = null;
}

function colorNameToHex(name) {
  if (!name) return '';
  if (typeof name !== 'string') return '';
  if (name.startsWith('#')) return name;
  // Use a temp element to resolve named colors to hex.
  const ctx = document.createElement('canvas').getContext('2d');
  ctx.fillStyle = name;
  return ctx.fillStyle;
}

// "black@0.5" or "#rrggbb@0.5" → "#rrggbb" (we ignore the alpha here;
// the color picker doesn't have an alpha channel, alpha lives on the
// preset). Returns "#000000" as a safe fallback.
function ffmpegColorToHex(c) {
  if (!c) return '#000000';
  const atIdx = c.indexOf('@');
  const base = atIdx >= 0 ? c.slice(0, atIdx) : c;
  return colorNameToHex(base) || '#000000';
}

// hex (#rrggbb) + alpha (0..1) → "rrggbb@a" (FFmpeg format).
function hexToFFmpegColor(hex, alpha) {
  if (!hex) return '';
  const clean = hex.startsWith('#') ? hex.slice(1) : hex;
  if (alpha != null && alpha >= 0 && alpha < 1) {
    return `0x${clean}@${alpha}`;
  }
  return `0x${clean}`;
}

// Decide which preset best matches a stored overlay's shadow fields.
function matchShadowPreset(overlay) {
  if (!overlay || !overlay.shadow_color) return 'none';
  if (overlay.glow) return 'glow';
  for (const name of ['soft', 'hard', 'long']) {
    const p = SHADOW_PRESETS[name].ffmpeg;
    if (p && p.shadow_x === (overlay.shadow_x || 0) && p.shadow_y === (overlay.shadow_y || 0) && p.shadow_color === overlay.shadow_color) {
      return name;
    }
  }
  return 'custom';
}

// Read current modal state and update the live HTML preview overlay.
function updateOverlayPreview() {
  const box = document.getElementById('overlay-preview-textbox');
  if (!box) return;

  const text = document.getElementById('overlay-text').value || 'Preview text';
  const size = parseInt(document.getElementById('overlay-size').value, 10) || 48;
  const color = document.getElementById('overlay-color').value || '#ffffff';
  const family = document.getElementById('overlay-font').value || 'Inter';
  const bold = document.getElementById('overlay-bold').checked;
  const italic = document.getElementById('overlay-italic').checked;
  const boxOn = document.getElementById('overlay-box').checked;
  const presetKey = document.getElementById('overlay-shadow-preset').value;
  const pos = document.querySelector('#overlay-position-grid .overlay-pos-cell.active')?.dataset.pos || 'bot-center';

  // Scale font size from source-frame coordinates to the preview's pixel
  // size so a "48px in 1920px wide source" looks proportional in the modal.
  const dims = ASPECT_DIMS[state.aspect_ratio] || ASPECT_DIMS.landscape;
  const frame = document.getElementById('overlay-preview-frame');
  const previewW = frame ? frame.clientWidth || 320 : 320;
  const scale = previewW / dims.w;
  const renderSize = Math.max(8, size * scale);

  // Reset position-related styles before applying the active cell's rules.
  ['top', 'bottom', 'left', 'right', 'transform'].forEach(p => { box.style[p] = ''; });
  Object.assign(box.style, OVERLAY_POSITION_CSS[pos] || OVERLAY_POSITION_CSS['bot-center']);

  // Font + colour + box.
  box.textContent = text;
  box.style.fontFamily = `'${cssFontFamily(family)}', sans-serif`;
  box.style.fontSize = renderSize + 'px';
  box.style.color = color;
  box.style.fontWeight = bold ? '700' : '400';
  box.style.fontStyle = italic ? 'italic' : 'normal';
  box.style.background = boxOn ? 'rgba(0,0,0,0.5)' : 'transparent';
  box.style.padding = boxOn ? '4px 10px' : '0';

  // Shadow: combine the preset baseline with the always-present outline
  // (matches drawtext's borderw=2:bordercolor=black@0.6 on the main pass).
  const outline = '0 0 0 transparent, 0 1px 0 rgba(0,0,0,0.6), 1px 0 0 rgba(0,0,0,0.6), -1px 0 0 rgba(0,0,0,0.6), 0 -1px 0 rgba(0,0,0,0.6)';
  let shadow = '';
  if (presetKey === 'custom') {
    const sx = parseInt(document.getElementById('overlay-shadow-x').value, 10) || 0;
    const sy = parseInt(document.getElementById('overlay-shadow-y').value, 10) || 0;
    const sc = document.getElementById('overlay-shadow-color').value || '#000000';
    if (sx !== 0 || sy !== 0) {
      shadow = `${sx}px ${sy}px 3px ${sc}`;
    }
  } else {
    const preset = SHADOW_PRESETS[presetKey];
    if (preset && preset.css) {
      const s = preset.css;
      shadow = `${s.x}px ${s.y}px ${s.blur}px ${s.color}`;
    }
  }
  box.style.textShadow = shadow ? `${shadow}, ${outline}` : outline;

  // Toggle the custom X/Y sliders visibility based on preset.
  const customRow = document.getElementById('overlay-shadow-custom');
  if (customRow) customRow.style.display = presetKey === 'custom' ? 'flex' : 'none';
}

// Map our internal family names to a CSS font-family the browser can find.
// The browser doesn't know about our bundled TTFs, so we map to a similar
// system font for the preview. The actual rendered video uses the TTFs in
// assets/fonts/ via FFmpeg drawtext.
function cssFontFamily(name) {
  switch (name) {
    case 'Inter':            return 'Inter, "Segoe UI", system-ui';
    case 'Roboto':           return 'Roboto, "Segoe UI", system-ui';
    case 'Montserrat':       return 'Montserrat, "Segoe UI", system-ui';
    case 'PlayfairDisplay':  return '"Playfair Display", "Times New Roman", Georgia, serif';
    case 'Bebas':            return '"Bebas Neue", Impact, "Arial Black", sans-serif';
    default:                 return name + ', "Segoe UI", system-ui';
  }
}

// Build the TextOverlay JSON to send to the backend from the current UI.
function buildOverlayPayloadFromUI() {
  const activeCell = document.querySelector('#overlay-position-grid .overlay-pos-cell.active');
  const presetKey = document.getElementById('overlay-shadow-preset').value;
  const preset = SHADOW_PRESETS[presetKey];

  let shadow = { shadow_color: '', shadow_x: 0, shadow_y: 0, glow: false };
  if (presetKey === 'custom') {
    const sx = parseInt(document.getElementById('overlay-shadow-x').value, 10) || 0;
    const sy = parseInt(document.getElementById('overlay-shadow-y').value, 10) || 0;
    const sc = document.getElementById('overlay-shadow-color').value || '#000000';
    shadow = {
      shadow_color: (sx === 0 && sy === 0) ? '' : hexToFFmpegColor(sc, 0.85),
      shadow_x: sx,
      shadow_y: sy,
      glow: false,
    };
  } else if (preset && preset.ffmpeg) {
    shadow = { ...preset.ffmpeg };
    // If the user picked a non-default shadow color, override the preset's.
    const sc = document.getElementById('overlay-shadow-color').value;
    if (sc && sc !== '#000000' && shadow.shadow_color !== '') {
      // Preserve the preset's alpha but swap the base RGB.
      const atIdx = shadow.shadow_color.indexOf('@');
      const alphaSuffix = atIdx >= 0 ? shadow.shadow_color.slice(atIdx) : '';
      shadow.shadow_color = hexToFFmpegColor(sc) + alphaSuffix.replace('0x', '');
    }
  }

  return {
    text: document.getElementById('overlay-text').value.trim(),
    position: activeCell ? activeCell.dataset.pos : 'bot-center',
    font_size: parseInt(document.getElementById('overlay-size').value, 10) || 48,
    font_color: document.getElementById('overlay-color').value || 'white',
    box_color: document.getElementById('overlay-box').checked ? 'black@0.5' : '',
    fade_in: document.getElementById('overlay-fade').checked,

    font_family: document.getElementById('overlay-font').value || 'Inter',
    bold: document.getElementById('overlay-bold').checked,
    italic: document.getElementById('overlay-italic').checked,

    shadow_color: shadow.shadow_color,
    shadow_x: shadow.shadow_x,
    shadow_y: shadow.shadow_y,
    glow: shadow.glow,
  };
}

async function saveOverlay() {
  if (!visualReviewEditingKey || !currentJobId) {
    closeOverlayEditor();
    return;
  }
  const body = buildOverlayPayloadFromUI();
  try {
    const res = await fetch(`${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(visualReviewEditingKey)}/overlay`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      alert('Failed to save overlay: ' + (err.error || res.statusText));
      return;
    }
    await reloadVisualReview();
    closeOverlayEditor();
  } catch (err) {
    alert('Failed to save overlay: ' + err.message);
  }
}

async function clearOverlay() {
  if (!visualReviewEditingKey || !currentJobId) return;
  try {
    await fetch(`${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(visualReviewEditingKey)}/overlay`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: '' }),
    });
    await reloadVisualReview();
    closeOverlayEditor();
  } catch (err) {
    alert('Failed to remove overlay: ' + err.message);
  }
}

// Trigger a backend FFmpeg render so the preview shows the *exact* drawtext
// output. Replaces the HTML overlay with the rendered video on success.
async function renderServerOverlayPreview() {
  if (!visualReviewEditingKey || !currentJobId) return;
  const btn = document.getElementById('btn-overlay-server-preview');
  const body = buildOverlayPayloadFromUI();
  if (!body.text) {
    alert('Enter some text first.');
    return;
  }
  if (btn) { btn.disabled = true; btn.textContent = '⏳ Rendering…'; }
  try {
    const res = await fetch(`${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(visualReviewEditingKey)}/overlay-preview`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      alert('Preview render failed: ' + (err.error || res.statusText));
      return;
    }
    const data = await res.json();
    overlayServerPreviewTs = data.ts || Date.now();
    const url = `${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(visualReviewEditingKey)}/overlay-preview?ts=${overlayServerPreviewTs}`;
    const videoEl = document.getElementById('overlay-preview-video');
    const imgEl = document.getElementById('overlay-preview-image');
    if (videoEl) {
      videoEl.src = url;
      videoEl.style.display = '';
      videoEl.play().catch(() => {});
    }
    if (imgEl) imgEl.style.display = 'none';
    // Hide the HTML overlay since the rendered version has the text baked in.
    const textbox = document.getElementById('overlay-preview-textbox');
    if (textbox) textbox.style.visibility = 'hidden';
  } catch (err) {
    alert('Preview render failed: ' + err.message);
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = '🎬 Render real preview'; }
  }
}

function openClipFullPreview(key) {
  const item = visualReviewClips.find(c => c.key === key);
  if (!item) return;
  const url = `${API}/api/jobs/${currentJobId}/clips/${encodeURIComponent(key)}/preview?t=${item.regen_count || 0}`;
  window.open(url, '_blank');
}

// Wire up live updates inside the overlay editor.
document.addEventListener('DOMContentLoaded', () => {
  // Re-render the HTML preview on any control change.
  const watchedIds = [
    'overlay-text',
    'overlay-size',
    'overlay-color',
    'overlay-font',
    'overlay-bold',
    'overlay-italic',
    'overlay-box',
    'overlay-fade',
    'overlay-shadow-preset',
    'overlay-shadow-color',
    'overlay-shadow-x',
    'overlay-shadow-y',
  ];
  watchedIds.forEach(id => {
    const el = document.getElementById(id);
    if (!el) return;
    const evt = (el.type === 'range' || el.type === 'text' || el.type === 'color') ? 'input' : 'change';
    el.addEventListener(evt, () => {
      // Show the HTML overlay again whenever a control changes (the server
      // preview becomes stale the moment the user edits anything).
      const textbox = document.getElementById('overlay-preview-textbox');
      if (textbox) textbox.style.visibility = '';
      updateOverlayPreview();
    });
  });

  const sizeInput = document.getElementById('overlay-size');
  if (sizeInput) {
    sizeInput.addEventListener('input', e => {
      const v = e.target.value;
      const out = document.getElementById('overlay-size-val');
      if (out) out.textContent = v;
    });
  }
  ['overlay-shadow-x', 'overlay-shadow-y'].forEach(id => {
    const el = document.getElementById(id);
    if (!el) return;
    el.addEventListener('input', e => {
      const out = document.getElementById(id + '-val');
      if (out) out.textContent = e.target.value;
    });
  });

  const grid = document.getElementById('overlay-position-grid');
  if (grid) {
    grid.addEventListener('click', e => {
      const cell = e.target.closest('.overlay-pos-cell');
      if (!cell) return;
      grid.querySelectorAll('.overlay-pos-cell').forEach(c => c.classList.remove('active'));
      cell.classList.add('active');
      const textbox = document.getElementById('overlay-preview-textbox');
      if (textbox) textbox.style.visibility = '';
      updateOverlayPreview();
    });
  }

  // Re-render the preview if the modal/window resizes so the font-size
  // scaling stays correct.
  window.addEventListener('resize', () => {
    if (document.getElementById('overlay-editor-backdrop')?.style.display === 'flex') {
      updateOverlayPreview();
    }
  });
});

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
  // Seed the AI music prompt preview from the default preset so the user
  // sees real text the first time they open the wizard's music step.
  const promptInput = document.getElementById('ai-music-prompt');
  if (promptInput && typeof aiMusicPresets !== 'undefined') {
    const seed = aiMusicPresets[state.music_preset] || aiMusicPresets.peaceful_aesthetic;
    promptInput.value = seed.prompt;
    state.music_ambience = seed.ambience.slice();
    document.querySelectorAll('.ambience-chip').forEach(chip => {
      chip.classList.toggle('active', state.music_ambience.includes(chip.dataset.ambience));
    });
    if (typeof updateAIMusicPromptPreview === 'function') updateAIMusicPromptPreview();
  }
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
          ${j.status === 'pending_visual_review' ? `<span onclick="reviewClips('${j.id}')" style="color:var(--accent);cursor:pointer;font-size:12px">🎬 Review Clips</span>` : ''}
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

// Re-open the per-clip review screen for a job that's already paused at
// pending_visual_review (e.g. after a page refresh).
function reviewClips(id) {
  currentJobId = id;
  showPage('create');
  showProgress(id);
  updateStep(4, 'done', 'Visuals fetched! Review and approve clips before rendering.');
  connectWebSocket(id);
  openVisualReview(id);
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

  // The recommenders key off the script; a new approval may follow several
  // refinement rounds, so re-enable the banner the user may have dismissed
  // earlier against a different draft.
  voiceRecDismissed = false;
  musicRecDismissed = false;

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

