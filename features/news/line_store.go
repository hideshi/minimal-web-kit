package news

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"minimal-web-kit/internal/store"
)

// lineRetryWindow は、同じ鍵で送り直せる時間です。LINE の鍵は最初の要求から24時間有効で、時刻のずれを見込んで23時間にします。
const lineRetryWindow = 23 * time.Hour

var (
	errLineNotPublic   = errors.New("news: post is not public")
	errLineSentAlready = errors.New("news: already sent to LINE")
	errLineUnknown     = errors.New("news: LINE result is unknown")
	errLineChanged     = errors.New("news: LINE message changed after confirmation")
)

// lineState は、送った記録の状態です（docs/domain/news.md の表）。
type lineState int

const (
	lineUnsent  lineState = iota // 記録が無い
	lineReady                    // 前の要求はすべて受け付けられなかった
	linePending                  // 受け付けたかもしれない要求があり、23時間のうち
	lineUnknown                  // 受け付けたかもしれない要求があり、23時間を過ぎた
	lineDone                     // 送信済み
)

// lineRecord は、記事ごとの送った記録です。
type lineRecord struct {
	PostID         int64
	Key            string
	Message        string
	KeyFirstSentAt int64 // 0 なら、まだ要求を出していない
	InFlight       int
	Uncertain      bool
	SentAt         int64 // 0 なら、まだ送信済みでない
}

// maybeAccepted は、受け付けたかもしれない要求があったかを返します。
func (r *lineRecord) maybeAccepted() bool { return r.Uncertain || r.InFlight > 0 }

// deadline は、同じ鍵で送り直せる最後の時刻（この時刻より前だけ送れる）です。
func (r *lineRecord) deadline() time.Time {
	return time.Unix(r.KeyFirstSentAt, 0).Add(lineRetryWindow)
}

// state は、記録の状態を返します。r が nil なら未送信です（NEWS-INV-21）。
func (r *lineRecord) state(now time.Time) lineState {
	switch {
	case r == nil:
		return lineUnsent
	case r.SentAt != 0:
		return lineDone
	case !r.maybeAccepted():
		return lineReady
	case r.KeyFirstSentAt != 0 && now.Before(r.deadline()):
		return linePending
	}
	return lineUnknown
}

// lineStore は、送った記録（news_line_broadcasts）を読み書きします。
type lineStore struct {
	db  *sql.DB
	now func() time.Time
}

const lineColumns = "post_id, retry_key, message, COALESCE(key_first_sent_at, 0), in_flight, uncertain, COALESCE(sent_at, 0)"

func scanLine(row interface{ Scan(...any) error }) (*lineRecord, error) {
	var r lineRecord
	err := row.Scan(&r.PostID, &r.Key, &r.Message, &r.KeyFirstSentAt, &r.InFlight, &r.Uncertain, &r.SentAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// get は記事の記録を返します。無ければ nil です。
func (s *lineStore) get(ctx context.Context, postID int64) (*lineRecord, error) {
	return scanLine(s.db.QueryRowContext(ctx, "SELECT "+lineColumns+" FROM news_line_broadcasts WHERE post_id = ?", postID))
}

// prepare は、一斉送信の前に、1つのトランザクションで記録を作るか直し、in_flight を1増やしてコミットします。
// 記事が公開側に出ていること（NEWS-INV-19）、確認の画面で見せた文面と同じこと、状態が送れることを、同じトランザクションで確かめます。
// コミットできたときだけ、返した鍵と文面で送ります（NEWS-INV-20）。鍵の入れ替えは、受け付けなかった要求だけの記録に限ります（NEWS-INV-21）。
func (s *lineStore) prepare(ctx context.Context, cfg *lineConfig, postID int64, digest string, newKey func() (string, error)) (lineRecord, error) {
	now := s.now()
	var out lineRecord
	err := store.Atomic(ctx, s.db, func(c *sql.Conn) error {
		var p Post
		err := c.QueryRowContext(ctx, "SELECT id, title FROM news_posts WHERE id = ? AND status = ? AND published_at <= ?",
			postID, statusPublished, now.Unix()).Scan(&p.ID, &p.Title)
		if errors.Is(err, sql.ErrNoRows) {
			var n int
			if err := c.QueryRowContext(ctx, "SELECT COUNT(*) FROM news_posts WHERE id = ?", postID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
			return errLineNotPublic
		}
		if err != nil {
			return err
		}
		rec, err := scanLine(c.QueryRowContext(ctx, "SELECT "+lineColumns+" FROM news_line_broadcasts WHERE post_id = ?", postID))
		if err != nil {
			return err
		}
		switch rec.state(now) {
		case lineDone:
			return errLineSentAlready
		case lineUnknown:
			return errLineUnknown
		case lineUnsent:
			msg, err := lineMessage(cfg, p)
			if err != nil {
				return err
			}
			if lineDigest(msg) != digest {
				return errLineChanged
			}
			key, err := newKey()
			if err != nil {
				return err
			}
			if _, err := c.ExecContext(ctx,
				"INSERT INTO news_line_broadcasts (post_id, retry_key, message, key_first_sent_at, in_flight, uncertain, created_at) VALUES (?, ?, ?, ?, 1, 0, ?)",
				postID, key, msg, now.Unix(), now.Unix()); err != nil {
				return err
			}
			out = lineRecord{PostID: postID, Key: key, Message: msg, KeyFirstSentAt: now.Unix(), InFlight: 1}
			return nil
		}
		// 記録がある: 文面は作り直さず、保存した文面を確認の画面で見せたかを確かめる
		if lineDigest(rec.Message) != digest {
			return errLineChanged
		}
		if rec.state(now) == lineReady && (rec.KeyFirstSentAt == 0 || !now.Before(rec.deadline())) {
			// 前の鍵の要求はどれも受け付けられていないので、期限の切れた鍵だけを新しくする
			key, err := newKey()
			if err != nil {
				return err
			}
			rec.Key, rec.KeyFirstSentAt = key, now.Unix()
		}
		res, err := c.ExecContext(ctx,
			"UPDATE news_line_broadcasts SET retry_key = ?, key_first_sent_at = ?, in_flight = in_flight + 1 WHERE post_id = ? AND sent_at IS NULL",
			rec.Key, rec.KeyFirstSentAt, postID)
		if err := store.AffectedOne(res, err, errLineSentAlready); err != nil {
			return err
		}
		rec.InFlight++
		out = *rec
		return nil
	})
	return out, err
}

// finish は、一斉送信の応答で記録を直します。in_flight を1減らし、受け付けたなら送信済みに、分からないなら uncertain を1にします。
// 送信済みの記録は変えません（NEWS-INV-22）。記事が消えて記録が無いときは、何もしません。
func (s *lineStore) finish(ctx context.Context, postID int64, outcome lineOutcome) error {
	var err error
	switch {
	case outcome.accepted():
		_, err = s.db.ExecContext(ctx,
			"UPDATE news_line_broadcasts SET in_flight = MAX(in_flight - 1, 0), sent_at = ? WHERE post_id = ? AND sent_at IS NULL",
			s.now().Unix(), postID)
	case outcome == lineUncertain:
		_, err = s.db.ExecContext(ctx,
			"UPDATE news_line_broadcasts SET in_flight = MAX(in_flight - 1, 0), uncertain = 1 WHERE post_id = ? AND sent_at IS NULL", postID)
	default:
		_, err = s.db.ExecContext(ctx,
			"UPDATE news_line_broadcasts SET in_flight = MAX(in_flight - 1, 0) WHERE post_id = ? AND sent_at IS NULL", postID)
	}
	return err
}
