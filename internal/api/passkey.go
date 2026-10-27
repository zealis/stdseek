package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"voidxo/internal/auth"
	"voidxo/internal/store"
)

// passkeyBody 前端提交的完成请求：session 来自 begin，credential 是
// navigator.credentials 序列化后的结构（原样透传给 webauthn 库解析）。
type passkeyBody struct {
	Session    string          `json:"session"`
	Credential json.RawMessage `json:"credential"`
}

// originOf 取请求来源（scheme://host）。WebAuthn 的 RPID 与 origin 必须和它一致。
func originOf(r *http.Request) string {
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

// handlePasskeyRegisterBegin POST /api/auth/passkey/register/begin（需登录）
func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if s.Passkey == nil {
		writeErr(w, http.StatusNotImplemented, "通行密钥未启用")
		return
	}
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	opts, sid, err := s.Passkey.BeginRegistration(r.Context(), u.ID, originOf(r))
	if err != nil {
		// 已知原因直接透给用户（比如用了 IP 访问），不要把库的原始错误裹一层
		if errors.Is(err, auth.ErrPasskeyNeedsDomain) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, "无法开始注册通行密钥："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sid, "publicKey": opts})
}

// handlePasskeyRegisterFinish POST /api/auth/passkey/register/finish（需登录）
func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if s.Passkey == nil {
		writeErr(w, http.StatusNotImplemented, "通行密钥未启用")
		return
	}
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body passkeyBody
	if !decodeAuthBody(w, r, &body) {
		return
	}
	pk, err := s.Passkey.FinishRegistration(r.Context(), u.ID, body.Session, originOf(r), bytes.NewReader(body.Credential))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if pk == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkey": pk})
}

// handlePasskeyLoginBegin POST /api/auth/passkey/login/begin {email}
//
// 必须带邮箱：无用户名登录要求密钥是 resident key，而注册时只是「偏好」级别，
// 不能假设它一定成立。
func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.Passkey == nil {
		writeErr(w, http.StatusNotImplemented, "通行密钥未启用")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	opts, sid, err := s.Passkey.BeginLogin(r.Context(), req.Email, originOf(r))
	if err != nil {
		if errors.Is(err, auth.ErrPasskeyNeedsDomain) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		authErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sid, "publicKey": opts})
}

// handlePasskeyLoginFinish POST /api/auth/passkey/login/finish
func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if s.Passkey == nil {
		writeErr(w, http.StatusNotImplemented, "通行密钥未启用")
		return
	}
	var body passkeyBody
	if !decodeAuthBody(w, r, &body) {
		return
	}
	uid, err := s.Passkey.FinishLogin(r.Context(), body.Session, originOf(r), bytes.NewReader(body.Credential))
	if err != nil {
		authErr(w, err)
		return
	}
	// 签名验过了：直接签发会话（这就是通行密钥的意义——不需要密码）
	token, hash, err := auth.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "签发会话失败")
		return
	}
	expires := time.Now().Unix() + int64(auth.SessionTTL.Seconds())
	if err := s.st.CreateSession(r.Context(), hash, uid, clientIP(r), r.UserAgent(), expires); err != nil {
		writeErr(w, http.StatusInternalServerError, "签发会话失败")
		return
	}
	_ = s.st.UpdateLoginStamp(r.Context(), uid, clientIP(r))
	setSessionCookie(w, r, token)
	u, err := s.st.UserByID(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取账号失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// handleListPasskeys GET /api/auth/passkeys（需登录）
func (s *Server) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	rows, err := s.st.ListPasskeys(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取通行密钥失败")
		return
	}
	if rows == nil {
		rows = []store.Passkey{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// handleDeletePasskey POST /api/auth/passkey/delete {id}（需登录）
func (s *Server) handleDeletePasskey(w http.ResponseWriter, r *http.Request) {
	u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if !decodeAuthBody(w, r, &req) {
		return
	}
	// 两个约束：只能删自己的；删完必须还剩登录手段（密码始终可用，所以不强制）
	if err := s.st.DeletePasskey(r.Context(), req.ID, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
