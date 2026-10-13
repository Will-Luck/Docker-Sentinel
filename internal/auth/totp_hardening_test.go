package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

type memPending struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *memPending) SavePendingTOTP(token, userID string, _ time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]string{}
	}
	p.m[token] = userID
	return nil
}
func (p *memPending) GetPendingTOTP(token string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id, ok := p.m[token]; ok {
		return id, nil
	}
	return "", errors.New("not found")
}
func (p *memPending) DeletePendingTOTP(token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, token)
	return nil
}

const totpPassword = "correct-horse-battery"

// totpUser creates an account with 2FA on and returns the service and secret.
func totpUser(t *testing.T) (*Service, string) {
	t.Helper()
	svc := newTestService(true)
	svc.PendingTOTP = &memPending{}
	key, err := GenerateTOTPSecret("alice")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := HashPassword(totpPassword)
	if err := svc.Users.CreateUser(User{ID: "u1", Username: "alice", PasswordHash: hash, RoleID: RoleAdminID, TOTPEnabled: true, TOTPSecret: key.Secret()}); err != nil {
		t.Fatal(err)
	}
	return svc, key.Secret()
}

func pendingFor(t *testing.T, svc *Service, ip string) string {
	t.Helper()
	_, _, err := svc.Login(context.Background(), "alice", totpPassword, ip, "ua")
	var need *ErrTOTPRequired
	if !errors.As(err, &need) {
		t.Fatalf("login = %v, want a pending 2FA token", err)
	}
	return need.PendingToken
}

// A code that has been accepted once cannot be used again in its window.
func TestVerifyTOTP_CodeCannotBeReplayed(t *testing.T) {
	svc, secret := totpUser(t)
	code, _ := totp.GenerateCode(secret, time.Now())

	if _, err := svc.VerifyTOTP(context.Background(), pendingFor(t, svc, "10.0.0.1"), code, "10.0.0.1", "ua"); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := svc.VerifyTOTP(context.Background(), pendingFor(t, svc, "10.0.0.2"), code, "10.0.0.2", "ua"); !errors.Is(err, ErrTOTPInvalidCode) {
		t.Errorf("replay = %v, want ErrTOTPInvalidCode", err)
	}
}

// A password success must not hand the IP a fresh budget of 2FA guesses.
func TestLogin_PasswordSuccessDoesNotResetTheTOTPThrottle(t *testing.T) {
	svc, secret := totpUser(t)
	ip := "10.0.0.5"
	wrong := func() {
		_, _ = svc.VerifyTOTP(context.Background(), pendingFor(t, svc, ip), "000000", ip, "ua")
	}
	for i := 0; i < maxLoginAttempts; i++ {
		wrong()
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.VerifyTOTP(context.Background(), "x", code, ip, "ua"); !errors.Is(err, ErrRateLimited) {
		t.Errorf("after %d wrong codes (with a password login between each) the IP is not throttled: %v", maxLoginAttempts, err)
	}
}

// Wrong codes use up a pending token, so a password holder cannot guess on it
// indefinitely.
func TestVerifyTOTP_PendingTokenDiesAfterTooManyWrongCodes(t *testing.T) {
	svc, secret := totpUser(t)
	token := pendingFor(t, svc, "10.0.0.9")
	for i := 0; i < maxTOTPAttempts; i++ {
		_, _ = svc.VerifyTOTP(context.Background(), token, "000000", "10.1.0."+string(rune('1'+i)), "ua")
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := svc.VerifyTOTP(context.Background(), token, code, "10.2.0.1", "ua"); !errors.Is(err, ErrTOTPInvalidToken) {
		t.Errorf("right code on a used-up token = %v, want ErrTOTPInvalidToken", err)
	}
}
