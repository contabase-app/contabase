function privacyToggleLabel(enabled) {
    return enabled ? 'Mostrar valores financeiros' : 'Ocultar valores financeiros';
}

function applyPrivacyState() {
    var enabled = sessionStorage.getItem('contabasePrivacy') === '1';
    var label = privacyToggleLabel(enabled);
    document.body.classList.toggle('privacy-enabled', enabled);
    document.querySelectorAll('[data-privacy-icon]').forEach(function(icon) {
        icon.setAttribute('data-lucide', enabled ? 'eye-off' : 'eye');
    });
    document.querySelectorAll('#privacyToggle,[data-privacy-toggle]').forEach(function(btn) {
        btn.setAttribute('aria-pressed', enabled ? 'true' : 'false');
        btn.setAttribute('aria-label', label);
        if (btn.hasAttribute('data-tooltip-hover')) {
            btn.setAttribute('data-tooltip-text', label);
            btn.removeAttribute('title');
        } else {
            btn.setAttribute('title', label);
        }
    });
    if (typeof window.refreshIcons === 'function') window.refreshIcons();
}

function togglePrivacyState() {
    var enabled = sessionStorage.getItem('contabasePrivacy') === '1';
    sessionStorage.setItem('contabasePrivacy', enabled ? '0' : '1');
    applyPrivacyState();
}

function bindPrivacyDelegation() {
    if (window.__contabasePrivacyDelegated === true) return;
    window.__contabasePrivacyDelegated = true;
    document.addEventListener('click', function(event) {
        var btn = event.target.closest('#privacyToggle,[data-privacy-toggle]');
        if (!btn) return;
        event.preventDefault();
        togglePrivacyState();
    });
}

function initPrivacyToggle(root) {
    bindPrivacyDelegation();
    (root || document).querySelectorAll('#privacyToggle,[data-privacy-toggle]').forEach(function(btn) {
        btn.dataset.privacyInit = 'true';
    });
    applyPrivacyState();
}
