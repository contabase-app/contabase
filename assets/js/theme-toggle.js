// Theme Toggle — extracted from layout.html (F2.6-B)
(function () {
    function getStoredTheme() {
        return localStorage.getItem('theme');
    }

    function getPreferredTheme() {
        var storedTheme = getStoredTheme();

        if (storedTheme === 'dark' || storedTheme === 'light') {
            return storedTheme;
        }

        return 'dark';
    }

    function applyTheme(theme) {
        var isDark = theme === 'dark';

        document.documentElement.classList.toggle('dark', isDark);
        document.documentElement.setAttribute('data-theme', theme);
        localStorage.setItem('theme', theme);

        updateThemeOptionButtons(theme);
    }

    function updateThemeOptionButtons(theme) {
        if (!theme) theme = document.documentElement.getAttribute('data-theme') || 'dark';
        var activeBorder = 'border-[var(--cb-accent)] ring-2 ring-[var(--cb-accent)]/10'.split(' ');
        var inactiveBorder = 'border-[var(--cb-border-subtle)]'.split(' ');

        document.querySelectorAll('[data-theme-option]').forEach(function (btn) {
            var isActive = btn.getAttribute('data-theme-option') === theme;
            if (isActive) {
                btn.classList.remove.apply(btn.classList, inactiveBorder);
                btn.classList.add.apply(btn.classList, activeBorder);
            } else {
                btn.classList.remove.apply(btn.classList, activeBorder);
                btn.classList.add.apply(btn.classList, inactiveBorder);
            }
        });
    }

    window.applyTheme = applyTheme;

    window.toggleTheme = function () {
        var isDark = document.documentElement.classList.contains('dark');
        applyTheme(isDark ? 'light' : 'dark');
    };

    applyTheme(getPreferredTheme());

    document.addEventListener('click', function (event) {
        var button = event.target.closest('[data-theme-toggle]');
        if (!button) return;

        event.preventDefault();
        window.toggleTheme();
    });

    // Re-apply theme after HTMX navigation (preserve user preference)
    document.body.addEventListener('htmx:afterSettle', function () {
        var currentTheme = document.documentElement.getAttribute('data-theme');
        var storedTheme = getStoredTheme();
        if (storedTheme && currentTheme !== storedTheme) {
            applyTheme(storedTheme);
        }
        updateThemeOptionButtons();
    });
})();
