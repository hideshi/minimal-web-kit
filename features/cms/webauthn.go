package cms

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// WebAuthnの検証を、標準ライブラリだけで行います。
// ブラウザの getPublicKey() / getAuthenticatorData() を前提とし、CBOR/COSEは解析しません。
// attestationは検証しません（attestation: "none"）。
//
// 検証の順序は、署名の検証に成功するまで状態（使用済みchallenge・署名カウンタ）を変更しないように並べています。

// 公開鍵のアルゴリズム（COSE Algorithm Identifier）
const (
	algES256 int64 = -7
	algEdDSA int64 = -8
	algRS256 int64 = -257
)

// authenticatorDataのフラグ
const (
	flagUP byte = 0x01 // User Present
	flagUV byte = 0x04 // User Verified
	flagBE byte = 0x08 // Backup Eligible（同期・バックアップ可能）
	flagBS byte = 0x10 // Backup State（同期・バックアップ済み）
	flagAT byte = 0x40 // Attested credential data included
)

const (
	maxCredentialIDLen = 1023
	challengeTTL       = 5 * time.Minute
	minRSABits         = 2048
)

// 拒否の理由。文言は固定で、値を含みません。
var (
	ErrChallengeInvalid    = errors.New("webauthn: challenge invalid")
	ErrChallengeUsed       = errors.New("webauthn: challenge already used")
	ErrClientDataInvalid   = errors.New("webauthn: client data invalid")
	ErrOriginMismatch      = errors.New("webauthn: origin mismatch")
	ErrAuthDataInvalid     = errors.New("webauthn: authenticator data invalid")
	ErrRPIDMismatch        = errors.New("webauthn: rp id mismatch")
	ErrUserNotVerified     = errors.New("webauthn: user presence or verification missing")
	ErrSyncedCredential    = errors.New("webauthn: synced or backup eligible credential")
	ErrCredentialIDInvalid = errors.New("webauthn: credential id invalid")
	ErrPublicKeyInvalid    = errors.New("webauthn: public key invalid or unsupported")
	ErrSignatureInvalid    = errors.New("webauthn: signature invalid")
	ErrUserHandleMismatch  = errors.New("webauthn: user handle mismatch")
	// ErrCloneSuspected は、署名は正しいが署名カウンタが進んでいないことを示します（認証器の複製の疑い）。
	ErrCloneSuspected = errors.New("webauthn: signature counter did not increase")
)

type challengePurpose byte

const (
	purposeRegister challengePurpose = 1
	purposeLogin    challengePurpose = 2
)

// challengeは「用途(1) | 有効期限のUNIX秒(8) | 乱数(32) | HMAC-SHA256(32)」の自己完結値です。
// 発行時にサーバーは何も保持しません。検証に成功したものだけを、期限まで使用済みとして記録します。
const (
	challengeBodyLen = 1 + 8 + 32
	challengeLen     = challengeBodyLen + sha256.Size
)

type challenges struct {
	key  []byte
	now  func() time.Time
	mu   sync.Mutex
	used map[string]time.Time
}

func newChallenges(now func() time.Time) *challenges {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("cms: generate challenge key: " + err.Error())
	}
	return &challenges{key: key, now: now, used: map[string]time.Time{}}
}

func (c *challenges) mac(body []byte) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write(body)
	return m.Sum(nil)
}

// issue は新しいchallengeを発行し、base64url（パディングなし）で返します。
func (c *challenges) issue(purpose challengePurpose) (string, error) {
	body := make([]byte, challengeBodyLen)
	body[0] = byte(purpose)
	binary.BigEndian.PutUint64(body[1:9], uint64(c.now().Add(challengeTTL).Unix()))
	if _, err := rand.Read(body[9:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(body, c.mac(body)...)), nil
}

// check は署名・用途・有効期限を確認し、challengeのバイト列を返します。使用済みかどうかは確認しません。
func (c *challenges) check(encoded string, purpose challengePurpose) ([]byte, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != challengeLen {
		return nil, ErrChallengeInvalid
	}
	body, sum := raw[:challengeBodyLen], raw[challengeBodyLen:]
	if !hmac.Equal(sum, c.mac(body)) || challengePurpose(body[0]) != purpose {
		return nil, ErrChallengeInvalid
	}
	if c.now().Unix() >= int64(binary.BigEndian.Uint64(body[1:9])) {
		return nil, ErrChallengeInvalid
	}
	return raw, nil
}

// consume は、未使用であることの確認と使用済みの記録を、ロック内で一体に行います。
// 同じchallengeの並行送信は1件だけ成功します。検証に成功した後にだけ呼びます。
func (c *challenges) consume(raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, exp := range c.used {
		if !now.Before(exp) {
			delete(c.used, k)
		}
	}
	key := string(raw)
	if _, ok := c.used[key]; ok {
		return ErrChallengeUsed
	}
	c.used[key] = time.Unix(int64(binary.BigEndian.Uint64(raw[1:9])), 0)
	return nil
}

// rpConfig は検証に使う、RP IDと許可するオリジンです。
type rpConfig struct {
	ID     string
	Origin string
}

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

// verifyClientData は clientDataJSON の種別・オリジンを確認し、challengeを検証してバイト列を返します。
func verifyClientData(rp rpConfig, ch *challenges, clientDataJSON []byte, wantType string, purpose challengePurpose) ([]byte, error) {
	var cd clientData
	if err := json.Unmarshal(clientDataJSON, &cd); err != nil || cd.Type != wantType {
		return nil, ErrClientDataInvalid
	}
	if cd.Origin != rp.Origin || cd.CrossOrigin {
		return nil, ErrOriginMismatch
	}
	return ch.check(cd.Challenge, purpose)
}

type authenticatorData struct {
	flags        byte
	signCount    uint32
	credentialID []byte // 登録時のみ（ATフラグ）
}

// parseAuthenticatorData は先頭37バイトと、登録時のcredential IDまでを読みます。
// credential IDより後ろ（COSE形式の公開鍵・拡張）は解析しません。
func parseAuthenticatorData(rp rpConfig, data []byte, requireAT bool) (authenticatorData, error) {
	if len(data) < 37 {
		return authenticatorData{}, ErrAuthDataInvalid
	}
	rpIDHash := sha256.Sum256([]byte(rp.ID))
	if !hmac.Equal(data[:32], rpIDHash[:]) {
		return authenticatorData{}, ErrRPIDMismatch
	}
	ad := authenticatorData{flags: data[32], signCount: binary.BigEndian.Uint32(data[33:37])}
	if ad.flags&flagUP == 0 || ad.flags&flagUV == 0 {
		return authenticatorData{}, ErrUserNotVerified
	}
	if ad.flags&(flagBE|flagBS) != 0 {
		return authenticatorData{}, ErrSyncedCredential
	}
	if requireAT {
		if ad.flags&flagAT == 0 || len(data) < 37+16+2 {
			return authenticatorData{}, ErrAuthDataInvalid
		}
		idLen := int(binary.BigEndian.Uint16(data[53:55]))
		if idLen == 0 || idLen > maxCredentialIDLen || len(data) < 55+idLen {
			return authenticatorData{}, ErrCredentialIDInvalid
		}
		ad.credentialID = data[55 : 55+idLen]
	}
	return ad, nil
}

// parsePublicKey は SPKI DER の公開鍵を読み、宣言されたアルゴリズムと鍵の型・強度が一致するか確認します。
func parsePublicKey(der []byte, alg int64) (crypto.PublicKey, error) {
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, ErrPublicKeyInvalid
	}
	switch alg {
	case algES256:
		if k, ok := key.(*ecdsa.PublicKey); ok && k.Curve == elliptic.P256() {
			return k, nil
		}
	case algEdDSA:
		if k, ok := key.(ed25519.PublicKey); ok && len(k) == ed25519.PublicKeySize {
			return k, nil
		}
	case algRS256:
		if k, ok := key.(*rsa.PublicKey); ok && k.N.BitLen() >= minRSABits {
			return k, nil
		}
	}
	return nil, ErrPublicKeyInvalid
}

// verifySignature は authenticatorData || SHA-256(clientDataJSON) への署名を検証します。
// ES256・RS256は対象のSHA-256に、Ed25519は対象そのもの（ハッシュしない）に署名されています。
func verifySignature(key crypto.PublicKey, alg int64, authData, clientDataJSON, sig []byte) error {
	cdHash := sha256.Sum256(clientDataJSON)
	signed := append(append([]byte{}, authData...), cdHash[:]...)
	ok := false
	switch alg {
	case algES256:
		digest := sha256.Sum256(signed)
		ok = ecdsa.VerifyASN1(key.(*ecdsa.PublicKey), digest[:], sig)
	case algRS256:
		digest := sha256.Sum256(signed)
		ok = rsa.VerifyPKCS1v15(key.(*rsa.PublicKey), crypto.SHA256, digest[:], sig) == nil
	case algEdDSA:
		ok = ed25519.Verify(key.(ed25519.PublicKey), signed, sig)
	}
	if !ok {
		return ErrSignatureInvalid
	}
	return nil
}

// registrationResponse はブラウザの登録結果です（JSでbase64urlから復元したバイト列）。
type registrationResponse struct {
	RawID              []byte
	ClientDataJSON     []byte
	AuthenticatorData  []byte // getAuthenticatorData()
	PublicKey          []byte // getPublicKey()（SPKI DER）
	PublicKeyAlgorithm int64  // getPublicKeyAlgorithm()
}

// newCredential は登録に成功したパスキーです。
type newCredential struct {
	ID        []byte
	PublicKey []byte
	Alg       int64
	SignCount uint32
}

// verifyRegistration は登録結果を検証します。成功した場合だけchallengeを使用済みにします。
// 登録トークンや管理者セッションによる認可は、呼び出し側で先に確認します。
func verifyRegistration(rp rpConfig, ch *challenges, r registrationResponse) (newCredential, error) {
	raw, err := verifyClientData(rp, ch, r.ClientDataJSON, "webauthn.create", purposeRegister)
	if err != nil {
		return newCredential{}, err
	}
	ad, err := parseAuthenticatorData(rp, r.AuthenticatorData, true)
	if err != nil {
		return newCredential{}, err
	}
	if len(r.RawID) == 0 || !bytes.Equal(ad.credentialID, r.RawID) {
		return newCredential{}, ErrCredentialIDInvalid
	}
	if _, err := parsePublicKey(r.PublicKey, r.PublicKeyAlgorithm); err != nil {
		return newCredential{}, err
	}
	if err := ch.consume(raw); err != nil {
		return newCredential{}, err
	}
	return newCredential{
		ID:        bytes.Clone(r.RawID),
		PublicKey: bytes.Clone(r.PublicKey),
		Alg:       r.PublicKeyAlgorithm,
		SignCount: ad.signCount,
	}, nil
}

// assertionResponse はブラウザの認証結果です。
type assertionResponse struct {
	RawID             []byte
	ClientDataJSON    []byte
	AuthenticatorData []byte
	Signature         []byte
	UserHandle        []byte
}

// storedCredential は登録済みのパスキーです。呼び出し側が RawID で検索し、無効化済みでないことを確認して渡します。
type storedCredential struct {
	ID        []byte
	PublicKey []byte
	Alg       int64
	SignCount uint32
}

// verifyAssertion は認証結果を検証し、新しい署名カウンタを返します。
// 署名の検証に成功した後にだけchallengeを使用済みにし、署名カウンタを比較します。
// カウンタが進んでいなければ ErrCloneSuspected を返します（呼び出し側はパスキーを無効化する）。
func verifyAssertion(rp rpConfig, ch *challenges, r assertionResponse, cred storedCredential, userID []byte) (uint32, error) {
	if !bytes.Equal(r.RawID, cred.ID) {
		return 0, ErrCredentialIDInvalid
	}
	if len(r.UserHandle) > 0 && !bytes.Equal(r.UserHandle, userID) {
		return 0, ErrUserHandleMismatch
	}
	raw, err := verifyClientData(rp, ch, r.ClientDataJSON, "webauthn.get", purposeLogin)
	if err != nil {
		return 0, err
	}
	ad, err := parseAuthenticatorData(rp, r.AuthenticatorData, false)
	if err != nil {
		return 0, err
	}
	key, err := parsePublicKey(cred.PublicKey, cred.Alg)
	if err != nil {
		return 0, err
	}
	if err := verifySignature(key, cred.Alg, r.AuthenticatorData, r.ClientDataJSON, r.Signature); err != nil {
		return 0, err
	}
	if err := ch.consume(raw); err != nil {
		return 0, err
	}
	// 新値と保存値のどちらかが0でなければ、新値は保存値より大きくなければならない
	if (ad.signCount != 0 || cred.SignCount != 0) && ad.signCount <= cred.SignCount {
		return 0, ErrCloneSuspected
	}
	return ad.signCount, nil
}
