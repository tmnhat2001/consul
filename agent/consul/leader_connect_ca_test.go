package consul

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/rpc"
	"net/url"
	"testing"
	"time"

	msgpackrpc "github.com/hashicorp/net-rpc-msgpackrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hashicorp/consul/agent/connect"
	ca "github.com/hashicorp/consul/agent/connect/ca"
	"github.com/hashicorp/consul/agent/consul/fsm"
	"github.com/hashicorp/consul/agent/consul/state"
	"github.com/hashicorp/consul/agent/structs"
	"github.com/hashicorp/consul/agent/token"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/hashicorp/consul/sdk/testutil/retry"
	"github.com/hashicorp/consul/testrpc"
)

// TODO(kyhavlov): replace with t.Deadline()
const CATestTimeout = 7 * time.Second

func TestCAManager_Initialize_Vault_Secondary_SharedVault(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}
	ca.SkipIfVaultNotPresent(t)

	vault := ca.NewTestVaultServer(t)

	_, serverDC1 := testServerWithConfig(t, func(c *Config) {
		c.CAConfig = &structs.CAConfiguration{
			Provider: "vault",
			Config: map[string]interface{}{
				"Address":             vault.Addr,
				"Token":               vault.RootToken,
				"RootPKIPath":         "pki-root/",
				"IntermediatePKIPath": "pki-primary/",
			},
		}
	})

	runStep(t, "check primary DC", func(t *testing.T) {
		testrpc.WaitForTestAgent(t, serverDC1.RPC, "dc1")

		codec := rpcClient(t, serverDC1)
		roots := structs.IndexedCARoots{}
		err := msgpackrpc.CallWithCodec(codec, "ConnectCA.Roots", &structs.DCSpecificRequest{}, &roots)
		require.NoError(t, err)
		require.Len(t, roots.Roots, 1)

		leafPEM := getLeafCert(t, codec, roots.TrustDomain, "dc1")
		verifyLeafCert(t, roots.Roots[0], leafPEM)
	})

	runStep(t, "start secondary DC", func(t *testing.T) {
		_, serverDC2 := testServerWithConfig(t, func(c *Config) {
			c.Datacenter = "dc2"
			c.PrimaryDatacenter = "dc1"
			c.CAConfig = &structs.CAConfiguration{
				Provider: "vault",
				Config: map[string]interface{}{
					"Address":             vault.Addr,
					"Token":               vault.RootToken,
					"RootPKIPath":         "pki-root/",
					"IntermediatePKIPath": "pki-secondary/",
				},
			}
		})
		defer serverDC2.Shutdown()
		joinWAN(t, serverDC2, serverDC1)
		testrpc.WaitForActiveCARoot(t, serverDC2.RPC, "dc2", nil)

		codec := rpcClient(t, serverDC2)
		roots := structs.IndexedCARoots{}
		err := msgpackrpc.CallWithCodec(codec, "ConnectCA.Roots", &structs.DCSpecificRequest{}, &roots)
		require.NoError(t, err)
		require.Len(t, roots.Roots, 1)

		leafPEM := getLeafCert(t, codec, roots.TrustDomain, "dc2")
		verifyLeafCert(t, roots.Roots[0], leafPEM)
	})
}

func verifyLeafCert(t *testing.T, root *structs.CARoot, leafCertPEM string) {
	t.Helper()
	leaf, intermediates, err := connect.ParseLeafCerts(leafCertPEM)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	ok := pool.AppendCertsFromPEM([]byte(root.RootCert))
	if !ok {
		t.Fatalf("Failed to add root CA PEM to cert pool")
	}

	// verify with intermediates from leaf CertPEM
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err, "failed to verify using intermediates from leaf cert PEM")

	// verify with intermediates from the CARoot
	intermediates = x509.NewCertPool()
	for _, intermediate := range root.IntermediateCerts {
		c, err := connect.ParseCert(intermediate)
		require.NoError(t, err)
		intermediates.AddCert(c)
	}

	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err, "failed to verify using intermediates from CARoot list")
}

type mockCAServerDelegate struct {
	t                     *testing.T
	config                *Config
	store                 *state.Store
	primaryRoot           *structs.CARoot
	secondaryIntermediate string
	callbackCh            chan string
}

func NewMockCAServerDelegate(t *testing.T, config *Config) *mockCAServerDelegate {
	delegate := &mockCAServerDelegate{
		t:           t,
		config:      config,
		store:       state.NewStateStore(nil),
		primaryRoot: connect.TestCAWithTTL(t, nil, 1*time.Second),
		callbackCh:  make(chan string, 0),
	}
	delegate.store.CASetConfig(1, testCAConfig())

	return delegate
}

func (m *mockCAServerDelegate) State() *state.Store {
	return m.store
}

func (m *mockCAServerDelegate) ProviderState(id string) (*structs.CAConsulProviderState, error) {
	_, s, err := m.store.CAProviderState(id)
	return s, err
}

func (m *mockCAServerDelegate) IsLeader() bool {
	return true
}

func (m *mockCAServerDelegate) ServersSupportMultiDCConnectCA() error {
	return nil
}

func (m *mockCAServerDelegate) ApplyCALeafRequest() (uint64, error) {
	return 3, nil
}

// ApplyCARequest mirrors FSM.applyConnectCAOperation because that functionality
// is not exported.
func (m *mockCAServerDelegate) ApplyCARequest(req *structs.CARequest) (interface{}, error) {
	idx, _, err := m.store.CAConfig(nil)
	if err != nil {
		return nil, err
	}

	m.callbackCh <- fmt.Sprintf("raftApply/ConnectCA")

	result := fsm.ApplyConnectCAOperationFromRequest(m.store, req, idx+1)
	if err, ok := result.(error); ok && err != nil {
		return nil, err
	}
	return result, nil
}

func (m *mockCAServerDelegate) forwardDC(method, dc string, args interface{}, reply interface{}) error {
	switch method {
	case "ConnectCA.Roots":
		roots := reply.(*structs.IndexedCARoots)
		roots.TrustDomain = connect.TestClusterID
		roots.Roots = []*structs.CARoot{m.primaryRoot}
		roots.ActiveRootID = m.primaryRoot.ID
	case "ConnectCA.SignIntermediate":
		r := reply.(*string)
		*r = m.secondaryIntermediate
	default:
		return fmt.Errorf("received call to unsupported method %q", method)
	}

	m.callbackCh <- fmt.Sprintf("forwardDC/%s", method)

	return nil
}

func (m *mockCAServerDelegate) generateCASignRequest(csr string) *structs.CASignRequest {
	return &structs.CASignRequest{
		Datacenter: m.config.PrimaryDatacenter,
		CSR:        csr,
	}
}

// mockCAProvider mocks an empty provider implementation with a channel in order to coordinate
// waiting for certain methods to be called.
type mockCAProvider struct {
	callbackCh      chan string
	rootPEM         string
	intermediatePem string
}

func (m *mockCAProvider) Configure(cfg ca.ProviderConfig) error { return nil }
func (m *mockCAProvider) State() (map[string]string, error)     { return nil, nil }
func (m *mockCAProvider) GenerateRoot() (ca.RootResult, error) {
	return ca.RootResult{PEM: m.rootPEM}, nil
}
func (m *mockCAProvider) GenerateIntermediateCSR() (string, error) {
	m.callbackCh <- "provider/GenerateIntermediateCSR"
	return "", nil
}
func (m *mockCAProvider) SetIntermediate(intermediatePEM, rootPEM string) error {
	m.callbackCh <- "provider/SetIntermediate"
	return nil
}
func (m *mockCAProvider) ActiveIntermediate() (string, error) {
	if m.intermediatePem == "" {
		return m.rootPEM, nil
	}
	return m.intermediatePem, nil
}
func (m *mockCAProvider) GenerateIntermediate() (string, error)                     { return "", nil }
func (m *mockCAProvider) Sign(*x509.CertificateRequest) (string, error)             { return "", nil }
func (m *mockCAProvider) SignIntermediate(*x509.CertificateRequest) (string, error) { return "", nil }
func (m *mockCAProvider) CrossSignCA(*x509.Certificate) (string, error)             { return "", nil }
func (m *mockCAProvider) SupportsCrossSigning() (bool, error)                       { return false, nil }
func (m *mockCAProvider) Cleanup(_ bool, _ map[string]interface{}) error            { return nil }

func waitForCh(t *testing.T, ch chan string, expected string) {
	t.Helper()
	select {
	case op := <-ch:
		if op != expected {
			t.Fatalf("got unexpected op %q, wanted %q", op, expected)
		}
	case <-time.After(CATestTimeout):
		t.Fatalf("never got op %q", expected)
	}
}

func waitForEmptyCh(t *testing.T, ch chan string) {
	select {
	case op := <-ch:
		t.Fatalf("got unexpected op %q", op)
	case <-time.After(1 * time.Second):
	}
}

func testCAConfig() *structs.CAConfiguration {
	return &structs.CAConfiguration{
		ClusterID: connect.TestClusterID,
		Provider:  "mock",
		Config: map[string]interface{}{
			"LeafCertTTL":         "72h",
			"IntermediateCertTTL": "2160h",
		},
	}
}

// initTestManager initializes a CAManager with a mockCAServerDelegate, consuming
// the ops that come through the channels and returning when initialization has finished.
func initTestManager(t *testing.T, manager *CAManager, delegate *mockCAServerDelegate) {
	t.Helper()
	initCh := make(chan struct{})
	go func() {
		require.NoError(t, manager.Initialize())
		close(initCh)
	}()
	for i := 0; i < 5; i++ {
		select {
		case <-delegate.callbackCh:
		case <-time.After(CATestTimeout):
			t.Fatal("failed waiting for initialization events")
		}
	}
	select {
	case <-initCh:
	case <-time.After(CATestTimeout):
		t.Fatal("failed waiting for initialization")
	}
}

func TestCAManager_Initialize(t *testing.T) {
	conf := DefaultConfig()
	conf.ConnectEnabled = true
	conf.PrimaryDatacenter = "dc1"
	conf.Datacenter = "dc2"
	delegate := NewMockCAServerDelegate(t, conf)
	delegate.secondaryIntermediate = delegate.primaryRoot.RootCert
	manager := NewCAManager(delegate, nil, testutil.Logger(t), conf)

	manager.providerShim = &mockCAProvider{
		callbackCh: delegate.callbackCh,
		rootPEM:    delegate.primaryRoot.RootCert,
	}

	// Call Initialize and then confirm the RPCs and provider calls
	// happen in the expected order.
	require.Equal(t, caStateUninitialized, manager.state)
	errCh := make(chan error)
	go func() {
		err := manager.Initialize()
		assert.NoError(t, err)
		errCh <- err
	}()

	waitForCh(t, delegate.callbackCh, "forwardDC/ConnectCA.Roots")
	require.EqualValues(t, caStateInitializing, manager.state)
	waitForCh(t, delegate.callbackCh, "provider/GenerateIntermediateCSR")
	waitForCh(t, delegate.callbackCh, "forwardDC/ConnectCA.SignIntermediate")
	waitForCh(t, delegate.callbackCh, "provider/SetIntermediate")
	waitForCh(t, delegate.callbackCh, "raftApply/ConnectCA")
	waitForEmptyCh(t, delegate.callbackCh)

	// Make sure the Initialize call returned successfully.
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(CATestTimeout):
		t.Fatal("never got result from errCh")
	}

	require.Equal(t, caStateInitialized, manager.state)
}

func TestCAManager_UpdateConfigWhileRenewIntermediate(t *testing.T) {

	// No parallel execution because we change globals
	patchIntermediateCertRenewInterval(t)

	conf := DefaultConfig()
	conf.ConnectEnabled = true
	conf.PrimaryDatacenter = "dc1"
	conf.Datacenter = "dc2"
	delegate := NewMockCAServerDelegate(t, conf)
	delegate.secondaryIntermediate = delegate.primaryRoot.RootCert
	manager := NewCAManager(delegate, nil, testutil.Logger(t), conf)
	manager.providerShim = &mockCAProvider{
		callbackCh: delegate.callbackCh,
		rootPEM:    delegate.primaryRoot.RootCert,
	}
	initTestManager(t, manager, delegate)

	// Simulate Wait half the TTL for the cert to need renewing.
	manager.timeNow = func() time.Time {
		return time.Now().Add(500 * time.Millisecond)
	}

	// Call RenewIntermediate and then confirm the RPCs and provider calls
	// happen in the expected order.
	errCh := make(chan error)
	go func() {
		errCh <- manager.RenewIntermediate(context.TODO(), false)
	}()

	waitForCh(t, delegate.callbackCh, "provider/GenerateIntermediateCSR")

	// Call UpdateConfiguration while RenewIntermediate is still in-flight to
	// make sure we get an error about the state being occupied.
	go func() {
		require.EqualValues(t, caStateRenewIntermediate, manager.state)
		require.Error(t, errors.New("already in state"), manager.UpdateConfiguration(&structs.CARequest{}))
	}()

	waitForCh(t, delegate.callbackCh, "forwardDC/ConnectCA.SignIntermediate")
	waitForCh(t, delegate.callbackCh, "provider/SetIntermediate")
	waitForCh(t, delegate.callbackCh, "raftApply/ConnectCA")
	waitForEmptyCh(t, delegate.callbackCh)

	// Make sure the RenewIntermediate call returned successfully.
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(CATestTimeout):
		t.Fatal("never got result from errCh")
	}

	require.EqualValues(t, caStateInitialized, manager.state)
}

func TestCAManager_SignCertificate_WithExpiredCert(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	args := []struct {
		testName              string
		notBeforeRoot         time.Time
		notAfterRoot          time.Time
		notBeforeIntermediate time.Time
		notAfterIntermediate  time.Time
		isError               bool
		errorMsg              string
	}{
		{"intermediate valid", time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 2), time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 2), false, ""},
		{"intermediate expired", time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 2), time.Now().AddDate(-2, 0, 0), time.Now().AddDate(0, 0, -1), true, "intermediate expired: certificate expired, expiration date"},
		{"root expired", time.Now().AddDate(-2, 0, 0), time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 2), true, "root expired: certificate expired, expiration date"},
		// a cert that is not yet valid is ok, assume it will be valid soon enough
		{"intermediate in the future", time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 2), time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 2), false, ""},
		{"root in the future", time.Now().AddDate(0, 0, 1), time.Now().AddDate(0, 0, 2), time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, 2), false, ""},
	}

	caPrivKey, err := rsa.GenerateKey(rand.Reader, 4096)
	require.NoError(t, err, "failed to generate key")

	for _, arg := range args {
		t.Run(arg.testName, func(t *testing.T) {
			// No parallel execution because we change globals
			// Set the interval and drift buffer low for renewing the cert.
			origInterval := structs.IntermediateCertRenewInterval
			origDriftBuffer := ca.CertificateTimeDriftBuffer
			defer func() {
				structs.IntermediateCertRenewInterval = origInterval
				ca.CertificateTimeDriftBuffer = origDriftBuffer
			}()
			structs.IntermediateCertRenewInterval = time.Millisecond
			ca.CertificateTimeDriftBuffer = 0

			conf := DefaultConfig()
			conf.ConnectEnabled = true
			conf.PrimaryDatacenter = "dc1"
			conf.Datacenter = "dc2"

			rootPEM := generateCertPEM(t, caPrivKey, arg.notBeforeRoot, arg.notAfterRoot)
			intermediatePEM := generateCertPEM(t, caPrivKey, arg.notBeforeIntermediate, arg.notAfterIntermediate)

			delegate := NewMockCAServerDelegate(t, conf)
			delegate.primaryRoot.RootCert = rootPEM
			delegate.secondaryIntermediate = intermediatePEM
			manager := NewCAManager(delegate, nil, testutil.Logger(t), conf)

			manager.providerShim = &mockCAProvider{
				callbackCh:      delegate.callbackCh,
				rootPEM:         rootPEM,
				intermediatePem: intermediatePEM,
			}
			initTestManager(t, manager, delegate)

			// Simulate Wait half the TTL for the cert to need renewing.
			manager.timeNow = func() time.Time {
				return time.Now().UTC().Add(500 * time.Millisecond)
			}

			// Call RenewIntermediate and then confirm the RPCs and provider calls
			// happen in the expected order.

			_, err := manager.SignCertificate(&x509.CertificateRequest{}, &connect.SpiffeIDAgent{})
			if arg.isError {
				require.Error(t, err)
				require.Contains(t, err.Error(), arg.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func generateCertPEM(t *testing.T, caPrivKey *rsa.PrivateKey, notBefore time.Time, notAfter time.Time) string {
	t.Helper()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(2019),
		Subject: pkix.Name{
			Organization:  []string{"Company, INC."},
			Country:       []string{"US"},
			Province:      []string{""},
			Locality:      []string{"San Francisco"},
			StreetAddress: []string{"Golden Gate Bridge"},
			PostalCode:    []string{"94016"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{connect.SpiffeIDAgent{Host: "foo"}.URI()},
	}

	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPrivKey.PublicKey, caPrivKey)
	require.NoError(t, err, "failed to create cert")

	caPEM := new(bytes.Buffer)
	err = pem.Encode(caPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caBytes,
	})
	require.NoError(t, err, "failed to encode")
	return caPEM.String()
}

func TestCADelegateWithState_GenerateCASignRequest(t *testing.T) {
	s := Server{config: &Config{PrimaryDatacenter: "east"}, tokens: new(token.Store)}
	d := &caDelegateWithState{Server: &s}
	req := d.generateCASignRequest("A")
	require.Equal(t, "east", req.RequestDatacenter())
}

func TestCAManager_Initialize_Logging(t *testing.T) {
	if testing.Short() {
		t.Skip("too slow for testing.Short")
	}

	t.Parallel()
	_, conf1 := testServerConfig(t)

	// Setup dummy logger to catch output
	var buf bytes.Buffer
	logger := testutil.LoggerWithOutput(t, &buf)

	deps := newDefaultDeps(t, conf1)
	deps.Logger = logger

	s1, err := NewServer(conf1, deps)
	require.NoError(t, err)
	defer s1.Shutdown()
	testrpc.WaitForLeader(t, s1.RPC, "dc1")

	// Wait til CA root is setup
	retry.Run(t, func(r *retry.R) {
		var out structs.IndexedCARoots
		r.Check(s1.RPC("ConnectCA.Roots", structs.DCSpecificRequest{
			Datacenter: conf1.Datacenter,
		}, &out))
	})

	require.Contains(t, buf.String(), "consul CA provider configured")
}

func TestCAManager_UpdateConfiguration_Vault_Primary(t *testing.T) {
	ca.SkipIfVaultNotPresent(t)
	vault := ca.NewTestVaultServer(t)

	_, s1 := testServerWithConfig(t, func(c *Config) {
		c.PrimaryDatacenter = "dc1"
		c.CAConfig = &structs.CAConfiguration{
			Provider: "vault",
			Config: map[string]interface{}{
				"Address":             vault.Addr,
				"Token":               vault.RootToken,
				"RootPKIPath":         "pki-root/",
				"IntermediatePKIPath": "pki-intermediate/",
			},
		}
	})
	defer func() {
		s1.Shutdown()
		s1.leaderRoutineManager.Wait()
	}()

	testrpc.WaitForLeader(t, s1.RPC, "dc1")

	_, origRoot, err := s1.fsm.State().CARootActive(nil)
	require.NoError(t, err)
	require.Len(t, origRoot.IntermediateCerts, 1)

	cert, err := connect.ParseCert(s1.caManager.getLeafSigningCertFromRoot(origRoot))
	require.NoError(t, err)
	require.Equal(t, connect.HexString(cert.SubjectKeyId), origRoot.SigningKeyID)

	err = s1.caManager.UpdateConfiguration(&structs.CARequest{
		Config: &structs.CAConfiguration{
			Provider: "vault",
			Config: map[string]interface{}{
				"Address":             vault.Addr,
				"Token":               vault.RootToken,
				"RootPKIPath":         "pki-root-2/",
				"IntermediatePKIPath": "pki-intermediate-2/",
			},
		},
	})
	require.NoError(t, err)

	_, newRoot, err := s1.fsm.State().CARootActive(nil)
	require.NoError(t, err)
	require.Len(t, newRoot.IntermediateCerts, 2,
		"expected one cross-sign cert and one local leaf sign cert")
	require.NotEqual(t, origRoot.ID, newRoot.ID)

	cert, err = connect.ParseCert(s1.caManager.getLeafSigningCertFromRoot(newRoot))
	require.NoError(t, err)
	require.Equal(t, connect.HexString(cert.SubjectKeyId), newRoot.SigningKeyID)
}

func getLeafCert(t *testing.T, codec rpc.ClientCodec, trustDomain string, dc string) string {
	pk, _, err := connect.GeneratePrivateKey()
	require.NoError(t, err)
	spiffeID := &connect.SpiffeIDService{
		Host:       trustDomain,
		Service:    "srv1",
		Datacenter: dc,
	}
	csr, err := connect.CreateCSR(spiffeID, pk, nil, nil)
	require.NoError(t, err)

	req := structs.CASignRequest{CSR: csr}
	cert := structs.IssuedCert{}
	err = msgpackrpc.CallWithCodec(codec, "ConnectCA.Sign", &req, &cert)
	require.NoError(t, err)

	return cert.CertPEM
}
