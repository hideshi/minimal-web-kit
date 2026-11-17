package news

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/notice"
)

// LINEでの一斉配信の画面と送信。仕様は docs/domain/news.md（NEWS-INV-19〜25）。

// 送信の結果の文。応答の本文やエラーの文は出さない（NEWS-INV-24）。
// 利用者が読む文なので、どういう状態か・もう一度配信できるか・配信したら何が起きるかを、技術の言葉を使わずに書く。
const (
	lineMsgToken     = "LINE につながらなかったため、配信していません。チャネル ID・チャネルシークレットの設定と、サーバーの通信を確かめてから、もう一度配信してください。"
	lineMsgRejected  = "LINE が配信を断ったため、配信していません。LINE 公式アカウントの Messaging API の設定を確かめてから、もう一度配信してください。"
	lineMsgLimited   = "LINE の配信の上限（1時間の回数か、月の通数）に当たったため、配信していません。時間をおくか、LINE 公式アカウントの管理画面で通数を確かめてから、もう一度配信してください。"
	lineMsgUncertain = "LINE から結果が返ってこなかったため、友だちに届いたかどうか分かりません。下の「LINEで一斉配信」に出ている期限までなら、もう一度配信できます。すでに届いていても、同じメッセージが2通届くことはありません。"
	lineMsgRecord    = "LINE への配信は行いましたが、その結果をこのサイトに記録できませんでした。友だちに届いている可能性があります。下の「LINEで一斉配信」に出ている期限までに、もう一度配信してください。届いていれば2通目は送られず、記録だけが配信済みになります。届いていなければ、そこで配信されます。"
	lineMsgNotPublic = "公開ページに出ていない記事は、LINEで一斉配信できません。"
	lineMsgSent      = "この記事は、もうLINEで一斉配信しています。1つの記事で配信できるのは1回だけです。"
	lineMsgUnknown   = "この記事は、友だちに届いたかどうか分からないまま、もう一度配信できる期限を過ぎたため、配信できません。"
	lineMsgChanged   = "確認の画面を開いた後に、配信する文面が変わりました。文面を確かめてから配信してください。"
)

// lineView は、編集の画面に出す LINEでの一斉配信の状況です。
type lineView struct {
	PostID   int64
	State    lineState
	Public   bool
	SentAt   time.Time
	Deadline time.Time
}

func (v lineView) CanSend() bool {
	return v.Public && (v.State == lineUnsent || v.State == lineReady || v.State == linePending)
}
func (v lineView) Done() bool    { return v.State == lineDone }
func (v lineView) Pending() bool { return v.State == linePending }
func (v lineView) Unknown() bool { return v.State == lineUnknown }

// registerLineRoutes は、LINEでの一斉配信の経路を登録します。設定が無ければ登録しません（NEWS-INV-25）。
// 確認の画面（GET）と送信（POST）だけで、サイト管理者以上の Page・Form で守ります。GET では送りません（NEWS-INV-23）。
func (f *Feature) registerLineRoutes(mux *web.IsolatedMux) {
	if f.line == nil {
		return
	}
	mux.HandleFunc("GET /admin/news/{id}/line", f.admin.Page(f.handleLinePage))
	mux.HandleFunc("POST /admin/news/{id}/line", f.admin.Form(f.handleLineSend))
}

// addLine は、設定があれば、編集の画面に LINEでの一斉配信の状況を加えます。
func (f *Feature) addLine(w http.ResponseWriter, r *http.Request, data map[string]any, p Post) bool {
	if f.line == nil {
		return true
	}
	rec, err := f.lines.get(r.Context(), p.ID)
	if err != nil {
		f.writeFailed(w, "news line record failed", err)
		return false
	}
	now := f.now()
	v := lineView{PostID: p.ID, State: rec.state(now), Public: p.Published && !p.PublishedAt.After(now)}
	if rec != nil {
		v.SentAt = time.Unix(rec.SentAt, 0).In(time.Local)
		v.Deadline = rec.deadline().In(time.Local)
	}
	data["Line"] = v
	return true
}

// handleLinePage は、送る文面をそのまま見せる確認の画面です。記録があれば保存した文面を、無ければ今の記事から作った文面を見せます。
func (f *Feature) handleLinePage(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	f.renderLineConfirm(w, r, s, id, http.StatusOK, "")
}

func (f *Feature) renderLineConfirm(w http.ResponseWriter, r *http.Request, s features.AdminSession, id int64, status int, errMsg string) {
	p, err := f.posts.GetPublished(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		f.renderEditError(w, r, s, id, http.StatusConflict, lineMsgNotPublic)
		return
	}
	if err != nil {
		f.writeFailed(w, "news get post failed", err)
		return
	}
	rec, err := f.lines.get(r.Context(), id)
	if err != nil {
		f.writeFailed(w, "news line record failed", err)
		return
	}
	switch rec.state(f.now()) {
	case lineDone:
		f.renderEditError(w, r, s, id, http.StatusConflict, lineMsgSent)
		return
	case lineUnknown:
		f.renderEditError(w, r, s, id, http.StatusConflict, lineMsgUnknown)
		return
	}
	msg := ""
	if rec != nil {
		msg = rec.Message
	} else if msg, err = lineMessage(f.cfg.Line, p); err != nil {
		f.writeFailed(w, "news line message failed", err)
		return
	}
	back := "/admin/news/" + strconv.FormatInt(id, 10)
	data := f.pageData(s, "LINEでの一斉配信の確認")
	data["Post"] = p
	data["Lines"] = strings.Split(msg, "\n")
	data["Stored"] = rec != nil
	data["Retry"] = rec != nil && rec.state(f.now()) == linePending
	data["Error"] = errMsg
	data["Confirm"] = form.Confirm{
		Action: back + "/line", Back: back, CSRFToken: s.CSRFToken(),
		Submit: "一斉配信する", BackText: "配信せずに戻る", Primary: true,
		Hidden: []form.Hidden{{Name: "digest", Value: lineDigest(msg)}},
	}
	f.renderAdminStatus(w, status, "admin_post_line.html", data)
}

// handleLineSend は、トークンを発行し、記録をコミットしてから一斉送信し、応答で記録を直します（NEWS-INV-20）。
func (f *Feature) handleLineSend(w http.ResponseWriter, r *http.Request, s features.AdminSession) {
	id, ok := web.PathID(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	// 送れない記録なら、トークンを発行する前に断る（同じ確かめは prepare のトランザクションでもう一度行う）
	rec, err := f.lines.get(ctx, id)
	if err != nil {
		f.writeFailed(w, "news line record failed", err)
		return
	}
	switch rec.state(f.now()) {
	case lineDone:
		f.renderEditError(w, r, s, id, http.StatusConflict, lineMsgSent)
		return
	case lineUnknown:
		f.renderEditError(w, r, s, id, http.StatusConflict, lineMsgUnknown)
		return
	}
	token, err := f.line.issueToken(ctx)
	if err != nil {
		f.logger.Warn("news line broadcast", f.requestID(w), postIDAttr(id), slog.String("result", "token_failed"))
		f.renderEditError(w, r, s, id, http.StatusBadGateway, lineMsgToken)
		return
	}
	prepared, err := f.lines.prepare(ctx, f.cfg.Line, id, r.PostForm.Get("digest"), f.newKey)
	switch {
	case errors.Is(err, ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, errLineNotPublic):
		f.renderEditError(w, r, s, id, http.StatusConflict, lineMsgNotPublic)
		return
	case errors.Is(err, errLineSentAlready):
		f.renderEditError(w, r, s, id, http.StatusConflict, lineMsgSent)
		return
	case errors.Is(err, errLineUnknown):
		f.renderEditError(w, r, s, id, http.StatusConflict, lineMsgUnknown)
		return
	case errors.Is(err, errLineChanged):
		f.renderLineConfirm(w, r, s, id, http.StatusConflict, lineMsgChanged)
		return
	case err != nil:
		// コミットできなければ送らない（NEWS-INV-20）
		f.writeFailed(w, "news line prepare failed", err)
		return
	}
	outcome := f.line.broadcast(ctx, token, prepared.Key, prepared.Message)
	f.logger.Info("news line broadcast", f.requestID(w), postIDAttr(id), slog.String("result", string(outcome)))
	if err := f.lines.finish(ctx, id, outcome); err != nil {
		// in_flight が残るので結果待ちになる。同じ鍵で送り直せば、受け付け済みなら 409 で送信済みになる
		level := slog.LevelError
		if store.Busy(err) {
			level = slog.LevelWarn
		}
		f.logger.Log(ctx, level, "news line record failed", f.requestID(w), postIDAttr(id))
		f.renderEditError(w, r, s, id, http.StatusServiceUnavailable, lineMsgRecord)
		return
	}
	switch outcome {
	case lineSent, lineAlreadyAccepted:
		notice.Redirect(w, r, "/admin/news/"+strconv.FormatInt(id, 10), "line_sent")
	case lineRateLimited:
		f.renderEditError(w, r, s, id, http.StatusBadGateway, lineMsgLimited)
	case lineRejected:
		f.renderEditError(w, r, s, id, http.StatusBadGateway, lineMsgRejected)
	default:
		f.renderEditError(w, r, s, id, http.StatusBadGateway, lineMsgUncertain)
	}
}

// renderEditError は、編集の画面を、画面全体のエラーを付けて返します。
func (f *Feature) renderEditError(w http.ResponseWriter, r *http.Request, s features.AdminSession, id int64, status int, msg string) {
	p, err := f.posts.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		f.writeFailed(w, "news get post failed", err)
		return
	}
	data := f.pageData(s, "お知らせの編集")
	data["Post"] = adminLocalTime(p)
	data["Form"] = formFromPost(p)
	data["Error"] = msg
	if f.addCover(w, r, data, formFromPost(p)) && f.addLine(w, r, data, p) {
		f.renderAdminStatus(w, status, "admin_post_form.html", data)
	}
}
