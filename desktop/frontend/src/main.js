import { Events } from "@wailsio/runtime";
import {
  AddOrigin,
  AquilaLog,
  AquilaState,
  ClearLogs,
  ClearPrinterQueue,
  CopyServerAddress,
  DiagnosePort,
  DiscoverNetworkPrinters,
  ExportConfig,
  GetAutostart,
  GetConfig,
  GetLogs,
  GetPrinterCount,
  GetPrinterPorts,
  GetPrinters,
  GetSettings,
  GetState,
  ImportConfig,
  InstallNetworkPrinter,
  OpenConfigFolder,
  OpenServerURL,
  QuitApp,
  ReloadConfig,
  RemoveOrigin,
  ResetConfig,
  RestartService,
  SaveSettings,
  SetAutostart,
  SetListen,
  SetPrinterPort,
  ShowPanel,
  StartService,
  StopService,
  TestConnection,
  TestPrinter,
  ToggleService,
  UninstallApp,
  UpdateOrigin,
} from "./api.js";
import "./style.css";

// Batas baris log di DOM. Sama dengan maxLogRows di sisi Go: memori UI tetap
// datar walau layanan berjalan berhari-hari.
const MAX_LOG_LINES = 200;

const isPopup = new URLSearchParams(location.search).has("popup");

// State ringkas di memori; hanya objek kecil yang disimpan, tanpa cache besar.
// Ikon SVG vektor modern (menggantikan emoji untuk konsistensi di Windows, Mac, dan Linux)
const ICONS = {
  printer: `<svg class="btn-icon" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 6 2 18 2 18 9"></polyline><path d="M6 18H4a2 2 0 0 1-2-2v-5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v5a2 2 0 0 1-2 2h-2"></path><rect x="6" y="14" width="12" height="8"></rect></svg>`,
  copy: `<svg class="btn-icon" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>`,
  search: `<svg class="btn-icon" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>`,
  trash: `<svg class="btn-icon" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>`,
  plus: `<svg class="btn-icon" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"></line><line x1="5" y1="12" x2="19" y2="12"></line></svg>`,
  check: `<svg class="btn-icon" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"></polyline></svg>`,
};

function normalizePrinterName(val) {
  return (val || "").toLowerCase().replace(/[^a-z0-9]/g, "");
}

const state = {
  service: null,
  config: null,
  settings: null,
  origins: [],
  autostart: false,
  printers: -1, // -1 = belum dimuat
  printerList: null,
  loadingPrinters: false,
  networkPrinters: null,
  loadingNetworkPrinters: false,
  editingIndex: -1,
  savingOrigin: false,
  autoscroll: true,
  uptimeTimer: 0,
};

const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[c]));

const $ = (sel) => document.querySelector(sel);
const errorMessage = (err) => err?.message || String(err);

// ------------------------------------------------------------------ tampilan

const STATUS_TEXT = {
  stopped: "Layanan berhenti",
  starting: "Layanan memulai…",
  running: "Layanan berjalan",
  error: "Layanan bermasalah",
};

function statusClass(st) {
  if (!st) return "";
  if (st.status === "error") return "error";
  if (st.status === "starting") return "starting";
  if (st.status === "running") return st.health_ok ? "running" : "error";
  return "";
}

function statusLabel(st) {
  if (!st) return "…";
  if (st.status === "running" && !st.health_ok) return "Layanan tidak merespons";
  return STATUS_TEXT[st.status] || st.status;
}

function formatUptime(seconds) {
  if (!seconds || seconds < 1) return "—";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return `${h} jam ${m} mnt`;
  if (m > 0) return `${m} mnt ${s} dtk`;
  return `${s} dtk`;
}

function formatTime(time) {
  return String(time || "").split(" ").pop();
}

function toggleLabel(st) {
  return st && (st.status === "running" || st.status === "starting")
    ? "Hentikan Layanan"
    : "Mulai Layanan";
}

// ------------------------------------------------------------------ render

function renderStatus() {
  const st = state.service;
  const cls = statusClass(st);
  const label = statusLabel(st);

  const serviceError = $("#service-error");
  if (serviceError) {
    serviceError.textContent = st?.status === "error"
      ? st.last_error || "Layanan gagal dimulai."
      : "";
    serviceError.className = `banner${st?.status === "error" ? " show err" : ""}`;
  }

  const pillDot = $("#pill-dot");
  const pillText = $("#pill-text");
  if (pillDot && pillText) {
    pillDot.className = `dot ${cls}`;
    pillText.textContent = label;
  }

  const ppDot = $("#pp-dot");
  const ppStatus = $("#pp-status");
  if (ppDot && ppStatus) {
    ppDot.className = `big-dot ${cls}`;
    ppStatus.textContent = label;
    $("#pp-listen").textContent = st ? `http://${st.listen || "…"}` : "…";
    $("#pp-uptime").textContent = st ? `Aktif selama ${formatUptime(st.uptime_seconds)}` : "";
    $("#pp-health").textContent = st
      ? st.status === "running"
        ? st.health_ok
          ? "Koneksi sehat"
          : "Tidak merespons"
        : "—"
      : "";
    $("#pp-toggle").textContent = toggleLabel(st);
    $("#pp-toggle").disabled = !st || st.status === "starting";
  }

  const btnToggle = $("#btn-toggle");
  if (btnToggle) {
    btnToggle.textContent = toggleLabel(st);
    btnToggle.disabled = !st || st.status === "starting";
    const btnRestart = $("#btn-restart");
    if (btnRestart) btnRestart.disabled = !st || st.status === "stopped";
    const btnTest = $("#btn-test");
    if (btnTest) btnTest.disabled = !st || st.status !== "running";
    const btnOpen = $("#btn-open-url");
    if (btnOpen) btnOpen.disabled = !st || st.status !== "running";
  }

  renderBeranda();
  startUptimeTick();
}

function renderBeranda() {
  const st = state.service;
  const cfg = state.config;
  if (!$("#bd-status")) return;
  $("#bd-status").textContent = statusLabel(st);
  $("#bd-listen").textContent = st ? st.listen || "—" : "—";
  $("#bd-health").textContent = st
    ? st.status === "running"
      ? st.health_ok
        ? "Sehat"
        : "Tidak merespons"
      : "—"
    : "—";
  $("#bd-uptime").textContent =
    st && st.status === "running" ? formatUptime(st.uptime_seconds) : "—";
  const printerEl = $("#bd-printers");
  if (printerEl) {
    if (state.printers < 0) {
      printerEl.textContent = "Memuat…";
    } else {
      printerEl.innerHTML = `${state.printers} printer <button data-tab-goto="printers" style="margin-left:8px;padding:2px 8px;font-size:0.75rem">Lihat</button>`;
    }
  }
  $("#bd-path").textContent = cfg ? cfg.path : "—";
}

function loadPrinters(force = false) {
  if (state.loadingPrinters && !force) return state.printersPromise || Promise.resolve();
  state.loadingPrinters = true;
  const listEl = $("#printer-list");
  if (listEl && (!state.printerList || force)) {
    listEl.innerHTML = '<p class="muted">Mendeteksi printer & port fisik di OS…</p>';
  }
  state.printersPromise = Promise.all([
    GetPrinters().catch(() => []),
    GetPrinterPorts().catch(() => []),
  ])
    .then(([printers, ports]) => {
      state.printerList = printers || [];
      state.availablePorts = ports || [];
      state.printers = state.printerList.length;
      state.loadingPrinters = false;
      renderPrinters();
      renderBeranda();
    })
    .catch((err) => {
      state.loadingPrinters = false;
      state.printerList = [];
      state.availablePorts = [];
      state.printers = 0;
      renderPrinters(err);
      renderBeranda();
    });
  return state.printersPromise;
}

function renderPrinters(error = null) {
  const listEl = $("#printer-list");
  const statsEl = $("#printer-stats");
  if (!listEl) return;

  if (error) {
    listEl.innerHTML = `<p class="muted" style="color:var(--err)">Gagal mendeteksi printer: ${esc(errorMessage(error))}</p>`;
    if (statsEl) statsEl.innerHTML = "";
    return;
  }

  if (!state.printerList) {
    listEl.innerHTML = '<p class="muted">Memuat daftar printer…</p>';
    if (statsEl) statsEl.innerHTML = "";
    return;
  }

  const list = state.printerList;
  if (list.length === 0) {
    listEl.innerHTML = '<p class="muted">Tidak ada printer terdeteksi. Pastikan driver printer sudah terpasang di Windows/OS.</p>';
    if (statsEl) statsEl.innerHTML = "";
    return;
  }

  let usbCount = 0;
  let netCount = 0;
  let serialCount = 0;
  list.forEach((p) => {
    if (p.connection_type === "usb") usbCount++;
    else if (p.connection_type === "network") netCount++;
    else if (p.connection_type === "serial") serialCount++;
  });

  // Filter hanya printer berstatus Ready untuk tab Printer OS
  const readyList = list
    .map((p, origIdx) => ({ ...p, _origIndex: origIdx }))
    .filter((p) => (p.state || "").toLowerCase() === "ready" || p.is_ready);

  if (statsEl) {
    statsEl.innerHTML = `
      <div class="printer-stat-chip">Printer Siap (Ready): <b>${readyList.length}</b></div>
      <div class="printer-stat-chip">Total Terpasang: <b>${list.length}</b></div>
      <div class="printer-stat-chip">USB: <b>${usbCount}</b></div>
      <div class="printer-stat-chip">Wi-Fi / LAN: <b>${netCount}</b></div>
      ${serialCount > 0 ? `<div class="printer-stat-chip">Serial: <b>${serialCount}</b></div>` : ""}
    `;
  }

  if (readyList.length === 0) {
    listEl.innerHTML = `
      <div class="card" style="background:var(--bg);border-style:dashed;text-align:center;padding:24px 16px">
        <p style="font-weight:600;margin-bottom:6px">Tidak ada printer OS yang berstatus Ready</p>
        <p class="muted">Ditemukan ${list.length} printer di OS, tetapi seluruhnya saat ini berstatus Offline atau nonaktif. Pastikan kabel printer terhubung dan printer dinyalakan.</p>
      </div>
    `;
    return;
  }

  const connBadge = (p) => {
    switch (p.connection_type) {
      case "usb":
        return '<span class="badge badge-usb">USB</span>';
      case "network":
        return '<span class="badge badge-network">Wi-Fi / LAN</span>';
      case "serial":
        return '<span class="badge badge-serial">Serial COM</span>';
      case "virtual":
        return '<span class="badge badge-virtual">Virtual</span>';
      default:
        return '<span class="badge badge-virtual">Lokal</span>';
    }
  };

  const stateBadge = (st) => {
    switch (st) {
      case "ready":
        return '<span class="badge badge-ready">Ready</span>';
      case "printing":
        return '<span class="badge badge-printing">Printing</span>';
      case "offline":
        return '<span class="badge badge-offline">Offline</span>';
      case "disabled":
        return '<span class="badge badge-disabled">Disabled</span>';
      default:
        return `<span class="badge badge-virtual">${esc(st || "Unknown")}</span>`;
    }
  };

  const availablePorts = state.availablePorts || [];

  listEl.innerHTML = readyList
    .map((p) => {
      const idx = p._origIndex;
      const isThermal = p.printer_type === "thermal" || p.connection_type === "serial" || /POS|THERMAL|RECEIPT|STRUK|KASIR|58MM|80MM|XP-|TM-/i.test(p.name + " " + (p.driver || ""));
      const currentPort = (p.port || "").trim();
      
      // Susun opsi port: port saat ini + port yang terdeteksi
      const portOptions = [...new Set([currentPort, ...availablePorts].filter(Boolean))].sort();

      return `
        <div class="printer-card" data-printer-name="${esc(p.name)}">
          <div class="printer-card-header">
            <div class="printer-name-group">
              <span class="printer-name">${esc(p.name)}</span>
              ${p.is_default ? '<span class="badge badge-default">Utama</span>' : ""}
              ${connBadge(p)}
              <span class="badge ${isThermal ? 'badge-usb' : 'badge-virtual'}">${isThermal ? 'Thermal / Struk' : 'Dokumen Biasa'}</span>
              ${stateBadge(p.state)}
            </div>
          </div>
          <div class="printer-details">
            <span>Status Hardware: <strong>Plug & Play (Otomatis Spooler Windows)</strong></span>
            <span>Driver: <strong>${esc(p.driver || "—")}</strong></span>
            ${p.ip ? `<span>IP: <strong>${esc(p.ip)}</strong></span>` : ""}
            <span title="${esc(p.uri)}">Antrean OS: <code>${esc(p.name)}</code></span>
          </div>

          <!-- Aksi Cepat & Uji Cetak Dinamis (Plug & Play) -->
          <div class="printer-actions" style="margin-top:10px">
            <button class="primary" data-action="test-auto-printer" data-index="${idx}">
              ${ICONS.printer} Tes Cetak (${isThermal ? 'Struk Thermal' : 'Dokumen'})
            </button>
            ${
              !isThermal
                ? `<button data-action="test-raw-printer" data-index="${idx}" title="Uji kirim byte mentah">Tes Mode RAW</button>`
                : `<button class="secondary" data-action="test-doc-printer" data-index="${idx}" title="Uji kirim format Windows GDI">Tes Cetak Dokumen</button>`
            }
            <button class="icon-btn danger" data-action="clear-printer-queue" data-index="${idx}" title="Hapus seluruh dokumen macet di printer ini">${ICONS.trash} Bersihkan Antrean</button>
          </div>
        </div>
      `;
    })
    .join("");
}

function loadNetworkPrinters(force = false) {
  if (typeof DiscoverNetworkPrinters !== "function") return Promise.resolve();
  if (state.loadingNetworkPrinters && !force) return state.networkPrintersPromise || Promise.resolve();
  state.loadingNetworkPrinters = true;
  const listEl = $("#network-printer-list");
  if (listEl && (!state.networkPrinters || force)) {
    listEl.innerHTML = '<p class="muted">Memindai printer di jaringan via Bonjour/mDNS…</p>';
  }
  // Panggil langsung ke Go lewat binding Wails (bukan fetch HTTP), karena
  // WebView desktop bukan origin yang diizinkan oleh print server lokal.
  state.networkPrintersPromise = Promise.resolve()
    .then(() => DiscoverNetworkPrinters())
    .then((list) => {
      state.networkPrinters = Array.isArray(list) ? list : [];
      state.loadingNetworkPrinters = false;
      renderNetworkPrinters();
    })
    .catch((err) => {
      state.loadingNetworkPrinters = false;
      state.networkPrinters = [];
      renderNetworkPrinters(err);
    });
  return state.networkPrintersPromise;
}

function renderNetworkPrinters(error = null) {
  const listEl = $("#network-printer-list");
  const statsEl = $("#network-printer-stats");
  if (!listEl) return;

  if (error) {
    listEl.innerHTML = `<p class="muted" style="color:var(--err)">Pencarian mDNS gagal: ${esc(errorMessage(error))}</p>`;
    if (statsEl) statsEl.innerHTML = "";
    return;
  }

  if (state.loadingNetworkPrinters && !state.networkPrinters) {
    listEl.innerHTML = '<p class="muted">Sedang memindai jaringan Wi-Fi/LAN (Bonjour / mDNS & Active LAN)…</p>';
    if (statsEl) statsEl.innerHTML = "";
    return;
  }

  const list = state.networkPrinters || [];
  if (list.length === 0) {
    listEl.innerHTML = '<p class="muted">Belum ada printer jaringan ditemukan. Pastikan printer jaringan menyala dan berada pada Wi-Fi/LAN yang sama dengan PC ini.</p>';
    if (statsEl) statsEl.innerHTML = "";
    return;
  }

  const pdfCount = list.filter((p) => (p.pdl || []).includes("application/pdf")).length;
  if (statsEl) {
    statsEl.innerHTML = `
      <div class="printer-stat-chip">Printer Jaringan: <b>${list.length}</b></div>
      <div class="printer-stat-chip">Mendukung PDF Direct: <b>${pdfCount}</b></div>
    `;
  }

  const installedQueues = (state.printerList || []).map((lp) => normalizePrinterName(lp.name));

  listEl.innerHTML = list
    .map((p, idx) => {
      const supportsPDF = (p.pdl || []).includes("application/pdf");
      const formats = (p.pdl || [])
        .map((f) => f.replace("application/", "").replace("image/", ""))
        .join(", ");

      const normNetName = normalizePrinterName(p.name);
      const normModel = normalizePrinterName(p.model || "");
      const isInstalled = installedQueues.some(
        (q) => q === normNetName || (normModel && q === normModel) || (q.length > 3 && normNetName.includes(q)) || (normNetName.length > 3 && q.includes(normNetName))
      );

      return `
        <div class="printer-card" data-printer-uri="${esc(p.uri)}">
          <div class="printer-card-header">
            <div class="printer-name-group">
              <span class="printer-name">${esc(p.name)}</span>
              <span class="badge badge-network">Wi-Fi / LAN</span>
              <span class="badge ${supportsPDF ? "badge-ready" : "badge-virtual"}">${supportsPDF ? "PDF Direct" : "Raster / URF"}</span>
              <span class="badge badge-usb">Port ${esc(String(p.port))}</span>
            </div>
            ${isInstalled ? `
              <span class="badge badge-ready" style="padding:4px 9px;font-size:0.8rem;display:inline-flex;align-items:center;gap:4px">
                ${ICONS.check} Terpasang di OS
              </span>
            ` : ""}
          </div>
          <div class="printer-details">
            <span>Model: <strong>${esc(p.model || p.name)}</strong></span>
            <span>Host: <strong>${esc(p.host)}</strong></span>
            ${p.ip ? `<span>IP: <strong>${esc(p.ip)}</strong></span>` : ""}
            <span title="${esc(p.uri)}">URI: <code>${esc(p.uri)}</code></span>
            ${formats ? `<span class="muted" style="font-size:0.8rem">Format: ${esc(formats)}</span>` : ""}
          </div>
          <div class="printer-actions" style="margin-top:10px">
            ${!isInstalled ? `
              <button class="secondary" data-action="install-device-printer" data-index="${idx}" title="Pasang printer ini langsung ke pengaturan antrean sistem operasi (OS) komputer ini">
                ${ICONS.plus} Pasang ke Komputer Ini
              </button>
            ` : ""}
            <button class="primary" data-action="test-network-printer" data-index="${idx}">
              ${ICONS.printer} Tes Cetak Langsung (${p.port === 9100 ? "RAW" : "IPP"})
            </button>
            <button data-action="copy-printer-uri" data-index="${idx}" title="Salin URI printer untuk didaftarkan ke aplikasi RSISA Farmasi">
              ${ICONS.copy} Salin Alamat URI
            </button>
          </div>
        </div>
      `;
    })
    .join("");
}

function renderOrigins() {
  const list = $("#origin-list");
  if (!list) return;
  $("#btn-origin-save").disabled = !state.config || state.savingOrigin;
  if (!state.config) {
    list.innerHTML = '<p class="muted">Memuat alamat dari konfigurasi…</p>';
    return;
  }
  if (state.origins.length === 0) {
    list.innerHTML =
      '<p class="muted">Belum ada alamat yang diizinkan. Tambahkan alamat aplikasi farmasi Anda.</p>';
  } else {
    const typeLabel = {
      local: "lokal",
      ip: "IP",
      domain: "domain",
      subdomain: "subdomain",
    };
    list.innerHTML = state.origins
      .map(
        (o, i) => `
      <div class="origin-item">
        <span class="origin-text">${esc(o.origin)}</span>
        <span class="tag">${esc(typeLabel[o.type] || o.type)}</span>
        <button class="icon-btn" data-action="origin-edit" data-index="${i}">Ubah</button>
        <button class="icon-btn danger" data-action="origin-remove" data-index="${i}">Hapus</button>
      </div>`
      )
      .join("");
  }
  const summary = $("#origin-summary");
  if (summary) {
    summary.textContent = `${state.origins.length} alamat diizinkan. Hanya origin persis (skema + host + port) yang diterima.`;
  }
}

function renderSettings() {
  const cfg = state.config;
  const st = state.settings;
  if (!$("#listen-host")) return;
  if (cfg) {
    $("#listen-host").value = cfg.listen_host || "127.0.0.1";
    $("#listen-port").value = String(cfg.listen_port || 17891);
  }
  if (st) {
    $("#set-start-service").checked = !!st.start_service_on_launch;
    $("#set-open-window").checked = !!st.open_window_on_launch;
    $("#set-hide-close").checked = !!st.hide_window_on_close;
    $("#set-notify").checked = !!st.notify_on_error;
    $("#set-confirm-quit").checked = !!st.confirm_quit;
    $("#set-autostart").checked = !!state.autostart;
  }
  const json = $("#cfg-json");
  if (json && cfg) json.textContent = cfg.json || "(kosong)";
  const ver = $("#cfg-version");
  if (ver && cfg) ver.textContent = cfg.version || "—";
}

function renderAll() {
  renderStatus();
  renderOrigins();
  renderSettings();
}

// ------------------------------------------------------------------- banner

function showBanner(id, message, ok) {
  const el = $(id);
  if (!el) return;
  el.textContent = message;
  el.className = `banner show ${ok ? "ok" : "err"}`;
}

function hideBanner(id) {
  const el = $(id);
  if (el) el.className = "banner";
}

// --------------------------------------------------------------------- log

function appendLog(entry) {
  const view = $("#log-view");
  if (!view || !entry) return;
  const line = document.createElement("div");
  line.className = `log-line ${esc(entry.level || "INFO")}`;
  const t = document.createElement("span");
  t.className = "t";
  t.textContent = formatTime(entry.time);
  const lv = document.createElement("span");
  lv.className = "lv";
  lv.textContent = entry.level || "INFO";
  const m = document.createElement("span");
  m.textContent = entry.message || "";
  line.append(t, lv, m);
  view.appendChild(line);
  // Buang baris tertua agar DOM tetap 200 baris (memori terikat).
  while (view.childElementCount > MAX_LOG_LINES) {
    view.removeChild(view.firstElementChild);
  }
  if (state.autoscroll) {
    view.scrollTop = view.scrollHeight;
  }
}

// ------------------------------------------------------------------ uptime

// Satu-satunya timer di UI: hanya memperbarui teks uptime selama layanan
// berjalan, dan otomatis mati saat berhenti.
function startUptimeTick() {
  const running = state.service && state.service.status === "running";
  if (running && !state.uptimeTimer) {
    state.uptimeTimer = window.setInterval(() => {
      if (state.service) state.service.uptime_seconds += 1;
      renderBeranda();
    }, 1000);
  } else if (!running && state.uptimeTimer) {
    clearInterval(state.uptimeTimer);
    state.uptimeTimer = 0;
  }
}

// ------------------------------------------------------------------- aksi

async function withBusy(button, fn) {
  if (button) button.disabled = true;
  try {
    await fn();
  } catch (err) {
    console.error(err);
  } finally {
    if (button) button.disabled = false;
    renderStatus();
  }
}

async function doToggleService(button) {
  await withBusy(button, async () => {
    await ToggleService();
  });
}

async function doTestConnection() {
  try {
    const result = await TestConnection();
    showBanner(
      "#banner",
      `${result.message} (${result.latency_ms} ms)`,
      result.ok
    );
  } catch (err) {
    showBanner("#banner", String(err), false);
  }
}

async function doDiagnosePort(button) {
  await withBusy(button, async () => {
    try {
      const result = await DiagnosePort();
      const detail = result.in_use
        ? `${result.message} Tutup aplikasi tersebut atau ubah port layanan.`
        : result.message;
      showBanner("#banner", detail, !result.in_use);
    } catch (err) {
      showBanner("#banner", `Pemeriksaan pemakai port gagal: ${errorMessage(err)}`, false);
    }
  });
}

async function doOriginSave() {
  if (!state.config || state.savingOrigin) return;
  state.savingOrigin = true;
  $("#btn-origin-save").disabled = true;
  const scheme = $("#origin-scheme").value;
  const host = $("#origin-host").value.trim();
  const port = $("#origin-port").value.trim();
  hideBanner("#origin-banner");
  try {
    if (state.editingIndex >= 0) {
      await UpdateOrigin(state.editingIndex, scheme, host, port);
    } else {
      await AddOrigin(scheme, host, port);
    }
    resetOriginForm();
    await reloadConfigData();
    showBanner("#origin-banner", "Alamat disimpan.", true);
  } catch (err) {
    await reloadConfigData().catch(() => {});
    showBanner("#origin-banner", errorMessage(err), false);
  } finally {
    state.savingOrigin = false;
    renderOrigins();
  }
}

function resetOriginForm() {
  state.editingIndex = -1;
  $("#origin-form-title").textContent = "Tambah Alamat Diizinkan";
  $("#btn-origin-save").textContent = "Tambah";
  $("#btn-origin-cancel").style.display = "none";
  $("#origin-scheme").value = "https";
  $("#origin-host").value = "";
  $("#origin-port").value = "";
}

function doOriginEdit(index) {
  const o = state.origins[index];
  if (!o) return;
  state.editingIndex = index;
  $("#origin-form-title").textContent = "Ubah Alamat";
  $("#btn-origin-save").textContent = "Simpan Perubahan";
  $("#btn-origin-cancel").style.display = "";
  $("#origin-scheme").value = o.scheme;
  $("#origin-host").value = o.host;
  $("#origin-port").value = o.port ? String(o.port) : "";
  hideBanner("#origin-banner");
  $("#origin-host").focus();
}

async function doOriginRemove(index) {
  hideBanner("#origin-banner");
  try {
    await RemoveOrigin(index);
    resetOriginForm();
    await reloadConfigData();
  } catch (err) {
    showBanner("#origin-banner", String(err), false);
  }
}

async function doSaveListen() {
  const host = $("#listen-host").value.trim();
  const port = parseInt($("#listen-port").value.trim(), 10);
  hideBanner("#settings-banner");
  try {
    await SetListen(host, port, true);
    state.config = await GetConfig();
    state.service = await GetState();
    renderAll();
    showBanner("#settings-banner", "Alamat dengarkan disimpan.", true);
  } catch (err) {
    showBanner("#settings-banner", String(err), false);
  }
}

async function doSaveSettings() {
  hideBanner("#settings-banner");
  try {
    const settings = {
      start_service_on_launch: $("#set-start-service").checked,
      open_window_on_launch: $("#set-open-window").checked,
      hide_window_on_close: $("#set-hide-close").checked,
      notify_on_error: $("#set-notify").checked,
      confirm_quit: $("#set-confirm-quit").checked,
    };
    await SaveSettings(settings);
    state.settings = settings;
    showBanner("#settings-banner", "Preferensi disimpan.", true);
  } catch (err) {
    showBanner("#settings-banner", String(err), false);
  }
}

async function doSetAutostart() {
  hideBanner("#settings-banner");
  try {
    await SetAutostart($("#set-autostart").checked);
    state.autostart = await GetAutostart();
    $("#set-autostart").checked = !!state.autostart;
  } catch (err) {
    $("#set-autostart").checked = !!state.autostart;
    showBanner("#settings-banner", String(err), false);
  }
}

// ------------------------------------------------------------------ event

function bindEvents() {
  // Delegasi satu listener untuk seluruh tombol ber-data-action.
  document.addEventListener("click", async (ev) => {
    const btn = ev.target.closest("[data-action]");
    if (!btn || btn.disabled) return;
    const index = parseInt(btn.dataset.index || "-1", 10);
    switch (btn.dataset.action) {
      case "toggle-service":
        doToggleService(btn);
        break;
      case "restart-service":
        withBusy(btn, () => RestartService());
        break;
      case "test-connection":
        doTestConnection();
        break;
      case "diagnose-port":
        doDiagnosePort(btn);
        break;
      case "copy-address":
        withBusy(btn, async () => {
          await CopyServerAddress();
          showBanner("#banner", "Alamat disalin ke papan klip.", true);
        });
        break;
      case "open-url":
        withBusy(btn, () => OpenServerURL());
        break;
      case "open-folder":
        withBusy(btn, () => OpenConfigFolder());
        break;
      case "refresh-origins":
        withBusy(btn, async () => {
          try {
            await ReloadConfig();
            resetOriginForm();
            await reloadConfigData();
            hideBanner("#origin-banner");
          } catch (err) {
            showBanner("#origin-banner", errorMessage(err), false);
          }
        });
        break;
      case "origin-cancel":
        resetOriginForm();
        hideBanner("#origin-banner");
        break;
      case "origin-edit":
        doOriginEdit(index);
        break;
      case "origin-remove":
        doOriginRemove(index);
        break;
      case "save-listen":
        doSaveListen();
        break;
      case "save-settings":
        doSaveSettings();
        break;
      case "import-config":
        withBusy(btn, async () => {
          await ImportConfig();
          await reloadConfigData();
          showBanner("#settings-banner", "Konfigurasi diimpor.", true);
        });
        break;
      case "export-config":
        withBusy(btn, () => ExportConfig());
        break;
      case "reload-config":
        withBusy(btn, async () => {
          await ReloadConfig();
          await reloadConfigData();
          showBanner("#settings-banner", "Konfigurasi dimuat ulang.", true);
        });
        break;
      case "reset-config":
        withBusy(btn, async () => {
          await ResetConfig();
          await reloadConfigData();
          showBanner("#settings-banner", "Konfigurasi dikembalikan ke default.", true);
        });
        break;
      case "clear-logs":
        withBusy(btn, async () => {
          await ClearLogs();
          $("#log-view").replaceChildren();
        });
        break;
      case "refresh-printers":
        withBusy(btn, async () => {
          hideBanner("#printer-banner");
          loadPrinters(true);
        });
        break;
      case "refresh-network-printers":
        withBusy(btn, async () => {
          hideBanner("#network-printer-banner");
          loadNetworkPrinters(true);
        });
        break;
      case "test-network-printer": {
        const idx = parseInt(btn.dataset.index, 10);
        const p = state.networkPrinters && state.networkPrinters[idx];
        if (!p) break;
        const oldHTML = btn.innerHTML;
        btn.disabled = true;
        btn.innerHTML = `${ICONS.printer} Mengirim…`;
        hideBanner("#network-printer-banner");
        try {
          await TestPrinter(p.name, p.uri, "bw");
          showBanner(
            "#network-printer-banner",
            `Uji cetak berhasil dikirim langsung via IPP ke "${p.name}" (${p.uri}). Silakan periksa hasil cetakan printer fisik.`,
            true
          );
        } catch (err) {
          showBanner(
            "#network-printer-banner",
            `Uji cetak ke "${p.name}" gagal: ${errorMessage(err)}`,
            false
          );
        } finally {
          btn.disabled = false;
          btn.innerHTML = oldHTML;
        }
        break;
      }
      case "install-device-printer": {
        const idx = parseInt(btn.dataset.index, 10);
        const p = state.networkPrinters && state.networkPrinters[idx];
        if (!p) break;
        const oldHTML = btn.innerHTML;
        btn.disabled = true;
        btn.innerHTML = `${ICONS.plus} Memasang ke OS…`;
        hideBanner("#network-printer-banner");
        try {
          await InstallNetworkPrinter(p.name, p.uri, p.model || p.name);
          showBanner(
            "#network-printer-banner",
            `Printer "${p.name}" berhasil dipasang ke sistem operasi komputer ini! Antrean sekarang tersedia di antrean OS lokal.`,
            true
          );
          await loadPrinters(true);
          renderNetworkPrinters();
        } catch (err) {
          showBanner(
            "#network-printer-banner",
            `Gagal memasang printer "${p.name}": ${errorMessage(err)}`,
            false
          );
          btn.disabled = false;
          btn.innerHTML = oldHTML;
        }
        break;
      }
      case "copy-printer-uri": {
        const idx = parseInt(btn.dataset.index, 10);
        const p = state.networkPrinters && state.networkPrinters[idx];
        if (!p || !p.uri) break;
        try {
          if (navigator.clipboard) {
            await navigator.clipboard.writeText(p.uri);
          }
          showBanner(
            "#network-printer-banner",
            `URI "${p.uri}" disalin ke papan klip! Anda dapat mendaftarkannya di pengaturan web RSISA.`,
            true
          );
        } catch (err) {
          showBanner("#network-printer-banner", `Gagal menyalin: ${errorMessage(err)}`, false);
        }
        break;
      }
      case "apply-printer-port": {
        const idx = parseInt(btn.dataset.index, 10);
        const p = state.printerList && state.printerList[idx];
        if (!p) break;
        const select = $(`#select-port-${idx}`);
        const newPort = select ? select.value : "";
        if (!newPort) break;
        const oldText = btn.textContent;
        btn.disabled = true;
        btn.textContent = "Menyimpan…";
        hideBanner("#printer-banner");
        SetPrinterPort(p.name, newPort)
          .then(() => {
            showBanner(
              "#printer-banner",
              `Port printer "${p.name}" berhasil dialihkan ke "${newPort}".`,
              true
            );
            loadPrinters(true);
          })
          .catch((err) => {
            showBanner(
              "#printer-banner",
              `Gagal mengubah port "${p.name}": ${errorMessage(err)}`,
              false
            );
          })
          .finally(() => {
            btn.disabled = false;
            btn.textContent = oldText;
          });
        break;
      }
      case "clear-printer-queue": {
        const idx = parseInt(btn.dataset.index, 10);
        const p = state.printerList && state.printerList[idx];
        if (!p) break;
        const oldText = btn.textContent;
        btn.disabled = true;
        btn.textContent = "Membersihkan…";
        hideBanner("#printer-banner");
        ClearPrinterQueue(p.name)
          .then(() => {
            showBanner(
              "#printer-banner",
              `Seluruh antrean cetak macet pada "${p.name}" berhasil dibersihkan.`,
              true
            );
            loadPrinters(true);
          })
          .catch((err) => {
            showBanner(
              "#printer-banner",
              `Gagal membersihkan antrean "${p.name}": ${errorMessage(err)}`,
              false
            );
          })
          .finally(() => {
            btn.disabled = false;
            btn.textContent = oldText;
          });
        break;
      }
      case "toggle-port-settings": {
        const idx = btn.dataset.index;
        const portBar = $(`#port-bar-${idx}`);
        if (portBar) {
          portBar.style.display = portBar.style.display === "none" ? "flex" : "none";
        }
        break;
      }
      case "test-auto-printer":
      case "test-raw-printer":
      case "test-doc-printer": {
        const action = btn.dataset.action;
        const idx = parseInt(btn.dataset.index, 10);
        const p = state.printerList && state.printerList[idx];
        if (!p) break;
        let mode = "auto";
        if (action === "test-raw-printer") mode = "raw";
        else if (action === "test-doc-printer") mode = "bw";
        const oldHTML = btn.innerHTML;
        btn.disabled = true;
        btn.innerHTML = `${ICONS.printer} Mengirim…`;
        hideBanner("#printer-banner");
        try {
          // Tentukan mode dinamis: jika thermal gunakan raw (ESC/POS), jika printer biasa gunakan bw (GDI Dokumen)
          let finalMode = mode;
          if (finalMode === "auto") {
            const isThermal = p.printer_type === "thermal" || /POS|THERMAL|RECEIPT|STRUK|KASIR|58MM|80MM|XP-|TM-/i.test(p.name + " " + (p.driver || ""));
            finalMode = isThermal ? "raw" : "bw";
          }

          // Kirim langsung via HTTP lokal ke print server
          const res = await fetch("http://127.0.0.1:17891/printers/test", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ name: p.name, mode: finalMode, paper: "A4" }),
          });
          if (!res.ok) {
            const errData = await res.json().catch(() => ({}));
            throw new Error(errData.message || `HTTP ${res.status}`);
          }
          showBanner(
            "#printer-banner",
            `Uji cetak (${finalMode === "raw" ? "Struk Thermal" : "Dokumen Halaman Kertas"}) berhasil dikirim ke antrean "${p.name}". Silakan periksa hasil cetakan printer fisik.`,
            true
          );
        } catch (fetchErr) {
          // Fallback via RPC Wails jika fetch langsung gagal
          try {
            await TestPrinter(p.name, p.uri, mode);
            showBanner(
              "#printer-banner",
              `Uji cetak berhasil dikirim ke antrean "${p.name}".`,
              true
            );
          } catch (rpcErr) {
            showBanner(
              "#printer-banner",
              `Uji cetak ke "${p.name}" gagal: ${errorMessage(rpcErr || fetchErr)}`,
              false
            );
          }
        } finally {
          btn.disabled = false;
          btn.innerHTML = oldHTML;
          GetLogs().then((logs) => {
            const view = $("#log-view");
            if (view && logs) {
              view.replaceChildren();
              for (const entry of logs) appendLog(entry);
            }
          }).catch(() => {});
        }
        break;
      }
      case "show-panel":
        withBusy(btn, () => ShowPanel());
        break;
      case "quit":
        QuitApp();
        break;
      case "uninstall":
        withBusy(btn, async () => {
          try {
            await UninstallApp();
          } catch (err) {
            showBanner("#uninstall-banner", errorMessage(err), false);
          }
        });
        break;
    }
  });

  // Enter di form origin langsung menyimpan.
  $("#form-origin")?.addEventListener("submit", (ev) => {
    ev.preventDefault();
    doOriginSave();
  });

  $("#set-autostart")?.addEventListener("change", doSetAutostart);

  $("#log-autoscroll")?.addEventListener("change", (ev) => {
    state.autoscroll = ev.target.checked;
  });

  // Tab switching (khusus tampilan panel).
  const switchTab = (tab) => {
    $("#tabs")?.querySelectorAll("button").forEach((b) => {
      b.classList.toggle("active", b.dataset.tab === tab);
    });
    document.querySelectorAll(".tab-pane").forEach((p) => {
      p.classList.toggle("active", p.id === `pane-${tab}`);
    });
    hideBanner("#banner");
    hideBanner("#origin-banner");
    hideBanner("#settings-banner");
    hideBanner("#printer-banner");
    if (tab === "printers") {
      if (!state.printerList || state.printerList.length === 0) {
        loadPrinters();
      }
      if (!state.networkPrinters || state.networkPrinters.length === 0) {
        loadNetworkPrinters();
      }
    }
  };

  $("#tabs")?.addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-tab]");
    if (!btn) return;
    switchTab(btn.dataset.tab);
  });

  $("#printer-subtabs")?.addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-subtab]");
    if (!btn) return;
    const subtab = btn.dataset.subtab;
    document.querySelectorAll("#printer-subtabs button").forEach((b) => b.classList.remove("active"));
    btn.classList.add("active");
    const osView = $("#printer-os-view");
    const bonjourView = $("#printer-bonjour-view");
    if (subtab === "printer-os") {
      if (osView) osView.style.display = "flex";
      if (bonjourView) bonjourView.style.display = "none";
    } else if (subtab === "printer-bonjour") {
      if (osView) osView.style.display = "none";
      if (bonjourView) bonjourView.style.display = "flex";
      if (!state.networkPrinters || state.networkPrinters.length === 0) {
        loadNetworkPrinters();
      }
    }
  });

  document.addEventListener("click", (ev) => {
    const gotoBtn = ev.target.closest("[data-tab-goto]");
    if (gotoBtn) {
      ev.preventDefault();
      switchTab(gotoBtn.dataset.tabGoto);
    }
  });
}

async function reloadConfigData() {
  state.config = await GetConfig();
  state.origins = state.config.origins || [];
  renderAll();
}

// ------------------------------------------------------------------ shell

function renderMainShell() {
  $("#app").innerHTML = `
    <div class="topbar">
      <div class="logo" aria-hidden="true"><img src="/aquila-icon.png" alt=""></div>
      <h1>RSISA Printer Server</h1>
      <div class="pill"><span class="dot" id="pill-dot"></span><span id="pill-text">…</span></div>
    </div>
    <div class="tabs" id="tabs">
      <button data-tab="beranda" class="active">Beranda</button>
      <button data-tab="printers">Printer</button>
      <button data-tab="origin">Alamat Diizinkan</button>
      <button data-tab="settings">Pengaturan</button>
      <button data-tab="log">Log</button>
      <button data-tab="about">Tentang RSISA Printer Server</button>
    </div>
    <div class="content">
      <div class="tab-pane active" id="pane-beranda">
        <div class="card">
          <h2>Status Layanan</h2>
          <div class="kv"><span class="k">Status</span><span class="v" id="bd-status">…</span></div>
          <div class="kv"><span class="k">Alamat dengarkan</span><span class="v" id="bd-listen">…</span></div>
          <div class="kv"><span class="k">Kesehatan</span><span class="v" id="bd-health">…</span></div>
          <div class="kv"><span class="k">Aktif selama</span><span class="v" id="bd-uptime">—</span></div>
          <div class="kv"><span class="k">Printer terdeteksi</span><span class="v" id="bd-printers">Memuat…</span></div>
          <div class="kv"><span class="k">File konfigurasi</span><span class="v" id="bd-path">…</span></div>
          <div class="banner" id="service-error" role="alert"></div>
        </div>
        <div class="card">
          <h2>Kendali</h2>
          <div class="banner" id="banner"></div>
          <div class="row" style="margin-bottom:8px">
            <button class="primary" id="btn-toggle" data-action="toggle-service">…</button>
            <button id="btn-restart" data-action="restart-service">Muat Ulang Layanan</button>
            <button id="btn-test" data-action="test-connection">Tes Koneksi</button>
            <button id="btn-diagnose-port" data-action="diagnose-port">Cek Pemakai Port</button>
          </div>
          <div class="row">
            <button data-action="copy-address">Salin Alamat</button>
            <button id="btn-open-url" data-action="open-url">Buka /health di Peramban</button>
            <button data-action="open-folder">Buka Folder Konfigurasi</button>
          </div>
        </div>
        <div class="card">
          <h2>Cara Pakai Singkat</h2>
          <p class="muted">Aplikasi farmasi mengirim dokumen PDF ke print server ini lewat HTTP,
          lalu diteruskan ke antrean printer PC. Pastikan alamat aplikasi farmasi sudah terdaftar di
          tab <b>Alamat Diizinkan</b>. Tombol <b>Tes Koneksi</b> memastikan print server merespons
          sebelum dipakai.</p>
        </div>
      </div>

      <div class="tab-pane" id="pane-printers">
        <div class="sub-tabs" id="printer-subtabs">
          <button class="active" data-subtab="printer-os">
            ${ICONS.printer} Printer OS
          </button>
          <button data-subtab="printer-bonjour">
            ${ICONS.search} Printer Bonjour
          </button>
        </div>

        <div class="printer-subview" id="printer-os-view">
          <div class="card">
            <div class="section-heading">
              <h2>Daftar Printer Antrean OS (Ready)</h2>
              <button data-action="refresh-printers">${ICONS.search} Deteksi Ulang</button>
            </div>
            <p class="muted">Antrean printer yang terpasang pada PC ini dan berstatus <b>Ready</b> (siap digunakan). Printer offline disembunyikan otomatis.</p>
            <div class="printer-stats" id="printer-stats" style="margin-top:12px"></div>
            <div class="banner" id="printer-banner" style="margin-top:8px"></div>
            <div class="printer-grid" id="printer-list" style="margin-top:12px">
              <p class="muted">Mendeteksi printer terpasang di OS…</p>
            </div>
          </div>
        </div>

        <div class="printer-subview" id="printer-bonjour-view" style="display:none">
          <div class="card">
            <div class="section-heading">
              <h2>Pencarian Jaringan (Bonjour / mDNS & Active LAN)</h2>
              <button data-action="refresh-network-printers" class="secondary">${ICONS.search} Pindai Jaringan</button>
            </div>
            <p class="muted">Seluruh printer jaringan (IPP, IPPS, dan RAW Port 9100) yang terdeteksi di subnet Wi-Fi/LAN lokal. Printer ini dapat langsung diuji cetak, dipasang ke sistem operasi, atau disalin URI-nya untuk RSISA Farmasi.</p>
            <div class="printer-stats" id="network-printer-stats" style="margin-top:12px"></div>
            <div class="banner" id="network-printer-banner" style="margin-top:8px"></div>
            <div class="printer-grid" id="network-printer-list" style="margin-top:12px">
              <p class="muted">Memindai printer di jaringan via Bonjour/mDNS & Active LAN…</p>
            </div>
          </div>
        </div>
      </div>

      <div class="tab-pane" id="pane-origin">
        <div class="card">
          <div class="section-heading"><h2>Daftar Alamat</h2><button data-action="refresh-origins">Muat Ulang</button></div>
          <p class="muted" id="origin-summary"></p>
          <div id="origin-list" style="margin-top:10px"></div>
        </div>
        <div class="card">
          <h2 id="origin-form-title">Tambah Alamat Diizinkan</h2>
          <form id="form-origin">
            <div class="row grow">
              <select id="origin-scheme" aria-label="Skema">
                <option value="https">https</option>
                <option value="http">http</option>
              </select>
              <input id="origin-host" placeholder="contoh: farmasi.example.test atau 192.168.10.25" aria-label="Host" />
              <input id="origin-port" placeholder="port (opsional)" inputmode="numeric" style="width:120px" aria-label="Port" />
            </div>
            <div class="row" style="margin-top:10px">
              <button type="submit" class="primary" id="btn-origin-save" disabled>Tambah</button>
              <button type="button" id="btn-origin-cancel" data-action="origin-cancel" style="display:none">Batal</button>
            </div>
          </form>
          <div class="banner" id="origin-banner" style="margin-top:10px"></div>
        </div>
        <div class="card">
          <h2>Catatan</h2>
          <p class="muted">Alamat asal (origin) adalah skema + host + port aplikasi yang boleh mengakses print server. Contoh:</p>
          <ul class="muted origin-notes">
            <li><code>https://farmasi.example.test</code> — domain atau subdomain</li>
            <li><code>http://localhost:4200</code> — localhost dengan port</li>
            <li><code>http://192.168.10.25:8080</code> — alamat IP dengan port</li>
          </ul>
          <p class="muted">Perubahan berlaku langsung tanpa restart.</p>
        </div>
      </div>

      <div class="tab-pane" id="pane-settings">
        <div class="card">
          <h2>Alamat Dengarkan</h2>
          <div class="row grow">
            <input id="listen-host" placeholder="127.0.0.1" aria-label="Host" />
            <input id="listen-port" placeholder="17891" inputmode="numeric" style="width:110px" aria-label="Port" />
            <button class="primary" data-action="save-listen">Simpan</button>
          </div>
          <p class="muted" style="margin-top:8px">Port harus tetap <code>17891</code> agar aplikasi farmasi dapat terhubung. Gunakan <code>127.0.0.1</code> untuk akses dari PC ini saja, atau <code>0.0.0.0</code> / IP LAN PC (contoh: <code>192.168.1.141</code>) agar dapat diakses oleh PC lain di jaringan LAN dengan mengatur token LAN pada konfigurasi. Layanan otomatis dimuat ulang bila sedang berjalan.</p>
          <div class="banner" id="settings-banner" style="margin-top:10px"></div>
        </div>
        <div class="card">
          <h2>Preferensi Aplikasi</h2>
          <label class="check">
            <input type="checkbox" id="set-start-service" />
            <div>
              <div class="check-label">Mulai layanan saat aplikasi dibuka</div>
              <div class="check-desc">Print server langsung aktif begitu RSISA Printer Server berjalan.</div>
            </div>
          </label>
          <label class="check">
            <input type="checkbox" id="set-open-window" />
            <div>
              <div class="check-label">Buka jendela panel saat aplikasi dibuka</div>
              <div class="check-desc">Bila dimatikan, RSISA Printer Server tetap berjalan di baki sistem.</div>
            </div>
          </label>
          <label class="check">
            <input type="checkbox" id="set-hide-close" />
            <div>
              <div class="check-label">Sembunyikan ke tray saat jendela ditutup</div>
              <div class="check-desc">Layanan tetap berjalan di latar belakang.</div>
            </div>
          </label>
          <label class="check">
            <input type="checkbox" id="set-notify" />
            <div>
              <div class="check-label">Beri tahu bila terjadi kesalahan</div>
            </div>
          </label>
          <label class="check">
            <input type="checkbox" id="set-confirm-quit" />
            <div>
              <div class="check-label">Konfirmasi sebelum keluar</div>
            </div>
          </label>
          <div class="row" style="margin-top:10px">
            <button class="primary" data-action="save-settings">Simpan Preferensi</button>
          </div>
        </div>
        <div class="card">
          <h2>Startup Sistem</h2>
          <label class="check">
            <input type="checkbox" id="set-autostart" />
            <div>
              <div class="check-label">Jalankan RSISA Printer Server saat login</div>
              <div class="check-desc">RSISA Printer Server muncul di baki sistem setiap komputer dinyalakan.</div>
            </div>
          </label>
        </div>
        <div class="card">
          <h2>File Konfigurasi</h2>
          <div class="row">
            <button data-action="import-config">Impor…</button>
            <button data-action="export-config">Ekspor…</button>
            <button data-action="reload-config">Muat Ulang</button>
            <button class="danger" data-action="reset-config">Kembalikan Default</button>
          </div>
          <p class="muted" style="margin-top:10px">Isi print-server.json saat ini:</p>
          <pre class="log-view" id="cfg-json" style="min-height:110px;max-height:200px;margin-top:8px"></pre>
        </div>
        <div class="card">
          <h2>Uninstall RSISA Printer Server</h2>
          <p class="muted">Hentikan layanan dan hapus aplikasi dari komputer ini. Konfigurasi serta daftar alamat tetap disimpan agar dapat dipakai saat instal ulang.</p>
          <button class="danger" data-action="uninstall" style="margin-top:12px">Uninstall RSISA Printer Server…</button>
          <div class="banner" id="uninstall-banner" role="status" style="margin-top:10px"></div>
        </div>
      </div>

      <div class="tab-pane" id="pane-about">
        <div class="card about-card">
          <div class="logo about-logo" aria-hidden="true"><img src="/aquila-icon.png" alt=""></div>
          <h2>RSISA PRINTER SERVER</h2>
          <p class="about-subtitle">Print Server</p>
          <p class="about-version">Versi <span id="cfg-version">…</span></p>
          <div class="about-author">
            <p class="muted">Dikembangkan oleh</p>
            <p class="about-company">CV ANAK BANGSA</p>
            <address>Jl. Ki Jagapati No. 22, RT. 02 RW. 01<br>Kel. Krandegan, Kec. / Kab. Banjarnegara</address>
          </div>
          <p class="about-copyright">© 2026 CV ANAK BANGSA.<br>Seluruh hak cipta dilindungi.</p>
        </div>
      </div>

      <div class="tab-pane" id="pane-log">
        <div class="card" style="display:flex;flex-direction:column;flex:1">
          <h2>Log Layanan</h2>
          <div class="row">
            <button data-action="clear-logs">Bersihkan Log</button>
            <label class="check" style="padding:0">
              <input type="checkbox" id="log-autoscroll" checked />
              <div class="check-label">Gulir otomatis</div>
            </label>
          </div>
          <div class="log-view" id="log-view" style="margin-top:12px"></div>
        </div>
      </div>
    </div>`;
}

function renderPopupShell() {
  $("#app").innerHTML = `
    <div class="popup">
      <div class="status-block" data-wails-drag>
        <div class="big-dot" id="pp-dot"></div>
        <div>
          <div class="status-text" id="pp-status">…</div>
          <div class="muted" id="pp-listen">…</div>
        </div>
      </div>
      <div class="meta">
        <div id="pp-uptime"></div>
        <div id="pp-health"></div>
        <div id="pp-printers"></div>
      </div>
      <div class="actions">
        <button class="primary" id="pp-toggle" data-action="toggle-service">…</button>
        <button data-action="show-panel">Buka Panel</button>
        <button class="danger" data-action="quit">Keluar</button>
      </div>
    </div>`;
}

// ------------------------------------------------------------------- init

async function init() {
  if (isPopup) {
    document.title = "RSISA Printer Server";
    renderPopupShell();
  } else {
    renderMainShell();
  }
  bindEvents();
  renderOrigins();

  // Event dari Go: hanya perbarui bagian yang berubah, tanpa polling.
  Events.On(AquilaState, (ev) => {
    state.service = ev.data;
    renderStatus();
  });
  Events.On(AquilaLog, (ev) => {
    appendLog(ev.data);
  });

  // Konfigurasi tetap tampil meski pemuatan log atau preferensi gagal.
  await Promise.allSettled([
    reloadConfigData().catch((err) => {
      const list = $("#origin-list");
      if (list) list.textContent = "Daftar alamat belum berhasil dimuat. Klik Muat Ulang untuk mencoba lagi.";
      showBanner("#origin-banner", errorMessage(err), false);
    }),
    GetState().then((svc) => { state.service = svc; renderStatus(); }),
    GetSettings().then((settings) => { state.settings = settings; renderSettings(); }),
    GetLogs().then((logs) => { for (const entry of logs) appendLog(entry); }),
  ]);

  if (!isPopup) {
    try {
      state.autostart = await GetAutostart();
    } catch {
      /* autostart tidak didukung di platform ini */
    }
    if (state.config && state.config.created) {
      showBanner(
        "#banner",
        `File konfigurasi baru dibuat di ${state.config.path}. Periksa tab Alamat Diizinkan.`,
        true
      );
    }
    // Muat printer di latar belakang.
    await loadPrinters();
    loadNetworkPrinters();
    renderSettings();
  } else {
    GetPrinterCount()
      .then((n) => {
        state.printers = n;
        const el = $("#pp-printers");
        if (el) el.textContent = `${n} printer terdeteksi`;
      })
      .catch(() => {});
  }

  renderStatus();
}

init();
