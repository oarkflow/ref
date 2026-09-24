package auth

import (
	"net/url"
	"strings"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/boilerplate/internal/domain"
)

// Handler serves HTTP endpoints for authentication (Web + REST API).
type Handler struct {
	authService *Service
}

// NewHandler constructs an auth HTTP handler.
func NewHandler(authService *Service) *Handler {
	return &Handler{authService: authService}
}

func formValue(c fh.Ctx, key string) string {
	values, err := url.ParseQuery(string(c.Body()))
	if err != nil {
		return ""
	}
	return values.Get(key)
}

// ---------------------------------------------------------------------------
// Web HTML Handlers (SPL Templates)
// ---------------------------------------------------------------------------

// ShowLogin renders the login page.
func (h *Handler) ShowLogin(c fh.Ctx) error {
	// If already authenticated, redirect to dashboard
	if user, ok := c.Context().Value(domain.ContextKeyUser).(*domain.User); ok && user != nil {
		return c.Redirect("/dashboard")
	}

	redirect := c.Query("redirect", "/dashboard")
	return c.Render("pages/auth/login", map[string]any{
		"title":    "Sign In — Robust Auth Boilerplate",
		"redirect": redirect,
		"error":    c.Query("error", ""),
		"success":  c.Query("success", ""),
	}, "layouts/auth")
}

// HandleLogin processes the web login form submission.
func (h *Handler) HandleLogin(c fh.Ctx) error {
	email := formValue(c, "email")
	password := formValue(c, "password")
	redirect := formValue(c, "redirect")
	if redirect == "" {
		redirect = "/dashboard"
	}

	user, sess, err := h.authService.Login(c.Context(), LoginInput{
		Email:     email,
		Password:  password,
		IP:        c.IP(),
		UserAgent: c.Get("User-Agent"),
	})

	if err != nil {
		return c.Render("pages/auth/login", map[string]any{
			"title":    "Sign In — Robust Auth Boilerplate",
			"redirect": redirect,
			"error":    err.Error(),
			"email":    email,
		}, "layouts/auth")
	}

	// Set session cookie
	c.SetCookie(&fh.Cookie{
		Name:     SessionCookieName,
		Value:    sess.ID,
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		SameSite: fh.SameSiteLax,
		Path:     "/",
	})

	_ = user
	return c.Redirect(redirect)
}

// ShowRegister renders the registration page.
func (h *Handler) ShowRegister(c fh.Ctx) error {
	if user, ok := c.Context().Value(domain.ContextKeyUser).(*domain.User); ok && user != nil {
		return c.Redirect("/dashboard")
	}

	return c.Render("pages/auth/register", map[string]any{
		"title":   "Create Account — Robust Auth Boilerplate",
		"error":   c.Query("error", ""),
		"success": c.Query("success", ""),
	}, "layouts/auth")
}

// HandleRegister processes the web registration form.
func (h *Handler) HandleRegister(c fh.Ctx) error {
	name := formValue(c, "name")
	email := formValue(c, "email")
	password := formValue(c, "password")
	role := formValue(c, "role")
	if role == "" {
		role = domain.RoleUser
	}

	_, err := h.authService.Register(c.Context(), RegisterInput{
		Name:     name,
		Email:    email,
		Password: password,
		Roles:    []string{role},
	})

	if err != nil {
		return c.Render("pages/auth/register", map[string]any{
			"title": "Create Account — Robust Auth Boilerplate",
			"error": err.Error(),
			"name":  name,
			"email": email,
			"role":  role,
		}, "layouts/auth")
	}

	return c.Redirect("/login?success=" + "Account created successfully! You can now log in.")
}

// ShowForgotPassword renders the password recovery request page.
func (h *Handler) ShowForgotPassword(c fh.Ctx) error {
	return c.Render("pages/auth/forgot_password", map[string]any{
		"title":   "Forgot Password — Robust Auth Boilerplate",
		"error":   c.Query("error", ""),
		"success": c.Query("success", ""),
	}, "layouts/auth")
}

// HandleForgotPassword processes the forgot password form submission.
func (h *Handler) HandleForgotPassword(c fh.Ctx) error {
	email := formValue(c, "email")

	token, err := h.authService.ForgotPassword(c.Context(), ForgotPasswordInput{Email: email})
	if err != nil {
		return c.Render("pages/auth/forgot_password", map[string]any{
			"title": "Forgot Password — Robust Auth Boilerplate",
			"error": err.Error(),
			"email": email,
		}, "layouts/auth")
	}

	// For demonstration, display the reset token directly on screen with a convenient link!
	return c.Render("pages/auth/forgot_password", map[string]any{
		"title":     "Forgot Password — Robust Auth Boilerplate",
		"success":   "If an account with that email exists, a password reset link has been dispatched.",
		"demoToken": token,
		"resetURL":  "/reset-password?token=" + token,
		"email":     email,
	}, "layouts/auth")
}

// ShowResetPassword renders the reset password form with the token.
func (h *Handler) ShowResetPassword(c fh.Ctx) error {
	token := c.Query("token", "")
	return c.Render("pages/auth/reset_password", map[string]any{
		"title": "Reset Password — Robust Auth Boilerplate",
		"token": token,
		"error": c.Query("error", ""),
	}, "layouts/auth")
}

// HandleResetPassword processes the password reset form.
func (h *Handler) HandleResetPassword(c fh.Ctx) error {
	token := formValue(c, "token")
	newPassword := formValue(c, "new_password")
	confirmPassword := formValue(c, "confirm_password")

	if newPassword != confirmPassword {
		return c.Render("pages/auth/reset_password", map[string]any{
			"title": "Reset Password — Robust Auth Boilerplate",
			"token": token,
			"error": "Passwords do not match",
		}, "layouts/auth")
	}

	err := h.authService.ResetPassword(c.Context(), ResetPasswordInput{
		Token:       token,
		NewPassword: newPassword,
	})

	if err != nil {
		return c.Render("pages/auth/reset_password", map[string]any{
			"title": "Reset Password — Robust Auth Boilerplate",
			"token": token,
			"error": err.Error(),
		}, "layouts/auth")
	}

	return c.Redirect("/login?success=" + "Your password has been successfully reset. Please log in with your new password.")
}

// HandleLogout terminates the active session and clears the cookie.
func (h *Handler) HandleLogout(c fh.Ctx) error {
	cookie := c.GetCookie(SessionCookieName)
	if cookie != "" {
		_ = h.authService.Logout(c.Context(), cookie)
	}

	c.DelCookie(SessionCookieName)
	return c.Redirect("/login?success=" + "You have been logged out.")
}

// ---------------------------------------------------------------------------
// REST API Handlers (JSON)
// ---------------------------------------------------------------------------

// APILogin authenticates and returns user details and session token.
func (h *Handler) APILogin(c fh.Ctx) error {
	var in LoginInput
	if err := c.BindJSON(&in); err != nil {
		return c.Status(400).JSON(map[string]any{"error": "Invalid JSON payload"})
	}

	in.IP = c.IP()
	in.UserAgent = c.Get("User-Agent")

	user, sess, err := h.authService.Login(c.Context(), in)
	if err != nil {
		return c.Status(401).JSON(map[string]any{"error": err.Error()})
	}

	return c.Status(200).JSON(map[string]any{
		"message": "Login successful",
		"token":   sess.ID,
		"expires": sess.ExpiresAt,
		"user": map[string]any{
			"id":    user.ID,
			"email": user.Email,
			"name":  user.Name,
			"roles": user.Roles,
		},
	})
}

// APIRegister registers a new user.
func (h *Handler) APIRegister(c fh.Ctx) error {
	var in RegisterInput
	if err := c.BindJSON(&in); err != nil {
		return c.Status(400).JSON(map[string]any{"error": "Invalid JSON payload"})
	}

	user, err := h.authService.Register(c.Context(), in)
	if err != nil {
		return c.Status(400).JSON(map[string]any{"error": err.Error()})
	}

	return c.Status(201).JSON(map[string]any{
		"message": "User registered successfully",
		"user": map[string]any{
			"id":    user.ID,
			"email": user.Email,
			"name":  user.Name,
			"roles": user.Roles,
		},
	})
}

// APIForgotPassword generates a reset token.
func (h *Handler) APIForgotPassword(c fh.Ctx) error {
	var in ForgotPasswordInput
	if err := c.BindJSON(&in); err != nil {
		return c.Status(400).JSON(map[string]any{"error": "Invalid JSON payload"})
	}

	token, err := h.authService.ForgotPassword(c.Context(), in)
	if err != nil {
		return c.Status(400).JSON(map[string]any{"error": err.Error()})
	}

	return c.Status(200).JSON(map[string]any{
		"message": "Password reset token generated",
		"token":   token,
	})
}

// APIResetPassword consumes the token and updates the password.
func (h *Handler) APIResetPassword(c fh.Ctx) error {
	var in ResetPasswordInput
	if err := c.BindJSON(&in); err != nil {
		return c.Status(400).JSON(map[string]any{"error": "Invalid JSON payload"})
	}

	if err := h.authService.ResetPassword(c.Context(), in); err != nil {
		return c.Status(400).JSON(map[string]any{"error": err.Error()})
	}

	return c.Status(200).JSON(map[string]any{
		"message": "Password reset successfully",
	})
}

// APILogout ends session.
func (h *Handler) APILogout(c fh.Ctx) error {
	authHeader := c.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		token = c.GetCookie(SessionCookieName)
	}

	_ = h.authService.Logout(c.Context(), token)
	return c.Status(200).JSON(map[string]any{"message": "Logged out successfully"})
}

// APIMe returns the current authenticated identity.
func (h *Handler) APIMe(c fh.Ctx) error {
	principal, ok := c.Context().Value(domain.ContextKeyPrincipal).(*domain.Principal)
	if !ok || principal == nil {
		return c.Status(401).JSON(map[string]any{"error": "Unauthorized"})
	}

	return c.Status(200).JSON(map[string]any{
		"principal": principal,
	})
}
