package cms

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

var testRP = rpConfig{ID: "localhost", Origin: "http://localhost:8080"}

// fakeAuthenticator は、テスト内で生成した鍵で署名する認証器です。
type fakeAuthenticator struct {
	t      *testing.T
	alg    int64
	signer crypto.Signer
	credID []byte
	count  uint32
	flags  byte
}

func newFakeAuthenticator(t *testing.T, alg int64) *fakeAuthenticator {
	t.Helper()
	var signer crypto.Signer
	var err error
	switch alg {
	case algES256:
		signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case algEdDSA:
		_, signer, err = ed25519.GenerateKey(rand.Reader)
	case algRS256:
		signer, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &fakeAuthenticator{t: t, alg: alg, signer: signer, credID: id, flags: flagUP | flagUV}
}

func (a *fakeAuthenticator) spki() []byte {
	der, err := x509.MarshalPKIXPublicKey(a.signer.Public())
	if err != nil {
		a.t.Fatal(err)
	}
	return der
}

func authData(rpID string, flags byte, count uint32, credID []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	data := append([]byte{}, h[:]...)
	data = append(data, flags)
	data = binary.BigEndian.AppendUint32(data, count)
	if credID != nil {
		data = append(data, make([]byte, 16)...) // AAGUID
		data = binary.BigEndian.AppendUint16(data, uint16(len(credID)))
		data = append(data, credID...)
		data = append(data, 0xa0) // COSE鍵の代わり（解析しない）
	}
	return data
}

func clientDataJSON(typ, challenge, origin string, crossOrigin bool) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": crossOrigin})
	return b
}

func (a *fakeAuthenticator) register(t *testing.T, ch *challenges) registrationResponse {
	t.Helper()
	challenge, err := ch.issue(purposeRegister)
	if err != nil {
		t.Fatal(err)
	}
	return registrationResponse{
		RawID:              a.credID,
		ClientDataJSON:     clientDataJSON("webauthn.create", challenge, testRP.Origin, false),
		AuthenticatorData:  authData(testRP.ID, a.flags|flagAT, a.count, a.credID),
		PublicKey:          a.spki(),
		PublicKeyAlgorithm: a.alg,
	}
}

func (a *fakeAuthenticator) sign(authData, cd []byte) []byte {
	cdHash := sha256.Sum256(cd)
	msg := append(append([]byte{}, authData...), cdHash[:]...)
	var sig []byte
	var err error
	if a.alg == algEdDSA {
		sig, err = a.signer.Sign(rand.Reader, msg, crypto.Hash(0))
	} else {
		digest := sha256.Sum256(msg)
		sig, err = a.signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	}
	if err != nil {
		a.t.Fatal(err)
	}
	return sig
}

func (a *fakeAuthenticator) assert(t *testing.T, ch *challenges, userID []byte) assertionResponse {
	t.Helper()
	challenge, err := ch.issue(purposeLogin)
	if err != nil {
		t.Fatal(err)
	}
	a.count++
	cd := clientDataJSON("webauthn.get", challenge, testRP.Origin, false)
	ad := authData(testRP.ID, a.flags, a.count, nil)
	return assertionResponse{RawID: a.credID, ClientDataJSON: cd, AuthenticatorData: ad, Signature: a.sign(ad, cd), UserHandle: userID}
}

func (a *fakeAuthenticator) stored(count uint32) storedCredential {
	return storedCredential{ID: a.credID, PublicKey: a.spki(), Alg: a.alg, SignCount: count}
}

var testUserID = []byte("0123456789abcdef")

func TestWebAuthn_RegisterAndLoginAllAlgorithms(t *testing.T) {
	for name, alg := range map[string]int64{"ES256": algES256, "EdDSA": algEdDSA, "RS256": algRS256} {
		t.Run(name, func(t *testing.T) {
			ch := newChallenges(time.Now)
			a := newFakeAuthenticator(t, alg)
			cred, err := verifyRegistration(testRP, ch, a.register(t, ch))
			if err != nil {
				t.Fatalf("registration: %v", err)
			}
			if string(cred.ID) != string(a.credID) || cred.Alg != alg {
				t.Fatal("registered credential mismatch")
			}
			stored := storedCredential{ID: cred.ID, PublicKey: cred.PublicKey, Alg: cred.Alg, SignCount: cred.SignCount}
			for i := 0; i < 2; i++ {
				count, err := verifyAssertion(testRP, ch, a.assert(t, ch, testUserID), stored, testUserID)
				if err != nil {
					t.Fatalf("login %d: %v", i, err)
				}
				stored.SignCount = count
			}
		})
	}
}

func TestWebAuthn_RegistrationRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse)
		want   error
	}{
		"wrong type": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			c, _ := ch.issue(purposeRegister)
			r.ClientDataJSON = clientDataJSON("webauthn.get", c, testRP.Origin, false)
		}, ErrClientDataInvalid},
		"challenge for login": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			c, _ := ch.issue(purposeLogin)
			r.ClientDataJSON = clientDataJSON("webauthn.create", c, testRP.Origin, false)
		}, ErrChallengeInvalid},
		"forged challenge": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			other := newChallenges(time.Now)
			c, _ := other.issue(purposeRegister)
			r.ClientDataJSON = clientDataJSON("webauthn.create", c, testRP.Origin, false)
		}, ErrChallengeInvalid},
		"malformed challenge": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.ClientDataJSON = clientDataJSON("webauthn.create", "not-base64!", testRP.Origin, false)
		}, ErrChallengeInvalid},
		"origin mismatch": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			c, _ := ch.issue(purposeRegister)
			r.ClientDataJSON = clientDataJSON("webauthn.create", c, "http://localhost:8081", false)
		}, ErrOriginMismatch},
		"cross origin": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			c, _ := ch.issue(purposeRegister)
			r.ClientDataJSON = clientDataJSON("webauthn.create", c, testRP.Origin, true)
		}, ErrOriginMismatch},
		"invalid json": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.ClientDataJSON = []byte("{")
		}, ErrClientDataInvalid},
		"rp id mismatch": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.AuthenticatorData = authData("example.com", a.flags|flagAT, 0, a.credID)
		}, ErrRPIDMismatch},
		"no user verification": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.AuthenticatorData = authData(testRP.ID, flagUP|flagAT, 0, a.credID)
		}, ErrUserNotVerified},
		"no user presence": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.AuthenticatorData = authData(testRP.ID, flagUV|flagAT, 0, a.credID)
		}, ErrUserNotVerified},
		"backup eligible (synced passkey)": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.AuthenticatorData = authData(testRP.ID, flagUP|flagUV|flagBE|flagAT, 0, a.credID)
		}, ErrSyncedCredential},
		"missing attested data": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.AuthenticatorData = authData(testRP.ID, flagUP|flagUV, 0, nil)
		}, ErrAuthDataInvalid},
		"truncated auth data": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.AuthenticatorData = r.AuthenticatorData[:36]
		}, ErrAuthDataInvalid},
		"credential id length overflow": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			binary.BigEndian.PutUint16(r.AuthenticatorData[53:55], 0xffff)
		}, ErrCredentialIDInvalid},
		"raw id differs": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.RawID = []byte("other-id")
		}, ErrCredentialIDInvalid},
		"algorithm mismatch": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.PublicKeyAlgorithm = algRS256
		}, ErrPublicKeyInvalid},
		"unsupported algorithm": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.PublicKeyAlgorithm = -35
		}, ErrPublicKeyInvalid},
		"P-384 key": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			k, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
			r.PublicKey, _ = x509.MarshalPKIXPublicKey(k.Public())
		}, ErrPublicKeyInvalid},
		"RSA 1024": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			k, _ := rsa.GenerateKey(rand.Reader, 1024)
			r.PublicKey, _ = x509.MarshalPKIXPublicKey(k.Public())
			r.PublicKeyAlgorithm = algRS256
		}, ErrPublicKeyInvalid},
		"garbage key": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *registrationResponse) {
			r.PublicKey = []byte("not a key")
		}, ErrPublicKeyInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ch := newChallenges(time.Now)
			a := newFakeAuthenticator(t, algES256)
			r := a.register(t, ch)
			c.mutate(t, ch, a, &r)
			if _, err := verifyRegistration(testRP, ch, r); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestWebAuthn_LoginRejects(t *testing.T) {
	cases := map[string]struct {
		mutate func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential)
		want   error
	}{
		"bad signature": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			r.Signature[len(r.Signature)-1] ^= 0xff
		}, ErrSignatureInvalid},
		"signed by another key": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			other := newFakeAuthenticator(t, algES256)
			r.Signature = other.sign(r.AuthenticatorData, r.ClientDataJSON)
		}, ErrSignatureInvalid},
		"tampered client data after signing": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			c, _ := ch.issue(purposeLogin)
			r.ClientDataJSON = clientDataJSON("webauthn.get", c, testRP.Origin, false)
		}, ErrSignatureInvalid},
		"tampered flags after signing": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			r.AuthenticatorData[32] |= 0x02
		}, ErrSignatureInvalid},
		"wrong type": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			c, _ := ch.issue(purposeLogin)
			r.ClientDataJSON = clientDataJSON("webauthn.create", c, testRP.Origin, false)
			r.Signature = a.sign(r.AuthenticatorData, r.ClientDataJSON)
		}, ErrClientDataInvalid},
		"registration challenge": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			c, _ := ch.issue(purposeRegister)
			r.ClientDataJSON = clientDataJSON("webauthn.get", c, testRP.Origin, false)
			r.Signature = a.sign(r.AuthenticatorData, r.ClientDataJSON)
		}, ErrChallengeInvalid},
		"origin mismatch": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			c, _ := ch.issue(purposeLogin)
			r.ClientDataJSON = clientDataJSON("webauthn.get", c, "https://evil.example", false)
			r.Signature = a.sign(r.AuthenticatorData, r.ClientDataJSON)
		}, ErrOriginMismatch},
		"rp id mismatch": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			r.AuthenticatorData = authData("example.com", a.flags, a.count, nil)
			r.Signature = a.sign(r.AuthenticatorData, r.ClientDataJSON)
		}, ErrRPIDMismatch},
		"no user verification": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			r.AuthenticatorData = authData(testRP.ID, flagUP, a.count, nil)
			r.Signature = a.sign(r.AuthenticatorData, r.ClientDataJSON)
		}, ErrUserNotVerified},
		"backup state set": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			r.AuthenticatorData = authData(testRP.ID, flagUP|flagUV|flagBS, a.count, nil)
			r.Signature = a.sign(r.AuthenticatorData, r.ClientDataJSON)
		}, ErrSyncedCredential},
		"user handle mismatch": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			r.UserHandle = []byte("someone-else")
		}, ErrUserHandleMismatch},
		"raw id differs from stored": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			r.RawID = []byte("other")
		}, ErrCredentialIDInvalid},
		"counter not increased": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			cred.SignCount = a.count
		}, ErrCloneSuspected},
		"counter reset to zero (stored 5, new 0)": {func(t *testing.T, ch *challenges, a *fakeAuthenticator, r *assertionResponse, cred *storedCredential) {
			cred.SignCount = 5
			r.AuthenticatorData = authData(testRP.ID, a.flags, 0, nil)
			r.Signature = a.sign(r.AuthenticatorData, r.ClientDataJSON)
		}, ErrCloneSuspected},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ch := newChallenges(time.Now)
			a := newFakeAuthenticator(t, algES256)
			cred := a.stored(0)
			r := a.assert(t, ch, testUserID)
			c.mutate(t, ch, a, &r, &cred)
			if _, err := verifyAssertion(testRP, ch, r, cred, testUserID); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestWebAuthn_CounterZeroAllowedWhenBothZero(t *testing.T) {
	ch := newChallenges(time.Now)
	a := newFakeAuthenticator(t, algES256)
	r := a.assert(t, ch, nil)
	r.AuthenticatorData = authData(testRP.ID, a.flags, 0, nil)
	r.Signature = a.sign(r.AuthenticatorData, r.ClientDataJSON)
	if count, err := verifyAssertion(testRP, ch, r, a.stored(0), testUserID); err != nil || count != 0 {
		t.Fatalf("authenticators without counters must be accepted: %v", err)
	}
}

// 失敗した検証はchallengeを使用済みにせず、成功したchallengeの再利用は拒否されること。
func TestWebAuthn_ReplayAndFailedAttemptsDoNotConsume(t *testing.T) {
	ch := newChallenges(time.Now)
	a := newFakeAuthenticator(t, algES256)
	r := a.assert(t, ch, testUserID)
	bad := r
	bad.Signature = append([]byte{}, r.Signature...)
	bad.Signature[0] ^= 0xff
	for i := 0; i < 3; i++ {
		if _, err := verifyAssertion(testRP, ch, bad, a.stored(0), testUserID); !errors.Is(err, ErrSignatureInvalid) {
			t.Fatal(err)
		}
	}
	if len(ch.used) != 0 {
		t.Fatal("failed attempts must not be recorded")
	}
	if _, err := verifyAssertion(testRP, ch, r, a.stored(0), testUserID); err != nil {
		t.Fatalf("valid assertion after failures: %v", err)
	}
	if _, err := verifyAssertion(testRP, ch, r, a.stored(0), testUserID); !errors.Is(err, ErrChallengeUsed) {
		t.Fatalf("replay must be rejected, got %v", err)
	}
}

// 同じ応答を並行に送っても、成功は1件だけであること。
func TestWebAuthn_ConcurrentReplay(t *testing.T) {
	ch := newChallenges(time.Now)
	a := newFakeAuthenticator(t, algES256)
	r := a.assert(t, ch, testUserID)
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := verifyAssertion(testRP, ch, r, a.stored(0), testUserID); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("expected exactly 1 success, got %d", successes)
	}
}

// 期限切れのchallengeは拒否され、使用済みの記録も期限後に消えること。
// 大量に発行しても状態を持たないため、正当な認証を妨げないこと。
func TestWebAuthn_ChallengeExpiryAndStatelessIssue(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	ch := newChallenges(func() time.Time { return clock })
	for i := 0; i < 10000; i++ {
		if _, err := ch.issue(purposeLogin); err != nil {
			t.Fatal(err)
		}
	}
	if len(ch.used) != 0 {
		t.Fatal("issuing must not store state")
	}
	a := newFakeAuthenticator(t, algES256)
	r := a.assert(t, ch, testUserID)

	clock = clock.Add(challengeTTL)
	if _, err := verifyAssertion(testRP, ch, r, a.stored(0), testUserID); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("expired challenge must be rejected, got %v", err)
	}

	clock = time.Unix(1_700_000_000, 0)
	if _, err := verifyAssertion(testRP, ch, r, a.stored(0), testUserID); err != nil {
		t.Fatal(err)
	}
	if len(ch.used) != 1 {
		t.Fatal("successful challenge must be recorded")
	}
	clock = clock.Add(challengeTTL + time.Second)
	r2 := a.assert(t, ch, testUserID)
	if _, err := verifyAssertion(testRP, ch, r2, a.stored(a.count-1), testUserID); err != nil {
		t.Fatal(err)
	}
	if len(ch.used) != 1 {
		t.Fatal("expired used entries must be pruned")
	}
}

func TestWebAuthn_ChallengeEncoding(t *testing.T) {
	ch := newChallenges(time.Now)
	c, err := ch.issue(purposeLogin)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || len(raw) != challengeLen {
		t.Fatal("challenge must be unpadded base64url of the fixed length")
	}
	// 1ビットでも改変すれば拒否される
	raw[10] ^= 0x01
	if _, err := ch.check(base64.RawURLEncoding.EncodeToString(raw), purposeLogin); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatal("tampered challenge must be rejected")
	}
	if _, err := ch.check(c+"=", purposeLogin); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatal("padded challenge must be rejected")
	}
}
