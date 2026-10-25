package app

import (
	"errors"
	"net/http"
)

func (a *App) changePassword(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		apiError(w, 400, "invalid_request", err.Error())
		return
	}
	if !a.allowPasswordAttempt(w, r) {
		return
	}
	var oldHash string
	if err := a.DB.QueryRowContext(r.Context(), "SELECT password FROM users WHERE id=?", u.ID).Scan(&oldHash); err != nil {
		apiError(w, 503, "database", "Password change temporarily unavailable")
		return
	}
	select {
	case passwordSlots <- struct{}{}:
	default:
		apiError(w, 429, "busy", "Password verification budget exhausted; retry later")
		return
	}
	valid := checkPassword(oldHash, in.CurrentPassword, a.Config.Password)
	<-passwordSlots
	if !valid {
		apiError(w, 400, "invalid_credentials", "Current password is incorrect")
		return
	}
	if in.NewPassword == in.CurrentPassword {
		apiError(w, 400, "password_policy", "New password must differ from the current password")
		return
	}
	hash, err := HashPassword(in.NewPassword, a.Config.Password)
	if err != nil {
		if errors.Is(err, errAuthBusy) {
			apiError(w, 429, "busy", err.Error())
		} else {
			apiError(w, 400, "password_policy", err.Error())
		}
		return
	}
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		apiError(w, 503, "database", "Password change temporarily unavailable")
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), "UPDATE users SET password=?,must_change_password=0 WHERE id=? AND password=?", hash, u.ID, oldHash)
	if err == nil {
		var changed int64
		changed, err = result.RowsAffected()
		if err == nil && changed != 1 {
			apiError(w, 409, "password_changed", "Password changed; try again")
			return
		}
	}
	cookie, _ := r.Cookie("archika_session")
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "DELETE FROM sessions WHERE user_id=? AND token!=?", u.ID, tokenHash(cookie.Value))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		apiError(w, 503, "database", "Password change temporarily unavailable")
		return
	}
	u.MustChangePassword = false
	jsonResponse(w, 200, u)
}
