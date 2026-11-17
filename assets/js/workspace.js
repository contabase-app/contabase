// Init Calls + Workspace/Drawer/Bottom Sheet + ESC Keydown
// Workspace selector interactions shared by the authenticated app shell.

// --- Init Calls ---

(function() {
    var csrfMeta = document.querySelector('meta[name="csrf-token"]');
    if (csrfMeta) csrfMeta.setAttribute('content', csrfToken());
})();

// --- Workspace/Drawer ---

// The drawer is a dialog: while closed it stays out of the accessibility tree
// and out of the tab order (`inert` + `aria-hidden`). Every trigger exposes the
// same expanded state so the contract matches what the user sees.
var _workspaceDrawerOpener = null;

function _workspaceDrawerSetExpanded(expanded) {
    document.querySelectorAll('[data-v2-workspace-trigger]').forEach(function(trigger) {
        trigger.setAttribute('aria-expanded', expanded ? 'true' : 'false');
    });
}

function _workspaceDrawerFocus(el) {
    if (!el || typeof el.focus !== 'function') return;
    try {
        el.focus({ preventScroll: true });
    } catch (error) {
        el.focus();
    }
}

window.openWorkspaceDrawer = function() {
    var drawer = document.getElementById('workspaceDrawer');
    var backdrop = document.getElementById('drawerBackdrop');
    if (typeof window.contabaseDebugUI === 'function') window.contabaseDebugUI('openWorkspaceDrawer called. Drawer:', !!drawer, 'Backdrop:', !!backdrop);
    if (!drawer || !backdrop) return;
    var wasClosed = drawer.classList.contains('translate-x-full');
    if (wasClosed) {
        var active = document.activeElement;
        _workspaceDrawerOpener = active && active !== document.body && active !== document.documentElement ? active : null;
    }
    drawer.removeAttribute('inert');
    drawer.removeAttribute('aria-hidden');
    drawer.classList.remove('translate-x-full');
    backdrop.classList.remove('opacity-0', 'pointer-events-none');
    backdrop.classList.add('opacity-100');
    _workspaceDrawerSetExpanded(true);
    if (wasClosed) _workspaceDrawerFocus(drawer.querySelector('[data-v2-workspace-close]'));
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
    drawer.setAttribute('inert', '');
    drawer.setAttribute('aria-hidden', 'true');
    backdrop.classList.add('opacity-0', 'pointer-events-none');
    backdrop.classList.remove('opacity-100');
    _workspaceDrawerSetExpanded(false);
    if (wasOpen) {
        var opener = _workspaceDrawerOpener;
        _workspaceDrawerOpener = null;
        // A trigger inside a closed <dialog> cannot take focus back.
        if (opener && document.contains(opener) && !(opener.closest && opener.closest('dialog:not([open])'))) {
            _workspaceDrawerFocus(opener);
        }
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    }
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
    var theme = payload.theme_token || '';
    if (['violeta', 'laranja', 'amarelo', 'azul', 'esmeralda', 'rosa'].indexOf(theme) === -1) return;
    body.dataset.workspaceTheme = theme;
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
