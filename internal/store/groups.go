package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

type Group struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	InviteCode string `json:"invite_code,omitempty"`
}

func (s *Store) Groups(ctx context.Context, user int64) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id,g.name,m.role,CASE WHEN m.role IN ('owner','admin') THEN g.invite_code ELSE '' END FROM groups g JOIN memberships m ON m.group_id=g.id WHERE m.user_id=? AND m.banned=0 ORDER BY g.created_at,g.id`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		var g Group
		if err = rows.Scan(&g.ID, &g.Name, &g.Role, &g.InviteCode); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
func (s *Store) CreateGroup(ctx context.Context, user int64, name string) (Group, error) {
	if !s.IsAdmin(user) {
		return Group{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 60 {
		return Group{}, ErrInvalid
	}
	g := Group{ID: randomID(), Name: name, Role: "owner", InviteCode: randomID()}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO groups(id,name,invite_code,created_at) VALUES(?,?,?,?)`, g.ID, g.Name, g.InviteCode, s.now()); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO memberships(group_id,user_id,role) VALUES(?,?,'owner')`, g.ID, user)
		return err
	})
	return g, err
}
func (s *Store) JoinGroup(ctx context.Context, user int64, code string) (Group, error) {
	var g Group
	if len(code) != 24 {
		return g, ErrInvalid
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT id,name FROM groups WHERE invite_code=?`, code).Scan(&g.ID, &g.Name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var banned bool
		err := tx.QueryRow(`SELECT role,banned FROM memberships WHERE group_id=? AND user_id=?`, g.ID, user).Scan(&g.Role, &banned)
		if err == nil {
			if banned {
				return ErrForbidden
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		g.Role = "member"
		_, err = tx.Exec(`INSERT INTO memberships(group_id,user_id) VALUES(?,?)`, g.ID, user)
		return err
	})
	return g, err
}
func (s *Store) RotateInvite(ctx context.Context, user int64, group string) (string, error) {
	code := randomID()
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		role, err := requireMember(tx, user, group)
		if err != nil {
			return err
		}
		if role == "member" {
			return ErrForbidden
		}
		_, err = tx.Exec(`UPDATE groups SET invite_code=? WHERE id=?`, code, group)
		return err
	})
	return code, err
}

// ManageMember also allows re-admission of someone previously excluded; invite links do not.
func (s *Store) ManageMember(ctx context.Context, actor int64, group string, target int64, action string) error {
	if actor == target {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		role, err := requireMember(tx, actor, group)
		if err != nil {
			return err
		}
		if role == "member" {
			return ErrForbidden
		}
		var targetRole string
		err = tx.QueryRow(`SELECT role FROM memberships WHERE group_id=? AND user_id=?`, group, target).Scan(&targetRole)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if targetRole == "owner" || (targetRole == "admin" && role != "owner") {
			return ErrForbidden
		}
		switch action {
		case "add":
			var exists int
			if err := tx.QueryRow(`SELECT 1 FROM users WHERE id=?`, target).Scan(&exists); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			_, err = tx.Exec(`INSERT INTO memberships(group_id,user_id) VALUES(?,?) ON CONFLICT(group_id,user_id) DO UPDATE SET banned=0`, group, target)
		case "remove":
			if targetRole == "" {
				return ErrNotFound
			}
			if err = s.leaveStatus(tx, target, group, s.now()); err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE memberships SET banned=1,role='member' WHERE group_id=? AND user_id=?`, group, target)
			if err != nil {
				return err
			}
			// Invalidate any old session card in the excluded person's private chat.
			if err = s.refreshCards(tx, group); err != nil {
				return err
			}
		case "admin", "member":
			if role != "owner" {
				return ErrForbidden
			}
			return affected(tx.Exec(`UPDATE memberships SET role=? WHERE group_id=? AND user_id=? AND banned=0`, action, group, target))
		default:
			return ErrInvalid
		}
		return err
	})
}
func (s *Store) LeaveGroup(ctx context.Context, user int64, group string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		role, err := requireMember(tx, user, group)
		if err != nil {
			return err
		}
		if role == "owner" {
			return ErrForbidden
		}
		if err = s.leaveStatus(tx, user, group, s.now()); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM memberships WHERE group_id=? AND user_id=?`, group, user); err != nil {
			return err
		}
		return s.refreshCards(tx, group)
	})
}
