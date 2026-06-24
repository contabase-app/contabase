// FAB Dynamic Controller
// Updates floating action button based on current route
(function() {
    function updateFAB() {
        var fabDiv = document.getElementById('fab-primary');
        if (!fabDiv) return;

        var onboardingEmpty = document.querySelector('[data-onboarding-empty="true"]');
        if (onboardingEmpty) {
            fabDiv.classList.add('hidden');
            return;
        }
        fabDiv.classList.remove('hidden');

        var button = fabDiv.querySelector('button');
        var visualDiv = fabDiv.querySelector('button > div');
        if (!button || !visualDiv) return;

        var path = window.location.pathname;

        // Reset standard attributes
        button.removeAttribute('hx-get');
        button.removeAttribute('hx-target');
        button.removeAttribute('hx-swap');
        button.onclick = null;

        // Reset visual classes
        visualDiv.className = "w-14 h-14 rounded-2xl flex items-center justify-center shadow-lg group-active:scale-95 transition-all duration-200";

        var newIconName = "plus";
        var extraIconClass = "group-hover:rotate-90";

        if (path === '/contatos') {
            visualDiv.classList.add('bg-fuchsia-600', 'group-hover:bg-fuchsia-700', 'shadow-fuchsia-500/25');
            newIconName = "user-plus";
            extraIconClass = "group-hover:scale-110";
            button.onclick = function(event) {
                if (typeof openContatosModal === 'function') {
                    openContatosModal(event);
                }
            };
        } else if (path === '/metas') {
            visualDiv.classList.add('bg-indigo-600', 'group-hover:bg-indigo-700', 'shadow-indigo-500/25');
            newIconName = "target";
            extraIconClass = "group-hover:scale-110";
            
            // Prefer DOM attribute (set by server via data-active-tab on #metas-tabs
            // or #metas-conteudo), over URL param, to avoid timing issues with hx-replace-url
            // during OOB swaps.
            var metasContent = document.getElementById('metas-tabs') || document.getElementById('metas-conteudo');
            var activeTab = metasContent ? metasContent.getAttribute('data-active-tab') : null;
            var aba = (activeTab === 'caixinhas') ? 'caixinha' : 'limite';
            button.setAttribute('hx-get', '/metas/novo?aba=' + aba);
            button.setAttribute('hx-target', '#bottom-sheet-container');
            button.setAttribute('hx-swap', 'innerHTML');
            button.setAttribute('hx-push-url', 'false');
            if (window.htmx) htmx.process(button);
        } else {
            visualDiv.classList.add('bg-violet-600', 'group-hover:bg-violet-700', 'shadow-violet-500/25');
            newIconName = "plus";
            extraIconClass = "group-hover:rotate-90";
            
            button.setAttribute('hx-get', '/transacoes/nova');
            button.setAttribute('hx-target', '#bottom-sheet-container');
            button.setAttribute('hx-swap', 'innerHTML');
            button.setAttribute('hx-push-url', 'false');
            if (window.htmx) htmx.process(button);
        }

        visualDiv.innerHTML = '<i data-lucide="' + newIconName + '" class="w-6 h-6 text-white transition-transform duration-300 pointer-events-none ' + extraIconClass + '"></i>';

        if (typeof window.refreshIcons === 'function') {
            window.refreshIcons();
        }
    }

    document.body.addEventListener('htmx:afterSwap', function(event) {
        updateFAB();
    });
    
    // Execute on load
    updateFAB();
    
    // Expose globally
    window.updateFAB = updateFAB;
})();
