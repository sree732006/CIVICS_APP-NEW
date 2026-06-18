package auth

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type otpSession struct {
	hash      string
	expiresAt time.Time
	verified  bool
}

var otpStore sync.Map

type Repository struct {
	DB *pgxpool.Pool
}

func (r *Repository) SaveOTP(ctx context.Context, phone, hash string) error {
	session := otpSession{
		hash:      hash,
		expiresAt: time.Now().Add(5 * time.Minute),
		verified:  false,
	}
	otpStore.Store(phone, session)
	return nil
}

func (r *Repository) GetOTP(ctx context.Context, phone string) (string, error) {
	val, ok := otpStore.Load(phone)
	if !ok {
		return "", errors.New("otp not found")
	}
	session := val.(otpSession)
	if time.Now().After(session.expiresAt) {
		otpStore.Delete(phone)
		return "", errors.New("otp expired")
	}
	return session.hash, nil
}

func (r *Repository) MarkOTPVerified(ctx context.Context, phone string) error {
	val, ok := otpStore.Load(phone)
	if !ok {
		return errors.New("otp not found")
	}
	session := val.(otpSession)
	session.verified = true
	otpStore.Store(phone, session)
	return nil
}

func (r *Repository) IsOTPValid(ctx context.Context, phone string) (string, error) {
	val, ok := otpStore.Load(phone)
	if !ok {
		return "", errors.New("otp not found")
	}
	session := val.(otpSession)
	if session.verified || time.Now().After(session.expiresAt) {
		return "", errors.New("otp invalid or expired")
	}
	return session.hash, nil
}

func (r *Repository) GetValidOTPHash(ctx context.Context, phone string) (string, error) {
	return r.IsOTPValid(ctx, phone)
}

func (r *Repository) MarkOTPUsed(ctx context.Context, phone string) error {
	return r.MarkOTPVerified(ctx, phone)
}

func (r *Repository) IsOfficer(ctx context.Context, phone string) (bool, error) {
	// Check if ANY non-CITIZEN role exists for this phone number.
	// A phone may have multiple rows (one per role) due to the ON CONFLICT seeding.
	var exists bool
	err := r.DB.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM users
			WHERE phone_number = $1
			  AND role != 'CITIZEN'
		)`, phone,
	).Scan(&exists)
	if err != nil {
		log.Printf("❌ IsOfficer query error for %s: %v", phone, err)
		return false, nil
	}
	log.Printf("🔍 IsOfficer(%s) = %v", phone, exists)
	return exists, nil
}

func (r *Repository) GetUserByPhone(ctx context.Context, phone string) (string, string, error) {
	var userID string
	var role string

	err := r.DB.QueryRow(ctx, `
		SELECT id, role
		FROM users
		WHERE phone_number = $1
	`, phone).Scan(&userID, &role)

	if err != nil {
		return "", "", err
	}

	return userID, role, nil
}

func (r *Repository) CreateSession(ctx context.Context, jti, userID, role string, expiresAt time.Time) error {
	_, err := r.DB.Exec(ctx, `
		INSERT INTO user_sessions (jti, user_id, role, expires_at)
		VALUES ($1, $2, $3, $4)
	`, jti, userID, role, expiresAt)
	return err
}

func (r *Repository) InvalidateSession(ctx context.Context, jti string) error {
	_, err := r.DB.Exec(ctx, `
		DELETE FROM user_sessions WHERE jti = $1
	`, jti)
	return err
}
