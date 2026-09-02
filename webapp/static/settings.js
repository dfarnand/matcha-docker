/*
 * Settings UI.
 *
 * The whole settings document is loaded once from /api/settings, edited
 * client-side, and written back with a single POST. Feed add/remove/reorder
 * are therefore plain array operations with no per-row endpoints and no
 * index-based races against the server.
 *
 * All feed URLs and remote feed titles are inserted with textContent, never
 * innerHTML: preview titles come from arbitrary third-party feeds.
 */
(function () {
    'use strict';

    var csrfToken = document.querySelector('meta[name="csrf-token"]').content;

    var state = {
        settings: null,
        apiKeySet: false,
        clearApiKey: false,
        dirty: false,
        degraded: false
    };

    /* Scalar inputs whose element id matches the settings key. */
    var SCALARS = {
        schedule: 'text',
        markdown_file_prefix: 'text',
        markdown_file_suffix: 'text',
        opml_file_path: 'text',
        openai_base_url: 'text',
        openai_model: 'text',
        summary_prompt: 'text',
        analyst_prompt: 'text',
        analyst_model: 'text',
        notification_trigger: 'text',
        notification_webhook_url: 'text',
        instapaper: 'bool',
        reading_time: 'bool',
        show_images: 'bool',
        sunrise_sunset: 'bool',
        weather_latitude: 'number',
        weather_longitude: 'number'
    };

    var FEED_KINDS = ['feeds', 'summary_feeds', 'analyst_feeds'];

    function $(id) { return document.getElementById(id); }

    function el(tag, className, text) {
        var n = document.createElement(tag);
        if (className) { n.className = className; }
        if (text !== undefined && text !== null) { n.textContent = text; }
        return n;
    }

    /* ---------- networking ---------- */

    function api(method, url, body) {
        var opts = {
            method: method,
            headers: { 'X-CSRF-Token': csrfToken },
            credentials: 'same-origin'
        };
        if (body !== undefined) {
            opts.headers['Content-Type'] = 'application/json';
            opts.body = JSON.stringify(body);
        }
        return fetch(url, opts).then(function (resp) {
            if (resp.status === 401) {
                window.location.href = '/login?next=' + encodeURIComponent('/settings');
                return Promise.reject(new Error('signed out'));
            }
            var ct = resp.headers.get('Content-Type') || '';
            if (ct.indexOf('application/json') === -1) {
                return resp.text().then(function (t) { return { status: resp.status, text: t }; });
            }
            return resp.json().then(function (data) {
                return { status: resp.status, data: data };
            });
        });
    }

    function toast(message, kind) {
        var host = $('toast-host');
        var node = el('div', 'st-toast ' + (kind === 'err' ? 'st-toast-err' : 'st-toast-ok'), message);
        host.appendChild(node);
        setTimeout(function () {
            if (node.parentNode) { node.parentNode.removeChild(node); }
        }, kind === 'err' ? 9000 : 4500);
    }

    /* ---------- dirty tracking ---------- */

    function markDirty() {
        state.dirty = true;
        $('dirty-state').textContent = 'Unsaved changes';
    }

    function markClean() {
        state.dirty = false;
        $('dirty-state').textContent = '';
    }

    window.addEventListener('beforeunload', function (e) {
        if (state.dirty) {
            e.preventDefault();
            e.returnValue = '';
        }
    });

    /* ---------- feed rows ---------- */

    function renderFeedList(kind) {
        var container = $(kind + '-list');
        var list = state.settings[kind] || [];
        container.innerHTML = '';

        if (list.length === 0) {
            container.appendChild(el('div', 'st-empty', 'No feeds yet.'));
            return;
        }

        list.forEach(function (feed, index) {
            container.appendChild(buildFeedRow(kind, feed, index, list.length));
        });
    }

    function buildFeedRow(kind, feed, index, total) {
        var row = el('div', 'st-feed-row');
        row.dataset.kind = kind;
        row.dataset.index = String(index);
        if (!feed.enabled) { row.classList.add('st-feed-disabled'); }

        var url = el('input', 'st-input st-feed-url');
        url.type = 'text';
        url.spellcheck = false;
        url.value = feed.url || '';
        url.placeholder = 'https://example.com/feed.xml';
        url.addEventListener('input', function () {
            feed.url = url.value;
            markDirty();
        });
        row.appendChild(url);

        /* matcha hardcodes 20 items for analyst feeds, and we do not expose a
           limit for summary feeds, so only the main list gets this box. */
        if (kind === 'feeds') {
            var limit = el('input', 'st-input st-feed-limit');
            limit.type = 'number';
            limit.min = '1';
            limit.max = '1000';
            limit.placeholder = '20';
            limit.title = 'Items to read from this feed';
            limit.value = feed.limit ? String(feed.limit) : '';
            limit.addEventListener('input', function () {
                feed.limit = limit.value === '' ? 0 : parseInt(limit.value, 10) || 0;
                markDirty();
            });
            row.appendChild(limit);
        }

        var toggleLabel = el('label', 'st-checkbox st-checkbox-inline');
        var toggle = document.createElement('input');
        toggle.type = 'checkbox';
        toggle.checked = !!feed.enabled;
        toggle.title = 'Include this feed in the digest';
        toggle.addEventListener('change', function () {
            feed.enabled = toggle.checked;
            row.classList.toggle('st-feed-disabled', !feed.enabled);
            markDirty();
        });
        toggleLabel.appendChild(toggle);
        toggleLabel.appendChild(el('span', null, 'on'));
        row.appendChild(toggleLabel);

        row.appendChild(iconButton('▲', 'Move up', index === 0, function () {
            moveFeed(kind, index, -1);
        }));
        row.appendChild(iconButton('▼', 'Move down', index === total - 1, function () {
            moveFeed(kind, index, 1);
        }));
        row.appendChild(iconButton('👁', 'Preview this feed', false, function () {
            previewFeed(feed.url);
        }));
        row.appendChild(iconButton('✕', 'Remove this feed', false, function () {
            state.settings[kind].splice(index, 1);
            renderFeedList(kind);
            markDirty();
        }));

        return row;
    }

    function iconButton(glyph, title, disabled, onClick) {
        var b = el('button', 'st-btn-icon', glyph);
        b.type = 'button';
        b.title = title;
        b.setAttribute('aria-label', title);
        b.disabled = disabled;
        b.addEventListener('click', onClick);
        return b;
    }

    function moveFeed(kind, index, delta) {
        var list = state.settings[kind];
        var target = index + delta;
        if (target < 0 || target >= list.length) { return; }
        var tmp = list[index];
        list[index] = list[target];
        list[target] = tmp;
        renderFeedList(kind);
        markDirty();
        /* Keep the keyboard on the row that just moved. */
        var rows = $(kind + '-list').querySelectorAll('.st-feed-row');
        if (rows[target]) { rows[target].querySelector('.st-feed-url').focus(); }
    }

    /* ---------- keyword chips ---------- */

    function renderKeywords() {
        var container = $('keywords-list');
        container.innerHTML = '';
        var list = state.settings.google_news_keywords || [];

        if (list.length === 0) {
            container.appendChild(el('div', 'st-empty', 'No keywords yet.'));
            return;
        }

        list.forEach(function (word, index) {
            var chip = el('span', 'st-chip', word);
            chip.appendChild(iconButton('✕', 'Remove ' + word, false, function () {
                state.settings.google_news_keywords.splice(index, 1);
                renderKeywords();
                markDirty();
            }));
            container.appendChild(chip);
        });
    }

    function addKeyword() {
        var input = $('keyword-input');
        var word = input.value.trim();
        if (!word) { return; }
        if (word.indexOf(',') !== -1) {
            toast('Keywords cannot contain commas. Add them one at a time.', 'err');
            return;
        }
        if (!state.settings.google_news_keywords) {
            state.settings.google_news_keywords = [];
        }
        state.settings.google_news_keywords.push(word);
        input.value = '';
        renderKeywords();
        markDirty();
    }

    /* ---------- feed preview ---------- */

    function openPanel() { $('preview-backdrop').hidden = false; }
    function closePanel() { $('preview-backdrop').hidden = true; }

    function previewFeed(url) {
        var body = $('preview-body');
        body.innerHTML = '';
        body.appendChild(el('p', null, 'Fetching…'));
        openPanel();

        api('POST', '/api/feed-preview', { url: url }).then(function (res) {
            var data = res.data || {};
            body.innerHTML = '';

            if (!data.ok) {
                var err = el('p', 'st-error', data.error || 'Could not read that feed.');
                body.appendChild(err);
                return;
            }

            if (data.feed_title) {
                body.appendChild(el('div', 'st-preview-feedtitle', data.feed_title));
            }
            var ol = document.createElement('ol');
            (data.items || []).forEach(function (item) {
                var li = document.createElement('li');
                if (item.link) {
                    var a = el('a', null, item.title);
                    a.href = item.link;
                    a.target = '_blank';
                    a.rel = 'noopener noreferrer';
                    li.appendChild(a);
                } else {
                    li.textContent = item.title;
                }
                ol.appendChild(li);
            });
            body.appendChild(ol);
        }).catch(function () {
            body.innerHTML = '';
            body.appendChild(el('p', 'st-error', 'The preview request failed.'));
        });
    }

    /* ---------- load and save ---------- */

    function populate(data) {
        state.settings = data.settings;
        state.apiKeySet = data.openai_api_key_set;
        state.clearApiKey = false;
        state.degraded = !!data.degraded;

        Object.keys(SCALARS).forEach(function (key) {
            var node = $(key);
            if (!node) { return; }
            var value = state.settings[key];
            if (SCALARS[key] === 'bool') {
                node.checked = !!value;
            } else {
                node.value = (value === undefined || value === null) ? '' : value;
            }
        });

        $('api-key-state').textContent = state.apiKeySet
            ? 'A key is stored. Leave this blank to keep it.'
            : 'No key stored.';
        $('clear-api-key').hidden = !state.apiKeySet;

        FEED_KINDS.forEach(renderFeedList);
        renderKeywords();
        renderMigrationWarnings();

        if (state.degraded) {
            $('save-all').disabled = true;
        }
        markClean();
    }

    function renderMigrationWarnings() {
        var warnings = state.settings.migration_warnings || [];
        var banner = $('migration-banner');
        if (warnings.length === 0 || state.settings.migration_warnings_ack) {
            banner.hidden = true;
            return;
        }
        var list = $('migration-list');
        list.innerHTML = '';
        warnings.forEach(function (w) { list.appendChild(el('li', null, w)); });
        banner.hidden = false;
    }

    function collect() {
        var out = JSON.parse(JSON.stringify(state.settings));

        Object.keys(SCALARS).forEach(function (key) {
            var node = $(key);
            if (!node) { return; }
            if (SCALARS[key] === 'bool') {
                out[key] = node.checked;
            } else if (SCALARS[key] === 'number') {
                out[key] = node.value === '' ? 0 : parseFloat(node.value) || 0;
            } else {
                out[key] = node.value;
            }
        });

        /* The browser never receives the stored key: blank means "unchanged",
           and the sentinel means "wipe it". */
        if (state.clearApiKey) {
            out.openai_api_key = '__MATCHA_CLEAR__';
        } else {
            out.openai_api_key = $('openai_api_key').value;
        }
        return out;
    }

    function clearFieldErrors() {
        document.querySelectorAll('.st-input-invalid').forEach(function (n) {
            n.classList.remove('st-input-invalid');
        });
        document.querySelectorAll('.st-feed-error').forEach(function (n) {
            n.parentNode.removeChild(n);
        });
    }

    /* Server-side field names look like "feeds[2]" or "schedule". */
    function showFieldError(field, message) {
        var match = /^(\w+)\[(\d+)\]$/.exec(field);
        if (match) {
            var rows = $(match[1] + '-list');
            if (rows) {
                var row = rows.querySelectorAll('.st-feed-row')[parseInt(match[2], 10)];
                if (row) {
                    row.querySelector('.st-feed-url').classList.add('st-input-invalid');
                    row.appendChild(el('div', 'st-feed-error', message));
                    return true;
                }
            }
        }
        var node = $(field);
        if (node) {
            node.classList.add('st-input-invalid');
            return true;
        }
        return false;
    }

    function save() {
        if (state.degraded) { return; }
        clearFieldErrors();

        var btn = $('save-all');
        btn.disabled = true;

        api('POST', '/api/settings', collect()).then(function (res) {
            btn.disabled = false;
            var data = res.data || {};

            if (res.status === 200 && data.ok) {
                markClean();
                state.clearApiKey = false;
                $('openai_api_key').value = '';
                toast('Settings saved.', 'ok');
                (data.warnings || []).forEach(function (w) { toast(w, 'ok'); });
                return load();
            }

            if (data.errors && data.errors.length) {
                var unplaced = [];
                data.errors.forEach(function (e) {
                    if (!showFieldError(e.field, e.message)) {
                        unplaced.push(e.field + ': ' + e.message);
                    }
                });
                toast('Could not save: ' + (unplaced.length
                    ? unplaced.join('; ')
                    : data.errors.map(function (e) { return e.message; }).join('; ')), 'err');
                return;
            }
            toast(data.error || 'Could not save settings.', 'err');
        }).catch(function () {
            btn.disabled = false;
            toast('Could not reach the server.', 'err');
        });
    }

    function load() {
        return api('GET', '/api/settings').then(function (res) {
            if (res.data) { populate(res.data); }
        });
    }

    /* ---------- run status ---------- */

    var pollTimer = null;

    function formatTime(iso) {
        if (!iso) { return '—'; }
        var d = new Date(iso);
        if (isNaN(d.getTime())) { return '—'; }
        return d.toLocaleString();
    }

    function renderStatus(data) {
        var box = $('run-status');
        box.innerHTML = '';

        if (data.running) {
            box.appendChild(el('div', null, 'Running now, started ' + formatTime(data.started_at) + '…'));
        } else if (data.last) {
            var last = data.last;
            var line = el('div', null, '');
            line.appendChild(el('span', last.ok ? 'st-ok' : 'st-fail', last.ok ? '● ' : '✕ '));
            line.appendChild(document.createTextNode(
                'Last run ' + formatTime(last.finished_at) +
                ' (' + last.source + ') — ' +
                (last.ok ? 'ok' : 'exit ' + last.exit_code) +
                ', ' + last.duration_sec.toFixed(1) + 's'
            ));
            box.appendChild(line);
            if (last.error) {
                box.appendChild(el('div', 'st-fail', last.error));
            }
        } else {
            box.appendChild(el('div', 'st-muted', 'No runs recorded yet.'));
        }

        if (data.next_run) {
            box.appendChild(el('div', 'st-muted',
                'Next run ' + formatTime(data.next_run) + ' (' + (data.timezone || 'local time') + ')'));
        }

        var hasOutput = data.last && data.last.output_tail;
        $('toggle-output').hidden = !hasOutput;
        if (hasOutput) {
            $('run-output').textContent = data.last.output_tail;
        }
        $('run-now').disabled = !!data.running;
    }

    function pollStatus(keepPolling) {
        return api('GET', '/api/run/status').then(function (res) {
            var data = res.data || {};
            renderStatus(data);
            if (keepPolling && data.running) {
                pollTimer = setTimeout(function () { pollStatus(true); }, 2000);
            } else if (keepPolling) {
                toast(data.last && data.last.ok
                    ? 'Run finished successfully.'
                    : 'Run finished with errors.', data.last && data.last.ok ? 'ok' : 'err');
            }
        });
    }

    function runNow() {
        $('run-now').disabled = true;
        api('POST', '/api/run').then(function (res) {
            if (res.status === 409) {
                toast('A run is already in progress.', 'err');
            } else {
                toast('Run started.', 'ok');
            }
            if (pollTimer) { clearTimeout(pollTimer); }
            pollTimer = setTimeout(function () { pollStatus(true); }, 800);
        }).catch(function () {
            $('run-now').disabled = false;
            toast('Could not start the run.', 'err');
        });
    }

    /* ---------- password ---------- */

    function changePassword() {
        var body = {
            current_password: $('current_password').value,
            new_password: $('new_password').value,
            confirm_password: $('confirm_password').value
        };
        api('POST', '/api/password', body).then(function (res) {
            var data = res.data || {};
            if (res.status === 200 && data.ok) {
                /* Changing the password rotates the session, and the CSRF
                   token is derived from it, so adopt the new one. */
                if (data.csrf_token) { csrfToken = data.csrf_token; }
                $('current_password').value = '';
                $('new_password').value = '';
                $('confirm_password').value = '';
                toast('Password changed.', 'ok');
                return;
            }
            toast(data.error || 'Could not change the password.', 'err');
        }).catch(function () {
            toast('Could not reach the server.', 'err');
        });
    }

    /* ---------- config preview ---------- */

    function refreshConfigPreview() {
        var reveal = $('reveal-secrets').checked ? '?reveal=1' : '';
        fetch('/api/config-preview' + reveal, { credentials: 'same-origin' })
            .then(function (r) { return r.text(); })
            .then(function (text) { $('config-preview').textContent = text; })
            .catch(function () { $('config-preview').textContent = 'Could not load the generated config.'; });
    }

    /* ---------- wiring ---------- */

    function init() {
        Object.keys(SCALARS).forEach(function (key) {
            var node = $(key);
            if (node) { node.addEventListener('input', markDirty); }
            if (node && SCALARS[key] === 'bool') { node.addEventListener('change', markDirty); }
        });

        $('schedule').addEventListener('input', function () {
            $('schedule-hint').textContent =
                'Five fields: minute hour day-of-month month day-of-week. Saved schedules are validated by the server.';
        });

        $('schedule-presets').addEventListener('click', function (e) {
            var cron = e.target.getAttribute('data-cron');
            if (!cron) { return; }
            $('schedule').value = cron;
            markDirty();
        });

        document.querySelectorAll('[data-add-feed]').forEach(function (btn) {
            btn.addEventListener('click', function () {
                var kind = btn.getAttribute('data-add-feed');
                if (!state.settings[kind]) { state.settings[kind] = []; }
                state.settings[kind].push({ url: '', limit: 0, enabled: true });
                renderFeedList(kind);
                markDirty();
                var rows = $(kind + '-list').querySelectorAll('.st-feed-row');
                var last = rows[rows.length - 1];
                if (last) { last.querySelector('.st-feed-url').focus(); }
            });
        });

        $('add-keyword').addEventListener('click', addKeyword);
        $('keyword-input').addEventListener('keydown', function (e) {
            if (e.key === 'Enter') { e.preventDefault(); addKeyword(); }
        });

        $('clear-api-key').addEventListener('click', function () {
            state.clearApiKey = true;
            $('openai_api_key').value = '';
            $('api-key-state').textContent = 'The stored key will be removed when you save.';
            markDirty();
        });
        $('openai_api_key').addEventListener('input', function () {
            state.clearApiKey = false;
            markDirty();
        });

        $('save-all').addEventListener('click', save);
        $('run-now').addEventListener('click', runNow);
        $('change-password').addEventListener('click', changePassword);
        $('refresh-config').addEventListener('click', refreshConfigPreview);
        $('reveal-secrets').addEventListener('change', refreshConfigPreview);

        $('toggle-output').addEventListener('click', function () {
            var pre = $('run-output');
            pre.hidden = !pre.hidden;
            $('toggle-output').textContent = pre.hidden ? 'Show last output' : 'Hide last output';
        });

        $('preview-close').addEventListener('click', closePanel);
        $('preview-backdrop').addEventListener('click', function (e) {
            if (e.target === $('preview-backdrop')) { closePanel(); }
        });
        document.addEventListener('keydown', function (e) {
            if (e.key === 'Escape') { closePanel(); }
        });

        var dismiss = $('dismiss-migration');
        if (dismiss) {
            dismiss.addEventListener('click', function () {
                state.settings.migration_warnings_ack = true;
                $('migration-banner').hidden = true;
                markDirty();
                save();
            });
        }

        var discard = $('discard-corrupt');
        if (discard) {
            discard.addEventListener('click', function () {
                api('POST', '/api/settings/discard', {}).then(function () {
                    window.location.reload();
                });
            });
        }

        load().then(function () { return pollStatus(false); });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
