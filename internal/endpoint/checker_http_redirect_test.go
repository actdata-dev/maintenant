// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckHTTP_RedirectKeepsTargetCertificate(t *testing.T) {
	auth := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	auth.TLS = &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t, "auth.example.com")}}
	auth.StartTLS()
	defer auth.Close()

	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, auth.URL+"/login", http.StatusFound)
	}))
	defer app.Close()
	require.NotEqual(t, app.Certificate().Raw, auth.Certificate().Raw)

	ep := httpEndpoint(app.URL)
	ep.Config.TLSVerify = false

	result := CheckHTTP(context.Background(), ep, nil)

	require.True(t, result.Success)
	require.NotNil(t, result.HTTPStatus)
	assert.Equal(t, http.StatusOK, *result.HTTPStatus, "the redirect must have been followed")
	require.NotEmpty(t, result.TLSPeerCertificates)
	assert.Equal(t, app.Certificate().Raw, result.TLSPeerCertificates[0].Raw)
}

func selfSignedCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
