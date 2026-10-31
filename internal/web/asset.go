package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"text/template/parse"
)

// assetVersionLen は、版に使う SHA-256 の16進の桁数です。
const assetVersionLen = 12

// staticHeadName は、CSS とアイコンの読み込みをまとめた共通部品の名前です。利用側では定義し直せません。
const staticHeadName = "static-head"

// assetVersions は、/static/ の各ファイル（static/ からの相対パス）と、その中身の SHA-256 の先頭12桁です。
// ファイルはバイナリに埋め込まれているため、起動時に1回だけ計算します。ファイルごとの版なので、CSS を変えてもアイコンの URL は変わりません。
var assetVersions = func() map[string]string {
	versions := map[string]string{}
	err := fs.WalkDir(contentFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(contentFS, path)
		if err != nil {
			return err
		}
		versions[strings.TrimPrefix(path, "static/")] = ContentVersion(b)
		return nil
	})
	if err != nil {
		panic(err)
	}
	return versions
}()

// staticHeadSource は、共通部品の static-head の中身です。利用側のテンプレートが同じ名前で差し替えていないかの照合に使います。
var staticHeadSource = func() string {
	t := template.Must(template.New("components").Funcs(componentFuncs).ParseFS(contentFS, "views/components.html"))
	head := t.Lookup(staticHeadName)
	if head == nil {
		panic("components.html must define " + staticHeadName)
	}
	return head.Tree.Root.String()
}()

// AssetURL は、/static/ のファイルの版付きの URL（例: /static/css/style.css?v=0123456789ab）を返します。
// name は static/ からの相対パスです。存在しないファイルはエラーにします。テンプレートでは {{asset "css/style.css"}} と書きます。
func AssetURL(name string) (string, error) {
	v, ok := assetVersions[name]
	if !ok {
		return "", fmt.Errorf("asset %q is not in /static/", name)
	}
	return "/static/" + name + "?v=" + v, nil
}

// ContentVersion は、ファイルの中身から版の文字列を作ります。機能が自分で配るファイルの版付きのURLに使います。
func ContentVersion(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:assetVersionLen]
}

// VersionedCache は、版付きのファイルの応答のキャッシュを決めます。?v= が今の版と一致すれば中身が変わらないので長くし、
// それ以外（版なし・古い版）は短くします。古い版のURLに今の中身を長くキャッシュさせないためです。
func VersionedCache(w http.ResponseWriter, r *http.Request, version string) {
	if v := r.URL.Query().Get("v"); v != "" && v == version {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	PublicCache(w, staticCacheSeconds)
}

// staticCache は /static/ の応答のキャッシュを決めます。?v= が今の版と一致すれば中身は変わらないため1年、
// それ以外（版なし・古い版）は短くします。古い版の URL に今の中身を長くキャッシュさせないためです。
func staticCache(w http.ResponseWriter, r *http.Request) {
	VersionedCache(w, r, assetVersions[strings.TrimPrefix(r.URL.Path, "/static/")])
}

// checkTemplates は、解析したテンプレートの全定義を、実行しない分岐も含めてたどり、次を確かめます。
//   - asset の引数が1つの文字列で、/static/ に実在するファイルを指すこと（描画時でなく起動時に誤りに気づくため）
//   - static-head が共通部品のままであること（同じ名前で定義し直すと、CSS を1か所で読み込む保証が崩れるため）
func checkTemplates(t *template.Template) error {
	if head := t.Lookup(staticHeadName); head == nil || head.Tree == nil || head.Tree.Root.String() != staticHeadSource {
		return fmt.Errorf("template %q is reserved for the shared component and must not be redefined", staticHeadName)
	}
	for _, tmpl := range t.Templates() {
		if tmpl.Tree == nil || tmpl.Tree.Root == nil {
			continue
		}
		if err := checkAssetNode(tmpl.Tree.Root); err != nil {
			return fmt.Errorf("template %q: %w", tmpl.Name(), err)
		}
	}
	return nil
}

func checkAssetNode(node parse.Node) error {
	switch n := node.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Nodes {
			if err := checkAssetNode(c); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return checkAssetNode(n.Pipe)
	case *parse.IfNode:
		return checkBranch(&n.BranchNode)
	case *parse.RangeNode:
		return checkBranch(&n.BranchNode)
	case *parse.WithNode:
		return checkBranch(&n.BranchNode)
	case *parse.TemplateNode:
		return checkAssetNode(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, cmd := range n.Cmds {
			if err := checkAssetCommand(cmd); err != nil {
				return err
			}
		}
	case *parse.ChainNode:
		return checkAssetNode(n.Node)
	}
	return nil
}

func checkBranch(b *parse.BranchNode) error {
	// ElseList は else が無ければ nil（checkAssetNode が nil の ListNode を読み飛ばす）
	for _, n := range []parse.Node{b.Pipe, b.List, b.ElseList} {
		if err := checkAssetNode(n); err != nil {
			return err
		}
	}
	return nil
}

func checkAssetCommand(cmd *parse.CommandNode) error {
	for i, arg := range cmd.Args {
		if id, ok := arg.(*parse.IdentifierNode); ok && id.Ident == "asset" {
			// 値として渡す・パイプで受けるなどの書き方は、引数を起動時に確かめられないため認めない
			if i != 0 || len(cmd.Args) != 2 {
				return fmt.Errorf("asset must be called as {{asset \"path\"}} with one string literal")
			}
			s, ok := cmd.Args[1].(*parse.StringNode)
			if !ok {
				return fmt.Errorf("asset must be called with a string literal, not %s", cmd.Args[1])
			}
			if _, err := AssetURL(s.Text); err != nil {
				return err
			}
			continue
		}
		if err := checkAssetNode(arg); err != nil {
			return err
		}
	}
	return nil
}
