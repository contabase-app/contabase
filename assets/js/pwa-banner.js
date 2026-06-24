// PWA Install Banner Logic
(function() {
    var deferredPrompt = null;
    var banner = document.getElementById('pwa-install-banner');
    var installBtn = document.getElementById('pwa-install-btn');
    var closeBtn = document.getElementById('pwa-close-btn');

    if (!banner || !installBtn || !closeBtn) return;

    var isStandalone = window.matchMedia('(display-mode: standalone)').matches || window.navigator.standalone === true;
    var isDismissed = localStorage.getItem('pwa_banner_dismissed') === 'true';

    function adjustFabPosition() {
        var fab = document.getElementById('fab-primary');
        if (!fab) return;

        if (window.innerWidth < 768) {
            if (banner && !banner.classList.contains('hidden')) {
                var bannerHeight = banner.offsetHeight || 120;
                var newBottom = 80 + bannerHeight + 16;
                fab.style.bottom = newBottom + 'px';
            } else {
                fab.style.bottom = '';
            }
        } else {
            fab.style.bottom = '';
        }
    }

    function handleLayoutUpdates() {
        adjustFabPosition();
    }

    var isIOS = /ipad|iphone|ipod/.test(navigator.userAgent.toLowerCase()) && !window.MSStream;

    window.addEventListener('resize', handleLayoutUpdates);
    document.body.addEventListener('htmx:afterSettle', handleLayoutUpdates);
    document.body.addEventListener('htmx:afterSwap', handleLayoutUpdates);

    if (isIOS && !isStandalone && !isDismissed) {
        setTimeout(function() {
            banner.classList.remove('hidden');
            installBtn.classList.add('hidden');
            document.getElementById('pwa-ios-instructions').classList.remove('hidden');
            document.getElementById('pwa-ios-instructions').classList.add('flex');
            handleLayoutUpdates();
        }, 2000);
    }

    window.addEventListener('beforeinstallprompt', function(e) {
        e.preventDefault();
        deferredPrompt = e;

        if (!isStandalone && !isDismissed && !isIOS) {
            banner.classList.remove('hidden');
            setTimeout(handleLayoutUpdates, 50);
        }
    });

    closeBtn.addEventListener('click', function() {
        banner.classList.add('hidden');
        localStorage.setItem('pwa_banner_dismissed', 'true');
        handleLayoutUpdates();
    });

    installBtn.addEventListener('click', function() {
        if (!deferredPrompt) return;
        deferredPrompt.prompt();
        deferredPrompt.userChoice.then(function() {
            deferredPrompt = null;
            banner.classList.add('hidden');
            handleLayoutUpdates();
        });
    });

    handleLayoutUpdates();
})();
