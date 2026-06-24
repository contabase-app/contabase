// Form Lancamento — extracted helpers, error handler, visual toggles, and contact combobox
// Phases D.5.3.2–D.5.3.5
// Registered once globally; survives HTMX swaps idempotently.

(function() {
    var ns = window.ContaBaseFormLancamento = window.ContaBaseFormLancamento || {};

    function findInRoot(root, selector) {
        if (!root || root === document) return document.querySelector(selector);
        if (root.matches && root.matches(selector)) return root;
        return root.querySelector ? root.querySelector(selector) : document.querySelector(selector);
    }

    // --- Pure monetary helpers ---

    ns.parseMoneyCents = function(value) {
        var digits = (value || '').toString().replace(/\D/g, '');
        if (!digits) return 0;
        var cents = parseInt(digits, 10);
        return Number.isFinite(cents) ? cents : 0;
    };

    // --- Form error handler (idempotent) ---

    if (!window.__lancamentoFormErrorHandlerRegistered) {
        window.__lancamentoFormErrorHandlerRegistered = true;

        window.__lancamentoFormErrorHandler = function(event) {
            var form = document.getElementById('lancamento-form');
            if (!form) return;

            document.querySelectorAll('.field-error').forEach(function(el) {
                el.classList.add('hidden');
                el.innerText = '';
            });
            document.querySelectorAll('.has-error').forEach(function(el) {
                el.classList.remove('!border-rose-500/50', '!bg-rose-500/10', 'has-error');
            });

            var field = event.detail && event.detail.field;
            var message = event.detail && event.detail.message;
            if (!field) return;

            var errEl = document.getElementById(field + '-error');
            if (errEl) {
                errEl.classList.remove('hidden');
                errEl.innerText = decodeURIComponent(message || '');
            }

            var inputEl = document.getElementById(field + '-input');
            if (inputEl) {
                inputEl.classList.add('has-error', '!border-rose-500/50', '!bg-rose-500/10');
            }
        };

        window.addEventListener('form-error', window.__lancamentoFormErrorHandler);
        window.addEventListener('formError', window.__lancamentoFormErrorHandler);
    }

    // --- Visual toggles (Phase D.5.3.3) ---

    function toggleAdditionalDetails(body, toggle, icon, forceOpen) {
        if (!body) return;
        var open = typeof forceOpen === 'boolean' ? forceOpen : body.classList.contains('hidden');
        body.classList.toggle('hidden', !open);
        if (toggle) toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
        if (icon) icon.classList.toggle('rotate-180', open);
    }

    function initDetailsToggle(root) {
        var toggle = findInRoot(root, '#detalhesAdicionaisToggle');
        if (!toggle || toggle.dataset.detailsEnhanced === 'true') return;
        toggle.dataset.detailsEnhanced = 'true';

        var body = findInRoot(root, '#detalhesAdicionaisBody');
        var icon = findInRoot(root, '#detalhesAdicionaisIcon');

        toggle.addEventListener('click', function() {
            toggleAdditionalDetails(body, toggle, icon);
        });
    }

    // --- Contact combobox (Phase D.5.3.5) ---

    function normalizeComboboxText(value) {
        return (value || '').normalize('NFD').replace(/[\u0300-\u036f]/g, '').toLowerCase();
    }

    function getSelectedLabel(input) {
        return input.dataset.selectedLabel || '';
    }

    function setSelectedLabel(input, label) {
        input.dataset.selectedLabel = label;
    }

    function closeContactList(contactList, contactInput) {
        if (!contactList) return;
        contactList.classList.add('hidden');
        if (contactInput) contactInput.setAttribute('aria-expanded', 'false');
    }

    function openContactList(contactList, contactInput) {
        if (!contactList) return;
        contactList.classList.remove('hidden');
        if (contactInput) contactInput.setAttribute('aria-expanded', 'true');
    }

    function filterContactOptions(contactInput, contactOptions, contactEmpty) {
        if (!contactInput || !contactOptions.length) {
            if (contactEmpty) contactEmpty.classList.toggle('hidden', contactOptions.length > 0);
            return;
        }
        var query = normalizeComboboxText(contactInput.value.trim());
        var visibleCount = 0;
        contactOptions.forEach(function(option) {
            var searchText = [
                option.querySelector('.js-contact-name') ? option.querySelector('.js-contact-name').textContent : '',
                option.querySelector('.js-contact-document') ? option.querySelector('.js-contact-document').textContent : '',
                option.querySelector('.js-contact-phone') ? option.querySelector('.js-contact-phone').textContent : '',
                option.textContent || ''
            ].join(' ');
            var visible = !query || normalizeComboboxText(searchText).includes(query);
            option.classList.toggle('hidden', !visible);
            if (visible) visibleCount += 1;
        });
        if (contactEmpty) contactEmpty.classList.toggle('hidden', visibleCount > 0);
    }

    function contactDisplayLabel(option) {
        if (!option) return '';
        var name = option.querySelector('.js-contact-name') ? option.querySelector('.js-contact-name').textContent.trim() : '';
        var documentValue = option.querySelector('.js-contact-document') ? option.querySelector('.js-contact-document').textContent.trim() : '';
        return name + (documentValue ? ' \u00b7 ' + documentValue : '');
    }

    function selectContactOption(option, contactInput, contactHidden, contactOptions, contactList, contactEmpty) {
        if (!contactInput || !contactHidden) return;
        if (!option || !option.value) {
            contactHidden.value = '';
            contactInput.value = '';
            setSelectedLabel(contactInput, '');
        } else {
            var label = contactDisplayLabel(option);
            setSelectedLabel(contactInput, label);
            contactHidden.value = option.value;
            contactInput.value = label;
        }
        filterContactOptions(contactInput, contactOptions, contactEmpty);
        setContactValidity(contactInput, contactErrorEl(), true);
        closeContactList(contactList, contactInput);
    }

    function contactErrorEl() {
        return document.getElementById('contato-error');
    }

    function isContactSelectionValid(contactInput, contactHidden) {
        if (!contactInput || !contactHidden) return true;
        var typedValue = contactInput.value.trim();
        if (typedValue === '' || typedValue === 'Sem contato') {
            contactInput.value = '';
            contactHidden.value = '';
            setSelectedLabel(contactInput, '');
            return true;
        }
        return contactHidden.value.trim() !== '';
    }

    function setContactValidity(contactInput, errorEl, valid) {
        if (!contactInput) return;
        contactInput.classList.toggle('has-error', !valid);
        contactInput.classList.toggle('!border-rose-500/50', !valid);
        contactInput.classList.toggle('!bg-rose-500/10', !valid);
        contactInput.setAttribute('aria-invalid', valid ? 'false' : 'true');
        if (errorEl) {
            errorEl.textContent = valid ? '' : 'Selecione um contato da lista ou deixe o campo vazio.';
            errorEl.classList.toggle('hidden', valid);
            errorEl.classList.toggle('block', !valid);
        }
    }

    ns.validateContactSelection = function() {
        var contactInput = document.getElementById('contato-input');
        var contactHidden = document.querySelector('input[name="contact_id"]');
        var valid = isContactSelectionValid(contactInput, contactHidden);
        var errorEl = contactErrorEl();
        setContactValidity(contactInput, errorEl, valid);
        if (!valid) {
            var contactList = document.getElementById('contato-options');
            if (contactList) {
                contactList.classList.remove('hidden');
            }
            if (contactInput) contactInput.setAttribute('aria-expanded', 'true');
            if (contactInput) contactInput.focus({ preventScroll: true });
        }
        return valid;
    };

    var contactComboboxInitSet = new WeakSet();

    function initContactCombobox(root) {
        var contactRoot = findInRoot(root, '#contatoCombobox');
        if (!contactRoot) return;
        var contactInput = findInRoot(root, '#contato-input');
        var contactHidden = findInRoot(root, 'input[name="contact_id"]');
        var contactList = findInRoot(root, '#contato-options');
        if (!contactInput || !contactList) return;

        if (contactComboboxInitSet.has(contactRoot)) return;
        contactComboboxInitSet.add(contactRoot);

        var contactOptions = Array.prototype.slice.call(contactRoot.querySelectorAll('.js-contact-option'));
        var contactEmpty = findInRoot(root, '#contato-empty');

        // Restore selected label from data attribute (survives HTMX swaps)
        if (contactHidden && contactHidden.value && !getSelectedLabel(contactInput)) {
            setSelectedLabel(contactInput, contactInput.value);
        }

        contactInput.addEventListener('focus', function() {
            filterContactOptions(contactInput, contactOptions, contactEmpty);
            openContactList(contactList, contactInput);
        });

        contactInput.addEventListener('input', function(event) {
            if (event.isTrusted && contactHidden && contactInput.value !== getSelectedLabel(contactInput)) {
                contactHidden.value = '';
                setSelectedLabel(contactInput, '');
            }
            filterContactOptions(contactInput, contactOptions, contactEmpty);
            setContactValidity(contactInput, contactErrorEl(), isContactSelectionValid(contactInput, contactHidden));
            openContactList(contactList, contactInput);
        });

        contactInput.addEventListener('keydown', function(event) {
            if (event.key === 'Escape') {
                event.stopPropagation();
                closeContactList(contactList, contactInput);
            }
        });

        contactRoot.addEventListener('focusout', function(event) {
            if (!contactRoot.contains(event.relatedTarget)) closeContactList(contactList, contactInput);
        });

        contactRoot.querySelectorAll('.sheet-popover-option').forEach(function(option) {
            option.addEventListener('click', function() {
                selectContactOption(option, contactInput, contactHidden, contactOptions, contactList, contactEmpty);
            });
        });

        // Click-away handler (idempotent per contactRoot lifecycle)
        if (!contactRoot.dataset.contactClickAwaySetup) {
            contactRoot.dataset.contactClickAwaySetup = 'true';
            var clickAwayHandler = function(event) {
                if (!contactRoot.contains(event.target)) {
                    closeContactList(contactList, contactInput);
                }
            };
            document.addEventListener('click', clickAwayHandler);
        }

        filterContactOptions(contactInput, contactOptions, contactEmpty);
        setContactValidity(contactInput, contactErrorEl(), true);
        closeContactList(contactList, contactInput);
    }

    // --- Icon & Category Helpers (Phase D.5.3.8.1) ---

    var colorClasses = {
        amber: ['bg-amber-500/10', 'text-amber-400'],
        emerald: ['bg-emerald-500/10', 'text-emerald-400'],
        indigo: ['bg-indigo-500/10', 'text-indigo-400'],
        lime: ['bg-lime-500/10', 'text-lime-400'],
        red: ['bg-red-500/10', 'text-red-400'],
        rose: ['bg-rose-500/10', 'text-rose-400'],
        sky: ['bg-sky-500/10', 'text-sky-400'],
        violet: ['bg-violet-500/10', 'text-violet-400'],
    };

    ns.expectedCategoryType = function(type) {
        return type === 'receita' ? 'INCOME' : 'EXPENSE';
    };

    ns.filterCategoryOptions = function(type) {
        var expected = ns.expectedCategoryType(type);
        document.querySelectorAll('.category-option').forEach(function(option) {
            option.classList.toggle('hidden', option.dataset.categoryType !== expected);
        });
    };

    ns.updateIcon = function(wrapper, icon, iconName, color) {
        Object.values(colorClasses).flat().forEach(function(c) { wrapper.classList.remove(c); icon.classList.remove(c); });
        wrapper.style.backgroundColor = '';
        wrapper.style.borderColor = '';
        icon.style.color = '';
        var cls = colorClasses[color] || colorClasses.indigo;
        wrapper.classList.add(cls[0]);
        icon.classList.add(cls[1]);
        icon.setAttribute('data-lucide', iconName);
        if (typeof window.refreshIcons === 'function') window.refreshIcons();
    };

    ns.updateAccountIcon = function(wrapper, icon, iconText, iconName, colorHex, providerMark) {
        Object.values(colorClasses).flat().forEach(function(c) { wrapper.classList.remove(c); icon.classList.remove(c); });
        wrapper.style.backgroundColor = '';
        wrapper.style.borderColor = '';
        icon.style.color = '';
        if (iconText) {
            iconText.style.color = '';
        }
        wrapper.style.setProperty('--ic', colorHex || '#6B7280');
        wrapper.style.borderWidth = '1px';
        wrapper.style.borderStyle = 'solid';
        if (providerMark && providerMark.trim() !== '') {
            if (iconText) {
                iconText.textContent = providerMark;
                iconText.className = 'font-bold uppercase tracking-wider select-none emblem-text emblem-len-' + providerMark.length;
                iconText.classList.remove('hidden');
            }
            icon.classList.add('hidden');
        } else {
            if (iconText) {
                iconText.classList.add('hidden');
                iconText.textContent = '';
            }
            icon.classList.remove('hidden');
            icon.setAttribute('data-lucide', iconName || 'wallet');
            if (typeof window.refreshIcons === 'function') window.refreshIcons();
        }
    };

    // --- Category selection bridge (Phase D.5.3.8.2) ---

    ns.applyCategorySelection = function(payload, callbacks) {
        var cb = callbacks || {};

        var categoryIdInput = document.getElementById('categoriaId');
        var categoryTypeInput = document.getElementById('categoriaType');
        var categoryNameEl = document.getElementById('categoriaNome');
        var categoryIconEl = document.getElementById('categoriaIcon');
        var categoryIconWrapEl = document.getElementById('categoriaIconWrap');

        if (!categoryIdInput || !categoryNameEl || !categoryIconEl || !categoryIconWrapEl) return;

        categoryIdInput.value = payload.id;
        categoryIdInput.dataset.boxId = payload.boxId || '';
        categoryIdInput.dataset.boxReserved = payload.boxReserved || '0';
        categoryIdInput.dataset.boxName = payload.boxName || '';
        categoryIdInput.dataset.limitMax = payload.limitMax || '0';
        categoryIdInput.dataset.limitSpent = payload.limitSpent || '0';

        if (payload.type !== undefined) {
            if (categoryTypeInput) categoryTypeInput.value = payload.type;
        }

        categoryNameEl.textContent = payload.name || '';

        if (cb.visualMode === 'local-option') {
            ns.updateIcon(categoryIconWrapEl, categoryIconEl, payload.icon, payload.color);
        } else {
            categoryIconEl.setAttribute('data-lucide', payload.icon);
            categoryIconEl.style.color = payload.color;
            categoryIconWrapEl.style.borderColor = payload.color;
            categoryIconWrapEl.style.backgroundColor = payload.color + '22';
            if (typeof cb.onRefreshIcons === 'function') cb.onRefreshIcons();
        }

        if (typeof cb.onCategoryInputSync === 'function') cb.onCategoryInputSync();

        ns.updateCategoryInfoWarnings();

        if (typeof cb.onBoxOverdraftChange === 'function') cb.onBoxOverdraftChange();

        if (typeof cb.onCloseModal === 'function') cb.onCloseModal();
    };

    // --- Origin selection bridge (Phase D.5.3.8.3.a) ---

    ns.applyOriginSelection = function(payload, callbacks) {
        var cb = callbacks || {};

        if (payload.mode === 'destination') {
            var destId = document.getElementById('destinoContaId');
            var destName = document.getElementById('destinoNome');
            var destIconWrap = document.getElementById('destinoIconWrap');
            var destIcon = document.getElementById('destinoIcon');
            var destIconText = document.getElementById('destinoIconText');

            if (destId) destId.value = payload.id;
            if (destName) destName.textContent = payload.name;
            ns.updateAccountIcon(destIconWrap, destIcon, destIconText, payload.icon, payload.color, payload.providerMark);

            if (typeof cb.onCloseModal === 'function') cb.onCloseModal();
            return;
        }

        var originIdEl = document.getElementById('origemContaId');
        var originTypeEl = document.getElementById('origemTipo');
        var originNameEl = document.getElementById('origemNome');
        var originLabelEl = document.getElementById('origemLabel');
        var originIconWrap = document.getElementById('origemIconWrap');
        var originIcon = document.getElementById('origemIcon');
        var originIconText = document.getElementById('origemIconText');

        if (originIdEl) originIdEl.value = payload.id;
        if (originTypeEl) originTypeEl.value = payload.kind;
        if (originNameEl) originNameEl.textContent = payload.name;
        if (originLabelEl && payload.originLabel) originLabelEl.textContent = payload.originLabel;

        ns.updateAccountIcon(originIconWrap, originIcon, originIconText, payload.icon, payload.color, payload.providerMark);

        if (typeof cb.onCardStateCaptured === 'function') cb.onCardStateCaptured(payload.closingDay, payload.dueDay);

        if (payload.kind === 'cartao') {
            if (typeof cb.onCardSelected === 'function') cb.onCardSelected();
        } else {
            if (typeof cb.onAccountSelected === 'function') cb.onAccountSelected();
        }

        if (typeof cb.onInvoiceNoticeUpdate === 'function') cb.onInvoiceNoticeUpdate();

        if (typeof cb.onCloseModal === 'function') cb.onCloseModal();
    };

    // --- Transaction type bridge (Phase D.5.3.8.3.c) ---

    ns.applyTransactionType = function(payload, callbacks) {
        var cb = callbacks || {};

        var tipoInput = document.getElementById('tipoLancamento');
        var tabs = document.querySelectorAll('[data-type]');
        var originLabelEl = document.getElementById('origemLabel');
        var categoryPickerEl = document.getElementById('categoriaPicker');
        var destinationPickerEl = document.getElementById('destinoPicker');
        var cardsModalGroupEl = document.getElementById('cartoesModalGroup');

        var type = payload.type;

        if (tipoInput) tipoInput.value = type;

        window.dispatchEvent(new CustomEvent('tx-type-changed', { detail: { type: type } }));

        var activeClass = {
            despesa: 'is-active-despesa',
            receita: 'is-active-receita',
            transferencia: 'is-active-transferencia'
        };
        tabs.forEach(function(t) {
            t.classList.remove('is-active-despesa', 'is-active-receita', 'is-active-transferencia');
            if (t.dataset.type === type) t.classList.add(activeClass[type]);
        });

        var isTransfer = type === 'transferencia';
        var isIncome = type === 'receita';

        if (originLabelEl) {
            originLabelEl.textContent = payload.currentOriginType === 'cartao'
                ? 'Cartão de crédito'
                : isTransfer ? 'Conta de origem' : isIncome ? 'Conta de entrada' : 'Conta de origem';
        }

        if (categoryPickerEl) categoryPickerEl.classList.toggle('hidden', isTransfer);
        if (destinationPickerEl) {
            destinationPickerEl.classList.toggle('hidden', !isTransfer);
            destinationPickerEl.classList.toggle('flex', isTransfer);
        }
        if (cardsModalGroupEl) cardsModalGroupEl.classList.toggle('hidden', isTransfer || isIncome);

        if (typeof cb.onUpdateTypeLabels === 'function') cb.onUpdateTypeLabels(type);
    };

    // --- Payment status bridge (Phase D.5.3.9.b.1) ---

    ns.syncPaymentStatusUI = function() {
        var hidden = document.getElementById('statusPagamentoHidden');
        if (!hidden) return;
        var isPaid = hidden.value !== 'pending';

        var btn = document.getElementById('statusPagamentoBtn');
        if (btn) {
            btn.classList.toggle('is-paid', isPaid);
            btn.classList.toggle('is-pending', !isPaid);
            btn.setAttribute('title', isPaid ? 'Pago \u2014 toque para marcar como pendente' : 'Pendente \u2014 toque para marcar como pago');
            btn.setAttribute('aria-label', isPaid ? 'Pago \u2014 toque para marcar como pendente' : 'Pendente \u2014 toque para marcar como pago');
            btn.setAttribute('aria-pressed', isPaid ? 'true' : 'false');
        }

        var paidIcon = document.getElementById('statusPagamentoPaidIcon');
        if (paidIcon) paidIcon.classList.toggle('hidden', !isPaid);
        var pendingIcon = document.getElementById('statusPagamentoPendingIcon');
        if (pendingIcon) pendingIcon.classList.toggle('hidden', isPaid);

        var dueSection = document.getElementById('dueDateSection');
        if (dueSection) {
            dueSection.classList.toggle('hidden', isPaid);
            dueSection.setAttribute('aria-hidden', isPaid ? 'true' : 'false');
        }
    };

    ns.applyPaymentStatus = function(isPaid) {
        var hidden = document.getElementById('statusPagamentoHidden');
        if (!hidden) return;
        hidden.value = isPaid ? 'paid' : 'pending';
        ns.syncPaymentStatusUI();
    };

    ns.initPaymentStatusControls = function(root) {
        var btn = findInRoot(root, '#statusPagamentoBtn');
        if (btn && btn.dataset.paymentStatusEnhanced !== 'true') {
            btn.dataset.paymentStatusEnhanced = 'true';
            btn.addEventListener('click', function() {
                var hidden = findInRoot(root, '#statusPagamentoHidden');
                ns.applyPaymentStatus(hidden && hidden.value === 'pending');
            });
        }

        var dateInput = findInRoot(root, '#dataLancamento');
        if (dateInput && dateInput.dataset.paymentDateEnhanced !== 'true') {
            dateInput.dataset.paymentDateEnhanced = 'true';
            dateInput.addEventListener('change', function(e) {
                if (e.isTrusted) {
                    var hidden = findInRoot(root, '#statusPagamentoHidden');
                    if (hidden && hidden.value !== 'pending') {
                        ns.applyPaymentStatus(true);
                    }
                }
            });
        }
    };

    // --- Installment/Recurrence visuals (Phase D.5.3.6) ---

    ns.updateInstallmentsSummary = function() {
        var toggle = document.getElementById('parcelada');
        var summary = document.getElementById('parcelasResumo');
        var select = document.getElementById('parcelas');
        var valueInput = document.getElementById('valor');
        if (!toggle || !summary || !select) return;
        if (!toggle.checked) {
            summary.textContent = 'Ative para escolher de 1x at\u00e9 12x';
            summary.className = 'sheet-muted-text block text-xs mt-0.5';
            return;
        }
        var amountCents = ns.parseMoneyCents(valueInput ? valueInput.value : '');
        var n = Number(select.value);
        var preview = window.ContaBaseInstallmentPreview;
        var text = preview && typeof preview.describe === 'function'
            ? preview.describe(amountCents, n)
            : '';
        if (text) {
            summary.textContent = text;
            summary.className = 'block text-sm font-semibold text-cyan-700 dark:text-cyan-300 mt-1';
        } else {
            summary.textContent = 'Informe um valor para calcular as parcelas';
            summary.className = 'sheet-muted-text block text-xs mt-0.5';
        }
    };

    ns.updateTypeLabels = function(type) {
        var label = type === 'receita' ? 'receita' : type === 'transferencia' ? 'transfer\u00eancia' : 'despesa';
        var installmentsTitle = document.getElementById('parcelamentoTitulo');
        var fixedTitle = document.getElementById('fixoTitulo');
        if (installmentsTitle) installmentsTitle.textContent = 'Esta ' + label + ' \u00e9 parcelada?';
        if (fixedTitle) fixedTitle.textContent = label.charAt(0).toUpperCase() + label.slice(1) + ' fixa';
    };

    var installmentInitSet = new WeakSet();

    function initInstallmentRecurrenceVisuals(root) {
        var formEl = findInRoot(root, '#lancamento-form');
        if (!formEl || installmentInitSet.has(formEl)) return;
        installmentInitSet.add(formEl);

        var installmentsToggle = findInRoot(formEl, '#parcelada');
        var installmentsBox = findInRoot(formEl, '#parcelasBox');
        var installmentsSelect = findInRoot(formEl, '#parcelas');
        var fixedToggle = findInRoot(formEl, '#lancamentoFixo');
        var recurrenceBox = findInRoot(formEl, '#recorrenciaBox');
        var totalOccurrencesBox = findInRoot(formEl, '#totalOccurrencesBox');

        if (installmentsToggle) {
            installmentsToggle.addEventListener('change', function() {
                if (!installmentsBox) return;
                installmentsBox.classList.toggle('hidden', !installmentsToggle.checked);
                if (installmentsToggle.checked) {
                    if (installmentsSelect && parseInt(installmentsSelect.value, 10) < 2) {
                        installmentsSelect.value = '2';
                    }
                    if (fixedToggle) fixedToggle.checked = false;
                    if (recurrenceBox) recurrenceBox.classList.add('hidden');
                }
                ns.updateInstallmentsSummary();
            });
        }

        if (installmentsSelect) {
            installmentsSelect.addEventListener('change', ns.updateInstallmentsSummary);
        }

        if (fixedToggle) {
            fixedToggle.addEventListener('change', function() {
                if (fixedToggle.checked) {
                    if (installmentsToggle) installmentsToggle.checked = false;
                    if (installmentsBox) installmentsBox.classList.add('hidden');
                }
                if (recurrenceBox) recurrenceBox.classList.toggle('hidden', !fixedToggle.checked);
                if (totalOccurrencesBox) totalOccurrencesBox.classList.toggle('hidden', !fixedToggle.checked);
            });
        }

        // Initial visibility
        if (recurrenceBox) recurrenceBox.classList.toggle('hidden', !(fixedToggle && fixedToggle.checked));
        if (totalOccurrencesBox) totalOccurrencesBox.classList.toggle('hidden', !(fixedToggle && fixedToggle.checked));
        if (installmentsBox) installmentsBox.classList.toggle('hidden', !(installmentsToggle && installmentsToggle.checked));
    }

    // --- Visual Modals (Phase D.5.3.9.e.1) ---

    ns.openModal = function(type, root) {
        var modalBackdrop = findInRoot(root, '#formModalBackdrop');
        var originModal = findInRoot(root, '#origemModal');
        var categoryModal = findInRoot(root, '#categoriaModal');

        if (modalBackdrop) modalBackdrop.classList.remove('hidden');
        if (originModal) originModal.classList.toggle('hidden', type !== 'origin');
        if (categoryModal) categoryModal.classList.toggle('hidden', type !== 'category');
        if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen('form-lancamento:' + type);
    };

    ns.closeModal = function(root) {
        var modalBackdrop = findInRoot(root, '#formModalBackdrop');
        var originModal = findInRoot(root, '#origemModal');
        var categoryModal = findInRoot(root, '#categoriaModal');

        if (modalBackdrop) modalBackdrop.classList.add('hidden');
        if (originModal) originModal.classList.add('hidden');
        if (categoryModal) categoryModal.classList.add('hidden');
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
    };

    ns.visibleModalPanel = function(root) {
        var originModal = findInRoot(root, '#origemModal');
        var categoryModal = findInRoot(root, '#categoriaModal');
        if (originModal && !originModal.classList.contains('hidden')) return originModal.querySelector('div');
        if (categoryModal && !categoryModal.classList.contains('hidden')) return categoryModal.querySelector('div');
        return null;
    };

    var modalInitSet = new WeakSet();

    ns.initFormModals = function(root) {
        var formEl = findInRoot(root, '#lancamento-form');
        if (!formEl || modalInitSet.has(formEl)) return;
        modalInitSet.add(formEl);

        var modalBackdrop = findInRoot(root, '#formModalBackdrop');
        if (modalBackdrop && modalBackdrop.dataset.backdropEnhanced !== 'true') {
            modalBackdrop.dataset.backdropEnhanced = 'true';
            modalBackdrop.addEventListener('click', function(e) {
                var panel = ns.visibleModalPanel(root);
                if (!panel || !panel.contains(e.target)) ns.closeModal(root);
            });
        }

        var closeButtons = root.querySelectorAll ? root.querySelectorAll('[data-close-modal]') : [];
        closeButtons.forEach(function(b) {
            if (b.dataset.modalCloseEnhanced !== 'true') {
                b.dataset.modalCloseEnhanced = 'true';
                b.addEventListener('click', function() { ns.closeModal(root); });
            }
        });
    };

    if (!window.__lancamentoFormEscapeRegistered) {
        window.__lancamentoFormEscapeRegistered = true;
        document.addEventListener('keydown', function(e) {
            if (e.key === 'Escape') {
                var modalBackdrop = document.getElementById('formModalBackdrop');
                if (modalBackdrop && !modalBackdrop.classList.contains('hidden')) {
                    e.stopImmediatePropagation();
                    ns.closeModal();
                }
            }
        });
    }

    // --- Category Info Warnings (Limit + Reserve) ---

    ns.updateCategoryInfoWarnings = function() {
        var categoryIdInput = document.getElementById('categoriaId');
        if (!categoryIdInput) return;

        var limitInfo = document.getElementById('categoryLimitInfo');
        var limitInfoText = document.getElementById('limitInfoText');
        var limitOverdraftWarn = document.getElementById('limitOverdraftWarn');
        var reserveInfo = document.getElementById('categoryReserveInfo');
        var reserveInfoText = document.getElementById('reserveInfoText');
        var valueInput = document.getElementById('valor');

        var limitMax = parseInt(categoryIdInput.dataset.limitMax || '0', 10);
        var limitSpent = parseInt(categoryIdInput.dataset.limitSpent || '0', 10);
        var boxId = categoryIdInput.dataset.boxId || '';
        var boxName = categoryIdInput.dataset.boxName || '';
        var boxReserved = parseInt(categoryIdInput.dataset.boxReserved || '0', 10);

        // Limit info
        if (limitMax > 0 && limitInfo && limitInfoText) {
            var categoryName = (document.getElementById('categoriaNome') || {}).textContent || '';
            var remaining = limitMax - limitSpent;
            if (remaining < 0) remaining = 0;
            limitInfoText.textContent = ' ' + categoryName + ' · R$ ' + ns.formatCurrencyCents(limitSpent) + ' de R$ ' + ns.formatCurrencyCents(limitMax) + ' usados este mês.';

            var amountCents = 0;
            if (valueInput) amountCents = ns.parseMoneyCents(valueInput.value);
            if (amountCents > 0 && (limitSpent + amountCents) > limitMax && limitOverdraftWarn) {
                limitOverdraftWarn.classList.remove('hidden');
            } else if (limitOverdraftWarn) {
                limitOverdraftWarn.classList.add('hidden');
            }
            limitInfo.classList.remove('hidden');
        } else if (limitInfo) {
            limitInfo.classList.add('hidden');
        }

        // Reserve info
        if (boxId && reserveInfo && reserveInfoText) {
            reserveInfoText.textContent = ' ' + boxName + ' · R$ ' + ns.formatCurrencyCents(boxReserved) + ' disponível.';
            reserveInfo.classList.remove('hidden');
        } else if (reserveInfo) {
            reserveInfo.classList.add('hidden');
        }
    };

    ns.formatCurrencyCents = function(cents) {
        var reais = Math.floor(Math.abs(cents) / 100);
        var frac = Math.abs(cents) % 100;
        var s = reais.toLocaleString('pt-BR') + ',' + (frac < 10 ? '0' : '') + frac;
        return cents < 0 ? '-' + s : s;
    };

    // --- Box Overdraft Warnings (Phase D.5.3.9.e.1) ---

    ns.syncAllowBoxOverdraftInput = function(root) {
        var allowBoxOverdraftInput = findInRoot(root, '#permitirExcedenteCaixinha');
        var allowBoxOverdraftCheckbox = findInRoot(root, '#allowBoxOverdraftCheckbox');
        if (!allowBoxOverdraftInput) return;
        allowBoxOverdraftInput.value = allowBoxOverdraftCheckbox && allowBoxOverdraftCheckbox.checked ? '1' : '0';
    };

    ns.setBoxOverdraftVisible = function(visible, root) {
        var boxOverdraftWarning = findInRoot(root, '#boxOverdraftWarning');
        var allowBoxOverdraftCheckbox = findInRoot(root, '#allowBoxOverdraftCheckbox');
        if (!boxOverdraftWarning) return;
        boxOverdraftWarning.classList.toggle('hidden', !visible);
        if (!visible && allowBoxOverdraftCheckbox && allowBoxOverdraftCheckbox.checked) {
            allowBoxOverdraftCheckbox.checked = false;
            allowBoxOverdraftCheckbox.dispatchEvent(new Event('input', { bubbles: true }));
            allowBoxOverdraftCheckbox.dispatchEvent(new Event('change', { bubbles: true }));
        }
        ns.syncAllowBoxOverdraftInput(root);
    };

    ns.updateBoxOverdraftWarning = function(root) {
        var transactionType = findInRoot(root, '#tipoLancamento');
        if (!transactionType || transactionType.value !== 'despesa') {
            ns.setBoxOverdraftVisible(false, root);
            return;
        }

        var categoryId = findInRoot(root, '#categoriaId');
        if (!categoryId || !categoryId.value) {
            ns.setBoxOverdraftVisible(false, root);
            return;
        }

        var option = findInRoot(root, '.category-option[data-category-id="' + categoryId.value + '"]');
        var boxId = option ? option.dataset.boxId : categoryId.dataset.boxId;
        if (!boxId) {
            ns.setBoxOverdraftVisible(false, root);
            return;
        }

        var valueInput = findInRoot(root, '#valor');
        var amountCents = ns.parseMoneyCents(valueInput ? valueInput.value : '');
        if (amountCents <= 0) {
            ns.setBoxOverdraftVisible(false, root);
            return;
        }

        var reserved = parseInt((option ? option.dataset.boxReserved : categoryId.dataset.boxReserved) || '0', 10);
        if (!Number.isFinite(reserved)) reserved = 0;
        ns.setBoxOverdraftVisible(amountCents > reserved, root);
    };

    ns.scheduleBoxOverdraftWarningUpdate = function(root) {
        ns.updateCategoryInfoWarnings();
        ns.updateBoxOverdraftWarning(root);
        window.setTimeout(function() {
            ns.updateCategoryInfoWarnings();
            ns.updateBoxOverdraftWarning(root);
        }, 0);
    };

    window.updateBoxOverdraftWarning = function() {
        ns.updateBoxOverdraftWarning();
    };

    var boxOverdraftInitSet = new WeakSet();

    ns.initBoxOverdraft = function(root) {
        var formEl = findInRoot(root, '#lancamento-form');
        if (!formEl || boxOverdraftInitSet.has(formEl)) return;
        boxOverdraftInitSet.add(formEl);

        var allowBoxOverdraftCheckbox = findInRoot(root, '#allowBoxOverdraftCheckbox');
        if (allowBoxOverdraftCheckbox && allowBoxOverdraftCheckbox.dataset.boxOverdraftEnhanced !== 'true') {
            allowBoxOverdraftCheckbox.dataset.boxOverdraftEnhanced = 'true';
            allowBoxOverdraftCheckbox.addEventListener('change', function() {
                ns.syncAllowBoxOverdraftInput(root);
            });
        }
    };

    // --- Series Modal Visual Helpers (Phase D.5.3.9.e.3) ---

    ns.rememberActiveElement = function(root) {
        var active = document.activeElement;
        return active && typeof active.focus === 'function' ? active : null;
    };

    ns.restoreFocus = function(target, root) {
        if (!target || !document.contains(target) || typeof target.focus !== 'function') return;
        window.setTimeout(function() {
            target.focus({ preventScroll: true });
        }, 0);
    };

    ns.focusSeriesModal = function(modal, inputName, root) {
        if (!modal) return;
        window.setTimeout(function() {
            var checked = findInRoot(modal, 'input[name="' + inputName + '"]:checked');
            var fallback = findInRoot(modal, 'button');
            var target = checked || fallback;
            if (target && typeof target.focus === 'function') {
                target.focus({ preventScroll: true });
            }
        }, 0);
    };

    ns.showSeriesModal = function(modal, source, inputName, root) {
        if (!modal || !modal.classList.contains('hidden')) return;
        modal.classList.remove('hidden');
        if (typeof window.overlayDidOpen === 'function') window.overlayDidOpen(source);
        ns.focusSeriesModal(modal, inputName, root);
    };

    ns.hideSeriesModal = function(modal, focusTarget, root) {
        if (!modal || modal.classList.contains('hidden')) return;
        modal.classList.add('hidden');
        if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
        ns.restoreFocus(focusTarget, root);
    };

    var formWiringInitSet = new WeakSet();
    var monthFormatter = new Intl.DateTimeFormat('pt-BR', { month: 'long', year: 'numeric' });
    var faturaSelectorWeakSet = window.__contabaseFaturaSelectorEnhanced || (window.__contabaseFaturaSelectorEnhanced = new WeakSet());
    var faturaCurrentClasses = ['text-indigo-400', 'hover:text-indigo-300', 'decoration-indigo-400/30'];
    var faturaNextClasses = ['text-zinc-500', 'hover:text-zinc-400', 'decoration-zinc-500/30'];

    ns.initFormWiring = function(root) {
        var formEl = findInRoot(root, '#lancamento-form');
        if (!formEl) return;
        if (formWiringInitSet.has(formEl)) return;
        formWiringInitSet.add(formEl);

        var valueInput = findInRoot(root, '#valor');
        var descriptionInput = findInRoot(root, '#descricaoLancamento');
        var dateInput = findInRoot(root, '#dataLancamento');
        var qsa = root.querySelectorAll ? root : document;
        var tabs = qsa.querySelectorAll('[data-type]');
        var transactionType = findInRoot(root, '#tipoLancamento');
        var originId = findInRoot(root, '#origemContaId');
        var originType = findInRoot(root, '#origemTipo');
        var originLabel = findInRoot(root, '#origemLabel');
        var originName = findInRoot(root, '#origemNome');
        var originIconWrap = findInRoot(root, '#origemIconWrap');
        var originIcon = findInRoot(root, '#origemIcon');
        var categoryId = findInRoot(root, '#categoriaId');
        var categoryType = findInRoot(root, '#categoriaType');
        var categoryPicker = findInRoot(root, '#categoriaPicker');
        var categoryName = findInRoot(root, '#categoriaNome');
        var categoryIconWrap = findInRoot(root, '#categoriaIconWrap');
        var categoryIcon = findInRoot(root, '#categoriaIcon');
        var invoiceNotice = findInRoot(root, '#faturaAviso');
        var destinationId = findInRoot(root, '#destinoContaId');
        var destinationPicker = findInRoot(root, '#destinoPicker');
        var destinationName = findInRoot(root, '#destinoNome');
        var destinationIconWrap = findInRoot(root, '#destinoIconWrap');
        var destinationIcon = findInRoot(root, '#destinoIcon');
        var modalBackdrop = findInRoot(root, '#formModalBackdrop');
        var originModal = findInRoot(root, '#origemModal');
        var originModalTitle = findInRoot(root, '#origemModalTitle');
        var cardsModalGroup = findInRoot(root, '#cartoesModalGroup');
        var categoryModal = findInRoot(root, '#categoriaModal');
        var statusBtn = findInRoot(root, '#statusPagamentoBtn');
        var originMode = 'origin';
        var selectedCardClosingDay = null;
        var selectedCardDueDay = null;
        var invoiceNoticeRequestSeq = 0;

        function syncPaymentStatusUI() {
            if (ns.syncPaymentStatusUI) ns.syncPaymentStatusUI();
        }

        function setPaymentStatus(isPaid) {
            if (ns.applyPaymentStatus) ns.applyPaymentStatus(isPaid);
        }

        (function setToday() {
            if (!dateInput || dateInput.value) return;
            var today = new Date();
            var y = today.getFullYear();
            var m = String(today.getMonth() + 1).padStart(2, '0');
            var d = String(today.getDate()).padStart(2, '0');
            dateInput.value = y + '-' + m + '-' + d;
        })();

        if (valueInput && valueInput.dataset.moneyMaskEnhanced !== 'true') {
            valueInput.dataset.moneyMaskEnhanced = 'true';
            valueInput.addEventListener('input', function(event) {
                if (typeof window.maskMoneyRealTime === 'function') {
                    event.target.value = window.maskMoneyRealTime(event.target.value);
                }
            });
        }

        function updateBoxOverdraftWarning() {
            if (ns.updateBoxOverdraftWarning) ns.updateBoxOverdraftWarning();
        }

        window.updateBoxOverdraftWarning = updateBoxOverdraftWarning;

        function getInvoiceText(closingDay, dueDay) {
            var selectedDate = dateInput && dateInput.value ? new Date(dateInput.value + 'T12:00:00') : new Date();
            var closingDate = new Date(selectedDate);
            if (selectedDate.getDate() > Number(closingDay)) closingDate.setMonth(closingDate.getMonth() + 1);
            if (window.isProximaFatura) closingDate.setMonth(closingDate.getMonth() + 1);
            var dueDate = new Date(closingDate);
            if (Number(dueDay) < Number(closingDay)) dueDate.setMonth(dueDate.getMonth() + 1);
            var month = monthFormatter.format(dueDate).replace(' de ', '/');
            return 'Fatura prevista: ' + month + '. Fecha dia ' + closingDay + ' e vence dia ' + dueDay + '.';
        }

        function currentFaturaOffset() {
            var selector = document.getElementById('faturaSelector');
            var input = selector ? selector.querySelector('input[name="fatura_offset"]') : null;
            return input && input.value === 'next' ? 'next' : 'auto';
        }

        function updateInvoiceNotice() {
            if (originType && originType.value === 'cartao' && selectedCardClosingDay && selectedCardDueDay) {
                var fallbackText = getInvoiceText(selectedCardClosingDay, selectedCardDueDay);
                var requestSeq = ++invoiceNoticeRequestSeq;
                if (invoiceNotice) {
                    invoiceNotice.textContent = fallbackText;
                    invoiceNotice.classList.remove('hidden');
                }
                loadInvoiceOptions();
                if (originId && originId.value) {
                    var params = new URLSearchParams();
                    params.set('data', dateInput ? dateInput.value || '' : '');
                    params.set('fatura_offset', currentFaturaOffset());
                    fetch('/cartoes/fatura-destino/' + encodeURIComponent(originId.value) + '?' + params.toString(), {
                        headers: { 'Accept': 'application/json', 'HX-Request': 'true' }
                    })
                        .then(function(response) {
                            if (!response.ok) throw new Error('invoice destination failed');
                            return response.json();
                        })
                        .then(function(payload) {
                            if (requestSeq !== invoiceNoticeRequestSeq) return;
                            if (payload && payload.notice && invoiceNotice) {
                                invoiceNotice.textContent = payload.notice;
                            }
                        })
                        .catch(function() {
                            if (requestSeq === invoiceNoticeRequestSeq && invoiceNotice) {
                                invoiceNotice.textContent = fallbackText;
                            }
                        });
                }
            } else {
                invoiceNoticeRequestSeq++;
                if (invoiceNotice) invoiceNotice.classList.add('hidden');
                hideInvoiceSelector();
            }
        }

        function syncFaturaSelector(selector, usarProximaFatura) {
            var input = selector ? selector.querySelector('input[name="fatura_offset"]') : null;
            var button = selector ? selector.querySelector('button[type="button"]') : null;
            if (!input || !button) return;
            input.value = usarProximaFatura ? 'next' : 'auto';
            button.textContent = usarProximaFatura ? '- Desfazer (lançar na fatura atual)' : '+ Lançar na próxima fatura!';
            button.setAttribute('aria-pressed', usarProximaFatura ? 'true' : 'false');
            button.classList.remove.apply(button.classList, faturaCurrentClasses.concat(faturaNextClasses));
            button.classList.add.apply(button.classList, usarProximaFatura ? faturaNextClasses : faturaCurrentClasses);
        }

        function initFaturaSelector() {
            var selector = document.getElementById('faturaSelector');
            if (!selector) return;
            var input = selector.querySelector('input[name="fatura_offset"]');
            var button = selector.querySelector('button[type="button"]');
            var usarProximaFatura = input ? input.value === 'next' : false;
            syncFaturaSelector(selector, usarProximaFatura);
            window.isProximaFatura = usarProximaFatura;
            if (faturaSelectorWeakSet.has(selector)) return;
            faturaSelectorWeakSet.add(selector);
            selector.addEventListener('fatura-toggle', function(e) {
                var nextState = e.detail === true;
                syncFaturaSelector(selector, nextState);
                window.isProximaFatura = nextState;
                updateInvoiceNotice();
            });
            if (button) {
                button.addEventListener('click', function() {
                    var currentInput = selector.querySelector('input[name="fatura_offset"]');
                    var nextState = !(currentInput && currentInput.value === 'next');
                    button.dispatchEvent(new CustomEvent('fatura-toggle', { bubbles: true, detail: nextState }));
                });
            }
        }

        initFaturaSelector();

        function loadInvoiceOptions() {
            var selector = document.getElementById('faturaSelector');
            if (!selector || !originId || !originId.value) return;
            selector.classList.remove('hidden');
        }

        function hideInvoiceSelector() {
            var selector = document.getElementById('faturaSelector');
            if (selector) selector.classList.add('hidden');
        }

        function closeModal() {
            if (ns.closeModal) ns.closeModal();
        }

        function selectOrigin(option) {
            if (!ns.applyOriginSelection) return;

            var kind = option.dataset.originKind;
            var isDest = originMode === 'destination';

            ns.applyOriginSelection(
                {
                    mode: isDest ? 'destination' : 'origin',
                    id: option.dataset.originId,
                    kind: kind,
                    name: option.dataset.originName,
                    icon: option.dataset.originIcon,
                    color: option.dataset.originColor,
                    providerMark: option.dataset.originProviderMark || '',
                    closingDay: option.dataset.closingDay || null,
                    dueDay: option.dataset.dueDay || null,
                    originLabel: isDest ? undefined : (kind === 'cartao' ? 'Cartão de crédito' : (transactionType && transactionType.value === 'receita') ? 'Conta de entrada' : 'Conta de origem')
                },
                {
                    onCardStateCaptured: function(closingDay, dueDay) {
                        selectedCardClosingDay = closingDay;
                        selectedCardDueDay = dueDay;
                    },
                    onCardSelected: function() {
                        if (statusBtn) statusBtn.classList.add('hidden');
                        setPaymentStatus(true);
                    },
                    onAccountSelected: function() {
                        if (statusBtn) statusBtn.classList.remove('hidden');
                        syncPaymentStatusUI();
                    },
                    onInvoiceNoticeUpdate: function() {
                        updateInvoiceNotice();
                    },
                    onCloseModal: function() {
                        closeModal();
                    }
                }
            );
        }

        function selectCategory(option) {
            if (!ns.applyCategorySelection) return;
            ns.applyCategorySelection(
                {
                    id: option.dataset.categoryId,
                    name: option.dataset.categoryName,
                    icon: option.dataset.categoryIcon,
                    color: option.dataset.categoryColor,
                    type: option.dataset.categoryType || '',
                    boxId: option.dataset.boxId,
                    boxReserved: option.dataset.boxReserved,
                    boxName: option.dataset.boxName || '',
                    limitMax: option.dataset.limitMax || '0',
                    limitSpent: option.dataset.limitSpent || '0'
                },
                {
                    visualMode: 'local-option',
                    onBoxOverdraftChange: updateBoxOverdraftWarning,
                    onCloseModal: closeModal,
                    onCategoryInputSync: function() {
                        var categoryQueryInput = document.querySelector('#categoriaPicker input[type="text"]');
                        if (categoryQueryInput) {
                            categoryQueryInput.value = option.dataset.categoryName || '';
                            categoryQueryInput.dispatchEvent(new Event('input', { bubbles: true }));
                            categoryQueryInput.dispatchEvent(new Event('change', { bubbles: true }));
                        }
                    }
                }
            );
        }

        window.selectPickerItem = function(id, name, icon, color, type, boxId, boxReserved, boxName, limitMax, limitSpent) {
            if (!ns.applyCategorySelection) return;
            ns.applyCategorySelection(
                { id: id, name: name, icon: icon, color: color, type: type || undefined, boxId: boxId, boxReserved: boxReserved, boxName: boxName || '', limitMax: limitMax || '0', limitSpent: limitSpent || '0' },
                {
                    visualMode: 'htmx-picker',
                    onBoxOverdraftChange: function() { if (window.updateBoxOverdraftWarning) window.updateBoxOverdraftWarning(); },
                    onRefreshIcons: function() { if (typeof window.refreshIcons === 'function') window.refreshIcons(); }
                }
            );
        };

        window.applyPredictiveSuggestion = function(item) {
            window.__predictiveFormApplyFlag = true;
            if (descriptionInput) {
                descriptionInput.value = item.dataset.description || '';
            }

            if (item.dataset.type) {
                setType(item.dataset.type);
            }

            var originOption = null;
            qsa.querySelectorAll('.origin-option').forEach(function(option) {
                if (option.dataset.originId === item.dataset.accountId && option.dataset.originKind === item.dataset.accountKind) {
                    originOption = option;
                }
            });
            if (originOption) {
                selectOrigin(originOption);
            }

            var categoryOption = null;
            qsa.querySelectorAll('.category-option').forEach(function(option) {
                if (option.dataset.categoryId === item.dataset.categoryId) {
                    categoryOption = option;
                }
            });
            if (categoryOption) {
                selectCategory(categoryOption);
            }

            if (item.dataset.destinationId) {
                var destOption = null;
                qsa.querySelectorAll('.origin-option').forEach(function(option) {
                    if (option.dataset.originId === item.dataset.destinationId) {
                        destOption = option;
                    }
                });
                if (destOption) {
                    var savedMode = originMode;
                    originMode = 'destination';
                    selectOrigin(destOption);
                    originMode = savedMode;
                }
            }

            if (window.__clearPredictiveForm) window.__clearPredictiveForm();
            window.__predictiveFormApplyFlag = false;
        };

        function ensureCategoryForType(type) {
            if (type === 'transferencia') return;
            var expected = ns.expectedCategoryType ? ns.expectedCategoryType(type) : (type === 'receita' ? 'INCOME' : 'EXPENSE');
            if (ns.filterCategoryOptions) ns.filterCategoryOptions(type);
            if (!categoryType || !categoryId) return;
            if (categoryType.value === expected && categoryId.value) return;
            categoryId.value = '';
            categoryType.value = '';
            categoryId.dataset.boxId = '';
            categoryId.dataset.boxReserved = '0';
            if (categoryName) {
                categoryName.textContent = 'Categoria';
            }
            if (categoryIconWrap && categoryIcon && ns.updateIcon) {
                ns.updateIcon(categoryIconWrap, categoryIcon, 'tag', 'rose');
            }
            var categoryQueryInput = document.querySelector('#categoriaPicker input[type="text"]');
            if (categoryQueryInput) {
                categoryQueryInput.value = '';
                categoryQueryInput.dispatchEvent(new Event('input', { bubbles: true }));
                categoryQueryInput.dispatchEvent(new Event('change', { bubbles: true }));
            }
            updateBoxOverdraftWarning();
        }

        function setType(type) {
            if (!ns.applyTransactionType) return;

            ns.applyTransactionType(
                { type: type, currentOriginType: originType ? originType.value : '' },
                {
                    onUpdateTypeLabels: ns.updateTypeLabels || function() {}
                }
            );

            var isTransfer = type === 'transferencia';
            var isIncome = type === 'receita';

            if (isTransfer || isIncome) {
                if (originType && originType.value === 'cartao') {
                    var firstConta = qsa.querySelector ? qsa.querySelector('[data-origin-kind="conta"]') : document.querySelector('[data-origin-kind="conta"]');
                    if (firstConta) firstConta.click();
                }
            }
            if (isTransfer) {
                if (categoryId) { categoryId.value = ''; categoryId.dataset.boxId = ''; categoryId.dataset.boxReserved = '0'; }
                if (categoryType) categoryType.value = '';
            } else {
                ensureCategoryForType(type);
            }
            updateInvoiceNotice();
            updateBoxOverdraftWarning();
        }

        qsa.querySelectorAll('.origin-option').forEach(function(o) {
            if (originId && o.dataset.originId === originId.value && o.dataset.originKind === 'cartao') {
                selectedCardClosingDay = o.dataset.closingDay || null;
                selectedCardDueDay = o.dataset.dueDay || null;
            }
        });

        (function initStatusToggle() {
            if (originType && originType.value === 'cartao') {
                if (statusBtn) statusBtn.classList.add('hidden');
                setPaymentStatus(true);
            }
        })();

        tabs.forEach(function(t) { t.addEventListener('click', function() { setType(t.dataset.type); }); });
        var origemPicker = findInRoot(root, '#origemPicker');
        if (origemPicker) {
            origemPicker.addEventListener('click', function() {
                originMode = 'origin';
                if (originModalTitle) originModalTitle.textContent = (transactionType && transactionType.value === 'receita') ? 'Conta de entrada' : 'Conta de origem';
                if (cardsModalGroup) cardsModalGroup.classList.toggle('hidden', transactionType && transactionType.value !== 'despesa');
                if (ns.openModal) ns.openModal('origin');
            });
        }
        if (destinationPicker) {
            destinationPicker.addEventListener('click', function() {
                originMode = 'destination';
                if (originModalTitle) originModalTitle.textContent = 'Conta de destino';
                if (cardsModalGroup) cardsModalGroup.classList.add('hidden');
                if (ns.openModal) ns.openModal('origin');
            });
        }

        qsa.querySelectorAll('.origin-option').forEach(function(o) { o.addEventListener('click', function() { selectOrigin(o); }); });
        qsa.querySelectorAll('.category-option').forEach(function(o) { o.addEventListener('click', function() { selectCategory(o); }); });
        if (valueInput) {
            valueInput.addEventListener('input', function() {
                if (ns.updateInstallmentsSummary) ns.updateInstallmentsSummary();
                if (ns.scheduleBoxOverdraftWarningUpdate) ns.scheduleBoxOverdraftWarningUpdate();
            });
        }
        if (dateInput) {
            dateInput.addEventListener('change', function() {
                updateInvoiceNotice();
            });
        }

        setType((transactionType && transactionType.value) || 'despesa');
        syncPaymentStatusUI();
        if (ns.syncAllowBoxOverdraftInput) ns.syncAllowBoxOverdraftInput();
        updateBoxOverdraftWarning();
        var csrfInput = findInRoot(root, '#csrfToken');
        if (csrfInput && typeof csrfToken === 'function') csrfInput.value = csrfToken();
        if (typeof window.refreshIcons === 'function') window.refreshIcons();

        var seriesModalEnhanced = window.__contabaseSeriesModalEnhanced || (window.__contabaseSeriesModalEnhanced = new WeakSet());
        var lastSeriesScopeFocus = null;
        var lastSeriesDeleteFocus = null;

        function isValidSeriesScope(scope) {
            return scope === 'single' || scope === 'future' || scope === 'all';
        }

        function selectedSeriesScope(modal, inputName) {
            var checked = modal ? modal.querySelector('input[name="' + inputName + '"]:checked') : null;
            return checked && isValidSeriesScope(checked.value) ? checked.value : '';
        }

        function htmxClient() {
            if (window.htmx && typeof window.htmx.ajax === 'function') return window.htmx;
            if (typeof htmx !== 'undefined' && htmx && typeof htmx.ajax === 'function') return htmx;
            return null;
        }

        function deleteSeriesTransactionAndRefresh(txId, scope) {
            var client = htmxClient();
            if (!client || !txId) {
                if (typeof window.deleteTransactionAndRefresh === 'function') {
                    window.deleteTransactionAndRefresh(txId, scope);
                }
                return;
            }
            if (scope !== 'single') {
                client.ajax('DELETE', '/transacoes/' + txId + '?escopo=' + scope, { swap: 'none' });
                return;
            }
            client.ajax('DELETE', '/transacoes/' + txId + '?escopo=' + scope, {
                target: '#tx-' + txId,
                swap: 'delete'
            });
        }

        function initSeriesModals(form) {
            if (!form || form.dataset.seriesEdit !== 'true') return;
            if (seriesModalEnhanced.has(form)) return;
            seriesModalEnhanced.add(form);

            var scopeModal = document.getElementById('seriesScopeModal');
            var deleteModal = document.getElementById('seriesDeleteModal');
            var scopeInput = document.getElementById('escopoEdicao');
            var deleteOpenButton = form.querySelector('[data-series-delete-open]');
            var scopeCancel = scopeModal ? scopeModal.querySelector('[data-series-scope-cancel]') : null;
            var scopeConfirm = scopeModal ? scopeModal.querySelector('[data-series-scope-confirm]') : null;
            var deleteCancel = deleteModal ? deleteModal.querySelector('[data-series-delete-cancel]') : null;
            var deleteConfirm = deleteModal ? deleteModal.querySelector('[data-series-delete-confirm]') : null;

            function shouldAskScope(event) {
                return event.target === form && form.dataset.seriesEdit === 'true' && !form.dataset.scopeConfirmed;
            }

            function openScopeModal() {
                lastSeriesScopeFocus = ns.rememberActiveElement ? ns.rememberActiveElement() : null;
                if (ns.showSeriesModal) ns.showSeriesModal(scopeModal, 'form-lancamento:series-scope', 'escopo_choice');
            }

            function closeScopeModal(restore) {
                if (ns.hideSeriesModal) ns.hideSeriesModal(scopeModal, restore === false ? null : lastSeriesScopeFocus);
                lastSeriesScopeFocus = null;
            }

            function openDeleteModal() {
                lastSeriesDeleteFocus = ns.rememberActiveElement ? ns.rememberActiveElement() : null;
                if (deleteModal) delete deleteModal.dataset.submitting;
                if (deleteConfirm) deleteConfirm.disabled = false;
                if (ns.showSeriesModal) ns.showSeriesModal(deleteModal, 'form-lancamento:series-delete', 'delete_choice');
            }

            function closeDeleteModal(restore) {
                if (ns.hideSeriesModal) ns.hideSeriesModal(deleteModal, restore === false ? null : lastSeriesDeleteFocus);
                lastSeriesDeleteFocus = null;
            }

            window.closeLancamentoSeriesScopeModal = closeScopeModal;
            window.closeLancamentoSeriesDeleteModal = closeDeleteModal;

            form.addEventListener('htmx:confirm', function(event) {
                if (!shouldAskScope(event)) return;
                event.preventDefault();
                openScopeModal();
            });

            form.addEventListener('submit', function(event) {
                if (!shouldAskScope(event)) return;
                event.preventDefault();
                openScopeModal();
            });

            if (scopeModal) {
                scopeModal.addEventListener('click', function(event) {
                    if (event.target === scopeModal) closeScopeModal();
                });
            }
            if (deleteModal) {
                deleteModal.addEventListener('click', function(event) {
                    if (event.target === deleteModal) closeDeleteModal();
                });
            }
            if (scopeCancel) scopeCancel.addEventListener('click', function() { closeScopeModal(); });
            if (deleteCancel) deleteCancel.addEventListener('click', function() { closeDeleteModal(); });
            if (deleteOpenButton) deleteOpenButton.addEventListener('click', openDeleteModal);

            if (scopeConfirm) {
                scopeConfirm.addEventListener('click', function() {
                    var scope = selectedSeriesScope(scopeModal, 'escopo_choice');
                    if (!scope || !scopeInput) return;
                    scopeInput.value = scope;
                    form.dataset.scopeConfirmed = 'true';
                    closeScopeModal(false);
                    form.requestSubmit();
                });
            }

            if (deleteConfirm) {
                deleteConfirm.addEventListener('click', function() {
                    if (!deleteModal || deleteModal.dataset.submitting === 'true') return;
                    var scope = selectedSeriesScope(deleteModal, 'delete_choice');
                    if (!scope) return;
                    var transactionID = deleteConfirm.dataset.transactionId || '';
                    var returnInvoiceID = deleteConfirm.dataset.returnInvoiceId || '';
                    deleteModal.dataset.submitting = 'true';
                    deleteConfirm.disabled = true;
                    closeDeleteModal(false);
                    if (deleteConfirm.dataset.returnInvoice === 'true' && returnInvoiceID) {
                        if (window.htmx && typeof window.htmx.ajax === 'function') {
                            window.htmx.ajax('DELETE', '/transacoes/' + transactionID + '?escopo=' + scope + '&invoice_id=' + returnInvoiceID + '&from_sheet=1', { swap: 'none' });
                        }
                    } else {
                        deleteSeriesTransactionAndRefresh(transactionID, scope);
                    }
                    if (typeof window.closeBottomSheet === 'function') window.closeBottomSheet();
                });
            }
        }

        initSeriesModals(formEl);

        if (formEl.dataset.contactValidationEnhanced !== 'true') {
            formEl.dataset.contactValidationEnhanced = 'true';
            formEl.addEventListener('submit', function(event) {
                if (ns.validateContactSelection && !ns.validateContactSelection()) {
                    event.preventDefault();
                    event.stopImmediatePropagation();
                }
            }, true);
        }
        var isEdit = formEl.hasAttribute('hx-put');
        var canFocusValue = window.matchMedia && window.matchMedia('(pointer: fine)').matches;
        if (valueInput && !isEdit && canFocusValue) {
            window.setTimeout(function() {
                valueInput.focus({ preventScroll: true });
                if (valueInput.value) valueInput.setSelectionRange(valueInput.value.length, valueInput.value.length);
            }, 220);
        }
    };

    document.addEventListener('DOMContentLoaded', function() {
        initDetailsToggle(document);
        initContactCombobox(document);
        initInstallmentRecurrenceVisuals(document);
        ns.initPaymentStatusControls(document);
        ns.initBoxOverdraft(document);
        ns.initFormModals(document);
        ns.initFormWiring(document);
    });
    document.body.addEventListener('htmx:afterSwap', function(event) {
        var target = (event && event.detail && event.detail.target) || document;
        initDetailsToggle(target);
        initContactCombobox(target);
        initInstallmentRecurrenceVisuals(target);
        ns.initPaymentStatusControls(target);
        ns.initBoxOverdraft(target);
        ns.initFormModals(target);
        ns.initFormWiring(target);
    });

    // --- Autofocus valor input ao abrir form ---
    function focusValorInput(root) {
        var amountInput = root.querySelector ? root.querySelector('#valor') : null;
        if (!amountInput) return;
        if (document.activeElement && document.activeElement !== document.body && document.activeElement.tagName !== 'BODY') return;
        if (document.getElementById('formModalBackdrop') && !document.getElementById('formModalBackdrop').classList.contains('hidden')) return;
        if (document.getElementById('picker-modal-container') && document.getElementById('picker-modal-container').children.length > 0) return;
        requestAnimationFrame(function() {
            amountInput.focus({ preventScroll: true });
            var v = amountInput.value || '';
            if (v === '' || v === '0,00' || v === 'R$ 0,00') {
                try { amountInput.select(); } catch (_) {}
            }
        });
    }

    document.body.addEventListener('htmx:afterSwap', function(e) {
        if (!e.detail || !e.detail.target) return;
        if (e.detail.target.id !== 'bottom-sheet-container') return;
        focusValorInput(e.detail.target);
    });
})();
