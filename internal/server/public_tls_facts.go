package server

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

// publicTLSLeafFactsMaxFileBytes bounds how much of a configured certificate
// file is read when listing mappings. Real chains are a few KiB.
const publicTLSLeafFactsMaxFileBytes = 1 << 20

// publicTLSLeafFactsCacheMaxEntries bounds the per-path cache so a long-lived
// server with many deleted mappings cannot grow it without limit.
const publicTLSLeafFactsCacheMaxEntries = 4096

// publicTLSLeafFacts are the read-only, non-secret facts about the leaf
// certificate stored for a public TLS mapping.
type publicTLSLeafFacts struct {
	State        p2pstreamv1.PublicTlsCertificateLeafState
	Issuer       string
	DNSNames     []string
	IPAddresses  []string
	SHA256       string
	SerialNumber string
}

type publicTLSLeafFactsFileSignature struct {
	size    int64
	modTime time.Time
}

type publicTLSLeafFactsEntry struct {
	signature   publicTLSLeafFactsFileSignature
	contentHash [sha256.Size]byte
	facts       publicTLSLeafFacts
}

// publicTLSLeafFactsCache memoizes leaf facts per certificate path. A cheap
// stat signature (size and modification time) avoids rereading unchanged
// files on every config list call; when the signature changes the file is
// reread and only reparsed if its content hash changed.
type publicTLSLeafFactsCache struct {
	mu      sync.Mutex
	entries map[string]publicTLSLeafFactsEntry
}

var defaultPublicTLSLeafFactsCache = &publicTLSLeafFactsCache{}

func publicTLSLeafFactsForPath(certPath string) publicTLSLeafFacts {
	return defaultPublicTLSLeafFactsCache.lookup(certPath)
}

func (c *publicTLSLeafFactsCache) lookup(certPath string) publicTLSLeafFacts {
	certPath = strings.TrimSpace(certPath)
	if certPath == "" {
		return publicTLSLeafFacts{State: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_MISSING}
	}
	info, err := os.Stat(certPath)
	if err != nil {
		c.forget(certPath)
		if errors.Is(err, fs.ErrNotExist) {
			return publicTLSLeafFacts{State: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_MISSING}
		}
		return publicTLSLeafFacts{State: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_UNREADABLE}
	}
	// Never open FIFOs, devices or directories from a list call: they can
	// block or stream without end.
	if !info.Mode().IsRegular() || info.Size() > publicTLSLeafFactsMaxFileBytes {
		c.forget(certPath)
		return publicTLSLeafFacts{State: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_UNREADABLE}
	}
	signature := publicTLSLeafFactsFileSignature{size: info.Size(), modTime: info.ModTime()}

	c.mu.Lock()
	cached, ok := c.entries[certPath]
	c.mu.Unlock()
	if ok && cached.signature == signature {
		return cached.facts.clone()
	}

	content, err := readPublicTLSLeafFactsFile(certPath)
	if err != nil {
		c.forget(certPath)
		return publicTLSLeafFacts{State: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_UNREADABLE}
	}
	contentHash := sha256.Sum256(content)
	var facts publicTLSLeafFacts
	if ok && cached.contentHash == contentHash {
		facts = cached.facts
	} else {
		facts = publicTLSLeafFactsFromPEM(content)
	}

	c.mu.Lock()
	if c.entries == nil || len(c.entries) >= publicTLSLeafFactsCacheMaxEntries {
		c.entries = make(map[string]publicTLSLeafFactsEntry)
	}
	c.entries[certPath] = publicTLSLeafFactsEntry{signature: signature, contentHash: contentHash, facts: facts}
	c.mu.Unlock()
	return facts.clone()
}

func (c *publicTLSLeafFactsCache) forget(certPath string) {
	c.mu.Lock()
	delete(c.entries, certPath)
	c.mu.Unlock()
}

func readPublicTLSLeafFactsFile(certPath string) ([]byte, error) {
	file, err := os.Open(certPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, publicTLSLeafFactsMaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > publicTLSLeafFactsMaxFileBytes {
		return nil, errors.New("certificate file is too large")
	}
	return content, nil
}

// publicTLSLeafFactsFromPEM returns facts for the first CERTIFICATE block,
// matching the leaf tls.LoadX509KeyPair serves. Other block types (for
// example a private key in a combined file) are skipped and never exposed.
func publicTLSLeafFactsFromPEM(content []byte) publicTLSLeafFacts {
	rest := content
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			break
		}
		return publicTLSLeafFactsFromCertificate(leaf)
	}
	return publicTLSLeafFacts{State: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_INVALID}
}

func publicTLSLeafFactsFromCertificate(leaf *x509.Certificate) publicTLSLeafFacts {
	if leaf == nil {
		return publicTLSLeafFacts{State: p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_INVALID}
	}
	digest := sha256.Sum256(leaf.Raw)
	facts := publicTLSLeafFacts{
		State:    p2pstreamv1.PublicTlsCertificateLeafState_PUBLIC_TLS_CERTIFICATE_LEAF_STATE_AVAILABLE,
		Issuer:   leaf.Issuer.String(),
		DNSNames: append([]string(nil), leaf.DNSNames...),
		SHA256:   hex.EncodeToString(digest[:]),
	}
	for _, ip := range leaf.IPAddresses {
		facts.IPAddresses = append(facts.IPAddresses, ip.String())
	}
	if leaf.SerialNumber != nil {
		facts.SerialNumber = strings.ToUpper(leaf.SerialNumber.Text(16))
	}
	return facts
}

func (f publicTLSLeafFacts) clone() publicTLSLeafFacts {
	f.DNSNames = append([]string(nil), f.DNSNames...)
	f.IPAddresses = append([]string(nil), f.IPAddresses...)
	return f
}

func (f publicTLSLeafFacts) applyTo(cert *p2pstreamv1.PublicTlsCertificate) {
	if cert == nil {
		return
	}
	cert.LeafState = f.State
	cert.LeafIssuer = f.Issuer
	cert.LeafDnsNames = f.DNSNames
	cert.LeafIpAddresses = f.IPAddresses
	cert.LeafSha256 = f.SHA256
	cert.LeafSerialNumber = f.SerialNumber
}
