package api

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func TestValidPushEndpoint(t *testing.T) {
	good := []string{
		"https://fcm.googleapis.com/fcm/send/abc:APA91b",
		"https://updates.push.services.mozilla.com/wpush/v2/gAAAA",
		"https://wns2-par02p.notify.windows.com/w/?token=BQYAAA",
		"https://web.push.apple.com/QGuQyavXutnMH",
		"https://api.push.apple.com/3/device/abc",
		"https://FCM.googleapis.com:443/fcm/send/x",
	}
	bad := []string{
		"",
		"http://fcm.googleapis.com/fcm/send/x", // not https
		"https://127.0.0.1/",                   // internal address
		"https://169.254.169.254/latest/meta-data/",               // cloud metadata
		"https://localhost:8080/",                                 // loopback
		"https://fcm.googleapis.com.evil.com/x",                   // suffix trick
		"https://evilfcm.googleapis.com/x",                        // not the exact host
		"https://notify.windows.com.attacker.net/x",               // suffix trick
		"https://evilpush.services.mozilla.com/x",                 // needs the leading dot
		"https://user:pw@fcm.googleapis.com/x",                    // userinfo
		"https://fcm.googleapis.com:8443/x",                       // non-default port
		"https://postgres:5432/",                                  // internal service
		"file:///etc/passwd",                                      // other scheme
		"https://fcm.googleapis.com/" + strings.Repeat("a", 4096), // oversized
	}
	for _, e := range good {
		if !validPushEndpoint(e) {
			t.Errorf("validPushEndpoint(%q) = false, want true", e)
		}
	}
	for _, e := range bad {
		if validPushEndpoint(e) {
			t.Errorf("validPushEndpoint(%q) = true, want false", e)
		}
	}
}

func TestVAPIDSubscriber(t *testing.T) {
	for in, want := range map[string]string{
		"mailto:admin@example.com":   "admin@example.com",
		" MAILTO:admin@example.com ": "admin@example.com",
		"admin@example.com":          "admin@example.com",
		"https://example.com/abuse":  "https://example.com/abuse",
	} {
		if got := vapidSubscriber(in); got != want {
			t.Errorf("vapidSubscriber(%q) = %q, want %q", in, got, want)
		}
	}
}

// The VAPID JWT must carry "sub": "mailto:..." exactly once. webpush-go adds
// the scheme itself, and Apple refuses "mailto:mailto:...".
func TestVAPIDJWTSubjectHasSingleMailto(t *testing.T) {
	vapidPriv, vapidPub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	_, _ = rand.Read(authSecret)

	var authz string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		authz = r.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	resp, err := webpush.SendNotificationWithContext(context.Background(), []byte("{}"), &webpush.Subscription{
		Endpoint: "https://web.push.apple.com/abc",
		Keys: webpush.Keys{
			P256dh: base64.RawURLEncoding.EncodeToString(clientKey.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(authSecret),
		},
	}, &webpush.Options{
		HTTPClient:      client,
		Subscriber:      vapidSubscriber("mailto:admin@example.com"),
		VAPIDPublicKey:  vapidPub,
		VAPIDPrivateKey: vapidPriv,
		TTL:             30,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// "vapid t=<header>.<claims>.<signature>, k=<key>"
	token, _, _ := strings.Cut(strings.TrimPrefix(authz, "vapid t="), ",")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("Authorization %q carries no JWT", authz)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "mailto:admin@example.com" {
		t.Fatalf("JWT sub = %q, want mailto:admin@example.com", claims.Sub)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPushHTTPClientRefusesRedirectsAndHasTimeout(t *testing.T) {
	if pushHTTPClient.Timeout <= 0 {
		t.Fatal("push client has no timeout")
	}
	if pushHTTPClient.CheckRedirect == nil || pushHTTPClient.CheckRedirect(nil, nil) == nil {
		t.Fatal("push client would follow redirects")
	}
}
