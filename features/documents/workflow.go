package documents

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"minimal-web-kit/internal/web/form"
)

func uniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// touch は文書の更新日時を進めます。
func (s *Store) touch(ctx context.Context, c *sql.Conn, id int64) error {
	_, err := c.ExecContext(ctx, "UPDATE documents SET updated_at = ? WHERE id = ?", s.now().Unix(), id)
	return err
}

// folderExists は、指定のフォルダがあることを確かめます。0はフォルダなしです。
func folderValue(ctx context.Context, c *sql.Conn, id int64) (any, error) {
	if id == 0 {
		return nil, nil
	}
	var found int64
	err := c.QueryRowContext(ctx, "SELECT id FROM document_folders WHERE id = ?", id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, InputError{"folder"}
	}
	return found, err
}

// Create は、文書と最初の下書きを作ります。既定では作成者しか読めません。
func (s *Store) Create(ctx context.Context, owner int64, in Input) (int64, error) {
	in, err := normalize(in)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.atomic(ctx, func(c *sql.Conn) error {
		folder, err := folderValue(ctx, c, in.FolderID)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		res, err := c.ExecContext(ctx, "INSERT INTO documents (owner_account_id, folder_id, created_at, updated_at) VALUES (?, ?, ?, ?)", owner, folder, now, now)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, "INSERT INTO document_versions (document_id, number, title, body, state, created_at) VALUES (?, 1, ?, ?, 'draft', ?)", id, in.Title, in.Body, now)
		return err
	})
	return id, err
}

// SaveDraft は下書きを保存します。versionID は画面を開いたときの下書きで、別の版に当たれば ErrConflict です。
// 不変条件: DOC-INV-3、DOC-INV-5
func (s *Store) SaveDraft(ctx context.Context, owner, id, versionID int64, in Input) error {
	in, err := normalize(in)
	if err != nil {
		return err
	}
	return s.atomic(ctx, func(c *sql.Conn) error {
		if _, err := s.owned(ctx, c, owner, id); err != nil {
			return err
		}
		folder, err := folderValue(ctx, c, in.FolderID)
		if err != nil {
			return err
		}
		res, err := c.ExecContext(ctx, "UPDATE document_versions SET title = ?, body = ? WHERE id = ? AND document_id = ? AND state = 'draft'", in.Title, in.Body, versionID, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrConflict
		}
		if _, err := c.ExecContext(ctx, "UPDATE documents SET folder_id = ? WHERE id = ?", folder, id); err != nil {
			return err
		}
		return s.touch(ctx, c, id)
	})
}

// newDraft は、指定の版と同じ内容の新しい下書きを作ります。
func (s *Store) newDraft(ctx context.Context, c *sql.Conn, id, from int64) error {
	_, err := c.ExecContext(ctx, `INSERT INTO document_versions (document_id, number, title, body, state, created_at)
		SELECT document_id, (SELECT MAX(number) + 1 FROM document_versions WHERE document_id = ?), title, body, 'draft', ?
		FROM document_versions WHERE id = ?`, id, s.now().Unix(), from)
	return err
}

// Revise は、今の版から改定の下書きを作ります。下書きか承認待ちがあれば ErrConflict です。公開中の版はそのままです。
// 不変条件: DOC-INV-4
func (s *Store) Revise(ctx context.Context, owner, id int64) error {
	return s.atomic(ctx, func(c *sql.Conn) error {
		d, err := s.owned(ctx, c, owner, id)
		if err != nil {
			return err
		}
		if d.CurrentID == 0 {
			return ErrConflict
		}
		if err := s.newDraft(ctx, c, id, d.CurrentID); uniqueViolation(err) {
			return ErrConflict
		} else if err != nil {
			return err
		}
		return s.touch(ctx, c, id)
	})
}

// makeCurrent は版を公開し、今の版にします。
func (s *Store) makeCurrent(ctx context.Context, c *sql.Conn, id, versionID int64, from string) error {
	now := s.now().Unix()
	res, err := c.ExecContext(ctx, "UPDATE document_versions SET state = 'published', published_at = ? WHERE id = ? AND document_id = ? AND state = ?", now, versionID, id, from)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrConflict
	}
	if _, err := c.ExecContext(ctx, "UPDATE documents SET current_version_id = ?, updated_at = ? WHERE id = ?", versionID, now, id); err != nil {
		return err
	}
	return nil
}

// Publish は、承認を受けない文書の下書きを公開します。一度でも承認を依頼した文書は、承認を経ないと公開できません
// （差し戻された内容や、承認済みの規定の改定を、承認なしで出さないため）。
// 不変条件: DOC-INV-6
func (s *Store) Publish(ctx context.Context, owner, id, versionID int64) error {
	return s.atomic(ctx, func(c *sql.Conn) error {
		if _, err := s.owned(ctx, c, owner, id); err != nil {
			return err
		}
		var asked bool
		err := c.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM document_approvers a JOIN document_versions v ON v.id = a.version_id WHERE v.document_id = ?)`, id).Scan(&asked)
		if err != nil {
			return err
		}
		if asked {
			return ErrConflict
		}
		return s.makeCurrent(ctx, c, id, versionID, stateDraft)
	})
}

// RequiresApproval は、一度でも承認を依頼した文書かを返します。
func (s *Store) RequiresApproval(ctx context.Context, id int64) (bool, error) {
	var asked bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM document_approvers a JOIN document_versions v ON v.id = a.version_id WHERE v.document_id = ?)`, id).Scan(&asked)
	return asked, err
}

// RequestApproval は下書きを固定して、承認を依頼します。所有者本人は承認者に選べません。
// active は有効なアカウントかを返す関数です。
// 不変条件: DOC-INV-5、DOC-INV-7
func (s *Store) RequestApproval(ctx context.Context, owner, id, versionID int64, approvers []int64, active func(int64) bool) error {
	if len(approvers) == 0 || slices.Contains(approvers, owner) {
		return InputError{"approvers"}
	}
	for _, a := range approvers {
		if !active(a) {
			return InputError{"approvers"}
		}
	}
	return s.atomic(ctx, func(c *sql.Conn) error {
		if _, err := s.owned(ctx, c, owner, id); err != nil {
			return err
		}
		res, err := c.ExecContext(ctx, "UPDATE document_versions SET state = 'pending', requested_at = ? WHERE id = ? AND document_id = ? AND state = 'draft'", s.now().Unix(), versionID, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrConflict
		}
		for _, a := range slices.Compact(slices.Sorted(slices.Values(approvers))) {
			if _, err := c.ExecContext(ctx, "INSERT INTO document_approvers (version_id, account_id) VALUES (?, ?)", versionID, a); err != nil {
				return err
			}
		}
		return s.touch(ctx, c, id)
	})
}

// closePending は承認待ちの版を閉じ、同じ内容の下書きを作ります。
func (s *Store) closePending(ctx context.Context, c *sql.Conn, id, versionID int64, state string) error {
	res, err := c.ExecContext(ctx, "UPDATE document_versions SET state = ? WHERE id = ? AND document_id = ? AND state = 'pending'", state, versionID, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrConflict
	}
	return s.newDraft(ctx, c, id, versionID)
}

// Withdraw は承認の依頼を取り下げます。固定した版は残し、同じ内容の下書きを作ります。
func (s *Store) Withdraw(ctx context.Context, owner, id, versionID int64) error {
	return s.atomic(ctx, func(c *sql.Conn) error {
		if _, err := s.owned(ctx, c, owner, id); err != nil {
			return err
		}
		if err := s.closePending(ctx, c, id, versionID, stateWithdrawn); err != nil {
			return err
		}
		return s.touch(ctx, c, id)
	})
}

// recordDecision は、承認待ちの版について、本人の未判断の行だけを更新します。
func (s *Store) recordDecision(ctx context.Context, c *sql.Conn, viewer, versionID int64, decision, reason string) error {
	res, err := c.ExecContext(ctx, `UPDATE document_approvers SET decision = ?, reason = ?, decided_at = ?
		WHERE version_id = ? AND account_id = ? AND decision IS NULL
		AND EXISTS (SELECT 1 FROM document_versions v WHERE v.id = ? AND v.state = 'pending')`,
		decision, reason, s.now().Unix(), versionID, viewer, versionID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrConflict
	}
	return nil
}

// Approve は版を承認します。全員がそろえば、同じトランザクションで公開します。
// 不変条件: DOC-INV-6
func (s *Store) Approve(ctx context.Context, viewer, id, versionID int64) error {
	return s.atomic(ctx, func(c *sql.Conn) error {
		if err := s.checkApprover(ctx, c, viewer, id, versionID); err != nil {
			return err
		}
		if err := s.recordDecision(ctx, c, viewer, versionID, "approved", ""); err != nil {
			return err
		}
		var waiting int
		err := c.QueryRowContext(ctx, "SELECT COUNT(*) FROM document_approvers WHERE version_id = ? AND (decision IS NULL OR decision != 'approved')", versionID).Scan(&waiting)
		if err != nil || waiting > 0 {
			return err
		}
		return s.makeCurrent(ctx, c, id, versionID, statePending)
	})
}

// Reject は版を差し戻します。残りの人は判断できなくなり、所有者には同じ内容の下書きが戻ります。
func (s *Store) Reject(ctx context.Context, viewer, id, versionID int64, reason string) error {
	reason, err := form.Line(reason, maxReasonRunes, true)
	if err != nil {
		return InputError{"reason"}
	}
	return s.atomic(ctx, func(c *sql.Conn) error {
		if err := s.checkApprover(ctx, c, viewer, id, versionID); err != nil {
			return err
		}
		if err := s.recordDecision(ctx, c, viewer, versionID, "rejected", reason); err != nil {
			return err
		}
		if err := s.closePending(ctx, c, id, versionID, stateRejected); err != nil {
			return err
		}
		return s.touch(ctx, c, id)
	})
}

// checkApprover は、閲覧者がその版の承認者であることを確かめます。承認者でなければ、版の有無が分からないよう ErrNotFound です。
// 不変条件: DOC-INV-2
func (s *Store) checkApprover(ctx context.Context, c *sql.Conn, viewer, id, versionID int64) error {
	var found int64
	err := c.QueryRowContext(ctx, `SELECT v.id FROM document_versions v JOIN document_approvers a ON a.version_id = v.id
		WHERE v.id = ? AND v.document_id = ? AND a.account_id = ?`, versionID, id, viewer).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// SetSharing は共有先を変えます。不変条件: DOC-INV-3
func (s *Store) SetSharing(ctx context.Context, owner, id int64, audience string, accounts []int64, active func(int64) bool) error {
	if audience != audiencePrivate && audience != audienceEveryone && audience != audienceSelected {
		return InputError{"audience"}
	}
	if audience != audienceSelected {
		accounts = nil
	} else if len(accounts) == 0 {
		return InputError{"accounts"}
	}
	for _, a := range accounts {
		if a == owner || !active(a) {
			return InputError{"accounts"}
		}
	}
	return s.atomic(ctx, func(c *sql.Conn) error {
		if _, err := s.owned(ctx, c, owner, id); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, "UPDATE documents SET audience = ? WHERE id = ?", audience, id); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, "DELETE FROM document_shares WHERE document_id = ?", id); err != nil {
			return err
		}
		for _, a := range slices.Compact(slices.Sorted(slices.Values(accounts))) {
			if _, err := c.ExecContext(ctx, "INSERT INTO document_shares (document_id, account_id) VALUES (?, ?)", id, a); err != nil {
				return err
			}
		}
		return s.touch(ctx, c, id)
	})
}

// Delete は文書を、版・共有・既読・承認の記録ごと削除します。不変条件: DOC-INV-3
func (s *Store) Delete(ctx context.Context, owner, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM documents WHERE id = ? AND owner_account_id = ?", id, owner)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddFolder はフォルダを作ります。
func (s *Store) AddFolder(ctx context.Context, by int64, name string) error {
	name, err := form.Line(name, maxFolderRunes, true)
	if err != nil {
		return InputError{"name"}
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO document_folders (name, created_by, created_at) VALUES (?, ?, ?)", name, by, s.now().Unix())
	if uniqueViolation(err) {
		return InputError{"name_taken"}
	}
	return err
}

// RenameFolder はフォルダの名前を変えます。作った人かシステム管理者だけが変えられます。
func (s *Store) RenameFolder(ctx context.Context, by int64, admin bool, id int64, name string) error {
	name, err := form.Line(name, maxFolderRunes, true)
	if err != nil {
		return InputError{"name"}
	}
	res, err := s.db.ExecContext(ctx, "UPDATE document_folders SET name = ? WHERE id = ? AND (created_by = ? OR ?)", name, id, by, admin)
	if uniqueViolation(err) {
		return InputError{"name_taken"}
	} else if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFolder はフォルダを削除します。文書が入っていれば ErrConflict です（読めない文書でも数える）。
func (s *Store) DeleteFolder(ctx context.Context, by int64, admin bool, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM document_folders WHERE id = ? AND (created_by = ? OR ?)", id, by, admin)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return ErrConflict
		}
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TransferOwner は、有効でない所有者の文書を、別のアカウントへ一括で移します。
// 書き込みの権利を得てから inactive を確かめるので、確かめた後に所有者が再開されることはありません。
// 承認待ちの版は取り下げます（新しい所有者が承認者に含まれると、本人が承認できてしまうため）。
// 不変条件: DOC-INV-7、DOC-INV-9
func (s *Store) TransferOwner(ctx context.Context, by, from, to int64, usable func(from, to int64) (bool, error)) (int, error) {
	if to == by || to == from {
		return 0, InputError{"to"}
	}
	var count int
	err := s.atomic(ctx, func(c *sql.Conn) error {
		ok, err := usable(from, to)
		if err != nil {
			return err
		}
		if !ok {
			return ErrConflict
		}
		rows, err := c.QueryContext(ctx, `SELECT v.document_id, v.id FROM document_versions v JOIN documents d ON d.id = v.document_id
			WHERE d.owner_account_id = ? AND v.state = 'pending'`, from)
		if err != nil {
			return err
		}
		var pending [][2]int64
		for rows.Next() {
			var p [2]int64
			if err := rows.Scan(&p[0], &p[1]); err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, p)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, p := range pending {
			if err := s.closePending(ctx, c, p[0], p[1], stateWithdrawn); err != nil {
				return err
			}
		}
		res, err := c.ExecContext(ctx, "UPDATE documents SET owner_account_id = ? WHERE owner_account_id = ?", to, from)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		count = int(n)
		// 新しい所有者は、自分の文書の共有先から外す
		if _, err := c.ExecContext(ctx, "DELETE FROM document_shares WHERE account_id = ? AND document_id IN (SELECT id FROM documents WHERE owner_account_id = ?)", to, to); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, "INSERT INTO document_transfers (from_account_id, to_account_id, by_account_id, count, transferred_at) VALUES (?, ?, ?, ?, ?)", from, to, by, count, s.now().Unix())
		return err
	})
	return count, err
}

// transferRetention は移管の記録の保持期間です。
const transferRetention = 3 * 365 * 24 * time.Hour

// Purge は保持期間を過ぎた移管の記録を消します。
func (s *Store) Purge(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM document_transfers WHERE transferred_at < ?", s.now().Add(-transferRetention).Unix())
	return err
}
