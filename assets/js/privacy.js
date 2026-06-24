function applyPrivacyState() {
    var enabled = sessionStorage.getItem('contabasePrivacy') === '1';
    document.body.classList.toggle('privacy-enabled', enabled);
    document.querySelectorAll('[data-privacy-icon]').forEach(function(icon) {
        icon.setAttribute('data-lucide', enabled ? 'eye-off' : 'eye');
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
    (root || document).querySelectorAll('#privacyToggle').forEach(function(btn) {
        btn.dataset.privacyInit = 'true';
    });
    applyPrivacyState();
}
