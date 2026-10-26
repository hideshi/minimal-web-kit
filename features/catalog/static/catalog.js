"use strict";
// 公開一覧のGETだけを強化する。管理フォームや入力値を保存しない。
(() => {
  const requests = new WeakMap();
  let currentRequest;
  const byId = id => document.getElementById(id);
  function relevant(event) { return event.detail.target && event.detail.target.id === "catalog-results"; }
  function finish() {
    byId("catalog-browser").classList.remove("catalog-busy");
    byId("catalog-results").removeAttribute("aria-busy");
  }
  document.addEventListener("htmx:beforeRequest", event => {
    if (!relevant(event)) return;
    const elt = event.detail.elt;
    const link = elt.closest("a");
    let path = link ? link.href : "/catalog?" + new URLSearchParams(new FormData(byId("catalog-filter"))).toString();
    const url = new URL(path, location.href);
    if (url.origin !== location.origin || url.pathname !== "/catalog") { event.preventDefault(); return; }
    currentRequest = event.detail.xhr;
    requests.set(event.detail.xhr, { path: url.href, page: !!elt.closest("[data-catalog-page]") });
    byId("catalog-failure").hidden = true;
    byId("catalog-status").textContent = "読み込み中です。";
    byId("catalog-browser").classList.add("catalog-busy");
    byId("catalog-results").setAttribute("aria-busy", "true");
  });
  document.addEventListener("htmx:afterSwap", event => {
    if (!relevant(event) || event.detail.xhr !== currentRequest) return;
    const info = requests.get(event.detail.xhr);
    byId("catalog-status").textContent = byId("catalog-summary").textContent;
    if (info && info.page) byId("catalog-results-heading").focus();
  });
  function showFailure(event) {
    if (!relevant(event) || event.detail.xhr !== currentRequest) return;
    finish();
    const info = requests.get(event.detail.xhr);
    byId("catalog-retry").href = info ? info.path : "/catalog";
    byId("catalog-failure").hidden = false;
    byId("catalog-status").textContent = "読み込めませんでした。";
  }
  document.addEventListener("htmx:afterRequest", event => {
    if (!relevant(event) || event.detail.xhr !== currentRequest) return;
    finish();
    if (event.detail.failed) showFailure(event);
  });
  for (const name of ["htmx:sendError", "htmx:timeout", "htmx:swapError"]) {
    document.addEventListener(name, showFailure);
  }
})();
