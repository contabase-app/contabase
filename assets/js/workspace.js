// Init Calls + Workspace/Drawer/Bottom Sheet + ESC Keydown
// Extracted from layout.html — Fase 9.9.5g

// --- Init Calls ---

(function() {
    var csrfMeta = document.querySelector('meta[name="csrf-token"]');
    if (csrfMeta) csrfMeta.setAttribute('content', csrfToken());
})();

// --- Workspace/Drawer ---

window.openWorkspaceDrawer = function() {
    var drawer = document.getElementById('workspaceDrawer');
    var backdrop = document.getElementById('drawerBackdrop');
    if (typeof window.contabaseDebugUI === 'function') window.contabaseDebugUI('openWorkspaceDrawer called. Drawer:', !!drawer, 'Backdrop:', !!backdrop);
    if (!drawer || !backdrop) return;
    drawer.classList.remove('translate-x-full');
    backdrop.classList.remove('opacity-0', 'pointer-events-none');
    backdrop.classList.add('opacity-100');
    if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('workspace-drawer');
    if (typeof window.contabaseDebugUI === 'function') window.contabaseDebugUI('Fetching /workspace/list via HTMX');
    htmx.ajax('GET', '/workspace/list', { target: '#ws-drawer-nav', swap: 'innerHTML' });
};

window.closeWorkspaceDrawer = function() {
    var drawer = document.getElementById('workspaceDrawer');
    var backdrop = document.getElementById('drawerBackdrop');
    if (typeof window.contabaseDebugUI === 'function') window.contabaseDebugUI('closeWorkspaceDrawer called. Drawer:', !!drawer, 'Backdrop:', !!backdrop);
    if (!drawer || !backdrop) return;
    var wasOpen = !drawer.classList.contains('translate-x-full');
    drawer.classList.add('translate-x-full');
    backdrop.classList.add('opacity-0', 'pointer-events-none');
    backdrop.classList.remove('opacity-100');
    if (wasOpen && typeof window.overlayDidClose === 'function') window.overlayDidClose();
};

window.toggleWorkspacePanel = function() {
    var drawer = document.getElementById('workspaceDrawer');
    var isClosed = drawer ? drawer.classList.contains('translate-x-full') : true;
    if (typeof window.contabaseDebugUI === 'function') window.contabaseDebugUI('toggleWorkspacePanel called. Drawer is currently closed?', isClosed);
    if (drawer && !isClosed) {
        window.closeWorkspaceDrawer();
    } else {
        window.openWorkspaceDrawer();
    }
};

window.toggleWorkspaceQuick = function(activeID, firstID, secondID) {
    var targetID = activeID === firstID ? secondID : firstID;
    if (!targetID || targetID === activeID) {
        window.openWorkspaceDrawer();
        return;
    }
    fetch('/workspace/switch', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8',
            'HX-Request': 'true',
            'X-CSRF-Token': csrfToken()
        },
        body: 'workspace_id=' + encodeURIComponent(targetID)
    })
        .then(function(response) {
            if (!response.ok) throw new Error('workspace switch failed');
            window.location.reload();
        })
        .catch(function() {
            showGlobalFeedback('Não foi possível trocar o workspace.', 'error');
        });
};

// --- Bottom Sheet ---

window.closeBottomSheet = function() {
    var el = document.getElementById('bottom-sheet-container');
    var hadContent = el && el.innerHTML.trim() !== '';
    if (el) el.innerHTML = '';
    var picker = document.getElementById('modalPickerBackdrop');
    if (picker) picker.remove();
    if ((hadContent || picker) && typeof window.overlayDidClose === 'function') window.overlayDidClose();
};

// --- Workspace Type UI ---

window.applyWorkspaceTypeUI = function(workspaceType) {
    var isBusiness = workspaceType === 'business';
    var contatos = document.getElementById('nav-contatos');
    if (contatos) contatos.classList.toggle('hidden', !isBusiness);
    var desktopContatos = document.getElementById('desktop-nav-contatos');
    if (desktopContatos) desktopContatos.classList.toggle('hidden', !isBusiness);
    document.querySelectorAll('[data-role-lancamentos]').forEach(function(el) {
        el.textContent = isBusiness ? 'Fluxo de Caixa' : 'Lançamentos';
    });
    document.querySelectorAll('[data-role-salario]').forEach(function(el) {
        el.textContent = isBusiness ? 'Faturamento/Receita' : 'Salário';
    });
};

window.applyWorkspaceVisualTheme = function(payload) {
    if (!payload) return;
    var body = document.body;
    var accent = payload.accent_rgb || '';
    var accentSoft = payload.accent_soft_rgb || '';
    var accentText = payload.accent_text_rgb || '';
    if (!accent || !accentSoft || !accentText) return;
    body.style.setProperty('--ws-accent-rgb', accent);
    body.style.setProperty('--ws-accent-soft-rgb', accentSoft);
    body.style.setProperty('--ws-accent-text-rgb', accentText);
    body.classList.add('workspace-theme-active');
};

window.refreshWorkspaceTypeUI = function() {
    fetch('/workspace/context', { headers: { 'HX-Request': 'true' } })
        .then(function(response) { return response.ok ? response.json() : null; })
        .then(function(payload) {
            if (!payload || !payload.type) return;
            window.applyWorkspaceTypeUI(payload.type);
            window.applyWorkspaceVisualTheme(payload);
        })
        .catch(function() {});
};

// --- Workspace Init ---

window.refreshWorkspaceTypeUI();

// --- ESC Keydown handled in modal-interceptor.js ---
