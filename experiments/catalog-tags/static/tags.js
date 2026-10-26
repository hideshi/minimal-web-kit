"use strict";
// Alpineより先に読み込み、CSPビルドへ名前付きの部品を登録する。
document.addEventListener("alpine:init", () => {
  Alpine.data("tagPicker", () => ({
    ready: false,
    query: "",
    selected: [],
    tags: [],
    init() {
      this.tags = Array.from(this.$el.querySelectorAll("input[name=tag]")).map(input => ({
        id: input.value, name: input.closest("label").dataset.name
      }));
      this.selected = Array.from(this.$el.querySelectorAll("input[name=tag]:checked")).map(input => input.value);
      this.ready = true;
    },
    get notReady() { return !this.ready; },
    get chosen() { return this.tags.filter(tag => this.selected.includes(tag.id)); },
    get summary() {
      const count = this.tags.filter(tag => !this.notMatches(tag.name)).length;
      return `${count}件の候補、${this.selected.length}個を選択中。` + (this.selected.length >= 20 ? "上限です。追加するには選択を解除してください。" : "");
    },
    notMatches(name) { return !name.toLocaleLowerCase("ja").includes(this.query.trim().toLocaleLowerCase("ja")); },
    isSelected(id) { return this.selected.includes(id); },
    blocked(id) { return this.selected.length >= 20 && !this.isSelected(id); },
    toggle(event) {
      const input = event.target;
      if (input.checked && !this.blocked(input.value)) this.selected = [...this.selected, input.value];
      else this.selected = this.selected.filter(id => id !== input.value);
    },
    removeLabel(name) { return `${name} の選択を解除する`; },
    remove(event) {
      const id = event.currentTarget.value;
      const form = event.currentTarget.closest("form");
      const search = form.querySelector("#tag-search");
      this.selected = this.selected.filter(value => value !== id);
      this.$nextTick(() => {
        const input = form.querySelector(`#tag-${id}`);
        if (input && input.offsetParent !== null) input.focus();
        else search.focus();
      });
    }
  }));
  // 入力欄に候補の一覧を付けるコンボボックス（WAI-ARIAのcombobox＋listbox）。
  // 選択値は隠したチェックボックスに反映し、JSなしの版と同じ形で送信する。
  Alpine.data("tagCombo", () => ({
    ready: false,
    open: false,
    query: "",
    active: -1,
    selected: [],
    tags: [],
    message: "",
    timer: 0,
    quietFocus: false,
    init() {
      this.tags = Array.from(this.$el.querySelectorAll("input[name=tag]")).map(input => ({
        id: input.value, name: input.closest("label").dataset.name
      }));
      this.selected = Array.from(this.$el.querySelectorAll("input[name=tag]:checked")).map(input => input.value);
      this.ready = true;
    },
    get notReady() { return !this.ready; },
    get chosen() { return this.selected.map(id => this.tags.find(tag => tag.id === id)).filter(Boolean); },
    get matches() {
      const q = this.query.trim().toLocaleLowerCase("ja");
      return this.tags.filter(tag => tag.name.toLocaleLowerCase("ja").includes(q));
    },
    get listHidden() { return !this.open || this.matches.length === 0; },
    get hasMatch() { return !this.open || this.matches.length > 0; },
    get activeId() {
      const tag = this.open ? this.matches[this.active] : null;
      return tag ? this.optionId(tag.id) : null;
    },
    get countText() { return `${this.selected.length}個を選択中（20個まで）`; },
    optionId(id) { return `combo-option-${id}`; },
    isSelected(id) { return this.selected.includes(id); },
    blocked(id) { return this.selected.length >= 20 && !this.isSelected(id); },
    optionClass(id) { return this.matches[this.active]?.id === id ? "is-active" : ""; },
    mark(id) { return this.isSelected(id) ? "✓" : ""; },
    removeLabel(name) { return `${name} の選択を解除する`; },
    say(text) {
      // 候補数の通知が後から追加・解除の通知を上書きしないよう、待っている通知を取り消す。
      clearTimeout(this.timer);
      // 同じ文言が続いても読み上げられるよう、一度空にしてから入れる。
      this.message = "";
      this.$nextTick(() => { this.message = text; });
    },
    show() {
      if (this.open || document.activeElement !== document.getElementById("combo-input")) return;
      this.open = true;
      this.active = this.matches.length ? 0 : -1;
      this.announceCount();
    },
    close() { this.open = false; this.active = -1; },
    onFocus() {
      // ×で外した後にフォーカスを戻すときは、候補を開かない。
      if (this.quietFocus) this.quietFocus = false;
      else this.show();
    },
    // フォーカスの移動に頼らず、外側を押したときにも閉じる。
    closeOutside() { if (this.open) this.close(); },
    announceCount() {
      clearTimeout(this.timer);
      this.timer = setTimeout(() => {
        if (this.open) this.say(this.matches.length ? `${this.matches.length}件の候補` : "一致するタグはありません");
      }, 400);
    },
    onInput() {
      this.open = true;
      this.active = this.matches.length ? 0 : -1;
      this.announceCount();
    },
    move(step) {
      const count = this.matches.length;
      if (!count) return;
      if (!this.open) {
        this.show();
        this.active = step > 0 ? 0 : count - 1;
      } else {
        this.active = (this.active + step + count) % count;
      }
      this.scrollToActive();
    },
    scrollToActive() {
      this.$nextTick(() => {
        const option = this.activeId && document.getElementById(this.activeId);
        if (option) option.scrollIntoView({ block: "nearest" });
      });
    },
    choose(tag) {
      if (this.isSelected(tag.id)) {
        this.selected = this.selected.filter(id => id !== tag.id);
        this.say(`${tag.name} を外しました。${this.countText}`);
        return;
      }
      if (this.blocked(tag.id)) {
        this.say("20個に達しています。追加するには選択を解除してください。");
        return;
      }
      this.selected = [...this.selected, tag.id];
      this.say(`${tag.name} を追加しました。${this.countText}`);
      if (this.query !== "") {
        // 次のタグを続けて入力できるよう検索語を空にし、選んだ候補の位置を保つ。
        this.query = "";
        this.active = this.matches.findIndex(item => item.id === tag.id);
        this.scrollToActive();
      }
    },
    onKey(event) {
      // 日本語入力の変換中のEnter・矢印は、変換の操作として扱う。
      if (event.isComposing || event.keyCode === 229) return;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        this.move(event.key === "ArrowDown" ? 1 : -1);
      } else if (event.key === "Enter") {
        // 入力途中のEnterでフォームを送信しない。送信はボタンで行う。
        event.preventDefault();
        const tag = this.open ? this.matches[this.active] : null;
        if (tag) this.choose(tag);
        else this.show();
      } else if (event.key === "Escape") {
        if (this.open) {
          event.preventDefault();
          this.close();
        } else if (this.query !== "") {
          event.preventDefault();
          this.query = "";
        }
      } else if (event.key === "Backspace" && this.query === "" && this.selected.length) {
        event.preventDefault();
        const last = this.chosen[this.chosen.length - 1];
        this.selected = this.selected.slice(0, -1);
        this.say(`${last.name} を外しました。${this.countText}`);
      }
    },
    pick(event) {
      const tag = this.tags.find(item => item.id === event.currentTarget.dataset.id);
      if (!tag) return;
      this.active = this.matches.indexOf(tag);
      this.choose(tag);
    },
    remove(event) {
      const tag = this.tags.find(item => item.id === event.currentTarget.value);
      this.selected = this.selected.filter(id => id !== tag.id);
      this.say(`${tag.name} を外しました。${this.countText}`);
      // x-forの中からは$refsを参照できないため、IDで入力欄を探す。
      this.$nextTick(() => {
        this.quietFocus = true;
        document.getElementById("combo-input").focus();
        this.quietFocus = false;
      });
    }
  }));
  // どちらかのJSが読めない場合は、Alpineが未登録の部品を処理しない。
  for (const form of document.querySelectorAll("[data-tag-picker]")) form.removeAttribute("x-ignore");
});
