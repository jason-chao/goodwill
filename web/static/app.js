(function () {
  'use strict';

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  // Country codes are stored; names come from the browser, in its language.
  function nameCountries(root) {
    if (!window.Intl || !Intl.DisplayNames) return;
    var names;
    try {
      names = new Intl.DisplayNames([navigator.language || 'en'], { type: 'region' });
    } catch (e) {
      return;
    }
    root.querySelectorAll('[data-country]').forEach(function (el) {
      try {
        var name = names.of(el.dataset.country);
        if (name) el.textContent = name;
      } catch (e) { /* unknown code: leave it as it is */ }
    });
  }

  function initChart() {
    var el = document.getElementById('chart');
    var src = document.getElementById('chart-data');
    if (!el || !src || !window.uPlot) return;

    var d = JSON.parse(src.textContent);
    var zone = d.timezone;
    var dateOpts = d.hourly
      ? { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }
      : { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' };
    var dateFmt, numFmt = new Intl.NumberFormat();
    try {
      dateFmt = new Intl.DateTimeFormat(undefined, Object.assign({ timeZone: zone }, dateOpts));
    } catch (e) {
      zone = 'UTC';
      dateFmt = new Intl.DateTimeFormat(undefined, Object.assign({ timeZone: zone }, dateOpts));
    }

    var tickFmt = new Intl.DateTimeFormat(undefined, Object.assign({ timeZone: zone },
      d.hourly ? { hour: '2-digit', minute: '2-digit' } : { day: 'numeric', month: 'short' }));

    var names = ['Visitors', 'Page views'];
    var keys = ['k1', 'k2'];
    var tip = document.createElement('div');
    tip.className = 'tip';
    tip.hidden = true;

    // One readout for every series at the hovered position. Values lead.
    function showTip(u) {
      var i = u.cursor.idx;
      if (i == null || u.cursor.left < 0) {
        tip.hidden = true;
        return;
      }
      tip.textContent = '';
      var date = document.createElement('div');
      date.className = 'tip-date';
      date.textContent = dateFmt.format(new Date(d.x[i] * 1000));
      tip.appendChild(date);
      [d.visitors, d.views].forEach(function (vals, s) {
        var row = document.createElement('div');
        row.className = 'tip-row';
        var key = document.createElement('i');
        key.className = 'key ' + keys[s];
        var value = document.createElement('strong');
        value.textContent = numFmt.format(vals[i]);
        var label = document.createElement('span');
        label.textContent = names[s];
        row.appendChild(key);
        row.appendChild(value);
        row.appendChild(label);
        tip.appendChild(row);
      });
      tip.hidden = false;
      var x = u.over.offsetLeft + u.cursor.left;
      var left = x + 14;
      if (left + tip.offsetWidth > el.clientWidth) left = x - tip.offsetWidth - 14;
      tip.style.left = Math.max(0, left) + 'px';
      tip.style.top = (u.over.offsetTop + 8) + 'px';
    }

    var incrs = [];
    for (var p = 1; p <= 1e9; p *= 10) incrs.push(p, 2 * p, 5 * p);

    var plot;
    function build() {
      if (plot) plot.destroy();
      var muted = cssVar('--muted'), grid = cssVar('--grid'), surface = cssVar('--surface');
      var colors = [cssVar('--series-1'), cssVar('--series-2')];
      var font = '12px system-ui, -apple-system, "Segoe UI", sans-serif';
      plot = new uPlot({
        width: el.clientWidth,
        height: 280,
        tzDate: function (ts) { return uPlot.tzDate(new Date(ts * 1000), zone); },
        legend: { show: false },
        cursor: {
          y: false,
          points: {
            size: 10,
            width: 2,
            stroke: function () { return surface; },
            fill: function (u, s) { return colors[s - 1]; }
          }
        },
        scales: {
          y: { range: function (u, min, max) { return [0, Math.max(max, 4) * 1.08]; } }
        },
        axes: [
          {
            stroke: muted, font: font, grid: { show: false }, ticks: { show: false },
            values: function (u, splits) { return splits.map(function (ts) { return tickFmt.format(new Date(ts * 1000)); }); }
          },
          {
            stroke: muted, font: font, size: 56, incrs: incrs,
            grid: { stroke: grid, width: 1 }, ticks: { show: false },
            values: function (u, vals) { return vals.map(function (v) { return numFmt.format(v); }); }
          }
        ],
        series: [
          {},
          { label: names[0], stroke: colors[0], width: 2, points: { show: false } },
          { label: names[1], stroke: colors[1], width: 2, points: { show: false } }
        ],
        hooks: { setCursor: [showTip] }
      }, [d.x, d.visitors, d.views], el);
      el.appendChild(tip);
    }
    build();

    if (window.ResizeObserver) {
      new ResizeObserver(function () {
        if (plot && el.clientWidth > 0 && el.clientWidth !== plot.width) {
          plot.setSize({ width: el.clientWidth, height: 280 });
        }
      }).observe(el);
    }
    if (window.matchMedia) {
      window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', build);
    }
    el.addEventListener('mouseleave', function () { tip.hidden = true; });
  }

  document.addEventListener('DOMContentLoaded', function () {
    nameCountries(document);
    initChart();
    document.body.addEventListener('htmx:afterSwap', function (e) { nameCountries(e.target); });
  });
})();
