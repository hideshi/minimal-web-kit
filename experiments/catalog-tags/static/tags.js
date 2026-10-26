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
  // どちらかのJSが読めない場合は、Alpineが未登録の部品を処理しない。
  for (const form of document.querySelectorAll("[data-tag-picker]")) form.removeAttribute("x-ignore");
});
