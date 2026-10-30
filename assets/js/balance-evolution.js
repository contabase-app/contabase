/* Evolução do saldo — painel do Dashboard.
   Consome somente /dashboard/balance-series (workspace ativo no servidor).
   Nenhum valor financeiro é calculado aqui: o servidor envia centavos inteiros
   e a variação já calculada. O JavaScript apenas posiciona e formata. */
(function () {
    'use strict';

    var ENDPOINT = '/dashboard/balance-series';
    var PERIODS = ['30d', '90d', '6m', '12m'];
    var currentController = null;
    var cache = Object.create(null);
    var MONTHS = ['jan', 'fev', 'mar', 'abr', 'mai', 'jun', 'jul', 'ago', 'set', 'out', 'nov', 'dez'];

    function parseDate(value) {
        var parts = String(value || '').split('-');
        return {
            year: Number(parts[0]),
            month: Number(parts[1]),
            day: Number(parts[2])
        };
    }

    function shortDate(value) {
        var d = parseDate(value);
        return String(d.day).padStart(2, '0') + ' ' + MONTHS[d.month - 1];
    }

    function longDate(value) {
        var d = parseDate(value);
        return String(d.day).padStart(2, '0') + ' de ' + MONTHS[d.month - 1] + ' de ' + d.year;
    }

    function numericDate(value) {
        var d = parseDate(value);
        return String(d.day).padStart(2, '0') + '/' + String(d.month).padStart(2, '0') + '/' + d.year;
    }

    function formatCents(cents) {
        var raw = String(cents == null ? '0' : cents);
        var negative = raw.charAt(0) === '-';
        if (negative) raw = raw.slice(1);
        raw = raw.replace(/^0+(?=\d)/, '');
        if (raw.length < 3) raw = new Array(4 - raw.length).join('0') + raw;
        var reais = raw.slice(0, -2).replace(/\B(?=(\d{3})+(?!\d))/g, '.');
        var centavos = raw.slice(-2);
        return (negative ? '-' : '') + 'R$ ' + reais + ',' + centavos;
    }

    function formatPercentTenths(value) {
        var sign = value > 0 ? '+' : (value < 0 ? '-' : '');
        var abs = Math.abs(value);
        return sign + Math.floor(abs / 10) + ',' + (abs % 10) + '%';
    }

    function privacyEnabled() {
        return document.body.classList.contains('privacy-enabled');
    }

    function createController(root) {
        var dashboard = root.closest('#dashboard-content');
        var toggle = dashboard ? dashboard.querySelector('[data-balance-series-toggle]') : null;
        var toggleLabel = toggle ? toggle.querySelector('[data-balance-series-toggle-label]') : null;
        var closeButton = root.querySelector('[data-balance-close]');
        var status = root.querySelector('[data-balance-status]');
        var composition = root.querySelector('[data-balance-composition]');
        var periodButtons = Array.prototype.slice.call(root.querySelectorAll('[data-balance-period]'));
        var variation = root.querySelector('[data-balance-variation]');
        var coverage = root.querySelector('[data-balance-coverage]');
        var loading = root.querySelector('[data-balance-loading]');
        var message = root.querySelector('[data-balance-message]');
        var messageText = root.querySelector('[data-balance-message-text]');
        var retry = root.querySelector('[data-balance-retry]');
        var frame = root.querySelector('[data-balance-frame]');
        var svg = root.querySelector('[data-balance-svg]');
        var table = root.querySelector('[data-balance-table]');
        var tableBody = root.querySelector('[data-balance-table-body]');
        var summary = root.querySelector('[data-balance-summary]');
        var live = root.querySelector('[data-balance-live]');
        var limitations = root.querySelector('[data-balance-limitations]');
        var caveat = root.querySelector('[data-balance-caveat]');

        var state = {
            open: false,
            period: '30d',
            data: null,
            requestSeq: 0,
            controller: null,
            chart: null
        };

        if (!toggle || !frame || !svg) {
            return { init: function () {}, destroy: function () {} };
        }

        function setStatus(text, tone) {
            if (!status) return;
            if (!text) {
                status.hidden = true;
                return;
            }
            status.hidden = false;
            status.textContent = text;
            status.classList.toggle('is-partial', tone === 'partial');
            status.classList.toggle('is-unavailable', tone === 'unavailable');
        }

        function resetMeta() {
            variation.textContent = '—';
            variation.classList.remove('is-positive', 'is-negative');
            coverage.textContent = '—';
            limitations.textContent = '';
            if (composition) {
                composition.hidden = true;
                composition.removeAttribute('title');
            }
            if (caveat) {
                caveat.hidden = true;
                caveat.textContent = '';
            }
            if (summary) summary.textContent = '';
        }

        function setLoading(isLoading) {
            loading.hidden = !isLoading;
            root.setAttribute('aria-busy', String(!!isLoading));
            if (isLoading) {
                state.data = null;
                resetMeta();
                message.hidden = true;
                frame.hidden = true;
                table.hidden = true;
                setStatus('Carregando', '');
            }
        }

        function showMessage(text, canRetry) {
            message.hidden = false;
            messageText.textContent = text;
            retry.hidden = !canRetry;
            frame.hidden = true;
            table.hidden = true;
            loading.hidden = true;
            root.setAttribute('aria-busy', 'false');
        }

        function clearChart() {
            if (state.chart) {
                state.chart.destroy();
                state.chart = null;
            }
        }

        function renderLimitations(data) {
            limitations.textContent = '';
            var caveatMessage = '';
            (data.limitations || []).forEach(function (item) {
                var li = document.createElement('li');
                li.textContent = item.message || item.code || '';
                limitations.appendChild(li);
                if (!caveatMessage && (item.code === 'account_archived_in_period' || item.code === 'partial_coverage')) {
                    caveatMessage = item.code === 'account_archived_in_period'
                        ? 'Contas arquivadas no período deixam de compor o total a partir do arquivamento; uma queda no gráfico pode não ser saída de caixa.'
                        : (item.message || '');
                }
            });
            if (composition) {
                var compositionChanged = (data.limitations || []).some(function (item) {
                    return item.code === 'account_archived_in_period' || item.code === 'account_lifecycle';
                });
                composition.hidden = !compositionChanged;
                if (compositionChanged) {
                    composition.title = 'Contas entraram ou saíram do consolidado no período; a variação não é receita nem despesa.';
                } else {
                    composition.removeAttribute('title');
                }
            }
            if (caveat) {
                caveat.hidden = !caveatMessage;
                caveat.textContent = caveatMessage;
            }
        }

        function renderTable(points) {
            tableBody.textContent = '';
            points.forEach(function (point) {
                var tr = document.createElement('tr');
                var d = document.createElement('td');
                d.textContent = numericDate(point.date);
                var v = document.createElement('td');
                var s = document.createElement('span');
                s.setAttribute('data-sensitive', '');
                s.textContent = formatCents(point.balance_cents);
                v.appendChild(s);
                tr.appendChild(d);
                tr.appendChild(v);
                tableBody.appendChild(tr);
            });
        }

        function renderSummary(data) {
            if (!summary || !data.points || data.points.length < 2) {
                if (summary) summary.textContent = '';
                return;
            }
            var first = data.points[0];
            var last = data.points[data.points.length - 1];
            if (privacyEnabled()) {
                summary.textContent = 'Gráfico de evolução do saldo consolidado de ' + longDate(first.date) + ' a ' + longDate(last.date) + '. Valores ocultos pelo modo de privacidade.';
            } else {
                summary.textContent = 'Gráfico de evolução do saldo consolidado de ' + longDate(first.date) + ' a ' + longDate(last.date) + ', de ' + formatCents(first.balance_cents) + ' a ' + formatCents(last.balance_cents) + '.';
            }
        }

        function renderVariation(data) {
            variation.classList.remove('is-positive', 'is-negative');
            variation.title = 'Variação do saldo consolidado entre o primeiro e o último ponto do período. Inclui contas que entraram ou saíram do consolidado e pode diferir da tendência de 30 dias do KPI.';
            if (!data.variation || !data.variation.available) {
                variation.textContent = data.variation && data.variation.reason === 'insufficient_base' ? 'Sem base' : '—';
                return;
            }
            var v = data.variation;
            if (v.capped) {
                variation.textContent = v.direction === 'down' ? '<-999%' : '>+999%';
            } else {
                var signedPercent = v.direction === 'down' ? -v.percent_tenths : v.percent_tenths;
                variation.textContent = formatPercentTenths(signedPercent);
            }
            if (v.direction === 'up') variation.classList.add('is-positive');
            if (v.direction === 'down') variation.classList.add('is-negative');
        }

        function renderData(data) {
            state.data = data;
            loading.hidden = true;
            root.setAttribute('aria-busy', 'false');
            renderLimitations(data);
            renderVariation(data);

            coverage.textContent = data.coverage_start && data.coverage_end
                ? shortDate(data.coverage_start) + ' – ' + shortDate(data.coverage_end)
                : '—';

            if (data.status === 'unavailable') {
                var text = 'Histórico indisponível para este período.';
                var tone = 'unavailable';
                if (data.reason === 'no_accounts') {
                    text = 'Ainda não há contas reais para compor um histórico.';
                    tone = 'partial';
                } else if (data.reason === 'reconciliation_mismatch') {
                    text = 'Não foi possível reconstruir o histórico com segurança: a série não reconciliou com o saldo consolidado atual. Nada foi exibido para não apresentar uma aproximação como fato.';
                }
                setStatus(tone === 'partial' ? 'Sem dados' : 'Indisponível', tone);
                showMessage(text, false);
                table.hidden = true;
                clearChart();
                if (summary) summary.textContent = '';
                return;
            }

            if (!data.points || data.points.length < 2) {
                setStatus('Cobertura parcial', 'partial');
                showMessage('Ainda não há pontos suficientes para desenhar uma evolução neste período.', false);
                table.hidden = true;
                clearChart();
                if (summary) summary.textContent = '';
                return;
            }

            setStatus(data.status === 'partial' ? 'Cobertura parcial' : 'Reconstruído', data.status === 'partial' ? 'partial' : '');
            message.hidden = true;
            frame.hidden = false;
            table.hidden = false;
            renderTable(data.points);
            renderSummary(data);
            clearChart();
            state.chart = createChart(svg, frame, live);
            state.chart.setMasked(privacyEnabled());
            state.chart.setData(data.points);
            if (privacyEnabled()) {
                state.chart.setMasked(true);
            }
        }

        function selectPeriod(period) {
            if (PERIODS.indexOf(period) === -1) return;
            state.period = period;
            periodButtons.forEach(function (button) {
                button.setAttribute('aria-pressed', String(button.getAttribute('data-balance-period') === period));
            });
            if (cache[period]) {
                renderData(cache[period]);
                return;
            }
            load(period, false);
        }

        function load(period, force) {
            var seq = ++state.requestSeq;
            if (state.controller) {
                state.controller.abort();
                state.controller = null;
            }
            if (cache[period] && !force) {
                renderData(cache[period]);
                return;
            }
            setLoading(true);
            var controller = new AbortController();
            state.controller = controller;
            fetch(ENDPOINT + '?period=' + encodeURIComponent(period), {
                method: 'GET',
                headers: { 'Accept': 'application/json' },
                credentials: 'same-origin',
                cache: 'no-store',
                signal: controller.signal
            }).then(function (response) {
                if (!response.ok) {
                    throw new Error('http_' + response.status);
                }
                return response.json();
            }).then(function (data) {
                if (seq !== state.requestSeq || period !== state.period) return;
                state.controller = null;
                cache[period] = data;
                renderData(data);
            }).catch(function (error) {
                if (error && error.name === 'AbortError') return;
                if (seq !== state.requestSeq || period !== state.period) return;
                state.controller = null;
                resetMeta();
                setStatus('Indisponível', 'unavailable');
                showMessage('Não foi possível carregar o histórico agora.', true);
                clearChart();
            });
        }

        function openPanel() {
            if (state.open) return;
            state.open = true;
            root.hidden = false;
            root.classList.remove('is-entering');
            // Reflow para reiniciar a animação somente quando o movimento é aceito.
            void root.offsetWidth;
            root.classList.add('is-entering');
            toggle.setAttribute('aria-expanded', 'true');
            if (toggleLabel) toggleLabel.textContent = 'Recolher evolução';
            if (cache[state.period]) {
                renderData(cache[state.period]);
            } else {
                load(state.period, false);
            }
        }

        function closePanel(returnFocus) {
            if (!state.open) return;
            state.open = false;
            root.hidden = true;
            toggle.setAttribute('aria-expanded', 'false');
            if (toggleLabel) toggleLabel.textContent = 'Ver evolução';
            if (returnFocus) toggle.focus();
        }

        toggle.addEventListener('click', function () {
            if (state.open) closePanel(false);
            else openPanel();
        });
        if (closeButton) {
            closeButton.addEventListener('click', function () {
                closePanel(true);
            });
        }
        periodButtons.forEach(function (button) {
            button.addEventListener('click', function () {
                selectPeriod(button.getAttribute('data-balance-period'));
            });
        });
        retry.addEventListener('click', function () {
            load(state.period, true);
        });

        var localPrivacyObserver = null;
        function onPrivacyChange() {
            if (state.chart) state.chart.setMasked(privacyEnabled());
            if (state.data) renderSummary(state.data);
            live.textContent = '';
        }
        if (typeof MutationObserver === 'function') {
            localPrivacyObserver = new MutationObserver(onPrivacyChange);
            localPrivacyObserver.observe(document.body, { attributes: true, attributeFilter: ['class'] });
        }

        return {
            root: root,
            init: function () {
                toggle.setAttribute('aria-expanded', 'false');
                root.hidden = true;
            },
            refresh: function () {
                if (state.open) load(state.period, true);
            },
            destroy: function () {
                clearChart();
                if (state.controller) state.controller.abort();
                if (localPrivacyObserver) {
                    localPrivacyObserver.disconnect();
                    localPrivacyObserver = null;
                }
            }
        };
    }

    function createChart(svg, frame, live) {
        var NS = 'http://www.w3.org/2000/svg';
        var points = [];
        var positions = [];
        var labels = [];
        var width = 0;
        var height = 0;
        var active = -1;
        var masked = false;
        var observer = null;
        var crosshair = null;
        var activeDot = null;
        var tooltipGroup = null;
        var tooltipBg = null;
        var tooltipDateText = null;
        var tooltipValueText = null;
        var resizeFrame = 0;

        function el(name, attrs) {
            var node = document.createElementNS(NS, name);
            Object.keys(attrs || {}).forEach(function (key) {
                node.setAttribute(key, String(attrs[key]));
            });
            return node;
        }

        function formatAxis(cents) {
            if (masked) return '••••';
            var negative = cents < 0;
            var abs = Math.abs(cents) / 100;
            var text;
            if (abs >= 1000000) {
                text = 'R$ ' + (abs / 1000000).toLocaleString('pt-BR', { maximumFractionDigits: 1 }) + ' mi';
            } else if (abs >= 1000) {
                text = 'R$ ' + (abs / 1000).toLocaleString('pt-BR', { maximumFractionDigits: 1 }) + ' mil';
            } else {
                text = 'R$ ' + abs.toLocaleString('pt-BR', { maximumFractionDigits: 0 });
            }
            return (negative ? '-' : '') + text;
        }

        function smoothPath(list) {
            var d = 'M ' + list[0].x.toFixed(2) + ' ' + list[0].y.toFixed(2);
            for (var i = 0; i < list.length - 1; i += 1) {
                var p0 = list[i - 1] || list[i];
                var p1 = list[i];
                var p2 = list[i + 1];
                var p3 = list[i + 2] || p2;
                var c1x = p1.x + (p2.x - p0.x) * 0.16;
                var c1y = p1.y + (p2.y - p0.y) * 0.16;
                var c2x = p2.x - (p3.x - p1.x) * 0.16;
                var c2y = p2.y - (p3.y - p1.y) * 0.16;
                d += ' C ' + c1x.toFixed(2) + ' ' + c1y.toFixed(2) + ' ' + c2x.toFixed(2) + ' ' + c2y.toFixed(2) + ' ' + p2.x.toFixed(2) + ' ' + p2.y.toFixed(2);
            }
            return d;
        }

        function xLabelIndexes(count) {
            var wanted = width < 430 ? 4 : width < 720 ? 6 : 8;
            if (wanted >= count) {
                return points.map(function (_, index) { return index; });
            }
            var indexes = [];
            var step = (count - 1) / (wanted - 1);
            for (var i = 0; i < wanted; i += 1) indexes.push(Math.round(i * step));
            indexes[indexes.length - 1] = count - 1;
            return indexes.filter(function (value, index, list) { return list.indexOf(value) === index; });
        }

        function isLongRange() {
            if (points.length < 2) return false;
            var a = parseDate(points[0].date);
            var b = parseDate(points[points.length - 1].date);
            return (b.year - a.year) * 365 + (b.month - a.month) * 30 + (b.day - a.day) > 120;
        }

        function render() {
            width = Math.round(frame.clientWidth);
            if (!width || points.length === 0) return;
            height = width < 560 ? 240 : 300;
            svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height);
            while (svg.firstChild) svg.removeChild(svg.firstChild);

            var margin = { top: 18, right: 14, bottom: 30, left: width < 560 ? 58 : 66 };
            var plotWidth = width - margin.left - margin.right;
            var plotHeight = height - margin.top - margin.bottom;

            var values = points.map(function (point) { return Number(point.balance_cents); });
            var rawMin = Math.min.apply(null, values);
            var rawMax = Math.max.apply(null, values);
            var span = rawMax - rawMin;
            var pad = span === 0 ? Math.max(Math.abs(rawMax) * 0.2, 100) : span * 0.14;
            var yMin = rawMin - pad;
            var yMax = rawMax + pad;
            if (span === 0) {
                yMin = rawMin - Math.max(Math.abs(rawMax) * 0.1, 100);
                yMax = rawMax + Math.max(Math.abs(rawMax) * 0.1, 100);
            }

            function dayNumber(value) {
                var d = parseDate(value);
                return Math.floor(Date.UTC(d.year, d.month - 1, d.day) / 86400000);
            }
            var firstDay = dayNumber(points[0].date);
            var lastDay = dayNumber(points[points.length - 1].date);
            var daySpan = Math.max(1, lastDay - firstDay);
            function xAt(index) {
                return margin.left + (plotWidth * (dayNumber(points[index].date) - firstDay)) / daySpan;
            }
            function yAt(value) {
                return margin.top + plotHeight - ((value - yMin) / (yMax - yMin)) * plotHeight;
            }

            var defs = el('defs');
            var gradient = el('linearGradient', { id: 'wallet-balance-gradient', x1: '0', y1: '0', x2: '0', y2: '1' });
            gradient.appendChild(el('stop', { offset: '0%', 'class': 'wallet-evolution-area-start' }));
            gradient.appendChild(el('stop', { offset: '62%', 'class': 'wallet-evolution-area-mid' }));
            gradient.appendChild(el('stop', { offset: '100%', 'class': 'wallet-evolution-area-end' }));
            defs.appendChild(gradient);
            svg.appendChild(defs);

            var ticks = 4;
            var zeroInDomain = yMin < 0 && yMax > 0;
            for (var t = 0; t <= ticks; t += 1) {
                var value = yMin + ((yMax - yMin) * t) / ticks;
                var y = yAt(value);
                svg.appendChild(el('line', { x1: margin.left, x2: width - margin.right, y1: y, y2: y, 'class': 'wallet-evolution-grid' }));
                var text = el('text', { x: margin.left - 8, y: y + 3.5, 'class': 'wallet-evolution-axis wallet-evolution-axis-y' });
                text.textContent = formatAxis(value);
                svg.appendChild(text);
            }
            if (zeroInDomain) {
                var zeroY = yAt(0);
                svg.appendChild(el('line', { x1: margin.left, x2: width - margin.right, y1: zeroY, y2: zeroY, 'class': 'wallet-evolution-zero' }));
            }

            positions = points.map(function (point, index) {
                return { x: xAt(index), y: yAt(Number(point.balance_cents)), point: point };
            });
            var linePath = smoothPath(positions);
            var baselineY = Math.min(Math.max(yAt(0), margin.top), height - margin.bottom);
            var areaPath = linePath +
                ' L ' + positions[positions.length - 1].x.toFixed(2) + ' ' + baselineY.toFixed(2) +
                ' L ' + positions[0].x.toFixed(2) + ' ' + baselineY.toFixed(2) + ' Z';

            svg.appendChild(el('path', { d: areaPath, 'class': 'wallet-evolution-area' }));
            svg.appendChild(el('path', { d: linePath, 'class': 'wallet-evolution-line' }));
            positions.forEach(function (position) {
                svg.appendChild(el('circle', { cx: position.x, cy: position.y, r: 2.1, 'class': 'wallet-evolution-dot' }));
            });

            crosshair = el('line', { x1: 0, x2: 0, y1: margin.top, y2: height - margin.bottom, 'class': 'wallet-evolution-crosshair', visibility: 'hidden' });
            activeDot = el('circle', { cx: 0, cy: 0, r: 4.6, 'class': 'wallet-evolution-active-dot', visibility: 'hidden' });
            svg.appendChild(crosshair);
            svg.appendChild(activeDot);

            // Tooltip em SVG: posicionado por atributos (transform/x/y), sem
            // style inline, compatível com style-src-attr 'none'.
            tooltipGroup = el('g', { 'class': 'wallet-evolution-tooltip', 'data-balance-tooltip': '', visibility: 'hidden' });
            tooltipBg = el('rect', { x: 0, y: 0, width: 118, height: 50, rx: 9, ry: 9, 'class': 'wallet-evolution-tooltip-bg' });
            tooltipDateText = el('text', { x: 10, y: 21, 'class': 'wallet-evolution-tooltip-date', 'data-balance-tooltip-date': '' });
            tooltipValueText = el('text', { x: 10, y: 39, 'class': 'wallet-evolution-tooltip-value', 'data-balance-tooltip-value': '' });
            tooltipGroup.appendChild(tooltipBg);
            tooltipGroup.appendChild(tooltipDateText);
            tooltipGroup.appendChild(tooltipValueText);
            svg.appendChild(tooltipGroup);

            var longRange = isLongRange();
            xLabelIndexes(points.length).forEach(function (index) {
                var point = points[index];
                var date = parseDate(point.date);
                var text = el('text', { x: xAt(index), y: height - 8, 'class': 'wallet-evolution-axis wallet-evolution-axis-x' });
                text.textContent = longRange
                    ? MONTHS[date.month - 1] + '/' + String(date.year).slice(-2)
                    : String(date.day).padStart(2, '0') + '/' + String(date.month).padStart(2, '0');
                svg.appendChild(text);
            });

            if (active >= positions.length) active = positions.length - 1;
            if (active >= 0) applyActive(active, false);
        }

        function applyActive(index, announce) {
            var position = positions[index];
            if (!position || !tooltipGroup) return;
            active = index;
            crosshair.setAttribute('x1', position.x);
            crosshair.setAttribute('x2', position.x);
            crosshair.setAttribute('visibility', 'visible');
            activeDot.setAttribute('cx', position.x);
            activeDot.setAttribute('cy', position.y);
            activeDot.setAttribute('visibility', 'visible');

            tooltipDateText.textContent = longDate(position.point.date);
            tooltipValueText.textContent = masked ? 'Valor oculto' : formatCents(position.point.balance_cents);
            tooltipGroup.setAttribute('visibility', 'visible');
            var tipWidth = Math.max(
                118,
                Math.max(tooltipDateText.getComputedTextLength(), tooltipValueText.getComputedTextLength()) + 20
            );
            var tipHeight = 50;
            tooltipBg.setAttribute('width', String(tipWidth));
            tooltipBg.setAttribute('height', String(tipHeight));
            var left = Math.min(Math.max(position.x - tipWidth / 2, 4), width - tipWidth - 4);
            var top = position.y - tipHeight - 10;
            if (top < 4) top = position.y + 14;
            tooltipGroup.setAttribute('transform', 'translate(' + left.toFixed(2) + ' ' + top.toFixed(2) + ')');
            if (announce) {
                live.textContent = longDate(position.point.date) + ': ' + (masked ? 'valor oculto' : formatCents(position.point.balance_cents));
            }
        }

        function clearActive() {
            active = -1;
            if (tooltipGroup) tooltipGroup.setAttribute('visibility', 'hidden');
            if (tooltipDateText) tooltipDateText.textContent = '';
            if (tooltipValueText) tooltipValueText.textContent = '';
            live.textContent = '';
            if (crosshair) crosshair.setAttribute('visibility', 'hidden');
            if (activeDot) activeDot.setAttribute('visibility', 'hidden');
        }

        function nearestIndex(clientX) {
            var rect = frame.getBoundingClientRect();
            var localX = clientX - rect.left;
            var best = 0;
            var bestDistance = Infinity;
            positions.forEach(function (position, index) {
                var distance = Math.abs(position.x - localX);
                if (distance < bestDistance) {
                    bestDistance = distance;
                    best = index;
                }
            });
            return best;
        }

        frame.addEventListener('pointermove', function (event) {
            if (!positions.length) return;
            applyActive(nearestIndex(event.clientX), false);
        });
        frame.addEventListener('pointerdown', function (event) {
            if (!positions.length) return;
            applyActive(nearestIndex(event.clientX), true);
        });
        frame.addEventListener('click', function (event) {
            if (!positions.length) return;
            applyActive(nearestIndex(event.clientX), true);
        });
        frame.addEventListener('pointerleave', function () {
            if (document.activeElement !== frame) clearActive();
        });
        frame.addEventListener('focus', function () {
            if (!positions.length) return;
            applyActive(active >= 0 ? active : positions.length - 1, true);
        });
        frame.addEventListener('blur', clearActive);
        frame.addEventListener('keydown', function (event) {
            if (!positions.length) return;
            var next = active;
            if (event.key === 'ArrowRight') next = Math.min(active + 1, positions.length - 1);
            else if (event.key === 'ArrowLeft') next = Math.max(active - 1, 0);
            else if (event.key === 'Home') next = 0;
            else if (event.key === 'End') next = positions.length - 1;
            else if (event.key === 'Escape') { clearActive(); return; }
            else return;
            event.preventDefault();
            applyActive(next, true);
        });

        function scheduleRender() {
            cancelAnimationFrame(resizeFrame);
            resizeFrame = requestAnimationFrame(render);
        }

        if (typeof ResizeObserver === 'function') {
            observer = new ResizeObserver(scheduleRender);
            observer.observe(frame);
        } else {
            window.addEventListener('resize', scheduleRender);
        }

        return {
            setData: function (nextPoints) {
                points = nextPoints.slice();
                active = -1;
                render();
            },
            setMasked: function (value) {
                if (masked === value) return;
                masked = value;
                render();
                live.textContent = '';
                if (active < 0) {
                    if (tooltipDateText) tooltipDateText.textContent = '';
                    if (tooltipValueText) tooltipValueText.textContent = '';
                }
            },
            destroy: function () {
                if (observer) observer.disconnect();
                cancelAnimationFrame(resizeFrame);
                window.removeEventListener('resize', scheduleRender);
                if (tooltipGroup) tooltipGroup.setAttribute('visibility', 'hidden');
            }
        };
    }

    function initialize(refreshOpen) {
        var root = document.querySelector('[data-balance-series]');
        if (!root) return;
        if (currentController && currentController.root === root) {
            if (refreshOpen && currentController.refresh) currentController.refresh();
            return;
        }
        if (currentController) currentController.destroy();
        currentController = createController(root);
        currentController.init();
    }

    document.addEventListener('htmx:afterSwap', function () {
        cache = Object.create(null);
        initialize(true);
    });
    document.addEventListener('htmx:oobAfterSwap', function () {
        cache = Object.create(null);
        initialize(true);
    });

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initialize, { once: true });
    } else {
        initialize();
    }
}());
