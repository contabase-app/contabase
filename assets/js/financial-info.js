(function () {
    function panelFor(button) {
        var id = button.getAttribute('aria-controls');
        if (!id) return null;
        return document.getElementById(id);
    }

    function close(button, panel) {
        if (panel) panel.hidden = true;
        button.setAttribute('aria-expanded', 'false');
    }

    function closeAll() {
        document.querySelectorAll('[data-f1-info-button][aria-expanded="true"]').forEach(function (button) {
            close(button, panelFor(button));
        });
    }

    document.addEventListener('click', function (event) {
        var button = event.target.closest('[data-f1-info-button]');
        if (button) {
            var panel = panelFor(button);
            if (!panel) return;
            var isOpen = button.getAttribute('aria-expanded') === 'true';
            closeAll();
            if (!isOpen) {
                panel.hidden = false;
                button.setAttribute('aria-expanded', 'true');
            }
            return;
        }

        if (!event.target.closest('[data-f1-info-panel]')) {
            closeAll();
        }
    });

    document.addEventListener('keydown', function (event) {
        if (event.key !== 'Escape') return;
        var open = document.querySelector('[data-f1-info-button][aria-expanded="true"]');
        if (!open) return;
        close(open, panelFor(open));
        open.focus();
    });

    document.body.addEventListener('htmx:afterSettle', closeAll);
})();
