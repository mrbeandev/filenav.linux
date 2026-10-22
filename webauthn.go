package main

// Minimal, dependency-free WebAuthn (passkeys) implementation.
// Supports ES256 (P-256), RS256 and EdDSA (Ed25519) credentials.
// Attestation statements are not verified (attestation "none" is requested),
// which is the normal choice for passkey logins.

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ---------- tiny CBOR decoder ----------

type cborDec struct {
	b []byte
	p int
}

func (d *cborDec) head() (major byte, val uint64, err error) {
	if d.p >= len(d.b) {
		return 0, 0, errors.New("cbor: eof")
	}
	ib := d.b[d.p]
	d.p++
	major = ib >> 5
	ai := ib & 0x1f
	switch {
	case ai < 24:
		val = uint64(ai)
	case ai <= 27:
		n := 1 << (ai - 24)
		if d.p+n > len(d.b) {
			return 0, 0, errors.New("cbor: eof")
		}
		for i := 0; i < n; i++ {
			val = val<<8 | uint64(d.b[d.p+i])
		}
		d.p += n
	default:
		return 0, 0, errors.New("cbor: indefinite lengths not supported")
	}
	return
}

func (d *cborDec) value(depth int) (any, error) {
	if depth > 16 {
		return nil, errors.New("cbor: too deep")
	}
	major, val, err := d.head()
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		return int64(val), nil
	case 1:
		return -1 - int64(val), nil
	case 2, 3:
		if val > uint64(len(d.b)-d.p) {
			return nil, errors.New("cbor: eof")
		}
		s := d.b[d.p : d.p+int(val)]
		d.p += int(val)
		if major == 3 {
			return string(s), nil
		}
		return s, nil
	case 4:
		if val > 1024 {
			return nil, errors.New("cbor: array too large")
		}
		arr := make([]any, 0, val)
		for i := uint64(0); i < val; i++ {
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		return arr, nil
	case 5:
		if val > 1024 {
			return nil, errors.New("cbor: map too large")
		}
		m := make(map[any]any, val)
		for i := uint64(0); i < val; i++ {
			k, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, errors.New("cbor: unsupported map key")
			}
			m[k] = v
		}
		return m, nil
	case 6: // tag: skip, return inner
		return d.value(depth + 1)
	case 7:
		switch val {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22, 23:
			return nil, nil
		}
		return nil, nil // floats not needed here
	}
	return nil, errors.New("cbor: bad major type")
}

func cborDecode(b []byte) (any, int, error) {
	d := &cborDec{b: b}
	v, err := d.value(0)
	return v, d.p, err
}

// ---------- COSE keys ----------

func parseCOSEKey(b []byte) (crypto.PublicKey, int, error) {
	v, _, err := cborDecode(b)
	if err != nil {
		return nil, 0, err
	}
	m, ok := v.(map[any]any)
	if !ok {
		return nil, 0, errors.New("cose: not a map")
	}
	i := func(k int64) int64 { x, _ := m[k].(int64); return x }
	bs := func(k int64) []byte { x, _ := m[k].([]byte); return x }
	kty, alg := i(1), i(3)
	switch {
	case kty == 2 && (alg == -7 || alg == 0): // EC2 / ES256
		if i(-1) != 1 {
			return nil, 0, errors.New("cose: unsupported curve")
		}
		x, y := bs(-2), bs(-3)
		if len(x) != 32 || len(y) != 32 {
			return nil, 0, errors.New("cose: bad EC point")
		}
		pk := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pk.Curve.IsOnCurve(pk.X, pk.Y) { //nolint:staticcheck // validation only
			return nil, 0, errors.New("cose: point not on curve")
		}
		return pk, -7, nil
	case kty == 3 && (alg == -257 || alg == 0): // RSA / RS256
		n, e := bs(-1), bs(-2)
		if len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return nil, 0, errors.New("cose: bad RSA key")
		}
		var ei int
		for _, c := range e {
			ei = ei<<8 | int(c)
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: ei}, -257, nil
	case kty == 1 && (alg == -8 || alg == 0): // OKP / EdDSA
		if i(-1) != 6 {
			return nil, 0, errors.New("cose: unsupported OKP curve")
		}
		x := bs(-2)
		if len(x) != ed25519.PublicKeySize {
			return nil, 0, errors.New("cose: bad Ed25519 key")
		}
		return ed25519.PublicKey(x), -8, nil
	}
	return nil, 0, fmt.Errorf("cose: unsupported key type %d alg %d", kty, alg)
}

func verifySig(pub crypto.PublicKey, data, sig []byte) bool {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		h := sha256.Sum256(data)
		return ecdsa.VerifyASN1(k, h[:], sig)
	case *rsa.PublicKey:
		h := sha256.Sum256(data)
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, h[:], sig) == nil
	case ed25519.PublicKey:
		return ed25519.Verify(k, data, sig)
	}
	return false
}

// ---------- authenticator data ----------

type authData struct {
	RPIDHash  []byte
	Flags     byte
	SignCount uint32
	CredID    []byte
	PubKey    []byte // raw COSE bytes
}

const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
)

func parseAuthData(b []byte) (*authData, error) {
	if len(b) < 37 {
		return nil, errors.New("authData too short")
	}
	ad := &authData{RPIDHash: b[:32], Flags: b[32], SignCount: binary.BigEndian.Uint32(b[33:37])}
	if ad.Flags&flagAT != 0 {
		rest := b[37:]
		if len(rest) < 18 {
			return nil, errors.New("attested data too short")
		}
		l := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]
		if l == 0 || l > 1023 || len(rest) < l {
			return nil, errors.New("bad credential id")
		}
		ad.CredID = rest[:l]
		rest = rest[l:]
		_, n, err := cborDecode(rest)
		if err != nil {
			return nil, fmt.Errorf("credential public key: %w", err)
		}
		ad.PubKey = rest[:n]
	}
	return ad, nil
}

// ---------- challenges ----------

type challengeStore struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newChallengeStore() *challengeStore { return &challengeStore{m: map[string]time.Time{}} }

func (c *challengeStore) New() string {
	ch := randomToken(32)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 1000 {
		c.gcLocked()
	}
	c.m[ch] = time.Now().Add(5 * time.Minute)
	return ch
}

// Take consumes a challenge, returning true if it existed and was fresh.
func (c *challengeStore) Take(ch string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.m[ch]
	delete(c.m, ch)
	return ok && time.Now().Before(exp)
}

func (c *challengeStore) GC() { c.mu.Lock(); c.gcLocked(); c.mu.Unlock() }

func (c *challengeStore) gcLocked() {
	now := time.Now()
	for k, v := range c.m {
		if now.After(v) {
			delete(c.m, k)
		}
	}
}

// ---------- helpers ----------

var b64u = base64.RawURLEncoding

func b64dec(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return b64u.DecodeString(s)
}

func (s *Server) rpID(r *http.Request) string {
	u, err := url.Parse(s.origin(r))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

func (s *Server) checkClientData(r *http.Request, raw []byte, typ string) error {
	var cd clientData
	if err := json.Unmarshal(raw, &cd); err != nil {
		return errors.New("bad clientDataJSON")
	}
	if cd.Type != typ {
		return errors.New("wrong ceremony type")
	}
	if !s.challenges.Take(cd.Challenge) {
		return errors.New("challenge expired or unknown — try again")
	}
	if cd.Origin != s.origin(r) {
		return fmt.Errorf("origin mismatch (%q vs %q); set FILENAV_ORIGIN", cd.Origin, s.origin(r))
	}
	return nil
}

func (s *Server) checkRPHash(r *http.Request, h []byte) bool {
	want := sha256.Sum256([]byte(s.rpID(r)))
	return bytes.Equal(h, want[:])
}

// ---------- handlers ----------

// GET /api/passkey/register/begin  (authenticated)
func (s *Server) handlePasskeyRegBegin(w http.ResponseWriter, r *http.Request) {
	var handle []byte
	var exclude []map[string]any
	s.st.View(func(st *State) {
		handle = st.UserHandle
		for _, pk := range st.Passkeys {
			exclude = append(exclude, map[string]any{"type": "public-key", "id": b64u.EncodeToString(pk.ID)})
		}
	})
	writeJSON(w, map[string]any{
		"challenge": s.challenges.New(),
		"rp":        map[string]any{"id": s.rpID(r), "name": s.cfg.Title},
		"user":      map[string]any{"id": b64u.EncodeToString(handle), "name": "admin", "displayName": s.cfg.Title + " admin"},
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": -7}, {"type": "public-key", "alg": -8}, {"type": "public-key", "alg": -257},
		},
		"timeout":            120000,
		"attestation":        "none",
		"excludeCredentials": exclude,
		"authenticatorSelection": map[string]any{
			"residentKey": "required", "requireResidentKey": true, "userVerification": "preferred",
		},
	})
}

// POST /api/passkey/register/finish  (authenticated)
func (s *Server) handlePasskeyRegFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name              string `json:"name"`
		ClientDataJSON    string `json:"clientDataJSON"`
		AttestationObject string `json:"attestationObject"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	cdj, err1 := b64dec(req.ClientDataJSON)
	att, err2 := b64dec(req.AttestationObject)
	if err1 != nil || err2 != nil {
		jsonErr(w, 400, "bad encoding")
		return
	}
	if err := s.checkClientData(r, cdj, "webauthn.create"); err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	v, _, err := cborDecode(att)
	m, ok := v.(map[any]any)
	if err != nil || !ok {
		jsonErr(w, 400, "bad attestationObject")
		return
	}
	adRaw, _ := m["authData"].([]byte)
	ad, err := parseAuthData(adRaw)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	if !s.checkRPHash(r, ad.RPIDHash) {
		jsonErr(w, 400, "rpId mismatch")
		return
	}
	if ad.Flags&flagUP == 0 || ad.CredID == nil {
		jsonErr(w, 400, "missing user presence or credential")
		return
	}
	_, alg, err := parseCOSEKey(ad.PubKey)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	now := time.Now()
	err = s.st.Update(func(st *State) error {
		for _, pk := range st.Passkeys {
			if bytes.Equal(pk.ID, ad.CredID) {
				return errors.New("passkey already registered")
			}
		}
		st.Passkeys = append(st.Passkeys, &Passkey{
			ID: append([]byte(nil), ad.CredID...), Name: name,
			PublicKey: append([]byte(nil), ad.PubKey...), Alg: alg,
			SignCount: ad.SignCount, Created: now,
		})
		return nil
	})
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// GET /api/passkey/login/begin  (public)
func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"challenge":        s.challenges.New(),
		"rpId":             s.rpID(r),
		"timeout":          120000,
		"userVerification": "preferred",
		"allowCredentials": []any{}, // discoverable credentials
	})
}

// POST /api/passkey/login/finish  (public)
func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		jsonErr(w, http.StatusForbidden, "missing CSRF header")
		return
	}
	ip := s.clientIP(r)
	if s.loginLimit.Blocked(ip) {
		jsonErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var req struct {
		ID                string `json:"id"`
		ClientDataJSON    string `json:"clientDataJSON"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	fail := func(msg string) {
		s.loginLimit.Fail(ip)
		jsonErr(w, http.StatusUnauthorized, msg)
	}
	id, e1 := b64dec(req.ID)
	cdj, e2 := b64dec(req.ClientDataJSON)
	adRaw, e3 := b64dec(req.AuthenticatorData)
	sig, e4 := b64dec(req.Signature)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		fail("bad encoding")
		return
	}
	if err := s.checkClientData(r, cdj, "webauthn.get"); err != nil {
		fail(err.Error())
		return
	}
	ad, err := parseAuthData(adRaw)
	if err != nil {
		fail(err.Error())
		return
	}
	if !s.checkRPHash(r, ad.RPIDHash) || ad.Flags&flagUP == 0 {
		fail("rpId mismatch or no user presence")
		return
	}
	var cred Passkey
	found := false
	s.st.View(func(st *State) {
		for _, pk := range st.Passkeys {
			if bytes.Equal(pk.ID, id) {
				cred, found = *pk, true
			}
		}
	})
	if !found {
		fail("unknown passkey")
		return
	}
	pub, _, err := parseCOSEKey(cred.PublicKey)
	if err != nil {
		fail(err.Error())
		return
	}
	cdh := sha256.Sum256(cdj)
	signed := append(append([]byte{}, adRaw...), cdh[:]...)
	if !verifySig(pub, signed, sig) {
		fail("bad signature")
		return
	}
	if ad.SignCount != 0 && cred.SignCount != 0 && ad.SignCount <= cred.SignCount {
		fail("signature counter went backwards (cloned authenticator?)")
		return
	}
	s.loginLimit.Reset(ip)
	_ = s.st.Update(func(st *State) error {
		for _, pk := range st.Passkeys {
			if bytes.Equal(pk.ID, id) {
				pk.SignCount = ad.SignCount
				pk.LastUsed = time.Now()
			}
		}
		return nil
	})
	if err := s.newSession(w, r, "passkey"); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// GET /api/passkeys (authenticated)
func (s *Server) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	type out struct {
		ID       string    `json:"id"`
		Name     string    `json:"name"`
		Created  time.Time `json:"created"`
		LastUsed time.Time `json:"last_used"`
	}
	list := []out{}
	s.st.View(func(st *State) {
		for _, pk := range st.Passkeys {
			list = append(list, out{b64u.EncodeToString(pk.ID), pk.Name, pk.Created, pk.LastUsed})
		}
	})
	writeJSON(w, map[string]any{"passkeys": list})
}

// DELETE /api/passkeys/{id} (authenticated)
func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	id, err := b64dec(r.PathValue("id"))
	if err != nil {
		jsonErr(w, 400, "bad id")
		return
	}
	_ = s.st.Update(func(st *State) error {
		out := st.Passkeys[:0]
		for _, pk := range st.Passkeys {
			if !bytes.Equal(pk.ID, id) {
				out = append(out, pk)
			}
		}
		st.Passkeys = out
		if len(out) == 0 {
			st.PasskeyOnly = false // never lock the owner out
		}
		return nil
	})
	writeJSON(w, map[string]any{"ok": true})
}
