package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/boilerplate/internal/domain"
)

var (
	ErrUserNotFound          = errors.New("user not found")
	ErrUserAlreadyExists     = errors.New("user with this email already exists")
	ErrSessionNotFound       = errors.New("session not found")
	ErrResetTokenNotFound    = errors.New("password reset token not found or expired")
	ErrResetTokenAlreadyUsed = errors.New("password reset token has already been consumed")
)

// UserRepository defines data access operations for user accounts.
type UserRepository interface {
	Create(ctx context.Context, user *domain.User) error
	FindByID(ctx context.Context, id string) (*domain.User, error)
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	Update(ctx context.Context, user *domain.User) error
	List(ctx context.Context, offset, limit int) ([]*domain.User, int64, error)
	Count(ctx context.Context) (int64, error)
}

// SessionRepository defines operations for user sessions.
type SessionRepository interface {
	CreateSession(ctx context.Context, session *domain.Session) error
	GetSession(ctx context.Context, sessionID string) (*domain.Session, error)
	DeleteSession(ctx context.Context, sessionID string) error
	DeleteUserSessions(ctx context.Context, userID string) error
}

// PasswordResetRepository defines operations for password reset tokens.
type PasswordResetRepository interface {
	CreateResetToken(ctx context.Context, token *domain.PasswordResetToken) error
	FindResetToken(ctx context.Context, tokenHash string) (*domain.PasswordResetToken, error)
	MarkResetTokenUsed(ctx context.Context, tokenHash string) error
	DeleteExpiredResetTokens(ctx context.Context) error
}

// MemoryStore is a thread-safe in-memory storage implementation for users, sessions, and reset tokens.
type MemoryStore struct {
	mu          sync.RWMutex
	users       map[string]*domain.User               // id -> user
	usersByMail map[string]string                     // lower(email) -> id
	sessions    map[string]*domain.Session            // id -> session
	resetTokens map[string]*domain.PasswordResetToken // tokenHash -> token
}

// NewMemoryStore constructs a fresh MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		users:       make(map[string]*domain.User),
		usersByMail: make(map[string]string),
		sessions:    make(map[string]*domain.Session),
		resetTokens: make(map[string]*domain.PasswordResetToken),
	}
}

// SeedInitialUsers populates default demo accounts with Argon2id hashed passwords.
func (s *MemoryStore) SeedInitialUsers(hasher *PasswordHasher) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	defaultPassword := "Password123!"
	hash, err := hasher.Hash(defaultPassword)
	if err != nil {
		return err
	}

	demoUsers := []*domain.User{
		{
			ID:           "usr_superadmin_01",
			Email:        "superadmin@example.com",
			Name:         "Super Administrator",
			PasswordHash: hash,
			Roles:        []string{domain.RoleSuperAdmin},
			Status:       domain.UserStatusActive,
			CreatedAt:    time.Now().UTC().Add(-24 * time.Hour),
			UpdatedAt:    time.Now().UTC().Add(-24 * time.Hour),
		},
		{
			ID:           "usr_admin_01",
			Email:        "admin@example.com",
			Name:         "Alice Admin",
			PasswordHash: hash,
			Roles:        []string{domain.RoleAdmin},
			Status:       domain.UserStatusActive,
			CreatedAt:    time.Now().UTC().Add(-12 * time.Hour),
			UpdatedAt:    time.Now().UTC().Add(-12 * time.Hour),
		},
		{
			ID:           "usr_manager_01",
			Email:        "manager@example.com",
			Name:         "Bob Manager",
			PasswordHash: hash,
			Roles:        []string{domain.RoleManager},
			Status:       domain.UserStatusActive,
			CreatedAt:    time.Now().UTC().Add(-6 * time.Hour),
			UpdatedAt:    time.Now().UTC().Add(-6 * time.Hour),
		},
		{
			ID:           "usr_user_01",
			Email:        "user@example.com",
			Name:         "Charlie User",
			PasswordHash: hash,
			Roles:        []string{domain.RoleUser},
			Status:       domain.UserStatusActive,
			CreatedAt:    time.Now().UTC().Add(-1 * time.Hour),
			UpdatedAt:    time.Now().UTC().Add(-1 * time.Hour),
		},
	}

	for _, u := range demoUsers {
		s.users[u.ID] = u
		s.usersByMail[strings.ToLower(u.Email)] = u.ID
	}
	return nil
}

// Create inserts a new user.
func (s *MemoryStore) Create(_ context.Context, u *domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := strings.ToLower(u.Email)
	if _, exists := s.usersByMail[key]; exists {
		return ErrUserAlreadyExists
	}

	cp := *u
	s.users[u.ID] = &cp
	s.usersByMail[key] = u.ID
	return nil
}

// FindByID returns a user by ID.
func (s *MemoryStore) FindByID(_ context.Context, id string) (*domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u, ok := s.users[id]
	if !ok {
		return nil, ErrUserNotFound
	}
	cp := *u
	return &cp, nil
}

// FindByEmail returns a user by email (case-insensitive).
func (s *MemoryStore) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.usersByMail[strings.ToLower(email)]
	if !ok {
		return nil, ErrUserNotFound
	}
	u := s.users[id]
	cp := *u
	return &cp, nil
}

// Update updates an existing user.
func (s *MemoryStore) Update(_ context.Context, u *domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	orig, ok := s.users[u.ID]
	if !ok {
		return ErrUserNotFound
	}

	oldMail := strings.ToLower(orig.Email)
	newMail := strings.ToLower(u.Email)
	if oldMail != newMail {
		if _, exists := s.usersByMail[newMail]; exists {
			return ErrUserAlreadyExists
		}
		delete(s.usersByMail, oldMail)
		s.usersByMail[newMail] = u.ID
	}

	cp := *u
	cp.UpdatedAt = time.Now().UTC()
	s.users[u.ID] = &cp
	return nil
}

// List returns a slice of users with pagination.
func (s *MemoryStore) List(_ context.Context, offset, limit int) ([]*domain.User, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := int64(len(s.users))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 10
	}

	res := make([]*domain.User, 0, limit)
	i := 0
	for _, u := range s.users {
		if i >= offset && len(res) < limit {
			cp := *u
			res = append(res, &cp)
		}
		i++
	}
	return res, total, nil
}

// Count returns the total number of users.
func (s *MemoryStore) Count(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int64(len(s.users)), nil
}

// CreateSession registers a new session.
func (s *MemoryStore) CreateSession(_ context.Context, sess *domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := *sess
	s.sessions[sess.ID] = &cp
	return nil
}

// GetSession retrieves an active session by ID.
func (s *MemoryStore) GetSession(_ context.Context, sessionID string) (*domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	if sess.IsExpired() {
		return nil, ErrSessionNotFound
	}
	cp := *sess
	return &cp, nil
}

// DeleteSession destroys a single session.
func (s *MemoryStore) DeleteSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, sessionID)
	return nil
}

// DeleteUserSessions invalidates all sessions belonging to a specific user (e.g. after password reset).
func (s *MemoryStore) DeleteUserSessions(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, sess := range s.sessions {
		if sess.UserID == userID {
			delete(s.sessions, id)
		}
	}
	return nil
}

// CreateResetToken saves a reset token hash.
func (s *MemoryStore) CreateResetToken(_ context.Context, token *domain.PasswordResetToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cp := *token
	s.resetTokens[token.TokenHash] = &cp
	return nil
}

// FindResetToken locates a reset token by hash.
func (s *MemoryStore) FindResetToken(_ context.Context, tokenHash string) (*domain.PasswordResetToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.resetTokens[tokenHash]
	if !ok || !t.IsValid() {
		return nil, ErrResetTokenNotFound
	}
	cp := *t
	return &cp, nil
}

// MarkResetTokenUsed consumes a reset token atomically.
func (s *MemoryStore) MarkResetTokenUsed(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.resetTokens[tokenHash]
	if !ok {
		return ErrResetTokenNotFound
	}
	if t.UsedAt != nil {
		return ErrResetTokenAlreadyUsed
	}
	now := time.Now().UTC()
	t.UsedAt = &now
	return nil
}

// DeleteExpiredResetTokens cleans expired records.
func (s *MemoryStore) DeleteExpiredResetTokens(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	for k, v := range s.resetTokens {
		if now.After(v.ExpiresAt) || v.UsedAt != nil {
			delete(s.resetTokens, k)
		}
	}
	return nil
}
