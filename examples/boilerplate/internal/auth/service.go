package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/oarkflow/ref/examples/boilerplate/internal/domain"
)

var (
	ErrInvalidEmail       = errors.New("invalid email address format")
	ErrWeakPassword       = errors.New("password must be at least 8 characters long and contain both letters and digits")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrAccountInactive    = errors.New("this account is suspended or pending activation")
	ErrTokenExpired       = errors.New("reset token is invalid or has expired")
	ErrCurrentPassWrong   = errors.New("current password is incorrect")
)

// Service defines auth business logic operations.
type Service struct {
	userRepo       UserRepository
	sessionRepo    SessionRepository
	resetRepo      PasswordResetRepository
	hasher         *PasswordHasher
	sessionTTL     time.Duration
	resetTokenTTL  time.Duration
}

// ServiceConfig configures auth behaviors.
type ServiceConfig struct {
	SessionTTL    time.Duration
	ResetTokenTTL time.Duration
}

// NewService constructs a new auth application service.
func NewService(
	userRepo UserRepository,
	sessionRepo SessionRepository,
	resetRepo PasswordResetRepository,
	hasher *PasswordHasher,
	cfg ...ServiceConfig,
) *Service {
	sessionTTL := 24 * time.Hour
	resetTTL := 15 * time.Minute
	if len(cfg) > 0 {
		if cfg[0].SessionTTL > 0 {
			sessionTTL = cfg[0].SessionTTL
		}
		if cfg[0].ResetTokenTTL > 0 {
			resetTTL = cfg[0].ResetTokenTTL
		}
	}
	return &Service{
		userRepo:      userRepo,
		sessionRepo:   sessionRepo,
		resetRepo:     resetRepo,
		hasher:        hasher,
		sessionTTL:    sessionTTL,
		resetTokenTTL: resetTTL,
	}
}

// RegisterInput carries parameters for user signup.
type RegisterInput struct {
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Password string   `json:"password"`
	Roles    []string `json:"roles,omitempty"`
}

// LoginInput carries credentials for user login.
type LoginInput struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// ForgotPasswordInput carries email for reset requests.
type ForgotPasswordInput struct {
	Email string `json:"email"`
}

// ResetPasswordInput carries the reset token and new desired password.
type ResetPasswordInput struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ChangePasswordInput carries parameters for an authenticated password update.
type ChangePasswordInput struct {
	UserID          string `json:"user_id"`
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// Register validates, hashes with Argon2id, and creates a new user.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*domain.User, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if err := validateEmail(email); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("name cannot be empty")
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}

	// Hash with Argon2id
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	roles := in.Roles
	if len(roles) == 0 {
		roles = []string{domain.RoleUser}
	}

	userID := fmt.Sprintf("usr_%d", time.Now().UnixNano())
	now := time.Now().UTC()

	user := &domain.User{
		ID:           userID,
		Email:        email,
		Name:         name,
		PasswordHash: hash,
		Roles:        roles,
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		return nil, err
	}

	return user, nil
}

// Login validates credentials with Argon2id and mints an active session.
func (s *Service) Login(ctx context.Context, in LoginInput) (*domain.User, *domain.Session, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if email == "" || in.Password == "" {
		return nil, nil, ErrInvalidCredentials
	}

	user, err := s.userRepo.FindByEmail(ctx, email)
	if err != nil {
		// Timing mitigation: run dummy verification
		s.hasher.DummyVerify(in.Password)
		return nil, nil, ErrInvalidCredentials
	}

	if user.Status != domain.UserStatusActive {
		return nil, nil, ErrAccountInactive
	}

	// Verify using Argon2id (timing-safe)
	match, err := s.hasher.Verify(in.Password, user.PasswordHash)
	if err != nil || !match {
		return nil, nil, ErrInvalidCredentials
	}

	// Mint new session ID
	sessID, err := GenerateRandomToken(24)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now().UTC()
	sess := &domain.Session{
		ID:        sessID,
		UserID:    user.ID,
		ExpiresAt: now.Add(s.sessionTTL),
		CreatedAt: now,
		IP:        in.IP,
		UserAgent: in.UserAgent,
	}

	if err := s.sessionRepo.CreateSession(ctx, sess); err != nil {
		return nil, nil, err
	}

	return user, sess, nil
}

// Logout destroys the current session.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return s.sessionRepo.DeleteSession(ctx, sessionID)
}

// ForgotPassword generates a secure one-time reset token and saves its hash.
// Returns the plaintext token for notification delivery (email, SMS).
func (s *Service) ForgotPassword(ctx context.Context, in ForgotPasswordInput) (string, error) {
	email := strings.TrimSpace(strings.ToLower(in.Email))
	if email == "" {
		return "", ErrInvalidEmail
	}

	user, err := s.userRepo.FindByEmail(ctx, email)
	if err != nil {
		// To prevent account enumeration, return success-like token or nil error
		dummyToken, _ := GenerateRandomToken(32)
		return dummyToken, nil
	}

	plainToken, err := GenerateRandomToken(32)
	if err != nil {
		return "", err
	}

	tokenHash := HashToken(plainToken)
	now := time.Now().UTC()

	record := &domain.PasswordResetToken{
		TokenHash: tokenHash,
		UserID:    user.ID,
		ExpiresAt: now.Add(s.resetTokenTTL),
		CreatedAt: now,
	}

	if err := s.resetRepo.CreateResetToken(ctx, record); err != nil {
		return "", err
	}

	return plainToken, nil
}

// ResetPassword consumes the token and updates the user's password using Argon2id.
func (s *Service) ResetPassword(ctx context.Context, in ResetPasswordInput) error {
	if in.Token == "" {
		return ErrTokenExpired
	}
	if err := validatePassword(in.NewPassword); err != nil {
		return err
	}

	tokenHash := HashToken(in.Token)
	record, err := s.resetRepo.FindResetToken(ctx, tokenHash)
	if err != nil {
		return ErrTokenExpired
	}

	// Mark token used
	if err := s.resetRepo.MarkResetTokenUsed(ctx, tokenHash); err != nil {
		return err
	}

	user, err := s.userRepo.FindByID(ctx, record.UserID)
	if err != nil {
		return ErrUserNotFound
	}

	// Hash new password with Argon2id
	newHash, err := s.hasher.Hash(in.NewPassword)
	if err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	user.PasswordHash = newHash
	if err := s.userRepo.Update(ctx, user); err != nil {
		return err
	}

	// Invalidate all existing sessions for this user for security
	_ = s.sessionRepo.DeleteUserSessions(ctx, user.ID)

	return nil
}

// ChangePassword allows an authenticated user to change their password.
func (s *Service) ChangePassword(ctx context.Context, in ChangePasswordInput) error {
	if in.UserID == "" {
		return errors.New("user ID is required")
	}
	if err := validatePassword(in.NewPassword); err != nil {
		return err
	}

	user, err := s.userRepo.FindByID(ctx, in.UserID)
	if err != nil {
		return ErrUserNotFound
	}

	match, err := s.hasher.Verify(in.CurrentPassword, user.PasswordHash)
	if err != nil || !match {
		return ErrCurrentPassWrong
	}

	newHash, err := s.hasher.Hash(in.NewPassword)
	if err != nil {
		return fmt.Errorf("failed to hash new password: %w", err)
	}

	user.PasswordHash = newHash
	return s.userRepo.Update(ctx, user)
}

// ValidateSession verifies a session ID and returns the associated user and session.
func (s *Service) ValidateSession(ctx context.Context, sessionID string) (*domain.User, *domain.Session, error) {
	if sessionID == "" {
		return nil, nil, ErrSessionNotFound
	}

	sess, err := s.sessionRepo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}

	user, err := s.userRepo.FindByID(ctx, sess.UserID)
	if err != nil {
		return nil, nil, ErrUserNotFound
	}

	if user.Status != domain.UserStatusActive {
		return nil, nil, ErrAccountInactive
	}

	return user, sess, nil
}

func validateEmail(email string) error {
	if len(email) < 3 || len(email) > 254 {
		return ErrInvalidEmail
	}
	_, err := mail.ParseAddress(email)
	if err != nil {
		return ErrInvalidEmail
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return ErrWeakPassword
	}
	var hasLetter, hasDigit bool
	for _, c := range password {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			hasLetter = true
		} else if c >= '0' && c <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return ErrWeakPassword
	}
	return nil
}
