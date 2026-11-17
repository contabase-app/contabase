// PWA install invite.
//
// The shell consolidation removed the original banner and the CSP migration
// deleted its script. This controller restores the Beta 3 behavior: capture
// beforeinstallprompt, keep the event until the user decides, and call prompt()
// directly from the install click. Browsers without the event simply do not
// receive an invite (no fake button and no manual-install promise).
(function () {
    if (window.__contabasePWAInstall) return;
    window.__contabasePWAInstall = true;

    var DISMISS_KEY = 'pwa_banner_dismissed';
    var INSTALLED_KEY = 'pwa_installed';
    var deferredPrompt = null;
    var initialized = false;

    function storageGet(key) {
        try { return window.localStorage.getItem(key); } catch (error) { return null; }
    }

    function storageSet(key, value) {
        try { window.localStorage.setItem(key, value); } catch (error) { /* storage can be unavailable */ }
    }

    function isStandalone() {
        if (window.navigator && window.navigator.standalone === true) return true;
        if (typeof window.matchMedia !== 'function') return false;
        return window.matchMedia('(display-mode: standalone)').matches ||
            window.matchMedia('(display-mode: fullscreen)').matches ||
            window.matchMedia('(display-mode: minimal-ui)').matches;
    }

    function isInstalled() {
        return isStandalone() || storageGet(INSTALLED_KEY) === 'true';
    }

    function isDismissed() {
        return storageGet(DISMISS_KEY) === 'true';
    }

    function elements() {
        return {
            banner: document.getElementById('pwa-install-banner'),
            installButton: document.getElementById('pwa-install-btn'),
            closeButton: document.getElementById('pwa-close-btn')
        };
    }

    function hideBanner() {
        var ui = elements();
        if (!ui.banner) return;
        ui.banner.hidden = true;
        ui.banner.setAttribute('aria-hidden', 'true');
        ui.banner.removeAttribute('data-pwa-mode');
    }

    function showBanner() {
        var ui = elements();
        if (!ui.banner) return;
        ui.banner.hidden = false;
        ui.banner.setAttribute('aria-hidden', 'false');
        ui.banner.setAttribute('data-pwa-mode', 'native');
    }

    function refresh() {
        if (!initialized) return;
        if (isInstalled() || isDismissed() || !deferredPrompt) {
            hideBanner();
            return;
        }
        showBanner();
    }

    function handleBeforeInstallPrompt(event) {
        if (!event || typeof event.preventDefault !== 'function') return;
        event.preventDefault();
        deferredPrompt = event;
        refresh();
    }

    function dismiss() {
        storageSet(DISMISS_KEY, 'true');
        hideBanner();
    }

    function install() {
        if (!deferredPrompt) return;
        var promptEvent = deferredPrompt;
        deferredPrompt = null;
        var ui = elements();
        if (ui.installButton) {
            ui.installButton.disabled = true;
            ui.installButton.setAttribute('aria-busy', 'true');
        }
        var choice;
        try {
            // Must run synchronously inside the user click. No await before this.
            choice = promptEvent.prompt();
        } catch (error) {
            choice = null;
        }
        return Promise.resolve(choice).then(function () {
            if (promptEvent.userChoice && typeof promptEvent.userChoice.then === 'function') {
                return promptEvent.userChoice;
            }
            return null;
        }).then(function (result) {
            if (result && result.outcome === 'accepted') {
                storageSet(INSTALLED_KEY, 'true');
                hideBanner();
                return;
            }
            hideBanner();
        }).catch(function () {
            hideBanner();
        }).then(function () {
            if (ui.installButton) {
                ui.installButton.disabled = false;
                ui.installButton.removeAttribute('aria-busy');
            }
        });
    }

    function registerServiceWorker() {
        if (!('serviceWorker' in window.navigator) || typeof window.navigator.serviceWorker.register !== 'function') return;
        window.navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(function (error) {
            if (typeof window.contabaseDebugUI === 'function') {
                window.contabaseDebugUI('service worker registration failed:', error);
            }
        });
    }

    function init() {
        if (initialized) return;
        var ui = elements();
        if (!ui.banner || !ui.installButton || !ui.closeButton) return;
        initialized = true;
        ui.installButton.addEventListener('click', install);
        ui.closeButton.addEventListener('click', dismiss);
        registerServiceWorker();
        refresh();
    }

    window.ContaBasePWA = {
        init: init,
        refresh: refresh,
        handleBeforeInstallPrompt: handleBeforeInstallPrompt,
        dismiss: dismiss,
        isStandalone: isStandalone
    };

    window.addEventListener('beforeinstallprompt', handleBeforeInstallPrompt);
    window.addEventListener('appinstalled', function () {
        storageSet(INSTALLED_KEY, 'true');
        deferredPrompt = null;
        hideBanner();
    });
    if (typeof window.matchMedia === 'function') {
        try {
            var standaloneQuery = window.matchMedia('(display-mode: standalone)');
            if (standaloneQuery && typeof standaloneQuery.addEventListener === 'function') {
                standaloneQuery.addEventListener('change', refresh);
            }
        } catch (error) { /* older browsers */ }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init, { once: true });
    } else {
        init();
    }
}());
