/**
 * CLEAR Medical Coding Platform — Coding Workspace
 * Features:
 *  - Dynamic rows for CPT codes (Add, Remove, Reorder, RVU subtotal)
 *  - Dynamic rows for ICD-10 Diagnosis codes (Add, Remove, Auto-priority, POA indicator)
 *  - E&M Medical Decision Making (MDM) level selection
 *  - Suspend / Clinical Query Modal
 *  - Native Form / JSON payload submission
 */

document.addEventListener('DOMContentLoaded', () => {
  initCptRows();
  initDxRows();
  initSuspendModal();
  initEmCalculator();
});

// --- Dynamic CPT Codes Rows ---
function initCptRows() {
  const container = document.getElementById('cpt-rows-container');
  const addBtn = document.getElementById('add-cpt-btn');
  if (!container || !addBtn) return;

  addBtn.addEventListener('click', () => {
    const index = container.querySelectorAll('.cpt-row').length;
    const row = document.createElement('div');
    row.className = 'cpt-row dynamic-row grid grid-cols-12 gap-3 items-center p-3 mb-2 bg-slate-900/60 rounded-lg border border-slate-800';
    row.innerHTML = `
      <div class="col-span-3">
        <label class="text-xs text-slate-400 block mb-1">CPT Code *</label>
        <input type="text" name="cpt_code[]" class="form-control text-sm font-mono uppercase" placeholder="e.g. 99284" required>
      </div>
      <div class="col-span-2">
        <label class="text-xs text-slate-400 block mb-1">Mod 1</label>
        <input type="text" name="cpt_mod1[]" maxlength="2" class="form-control text-sm font-mono uppercase" placeholder="25">
      </div>
      <div class="col-span-2">
        <label class="text-xs text-slate-400 block mb-1">Mod 2</label>
        <input type="text" name="cpt_mod2[]" maxlength="2" class="form-control text-sm font-mono uppercase" placeholder="59">
      </div>
      <div class="col-span-2">
        <label class="text-xs text-slate-400 block mb-1">Units</label>
        <input type="number" name="cpt_units[]" min="1" max="99" value="1" class="form-control text-sm font-mono">
      </div>
      <div class="col-span-2">
        <label class="text-xs text-slate-400 block mb-1">Fee / RVU</label>
        <span class="text-sm text-slate-300 font-mono block pt-1 rvu-value">--</span>
      </div>
      <div class="col-span-1 text-right pt-4">
        <button type="button" class="btn-icon text-rose-400 hover:text-rose-300 remove-cpt-btn" title="Remove Code">
          <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" /></svg>
        </button>
      </div>
    `;
    container.appendChild(row);
    attachCptEvents(row);
  });

  container.querySelectorAll('.cpt-row').forEach(row => attachCptEvents(row));
}

function attachCptEvents(row) {
  const removeBtn = row.querySelector('.remove-cpt-btn');
  if (removeBtn) {
    removeBtn.addEventListener('click', () => {
      const container = document.getElementById('cpt-rows-container');
      if (container && container.querySelectorAll('.cpt-row').length > 1) {
        row.remove();
      } else {
        // Clear fields if it's the only row
        row.querySelectorAll('input').forEach(inp => inp.value = '');
      }
    });
  }
}

// --- Dynamic ICD-10 Diagnosis Codes Rows ---
function initDxRows() {
  const container = document.getElementById('dx-rows-container');
  const addBtn = document.getElementById('add-dx-btn');
  if (!container || !addBtn) return;

  addBtn.addEventListener('click', () => {
    const index = container.querySelectorAll('.dx-row').length + 1;
    const row = document.createElement('div');
    row.className = 'dx-row dynamic-row grid grid-cols-12 gap-3 items-center p-3 mb-2 bg-slate-900/60 rounded-lg border border-slate-800';
    row.innerHTML = `
      <div class="col-span-1 text-center">
        <label class="text-xs text-slate-400 block mb-1">Seq</label>
        <span class="dx-priority-badge font-mono text-sm px-2 py-1 rounded bg-slate-800 text-indigo-400 font-bold">${index}</span>
        <input type="hidden" name="dx_priority[]" value="${index}">
      </div>
      <div class="col-span-4">
        <label class="text-xs text-slate-400 block mb-1">ICD-10 Code *</label>
        <input type="text" name="dx_code[]" class="form-control text-sm font-mono uppercase" placeholder="e.g. R07.9" required>
      </div>
      <div class="col-span-5">
        <label class="text-xs text-slate-400 block mb-1">POA (Present on Admission)</label>
        <select name="dx_poa[]" class="form-control text-sm">
          <option value="Y" selected>Y - Yes (Present at admission)</option>
          <option value="N">N - No (Not present at admission)</option>
          <option value="U">U - Unknown / Insufficient documentation</option>
          <option value="W">W - Clinically Undetermined</option>
          <option value="1">1 - Exempt from POA reporting</option>
        </select>
      </div>
      <div class="col-span-2 text-right pt-4">
        <button type="button" class="btn-icon text-rose-400 hover:text-rose-300 remove-dx-btn" title="Remove Diagnosis">
          <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" /></svg>
        </button>
      </div>
    `;
    container.appendChild(row);
    attachDxEvents(row);
    reindexDxPriorities();
  });

  container.querySelectorAll('.dx-row').forEach(row => attachDxEvents(row));
}

function attachDxEvents(row) {
  const removeBtn = row.querySelector('.remove-dx-btn');
  if (removeBtn) {
    removeBtn.addEventListener('click', () => {
      const container = document.getElementById('dx-rows-container');
      if (container && container.querySelectorAll('.dx-row').length > 1) {
        row.remove();
        reindexDxPriorities();
      } else {
        row.querySelectorAll('input:not([type=hidden])').forEach(inp => inp.value = '');
      }
    });
  }
}

function reindexDxPriorities() {
  const container = document.getElementById('dx-rows-container');
  if (!container) return;
  container.querySelectorAll('.dx-row').forEach((row, i) => {
    const seq = i + 1;
    const badge = row.querySelector('.dx-priority-badge');
    const input = row.querySelector('input[name="dx_priority[]"]');
    if (badge) badge.textContent = seq;
    if (input) input.value = seq;
  });
}

// --- E&M MDM Calculator & Selector ---
function initEmCalculator() {
  const mdmSelect = document.getElementById('em_mdm_level');
  const emCodeSelect = document.getElementById('em_code');
  if (!mdmSelect || !emCodeSelect) return;

  const mdmToCodeMap = {
    'Straightforward': '99281',
    'Low': '99282',
    'Moderate': '99283',
    'High': '99285',
  };

  mdmSelect.addEventListener('change', () => {
    const selected = mdmSelect.value;
    if (mdmToCodeMap[selected]) {
      emCodeSelect.value = mdmToCodeMap[selected];
    }
  });
}

// --- Suspend Modal Management ---
function initSuspendModal() {
  const openModalBtn = document.getElementById('open-suspend-modal');
  const modal = document.getElementById('suspend-modal');
  const closeBtn = document.getElementById('close-suspend-modal');
  const cancelBtn = document.getElementById('cancel-suspend-modal');

  if (!modal) return;

  if (openModalBtn) {
    openModalBtn.addEventListener('click', () => {
      modal.classList.remove('hidden');
      modal.classList.add('flex');
    });
  }

  const hideModal = () => {
    modal.classList.add('hidden');
    modal.classList.remove('flex');
  };

  if (closeBtn) closeBtn.addEventListener('click', hideModal);
  if (cancelBtn) cancelBtn.addEventListener('click', hideModal);
}
