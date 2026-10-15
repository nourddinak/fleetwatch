package api

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// The exact body a browser sends: PushSubscription.toJSON(), including expirationTime.
func TestPushSubscribeBrowserShape(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)

	k, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	var body map[string]any
	json.Unmarshal([]byte(`{"endpoint":"https://fcm.googleapis.com/fcm/send/abc:def","expirationTime":null,"keys":{"p256dh":"`+
		base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())+`","auth":"`+base64.RawURLEncoding.EncodeToString(auth)+`"}}`), &body)

	if st, out := c.do("POST", "/api/push/subscribe", body); st != 200 {
		t.Fatalf("browser subscription rejected: %d %v", st, out)
	}
	if devs := c.list("/api/push/devices"); len(devs) != 1 {
		t.Fatalf("devices: %v", devs)
	}
	if st, out := c.do("GET", "/api/push/key", nil); st != 200 || out["publicKey"] == "" {
		t.Fatalf("key: %d %v", st, out)
	}
}
