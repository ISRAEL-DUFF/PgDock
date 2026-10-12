// Package ha runs the etcd cluster HA instances keep their Patroni state in
// (V3 §2.2): its own certificate authority, the members on three nodes,
// and their health.
package ha

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/store"
)

const (
	caSettingsKey = "etcd_ca"
	caKeyAAD      = "settings.etcd_ca.key"
)

// CA signs the etcd members' certificates and the client certificates of
// the Patroni members using them. It is separate from the agent CA, so an
// etcd certificate can't pass for an agent's, nor the reverse.
type CA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     *ecdsa.PrivateKey
}

type storedCA struct {
	CertPEM   string `json:"cert_pem"`
	KeySealed []byte `json:"key_sealed"`
}

// LoadCA loads the etcd CA, creating it on first use. Its key is sealed
// with the master key.
func LoadCA(ctx context.Context, db *pgxpool.Pool, keyring *crypto.Keyring) (*CA, error) {
	var ca *CA
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('pgdock_etcd_ca'))`); err != nil {
			return err
		}
		q := store.New(tx)
		raw, err := q.GetSetting(ctx, caSettingsKey)
		if err == nil {
			var s storedCA
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			der, err := keyring.Decrypt(s.KeySealed, []byte(caKeyAAD))
			if err != nil {
				return fmt.Errorf("etcd CA key: %w", err)
			}
			ca, err = parseCA([]byte(s.CertPEM), der)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if ca, err = newCA(); err != nil {
			return err
		}
		der, err := x509.MarshalPKCS8PrivateKey(ca.key)
		if err != nil {
			return err
		}
		sealed, err := keyring.Encrypt(der, []byte(caKeyAAD))
		if err != nil {
			return err
		}
		b, _ := json.Marshal(storedCA{CertPEM: string(ca.certPEM), KeySealed: sealed})
		return q.PutSetting(ctx, store.PutSettingParams{Key: caSettingsKey, Value: b})
	})
	return ca, err
}

func newCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "PGDock etcd CA", Organization: []string{"PGDock"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return parseCAKey(der, key)
}

func parseCA(certPEM, keyDER []byte) (*CA, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil {
		return nil, errors.New("etcd CA: bad certificate")
	}
	k, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, err
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("etcd CA: not an ECDSA key")
	}
	return parseCAKey(b.Bytes, key)
}

func parseCAKey(der []byte, key *ecdsa.PrivateKey) (*CA, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key: key}, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

// CertPEM is the CA certificate members and clients trust.
func (ca *CA) CertPEM() []byte { return ca.certPEM }

// Pool is a pool holding the CA.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// Pair is a certificate and its private key, PEM-encoded.
type Pair struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// Member issues an etcd member's certificate: it serves clients and peers
// on hosts, and authenticates to the other members.
func (ca *CA) Member(name string, hosts []string) (Pair, error) {
	return ca.issue("etcd:"+name, hosts, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, 5)
}

// Client issues a client certificate (a Patroni member, or pgdock-server).
func (ca *CA) Client(cn string) (Pair, error) {
	return ca.issue(cn, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, 5)
}

func (ca *CA) issue(cn string, hosts []string, usage []x509.ExtKeyUsage, years int) (Pair, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Pair{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"PGDock"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(years, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usage,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return Pair{}, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Pair{}, err
	}
	return Pair{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})),
	}, nil
}
