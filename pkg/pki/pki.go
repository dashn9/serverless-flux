package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// PKI manages a local certificate authority and SSH key pair for Flux.
// All material is persisted under CertsDir:
//
//	<certs_dir>/
//	  ca/
//	    ca.pem            # CA certificate
//	    ca-key.pem        # CA private key (never leaves Flux host)
//	  flux/
//	    flux.pem          # Flux client certificate
//	    flux-key.pem      # Flux private key
//	  ssh/
//	    flux-agent.pem    # SSH private key (shared across all agents)
//	    flux-agent.pub    # SSH public key (imported to cloud providers)
//	  agents/
//	    <agent-id>/
//	      agent.pem       # Per-agent server certificate
//	      agent-key.pem   # Per-agent private key
type PKI struct {
	certsDir     string
	caCert       *x509.Certificate
	caKey        *ecdsa.PrivateKey
	sshPublicKey []byte // authorized_keys format
}

// New initialises the PKI rooted at certsDir. On the first call it generates
// a CA, a Flux certificate, and an SSH key pair. On subsequent calls it loads
// existing material from disk.
func New(certsDir string) (*PKI, error) {
	abs, err := filepath.Abs(certsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve certs_dir: %w", err)
	}

	p := &PKI{certsDir: abs}

	if err := p.initCA(); err != nil {
		return nil, err
	}

	if err := p.initFluxCert(); err != nil {
		return nil, err
	}

	if err := p.initSSHKey(); err != nil {
		return nil, err
	}

	return p, nil
}

// MintAgentCert generates a new TLS certificate for the given agent, writes
// it to disk under agents/<agentID>/, and returns PEM-encoded cert, key, and
// CA cert bytes for direct upload during bootstrap.
func (p *PKI) MintAgentCert(agentID string) (certPEM, keyPEM, caPEM []byte, err error) {
	agentDir := filepath.Join(p.certsDir, "agents", agentID)
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		return nil, nil, nil, fmt.Errorf("mkdir %s: %w", agentDir, err)
	}

	certPEM, keyPEM, err = p.generateLeaf("flux-agent", []string{"flux-agent"}, 365*24*time.Hour)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate agent cert: %w", err)
	}

	if err := writeFile(filepath.Join(agentDir, "agent.pem"), certPEM, 0644); err != nil {
		return nil, nil, nil, err
	}
	if err := writeFile(filepath.Join(agentDir, "agent-key.pem"), keyPEM, 0600); err != nil {
		return nil, nil, nil, err
	}

	caPEM = pemEncodeCert(p.caCert.Raw)
	return certPEM, keyPEM, caPEM, nil
}

// CACertPath returns the absolute path to the CA certificate.
func (p *PKI) CACertPath() string {
	return filepath.Join(p.certsDir, "ca", "ca.pem")
}

// FluxCertPath returns the absolute path to the Flux certificate.
func (p *PKI) FluxCertPath() string {
	return filepath.Join(p.certsDir, "flux", "flux.pem")
}

// FluxKeyPath returns the absolute path to the Flux private key.
func (p *PKI) FluxKeyPath() string {
	return filepath.Join(p.certsDir, "flux", "flux-key.pem")
}

// SSHPrivateKeyPath returns the absolute path to the shared SSH private key.
func (p *PKI) SSHPrivateKeyPath() string {
	return filepath.Join(p.certsDir, "ssh", "flux-agent.pem")
}

// SSHPublicKey returns the SSH public key in authorized_keys format.
func (p *PKI) SSHPublicKey() []byte {
	return p.sshPublicKey
}

// --- CA ---

func (p *PKI) initCA() error {
	caDir := filepath.Join(p.certsDir, "ca")
	certPath := filepath.Join(caDir, "ca.pem")
	keyPath := filepath.Join(caDir, "ca-key.pem")

	if fileExists(certPath) && fileExists(keyPath) {
		var err error
		p.caCert, p.caKey, err = loadCA(certPath, keyPath)
		if err != nil {
			return fmt.Errorf("load existing CA: %w", err)
		}
		return nil
	}

	if err := os.MkdirAll(caDir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", caDir, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, err := randomSerial()
	if err != nil {
		return err
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "flux-ca"},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}

	if err := writeFile(certPath, pemEncodeCert(der), 0644); err != nil {
		return err
	}
	if err := writeECKey(keyPath, key); err != nil {
		return err
	}

	p.caCert = cert
	p.caKey = key
	return nil
}

// --- Flux cert ---

func (p *PKI) initFluxCert() error {
	fluxDir := filepath.Join(p.certsDir, "flux")
	certPath := filepath.Join(fluxDir, "flux.pem")
	keyPath := filepath.Join(fluxDir, "flux-key.pem")

	if fileExists(certPath) && fileExists(keyPath) {
		return nil
	}

	if err := os.MkdirAll(fluxDir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", fluxDir, err)
	}

	certPEM, keyPEM, err := p.generateLeaf("flux", []string{"flux"}, 5*365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("generate flux cert: %w", err)
	}

	if err := writeFile(certPath, certPEM, 0644); err != nil {
		return err
	}
	return writeFile(keyPath, keyPEM, 0600)
}

// --- SSH key ---

func (p *PKI) initSSHKey() error {
	sshDir := filepath.Join(p.certsDir, "ssh")
	privPath := filepath.Join(sshDir, "flux-agent.pem")
	pubPath := filepath.Join(sshDir, "flux-agent.pub")

	if fileExists(privPath) && fileExists(pubPath) {
		pubData, err := os.ReadFile(pubPath)
		if err != nil {
			return fmt.Errorf("read SSH public key: %w", err)
		}
		p.sshPublicKey = pubData
		return nil
	}

	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", sshDir, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	// Write private key in OpenSSH format (PEM-encoded PKCS8 wrapped in openssh).
	// Use SEC1 EC format which is widely compatible.
	if err := writeECKey(privPath, key); err != nil {
		return err
	}

	// Write public key in authorized_keys format.
	sshPub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("convert to SSH public key: %w", err)
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	if err := writeFile(pubPath, pubBytes, 0644); err != nil {
		return err
	}

	p.sshPublicKey = pubBytes
	return nil
}

// --- leaf cert generation ---

func (p *PKI) generateLeaf(cn string, dnsNames []string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pemEncodeCert(der)

	ecBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecBytes})

	return certPEM, keyPEM, nil
}

// --- helpers ---

func loadCA(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(certData)
	if block == nil {
		return nil, nil, fmt.Errorf("no PEM block in %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	keyBlock, _ := pem.Decode(keyData)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("no PEM block in %s", keyPath)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

func writeECKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	return writeFile(path, data, 0600)
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func pemEncodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
