"use strict";
// 素のJSのタグ選択コンボボックス（WAI-ARIAのcombobox＋listbox）。
// 候補・入力欄はサーバーが描き、このファイルは振る舞いだけを足す。選択値は隠したチェックボックスに反映し、
// JSが動かない場合と同じ形で送信する。HTMLを文字列から作らず、属性・textContent・<template>の複製だけを使う。
(() => {
  const LIMIT = 20;

  function setup(root) {
    if (root.dataset.comboboxReady) return;
    const ui = root.querySelector("[data-combobox-ui]");
    const fallback = root.querySelector("#tag-list");
    const input = ui.querySelector("[role=combobox]");
    const list = ui.querySelector("[role=listbox]");
    const options = Array.from(list.querySelectorAll("[role=option]"));
    const boxes = new Map(Array.from(fallback.querySelectorAll("input[name=tag]")).map(box => [box.value, box]));
    const chips = ui.querySelector("[data-chips]");
    const chipTemplate = ui.querySelector("[data-chip-template]");
    const count = ui.querySelector("[data-count]");
    const empty = ui.querySelector("[data-empty]");
    const status = ui.querySelector("[data-status]");

    let selected = options.map(option => option.dataset.id).filter(id => boxes.get(id)?.checked);
    let visible = options;
    let active = -1;
    let open = false;
    let timer = 0;

    const nameOf = id => options.find(option => option.dataset.id === id).dataset.name;
    const countText = () => `${selected.length}個を選択中（${LIMIT}個まで）`;

    function say(text) {
      // 候補数の通知が後から上書きしないよう取り消し、同じ文言でも読み上げられるよう一度空にする。
      clearTimeout(timer);
      status.textContent = "";
      requestAnimationFrame(() => { status.textContent = text; });
    }

    // 選択が変わったときだけ、候補の印・チェックボックス・選択済みの一覧を描き直す。
    function renderSelection() {
      for (const option of options) {
        const on = selected.includes(option.dataset.id);
        option.setAttribute("aria-selected", String(on));
        option.querySelector("[data-mark]").textContent = on ? "✓" : "";
        if (!on && selected.length >= LIMIT) option.setAttribute("aria-disabled", "true");
        else option.removeAttribute("aria-disabled");
        boxes.get(option.dataset.id).checked = on;
      }
      chips.replaceChildren(...selected.map(id => {
        const item = chipTemplate.content.firstElementChild.cloneNode(true);
        const button = item.querySelector("button");
        button.value = id;
        button.setAttribute("aria-label", `${nameOf(id)} の選択を解除する`);
        item.querySelector("[data-chip-name]").textContent = nameOf(id);
        return item;
      }));
      count.textContent = countText();
    }

    // 開閉と、今いる候補の表示を揃える。
    function renderList() {
      list.hidden = !open || visible.length === 0;
      empty.hidden = !open || visible.length > 0;
      input.setAttribute("aria-expanded", String(open));
      const current = open ? visible[active] : null;
      for (const option of options) option.classList.toggle("is-active", option === current);
      if (current) {
        input.setAttribute("aria-activedescendant", current.id);
        current.scrollIntoView({ block: "nearest" });
      } else {
        input.removeAttribute("aria-activedescendant");
      }
    }

    function filter() {
      const query = input.value.trim().toLocaleLowerCase("ja");
      visible = [];
      for (const option of options) {
        const hit = option.dataset.name.toLocaleLowerCase("ja").includes(query);
        option.hidden = !hit;
        if (hit) visible.push(option);
      }
    }

    function announceCount() {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (open) say(visible.length ? `${visible.length}件の候補` : "一致するタグはありません");
      }, 400);
    }

    function show() {
      if (open) return;
      open = true;
      active = visible.length ? 0 : -1;
      renderList();
      announceCount();
    }

    function close() {
      open = false;
      active = -1;
      renderList();
    }

    function move(step) {
      if (!visible.length) return;
      if (!open) {
        show();
        active = step > 0 ? 0 : visible.length - 1;
      } else {
        active = (active + step + visible.length) % visible.length;
      }
      renderList();
    }

    function choose(option) {
      const id = option.dataset.id;
      if (selected.includes(id)) {
        selected = selected.filter(value => value !== id);
        renderSelection();
        say(`${nameOf(id)} を外しました。${countText()}`);
        return;
      }
      if (selected.length >= LIMIT) {
        say(`${LIMIT}個に達しています。追加するには選択を解除してください。`);
        return;
      }
      selected = [...selected, id];
      renderSelection();
      say(`${nameOf(id)} を追加しました。${countText()}`);
      if (input.value !== "") {
        // 次のタグを続けて入力できるよう検索語を空にし、選んだ候補の位置を保つ。
        input.value = "";
        filter();
        active = visible.indexOf(option);
        renderList();
      }
    }

    function removeLast() {
      const id = selected[selected.length - 1];
      selected = selected.slice(0, -1);
      renderSelection();
      say(`${nameOf(id)} を外しました。${countText()}`);
    }

    input.addEventListener("focus", show);
    input.addEventListener("click", show);
    input.addEventListener("blur", close);
    input.addEventListener("input", () => {
      filter();
      open = true;
      active = visible.length ? 0 : -1;
      renderList();
      announceCount();
    });
    input.addEventListener("keydown", event => {
      // 日本語入力の変換中のEnter・矢印は、変換の操作として扱う。
      if (event.isComposing || event.keyCode === 229) return;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        move(event.key === "ArrowDown" ? 1 : -1);
      } else if (event.key === "Enter") {
        // 入力途中のEnterでフォームを送信しない。送信はボタンで行う。
        event.preventDefault();
        const option = open ? visible[active] : null;
        if (option) choose(option);
        else show();
      } else if (event.key === "Escape") {
        if (open) {
          event.preventDefault();
          close();
        } else if (input.value !== "") {
          event.preventDefault();
          input.value = "";
          filter();
        }
      } else if (event.key === "Backspace" && input.value === "" && selected.length) {
        event.preventDefault();
        removeLast();
      }
    });
    // 候補を押しても入力欄からフォーカスを外さない。
    list.addEventListener("mousedown", event => event.preventDefault());
    list.addEventListener("click", event => {
      const option = event.target.closest("[role=option]");
      if (!option) return;
      active = visible.indexOf(option);
      choose(option);
      renderList();
    });
    chips.addEventListener("click", event => {
      const button = event.target.closest("button");
      if (!button) return;
      selected = selected.filter(id => id !== button.value);
      renderSelection();
      say(`${nameOf(button.value)} を外しました。${countText()}`);
      input.focus();
    });

    renderSelection();
    fallback.hidden = true;
    ui.hidden = false;
    root.dataset.comboboxReady = "true";
  }

  const setupAll = scope => scope.querySelectorAll("[data-combobox]").forEach(setup);
  setupAll(document);
  // htmxで差し替えた部分にも付ける（htmxが無い画面では発生しない）。
  document.addEventListener("htmx:load", event => setupAll(event.detail.elt.parentElement || document));
})();
