/*
 * Seletor de ícones reutilizável (Categorias, Limites e Metas).
 *
 * O componente é declarativo: o catálogo vem renderizado pelo servidor e este
 * arquivo apenas liga trigger, busca, seleção e teclado. Nada é persistido
 * aqui — a chave escolhida vai para o input oculto e é enviada no POST do
 * formulário que contém o seletor.
 *
 * Acessibilidade: diálogo nativo (ESC fecha), busca rotulada, navegação por
 * setas no grid, Home/End, Enter seleciona e o foco é devolvido ao trigger.
 */
(function () {
    'use strict';

    var normalize = function (value) {
        return (value || '')
            .toString()
            .toLocaleLowerCase('pt-BR')
            .normalize('NFD')
            .replace(/[\u0300-\u036f]/g, '')
            .trim();
    };

    function choicesOf(grid) {
        return Array.prototype.slice.call(grid.querySelectorAll('[data-icon-choice]'));
    }

    function initPicker(root) {
        if (!root || root.dataset.iconPickerReady === 'true') return;
        var dialog = root.querySelector('[data-icon-dialog]');
        var trigger = root.querySelector('[data-icon-open]');
        var input = root.querySelector('[data-icon-picker-value]');
        var grid = root.querySelector('[data-icon-grid]');
        var search = root.querySelector('[data-icon-search]');
        var empty = root.querySelector('[data-icon-empty]');
        if (!dialog || !trigger || !input || !grid) return;

        root.dataset.iconPickerReady = 'true';

        var triggerMark = root.querySelector('[data-icon-trigger-mark]');
        var triggerName = root.querySelector('[data-icon-trigger-name]');
        var triggerHint = root.querySelector('[data-icon-trigger-hint]');
        var previewMark = root.querySelector('[data-icon-preview-mark]');
        var previewName = root.querySelector('[data-icon-preview-name]');
        var previewHint = root.querySelector('[data-icon-preview-hint]');
        var closeButton = root.querySelector('[data-icon-close]');
        var choices = choicesOf(grid);

        var renderIcon = function (target, key) {
            if (!target) return;
            target.innerHTML = '';
            var icon = document.createElement('i');
            icon.setAttribute('data-lucide', key);
            icon.setAttribute('aria-hidden', 'true');
            target.appendChild(icon);
            if (window.lucide && typeof window.lucide.createIcons === 'function') {
                window.lucide.createIcons();
            }
        };

        var applySelection = function (key, label, explicit) {
            input.value = explicit ? key : '';
            choices.forEach(function (choice) {
                var choiceKey = choice.getAttribute('data-icon-key') || '';
                var selected = explicit ? choiceKey === key : choice.hasAttribute('data-icon-clear');
                choice.setAttribute('aria-pressed', String(selected));
            });
            renderIcon(triggerMark, key);
            if (triggerName) triggerName.textContent = explicit ? label : 'Automático';
            if (triggerHint) {
                triggerHint.textContent = explicit ? 'Toque para trocar o ícone' : 'Herdado da categoria';
            }
            renderIcon(previewMark, key);
            if (previewName) previewName.textContent = label;
            if (previewHint) {
                previewHint.textContent = explicit ? 'Selecionado' : 'Automático (herdado ou padrão)';
            }
        };

        var filterChoices = function (term) {
            var needle = normalize(term);
            var visible = 0;
            choices.forEach(function (choice) {
                if (choice.hasAttribute('data-icon-clear')) {
                    choice.classList.toggle('hidden', needle !== '');
                    return;
                }
                var haystack = normalize(choice.getAttribute('data-icon-search-text'));
                var match = needle === '' || haystack.indexOf(needle) !== -1;
                choice.classList.toggle('hidden', !match);
                if (match) visible += 1;
            });
            // Um grupo só aparece quando tem pelo menos um ícone visível.
            var groupTitles = grid.querySelectorAll('[data-icon-group]');
            groupTitles.forEach(function (title) {
                var sibling = title.nextElementSibling;
                var groupVisible = false;
                while (sibling && !sibling.hasAttribute('data-icon-group')) {
                    if (sibling.hasAttribute('data-icon-choice') && !sibling.classList.contains('hidden')) {
                        groupVisible = true;
                        break;
                    }
                    sibling = sibling.nextElementSibling;
                }
                title.classList.toggle('hidden', !groupVisible);
            });
            if (empty) empty.classList.toggle('hidden', visible > 0 || needle === '');
            return visible;
        };

        var visibleChoices = function () {
            return choices.filter(function (choice) {
                return !choice.classList.contains('hidden');
            });
        };

        var open = function () {
            filterChoices('');
            if (search) search.value = '';
            if (typeof dialog.showModal === 'function') {
                dialog.showModal();
            } else {
                dialog.setAttribute('open', '');
            }
            trigger.setAttribute('aria-expanded', 'true');
            if (search) search.focus();
        };

        var close = function () {
            if (typeof dialog.close === 'function') {
                dialog.close();
            } else {
                dialog.removeAttribute('open');
            }
            trigger.setAttribute('aria-expanded', 'false');
            trigger.focus();
        };

        trigger.addEventListener('click', open);
        if (closeButton) closeButton.addEventListener('click', close);
        dialog.addEventListener('close', function () {
            trigger.setAttribute('aria-expanded', 'false');
        });
        dialog.addEventListener('click', function (event) {
            if (event.target === dialog) close();
        });

        choices.forEach(function (choice) {
            choice.addEventListener('click', function () {
                var explicit = !choice.hasAttribute('data-icon-clear');
                applySelection(
                    choice.getAttribute('data-icon-key') || '',
                    choice.getAttribute('data-icon-label') || 'Automático',
                    explicit
                );
                close();
            });
        });

        if (search) {
            search.addEventListener('input', function () {
                filterChoices(search.value);
            });
            // Enter busca/seleciona em vez de submeter o formulário hospedeiro.
            search.addEventListener('keydown', function (event) {
                if (event.key === 'Enter') {
                    event.preventDefault();
                    var first = visibleChoices()[0];
                    if (first) first.focus();
                    return;
                }
                if (event.key === 'ArrowDown') {
                    event.preventDefault();
                    var candidates = visibleChoices();
                    if (candidates.length) candidates[0].focus();
                }
            });
        }

        grid.addEventListener('keydown', function (event) {
            var candidates = visibleChoices();
            if (!candidates.length) return;
            var current = candidates.indexOf(document.activeElement);
            var next = null;
            switch (event.key) {
                case 'ArrowRight':
                    next = current < 0 ? 0 : (current + 1) % candidates.length;
                    break;
                case 'ArrowLeft':
                    next = current <= 0 ? candidates.length - 1 : current - 1;
                    break;
                case 'ArrowDown':
                    next = current < 0 ? 0 : (current + 4) % candidates.length;
                    break;
                case 'ArrowUp':
                    next = current < 0 ? candidates.length - 1 : (current - 4 + candidates.length * 2) % candidates.length;
                    break;
                case 'Home':
                    next = 0;
                    break;
                case 'End':
                    next = candidates.length - 1;
                    break;
                default:
                    return;
            }
            event.preventDefault();
            candidates[next].focus();
        });
    }

    function initializeAll(scope) {
        var root = scope || document;
        var pickers = root.querySelectorAll('[data-icon-picker]');
        pickers.forEach(function (picker) {
            initPicker(picker);
        });
    }

    window.ContaBaseIconPicker = { init: initializeAll };

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function () {
            initializeAll(document);
        }, { once: true });
    } else {
        initializeAll(document);
    }

    document.body.addEventListener('htmx:afterSettle', function (event) {
        initializeAll(event.target || document);
    });
}());
