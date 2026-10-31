package features

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"minimal-web-kit/internal/federation"
	"minimal-web-kit/internal/web"
)

// Feature は独立した業務機能モジュールを表すインターフェースです。
type Feature interface {
	Name() string
	Init(db *sql.DB) error
	RegisterRoutes(mux *http.ServeMux, sm *web.SessionManager)
}

// CustomRootFeature は、Baseのデフォルトトップ画面（GET /）を独自のトップ画面（LPやHPなど）に差し替える機能モジュールが実装します。
type CustomRootFeature interface {
	Feature
	HandleRoot(w http.ResponseWriter, r *http.Request)
}

// IsolatedRoutesFeature は、CoreのCSRF検査とセッション解決（匿名セッションCookieの照合）を通さない経路を持つ機能が実装します。
// Cookieを照合しない公開ページや、独自の認証・CSRF保護を持つ管理画面に使います。
// 独立経路の認証・CSRF・Origin検査は、機能自身が実装する責任を負います。
type IsolatedRoutesFeature interface {
	Feature
	RegisterIsolatedRoutes(mux *web.IsolatedMux)
}

// NotFoundFeature は、静的ホスティング用の 404 ページを持つ機能（例: Site）が実装します。
// export-static が 404.html として書き出します。
type NotFoundFeature interface {
	Feature
	HandleNotFound(w http.ResponseWriter, r *http.Request)
}

// PurgeFeature は、期限切れデータの削除をCoreの起動時と定期のGCから呼ばれる機能が実装します。
type PurgeFeature interface {
	Feature
	Purge(ctx context.Context) error
}

// Command は、サーバーの実行ファイルのサブコマンドです（例: minimal-web-kit auth-setup）。
// 本番サーバーにGoを入れずに、運用の操作を実行できるようにします。
// サブコマンドの実行時は Init を呼ばないため、必要なマイグレーションや設定の検証はRunの中で行います。
type Command struct {
	Name    string
	Summary string
	Run     func(ctx context.Context, db *sql.DB, args []string, in io.Reader, out io.Writer) error
}

// CommandFeature は、サーバーの実行ファイルにサブコマンドを追加する機能が実装します。
type CommandFeature interface {
	Feature
	Commands() []Command
}

// NavLinkFeature は、共通のナビゲーションにリンクを足す機能が実装します（例: お知らせ機能の「お知らせ」）。
// 機能を無効にすればリンクも消えるため、他の機能が「この機能があるか」を判定する必要はありません。
type NavLinkFeature interface {
	Feature
	NavLinks() []web.NavLink
}

// PrivacyFeature は、保存する情報を説明する機能が実装します。
// 外部の人（閲覧者、問い合わせる人等）の情報は、Coreのプライバシーポリシーの画面に表として載ります（例: お問い合わせ）。
// ログインして使う社内の人の情報は Internal を付け、ログイン後の画面に出します（例: アカウント、社内文書）。
// 内容は docs/data-inventory.md と揃えます。
type PrivacyFeature interface {
	Feature
	PrivacyNotices() []web.PrivacyNotice
}

// AccountNoticeConsumer は、社内の人向けの保存する情報を、ログイン後の画面に出す機能（features/auth）が実装します。
type AccountNoticeConsumer interface {
	Feature
	SetAccountNotices(notices []web.PrivacyNotice)
}

// FallbackFeature は、どの経路にも当たらなかったGET・HEADを受ける機能が実装します（例: 固定ページの /{slug}）。
// 独立経路に1階層のワイルドカードを登録すると、Coreの経路（/privacy 等）を取ってしまうため、この口で受けます。
// 受け皿は1つまでで、2つ以上あると起動時にエラーになります。Cookieを照合しません。
type FallbackFeature interface {
	Feature
	HandleFallback(w http.ResponseWriter, r *http.Request)
}

// NotFoundConsumer は、404の画面（Siteがあればその画面）を受け取る機能が実装します。Siteが無ければ http.NotFound を受け取ります。
type NotFoundConsumer interface {
	Feature
	SetNotFound(handler http.HandlerFunc)
}

// RouteCheckConsumer は、パスが既存の経路に当たるかを確かめる関数を受け取る機能が実装します（例: 固定ページのスラッグの予約語）。
// 経路を作った後に渡されます。
type RouteCheckConsumer interface {
	Feature
	SetRouteTaken(taken func(path string) bool)
}

// ContactLinkFeature は、お問い合わせの行き先を申告する機能が実装します（Siteの外部フォームのURL、お問い合わせ機能の /contact）。
// ok が false なら申告しません。2つ以上の機能が申告した場合は、受付の口が二重になるため起動時にエラーになります。
type ContactLinkFeature interface {
	Feature
	ContactLink() (link web.ContactLink, ok bool)
}

// ContactLinkConsumer は、お問い合わせへのボタン等を描く機能（例: Site）が実装します。申告が無ければ呼ばれません。
type ContactLinkConsumer interface {
	Feature
	SetContactLink(link web.ContactLink)
}

// NavLinkConsumer は、独自のナビゲーションを描画する機能（例: Site）が実装します。
// 起動時に、有効な全機能のリンクを受け取ります。
// links は描画のたびに呼び、その時点のリンクを返します（固定ページの公開・非公開を再起動なしで反映するため）。
type NavLinkConsumer interface {
	Feature
	SetNavLinks(links func() []web.NavLink)
}

// DynamicNavLinkFeature は、公開ナビのリンクが実行中に変わる機能（例: 固定ページ）が実装します。
// DynamicNavLinks は描画のたびに呼ばれるため、DBを読まず、メモリに持った値を返します。
// 書式の検査は起動時に行わないため、リンクの書式（/ で始まるサイト内のパス）は機能が保証します。
type DynamicNavLinkFeature interface {
	Feature
	DynamicNavLinks() []web.NavLink
}

// LayoutFeature は、サイト全体のレイアウト（ヘッダー・フッター）を提供する機能（例: Site）が実装します。
// 有効な場合、Coreの画面（/privacy 等）や LayoutConsumer の公開ページも、このレイアウトで描画します。
type LayoutFeature interface {
	Feature
	Layout() web.Layout
}

// LayoutConsumer は、公開ページをサイト全体のレイアウトで描画できる機能（例: お知らせ機能の /news）が実装します。
// 起動時に、LayoutFeature が提供するレイアウトを受け取ります（無ければ呼ばれず、自分のレイアウトを使う）。
type LayoutConsumer interface {
	Feature
	SetLayout(layout web.Layout) error
}

// SitemapFeature は、sitemap.xml に載せるページ（例: お知らせ機能の公開中の記事）を持つ機能が実装します。
type SitemapFeature interface {
	Feature
	SitemapEntries(ctx context.Context) ([]web.SitemapEntry, error)
}

// SitemapConsumer は、sitemap.xml を返す機能（例: Site）が実装します。起動時に、SitemapFeature の一覧を受け取ります。
type SitemapConsumer interface {
	Feature
	SetSitemapSources(sources []SitemapFeature)
}

// Role はアカウントのロールです。管理者認証が決めるのは「アカウントを管理できるか」だけで、
// 業務の権限（記事を公開できるか等）は各機能が決めます。
type Role string

const (
	// RoleAdmin（システム管理者）は、アカウントの追加・ロール変更・停止・削除と登録URLの発行ができます。
	RoleAdmin Role = "admin"
	// RoleSiteAdmin（サイト管理者）は、各機能の管理画面を使えます。アカウントの管理はできません。
	RoleSiteAdmin Role = "site_admin"
	// RoleUser（一般ユーザー）は、AccountPage・AccountForm の画面だけを使えます。公開サイトの編集はできません。
	RoleUser Role = "user"
)

// Known は、定義されたロールかを返します。
func (r Role) Known() bool { return r.CanManageSite() || r == RoleUser }

// CanManageSite は、各機能の管理画面を使えるロールかを返します。未知の値は false です。
func (r Role) CanManageSite() bool { return r == RoleAdmin || r == RoleSiteAdmin }

// AdminSession は、ログイン中のアカウントの要求に付く情報です（同梱機能の管理者認証 features/auth が作る）。
type AdminSession interface {
	// AccountID はアカウントのIDです。再利用されません。呼び名（表示名）は返しません。
	// 機能が自分の表に保存する場合は、承認境界2（保存データの追加）と台帳の更新が要ります。
	AccountID() int64
	// Role はアカウントのロールです。要求ごとにアカウントから読みます。
	Role() Role
	// IsAdmin はロールがシステム管理者かを返します。
	IsAdmin() bool
	// CSRFToken は、管理画面のフォームに埋め込むCSRFトークンです（name="csrf_token"）。
	CSRFToken() string
	// PageData は、管理画面のレイアウトに渡す値（タイトル、現在のナビ、CSRFトークン、警告等）を返します。
	// current は、ナビで現在地として示すリンク先（例: "/admin/news"）です。
	PageData(title, current string) map[string]any
}

// Admin は、管理者認証（features/auth）が、管理画面を持つ他の機能に提供する口です。
// 他の機能は、ログイン・セッション・CSRFを自分で実装せず、これを使います。
type Admin interface {
	// Page は、各機能の管理画面（GET）を守ります。未ログインならログイン画面へ移し、
	// CanManageSite でないロールには403を返します。
	Page(next func(w http.ResponseWriter, r *http.Request, s AdminSession)) http.HandlerFunc
	// Form は、各機能の管理画面のフォーム送信（POST）を守ります。同一オリジン・セッション・CSRFトークンを確認し、
	// CanManageSite でないロールには403を返します。
	Form(next func(w http.ResponseWriter, r *http.Request, s AdminSession)) http.HandlerFunc
	// AdminPage は Page と同じく守り、さらにロールがシステム管理者でなければ403を返します。
	AdminPage(next func(w http.ResponseWriter, r *http.Request, s AdminSession)) http.HandlerFunc
	// AdminForm は Form と同じく守り、さらにロールがシステム管理者でなければ403を返します。
	AdminForm(next func(w http.ResponseWriter, r *http.Request, s AdminSession)) http.HandlerFunc
	// AccountPage は、ログインしていれば全てのロールが見られる画面（GET）を守ります。
	AccountPage(next func(w http.ResponseWriter, r *http.Request, s AdminSession)) http.HandlerFunc
	// AccountForm は、ログインしていれば全てのロールが送れるフォーム（POST）を守ります。
	// 同一オリジン・セッション・CSRFトークンの確認は Form と同じです。
	AccountForm(next func(w http.ResponseWriter, r *http.Request, s AdminSession)) http.HandlerFunc
	// Layout は、管理画面のレイアウト（ヘッダーのナビ、ログアウト、警告）です。
	Layout() web.Layout
}

// AdminProvider は、管理者認証を提供する機能（features/auth）が実装します。
type AdminProvider interface {
	Feature
	Admin() Admin
}

// AdminConsumer は、管理画面を持つ機能（例: お知らせ機能）が実装します。起動時に、管理者認証を受け取ります。
// 管理者認証が有効でない場合は、起動時にエラーになります。
type AdminConsumer interface {
	Feature
	SetAdmin(admin Admin) error
}

// DirectoryAccount は、共有先や承認者を選ぶためのアカウントの情報です。Disabled は停止中かです。
type DirectoryAccount struct {
	ID       int64
	Name     string
	Disabled bool
}

// AccountDirectory は、機能がアカウントの呼び名と状態を読む口です。認証の表を直接読まずに、これを使います。
// 一覧に無いIDは、削除済みのアカウントです。
type AccountDirectory interface {
	Accounts(ctx context.Context) ([]DirectoryAccount, error)
}

// AccountDirectoryProvider は、アカウントの一覧を提供する機能（features/auth）が実装します。
type AccountDirectoryProvider interface {
	Feature
	AccountDirectory() AccountDirectory
}

// AccountDirectoryConsumer は、アカウントの一覧を使う機能（例: 社内文書）が実装します。
// 提供する機能が無い場合（呼び名を持たない federatedauth 等）は、起動時にエラーになります。
type AccountDirectoryConsumer interface {
	Feature
	SetAccountDirectory(directory AccountDirectory)
}

// AdminNavFeature は、管理画面のナビにリンク（例: お知らせ機能の「お知らせの管理」）を足す機能が実装します。
type AdminNavFeature interface {
	Feature
	AdminNavLinks() []web.NavLink
}

// AdminNavConsumer は、管理画面のナビを描く機能（features/auth）が実装します。
type AdminNavConsumer interface {
	Feature
	SetAdminNavLinks(links []web.NavLink)
}

// ErrBearerInvalid は、トークンが無効なことを表します。ほかのエラーは確認不能で、どちらでも実行してはいけません。
var ErrBearerInvalid = errors.New("features: invalid bearer token")

// AgentScope は、エージェントに許す権限の1つです。Label は同意の画面に出します。
type AgentScope struct{ Name, Label string }

// AgentAuthConfig は、エージェントの認証を有効にする機能（features/mcp）が渡す設定です。
type AgentAuthConfig struct {
	Path   string // 保護するリソースのパス（例: /mcp）
	Scopes []AgentScope
}

// AgentPrincipal は、検証済みのトークンの持ち主です。ロールは要求ごとにアカウントから読みます。
type AgentPrincipal struct {
	AccountID  int64
	Client     string
	Role       Role
	Scopes     []string
	Generation string // 認可の世代。確認の結びつけに使う。意味は解釈しない
}

// HasScope は、トークンがその権限を持つかを返します。
func (p AgentPrincipal) HasScope(name string) bool {
	return slices.Contains(p.Scopes, name)
}

// エージェントのクライアントの管理の失敗です。入力の誤りと、対象が無いことを分けます。
var (
	ErrAgentInvalid  = errors.New("features: invalid agent client input")
	ErrAgentNotFound = errors.New("features: agent client not found")
	// ErrAgentConflict は、画面を開いた後に状態が変わっていて、操作が当たらないことを表します。何も変えていません。
	ErrAgentConflict = errors.New("features: agent client state changed")
)

// AgentAccount は、クライアントの利用を許可できるアカウントです。Name は管理画面に出す呼び名です。
type AgentAccount struct {
	ID      int64
	Name    string
	Allowed bool
}

// AgentClient は、登録したエージェントのクライアントです。ID は秘密の値ではありません。
type AgentClient struct {
	ID, Label, Redirect string
	Enabled             bool
	Accounts            []AgentAccount
}

// AgentAuth は、エージェントの認証の口です。呼び出した機能が RegisterAgentRoutes で経路を足します。
type AgentAuth interface {
	RegisterAgentRoutes(mux *web.IsolatedMux)
	// VerifyBearer は、要求ごとにトークンを確かめます。無効なら ErrBearerInvalid を返します。
	VerifyBearer(ctx context.Context, token, resource string) (AgentPrincipal, error)
	AuthorizationServers() []string
	// Resource は、保護するリソースの正規のURLです（例: https://example.com/mcp）。
	Resource() string
	// Origin は、サイトのオリジンです。
	Origin() string
	// AgentClients は、登録したクライアントを返します。OIDCの連携先は含みません。
	AgentClients(ctx context.Context) ([]AgentClient, error)
	// AgentClient は、1件と、有効なアカウントごとの許可の状態を返します。
	AgentClient(ctx context.Context, id string) (AgentClient, error)
	// AddAgentClient は、クライアントを登録し、同じトランザクションで owner に利用を許可します。
	AddAgentClient(ctx context.Context, label, redirect string, owner int64) (AgentClient, error)
	// SetAgentClientEnabled は、停止・再開します。停止すると発行済みのトークンも無効になります。
	SetAgentClientEnabled(ctx context.Context, id string, enabled bool) error
	// SetAgentGrant は、アカウントへの許可を変えます。取り消すと発行済みのトークンも無効になります。
	SetAgentGrant(ctx context.Context, id string, account int64, allowed bool) error
}

// AgentAuthProvider は、エージェントの認証を提供する機能（features/auth）が実装します。
type AgentAuthProvider interface {
	Feature
	AgentAuth(cfg AgentAuthConfig) (AgentAuth, error)
}

// AgentAuthConsumer は、エージェントの認証を使う機能（features/mcp）が実装します。起動時に、提供者から口を受け取ります。
type AgentAuthConsumer interface {
	Feature
	AgentAuthConfig() AgentAuthConfig
	SetAgentAuth(auth AgentAuth) error
}

// MCPEffect は、ツールが状態を変えるかです。ゼロ値は書き込みで、宣言を省いても確認なしになりません。
type MCPEffect int

const (
	MCPWrite MCPEffect = iota
	MCPRead
)

// ErrMCPConflict は、確認の後に対象が変わったことを表します。何も保存していません。
var ErrMCPConflict = errors.New("features: target changed after confirmation")

// MCPInputError は、エージェントに返してよい固定の説明です。入力の値を含めません。
type MCPInputError struct{ Message string }

func (e MCPInputError) Error() string { return e.Message }

// MCPPlan は、書き込みの検査の結果です。保存はしていません。
type MCPPlan struct {
	Summary   string          // 確認に見せる文。固定の説明と引用をサーバーが組み立てる
	Canonical json.RawMessage // 検査を通した入力。実行はこの値で行う
	State     string          // 対象の状態のハッシュ。Apply の照合に使う
}

// MCPTool は、機能がMCPで提供する操作です。読み取りは Read だけ、書き込みは Prepare と Apply だけを持ちます。
type MCPTool struct {
	Name, Title, Description string
	InputSchema              json.RawMessage
	Effect                   MCPEffect
	// AllRoles は、一般ユーザーにも開くツールに付けます。省くと、各機能の管理画面を使えるロールだけが使えます。
	AllRoles bool
	Read     func(ctx context.Context, p AgentPrincipal, args json.RawMessage) (any, error)
	Prepare  func(ctx context.Context, p AgentPrincipal, args json.RawMessage) (MCPPlan, error)
	// Apply は、state の照合と保存を不可分に行います。変わっていたら ErrMCPConflict を返します。
	Apply func(ctx context.Context, p AgentPrincipal, canonical json.RawMessage, state string) (any, error)
}

// MCPFeature は、MCPのツールを提供する機能が実装します。別の機能も、これを実装すれば足せます。
type MCPFeature interface {
	Feature
	MCPLabel() string
	MCPTools() []MCPTool
}

// MCPEnableNoticeFeature は、MCPを有効にする前にシステム管理者へ注意を見せる機能が実装します。
// 確認の画面を経ないと、画面の操作では有効になりません。
type MCPEnableNoticeFeature interface {
	MCPFeature
	MCPEnableNotice() string
}

// MCPConsumer は、MCPのサーバーを持つ機能（features/mcp）が実装します。起動時に、ツールを提供する機能を受け取ります。
type MCPConsumer interface {
	Feature
	SetMCPFeatures(list []MCPFeature) error
}

// MCPDecode は、ツールの引数を厳密に読みます。未知の項目・重複したキー・末尾の別のJSONは拒否します。
func MCPDecode(args json.RawMessage, target any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if err := federation.Decode(args, target); err != nil {
		return MCPInputError{"引数の形が正しくありません。"}
	}
	return nil
}

// MCPQuote は、利用者の入力を引用として示します。行の先頭に印を付け、確認の文の枠と区別します。
func MCPQuote(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// MCPSummary は、確認の文を組み立てます。クライアントは長い文の途中を折りたたむことがあるので、
// 操作・対象・変更する欄の名前を最初の3行に置き、全文の引用はその後に続けます。
func MCPSummary(action, target string, changes []string, details string) string {
	summary := action + "\n" + target + "\n"
	if len(changes) == 0 {
		summary += "変更: なし"
	} else {
		summary += "変更: " + strings.Join(changes, "、")
	}
	if details != "" {
		summary += "\n\n" + strings.TrimRight(details, "\n")
	}
	return summary
}

// MCPState は、状態を構成する値から、確認の照合に使うハッシュを作ります。区切りが混ざらないようJSONにします。
func MCPState(values ...any) string {
	raw, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Registry は有効化されている全機能のリストを保持します。
type Registry struct {
	features []Feature
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Register(f Feature) {
	r.features = append(r.features, f)
}

func (r *Registry) All() []Feature {
	return r.features
}

// NavLinks は、有効な機能が宣言したナビゲーションのリンクを登録順に返します。
// リンク先はサイト内のパス（"/" で始まり "//" で始まらない）に限ります。
// NavLinkSource は、静的なリンク（NavLinks。起動時に検査する）に、実行中に変わるリンク（DynamicNavLinks）を
// 後ろに足して返す関数を作ります。
func (r *Registry) NavLinkSource() (func() []web.NavLink, error) {
	static, err := r.NavLinks()
	if err != nil {
		return nil, err
	}
	var dynamic []DynamicNavLinkFeature
	for _, f := range r.features {
		if d, ok := f.(DynamicNavLinkFeature); ok {
			dynamic = append(dynamic, d)
		}
	}
	if len(dynamic) == 0 {
		return func() []web.NavLink { return static }, nil
	}
	return func() []web.NavLink {
		links := append([]web.NavLink{}, static...)
		for _, d := range dynamic {
			links = append(links, d.DynamicNavLinks()...)
		}
		return links
	}, nil
}

func (r *Registry) NavLinks() ([]web.NavLink, error) {
	var links []web.NavLink
	for _, f := range r.features {
		nf, ok := f.(NavLinkFeature)
		if !ok {
			continue
		}
		for _, l := range nf.NavLinks() {
			if l.Label == "" || !strings.HasPrefix(l.Href, "/") || strings.HasPrefix(l.Href, "//") || strings.ContainsAny(l.Href, "\\ \t\r\n") {
				return nil, fmt.Errorf("feature %s: nav link must have a label and a same-site path", f.Name())
			}
			links = append(links, l)
		}
	}
	return links, nil
}
