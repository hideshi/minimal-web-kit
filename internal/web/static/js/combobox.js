"use strict";
// 画面部品 "combobox" の振る舞い（WAI-ARIAのcombobox＋listbox、複数選択）。外部のライブラリは使わない。
// 候補・入力欄はサーバーが描き、このファイルは属性・textContent・<template>の複製だけで画面を変える。
// 選んだ値はJSなし用のチェックボックスに反映し、JSが動かない場合と同じ形で送信する。
// 使い方と決まりは docs/components.md と docs/ui-guidelines.md を参照。
(() => {
  const instances = [];

  function setup(root) {
    const ui = root.querySelector("[data-combobox-ui]");
    const fallback = root.querySelector("[data-combobox-fallback]");
    const input = ui.querySelector("[role=combobox]");
    const list = ui.querySelector("[role=listbox]");
    const options = Array.from(list.querySelectorAll("[role=option]"));
    const boxes = Array.from(fallback.querySelectorAll("input[type=checkbox]"));
    const chips = ui.querySelector("[data-chips]");
    const chipTemplate = ui.querySelector("[data-chip-template]");
    const count = ui.querySelector("[data-count]");
    const empty = ui.querySelector("[data-empty]");
    const status = ui.querySelector("[data-status]");
    const limit = Number(root.dataset.limit) || 0;
    if (options.length !== boxes.length) return;

    // 選択は候補の番号で持つ。並びは選んだ順。
    let selected = boxes.flatMap((box, index) => (box.checked ? [index] : []));
    let visible = options;
    let active = -1; // 今いる候補（visible の中の位置）。-1 なら指していない
    let open = false;
    let timer = 0;
    let quietFocus = false;

    const labelOf = index => options[index].dataset.label;
    const full = () => limit > 0 && selected.length >= limit;
    const countText = () => `${selected.length}個を選択中` + (limit > 0 ? `（${limit}個まで）` : "");

    function say(text) {
      // 件数の通知が後から上書きしないよう取り消し、同じ文言でも読み上げられるよう一度空にする。
      clearTimeout(timer);
      status.textContent = "";
      requestAnimationFrame(() => { status.textContent = text; });
    }

    // 選択が変わったときだけ、候補の印・チェックボックス・選択済みの一覧を描き直す。
    function renderSelection() {
      options.forEach((option, index) => {
        const on = selected.includes(index);
        option.setAttribute("aria-selected", String(on));
        option.querySelector("[data-mark]").textContent = on ? "✓" : "";
        if (!on && full()) option.setAttribute("aria-disabled", "true");
        else option.removeAttribute("aria-disabled");
        boxes[index].checked = on;
      });
      chips.replaceChildren(...selected.map(index => {
        const item = chipTemplate.content.firstElementChild.cloneNode(true);
        const button = item.querySelector("button");
        button.value = String(index);
        button.setAttribute("aria-label", `${labelOf(index)} の選択を解除する`);
        item.querySelector("[data-chip-label]").textContent = labelOf(index);
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
      visible = options.filter(option => {
        const hit = option.dataset.label.toLocaleLowerCase("ja").includes(query);
        option.hidden = !hit;
        return hit;
      });
    }

    function announceCount() {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (open) say(visible.length ? `${visible.length}件の候補` : "一致する候補はありません");
      }, 400);
    }

    // 開くだけで、今いる候補は決めない（↓↑で初めて指す）。
    function show() {
      if (open || document.activeElement !== input) return;
      open = true;
      active = -1;
      renderList();
      announceCount();
    }

    function close() {
      if (!open) return;
      open = false;
      active = -1;
      renderList();
    }

    function move(step) {
      if (!open) {
        open = true;
        active = -1;
      }
      if (visible.length) {
        if (active < 0) active = step > 0 ? 0 : visible.length - 1;
        else active = (active + step + visible.length) % visible.length;
      }
      renderList();
    }

    function toggle(option) {
      const index = options.indexOf(option);
      if (selected.includes(index)) {
        selected = selected.filter(value => value !== index);
        renderSelection();
        say(`${labelOf(index)} を外しました。${countText()}`);
        return;
      }
      if (full()) {
        say(`${limit}個に達しています。追加するには選択を解除してください。`);
        return;
      }
      selected = [...selected, index];
      renderSelection();
      say(`${labelOf(index)} を追加しました。${countText()}`);
      if (input.value !== "") {
        // 次の候補を続けて入力できるよう検索語を空にし、選んだ候補の位置を保つ。
        input.value = "";
        filter();
        active = visible.indexOf(option);
      }
    }

    function unselect(index) {
      selected = selected.filter(value => value !== index);
      renderSelection();
      say(`${labelOf(index)} を外しました。${countText()}`);
    }

    input.addEventListener("focus", () => {
      // ×で外した後にフォーカスを戻すときは、候補を開かない。
      if (!quietFocus) show();
    });
    input.addEventListener("click", show);
    input.addEventListener("blur", close);
    input.addEventListener("input", () => {
      filter();
      open = true;
      active = -1;
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
        if (option) {
          toggle(option);
          renderList();
        }
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
        unselect(selected[selected.length - 1]);
      }
    });
    // 候補を押しても入力欄からフォーカスを外さない。
    list.addEventListener("mousedown", event => event.preventDefault());
    list.addEventListener("click", event => {
      const option = event.target.closest("[role=option]");
      if (!option) return;
      active = visible.indexOf(option);
      toggle(option);
      renderList();
    });
    chips.addEventListener("click", event => {
      const button = event.target.closest("button");
      if (!button) return;
      unselect(Number(button.value));
      quietFocus = true;
      input.focus();
      quietFocus = false;
    });

    renderSelection();
    fallback.hidden = true;
    ui.hidden = false;
    instances.push({ ui, close });
  }

  document.querySelectorAll("[data-combobox]").forEach(setup);
  // フォーカスの移動に頼らず、部品の外側を押したときにも閉じる。
  document.addEventListener("pointerdown", event => {
    for (const instance of instances) {
      if (!instance.ui.contains(event.target)) instance.close();
    }
  });
})();
